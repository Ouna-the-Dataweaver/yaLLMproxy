"""Base database class and connection management."""

import logging
from abc import ABC, abstractmethod
from contextlib import contextmanager
from pathlib import Path
from typing import Any, Generator, Optional

from sqlalchemy import create_engine, event
from sqlalchemy.engine import Engine
from sqlalchemy.pool import NullPool, StaticPool
from sqlalchemy.orm import Session, sessionmaker, declarative_base

logger = logging.getLogger("yallmp-proxy")

# Base class for SQLAlchemy models
Base = declarative_base()


class DatabaseBase(ABC):
    """Abstract base class for database operations."""

    def __init__(self, config: dict[str, Any]) -> None:
        """Initialize database with configuration.

        Args:
            config: Database configuration dictionary.
        """
        self.config = config
        self._engine: Optional[Engine] = None
        self._session_factory: Optional[sessionmaker[Session]] = None

    @property
    @abstractmethod
    def backend_name(self) -> str:
        """Return the name of the database backend."""
        pass

    @abstractmethod
    def get_connection_string(self) -> str:
        """Return the database connection string."""
        pass

    def get_pool_options(self) -> dict[str, Any]:
        """Get connection pool options for this backend.

        Returns:
            Dictionary of options to pass to create_engine.
        """
        return {
            "pool_size": self.config.get("pool_size", 5),
            "max_overflow": self.config.get("max_overflow", 10),
            "pool_pre_ping": True,
        }

    def initialize(self) -> None:
        """Initialize the database engine and session factory."""
        if self._engine is not None:
            return

        connection_string = self.get_connection_string()
        logger.info(f"Initializing {self.backend_name} database connection")

        # Get pool options for this backend
        pool_options = self.get_pool_options()

        self._engine = create_engine(
            connection_string,
            **pool_options,
            echo=False,  # Set to True for debugging SQL
        )

        # Create session factory
        self._session_factory = sessionmaker(bind=self._engine)

        # Create tables if they don't exist
        Base.metadata.create_all(self._engine)

        logger.info(f"{self.backend_name} database initialized successfully")

    def get_session(self) -> Session:
        """Get a new database session.

        Returns:
            A new SQLAlchemy Session instance.

        Raises:
            RuntimeError: If database is not initialized.
        """
        if self._session_factory is None:
            raise RuntimeError("Database not initialized. Call initialize() first.")
        return self._session_factory()

    @contextmanager
    def session(self) -> Generator[Session, None, None]:
        """Context manager for database sessions.

        Yields:
            A database session that is automatically committed/rolled back.
        """
        sess = self.get_session()
        try:
            yield sess
            sess.commit()
        except Exception:
            sess.rollback()
            raise
        finally:
            sess.close()

    def close(self) -> None:
        """Close the database engine and release all connections."""
        if self._engine is not None:
            logger.info(f"Closing {self.backend_name} database connection")
            self._engine.dispose()
            self._engine = None
            self._session_factory = None

    @property
    def is_initialized(self) -> bool:
        """Check if the database is initialized."""
        return self._engine is not None

    def health_check(self) -> bool:
        """Check if the database connection is healthy.

        Returns:
            True if the database is accessible, False otherwise.
        """
        from sqlalchemy import text

        if not self.is_initialized:
            return False
        try:
            with self.session() as sess:
                sess.execute(text("SELECT 1"))
            return True
        except Exception as e:
            logger.error(f"Database health check failed: {e}")
            return False
