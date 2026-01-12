"""Tests for logs repository functionality."""

import sys
from datetime import datetime, timezone
from pathlib import Path
from typing import Any
from uuid import UUID, uuid4

import pytest

# Add src directory to path for imports
sys.path.insert(0, str(Path(__file__).parent / "src"))

# Skip tests if database module is not available
pytest.importorskip("src.database")

from src.database.base import Base
from src.database.factory import get_database, reset_database_instance
from src.database.logs_repository import BODY_MAX_CHARS_DEFAULT, LogsRepository
from src.database.models import RequestLog, ErrorLog


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
    """Reset the database instances before each test."""
    reset_database_instance()
    yield
    reset_database_instance()


class TestLogsRepository:
    """Tests for LogsRepository class."""

    def _create_repo(self, sqlite_config: dict[str, Any]) -> LogsRepository:
        """Create a LogsRepository with the test database."""
        db = get_database(sqlite_config)
        db.initialize()
        repo = LogsRepository()
        return repo

    def test_get_logs_pagination(self, sqlite_config: dict[str, Any]) -> None:
        """Test getting paginated logs."""
        repo = self._create_repo(sqlite_config)

        # Create some logs
        with repo._database.session() as session:
            for i in range(10):
                log = RequestLog(
                    request_time=datetime.now(timezone.utc),
                    model_name=f"model-{i}",
                    outcome="success",
                    duration_ms=1000,
                )
                session.add(log)

        # Get first page
        result = repo.get_logs(limit=5, offset=0)
        assert result["logs"] is not None
        assert len(result["logs"]) == 5
        assert result["total"] == 10
        assert result["limit"] == 5
        assert result["offset"] == 0
        assert result["has_more"] is True

        # Get second page
        result = repo.get_logs(limit=5, offset=5)
        assert len(result["logs"]) == 5
        assert result["offset"] == 5
        assert result["has_more"] is False

    def test_get_logs_with_filters(self, sqlite_config: dict[str, Any]) -> None:
        """Test getting logs with filters."""
        repo = self._create_repo(sqlite_config)

        # Create logs with different models and outcomes
        with repo._database.session() as session:
            for i in range(5):
                log = RequestLog(
                    request_time=datetime.now(timezone.utc),
                    model_name="gpt-4",
                    outcome="success",
                )
                session.add(log)
            for i in range(3):
                log = RequestLog(
                    request_time=datetime.now(timezone.utc),
                    model_name="claude-3",
                    outcome="error",
                )
                session.add(log)

        # Filter by model
        result = repo.get_logs(model_name="gpt-4")
        assert result["total"] == 5

        # Filter by outcome
        result = repo.get_logs(outcome="error")
        assert result["total"] == 3

        # Filter by both
        result = repo.get_logs(model_name="gpt-4", outcome="error")
        assert result["total"] == 0


