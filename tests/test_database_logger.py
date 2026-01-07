"""Tests for database logger integration."""

import asyncio
import tempfile
from datetime import datetime, timezone
from pathlib import Path
from typing import Any
from unittest.mock import MagicMock, patch
from uuid import UUID

import pytest

# Skip tests if database module is not available
pytest.importorskip("src.database")

from src.database.factory import get_database, reset_database_instance
from src.database.logger import DatabaseLogRecorder, get_db_logger


@pytest.fixture
def sqlite_config() -> dict[str, Any]:
    """Create a SQLite configuration for testing."""
    return {
        "backend": "sqlite",
        "connection": {
            "sqlite": {
                "path": ":memory:",  # In-memory database for testing
            }
        },
        "pool_size": 2,
        "max_overflow": 0,
    }


@pytest.fixture(autouse=True)
def reset_db() -> None:
    """Reset the database instance before each test."""
    reset_database_instance()
    yield
    reset_database_instance()


class TestDatabaseLogRecorder:
    """Tests for DatabaseLogRecorder class."""

    def test_initialize_recorder(self, sqlite_config: dict[str, Any]) -> None:
        """Test recorder initialization."""
        reset_database_instance()
        db = get_database(sqlite_config)
        db.initialize()

        recorder = DatabaseLogRecorder()
        assert recorder._database is not None
        # Note: _initialized is False until we try to log something
        # because the database is lazily initialized
        assert recorder._initialized is False

        # Now call a logging method to trigger initialization
        recorder.log_request(
            model_name="test-model",
            is_stream=False,
            path="/v1/chat/completions",
            method="POST",
            query="",
            headers={},
            body={},
            outcome="success",
        )
        # After logging, it should be initialized
        assert recorder._initialized is True

    def test_log_request(self, sqlite_config: dict[str, Any]) -> None:
        """Test logging a request."""
        reset_database_instance()
        db = get_database(sqlite_config)
        db.initialize()

        recorder = DatabaseLogRecorder()

        request_id = recorder.log_request(
            model_name="test-model",
            is_stream=False,
            path="/v1/chat/completions",
            method="POST",
            query="",
            headers={"content-type": "application/json"},
            body={"messages": [{"role": "user", "content": "Hello"}]},
            outcome="success",
            duration_ms=1500,
        )

        assert request_id is not None
        assert len(request_id) == 36  # UUID string length

    def test_log_request_with_all_fields(self, sqlite_config: dict[str, Any]) -> None:
        """Test logging a request with all fields populated."""
        reset_database_instance()
        db = get_database(sqlite_config)
        db.initialize()

        recorder = DatabaseLogRecorder()

        request_id = recorder.log_request(
            model_name="gpt-4",
            is_stream=True,
            path="/v1/chat/completions",
            method="POST",
            query="",
            headers={"content-type": "application/json", "authorization": "Bearer ***"},
            body={"model": "gpt-4", "messages": [{"role": "user", "content": "Hello"}]},
            route=["gpt-4"],
            backend_attempts=[
                {"backend": "gpt-4", "status": 200, "url": "https://api.openai.com/v1/chat/completions"}
            ],
            stream_chunks=[{"chunk": "data1"}, {"chunk": "data2"}],
            errors=None,
            usage_stats={"prompt_tokens": 10, "completion_tokens": 50, "total_tokens": 60},
            outcome="success",
            duration_ms=2500,
            request_time=datetime.now(timezone.utc),
        )

        assert request_id is not None

        # Verify the data was saved
        with db.session() as session:
            from src.database.models import RequestLog
            from sqlalchemy import select

            result = session.execute(
                select(RequestLog).where(RequestLog.id == UUID(request_id))
            )
            log = result.scalar_one_or_none()
            assert log is not None
            assert log.model_name == "gpt-4"
            assert log.is_stream is True
            assert log.outcome == "success"
            assert log.duration_ms == 2500
            assert len(log.backend_attempts) == 1
            assert len(log.stream_chunks) == 2

    def test_log_error(self, sqlite_config: dict[str, Any]) -> None:
        """Test logging an error."""
        reset_database_instance()
        db = get_database(sqlite_config)
        db.initialize()

        recorder = DatabaseLogRecorder()

        error_id = recorder.log_error(
            model_name="test-model",
            error_type="sse_stream_error",
            error_message="Stream connection failed",
            backend_name="test-backend",
            http_status=500,
            request_path="/v1/chat/completions",
            request_log_id=None,
            extra_context={"retry_count": 3},
        )

        assert error_id is not None
        assert len(error_id) == 36  # UUID string length

        # Verify the error was saved
        with db.session() as session:
            from src.database.models import ErrorLog
            from sqlalchemy import select

            result = session.execute(
                select(ErrorLog).where(ErrorLog.id == UUID(error_id))
            )
            log = result.scalar_one_or_none()
            assert log is not None
            assert log.error_type == "sse_stream_error"
            assert log.error_message == "Stream connection failed"
            assert log.http_status == 500

    def test_log_error_with_request_reference(self, sqlite_config: dict[str, Any]) -> None:
        """Test logging an error with a request reference."""
        reset_database_instance()
        db = get_database(sqlite_config)
        db.initialize()

        recorder = DatabaseLogRecorder()

        # First create a request
        request_id = recorder.log_request(
            model_name="test-model",
            is_stream=False,
            path="/v1/chat/completions",
            method="POST",
            query="",
            headers={},
            body={},
            outcome="error",
        )

        # Then create an error referencing that request
        error_id = recorder.log_error(
            model_name="test-model",
            error_type="timeout",
            error_message="Request timed out",
            request_log_id=request_id,
        )

        # Verify the error references the request
        with db.session() as session:
            from src.database.models import ErrorLog
            from sqlalchemy import select

            result = session.execute(
                select(ErrorLog).where(ErrorLog.id == UUID(error_id))
            )
            log = result.scalar_one_or_none()
            assert log is not None
            assert log.request_log_id is not None
            assert str(log.request_log_id) == request_id


