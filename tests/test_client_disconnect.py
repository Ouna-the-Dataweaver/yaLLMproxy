"""Tests for ClientDisconnect handling in API endpoints.

This test verifies that when a client disconnects during request body reading,
the usage tracker is properly cleaned up and ongoing counter doesn't get stuck.
"""

import importlib.util
import os
import sys
import uuid
from pathlib import Path
from unittest.mock import AsyncMock, MagicMock, patch

import pytest
import yaml
from starlette.requests import ClientDisconnect


def _load_proxy_with_config(config_path: Path):
    """Load the proxy module with specific config file."""
    os.environ["YALLMP_CONFIG"] = str(config_path)
    module_name = f"proxy_test_{uuid.uuid4().hex}"

    src_path = Path(__file__).resolve().parents[1] / "src"
    src_init_path = src_path / "__init__.py"

    spec = importlib.util.spec_from_file_location(module_name, src_init_path)
    module = importlib.util.module_from_spec(spec)
    sys.modules[module_name] = module
    assert spec.loader is not None
    spec.loader.exec_module(module)

    recorder_module_name = f"{module_name}.logging.recorder"
    if recorder_module_name in sys.modules:
        recorder_mod = sys.modules[recorder_module_name]
        if hasattr(recorder_mod, "set_db_logging_enabled"):
            recorder_mod.set_db_logging_enabled(False)

    module._test_config = config_path
    return module


@pytest.fixture()
def proxy_module(tmp_path):
    """Create a proxy module with test configuration including an anthropic backend."""
    config = {
        "model_list": [
            {
                "model_name": "test-anthropic",
                "model_params": {
                    "model": "anthropic/claude-3-haiku",
                    "api_base": "http://test.local/v1",
                    "api_key": "test-key",
                },
            },
            {
                "model_name": "test-openai",
                "model_params": {
                    "model": "openai/gpt-4o-mini",
                    "api_base": "http://test.local/v1",
                    "api_key": "test-key",
                },
            },
        ],
        "router_settings": {"num_retries": 1},
        "general_settings": {
            "server": {"host": "127.0.0.1", "port": 9999},
        },
    }
    config_path = tmp_path / "config.yaml"
    config_path.write_text(yaml.safe_dump(config), encoding="utf-8")
    return _load_proxy_with_config(config_path)


@pytest.mark.asyncio
async def test_messages_endpoint_client_disconnect_cleans_up_tracker(proxy_module):
    """Test that ClientDisconnect during body read properly cleans up the usage tracker.

    This test simulates a client disconnecting while the server is reading the request body.
    The bug: if ClientDisconnect is raised, tracker.finish() is never called, leaving
    the 'ongoing' counter stuck at an elevated value.
    """
    from src.usage_metrics import USAGE_COUNTERS
    from src.api.routes.messages import messages_endpoint

    # Get initial ongoing count
    initial_snapshot = USAGE_COUNTERS.snapshot()
    initial_ongoing = initial_snapshot["ongoing"]

    # Create a mock request that raises ClientDisconnect when body() is called
    mock_request = MagicMock()
    mock_request.body = AsyncMock(side_effect=ClientDisconnect())
    mock_request.headers = {}
    mock_request.url.path = "/v1/messages"
    mock_request.url.query = ""

    # Call the endpoint - it should handle the disconnect gracefully
    try:
        await messages_endpoint(mock_request)
    except ClientDisconnect:
        # Current buggy behavior: exception propagates up unhandled
        pass
    except Exception:
        # Any other exception handling is acceptable
        pass

    # Check that the ongoing counter is back to initial value
    final_snapshot = USAGE_COUNTERS.snapshot()
    final_ongoing = final_snapshot["ongoing"]

    # The test fails if ongoing counter is stuck (wasn't decremented)
    assert final_ongoing == initial_ongoing, (
        f"Usage tracker leak detected! "
        f"Initial ongoing: {initial_ongoing}, Final ongoing: {final_ongoing}. "
        f"tracker.finish() was not called after ClientDisconnect."
    )


@pytest.mark.asyncio
async def test_chat_endpoint_client_disconnect_cleans_up_tracker(proxy_module):
    """Test that ClientDisconnect during body read properly cleans up the usage tracker for chat endpoint."""
    from src.usage_metrics import USAGE_COUNTERS
    from src.api.routes.chat import handle_openai_request

    # Get initial ongoing count
    initial_snapshot = USAGE_COUNTERS.snapshot()
    initial_ongoing = initial_snapshot["ongoing"]

    # Create a mock request that raises ClientDisconnect when body() is called
    mock_request = MagicMock()
    mock_request.body = AsyncMock(side_effect=ClientDisconnect())
    mock_request.headers = {}
    mock_request.url.path = "/v1/chat/completions"
    mock_request.url.query = ""
    mock_request.method = "POST"

    # Call the endpoint - it should handle the disconnect gracefully
    try:
        await handle_openai_request(mock_request)
    except ClientDisconnect:
        # Current buggy behavior: exception propagates up unhandled
        pass
    except Exception:
        # Any other exception handling is acceptable
        pass

    # Check that the ongoing counter is back to initial value
    final_snapshot = USAGE_COUNTERS.snapshot()
    final_ongoing = final_snapshot["ongoing"]

    # The test fails if ongoing counter is stuck (wasn't decremented)
    assert final_ongoing == initial_ongoing, (
        f"Usage tracker leak detected! "
        f"Initial ongoing: {initial_ongoing}, Final ongoing: {final_ongoing}. "
        f"tracker.finish() was not called after ClientDisconnect."
    )