class TestGetLogById:
    """Tests for get_log_by_id method."""

    def _create_repo(self, sqlite_config: dict[str, Any]) -> LogsRepository:
        """Create a LogsRepository with the test database."""
        db = get_database(sqlite_config)
        db.initialize()
        repo = LogsRepository()
        return repo

    def test_get_log_by_id_basic(self, sqlite_config: dict[str, Any]) -> None:
        """Test getting a log by ID."""
        repo = self._create_repo(sqlite_config)

        # Create a log
        with repo._database.session() as session:
            log = RequestLog(
                request_time=datetime.now(timezone.utc),
                model_name="test-model",
                outcome="success",
                duration_ms=1500,
                body={"messages": [{"role": "user", "content": "Hello"}]},
            )
            session.add(log)
            session.flush()
            log_id = log.id

        # Retrieve the log
        result = repo.get_log_by_id(log_id)

        assert result is not None
        assert result["model_name"] == "test-model"
        assert result["outcome"] == "success"
        assert result["duration_ms"] == 1500
        assert result["body"] == {"messages": [{"role": "user", "content": "Hello"}]}

    def test_get_log_by_id_not_found(self, sqlite_config: dict[str, Any]) -> None:
        """Test getting a non-existent log."""
        repo = self._create_repo(sqlite_config)

        result = repo.get_log_by_id(uuid4())
        assert result is None

    def test_get_log_by_id_with_large_stream_chunks(self, sqlite_config: dict[str, Any]) -> None:
        """Test that get_log_by_id truncates large stream_chunks to prevent timeouts.

        This test verifies that logs with many stream chunks don't cause
        performance issues by truncating to the first 50 chunks.
        """
        repo = self._create_repo(sqlite_config)

        # Create a log with many stream chunks (simulating a real streaming response)
        large_stream_chunks = [
            {"id": f"chunk-{i}", "choices": [{"delta": {"content": f"This is chunk {i} "}}]}
            for i in range(1000)  # 1000 chunks is not unrealistic for long responses
        ]

        with repo._database.session() as session:
            log = RequestLog(
                request_time=datetime.now(timezone.utc),
                model_name="test-model",
                outcome="success",
                is_stream=True,
                stream_chunks=large_stream_chunks,
            )
            session.add(log)
            session.flush()
            log_id = log.id

        # Retrieve the log - should be truncated to 50 chunks
        result = repo.get_log_by_id(log_id)

        assert result is not None
        assert result["is_stream"] is True
        assert result["stream_chunks"] is not None
        # Should be truncated to 50
        assert len(result["stream_chunks"]) == 50
        # Should indicate truncation
        assert result.get("stream_chunks_truncated") is True
        assert result.get("stream_chunks_total") == 1000

    def test_get_log_by_id_with_large_full_response(self, sqlite_config: dict[str, Any]) -> None:
        """Test that get_log_by_id truncates very large full_response to prevent timeouts.

        This test verifies that logs with huge response text don't cause
        performance issues by truncating to 100KB.
        """
        repo = self._create_repo(sqlite_config)

        # Create a log with a very large full_response
        large_response = "This is a very long response. " * 10000  # ~260KB of text

        with repo._database.session() as session:
            log = RequestLog(
                request_time=datetime.now(timezone.utc),
                model_name="test-model",
                outcome="success",
                full_response=large_response,
            )
            session.add(log)
            session.flush()
            log_id = log.id

        # Retrieve the log - should be truncated to 100KB
        result = repo.get_log_by_id(log_id)

        assert result is not None
        # Should be truncated
        assert len(result["full_response"]) == 100000
        assert result.get("full_response_truncated") is True

    def test_get_log_by_id_with_large_body(self, sqlite_config: dict[str, Any]) -> None:
        """Test that get_log_by_id truncates very large bodies by default."""
        repo = self._create_repo(sqlite_config)

        # Create a log with a large request body (long conversation history)
        large_content = "x" * 2000
        large_body = {
            "model": "test-model",
            "messages": [
                {"role": "user" if i % 2 == 0 else "assistant", "content": large_content}
                for i in range(120)
            ],
            "max_tokens": 4000,
            "temperature": 0.7,
        }

        with repo._database.session() as session:
            log = RequestLog(
                request_time=datetime.now(timezone.utc),
                model_name="test-model",
                outcome="success",
                body=large_body,
            )
            session.add(log)
            session.flush()
            log_id = log.id

        # Retrieve the log (default truncation)
        result = repo.get_log_by_id(log_id)

        assert result is not None
        assert result.get("body_truncated") is True
        assert isinstance(result["body"], str)
        assert len(result["body"]) == BODY_MAX_CHARS_DEFAULT
        assert result.get("body_total_chars", 0) > BODY_MAX_CHARS_DEFAULT

    def test_get_log_by_id_with_large_body_no_limit(self, sqlite_config: dict[str, Any]) -> None:
        """Test that get_log_by_id can return the full body when truncation is disabled."""
        repo = self._create_repo(sqlite_config)

        large_content = "x" * 2000
        large_body = {
            "model": "test-model",
            "messages": [
                {"role": "user" if i % 2 == 0 else "assistant", "content": large_content}
                for i in range(120)
            ],
            "max_tokens": 4000,
            "temperature": 0.7,
        }

        with repo._database.session() as session:
            log = RequestLog(
                request_time=datetime.now(timezone.utc),
                model_name="test-model",
                outcome="success",
                body=large_body,
            )
            session.add(log)
            session.flush()
            log_id = log.id

        result = repo.get_log_by_id(log_id, body_max_chars=0)

        assert result is not None
        assert result["body"] == large_body
        assert result.get("body_truncated") is None

    def test_get_log_by_id_with_large_backend_attempts(self, sqlite_config: dict[str, Any]) -> None:
        """Test that get_log_by_id truncates large backend_attempts to prevent timeouts.

        Backend attempts with full responses can be very large.
        """
        repo = self._create_repo(sqlite_config)

        # Create a log with many backend attempts
        large_backend_attempts = [
            {
                "backend": f"backend-{i}",
                "status": 200,
                "url": "https://api.example.com/v1/chat/completions",
                "response": {"choices": [{"message": {"content": f"Response {i}"}}]}
            }
            for i in range(20)  # 20 attempts is a lot
        ]

        with repo._database.session() as session:
            log = RequestLog(
                request_time=datetime.now(timezone.utc),
                model_name="test-model",
                outcome="success",
                backend_attempts=large_backend_attempts,
            )
            session.add(log)
            session.flush()
            log_id = log.id

        # Retrieve the log - should be truncated to 5
        result = repo.get_log_by_id(log_id)

        assert result is not None
        # Should be truncated to 5
        assert len(result["backend_attempts"]) == 5
        assert result.get("backend_attempts_truncated") is True

    def test_get_log_by_id_includes_linked_error_logs(self, sqlite_config: dict[str, Any]) -> None:
        """Test that get_log_by_id returns linked error logs."""
        repo = self._create_repo(sqlite_config)

        # Create a request log with linked error logs
        with repo._database.session() as session:
            request_log = RequestLog(
                request_time=datetime.now(timezone.utc),
                model_name="test-model",
                outcome="error",
            )
            session.add(request_log)
            session.flush()

            # Create error logs linked to this request
            for i in range(3):
                error_log = ErrorLog(
                    timestamp=datetime.now(timezone.utc),
                    model_name="test-model",
                    error_type="timeout",
                    error_message=f"Timeout {i}",
                    request_log_id=request_log.id,
                )
                session.add(error_log)

            log_id = request_log.id

        # Retrieve the log
        result = repo.get_log_by_id(log_id)

        assert result is not None
        assert "error_logs" in result
        assert len(result["error_logs"]) == 3

        # Verify the error logs have the expected structure
        for i, error_log in enumerate(result["error_logs"]):
            assert error_log["error_type"] == "timeout"
            assert error_log["error_message"] == f"Timeout {i}"


