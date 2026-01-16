"""Response state model for Responses API storage.

Stores response objects for the store + previous_response_id functionality.
"""

from datetime import datetime
from typing import Any, Optional

from sqlalchemy import Column, DateTime, Index, String, Text
from sqlalchemy.types import JSON

from ..base import Base
from .base import TimestampMixin


class ResponseState(Base, TimestampMixin):
    """Model for storing Responses API response states.

    This model enables the `store` and `previous_response_id` functionality
    by persisting responses for later retrieval and conversation continuation.
    """

    __tablename__ = "response_states"
    __comment__ = "Stored responses for the Responses API"

    # Primary key - the response ID (e.g., resp_abc123...)
    id = Column(
        String(64),
        primary_key=True,
        comment="Unique response identifier (e.g., resp_xxx)"
    )

    # Reference to previous response for conversation chaining
    previous_response_id = Column(
        String(64),
        nullable=True,
        index=True,
        comment="ID of the previous response in the conversation chain"
    )

    # Model information
    model = Column(
        String(255),
        nullable=False,
        index=True,
        comment="The model name used for this response"
    )

    # Response status
    status = Column(
        String(32),
        nullable=False,
        default="completed",
        comment="Response status (completed, failed, etc.)"
    )

    # Input data (original request input)
    input_data = Column(
        JSON,
        nullable=True,
        comment="Original input from the request (string or items array)"
    )

    # Output data (the output array)
    output_data = Column(
        JSON,
        nullable=True,
        comment="Response output items array"
    )

    # Full response object (for complete retrieval)
    full_response = Column(
        JSON,
        nullable=True,
        comment="Complete response object for retrieval"
    )

    # Usage statistics
    usage = Column(
        JSON,
        nullable=True,
        comment="Token usage statistics"
    )

    # Metadata
    response_metadata = Column(
        JSON,
        nullable=True,
        comment="User-provided metadata"
    )

    # TTL/expiration
    expires_at = Column(
        DateTime,
        nullable=True,
        index=True,
        comment="Expiration time for automatic cleanup"
    )

    # Indexes for efficient querying
    __table_args__ = (
        Index("ix_response_states_model_created", "model", "created_at"),
        Index("ix_response_states_previous", "previous_response_id"),
    )

    def to_dict(self) -> dict:
        """Convert to dictionary for API responses."""
        return {
            "id": self.id,
            "previous_response_id": self.previous_response_id,
            "model": self.model,
            "status": self.status,
            "input_data": self.input_data,
            "output_data": self.output_data,
            "usage": self.usage,
            "metadata": self.response_metadata,
            "created_at": self.created_at.isoformat() if self.created_at else None,
            "_full_response": self.full_response,
        }
