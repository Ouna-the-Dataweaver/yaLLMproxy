"""Tests for database functionality."""

import tempfile
from datetime import datetime, timezone
from pathlib import Path
from typing import Any
from uuid import UUID

import pytest
from sqlalchemy import select

# Skip tests if database module is not available
pytest.importorskip("src.database")

from src.database.base import Base
from src.database.factory import get_database, reset_database_instance
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
    """Reset the database instance before each test."""
    reset_database_instance()
    yield
    reset_database_instance()


class TestDatabaseFactory:
    """Tests for database factory."""

    def test_get_sqlite_database(self, sqlite_config: dict[str, Any]) -> None:
        """Test creating a SQLite database instance."""
        db = get_database(sqlite_config)
        assert db.backend_name == "sqlite"
        assert db.config["backend"] == "sqlite"

    def test_get_database_default(self) -> None:
        """Test getting default database instance."""
        reset_database_instance()
        db = get_database()
        assert db is not None
        assert db.backend_name == "sqlite"

    def test_get_database_singleton(self, sqlite_config: dict[str, Any]) -> None:
        """Test that get_database returns a singleton."""
        db1 = get_database(sqlite_config)
        db2 = get_database()
        assert db1 is db2


class TestSQLiteDatabase:
    """Tests for SQLite database functionality."""

    def test_initialize_database(self, sqlite_config: dict[str, Any]) -> None:
        """Test database initialization."""
        db = get_database(sqlite_config)
        assert not db.is_initialized
        db.initialize()
        assert db.is_initialized

    def test_get_session(self, sqlite_config: dict[str, Any]) -> None:
        """Test getting a database session."""
        db = get_database(sqlite_config)
        db.initialize()
        session = db.get_session()
        assert session is not None
        session.close()

    def test_session_context_manager(self, sqlite_config: dict[str, Any]) -> None:
        """Test session context manager."""
        db = get_database(sqlite_config)
        db.initialize()
        from sqlalchemy import text
        with db.session() as session:
            # Execute a simple query to verify session works
            result = session.execute(text("SELECT 1"))
            assert result.scalar() == 1

    def test_health_check(self, sqlite_config: dict[str, Any]) -> None:
        """Test database health check."""
        db = get_database(sqlite_config)
        assert db.health_check() is False
        db.initialize()
        assert db.health_check() is True

    def test_close_database(self, sqlite_config: dict[str, Any]) -> None:
        """Test database close."""
        db = get_database(sqlite_config)
        db.initialize()
        assert db.is_initialized
        db.close()
        assert not db.is_initialized


class TestRequestLogModel:
    """Tests for RequestLog model."""

    def test_create_request_log(self, sqlite_config: dict[str, Any]) -> None:
        """Test creating a request log entry."""
        db = get_database(sqlite_config)
        db.initialize()

        with db.session() as session:
            log = RequestLog(
                request_time=datetime.now(timezone.utc),
                model_name="test-model",
                is_stream=False,
                path="/v1/chat/completions",
                method="POST",
                body={"messages": [{"role": "user", "content": "Hello"}]},
                outcome="success",
                duration_ms=1500,
            )
            session.add(log)
            session.flush()

            # Verify the log was created
            assert log.id is not None
            assert isinstance(log.id, UUID)
            assert log.model_name == "test-model"
            assert log.outcome == "success"

    def test_request_log_to_dict(self, sqlite_config: dict[str, Any]) -> None:
        """Test converting request log to dictionary."""
        db = get_database(sqlite_config)
        db.initialize()

        with db.session() as session:
            log = RequestLog(
                request_time=datetime.now(timezone.utc),
                model_name="test-model",
                is_stream=True,
                path="/v1/chat/completions",
                method="POST",
                body={"messages": [{"role": "user", "content": "Hello"}]},
                outcome="success",
                duration_ms=1500,
            )
            session.add(log)
            session.flush()

            log_dict = log.to_dict()
            assert log_dict["model_name"] == "test-model"
            assert log_dict["is_stream"] is True
            assert log_dict["outcome"] == "success"
            assert log_dict["duration_ms"] == 1500

    def test_request_log_properties(self, sqlite_config: dict[str, Any]) -> None:
        """Test request log properties."""
        db = get_database(sqlite_config)
        db.initialize()

        with db.session() as session:
            # Test successful log
            log = RequestLog(
                request_time=datetime.now(timezone.utc),
                model_name="test-model",
                outcome="success",
            )
            session.add(log)
            session.flush()
            assert log.successful is True
            assert log.had_errors is False

            # Test error log
            error_log = RequestLog(
                request_time=datetime.now(timezone.utc),
                model_name="test-model",
                errors=[{"error": "test error"}],
            )
            session.add(error_log)
            session.flush()
            assert error_log.had_errors is True


