"""Database factory for creating interchangeable database instances."""

import logging
from typing import Any, Optional

from .base import DatabaseBase
from .sqlite import SQLiteDatabase
from .postgres import PostgreSQLDatabase

logger = logging.getLogger("yallmp-proxy")

# Global database instances (keyed for multi-DB support)
_database_instance: Optional[DatabaseBase] = None
_database_instances: dict[str, DatabaseBase] = {}


def get_database(
    config: Optional[dict[str, Any]] = None,
    instance_key: str = "default",
) -> DatabaseBase:
    """Get the database instance based on configuration.

    Args:
        config: Database configuration dictionary. If None, uses default SQLite configuration.

    Returns:
        A database instance (SQLite or PostgreSQL).

    Raises:
        ValueError: If an unsupported database backend is specified.
    """
    global _database_instance
    global _database_instances

    key = instance_key or "default"

    if key in _database_instances:
        return _database_instances[key]
    if key == "default" and _database_instance is not None:
        _database_instances[key] = _database_instance
        return _database_instance

    if config is None:
        # Default to SQLite
        config = {
            "backend": "sqlite",
            "connection": {
                "sqlite": {"path": "logs/yaLLM.db"}
            },
            "pool_size": 5,
            "max_overflow": 10
        }

    backend = config.get("backend", "sqlite").lower()

    if backend == "sqlite":
        instance = SQLiteDatabase(config)
    elif backend == "postgres" or backend == "postgresql":
        instance = PostgreSQLDatabase(config)
    else:
        raise ValueError(f"Unsupported database backend: {backend}. Supported backends: sqlite, postgres")

    _database_instances[key] = instance
    if key == "default":
        _database_instance = instance
    logger.info(f"Database factory created {backend} database instance (key={key})")
    return instance


def set_database_instance(instance: DatabaseBase, instance_key: str = "default") -> None:
    """Set a custom database instance.

    Args:
        instance: A database instance to use.
    """
    global _database_instance
    global _database_instances
    key = instance_key or "default"
    _database_instances[key] = instance
    if key == "default":
        _database_instance = instance
    logger.info(f"Custom database instance set: {instance.backend_name} (key={key})")


def reset_database_instance(instance_key: Optional[str] = None) -> None:
    """Reset the global database instance.

    This is mainly useful for testing.
    """
    global _database_instance
    global _database_instances

    if instance_key is None:
        for instance in _database_instances.values():
            instance.close()
        _database_instances.clear()
        _database_instance = None
        logger.debug("All database instances reset")
        return

    key = instance_key or "default"
    instance = _database_instances.pop(key, None)
    if instance is not None:
        instance.close()
    if key == "default":
        _database_instance = None
    logger.debug("Database instance reset: %s", key)


def get_current_backend(instance_key: str = "default") -> Optional[str]:
    """Get the current database backend name.

    Returns:
        The backend name (e.g., 'sqlite', 'postgres') or None if not initialized.
    """
    key = instance_key or "default"
    instance = _database_instances.get(key) or (_database_instance if key == "default" else None)
    if instance is None:
        return None
    return instance.backend_name
