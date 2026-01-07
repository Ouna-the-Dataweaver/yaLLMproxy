"""SQLite database implementation."""

import logging
from pathlib import Path
from typing import Any

from sqlalchemy.pool import NullPool, StaticPool

from .base import DatabaseBase

logger = logging.getLogger("yallmp-proxy")


class SQLiteDatabase(DatabaseBase):
    """SQLite database implementation."""

    @property
    def backend_name(self) -> str:
        """Return the name of the database backend."""
        return "sqlite"

    def get_connection_string(self) -> str:
        """Return the SQLite connection string.

        Returns:
            A SQLite connection string for SQLAlchemy.
        """
        sqlite_config = self.config.get("connection", {}).get("sqlite", {})
        db_path = sqlite_config.get("path", "logs/yaLLM.db")

        # Handle in-memory databases specially
        if db_path == ":memory:":
            logger.debug("SQLite database: in-memory")
            return "sqlite:///:memory:"

        # Convert to absolute path relative to project root
        if not Path(db_path).is_absolute():
            project_root = Path(__file__).parent.parent.parent
            db_path = project_root / db_path

        # Ensure parent directory exists
        db_path = Path(db_path)
        db_path.parent.mkdir(parents=True, exist_ok=True)

        logger.debug(f"SQLite database path: {db_path}")
        return f"sqlite:///{db_path}"

    def get_pool_options(self) -> dict[str, Any]:
        """Get connection pool options for SQLite.

        Returns:
            Dictionary of options to pass to create_engine.
        """
        sqlite_config = self.config.get("connection", {}).get("sqlite", {})
        db_path = sqlite_config.get("path", "logs/yaLLM.db")

        # In-memory SQLite needs StaticPool to share the database across connections
        if db_path == ":memory:":
            return {
                "poolclass": StaticPool,
                "connect_args": {"check_same_thread": False},
            }

        # File-based SQLite uses NullPool to avoid locking issues
        return {
            "poolclass": NullPool,
        }

    def initialize(self) -> None:
        """Initialize the SQLite database.

        Creates the database file and all tables if they don't exist.
        """
        # Check if already initialized to avoid duplicate logging
        if self._engine is not None:
            return

        super().initialize()
        logger.info(f"SQLite database initialized at: {self.config.get('connection', {}).get('sqlite', {}).get('path', 'logs/yaLLM.db')}")
