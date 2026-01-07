"""Base declarative model for database tables."""

import uuid
from datetime import datetime, timezone
from typing import Any

from sqlalchemy import Column, DateTime, String, Text
from sqlalchemy.dialects.postgresql import UUID
from sqlalchemy.types import JSON

from ..base import Base


class TimestampMixin:
    """Mixin for adding timestamp fields to models."""

    created_at = Column(
        DateTime,
        default=lambda: datetime.now(timezone.utc),
        nullable=False,
        comment="Record creation timestamp"
    )


class UUIDPrimaryKeyMixin:
    """Mixin for adding UUID primary key to models."""

    id = Column(
        UUID(as_uuid=True),
        primary_key=True,
        default=uuid.uuid4,
        comment="Unique identifier (UUID)"
    )


def json_column(name: str, **kwargs: Any) -> Column:
    """Create a JSON column that supports JSONB in PostgreSQL.

    Args:
        name: The column name.
        **kwargs: Additional column arguments.

    Returns:
        A SQLAlchemy Column configured for JSON/JSONB.
    """
    return Column(
        name,
        JSON,
        comment="JSON data",
        **kwargs
    )
