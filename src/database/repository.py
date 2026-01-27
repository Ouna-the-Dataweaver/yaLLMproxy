"""Repository classes for database queries.

Provides specialized query classes for different data access patterns.
"""

import logging
from datetime import datetime, timedelta, timezone
from typing import Any, Optional

from sqlalchemy import func, select, text, Integer
from sqlalchemy.orm import Session

from .factory import get_database, get_current_backend
from .models import RequestLog, ErrorLog

logger = logging.getLogger("yallmp-proxy")


def _truncate_timestamp(column, interval: str = "hour"):
    """Generate a database-specific timestamp truncation expression.

    Args:
        column: The timestamp column to truncate.
        interval: Time interval (hour, day, minute).

    Returns:
        A SQLAlchemy expression for truncated timestamp.
    """
    backend = get_current_backend()

    if backend == "postgres":
        # PostgreSQL: use date_trunc function
        if interval == "hour":
            return func.date_trunc("hour", column)
        elif interval == "day":
            return func.date_trunc("day", column)
        elif interval == "minute":
            return func.date_trunc("minute", column)
        else:
            return func.date_trunc("hour", column)
    else:
        # SQLite: use strftime function
        # Format: YYYY-MM-DD HH:MM:SS for SQLite datetime format
        if interval == "hour":
            # Truncate to hour: YYYY-MM-DD HH:00:00
            return func.strftime("%Y-%m-%d %H:00:00", column)
        elif interval == "day":
            # Truncate to day: YYYY-MM-DD 00:00:00
            return func.strftime("%Y-%m-%d 00:00:00", column)
        elif interval == "minute":
            # Truncate to minute: YYYY-MM-DD HH:MM:00
            return func.strftime("%Y-%m-%d %H:%M:00", column)
        else:
            return func.strftime("%Y-%m-%d %H:00:00", column)


def _format_timestamp(value):
    """Format a timestamp to ISO format string.

    Handles both datetime objects (from PostgreSQL) and strings (from SQLite).

    Args:
        value: A datetime object or string timestamp.

    Returns:
        ISO format timestamp string, or None if value is None.
    """
    if value is None:
        return None
    if isinstance(value, str):
        # Already a string (from SQLite strftime)
        return value
    # Datetime object (from PostgreSQL date_trunc)
    return value.isoformat()


def _get_json_value(key):
    """Generate a backend-specific expression to extract a value from a JSON column.

    Args:
        key: The key to extract (supports dot notation for nested keys like 'prompt_tokens_details.cached_tokens').

    Returns:
        A SQLAlchemy expression for the extracted value.
    """
    backend = get_current_backend()

    if backend == "postgres":
        # PostgreSQL: use ->> operator for text extraction
        # Split on '.' for nested keys
        keys = key.split('.')
        expr = RequestLog.usage_stats
        for k in keys:
            # Use -> for JSON objects, ->> for text result
            expr = expr.op('->>')(k)
        return func.coalesce(expr.cast(Integer), 0)
    else:
        # SQLite: use json_extract function with dot notation
        return func.coalesce(func.json_extract(RequestLog.usage_stats, f'$.{key}'), 0)