class TestErrorLogModel:
    """Tests for ErrorLog model."""

    def test_create_error_log(self, sqlite_config: dict[str, Any]) -> None:
        """Test creating an error log entry."""
        db = get_database(sqlite_config)
        db.initialize()

        with db.session() as session:
            log = ErrorLog(
                timestamp=datetime.now(timezone.utc),
                model_name="test-model",
                error_type="sse_stream_error",
                error_message="Stream connection failed",
                backend_name="test-backend",
                http_status=500,
            )
            session.add(log)
            session.flush()

            assert log.id is not None
            assert isinstance(log.id, UUID)
            assert log.error_type == "sse_stream_error"

    def test_error_log_to_dict(self, sqlite_config: dict[str, Any]) -> None:
        """Test converting error log to dictionary."""
        db = get_database(sqlite_config)
        db.initialize()

        with db.session() as session:
            log = ErrorLog(
                timestamp=datetime.now(timezone.utc),
                model_name="test-model",
                error_type="http_error",
                error_message="HTTP 404 Not Found",
                http_status=404,
            )
            session.add(log)
            session.flush()

            log_dict = log.to_dict()
            assert log_dict["model_name"] == "test-model"
            assert log_dict["error_type"] == "http_error"
            assert log_dict["http_status"] == 404

    def test_error_log_request_reference(self, sqlite_config: dict[str, Any]) -> None:
        """Test error log with request reference."""
        db = get_database(sqlite_config)
        db.initialize()

        with db.session() as session:
            # Create a request log first
            request_log = RequestLog(
                request_time=datetime.now(timezone.utc),
                model_name="test-model",
                outcome="error",
            )
            session.add(request_log)
            session.flush()

            # Create an error log referencing the request
            error_log = ErrorLog(
                timestamp=datetime.now(timezone.utc),
                model_name="test-model",
                error_type="timeout",
                error_message="Request timed out",
                request_log_id=request_log.id,
            )
            session.add(error_log)
            session.flush()

            assert error_log.has_request_reference is True
            assert error_log.request_log_id == request_log.id


