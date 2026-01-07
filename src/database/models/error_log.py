"""Error log model for database storage."""

from datetime import datetime, timezone
from typing import Any, Optional
from uuid import UUID

from sqlalchemy import Column, DateTime, ForeignKey, Integer, String, Text
from sqlalchemy.dialects.postgresql import UUID
from sqlalchemy.types import JSON

from ..base import Base
from .base import TimestampMixin, UUIDPrimaryKeyMixin, json_column


class ErrorLog(Base, UUIDPrimaryKeyMixin, TimestampMixin):
    """Model for storing error logs.

    This model captures error information including error type, message,
    and optional reference to the original request log.
    """

    __tablename__ = "error_logs"
    __comment__ = "Error event logs with optional request reference"

    # Error information
    timestamp = Column(
        DateTime,
        nullable=False,
        index=True,
        comment="Timestamp when the error occurred"
    )

    model_name = Column(
        String(255),
        nullable=True,
        index=True,
        comment="The model name associated with the error (if applicable)"
    )

    error_type = Column(
        String(100),
        nullable=False,
        index=True,
        comment="Error type (e.g., sse_stream_error, http_error, timeout)"
    )

    error_message = Column(
        Text,
        nullable=False,
        comment="Detailed error message"
    )

    # Backend and request information
    backend_name = Column(
        String(255),
        nullable=True,
        comment="The backend that produced the error"
    )

    http_status = Column(
        Integer,
        nullable=True,
        comment="HTTP status code if applicable"
    )

    request_path = Column(
        String(1024),
        nullable=True,
        comment="Request path where the error occurred"
    )

    # Foreign key to request log (optional)
    request_log_id = Column(
        UUID(as_uuid=True),
        ForeignKey("request_logs.id", ondelete="SET NULL"),
        nullable=True,
        comment="Reference to the request log (if error occurred during a request)"
    )

    # Additional context as JSON
    extra_context = Column(
        JSON,
        nullable=True,
        comment="Additional error context as JSON"
    )

    @property
    def has_request_reference(self) -> bool:
        """Check if this error has a reference to a request log."""
        return self.request_log_id is not None

    def to_dict(self) -> dict[str, Any]:
        """Convert the model to a dictionary.

        Returns:
            A dictionary representation of the error log.
        """
        return {
            "id": str(self.id),
            "timestamp": self.timestamp.isoformat() if self.timestamp else None,
            "model_name": self.model_name,
            "error_type": self.error_type,
            "error_message": self.error_message,
            "backend_name": self.backend_name,
            "http_status": self.http_status,
            "request_path": self.request_path,
            "request_log_id": str(self.request_log_id) if self.request_log_id else None,
            "extra_context": self.extra_context,
            "created_at": self.created_at.isoformat() if self.created_at else None,
        }

    def __repr__(self) -> str:
        """Return a string representation of the error log."""
        return (
            f"<ErrorLog(id={self.id}, type={self.error_type}, "
            f"model={self.model_name}, message={self.error_message[:50]}...)>"
        )
