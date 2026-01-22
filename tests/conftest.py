"""Pytest configuration and fixtures for testing."""

from __future__ import annotations

import sys
from pathlib import Path
from typing import Any, Generator

import httpx
import pytest

# Add src directory to path for imports
sys.path.insert(0, str(Path(__file__).parent / "src"))


@pytest.fixture(autouse=True)
def disable_database_logging():
    """Disable database logging during all tests to prevent test data from being saved to the database.

    This fixture is automatically used for all tests due to autouse=True.
    It ensures that test requests don't pollute the production database with test model names.
    """
    from src.logging import set_db_logging_enabled

    # Disable database logging before the test
    set_db_logging_enabled(False)

    yield

    # Re-enable database logging after the test (in case subsequent tests need it)
    set_db_logging_enabled(True)


# =============================================================================
# Transport Registry Fixtures
# =============================================================================


@pytest.fixture
def clear_transport_registry() -> Generator[None, None, None]:
    """Clear upstream transport registry after test.

    Use this fixture in tests that register fake transports.
    """
    from src.core.upstream_transport import clear_upstream_transports

    yield
    clear_upstream_transports()


@pytest.fixture
def reset_concurrency() -> Generator[None, None, None]:
    """Reset concurrency manager before and after test.

    Use this fixture in tests that use concurrency management.
    """
    from src.concurrency import reset_concurrency_manager

    reset_concurrency_manager()
    yield
    reset_concurrency_manager()


# =============================================================================
# Harness Configuration Builders
# =============================================================================


def build_messages_config(
    base_url: str,
    *,
    backend_type: str = "openai",
    model_name: str = "test-model",
    api_key: str = "test-key",
    concurrency_limit: int = 0,
    priority: int = 100,
    queue_timeout: float = 30.0,
) -> dict[str, Any]:
    """Build a config for messages endpoint testing.

    Args:
        base_url: Upstream server URL
        backend_type: Backend type (openai, anthropic)
        model_name: Model name to register
        api_key: API key for the model
        concurrency_limit: Concurrency limit (0 = unlimited)
        priority: Request priority
        queue_timeout: Queue timeout in seconds

    Returns:
        Config dict for ProxyHarness
    """
    return {
        "model_list": [
            {
                "model_name": model_name,
                "model_params": {
                    "model": f"{backend_type}/fake",
                    "api_base": base_url,
                    "api_key": api_key,
                },
            }
        ],
        "proxy_settings": {
            "concurrency": {
                "enabled": concurrency_limit > 0,
                "default_limit": concurrency_limit,
                "default_priority": priority,
                "default_queue_timeout": queue_timeout,
            }
        },
    }


def build_chat_config(
    base_url: str,
    *,
    model_name: str = "test-model",
    api_key: str = "test-key",
    modules: dict[str, Any] | None = None,
) -> dict[str, Any]:
    """Build a config for chat completions endpoint testing.

    Args:
        base_url: Upstream server URL
        model_name: Model name to register
        api_key: API key
        modules: Optional response modules config

    Returns:
        Config dict for ProxyHarness
    """
    config: dict[str, Any] = {
        "model_list": [
            {
                "model_name": model_name,
                "model_params": {
                    "model": "openai/fake",
                    "api_base": base_url,
                    "api_key": api_key,
                },
            }
        ],
    }

    if modules:
        config["proxy_settings"] = {"modules": modules}

    return config


def build_multi_backend_config(
    backends: list[dict[str, Any]],
) -> dict[str, Any]:
    """Build a config with multiple backends for failover testing.

    Args:
        backends: List of backend configs, each with:
            - name: Model name
            - base_url: Backend URL
            - type: Backend type (openai, anthropic)
            - api_key: API key

    Returns:
        Config dict for ProxyHarness
    """
    model_list = []
    for backend in backends:
        model_list.append(
            {
                "model_name": backend["name"],
                "model_params": {
                    "model": f"{backend.get('type', 'openai')}/fake",
                    "api_base": backend["base_url"],
                    "api_key": backend.get("api_key", "test-key"),
                },
            }
        )

    return {"model_list": model_list}


# =============================================================================
# Harness Fixtures
# =============================================================================


@pytest.fixture
def messages_harness(
    clear_transport_registry: None,
    reset_concurrency: None,
) -> Generator[tuple[Any, Any], None, None]:
    """Create a harness configured for messages endpoint testing.

    Returns:
        Tuple of (FakeUpstream, ProxyHarness)

    Usage:
        def test_messages(messages_harness):
            upstream, harness = messages_harness
            upstream.enqueue_openai_chat_response("Hello")
            # ... test code ...
    """
    from src.core.upstream_transport import register_upstream_transport
    from src.testing import FakeUpstream, ProxyHarness

    base_url = "http://upstream.local/v1"
    upstream = FakeUpstream()
    register_upstream_transport("upstream.local", httpx.ASGITransport(app=upstream.app))

    config = build_messages_config(base_url)
    harness = ProxyHarness(
        config,
        enable_messages_endpoint=True,
        enable_concurrency=True,
        reset_concurrency=True,
    )

    try:
        yield upstream, harness
    finally:
        harness.close()


@pytest.fixture
def concurrent_harness(
    clear_transport_registry: None,
    reset_concurrency: None,
) -> Generator[tuple[Any, Any], None, None]:
    """Create a harness with concurrency management enabled.

    Returns:
        Tuple of (FakeUpstream, ProxyHarness) with concurrency enabled

    Usage:
        def test_concurrent(concurrent_harness):
            upstream, harness = concurrent_harness
            # ... test concurrent requests ...
    """
    from src.core.upstream_transport import register_upstream_transport
    from src.testing import FakeUpstream, ProxyHarness

    base_url = "http://upstream.local/v1"
    upstream = FakeUpstream()
    register_upstream_transport("upstream.local", httpx.ASGITransport(app=upstream.app))

    # Config with concurrency enabled (limit=5)
    config = build_messages_config(
        base_url,
        concurrency_limit=5,
        priority=100,
        queue_timeout=10.0,
    )
    harness = ProxyHarness(
        config,
        enable_messages_endpoint=True,
        enable_concurrency=True,
        reset_concurrency=True,
    )

    try:
        yield upstream, harness
    finally:
        harness.close()


@pytest.fixture
def chat_harness(
    clear_transport_registry: None,
) -> Generator[tuple[Any, Any], None, None]:
    """Create a harness for chat completions endpoint testing.

    Returns:
        Tuple of (FakeUpstream, ProxyHarness)
    """
    from src.core.upstream_transport import register_upstream_transport
    from src.testing import FakeUpstream, ProxyHarness

    base_url = "http://upstream.local/v1"
    upstream = FakeUpstream()
    register_upstream_transport("upstream.local", httpx.ASGITransport(app=upstream.app))

    config = build_chat_config(base_url)
    harness = ProxyHarness(config)

    try:
        yield upstream, harness
    finally:
        harness.close()


# =============================================================================
# Helper Functions for Tests
# =============================================================================


def register_fake_upstream(
    host: str,
    upstream: Any,
) -> None:
    """Register a FakeUpstream for the given host.

    Args:
        host: Host to register (e.g., "upstream.local")
        upstream: FakeUpstream instance
    """
    from src.core.upstream_transport import register_upstream_transport

    register_upstream_transport(host, httpx.ASGITransport(app=upstream.app))
