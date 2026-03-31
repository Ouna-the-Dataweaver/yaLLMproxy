"""Tests for recorder integration with metadata DB and file payload storage."""

from __future__ import annotations

import json
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

import pytest
from sqlalchemy import select

from src.database.factory import get_database, reset_database_instance
from src.database import logger as db_logger_module
from src.database.models import RequestMetadata
from src.logging import full_request_store as full_request_store_module
from src.logging.recorder import RequestLogRecorder, set_db_logging_enabled


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
    db_logger_module._db_logger = None
    db_logger_module._db_loggers.clear()
    yield
    reset_database_instance()
    db_logger_module._db_logger = None
    db_logger_module._db_loggers.clear()


@pytest.fixture
def storage_root(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> Path:
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


def test_request_log_recorder_writes_metadata_and_payload(
    sqlite_config: dict[str, Any],
    storage_root: Path,
) -> None:
    set_db_logging_enabled(True)
    db = get_database(sqlite_config)
    db.initialize()

    recorder = RequestLogRecorder(
        model_name="test-model",
        is_stream=False,
        path="/v1/chat/completions",
        log_to_disk=False,
    )
    recorder.record_request(
        "POST",
        "",
        {"content-type": "application/json"},
        json.dumps({"messages": [{"role": "user", "content": "hello"}]}).encode("utf-8"),
    )
    recorder.record_backend_attempt("backend-a", 1, "https://example.test/v1/chat/completions")
    recorder.record_usage_stats({"prompt_tokens": 10, "completion_tokens": 20, "total_tokens": 30})
    recorder.record_stop_reason("stop")
    recorder.finalize("success")

    with db.session() as session:
        metadata = session.execute(select(RequestMetadata)).scalar_one()
        assert metadata.model_name == "test-model"
        assert metadata.total_tokens == 30
        assert metadata.full_request_path is not None
        payload_path = Path(metadata.full_request_path)

    assert payload_path.exists()
    payload = json.loads(payload_path.read_text(encoding="utf-8"))
    assert payload["model_name"] == "test-model"
    assert payload["body"]["messages"][0]["content"] == "hello"
    assert payload["backend_attempts"][0]["backend"] == "backend-a"
    assert payload["usage_stats"]["total_tokens"] == 30
    assert payload["expires_at"]
