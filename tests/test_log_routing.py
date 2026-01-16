"""Tests for database log routing for test requests."""

from __future__ import annotations

from src.logging import resolve_db_log_target, set_db_logging_enabled


def _sqlite_config(path: str = "logs/yaLLM.db") -> dict[str, object]:
    return {
        "backend": "sqlite",
        "connection": {"sqlite": {"path": path}},
        "pool_size": 5,
        "max_overflow": 10,
    }


def _postgres_config() -> dict[str, object]:
    return {
        "backend": "postgres",
        "connection": {
            "postgres": {
                "host": "localhost",
                "port": 5432,
                "database": "yallm_proxy",
                "user": "user",
                "password": "pass",
            }
        },
    }


def test_unknown_model_routes_to_test_db() -> None:
    set_db_logging_enabled(True)
    config = _sqlite_config("logs/yaLLM.db")
    config["testing"] = {"enabled": True}
    target = resolve_db_log_target(
        model_name="unknown/alpha",
        headers={},
        known_models={"gpt-4"},
        db_config=config,
    )
    assert target.instance_key == "testing"
    assert target.enabled is True
    assert target.config is not None
    assert target.config["connection"]["sqlite"]["path"].endswith("yaLLM.test.db")


def test_header_forces_testing_route() -> None:
    set_db_logging_enabled(True)
    config = _sqlite_config()
    config["testing"] = {"enabled": True}
    target = resolve_db_log_target(
        model_name="gpt-4",
        headers={"x-yallmp-test": "1"},
        known_models={"gpt-4"},
        db_config=config,
    )
    assert target.instance_key == "testing"
    assert target.enabled is True


def test_testing_disabled_skips_routing() -> None:
    set_db_logging_enabled(True)
    config = _sqlite_config()
    config["testing"] = {"enabled": False}
    target = resolve_db_log_target(
        model_name="unknown",
        headers={},
        known_models={"gpt-4"},
        db_config=config,
    )
    assert target.instance_key == "default"
    assert target.enabled is True


def test_postgres_without_override_disables_test_db() -> None:
    set_db_logging_enabled(True)
    config = _postgres_config()
    config["testing"] = {"enabled": True}
    target = resolve_db_log_target(
        model_name="unknown",
        headers={},
        known_models={"gpt-4"},
        db_config=config,
    )
    assert target.instance_key == "testing"
    assert target.enabled is False