class TestGetLogsExcludesLargeFields:
    """Tests verifying that get_logs excludes large fields for performance."""

    def _create_repo(self, sqlite_config: dict[str, Any]) -> LogsRepository:
        """Create a LogsRepository with the test database."""
        db = get_database(sqlite_config)
        db.initialize()
        repo = LogsRepository()
        return repo

    def test_get_logs_excludes_full_response(self, sqlite_config: dict[str, Any]) -> None:
        """Test that get_logs (list endpoint) does NOT include full_response.

        The list endpoint should exclude large fields like full_response
        and stream_chunks to ensure fast response times.
        """
        repo = self._create_repo(sqlite_config)

        with repo._database.session() as session:
            log = RequestLog(
                request_time=datetime.now(timezone.utc),
                model_name="test-model",
                outcome="success",
                full_response="This is a very long response. " * 1000,
                stream_chunks=[{"chunk": f"data-{i}"} for i in range(100)],
            )
            session.add(log)
            session.flush()
            log_id = log.id

        # Get logs list - should NOT include full_response or stream_chunks
        result = repo.get_logs(limit=1, offset=0)

        assert result["logs"] is not None
        assert len(result["logs"]) == 1

        log_summary = result["logs"][0]
        # These large fields should NOT be in the summary
        assert "full_response" not in log_summary, "get_logs should exclude full_response"
        assert "stream_chunks" not in log_summary, "get_logs should exclude stream_chunks"
        # But the body should be excluded too for performance
        assert "body" not in log_summary, "get_logs should exclude body"

    def test_get_logs_includes_essential_fields(self, sqlite_config: dict[str, Any]) -> None:
        """Test that get_logs includes all essential fields for the list view."""
        repo = self._create_repo(sqlite_config)

        with repo._database.session() as session:
            log = RequestLog(
                request_time=datetime.now(timezone.utc),
                model_name="test-model",
                outcome="success",
                is_stream=False,
                duration_ms=1500,
                usage_stats={"prompt_tokens": 10, "completion_tokens": 50, "total_tokens": 60},
                is_tool_call=False,
            )
            session.add(log)
            session.flush()

        result = repo.get_logs(limit=1, offset=0)
        log_summary = result["logs"][0]

        # Essential fields should be present
        assert "id" in log_summary
        assert "request_time" in log_summary
        assert "model_name" in log_summary
        assert "outcome" in log_summary
        assert "duration_ms" in log_summary
        assert "usage_stats" in log_summary
        assert "is_tool_call" in log_summary