class TestJSONBOperations:
    """Tests for JSONB column operations."""

    def test_json_body_storage(self, sqlite_config: dict[str, Any]) -> None:
        """Test storing and retrieving JSON body."""
        db = get_database(sqlite_config)
        db.initialize()

        test_body = {
            "model": "gpt-4",
            "messages": [
                {"role": "system", "content": "You are a helpful assistant."},
                {"role": "user", "content": "Hello!"},
            ],
            "temperature": 0.7,
            "max_tokens": 100,
        }

        with db.session() as session:
            log = RequestLog(
                request_time=datetime.now(timezone.utc),
                model_name="test-model",
                body=test_body,
            )
            session.add(log)
            session.flush()

            # Retrieve and verify
            retrieved = session.get(RequestLog, log.id)
            assert retrieved is not None
            assert retrieved.body == test_body
            assert retrieved.body["model"] == "gpt-4"
            assert retrieved.body["messages"][0]["role"] == "system"

    def test_json_headers_storage(self, sqlite_config: dict[str, Any]) -> None:
        """Test storing and retrieving JSON headers."""
        db = get_database(sqlite_config)
        db.initialize()

        test_headers = {
            "content-type": "application/json",
            "authorization": "Bearer ***",
            "user-agent": "test-client/1.0",
        }

        with db.session() as session:
            log = RequestLog(
                request_time=datetime.now(timezone.utc),
                model_name="test-model",
                headers=test_headers,
            )
            session.add(log)
            session.flush()

            retrieved = session.get(RequestLog, log.id)
            assert retrieved is not None
            assert retrieved.headers == test_headers

    def test_json_error_storage(self, sqlite_config: dict[str, Any]) -> None:
        """Test storing and retrieving JSON errors."""
        db = get_database(sqlite_config)
        db.initialize()

        test_errors = [
            {"type": "connection_error", "message": "Connection refused"},
            {"type": "timeout", "message": "Request timed out after 30s"},
        ]

        with db.session() as session:
            log = RequestLog(
                request_time=datetime.now(timezone.utc),
                model_name="test-model",
                errors=test_errors,
            )
            session.add(log)
            session.flush()

            retrieved = session.get(RequestLog, log.id)
            assert retrieved is not None
            assert retrieved.errors == test_errors
            assert len(retrieved.errors) == 2

    def test_json_usage_stats(self, sqlite_config: dict[str, Any]) -> None:
        """Test storing and retrieving JSON usage stats."""
        db = get_database(sqlite_config)
        db.initialize()

        test_usage = {
            "prompt_tokens": 150,
            "completion_tokens": 300,
            "total_tokens": 450,
        }

        with db.session() as session:
            log = RequestLog(
                request_time=datetime.now(timezone.utc),
                model_name="test-model",
                usage_stats=test_usage,
            )
            session.add(log)
            session.flush()

            retrieved = session.get(RequestLog, log.id)
            assert retrieved is not None
            assert retrieved.usage_stats == test_usage
            assert retrieved.usage_stats["total_tokens"] == 450


class TestDatabaseIntegration:
    """Integration tests for database operations."""

    def test_full_request_flow(self, sqlite_config: dict[str, Any]) -> None:
        """Test complete request logging flow."""
        db = get_database(sqlite_config)
        db.initialize()

        request_time = datetime.now(timezone.utc)

        with db.session() as session:
            # Create request log
            request_log = RequestLog(
                request_time=request_time,
                model_name="gpt-4",
                is_stream=True,
                path="/v1/chat/completions",
                method="POST",
                body={"messages": [{"role": "user", "content": "Hello"}]},
                headers={"content-type": "application/json"},
                backend_attempts=[
                    {"backend": "gpt-4", "status": 200, "url": "https://api.openai.com/v1/chat/completions"}
                ],
                usage_stats={"prompt_tokens": 10, "completion_tokens": 50, "total_tokens": 60},
                outcome="success",
                duration_ms=2500,
            )
            session.add(request_log)
            session.flush()

            # Create error log referencing the request
            error_log = ErrorLog(
                timestamp=request_time,
                model_name="gpt-4",
                error_type="client_disconnect",
                error_message="Client disconnected before response complete",
                backend_name="gpt-4",
                http_status=200,
                request_log_id=request_log.id,
                extra_context={"chunks_sent": 5, "chunks_expected": 10},
            )
            session.add(error_log)

        # Verify data was saved
        with db.session() as session:
            # Query request log
            result = session.execute(
                select(RequestLog).where(RequestLog.model_name == "gpt-4")
            )
            retrieved_request = result.scalar_one_or_none()
            assert retrieved_request is not None
            assert retrieved_request.is_stream is True
            assert retrieved_request.usage_stats["total_tokens"] == 60

            # Query error log
            result = session.execute(
                select(ErrorLog).where(ErrorLog.request_log_id == retrieved_request.id)
            )
            retrieved_error = result.scalar_one_or_none()
            assert retrieved_error is not None
            assert retrieved_error.has_request_reference is True
            assert retrieved_error.extra_context["chunks_sent"] == 5
