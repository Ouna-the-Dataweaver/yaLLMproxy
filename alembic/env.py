"""Alembic environment configuration."""

import os
import sys
from logging.config import fileConfig

from sqlalchemy import pool, create_engine
from sqlalchemy.engine import Connection
from alembic.config import Config as AlembicConfig
from alembic import context

# Add the src directory to the path
sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

# Import the base and models for autogeneration
from src.database.base import Base
from src.database.models import RequestLog, ErrorLog

# Import configuration loader
try:
    from src.config_loader import load_config
except ImportError:
    from config_loader import load_config


# this is the Alembic Config object, which provides
# access to the values set within the .ini file in use.
alembic_config = AlembicConfig()

# Interpret the config file for Python logging.
# This line sets up loggers basically.
if alembic_config.config_file_name is not None:
    fileConfig(alembic_config.config_file_name)

# add your model's MetaData object here
# for 'autogenerate' support
target_metadata = Base.metadata


def get_database_url() -> str:
    """Get the database URL from configuration.

    Returns:
        A SQLAlchemy database URL string.
    """
    # Try to load from config file
    try:
        cfg = load_config("configs/config.yaml")
        db_config = cfg.get("database", {})

        backend = db_config.get("backend", "sqlite").lower()

        if backend == "sqlite":
            sqlite_config = db_config.get("connection", {}).get("sqlite", {})
            db_path = sqlite_config.get("path", "logs/yaLLM.db")
            if not os.path.isabs(db_path):
                project_root = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
                db_path = os.path.join(project_root, db_path)
            return f"sqlite:///{db_path}"

        elif backend in ("postgres", "postgresql"):
            pg_config = db_config.get("connection", {}).get("postgres", {})
            host = pg_config.get("host", "localhost")
            port = pg_config.get("port", 5432)
            database = pg_config.get("database", "yallm_proxy")
            user = pg_config.get("user", "postgres")
            password = pg_config.get("password", "")
            return f"postgresql://{user}:{password}@{host}:{port}/{database}"

    except Exception:
        # If config loading fails, default to SQLite
        pass

    # Default to SQLite in logs directory
    project_root = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
    db_path = os.path.join(project_root, "logs", "yaLLM.db")
    return f"sqlite:///{db_path}"


def run_migrations_offline() -> None:
    """Run migrations in 'offline' mode.

    This configures the context with just a URL
    and not an Engine, though an Engine is acceptable
    here too.  By skipping the Engine creation
    we don't even need a DBAPI to be available.
    """
    url = get_database_url()

    context.configure(
        url=url,
        target_metadata=target_metadata,
        literal_binds=True,
        dialect_opts={"paramstyle": "named"},
    )

    with context.begin_transaction():
        context.run_migrations()


def run_migrations_online() -> None:
    """Run migrations in 'online' mode.

    In this scenario we need to create an Engine
    and associate a connection with the context.
    """
    connectable = create_engine(
        get_database_url(),
        poolclass=pool.NullPool,
    )

    with connectable.connect() as connection:
        context.configure(
            connection=connection,
            target_metadata=target_metadata
        )

        with context.begin_transaction():
            context.run_migrations()


if context.is_offline_mode():
    run_migrations_offline()
else:
    run_migrations_online()
