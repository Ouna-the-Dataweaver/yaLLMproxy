"""The master switch must suppress every persistent request/error log sink."""

from __future__ import annotations

import asyncio
import json
import os
import subprocess
import sys
from pathlib import Path
from unittest.mock import Mock

import pytest

from src.logging import recorder
from src.logging.full_request_store import (
    CONFIG_STORE,
    FullRequestStore,
    get_full_request_storage_settings,
    is_logging_enabled,
)


@pytest.fixture
def disabled_logging(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> Mock:
    config = {
        "proxy_settings": {
            "logging": {
                "enabled": False,
                "full_request_storage": {
                    "enabled": True,
                    "path": str(tmp_path / "requests"),
                },
            }
        }
    }
    monkeypatch.setattr(CONFIG_STORE, "get_runtime_config", lambda: config)
    monkeypatch.setattr(recorder, "REQUEST_LOG_DIR", tmp_path / "requests")
    monkeypatch.setattr(recorder, "ERROR_LOG_DIR", tmp_path / "errors")
    monkeypatch.setattr(recorder, "_DB_LOGGING_ENABLED", True)
    database = Mock(side_effect=AssertionError("Database logging must stay disabled"))
    monkeypatch.setattr(recorder, "get_db_logger", database)
    store = FullRequestStore(get_full_request_storage_settings(config))
    monkeypatch.setattr(recorder, "get_full_request_store", lambda: store)
    return database


def _record_request(stream: bool, outcome: str) -> recorder.RequestLogRecorder:
    log = recorder.RequestLogRecorder(
        "test",
        stream,
        "/v1/chat/completions",
        log_to_disk=True,
        log_parsed_response=True,
        log_parsed_stream=True,
    )
    log.record_response_content("response")
    if stream:
        log.record_stream_chunk(b'data: {"choices": []}\n\n')
        log.record_parsed_stream_chunk(b'data: {"choices": []}\n\n')
    else:
        log.record_parsed_response(200, {}, b'{"response": "test"}')
    if outcome == "error":
        log.record_error("synthetic error")
        recorder.log_error_event("test", "test_error", "synthetic error")
    log.finalize(outcome)
    log.finalize(outcome)
    return log


@pytest.mark.parametrize("stream", [False, True])
@pytest.mark.parametrize("outcome", ["success", "error"])
def test_disabled_logging_writes_nothing(
    tmp_path: Path, disabled_logging: Mock, stream: bool, outcome: str
) -> None:
    log = _record_request(stream, outcome)
    assert log.finalized
    assert list(tmp_path.rglob("*")) == []
    disabled_logging.assert_not_called()


@pytest.mark.asyncio
@pytest.mark.parametrize("stream", [False, True])
@pytest.mark.parametrize("outcome", ["success", "error"])
async def test_disabled_logging_schedules_no_background_writes(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    disabled_logging: Mock,
    stream: bool,
    outcome: str,
) -> None:
    register_task = Mock()
    monkeypatch.setattr(recorder, "_register_background_task", register_task)
    log = _record_request(stream, outcome)
    await asyncio.sleep(0)
    assert log.finalized
    register_task.assert_not_called()
    disabled_logging.assert_not_called()
    assert list(tmp_path.rglob("*")) == []


@pytest.mark.parametrize(
    "value, expected",
    [(False, False), ("false", False), (True, True), ("true", True), (None, True)],
)
def test_master_switch_default_and_values(
    value: bool | str | None, expected: bool
) -> None:
    config = {"proxy_settings": {"logging": {"enabled": value}}}
    assert get_full_request_storage_settings(config).enabled is expected


@pytest.mark.parametrize("master_enabled", [False, True])
@pytest.mark.parametrize("storage_enabled", [False, True])
def test_master_and_storage_switches(
    master_enabled: bool, storage_enabled: bool
) -> None:
    config = {
        "proxy_settings": {
            "logging": {
                "enabled": master_enabled,
                "full_request_storage": {"enabled": storage_enabled},
            }
        }
    }
    assert get_full_request_storage_settings(config).enabled is (
        master_enabled and storage_enabled
    )


@pytest.mark.parametrize("explicit_enabled", [False, True])
def test_enabled_logging_preserves_request_and_error_files(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    explicit_enabled: bool,
) -> None:
    logging_config = {"full_request_storage": {"path": str(tmp_path / "requests")}}
    if explicit_enabled:
        logging_config["enabled"] = True
    config = {"proxy_settings": {"logging": logging_config}}
    monkeypatch.setattr(CONFIG_STORE, "get_runtime_config", lambda: config)
    monkeypatch.setattr(recorder, "REQUEST_LOG_DIR", tmp_path / "requests")
    monkeypatch.setattr(recorder, "ERROR_LOG_DIR", tmp_path / "errors")
    store = FullRequestStore(get_full_request_storage_settings(config))
    monkeypatch.setattr(recorder, "get_full_request_store", lambda: store)

    log = _record_request(False, "error")

    assert log.finalized
    assert log.log_path.is_file()
    assert log.parsed_log_path.is_file()
    payload = json.loads(log.log_path.with_suffix(".json").read_text())
    assert payload["outcome"] == "error"
    error_files = list((tmp_path / "errors").glob("*.err"))
    assert error_files
    assert "synthetic error" in error_files[0].read_text()


def test_explicit_empty_config_keeps_logging_enabled(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.setattr(
        CONFIG_STORE,
        "get_runtime_config",
        lambda: {"proxy_settings": {"logging": {"enabled": False}}},
    )
    assert is_logging_enabled({})


def test_disabled_logging_overrides_debug_at_app_initialization(tmp_path: Path) -> None:
    config_path = tmp_path / "config.yaml"
    config_path.write_text(
        json.dumps(
            {
                "model_list": [
                    {
                        "model_name": "test",
                        "model_params": {
                            "model": "openai/test",
                            "api_base": "http://upstream.local/v1",
                            "api_key": "test-key",
                        },
                    }
                ],
                "proxy_settings": {"debug": True, "logging": {"enabled": False}},
            }
        )
    )
    result = subprocess.run(
        [
            sys.executable,
            "-c",
            (
                "import logging; import src.main; "
                "assert not logging.getLogger('yallmp-proxy').isEnabledFor(logging.CRITICAL); "
                "assert not any(isinstance(h, logging.FileHandler) "
                "for h in logging.getLogger('yallmp-proxy').handlers)"
            ),
        ],
        cwd=tmp_path,
        env={**os.environ, "YALLMP_CONFIG": str(config_path)},
        capture_output=True,
        text=True,
        timeout=30,
    )
    assert result.returncode == 0, result.stdout + result.stderr
    assert not (tmp_path / "logs" / "console.log").exists()
