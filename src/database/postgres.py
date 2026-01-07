"""PostgreSQL database implementation."""

import logging
from typing import Any

from .base import DatabaseBase

logger = logging.getLogger("yallmp-proxy")


class PostgreSQLDatabase(DatabaseBase):
    """PostgreSQL database implementation."""

    @property
    def backend_name(self) -> str:
        """Return the name of the database backend."""
        return "postgresql"

    def get_connection_string(self) -> str:
        """Return the PostgreSQL connection string.

        Returns:
            A PostgreSQL connection string for SQLAlchemy using psycopg2.
        """
        pg_config = self.config.get("connection", {}).get("postgres", {})

        host = pg_config.get("host", "localhost")
        port = pg_config.get("port", 5432)
        database = pg_config.get("database", "yallm_proxy")
        user = pg_config.get("user", "postgres")
        password = pg_config.get("password", "")

        # Log connection info (without password)
        logger.debug(f"PostgreSQL connection: host={host}, port={port}, database={database}, user={user}")

        return f"postgresql://{user}:{password}@{host}:{port}/{database}"

    def initialize(self) -> None:
        """Initialize the PostgreSQL database.

        Connects to the PostgreSQL server and creates tables if they don't exist.
        """
        # Check if already initialized to avoid duplicate logging
        if self._engine is not None:
            return

        super().initialize()
        pg_config = self.config.get("connection", {}).get("postgres", {})
        logger.info(f"PostgreSQL database initialized: {pg_config.get('host', 'localhost')}:{pg_config.get('port', 5432)}/{pg_config.get('database', 'yallm_proxy')}")
