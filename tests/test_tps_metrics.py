"""Tests for throughput (Tokens Per Second) metrics feature."""

import time
from datetime import datetime, timedelta, timezone
from typing import Any
from unittest.mock import MagicMock, patch
from uuid import UUID

import pytest

# Skip tests if required modules are not available
pytest.importorskip("src.logging.recorder")
pytest.importorskip("src.database")

from src.logging.recorder import RequestLogRecorder, set_db_logging_enabled
from src.database.factory import get_database, reset_database_instance
from src.database.repository import UsageRepository


@pytest.fixture(autouse=True)
def disable_db_logging():
    """Disable database logging during tests to avoid side effects."""
    set_db_logging_enabled(False)
    yield
    set_db_logging_enabled(True)


class TestRequestLogRecorderThroughput:
    """Tests for throughput calculation in RequestLogRecorder."""

    def test_calculate_throughput_with_tokens(self):
        """Test throughput calculation with valid token counts and duration."""
        recorder = RequestLogRecorder(
            model_name="test-model",
            is_stream=True,
            path="/v1/chat/completions",
            log_to_disk=False,
        )

        # Set usage stats: 50 prompt + 100 completion = 150 tokens
        recorder._usage_stats = {
            "completion_tokens": 100,
            "prompt_tokens": 50,
            "total_tokens": 150,
        }

        # 150 tokens in 1000ms = 150 tok/s
        metrics = recorder._calculate_throughput_metrics(duration_ms=1000)

        assert metrics is not None
        assert "tokens_per_second" in metrics
        assert "processed_tokens" in metrics
        assert metrics["processed_tokens"] == 150
        assert metrics["tokens_per_second"] == 150.0

    def test_calculate_throughput_with_cached_tokens(self):
        """Test throughput calculation excludes cached tokens."""
        recorder = RequestLogRecorder(
            model_name="test-model",
            is_stream=True,
            path="/v1/chat/completions",
            log_to_disk=False,
        )

        # 100 prompt - 30 cached + 50 completion = 120 processed tokens
        recorder._usage_stats = {
            "completion_tokens": 50,
            "prompt_tokens": 100,
            "total_tokens": 150,
            "prompt_tokens_details": {
                "cached_tokens": 30,
            },
        }

        # 120 tokens in 1000ms = 120 tok/s
        metrics = recorder._calculate_throughput_metrics(duration_ms=1000)

        assert metrics is not None
        assert metrics["processed_tokens"] == 120
        assert metrics["tokens_per_second"] == 120.0

    def test_calculate_throughput_without_tokens(self):
        """Test that throughput returns None without token data."""
        recorder = RequestLogRecorder(
            model_name="test-model",
            is_stream=True,
            path="/v1/chat/completions",
            log_to_disk=False,
        )

        # No usage stats
        metrics = recorder._calculate_throughput_metrics(duration_ms=1000)
        assert metrics is None

        # Empty usage stats
        recorder._usage_stats = {}
        metrics = recorder._calculate_throughput_metrics(duration_ms=1000)
        assert metrics is None

    def test_calculate_throughput_without_duration(self):
        """Test that throughput returns None without duration."""
        recorder = RequestLogRecorder(
            model_name="test-model",
            is_stream=True,
            path="/v1/chat/completions",
            log_to_disk=False,
        )

        recorder._usage_stats = {
            "completion_tokens": 100,
            "prompt_tokens": 50,
        }

        # No duration
        metrics = recorder._calculate_throughput_metrics(duration_ms=None)
        assert metrics is None

        # Zero duration
        metrics = recorder._calculate_throughput_metrics(duration_ms=0)
        assert metrics is None

    def test_finalize_merges_throughput_into_usage_stats(self):
        """Test that finalize() merges throughput metrics into usage_stats."""
        recorder = RequestLogRecorder(
            model_name="test-model",
            is_stream=True,
            path="/v1/chat/completions",
            log_to_disk=False,
        )

        recorder._usage_stats = {
            "completion_tokens": 100,
            "prompt_tokens": 50,
            "total_tokens": 150,
        }

        # Add delay to get measurable duration
        time.sleep(0.05)  # 50ms

        recorder.finalize("success")

        # Check that throughput was merged into usage_stats
        assert "tokens_per_second" in recorder._usage_stats
        assert "processed_tokens" in recorder._usage_stats
        assert recorder._usage_stats["processed_tokens"] == 150
        assert recorder._usage_stats["tokens_per_second"] > 0

    def test_finalize_without_usage_data(self):
        """Test finalize() when no usage data is available."""
        recorder = RequestLogRecorder(
            model_name="test-model",
            is_stream=True,
            path="/v1/chat/completions",
            log_to_disk=False,
        )

        # No usage stats means no throughput
        recorder.finalize("success")

        # Should not crash and usage_stats should remain None or unchanged
        if recorder._usage_stats:
            assert "tokens_per_second" not in recorder._usage_stats

    def test_timing_methods_are_noops(self):
        """Test that legacy timing methods don't crash (they're no-ops now)."""
        recorder = RequestLogRecorder(
            model_name="test-model",
            is_stream=True,
            path="/v1/chat/completions",
            log_to_disk=False,
        )

        # These should not raise
        recorder.record_first_content_time()
        recorder.record_generation_end_time()


