"""Repository for querying log metadata and retained full payloads."""

from __future__ import annotations

import json
import logging
from datetime import datetime, timedelta, timezone
from typing import Any
from uuid import UUID

from sqlalchemy import and_, func, select

from ..logging.full_request_store import get_full_request_store
from .factory import get_database
from .models import ErrorLog, RequestMetadata

logger = logging.getLogger("yallmp-proxy")

BODY_MAX_CHARS_DEFAULT = 100_000


def _truncate_json_payload(value: Any, max_chars: int) -> tuple[Any, bool, int]:
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
    """Repository for metadata-backed request logs."""

    def __init__(self) -> None:
        self._database = get_database()
        self._full_request_store = get_full_request_store()

    def get_logs(
        self,
        limit: int = 100,
        offset: int = 0,
        model_name: str | None = None,
        outcome: str | None = None,
        stop_reason: str | None = None,
        is_tool_call: bool | None = None,
        start_time: datetime | None = None,
        end_time: datetime | None = None,
        search: str | None = None,
    ) -> dict[str, Any]:
        self._database.initialize()

        query = select(RequestMetadata)
        count_query = select(func.count()).select_from(RequestMetadata)
        conditions = []

        if model_name:
            conditions.append(RequestMetadata.model_name.ilike(f"%{model_name}%"))
        if outcome:
            conditions.append(RequestMetadata.outcome == outcome)
        if stop_reason:
            conditions.append(RequestMetadata.stop_reason == stop_reason)
        if is_tool_call is not None:
            conditions.append(RequestMetadata.is_tool_call == is_tool_call)
        if start_time:
            conditions.append(RequestMetadata.request_time >= start_time)
        if end_time:
            conditions.append(RequestMetadata.request_time <= end_time)

        if search:
            ids = {
                UUID(request_id)
                for request_id in self._full_request_store.search_request_ids(
                    search,
                    start_time=start_time,
                    end_time=end_time,
                )
            }
            if not ids:
                return {
                    "logs": [],
                    "total": 0,
                    "limit": limit,
                    "offset": offset,
                    "has_more": False,
                }
            conditions.append(RequestMetadata.id.in_(ids))

        if conditions:
            where_clause = and_(*conditions)
            query = query.where(where_clause)
            count_query = count_query.where(where_clause)

        with self._database.session() as sess:
            total_count = sess.execute(count_query).scalar() or 0
            result = sess.execute(
                query.order_by(RequestMetadata.request_time.desc()).offset(offset).limit(limit)
            )
            logs = [self._metadata_to_summary_dict(row[0]) for row in result.fetchall()]

        return {
            "logs": logs,
            "total": total_count,
            "limit": limit,
            "offset": offset,
            "has_more": offset + len(logs) < total_count,
        }

    def _metadata_to_summary_dict(self, log: RequestMetadata) -> dict[str, Any]:
        return {
            "id": str(log.id),
            "request_time": log.request_time.isoformat() if log.request_time else None,
            "model_name": log.model_name,
            "is_stream": log.is_stream,
            "path": log.path,
            "method": log.method,
            "query": log.query,
            "usage_stats": log.usage_stats,
            "outcome": log.outcome,
            "duration_ms": log.duration_ms,
            "duration_seconds": log.duration_seconds,
            "request_path": log.request_path,
            "stop_reason": log.stop_reason,
            "is_tool_call": log.is_tool_call,
            "conversation_turn": log.conversation_turn,
            "created_at": log.created_at.isoformat() if log.created_at else None,
        }

    def get_log_by_id(
        self,
        log_id: UUID,
        body_max_chars: int | None = BODY_MAX_CHARS_DEFAULT,
    ) -> dict[str, Any] | None:
        self._database.initialize()

        with self._database.session() as sess:
            log = sess.execute(
                select(RequestMetadata).where(RequestMetadata.id == log_id)
            ).scalar_one_or_none()
            if log is None:
                return None

            log_dict = log.to_dict()
            full_request_status = self._resolve_full_request_status(log)
            log_dict["full_request_status"] = full_request_status

            payload = None
            if full_request_status == "available":
                payload = self._full_request_store.read_payload(log.full_request_path)

            if payload:
                log_dict.update(
                    {
                        "headers": payload.get("headers"),
                        "body": payload.get("body"),
                        "route": payload.get("route"),
                        "backend_attempts": payload.get("backend_attempts"),
                        "stream_chunks": payload.get("stream_chunks"),
                        "errors": payload.get("errors"),
                        "full_response": payload.get("full_response"),
                        "tool_calls": payload.get("tool_calls"),
                        "modules_log": payload.get("modules_log"),
                    }
                )
                self._truncate_large_fields(log_dict, body_max_chars)

            error_logs = sess.execute(
                select(ErrorLog).where(ErrorLog.request_log_id == log_id)
            ).fetchall()
            log_dict["error_logs"] = [row[0].to_dict() for row in error_logs]

            return log_dict

    def _resolve_full_request_status(self, log: RequestMetadata) -> str:
        expires_at = log.full_request_expires_at
        if expires_at is not None and expires_at.tzinfo is None:
            expires_at = expires_at.replace(tzinfo=timezone.utc)
        if log.full_request_path:
            payload = self._full_request_store.read_payload(log.full_request_path)
            if payload is not None:
                payload_expires_at = self._full_request_store.payload_expires_at(payload)
                if payload_expires_at is not None and payload_expires_at <= datetime.now(timezone.utc):
                    return "expired"
                return "available"
            if expires_at and expires_at <= datetime.now(timezone.utc):
                return "expired"
        elif expires_at and expires_at <= datetime.now(timezone.utc):
            return "expired"
        return "missing"

    def _truncate_large_fields(
        self,
        log_dict: dict[str, Any],
        body_max_chars: int | None,
    ) -> None:
        stream_chunks = log_dict.get("stream_chunks")
        if isinstance(stream_chunks, list) and len(stream_chunks) > 50:
            log_dict["stream_chunks"] = stream_chunks[:50]
            log_dict["stream_chunks_truncated"] = True
            log_dict["stream_chunks_total"] = len(stream_chunks)

        if body_max_chars is None:
            body_max_chars = BODY_MAX_CHARS_DEFAULT
        if body_max_chars and body_max_chars > 0:
            body_value, truncated, original_len = _truncate_json_payload(
                log_dict.get("body"),
                body_max_chars,
            )
            if truncated:
                log_dict["body"] = body_value
                log_dict["body_truncated"] = True
                log_dict["body_total_chars"] = original_len
                log_dict["body_preview_chars"] = min(original_len, body_max_chars)

        full_response = log_dict.get("full_response")
        if isinstance(full_response, str) and len(full_response) > 100000:
            log_dict["full_response"] = full_response[:100000]
            log_dict["full_response_truncated"] = True

        backend_attempts = log_dict.get("backend_attempts")
        if isinstance(backend_attempts, list) and len(backend_attempts) > 5:
            log_dict["backend_attempts"] = backend_attempts[:5]
            log_dict["backend_attempts_truncated"] = True

    def get_stop_reason_counts(
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
            total = (
                sess.execute(
                    select(func.count())
                    .where(RequestMetadata.request_time >= start_time)
                    .where(RequestMetadata.request_time <= end_time)
                    .where(RequestMetadata.stop_reason.isnot(None))
                ).scalar()
                or 0
            )
            results = sess.execute(
                select(RequestMetadata.stop_reason, func.count().label("count"))
                .where(RequestMetadata.request_time >= start_time)
                .where(RequestMetadata.request_time <= end_time)
                .where(RequestMetadata.stop_reason.isnot(None))
                .group_by(RequestMetadata.stop_reason)
                .order_by(func.count().desc())
            ).fetchall()

        return [
            {
                "reason": row[0],
                "count": row[1],
                "percentage": round((row[1] / total * 100), 2) if total > 0 else 0,
            }
            for row in results
        ]

    def get_tool_call_rate(
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
            tool_count = (
                sess.execute(
                    select(func.count())
                    .where(RequestMetadata.request_time >= start_time)
                    .where(RequestMetadata.request_time <= end_time)
                    .where(RequestMetadata.is_tool_call.is_(True))
                ).scalar()
                or 0
            )

        return {
            "total_requests": total,
            "tool_call_requests": tool_count,
            "tool_call_rate": round((tool_count / total * 100), 2) if total > 0 else 0,
            "start_time": start_time.isoformat(),
            "end_time": end_time.isoformat(),
        }

    def get_requests_per_model_with_stop_reason(
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
            results = sess.execute(
                select(RequestMetadata.model_name, func.count().label("total"))
                .where(RequestMetadata.request_time >= start_time)
                .where(RequestMetadata.request_time <= end_time)
                .group_by(RequestMetadata.model_name)
                .order_by(func.count().desc())
                .limit(limit)
            ).fetchall()

        models = []
        for row in results:
            models.append(
                {
                    "model_name": row[0],
                    "total_requests": row[1],
                    "stop_reasons": self.get_stop_reason_counts_for_model(
                        row[0],
                        start_time,
                        end_time,
                    ),
                }
            )
        return models

    def get_stop_reason_counts_for_model(
        self,
        model_name: str,
        start_time: datetime | None = None,
        end_time: datetime | None = None,
    ) -> dict[str, int]:
        self._database.initialize()
        if start_time is None:
            start_time = datetime.now(timezone.utc) - timedelta(days=1)
        if end_time is None:
            end_time = datetime.now(timezone.utc)

        with self._database.session() as sess:
            results = sess.execute(
                select(RequestMetadata.stop_reason, func.count().label("count"))
                .where(RequestMetadata.model_name == model_name)
                .where(RequestMetadata.request_time >= start_time)
                .where(RequestMetadata.request_time <= end_time)
                .where(RequestMetadata.stop_reason.isnot(None))
                .group_by(RequestMetadata.stop_reason)
            ).fetchall()
        return {row[0]: row[1] for row in results}


_logs_repository: LogsRepository | None = None


def get_logs_repository() -> LogsRepository:
    global _logs_repository
    if _logs_repository is None:
        _logs_repository = LogsRepository()
    return _logs_repository
