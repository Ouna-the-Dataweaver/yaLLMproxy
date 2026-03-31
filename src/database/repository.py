"""Repository classes for metadata-backed analytics queries."""

from __future__ import annotations

import logging
from datetime import datetime, timedelta, timezone
from typing import Any

from sqlalchemy import func, select

from .factory import get_current_backend, get_database
from .models import RequestMetadata

logger = logging.getLogger("yallmp-proxy")


def _truncate_timestamp(column, interval: str = "hour"):
    """Generate a database-specific timestamp truncation expression."""
    backend = get_current_backend()

    if backend == "postgres":
        if interval == "hour":
            return func.date_trunc("hour", column)
        if interval == "day":
            return func.date_trunc("day", column)
        if interval == "minute":
            return func.date_trunc("minute", column)
        return func.date_trunc("hour", column)

    if interval == "hour":
        return func.strftime("%Y-%m-%d %H:00:00", column)
    if interval == "day":
        return func.strftime("%Y-%m-%d 00:00:00", column)
    if interval == "minute":
        return func.strftime("%Y-%m-%d %H:%M:00", column)
    return func.strftime("%Y-%m-%d %H:00:00", column)


def _format_timestamp(value: Any) -> str | None:
    if value is None:
        return None
    if isinstance(value, str):
        return value
    return value.isoformat()


