"""Request log model for database storage."""

from datetime import datetime, timezone
from typing import Any, Optional
from uuid import UUID

from sqlalchemy import Boolean, Column, DateTime, Integer, String, Text
from sqlalchemy.dialects.postgresql import UUID
from sqlalchemy.types import JSON

from ..base import Base
from .base import TimestampMixin, UUIDPrimaryKeyMixin, json_column


class RequestLog(Base, UUIDPrimaryKeyMixin, TimestampMixin):
    """Model for storing request/response logs.

    This model captures all details of a chat completion request including
    the request body, response data, and usage statistics.
    """

    __tablename__ = "request_logs"
    __comment__ = "Chat completion request and response logs"

    # Request information
    request_time = Column(
        DateTime,
        nullable=False,
        index=True,
        comment="Timestamp when the request was received"
    )

    model_name = Column(
        String(255),
        nullable=False,
        index=True,
        comment="The model name used for the request"
    )

    is_stream = Column(
        Boolean,
        nullable=False,
        default=False,
        comment="Whether the request was streaming"
    )

    path = Column(
        String(512),
        nullable=True,
        comment="Request path (e.g., /v1/chat/completions)"
    )

    method = Column(
        String(10),
        nullable=True,
        comment="HTTP method (e.g., POST)"
    )

    query = Column(
        String(1024),
        nullable=True,
        comment="Query string if present"
    )

    # JSON data fields
    headers = Column(
        JSON,
        nullable=True,
        comment="Request headers (sanitized)"
    )

    body = Column(
        JSON,
        nullable=True,
        comment="Request body as JSON"
    )

    route = Column(
        JSON,
        nullable=True,
        comment="Routing information (list of backends tried)"
    )

    backend_attempts = Column(
        JSON,
        nullable=True,
        comment="Backend attempts with responses"
    )

    stream_chunks = Column(
        JSON,
        nullable=True,
        comment="Stream chunks data (if logged)"
    )

    errors = Column(
        JSON,
        nullable=True,
        comment="Error information if any errors occurred"
    )

    usage_stats = Column(
        JSON,
        nullable=True,
        comment="Usage statistics from the response"
    )

    # Outcome and metrics
    outcome = Column(
        String(50),
        nullable=True,
        comment="Request outcome: success, error, cancelled"
    )

    duration_ms = Column(
        Integer,
        nullable=True,
        comment="Request duration in milliseconds"
    )

    # Additional metadata
    request_path = Column(
        String(1024),
        nullable=True,
        comment="Full request path including query"
    )

    # Enhanced logging fields for stop_reason and agentic workflows
    stop_reason = Column(
        String(50),
        nullable=True,
        index=True,
        comment="Finish reason from the response: stop, tool_calls, length, content_filter, etc."
    )

    full_response = Column(
        Text,
        nullable=True,
        comment="Concatenated complete response text (especially for streaming)"
    )

    is_tool_call = Column(
        Boolean,
        nullable=False,
        default=False,
        comment="Whether this request resulted in tool/function calls"
    )

    conversation_turn = Column(
        Integer,
        nullable=True,
        comment="Turn number in agentic conversation sequence"
    )

    @property
    def duration_seconds(self) -> Optional[float]:
        """Get duration in seconds."""
        if self.duration_ms is None:
            return None
        return self.duration_ms / 1000.0

    @property
    def successful(self) -> bool:
        """Check if the request was successful."""
        return self.outcome == "success" if self.outcome else False

    @property
    def had_errors(self) -> bool:
        """Check if the request had errors."""
        return self.errors is not None and len(self.errors) > 0

    def to_dict(self) -> dict[str, Any]:
        """Convert the model to a dictionary.

        Returns:
            A dictionary representation of the request log.
        """
        return {
            "id": str(self.id),
            "request_time": self.request_time.isoformat() if self.request_time else None,
            "model_name": self.model_name,
            "is_stream": self.is_stream,
            "path": self.path,
            "method": self.method,
            "query": self.query,
            "headers": self.headers,
            "body": self.body,
            "route": self.route,
            "backend_attempts": self.backend_attempts,
            "stream_chunks": self.stream_chunks,
            "errors": self.errors,
            "usage_stats": self.usage_stats,
            "outcome": self.outcome,
            "duration_ms": self.duration_ms,
            "duration_seconds": self.duration_seconds,
            "request_path": self.request_path,
            "stop_reason": self.stop_reason,
            "full_response": self.full_response,
            "is_tool_call": self.is_tool_call,
            "conversation_turn": self.conversation_turn,
            "created_at": self.created_at.isoformat() if self.created_at else None,
        }

    def __repr__(self) -> str:
        """Return a string representation of the request log."""
        return (
            f"<RequestLog(id={self.id}, model={self.model_name}, "
            f"outcome={self.outcome}, duration_ms={self.duration_ms})>"
        )