class TestGetDbLogger:
    """Tests for get_db_logger function."""

    def test_get_db_logger_singleton(self, sqlite_config: dict[str, Any]) -> None:
        """Test that get_db_logger returns a singleton."""
        reset_database_instance()
        db = get_database(sqlite_config)
        db.initialize()

        logger1 = get_db_logger()
        logger2 = get_db_logger()

        assert logger1 is logger2

    def test_get_db_logger_without_init(self) -> None:
        """Test get_db_logger without prior initialization."""
        reset_database_instance()

        # This should work but database won't be initialized
        # until we try to log something
        logger = get_db_logger()
        assert logger is not None
        assert logger._initialized is False


class TestDatabaseLoggerIntegration:
    """Integration tests for database logger."""

    def test_multiple_requests_logging(self, sqlite_config: dict[str, Any]) -> None:
        """Test logging multiple requests."""
        reset_database_instance()
        db = get_database(sqlite_config)
        db.initialize()

        recorder = DatabaseLogRecorder()

        # Log multiple requests
        request_ids = []
        for i in range(5):
            request_id = recorder.log_request(
                model_name=f"model-{i}",
                is_stream=False,
                path="/v1/chat/completions",
                method="POST",
                query="",
                headers={},
                body={},
                outcome="success",
            )
            request_ids.append(request_id)

        assert len(request_ids) == 5

        # Verify all requests were saved
        with db.session() as session:
            from src.database.models import RequestLog
            from sqlalchemy import select

            result = session.execute(select(RequestLog))
            logs = result.scalars().all()
            assert len(logs) == 5

    def test_mixed_success_and_errors(self, sqlite_config: dict[str, Any]) -> None:
        """Test logging mixed success and error outcomes."""
        reset_database_instance()
        db = get_database(sqlite_config)
        db.initialize()

        recorder = DatabaseLogRecorder()

        # Log some successful requests
        for i in range(3):
            recorder.log_request(
                model_name="model",
                is_stream=False,
                path="/v1/chat/completions",
                method="POST",
                query="",
                headers={},
                body={},
                outcome="success",
            )

        # Log some errors
        for i in range(2):
            recorder.log_error(
                model_name="model",
                error_type="http_error",
                error_message=f"Error {i}",
            )

        # Verify counts
        with db.session() as session:
            from src.database.models import RequestLog, ErrorLog
            from sqlalchemy import func, select

            request_count = session.execute(
                select(func.count()).select_from(RequestLog)
            ).scalar_one()
            error_count = session.execute(
                select(func.count()).select_from(ErrorLog)
            ).scalar_one()

            assert request_count == 3
            assert error_count == 2


class TestBackgroundTasks:
    """Tests for background task handling."""

    def test_background_task_registration(self, sqlite_config: dict[str, Any]) -> None:
        """Test that background tasks are registered for cleanup."""
        reset_database_instance()
        db = get_database(sqlite_config)
        db.initialize()

        from src.database.logger import _PENDING_DB_TASKS

        async def _run_test() -> None:
            initial_count = len(_PENDING_DB_TASKS)
            unblock = asyncio.Event()

            async def _fake_to_thread(func, /, *args, **kwargs):
                await unblock.wait()
                return func(*args, **kwargs)

            recorder = DatabaseLogRecorder()
            with patch("src.database.logger.asyncio.to_thread", new=_fake_to_thread):
                recorder.log_request(
                    model_name="test-model",
                    is_stream=False,
                    path="/v1/chat/completions",
                    method="POST",
                    query="",
                    headers={},
                    body={},
                    outcome="success",
                )

                await asyncio.sleep(0)
                assert len(_PENDING_DB_TASKS) > initial_count

                unblock.set()
                while _PENDING_DB_TASKS:
                    await asyncio.sleep(0)

        asyncio.run(_run_test())