class UsageRepository:
    """Repository for querying long-term request metadata."""

    def __init__(self) -> None:
        self._database = get_database()

    def get_requests_per_model(
        self,
        start_time: datetime | None = None,
        end_time: datetime | None = None,
        limit: int = 100,
    ) -> list[dict[str, Any]]:
        self._database.initialize()
        if start_time is None:
            start_time = datetime.now(timezone.utc) - timedelta(days=1)
        if end_time is None:
            end_time = datetime.now(timezone.utc)

        with self._database.session() as sess:
            query = (
                select(RequestMetadata.model_name, func.count().label("count"))
                .where(RequestMetadata.request_time >= start_time)
                .where(RequestMetadata.request_time <= end_time)
                .group_by(RequestMetadata.model_name)
                .order_by(func.count().desc())
                .limit(limit)
            )
            result = sess.execute(query)
            return [{"model_name": row[0], "count": row[1]} for row in result.fetchall()]

    def get_error_rate_by_model(
        self,
        start_time: datetime | None = None,
        end_time: datetime | None = None,
    ) -> list[dict[str, Any]]:
        self._database.initialize()
        if start_time is None:
            start_time = datetime.now(timezone.utc) - timedelta(days=1)
        if end_time is None:
            end_time = datetime.now(timezone.utc)

        with self._database.session() as sess:
            total_query = (
                select(RequestMetadata.model_name, func.count().label("total"))
                .where(RequestMetadata.request_time >= start_time)
                .where(RequestMetadata.request_time <= end_time)
                .group_by(RequestMetadata.model_name)
            )
            totals = {row[0]: row[1] for row in sess.execute(total_query).fetchall()}

            error_query = (
                select(RequestMetadata.model_name, func.count().label("errors"))
                .where(RequestMetadata.request_time >= start_time)
                .where(RequestMetadata.request_time <= end_time)
                .where(RequestMetadata.outcome != "success")
                .group_by(RequestMetadata.model_name)
            )
            errors = {row[0]: row[1] for row in sess.execute(error_query).fetchall()}

        rows = []
        for model_name, total in totals.items():
            error_count = errors.get(model_name, 0)
            error_rate = round((error_count / total * 100) if total > 0 else 0, 2)
            rows.append(
                {
                    "model_name": model_name,
                    "total_requests": total,
                    "error_count": error_count,
                    "error_rate": error_rate,
                }
            )
        return sorted(rows, key=lambda item: item["error_rate"], reverse=True)

    def get_average_response_time(
        self,
        start_time: datetime | None = None,
        end_time: datetime | None = None,
    ) -> list[dict[str, Any]]:
        self._database.initialize()
        if start_time is None:
            start_time = datetime.now(timezone.utc) - timedelta(days=1)
        if end_time is None:
            end_time = datetime.now(timezone.utc)

        with self._database.session() as sess:
            query = (
                select(
                    RequestMetadata.model_name,
                    func.avg(RequestMetadata.duration_ms).label("avg"),
                    func.min(RequestMetadata.duration_ms).label("min"),
                    func.max(RequestMetadata.duration_ms).label("max"),
                )
                .where(RequestMetadata.request_time >= start_time)
                .where(RequestMetadata.request_time <= end_time)
                .where(RequestMetadata.duration_ms.isnot(None))
                .group_by(RequestMetadata.model_name)
                .order_by(func.avg(RequestMetadata.duration_ms).desc())
            )
            result = sess.execute(query)
            return [
                {
                    "model_name": row[0],
                    "avg_duration_ms": round(row[1], 2) if row[1] is not None else None,
                    "min_ms": row[2],
                    "max_ms": row[3],
                }
                for row in result.fetchall()
            ]

    def get_usage_trends(
        self,
        start_time: datetime | None = None,
        end_time: datetime | None = None,
        interval: str = "hour",
        limit: int = 48,
    ) -> list[dict[str, Any]]:
        self._database.initialize()
        if start_time is None:
            start_time = datetime.now(timezone.utc) - timedelta(days=1)
        if end_time is None:
            end_time = datetime.now(timezone.utc)

        truncated = _truncate_timestamp(RequestMetadata.request_time, interval)
        with self._database.session() as sess:
            query = (
                select(truncated.label("interval"), func.count().label("count"))
                .where(RequestMetadata.request_time >= start_time)
                .where(RequestMetadata.request_time <= end_time)
                .group_by(truncated)
                .order_by(truncated.desc())
                .limit(limit)
            )
            result = sess.execute(query)
            return [
                {"timestamp": _format_timestamp(row[0]), "count": row[1]}
                for row in result.fetchall()
            ]

    def get_total_stats(
        self,
        start_time: datetime | None = None,
        end_time: datetime | None = None,
    ) -> dict[str, Any]:
        self._database.initialize()
        if start_time is None:
            start_time = datetime.now(timezone.utc) - timedelta(days=1)
        if end_time is None:
            end_time = datetime.now(timezone.utc)

        with self._database.session() as sess:
            total = (
                sess.execute(
                    select(func.count())
                    .where(RequestMetadata.request_time >= start_time)
                    .where(RequestMetadata.request_time <= end_time)
                ).scalar()
                or 0
            )
            successful = (
                sess.execute(
                    select(func.count())
                    .where(RequestMetadata.request_time >= start_time)
                    .where(RequestMetadata.request_time <= end_time)
                    .where(RequestMetadata.outcome == "success")
                ).scalar()
                or 0
            )
            avg_duration = sess.execute(
                select(func.avg(RequestMetadata.duration_ms))
                .where(RequestMetadata.request_time >= start_time)
                .where(RequestMetadata.request_time <= end_time)
                .where(RequestMetadata.duration_ms.isnot(None))
            ).scalar()

        failed = total - successful
        return {
            "total_requests": total,
            "successful_requests": successful,
            "failed_requests": failed,
            "success_rate": round((successful / total * 100) if total > 0 else 0, 2),
            "avg_duration_ms": round(avg_duration, 2) if avg_duration is not None else None,
            "start_time": start_time.isoformat(),
            "end_time": end_time.isoformat(),
        }

    def get_token_stats(
        self,
        start_time: datetime | None = None,
        end_time: datetime | None = None,
    ) -> dict[str, Any]:
        self._database.initialize()
        if start_time is None:
            start_time = datetime.now(timezone.utc) - timedelta(days=1)
        if end_time is None:
            end_time = datetime.now(timezone.utc)

        with self._database.session() as sess:
            result = sess.execute(
                select(
                    func.sum(RequestMetadata.total_tokens).label("total_tokens"),
                    func.sum(RequestMetadata.prompt_tokens).label("prompt_tokens"),
                    func.sum(RequestMetadata.completion_tokens).label("completion_tokens"),
                    func.sum(RequestMetadata.cached_tokens).label("cached_tokens"),
                    func.sum(RequestMetadata.reasoning_tokens).label("reasoning_tokens"),
                    func.count().label("request_count"),
                )
                .where(RequestMetadata.request_time >= start_time)
                .where(RequestMetadata.request_time <= end_time)
                .where(RequestMetadata.outcome == "success")
                .where(RequestMetadata.total_tokens.isnot(None))
            ).one()

        total_tokens = result.total_tokens or 0
        request_count = result.request_count or 0
        return {
            "total_tokens": total_tokens,
            "total_prompt_tokens": result.prompt_tokens or 0,
            "total_completion_tokens": result.completion_tokens or 0,
            "avg_tokens_per_request": round(total_tokens / request_count, 2) if request_count > 0 else 0,
            "total_cached_tokens": result.cached_tokens or 0,
            "total_reasoning_tokens": result.reasoning_tokens or 0,
            "start_time": start_time.isoformat(),
            "end_time": end_time.isoformat(),
        }

    def get_tokens_by_model(
        self,
        start_time: datetime | None = None,
        end_time: datetime | None = None,
        limit: int = 100,
    ) -> list[dict[str, Any]]:
        self._database.initialize()
        if start_time is None:
            start_time = datetime.now(timezone.utc) - timedelta(days=1)
        if end_time is None:
            end_time = datetime.now(timezone.utc)

        with self._database.session() as sess:
            query = (
                select(
                    RequestMetadata.model_name,
                    func.sum(RequestMetadata.total_tokens).label("total_tokens"),
                    func.sum(RequestMetadata.prompt_tokens).label("prompt_tokens"),
                    func.sum(RequestMetadata.completion_tokens).label("completion_tokens"),
                )
                .where(RequestMetadata.request_time >= start_time)
                .where(RequestMetadata.request_time <= end_time)
                .where(RequestMetadata.outcome == "success")
                .where(RequestMetadata.total_tokens.isnot(None))
                .group_by(RequestMetadata.model_name)
                .order_by(func.sum(RequestMetadata.total_tokens).desc())
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
        start_time: datetime | None = None,
        end_time: datetime | None = None,
        interval: str = "hour",
        limit: int = 48,
    ) -> list[dict[str, Any]]:
        self._database.initialize()
        if start_time is None:
            start_time = datetime.now(timezone.utc) - timedelta(days=2)
        if end_time is None:
            end_time = datetime.now(timezone.utc)

        truncated = _truncate_timestamp(RequestMetadata.request_time, interval)
        with self._database.session() as sess:
            query = (
                select(
                    truncated.label("interval"),
                    func.sum(RequestMetadata.total_tokens).label("total_tokens"),
                )
                .where(RequestMetadata.request_time >= start_time)
                .where(RequestMetadata.request_time <= end_time)
                .where(RequestMetadata.outcome == "success")
                .where(RequestMetadata.total_tokens.isnot(None))
                .group_by(truncated)
                .order_by(truncated.desc())
                .limit(limit)
            )
            result = sess.execute(query)
            return [
                {"timestamp": _format_timestamp(row[0]), "total_tokens": row[1] or 0}
                for row in result.fetchall()
            ]

    def get_avg_tps_by_model(
        self,
        start_time: datetime | None = None,
        end_time: datetime | None = None,
        limit: int = 100,
    ) -> list[dict[str, Any]]:
        self._database.initialize()
        if start_time is None:
            start_time = datetime.now(timezone.utc) - timedelta(days=1)
        if end_time is None:
            end_time = datetime.now(timezone.utc)

        with self._database.session() as sess:
            query = (
                select(
                    RequestMetadata.model_name,
                    func.sum(RequestMetadata.tokens_per_second * RequestMetadata.completion_tokens).label("weighted_tps_sum"),
                    func.sum(RequestMetadata.completion_tokens).label("total_completion_tokens"),
                    func.count().label("request_count"),
                )
                .where(RequestMetadata.request_time >= start_time)
                .where(RequestMetadata.request_time <= end_time)
                .where(RequestMetadata.outcome == "success")
                .where(RequestMetadata.tokens_per_second.isnot(None))
                .where(RequestMetadata.tokens_per_second > 0)
                .where(RequestMetadata.completion_tokens.isnot(None))
                .group_by(RequestMetadata.model_name)
                .order_by(func.sum(RequestMetadata.completion_tokens).desc())
                .limit(limit)
            )
            rows = sess.execute(query).fetchall()

        results = []
        for row in rows:
            total_completion = row.total_completion_tokens or 0
            avg_tps = (row.weighted_tps_sum or 0) / total_completion if total_completion > 0 else 0.0
            results.append(
                {
                    "model_name": row.model_name or "Unknown",
                    "avg_tps": round(avg_tps, 2),
                    "total_completion_tokens": total_completion,
                    "request_count": row.request_count or 0,
                }
            )
        return results

    def get_tps_stats(
        self,
        start_time: datetime | None = None,
        end_time: datetime | None = None,
    ) -> dict[str, Any]:
        self._database.initialize()
        if start_time is None:
            start_time = datetime.now(timezone.utc) - timedelta(days=1)
        if end_time is None:
            end_time = datetime.now(timezone.utc)

        with self._database.session() as sess:
            result = sess.execute(
                select(
                    func.sum(RequestMetadata.tokens_per_second * RequestMetadata.completion_tokens).label("weighted_tps_sum"),
                    func.sum(RequestMetadata.completion_tokens).label("total_completion_tokens"),
                    func.min(RequestMetadata.tokens_per_second).label("min_tps"),
                    func.max(RequestMetadata.tokens_per_second).label("max_tps"),
                    func.count().label("request_count"),
                )
                .where(RequestMetadata.request_time >= start_time)
                .where(RequestMetadata.request_time <= end_time)
                .where(RequestMetadata.outcome == "success")
                .where(RequestMetadata.tokens_per_second.isnot(None))
                .where(RequestMetadata.tokens_per_second > 0)
                .where(RequestMetadata.completion_tokens.isnot(None))
            ).one()

        request_count = result.request_count or 0
        if request_count == 0:
            return {
                "overall_avg_tps": None,
                "min_tps": None,
                "max_tps": None,
                "request_count": 0,
            }

        total_completion = result.total_completion_tokens or 0
        avg_tps = (result.weighted_tps_sum or 0) / total_completion if total_completion > 0 else 0.0
        return {
            "overall_avg_tps": round(avg_tps, 2),
            "min_tps": round(result.min_tps, 2) if result.min_tps is not None else None,
            "max_tps": round(result.max_tps, 2) if result.max_tps is not None else None,
            "request_count": request_count,
        }


_usage_repository: UsageRepository | None = None


def get_usage_repository() -> UsageRepository:
    global _usage_repository
    if _usage_repository is None:
        _usage_repository = UsageRepository()
    return _usage_repository