class TestUsageRepositoryTps:
    """Tests for TPS aggregation queries in UsageRepository."""

    @pytest.fixture
    def sqlite_config(self) -> dict[str, Any]:
        """Create a SQLite configuration for testing."""
        return {
            "backend": "sqlite",
            "connection": {
                "sqlite": {
                    "path": ":memory:",
                }
            },
            "pool_size": 2,
            "max_overflow": 0,
        }

    @pytest.fixture(autouse=True)
    def reset_db(self) -> None:
        """Reset the database instance before each test."""
        reset_database_instance()
        yield
        reset_database_instance()

    def test_get_avg_tps_by_model_weighted_average(
        self, sqlite_config: dict[str, Any]
    ) -> None:
        """Test that get_avg_tps_by_model calculates weighted average correctly."""
        reset_database_instance()
        db = get_database(sqlite_config)
        db.initialize()

        # Insert test data directly
        from src.database.models import RequestLog
        from datetime import datetime, timezone
        import uuid

        with db.session() as session:
            # Model A: Two requests with different TPS and completion tokens
            # Request 1: TPS=50, completion=100 -> weighted = 5000
            # Request 2: TPS=100, completion=200 -> weighted = 20000
            # Total completion = 300, weighted sum = 25000
            # Weighted avg = 25000 / 300 = 83.33
            session.add(RequestLog(
                id=uuid.uuid4(),
                model_name="model-a",
                is_stream=True,
                path="/v1/chat/completions",
                outcome="success",
                request_time=datetime.now(timezone.utc),
                usage_stats={
                    "completion_tokens": 100,
                    "tokens_per_second": 50,
                },
            ))
            session.add(RequestLog(
                id=uuid.uuid4(),
                model_name="model-a",
                is_stream=True,
                path="/v1/chat/completions",
                outcome="success",
                request_time=datetime.now(timezone.utc),
                usage_stats={
                    "completion_tokens": 200,
                    "tokens_per_second": 100,
                },
            ))
            session.commit()

        repository = UsageRepository()
        results = repository.get_avg_tps_by_model()

        assert len(results) == 1
        model_a = results[0]
        assert model_a["model_name"] == "model-a"
        assert model_a["request_count"] == 2
        assert model_a["total_completion_tokens"] == 300
        # Weighted average: (50*100 + 100*200) / 300 = 25000/300 = 83.33
        assert abs(model_a["avg_tps"] - 83.33) < 0.1

    def test_get_tps_stats_aggregation(
        self, sqlite_config: dict[str, Any]
    ) -> None:
        """Test get_tps_stats returns correct aggregation."""
        reset_database_instance()
        db = get_database(sqlite_config)
        db.initialize()

        from src.database.models import RequestLog
        from datetime import datetime, timezone
        import uuid

        with db.session() as session:
            # Add requests with various TPS values
            for tps, completion in [(50, 100), (100, 200), (150, 100)]:
                session.add(RequestLog(
                    id=uuid.uuid4(),
                    model_name="test-model",
                    is_stream=True,
                    path="/v1/chat/completions",
                    outcome="success",
                    request_time=datetime.now(timezone.utc),
                    usage_stats={
                        "completion_tokens": completion,
                        "tokens_per_second": tps,
                    },
                ))
            session.commit()

        repository = UsageRepository()
        stats = repository.get_tps_stats()

        assert stats["request_count"] == 3
        assert stats["min_tps"] == 50
        assert stats["max_tps"] == 150
        # Weighted avg: (50*100 + 100*200 + 150*100) / 400 = 40000/400 = 100
        assert stats["overall_avg_tps"] == 100

    def test_get_tps_stats_no_data(
        self, sqlite_config: dict[str, Any]
    ) -> None:
        """Test get_tps_stats returns empty stats when no data."""
        reset_database_instance()
        db = get_database(sqlite_config)
        db.initialize()

        repository = UsageRepository()
        stats = repository.get_tps_stats()

        assert stats["request_count"] == 0
        assert stats["overall_avg_tps"] is None
        assert stats["min_tps"] is None
        assert stats["max_tps"] is None

    def test_get_avg_tps_by_model_excludes_zero_tps(
        self, sqlite_config: dict[str, Any]
    ) -> None:
        """Test that requests without TPS data are excluded."""
        reset_database_instance()
        db = get_database(sqlite_config)
        db.initialize()

        from src.database.models import RequestLog
        from datetime import datetime, timezone
        import uuid

        with db.session() as session:
            # Request with TPS
            session.add(RequestLog(
                id=uuid.uuid4(),
                model_name="model-with-tps",
                is_stream=True,
                path="/v1/chat/completions",
                outcome="success",
                request_time=datetime.now(timezone.utc),
                usage_stats={
                    "completion_tokens": 100,
                    "tokens_per_second": 50,
                },
            ))
            # Request without TPS
            session.add(RequestLog(
                id=uuid.uuid4(),
                model_name="model-without-tps",
                is_stream=False,
                path="/v1/chat/completions",
                outcome="success",
                request_time=datetime.now(timezone.utc),
                usage_stats={
                    "completion_tokens": 100,
                },
            ))
            session.commit()

        repository = UsageRepository()
        results = repository.get_avg_tps_by_model()

        # Only model-with-tps should be included
        assert len(results) == 1
        assert results[0]["model_name"] == "model-with-tps"
