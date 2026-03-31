"""Tests for metadata-backed logs repository functionality."""

from __future__ import annotations

from datetime import datetime, timedelta, timezone
from pathlib import Path
from typing import Any
from uuid import UUID, uuid4

import pytest

pytest.importorskip("src.database")

from src.database.factory import get_database, reset_database_instance
from src.database.logs_repository import BODY_MAX_CHARS_DEFAULT, LogsRepository
from src.database.models import ErrorLog, RequestMetadata
from src.logging import full_request_store as full_request_store_module


@pytest.fixture
def sqlite_config() -> dict[str, Any]:
    return {
        "backend": "sqlite",
        "connection": {"sqlite": {"path": ":memory:"}},
        "pool_size": 2,
        "max_overflow": 0,
    }


@pytest.fixture(autouse=True)
def reset_db() -> None:
    reset_database_instance()
    yield
    reset_database_instance()


@pytest.fixture
def storage_config(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> Path:
    requests_root = tmp_path / "requests"
    runtime_config = {
        "proxy_settings": {
            "logging": {
                "full_request_storage": {
                    "enabled": True,
                    "path": str(requests_root),
                    "retention_hours": 48,
                    "cleanup_on_startup": True,
                    "cleanup_interval_hours": 24,
                }
            }
        }
    }
    monkeypatch.setattr(
        full_request_store_module.CONFIG_STORE,
        "get_runtime_config",
        lambda: runtime_config,
    )
    full_request_store_module._FULL_REQUEST_STORE = None
    full_request_store_module._FULL_REQUEST_STORE_KEY = None
    return requests_root


def _create_repo(sqlite_config: dict[str, Any]) -> LogsRepository:
    db = get_database(sqlite_config)
    db.initialize()
    return LogsRepository()


def _create_metadata(
    repo: LogsRepository,
    *,
    request_id: str | None = None,
    model_name: str = "test-model",
    outcome: str = "success",
    request_time: datetime | None = None,
    full_request_path: str | None = None,
    full_request_expires_at: datetime | None = None,
    usage_tokens: tuple[int, int, int] | None = None,
    is_tool_call: bool = False,
    stop_reason: str | None = None,
) -> RequestMetadata:
    request_time = request_time or datetime.now(timezone.utc)
    prompt_tokens = completion_tokens = total_tokens = None
    if usage_tokens:
        prompt_tokens, completion_tokens, total_tokens = usage_tokens

    with repo._database.session() as session:
        metadata_id = UUID(request_id) if request_id else None
        log = RequestMetadata(
            id=metadata_id,
            request_time=request_time,
            model_name=model_name,
            outcome=outcome,
            is_stream=False,
            path="/v1/chat/completions",
            method="POST",
            duration_ms=1500,
            prompt_tokens=prompt_tokens,
            completion_tokens=completion_tokens,
            total_tokens=total_tokens,
            is_tool_call=is_tool_call,
            stop_reason=stop_reason,
            full_request_path=full_request_path,
            full_request_expires_at=full_request_expires_at,
        )
        session.add(log)
        session.flush()
        session.expunge(log)
        return log


class TestLogsRepository:
    def test_get_logs_pagination_and_filters(
        self,
        sqlite_config: dict[str, Any],
        storage_config: Path,
    ) -> None:
        repo = _create_repo(sqlite_config)

        _create_metadata(repo, model_name="gpt-4", outcome="success")
        _create_metadata(repo, model_name="gpt-4", outcome="success")
        _create_metadata(repo, model_name="claude", outcome="error")

        result = repo.get_logs(limit=2, offset=0)
        assert len(result["logs"]) == 2
        assert result["total"] == 3
        assert result["has_more"] is True

        filtered = repo.get_logs(model_name="gpt-4")
        assert filtered["total"] == 2

        failed = repo.get_logs(outcome="error")
        assert failed["total"] == 1

    def test_get_log_by_id_reads_payload_file(
        self,
        sqlite_config: dict[str, Any],
        storage_config: Path,
    ) -> None:
        repo = _create_repo(sqlite_config)
        store = full_request_store_module.get_full_request_store()

        request_time = datetime.now(timezone.utc)
        request_id = uuid4()
        payload_path, expires_at = store.write_payload(
            request_id,
            request_time,
            {
                "id": str(request_id),
                "request_time": request_time.isoformat(),
                "headers": {"content-type": "application/json"},
                "body": {"messages": [{"role": "user", "content": "hello"}]},
                "backend_attempts": [{"backend": "test", "status": 200}],
                "stream_chunks": [{"delta": "one"} for _ in range(80)],
                "errors": [{"type": "timeout", "message": "oops"}],
                "full_response": "x" * 120000,
                "tool_calls": [{"name": "tool"}],
                "modules_log": {"total_events": 2},
                "usage_stats": {"prompt_tokens": 10, "completion_tokens": 20, "total_tokens": 30},
            },
        )
        assert payload_path is not None
        log = _create_metadata(
            repo,
            request_time=request_time,
            full_request_path=str(payload_path),
            full_request_expires_at=expires_at,
            usage_tokens=(10, 20, 30),
        )

        result = repo.get_log_by_id(log.id)
        assert result is not None
        assert result["full_request_status"] == "available"
        assert result["body"]["messages"][0]["content"] == "hello"
        assert result["backend_attempts"][0]["backend"] == "test"
        assert result["errors"][0]["type"] == "timeout"
        assert result["tool_calls"][0]["name"] == "tool"
        assert result["stream_chunks_truncated"] is True
        assert len(result["stream_chunks"]) == 50
        assert result["full_response_truncated"] is True
        assert len(result["full_response"]) == 100000

    def test_get_log_by_id_marks_expired_payload(
        self,
        sqlite_config: dict[str, Any],
        storage_config: Path,
    ) -> None:
        repo = _create_repo(sqlite_config)
        log = _create_metadata(
            repo,
            full_request_path=str(storage_config / "missing.json"),
            full_request_expires_at=datetime.now(timezone.utc) - timedelta(hours=1),
            usage_tokens=(10, 20, 30),
        )

        result = repo.get_log_by_id(log.id)
        assert result is not None
        assert result["full_request_status"] == "expired"
        assert result["usage_stats"]["total_tokens"] == 30
        assert "body" not in result

    def test_get_log_by_id_includes_linked_error_logs(
        self,
        sqlite_config: dict[str, Any],
        storage_config: Path,
    ) -> None:
        repo = _create_repo(sqlite_config)
        log = _create_metadata(repo, outcome="error")

        with repo._database.session() as session:
            session.add(
                ErrorLog(
                    timestamp=datetime.now(timezone.utc),
                    model_name="test-model",
                    error_type="timeout",
                    error_message="Timed out",
                    request_log_id=log.id,
                )
            )

        result = repo.get_log_by_id(log.id)
        assert result is not None
        assert len(result["error_logs"]) == 1
        assert result["error_logs"][0]["error_type"] == "timeout"

    def test_search_only_matches_retained_payloads(
        self,
        sqlite_config: dict[str, Any],
        storage_config: Path,
    ) -> None:
        repo = _create_repo(sqlite_config)
        store = full_request_store_module.get_full_request_store()

        request_time = datetime.now(timezone.utc)
        retained_id = uuid4()
        retained_path, retained_expires = store.write_payload(
            retained_id,
            request_time,
            {
                "id": str(retained_id),
                "request_time": request_time.isoformat(),
                "body": {"messages": [{"content": "find-me"}]},
            },
        )
        _create_metadata(
            repo,
            request_id=str(retained_id),
            request_time=request_time,
            full_request_path=str(retained_path),
            full_request_expires_at=retained_expires,
        )

        expired_log = _create_metadata(
            repo,
            request_time=request_time - timedelta(days=3),
            full_request_path=str(storage_config / "expired.json"),
            full_request_expires_at=datetime.now(timezone.utc) - timedelta(hours=1),
        )

        result = repo.get_logs(search="find-me")
        assert result["total"] == 1
        assert result["logs"][0]["id"] != str(expired_log.id)

    def test_analytics_use_metadata_rows(
        self,
        sqlite_config: dict[str, Any],
        storage_config: Path,
    ) -> None:
        repo = _create_repo(sqlite_config)

        for _ in range(3):
            _create_metadata(repo, model_name="model-a", stop_reason="stop", is_tool_call=False)
        for _ in range(2):
            _create_metadata(repo, model_name="model-b", stop_reason="tool_calls", is_tool_call=True)

        stop_reasons = repo.get_stop_reason_counts()
        assert {item["reason"] for item in stop_reasons} == {"stop", "tool_calls"}

        tool_rate = repo.get_tool_call_rate()
        assert tool_rate["total_requests"] == 5
        assert tool_rate["tool_call_requests"] == 2


def test_cleanup_deletes_expired_payloads_and_sidecars(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    requests_root = tmp_path / "requests"
    runtime_config = {
        "proxy_settings": {
            "logging": {
                "full_request_storage": {
                    "enabled": True,
                    "path": str(requests_root),
                    "retention_hours": 1,
                    "cleanup_on_startup": True,
                    "cleanup_interval_hours": 24,
                }
            }
        }
    }
    monkeypatch.setattr(
        full_request_store_module.CONFIG_STORE,
        "get_runtime_config",
        lambda: runtime_config,
    )
    full_request_store_module._FULL_REQUEST_STORE = None
    full_request_store_module._FULL_REQUEST_STORE_KEY = None
    store = full_request_store_module.get_full_request_store()

    request_time = datetime.now(timezone.utc) - timedelta(hours=2)
    request_id = uuid4()
    payload_path, _ = store.write_payload(
        request_id,
        request_time,
        {"id": str(request_id), "request_time": request_time.isoformat()},
    )
    assert payload_path is not None
    payload_path.with_suffix(".log").write_text("log", encoding="utf-8")
    payload_path.with_suffix(".parsed.log").write_text("parsed", encoding="utf-8")

    deleted = store.cleanup_expired(now=datetime.now(timezone.utc))
    assert deleted == 1
    assert not payload_path.exists()
    assert not payload_path.with_suffix(".log").exists()
    assert not payload_path.with_suffix(".parsed.log").exists()
