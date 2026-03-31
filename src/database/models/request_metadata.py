"""Request metadata model for long-term analytics and log listing."""

from __future__ import annotations

from datetime import datetime
from typing import Any

from sqlalchemy import Boolean, Column, DateTime, Float, Integer, String

from ..base import Base
from .base import TimestampMixin, UUIDPrimaryKeyMixin


class RequestMetadata(Base, UUIDPrimaryKeyMixin, TimestampMixin):
    """Narrow request metadata record for analytics and log listing."""

    __tablename__ = "request_metadata"
    __comment__ = "Narrow request metadata for analytics and log listing"

    request_time = Column(DateTime, nullable=False, index=True)
    model_name = Column(String(255), nullable=False, index=True)
    is_stream = Column(Boolean, nullable=False, default=False)
    path = Column(String(512), nullable=True)
    method = Column(String(10), nullable=True)
    query = Column(String(1024), nullable=True)
    request_path = Column(String(1024), nullable=True)

    outcome = Column(String(50), nullable=True, index=True)
    duration_ms = Column(Integer, nullable=True)
    stop_reason = Column(String(50), nullable=True, index=True)
    is_tool_call = Column(Boolean, nullable=False, default=False)
    conversation_turn = Column(Integer, nullable=True)

    app_key_id = Column(String(255), nullable=True, index=True)
    backend_name = Column(String(255), nullable=True)
    backend_status = Column(Integer, nullable=True)

    prompt_tokens = Column(Integer, nullable=True)
    completion_tokens = Column(Integer, nullable=True)
    total_tokens = Column(Integer, nullable=True)
    cached_tokens = Column(Integer, nullable=True)
    reasoning_tokens = Column(Integer, nullable=True)
    tokens_per_second = Column(Float, nullable=True)
    weighted_tokens = Column(Float, nullable=True)

    full_request_path = Column(String(2048), nullable=True)
    full_request_expires_at = Column(DateTime, nullable=True, index=True)

    @property
    def duration_seconds(self) -> float | None:
        if self.duration_ms is None:
            return None
        return self.duration_ms / 1000.0

    @property
    def usage_stats(self) -> dict[str, Any] | None:
        usage: dict[str, Any] = {}

        if self.prompt_tokens is not None:
            usage["prompt_tokens"] = self.prompt_tokens
        if self.completion_tokens is not None:
            usage["completion_tokens"] = self.completion_tokens
        if self.total_tokens is not None:
            usage["total_tokens"] = self.total_tokens

        prompt_details: dict[str, Any] = {}
        if self.cached_tokens:
            prompt_details["cached_tokens"] = self.cached_tokens
        if prompt_details:
            usage["prompt_tokens_details"] = prompt_details

        completion_details: dict[str, Any] = {}
        if self.reasoning_tokens:
            completion_details["reasoning_tokens"] = self.reasoning_tokens
        if completion_details:
            usage["completion_tokens_details"] = completion_details

        if self.tokens_per_second is not None:
            usage["tokens_per_second"] = self.tokens_per_second
        if self.weighted_tokens is not None:
            usage["weighted_tokens"] = self.weighted_tokens

        return usage or None

    def to_dict(self) -> dict[str, Any]:
        return {
            "id": str(self.id),
            "request_time": self.request_time.isoformat() if self.request_time else None,
            "model_name": self.model_name,
            "is_stream": self.is_stream,
            "path": self.path,
            "method": self.method,
            "query": self.query,
            "outcome": self.outcome,
            "duration_ms": self.duration_ms,
            "duration_seconds": self.duration_seconds,
            "request_path": self.request_path,
            "stop_reason": self.stop_reason,
            "is_tool_call": self.is_tool_call,
            "conversation_turn": self.conversation_turn,
            "app_key_id": self.app_key_id,
            "backend_name": self.backend_name,
            "backend_status": self.backend_status,
            "usage_stats": self.usage_stats,
            "full_request_path": self.full_request_path,
            "full_request_expires_at": (
                self.full_request_expires_at.isoformat()
                if self.full_request_expires_at
                else None
            ),
            "created_at": self.created_at.isoformat() if self.created_at else None,
        }

    def __repr__(self) -> str:
        return (
            f"<RequestMetadata(id={self.id}, model={self.model_name}, "
            f"outcome={self.outcome}, duration_ms={self.duration_ms})>"
        )
