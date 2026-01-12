"""Repository for querying individual request logs from the database.

Provides methods for paginated log queries, filtering, and analytics.
"""

import json
import logging
from datetime import datetime, timedelta, timezone
from typing import Any, Optional
from uuid import UUID

from sqlalchemy import and_, or_, select, text, func
from sqlalchemy.orm import Session

from .factory import get_database
from .models import RequestLog, ErrorLog

logger = logging.getLogger("yallmp-proxy")

BODY_MAX_CHARS_DEFAULT = 100_000


def _truncate_json_payload(value: Any, max_chars: int) -> tuple[Any, bool, int]:
    """Truncate JSON-serializable values to a max character count.

    Returns the possibly-truncated value, a truncation flag, and the original
    serialized length (0 when value is None).
    """
    if value is None:
        return value, False, 0
    if max_chars <= 0:
        return value, False, 0
    try:
        serialized = json.dumps(value, ensure_ascii=False)
    except (TypeError, ValueError):
        serialized = str(value)
    original_len = len(serialized)
    if original_len <= max_chars:
        return value, False, original_len
    return serialized[:max_chars], True, original_len


class LogsRepository:
    """Repository for querying request logs."""

    def __init__(self) -> None:
        """Initialize the logs repository."""
        self._database = get_database()

    def get_logs(
        self,
        limit: int = 100,
        offset: int = 0,
        model_name: Optional[str] = None,
        outcome: Optional[str] = None,
        stop_reason: Optional[str] = None,
        is_tool_call: Optional[bool] = None,
        start_time: Optional[datetime] = None,
        end_time: Optional[datetime] = None,
        search: Optional[str] = None,
    ) -> dict[str, Any]:
        """Get paginated request logs with optional filters.

        Args:
            limit: Maximum number of logs to return.
            offset: Number of logs to skip.
            model_name: Filter by model name.
            outcome: Filter by outcome (success, error, cancelled).
            stop_reason: Filter by stop reason.
            is_tool_call: Filter by whether tool calls were made.
            start_time: Filter logs from this time.
            end_time: Filter logs until this time.
            search: Full-text search in request/response body.

        Returns:
            Dictionary with logs list, total count, and pagination info.
        """
        self._database.initialize()

        # Build base query
        query = select(RequestLog)
        count_query = select(func.count()).select_from(RequestLog)

        # Apply filters
        conditions = []

        if model_name:
            conditions.append(RequestLog.model_name.ilike(f"%{model_name}%"))

        if outcome:
            conditions.append(RequestLog.outcome == outcome)

        if stop_reason:
            conditions.append(RequestLog.stop_reason == stop_reason)

        if is_tool_call is not None:
            conditions.append(RequestLog.is_tool_call == is_tool_call)

        if start_time:
            conditions.append(RequestLog.request_time >= start_time)

        if end_time:
            conditions.append(RequestLog.request_time <= end_time)

        if search:
            # Search in body content and full_response
            search_pattern = f"%{search}%"
            conditions.append(
                or_(
                    RequestLog.body.cast(text).ilike(search_pattern),
                    RequestLog.full_response.ilike(search_pattern),
                )
            )

        if conditions:
            where_clause = and_(*conditions)
            query = query.where(where_clause)
            count_query = count_query.where(where_clause)

        # Get total count
        with self._database.session() as sess:
            total_count = sess.execute(count_query).scalar() or 0

        # Add ordering and pagination
        query = query.order_by(RequestLog.request_time.desc()).offset(offset).limit(limit)

        # Execute query and convert to dicts within session context
        with self._database.session() as sess:
            result = sess.execute(query)
            logs = [row[0] for row in result.fetchall()]
            # Convert to dicts while session is still open, excluding large fields
            logs_dicts = [self._log_to_summary_dict(log) for log in logs]

        return {
            "logs": logs_dicts,
            "total": total_count,
            "limit": limit,
            "offset": offset,
            "has_more": offset + len(logs) < total_count,
        }

    def _log_to_summary_dict(self, log: RequestLog) -> dict[str, Any]:
        """Convert a RequestLog to a dictionary for the list endpoint.

        Excludes large fields like full_response and stream_chunks to improve performance.
        """
        return {
            "id": str(log.id),
            "request_time": log.request_time.isoformat() if log.request_time else None,
            "model_name": log.model_name,
            "is_stream": log.is_stream,
            "path": log.path,
            "method": log.method,
            "query": log.query,
            "headers": log.headers,
            # Skip large field: body
            "route": log.route,
            "backend_attempts": log.backend_attempts,
            # Skip large fields: stream_chunks, errors, usage_stats
            "errors": log.errors,
            "usage_stats": log.usage_stats,
            "outcome": log.outcome,
            "duration_ms": log.duration_ms,
            "duration_seconds": log.duration_seconds,
            "request_path": log.request_path,
            "stop_reason": log.stop_reason,
            # Skip large field: full_response
            "is_tool_call": log.is_tool_call,
            "conversation_turn": log.conversation_turn,
            "created_at": log.created_at.isoformat() if log.created_at else None,
        }

    def get_log_by_id(
        self,
        log_id: UUID,
        body_max_chars: Optional[int] = BODY_MAX_CHARS_DEFAULT,
    ) -> Optional[dict[str, Any]]:
        """Get a single log by ID with full details.

        Note: Large fields like stream_chunks are limited to the first 50 chunks
        to prevent timeouts and memory issues. Use a dedicated endpoint for
        full stream data if needed.

        Args:
            log_id: The UUID of the log to retrieve.

        Returns:
            Dictionary with log details, or None if not found.
        """
        self._database.initialize()

        with self._database.session() as sess:
            query = select(RequestLog).where(RequestLog.id == log_id)
            result = sess.execute(query)
            log = result.scalar_one_or_none()

            if not log:
                return None

            log_dict = log.to_dict()

            # Limit stream_chunks to prevent huge response payloads
            # Full stream data can be 500KB+ which causes timeouts
            if log_dict.get("stream_chunks") and len(log_dict["stream_chunks"]) > 50:
                original_count = len(log_dict["stream_chunks"])
                log_dict["stream_chunks"] = log_dict["stream_chunks"][:50]
                log_dict["stream_chunks_truncated"] = True
                log_dict["stream_chunks_total"] = original_count

            # Truncate very large request bodies to avoid large responses
            if body_max_chars is None:
                body_max_chars = BODY_MAX_CHARS_DEFAULT
            if body_max_chars is not None and body_max_chars > 0:
                body_value, truncated, original_len = _truncate_json_payload(
                    log_dict.get("body"),
                    body_max_chars,
                )
                if truncated:
                    log_dict["body"] = body_value
                    log_dict["body_truncated"] = True
                    log_dict["body_total_chars"] = original_len
                    log_dict["body_preview_chars"] = min(original_len, body_max_chars)

            # Truncate very long full_response to prevent timeouts
            if log_dict.get("full_response") and len(log_dict["full_response"]) > 100000:
                log_dict["full_response"] = log_dict["full_response"][:100000]
                log_dict["full_response_truncated"] = True

            # Limit backend_attempts to prevent large payloads
            if log_dict.get("backend_attempts") and len(log_dict["backend_attempts"]) > 5:
                log_dict["backend_attempts"] = log_dict["backend_attempts"][:5]
                log_dict["backend_attempts_truncated"] = True

            # Get linked error logs if any
            error_query = select(ErrorLog).where(ErrorLog.request_log_id == log_id)
            error_result = sess.execute(error_query)
            error_logs = [row[0].to_dict() for row in error_result.fetchall()]
            log_dict["error_logs"] = error_logs

            return log_dict

    def get_stop_reason_counts(
        self,
        start_time: Optional[datetime] = None,
        end_time: Optional[datetime] = None,
    ) -> list[dict[str, Any]]:
        """Get counts grouped by stop reason.

        Args:
            start_time: Start of time range.
            end_time: End of time range.

        Returns:
            List of dicts with stop_reason, count, and percentage.
        """
        self._database.initialize()

        if start_time is None:
            start_time = datetime.now(timezone.utc) - timedelta(days=1)
        if end_time is None:
            end_time = datetime.now(timezone.utc)

        # Get total count with stop_reason
        total_query = (
            select(func.count())
            .where(RequestLog.request_time >= start_time)
            .where(RequestLog.request_time <= end_time)
            .where(RequestLog.stop_reason.isnot(None))
        )

        with self._database.session() as sess:
            total = sess.execute(total_query).scalar() or 0

            # Get counts by stop_reason
            query = (
                select(
                    RequestLog.stop_reason,
                    func.count().label("count"),
                )
                .where(RequestLog.request_time >= start_time)
                .where(RequestLog.request_time <= end_time)
                .where(RequestLog.stop_reason.isnot(None))
                .group_by(RequestLog.stop_reason)
                .order_by(func.count().desc())
            )
            results = sess.execute(query).fetchall()

        stop_reasons = []
        for row in results:
            reason = row[0]
            count = row[1]
            percentage = round((count / total * 100), 2) if total > 0 else 0
            stop_reasons.append({
                "reason": reason,
                "count": count,
                "percentage": percentage,
            })

        return stop_reasons

    def get_tool_call_rate(
        self,
        start_time: Optional[datetime] = None,
        end_time: Optional[datetime] = None,
    ) -> dict[str, Any]:
        """Get the percentage of requests that made tool calls.

        Args:
            start_time: Start of time range.
            end_time: End of time range.

        Returns:
            Dictionary with tool call statistics.
        """
        self._database.initialize()

        if start_time is None:
            start_time = datetime.now(timezone.utc) - timedelta(days=1)
        if end_time is None:
            end_time = datetime.now(timezone.utc)

        with self._database.session() as sess:
            # Total requests
            total_query = (
                select(func.count())
                .where(RequestLog.request_time >= start_time)
                .where(RequestLog.request_time <= end_time)
            )
            total = sess.execute(total_query).scalar() or 0

            # Tool call requests
            tool_query = (
                select(func.count())
                .where(RequestLog.request_time >= start_time)
                .where(RequestLog.request_time <= end_time)
                .where(RequestLog.is_tool_call == True)
            )
            tool_count = sess.execute(tool_query).scalar() or 0

        rate = round((tool_count / total * 100), 2) if total > 0 else 0

        return {
            "total_requests": total,
            "tool_call_requests": tool_count,
            "tool_call_rate": rate,
            "start_time": start_time.isoformat(),
            "end_time": end_time.isoformat(),
        }

    def get_requests_per_model_with_stop_reason(
        self,
        start_time: Optional[datetime] = None,
        end_time: Optional[datetime] = None,
        limit: int = 100,
    ) -> list[dict[str, Any]]:
        """Get request counts per model with stop reason breakdown.

        Args:
            start_time: Start of time range.
            end_time: End of time range.
            limit: Maximum number of models to return.

        Returns:
            List of dicts with model name, total count, and stop reason breakdown.
        """
        self._database.initialize()

        if start_time is None:
            start_time = datetime.now(timezone.utc) - timedelta(days=1)
        if end_time is None:
            end_time = datetime.now(timezone.utc)

        with self._database.session() as sess:
            # Get requests per model
            query = (
                select(
                    RequestLog.model_name,
                    func.count().label("total"),
                )
                .where(RequestLog.request_time >= start_time)
                .where(RequestLog.request_time <= end_time)
                .group_by(RequestLog.model_name)
                .order_by(func.count().desc())
                .limit(limit)
            )
            results = sess.execute(query).fetchall()

        models = []
        for row in results:
            model_name = row[0]
            total = row[1]

            # Get stop reason breakdown for this model
            stop_reasons = self.get_stop_reason_counts_for_model(
                model_name, start_time, end_time
            )

            models.append({
                "model_name": model_name,
                "total_requests": total,
                "stop_reasons": stop_reasons,
            })

        return models

    def get_stop_reason_counts_for_model(
        self,
        model_name: str,
        start_time: Optional[datetime] = None,
        end_time: Optional[datetime] = None,
    ) -> dict[str, int]:
        """Get stop reason counts for a specific model.

        Args:
            model_name: The model name to filter by.
            start_time: Start of time range.
            end_time: End of time range.

        Returns:
            Dictionary mapping stop_reason to count.
        """
        self._database.initialize()

        if start_time is None:
            start_time = datetime.now(timezone.utc) - timedelta(days=1)
        if end_time is None:
            end_time = datetime.now(timezone.utc)

        with self._database.session() as sess:
            query = (
                select(
                    RequestLog.stop_reason,
                    func.count().label("count"),
                )
                .where(RequestLog.model_name == model_name)
                .where(RequestLog.request_time >= start_time)
                .where(RequestLog.request_time <= end_time)
                .where(RequestLog.stop_reason.isnot(None))
                .group_by(RequestLog.stop_reason)
            )
            results = sess.execute(query).fetchall()

        return {row[0]: row[1] for row in results}


# Global repository instance
_logs_repository: Optional[LogsRepository] = None


def get_logs_repository() -> LogsRepository:
    """Get the global logs repository instance.

    Returns:
        The global LogsRepository instance.
    """
    global _logs_repository
    if _logs_repository is None:
        _logs_repository = LogsRepository()
    return _logs_repository