class TestLogsRepositoryAnalytics:
    """Tests for analytics methods in LogsRepository."""

    def _create_repo(self, sqlite_config: dict[str, Any]) -> LogsRepository:
        """Create a LogsRepository with the test database."""
        db = get_database(sqlite_config)
        db.initialize()
        repo = LogsRepository()
        return repo

    def test_get_stop_reason_counts(self, sqlite_config: dict[str, Any]) -> None:
        """Test getting stop reason counts."""
        repo = self._create_repo(sqlite_config)

        # Create logs with different stop reasons
        with repo._database.session() as session:
            for _ in range(5):
                log = RequestLog(
                    request_time=datetime.now(timezone.utc),
                    model_name="test-model",
                    stop_reason="stop",
                )
                session.add(log)
            for _ in range(3):
                log = RequestLog(
                    request_time=datetime.now(timezone.utc),
                    model_name="test-model",
                    stop_reason="tool_calls",
                )
                session.add(log)

        result = repo.get_stop_reason_counts()

        assert result is not None
        assert len(result) == 2

    def test_get_tool_call_rate(self, sqlite_config: dict[str, Any]) -> None:
        """Test getting tool call rate."""
        repo = self._create_repo(sqlite_config)

        # Create logs with and without tool calls
        with repo._database.session() as session:
            for _ in range(7):
                log = RequestLog(
                    request_time=datetime.now(timezone.utc),
                    model_name="test-model",
                    is_tool_call=False,
                )
                session.add(log)
            for _ in range(3):
                log = RequestLog(
                    request_time=datetime.now(timezone.utc),
                    model_name="test-model",
                    is_tool_call=True,
                )
                session.add(log)

        result = repo.get_tool_call_rate()

        assert result["total_requests"] == 10
        assert result["tool_call_requests"] == 3
        assert result["tool_call_rate"] == 30.0

    def test_get_requests_per_model(self, sqlite_config: dict[str, Any]) -> None:
        """Test getting requests per model."""
        repo = self._create_repo(sqlite_config)

        # Create logs for different models
        with repo._database.session() as session:
            for _ in range(10):
                log = RequestLog(
                    request_time=datetime.now(timezone.utc),
                    model_name="gpt-4",
                )
                session.add(log)
            for _ in range(5):
                log = RequestLog(
                    request_time=datetime.now(timezone.utc),
                    model_name="claude-3",
                )
                session.add(log)

        result = repo.get_requests_per_model_with_stop_reason(limit=10)

        assert result is not None
        assert len(result) == 2

        # Should be sorted by count descending
        assert result[0]["model_name"] == "gpt-4"
        assert result[0]["total_requests"] == 10
