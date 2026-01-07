"""Database factory for creating interchangeable database instances."""

import logging
from typing import Any, Optional

from .base import DatabaseBase
from .sqlite import SQLiteDatabase
from .postgres import PostgreSQLDatabase

logger = logging.getLogger("yallmp-proxy")

# Global database instance
_database_instance: Optional[DatabaseBase] = None


def get_database(config: Optional[dict[str, Any]] = None) -> DatabaseBase:
    """Get the database instance based on configuration.

    Args:
        config: Database configuration dictionary. If None, uses default SQLite configuration.

    Returns:
        A database instance (SQLite or PostgreSQL).

    Raises:
        ValueError: If an unsupported database backend is specified.
    """
    global _database_instance

    if _database_instance is not None:
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
        _database_instance = SQLiteDatabase(config)
    elif backend == "postgres" or backend == "postgresql":
        _database_instance = PostgreSQLDatabase(config)
    else:
        raise ValueError(f"Unsupported database backend: {backend}. Supported backends: sqlite, postgres")

    logger.info(f"Database factory created {backend} database instance")
    return _database_instance


def set_database_instance(instance: DatabaseBase) -> None:
    """Set a custom database instance.

    Args:
        instance: A database instance to use.
    """
    global _database_instance
    _database_instance = instance
    logger.info(f"Custom database instance set: {instance.backend_name}")


def reset_database_instance() -> None:
    """Reset the global database instance.

    This is mainly useful for testing.
    """
    global _database_instance
    if _database_instance is not None:
        _database_instance.close()
    _database_instance = None
    logger.debug("Database instance reset")


def get_current_backend() -> Optional[str]:
    """Get the current database backend name.

    Returns:
        The backend name (e.g., 'sqlite', 'postgres') or None if not initialized.
    """
    if _database_instance is None:
        return None
    return _database_instance.backend_name