class UsageRepository:
    """Repository for querying usage statistics from the database."""

    def __init__(self) -> None:
        """Initialize the usage repository."""
        self._database = get_database()

    def get_requests_per_model(
        self,
        start_time: Optional[datetime] = None,
        end_time: Optional[datetime] = None,
        limit: int = 100,
    ) -> list[dict[str, Any]]:
        """Get request counts grouped by model.

        Args:
            start_time: Start of time range (default: 24 hours ago).
            end_time: End of time range (default: now).
            limit: Maximum number of models to return.

        Returns:
            List of dicts with model_name and count.
        """
        self._database.initialize()

        if start_time is None:
            start_time = datetime.now(timezone.utc) - timedelta(days=1)
        if end_time is None:
            end_time = datetime.now(timezone.utc)

        with self._database.session() as sess:
            query = (
                select(
                    RequestLog.model_name,
                    func.count().label("count"),
                )
                .where(RequestLog.request_time >= start_time)
                .where(RequestLog.request_time <= end_time)
                .group_by(RequestLog.model_name)
                .order_by(func.count().desc())
                .limit(limit)
            )
            result = sess.execute(query)
            return [{"model_name": row[0], "count": row[1]} for row in result.fetchall()]

    def get_error_rate_by_model(
        self,
        start_time: Optional[datetime] = None,
        end_time: Optional[datetime] = None,
    ) -> list[dict[str, Any]]:
        """Get error rates grouped by model.

        Args:
            start_time: Start of time range (default: 24 hours ago).
            end_time: End of time range (default: now).

        Returns:
            List of dicts with model_name, total_requests, error_count, and error_rate.
        """
        self._database.initialize()

        if start_time is None:
            start_time = datetime.now(timezone.utc) - timedelta(days=1)
        if end_time is None:
            end_time = datetime.now(timezone.utc)

        with self._database.session() as sess:
            # Get total requests per model
            total_query = (
                select(
                    RequestLog.model_name,
                    func.count().label("total"),
                )
                .where(RequestLog.request_time >= start_time)
                .where(RequestLog.request_time <= end_time)
                .group_by(RequestLog.model_name)
            )
            total_results = {row[0]: row[1] for row in sess.execute(total_query).fetchall()}

            # Get errors per model (where outcome is not 'success')
            error_query = (
                select(
                    RequestLog.model_name,
                    func.count().label("errors"),
                )
                .where(RequestLog.request_time >= start_time)
                .where(RequestLog.request_time <= end_time)
                .where(RequestLog.outcome != "success")
                .group_by(RequestLog.model_name)
            )
            error_results = {row[0]: row[1] for row in sess.execute(error_query).fetchall()}

            # Calculate error rates
            results = []
            for model, total in total_results.items():
                errors = error_results.get(model, 0)
                error_rate = errors / total if total > 0 else 0.0
                results.append({
                    "model_name": model,
                    "total_requests": total,
                    "error_count": errors,
                    "error_rate": round(error_rate * 100, 2),  # Percentage
                })

            return sorted(results, key=lambda x: x["error_rate"], reverse=True)

    def get_average_response_time(
        self,
        start_time: Optional[datetime] = None,
        end_time: Optional[datetime] = None,
    ) -> list[dict[str, Any]]:
        """Get average response times grouped by model.

        Args:
            start_time: Start of time range (default: 24 hours ago).
            end_time: End of time range (default: now).

        Returns:
            List of dicts with model_name, avg_duration_ms, min_ms, max_ms.
        """
        self._database.initialize()

        if start_time is None:
            start_time = datetime.now(timezone.utc) - timedelta(days=1)
        if end_time is None:
            end_time = datetime.now(timezone.utc)

        with self._database.session() as sess:
            query = (
                select(
                    RequestLog.model_name,
                    func.avg(RequestLog.duration_ms).label("avg"),
                    func.min(RequestLog.duration_ms).label("min"),
                    func.max(RequestLog.duration_ms).label("max"),
                )
                .where(RequestLog.request_time >= start_time)
                .where(RequestLog.request_time <= end_time)
                .where(RequestLog.duration_ms.isnot(None))
                .group_by(RequestLog.model_name)
                .order_by(func.avg(RequestLog.duration_ms).desc())
            )
            result = sess.execute(query)
            return [
                {
                    "model_name": row[0],
                    "avg_duration_ms": round(row[1], 2) if row[1] else None,
                    "min_ms": row[2],
                    "max_ms": row[3],
                }
                for row in result.fetchall()
            ]

    def get_usage_trends(
        self,
        start_time: Optional[datetime] = None,
        end_time: Optional[datetime] = None,
        interval: str = "hour",
        limit: int = 48,
    ) -> list[dict[str, Any]]:
        """Get usage trends over time.

        Args:
            start_time: Start of time range (default: 24 hours ago).
            end_time: End of time range (default: now).
            interval: Time interval grouping (hour, day, minute).
            limit: Maximum number of intervals to return.

        Returns:
            List of dicts with timestamp and request count.
        """
        self._database.initialize()

        if start_time is None:
            start_time = datetime.now(timezone.utc) - timedelta(days=1)
        if end_time is None:
            end_time = datetime.now(timezone.utc)

        # Truncate timestamp to interval using backend-specific function
        truncated = _truncate_timestamp(RequestLog.request_time, interval)

        with self._database.session() as sess:
            query = (
                select(
                    truncated.label("interval"),
                    func.count().label("count"),
                )
                .where(RequestLog.request_time >= start_time)
                .where(RequestLog.request_time <= end_time)
                .group_by(truncated)
                .order_by(truncated.desc())
                .limit(limit)
            )
            result = sess.execute(query)
            return [
                {
                    "timestamp": _format_timestamp(row[0]),
                    "count": row[1],
                }
                for row in result.fetchall()
            ]

    def get_total_stats(
        self,
        start_time: Optional[datetime] = None,
        end_time: Optional[datetime] = None,
    ) -> dict[str, Any]:
        """Get total statistics for a time range.

        Args:
            start_time: Start of time range (default: 24 hours ago).
            end_time: End of time range (default: now).

        Returns:
            Dictionary with total_requests, successful_requests, failed_requests, avg_duration_ms.
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

            # Successful requests
            success_query = (
                select(func.count())
                .where(RequestLog.request_time >= start_time)
                .where(RequestLog.request_time <= end_time)
                .where(RequestLog.outcome == "success")
            )
            successful = sess.execute(success_query).scalar() or 0

            # Failed requests
            failed = total - successful

            # Average duration
            avg_duration_query = (
                select(func.avg(RequestLog.duration_ms))
                .where(RequestLog.request_time >= start_time)
                .where(RequestLog.request_time <= end_time)
                .where(RequestLog.duration_ms.isnot(None))
            )
            avg_duration = sess.execute(avg_duration_query).scalar()

            return {
                "total_requests": total,
                "successful_requests": successful,
                "failed_requests": failed,
                "success_rate": round((successful / total * 100) if total > 0 else 0, 2),
                "avg_duration_ms": round(avg_duration, 2) if avg_duration else None,
                "start_time": start_time.isoformat(),
                "end_time": end_time.isoformat(),
            }

    def get_token_stats(
        self,
        start_time: Optional[datetime] = None,
        end_time: Optional[datetime] = None,
    ) -> dict[str, Any]:
        """Get aggregated token statistics for a time range.

        Args:
            start_time: Start of time range (default: 24 hours ago).
            end_time: End of time range (default: now).

        Returns:
            Dictionary with total_tokens, prompt_tokens, completion_tokens, etc.
        """
        self._database.initialize()

        if start_time is None:
            start_time = datetime.now(timezone.utc) - timedelta(days=1)
        if end_time is None:
            end_time = datetime.now(timezone.utc)

        with self._database.session() as sess:
            # Get total token counts
            total_query = (
                select(
                    func.sum(_get_json_value('total_tokens')).label("total_tokens"),
                    func.sum(_get_json_value('prompt_tokens')).label("prompt_tokens"),
                    func.sum(_get_json_value('completion_tokens')).label("completion_tokens"),
                    func.sum(_get_json_value('prompt_tokens_details.cached_tokens')).label("cached_tokens"),
                    func.sum(_get_json_value('completion_tokens_details.reasoning_tokens')).label("reasoning_tokens"),
                    func.count().label("request_count"),
                )
                .where(RequestLog.request_time >= start_time)
                .where(RequestLog.request_time <= end_time)
                .where(RequestLog.outcome == "success")
                .where(RequestLog.usage_stats.isnot(None))
            )
            result = sess.execute(total_query).one_or_none()

            if result is None:
                return {
                    "total_tokens": 0,
                    "total_prompt_tokens": 0,
                    "total_completion_tokens": 0,
                    "avg_tokens_per_request": 0,
                    "total_cached_tokens": 0,
                    "total_reasoning_tokens": 0,
                    "start_time": start_time.isoformat(),
                    "end_time": end_time.isoformat(),
                }

            total_tokens = result.total_tokens or 0
            prompt_tokens = result.prompt_tokens or 0
            completion_tokens = result.completion_tokens or 0
            cached_tokens = result.cached_tokens or 0
            reasoning_tokens = result.reasoning_tokens or 0
            request_count = result.request_count or 0

            avg_tokens = round(total_tokens / request_count, 2) if request_count > 0 else 0

            return {
                "total_tokens": total_tokens,
                "total_prompt_tokens": prompt_tokens,
                "total_completion_tokens": completion_tokens,
                "avg_tokens_per_request": avg_tokens,
                "total_cached_tokens": cached_tokens,
                "total_reasoning_tokens": reasoning_tokens,
                "start_time": start_time.isoformat(),
                "end_time": end_time.isoformat(),
            }

    def get_tokens_by_model(
        self,
        start_time: Optional[datetime] = None,
        end_time: Optional[datetime] = None,
        limit: int = 100,
    ) -> list[dict[str, Any]]:
        """Get token counts grouped by model.

        Args:
            start_time: Start of time range (default: 24 hours ago).
            end_time: End of time range (default: now).
            limit: Maximum number of models to return.

        Returns:
            List of dicts with model_name, total_tokens, prompt_tokens, completion_tokens.
        """
        self._database.initialize()

        if start_time is None:
            start_time = datetime.now(timezone.utc) - timedelta(days=1)
        if end_time is None:
            end_time = datetime.now(timezone.utc)

        with self._database.session() as sess:
            query = (
                select(
                    RequestLog.model_name,
                    func.sum(_get_json_value('total_tokens')).label("total_tokens"),
                    func.sum(_get_json_value('prompt_tokens')).label("prompt_tokens"),
                    func.sum(_get_json_value('completion_tokens')).label("completion_tokens"),
                )
                .where(RequestLog.request_time >= start_time)
                .where(RequestLog.request_time <= end_time)
                .where(RequestLog.outcome == "success")
                .where(RequestLog.usage_stats.isnot(None))
                .group_by(RequestLog.model_name)
                .order_by(func.sum(_get_json_value('total_tokens')).desc())
                .limit(limit)
            )
            result = sess.execute(query)
            return [
                {
                    "model_name": row[0] or "Unknown",
                    "total_tokens": row[1] or 0,
                    "prompt_tokens": row[2] or 0,
                    "completion_tokens": row[3] or 0,
                }
                for row in result.fetchall()
            ]

    def get_token_trends(
        self,
        start_time: Optional[datetime] = None,
        end_time: Optional[datetime] = None,
        interval: str = "hour",
        limit: int = 48,
    ) -> list[dict[str, Any]]:
        """Get token usage trends over time.

        Args:
            start_time: Start of time range (default: 48 hours ago).
            end_time: End of time range (default: now).
            interval: Time interval grouping (hour, day, minute).
            limit: Maximum number of intervals to return.

        Returns:
            List of dicts with timestamp and total_tokens.
        """
        self._database.initialize()

        if start_time is None:
            start_time = datetime.now(timezone.utc) - timedelta(days=2)
        if end_time is None:
            end_time = datetime.now(timezone.utc)

        # Truncate timestamp to interval using backend-specific function
        truncated = _truncate_timestamp(RequestLog.request_time, interval)

        with self._database.session() as sess:
            query = (
                select(
                    truncated.label("interval"),
                    func.sum(_get_json_value('total_tokens')).label("total_tokens"),
                )
                .where(RequestLog.request_time >= start_time)
                .where(RequestLog.request_time <= end_time)
                .where(RequestLog.outcome == "success")
                .where(RequestLog.usage_stats.isnot(None))
                .group_by(truncated)
                .order_by(truncated.desc())
                .limit(limit)
            )
            result = sess.execute(query)
            return [
                {
                    "timestamp": _format_timestamp(row[0]),
                    "total_tokens": row[1] or 0,
                }
                for row in result.fetchall()
            ]


# Global repository instance
_usage_repository: Optional[UsageRepository] = None


def get_usage_repository() -> UsageRepository:
    """Get the global usage repository instance.

    Returns:
        The global UsageRepository instance.
    """
    global _usage_repository
    if _usage_repository is None:
        _usage_repository = UsageRepository()
    return _usage_repository
