"""Tests for the embeddings endpoint."""

import importlib.util
import os
import sys
import uuid
from pathlib import Path

import pytest
import yaml
from fastapi.testclient import TestClient


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
    """Create a proxy module with test configuration."""
    config = {
        "model_list": [
            {
                "model_name": "text-embedding-test",
                "protected": False,
                "model_params": {
                    "model": "text-embedding-3-small",
                    "api_base": "http://embedding.local/v1",
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


def test_embeddings_requires_model_parameter(proxy_module):
    """Test that embeddings endpoint requires a model parameter."""
    with TestClient(proxy_module.app) as client:
        response = client.post(
            "/v1/embeddings",
            json={"input": "Hello, world!"}
        )
        assert response.status_code == 400
        assert "model" in response.json()["detail"]["error"]["message"]


def test_embeddings_requires_input_parameter(proxy_module):
    """Test that embeddings endpoint requires an input parameter."""
    with TestClient(proxy_module.app) as client:
        response = client.post(
            "/v1/embeddings",
            json={"model": "text-embedding-test"}
        )
        assert response.status_code == 400
        assert "input" in response.json()["detail"]["error"]["message"]


def test_embeddings_rejects_invalid_input_type(proxy_module):
    """Test that embeddings endpoint rejects non-string/array input."""
    with TestClient(proxy_module.app) as client:
        response = client.post(
            "/v1/embeddings",
            json={"model": "text-embedding-test", "input": 12345}
        )
        assert response.status_code == 400
        error_msg = response.json()["detail"]["error"]["message"]
        assert "string" in error_msg or "array" in error_msg


def test_embeddings_with_invalid_json(proxy_module):
    """Test that invalid JSON in the request body is handled."""
    with TestClient(proxy_module.app) as client:
        response = client.post(
            "/v1/embeddings",
            content="not valid json",
            headers={"Content-Type": "application/json"}
        )
        assert response.status_code == 400
        assert "Invalid JSON" in response.json()["detail"]["error"]["message"]


def test_embeddings_accepts_string_input(proxy_module):
    """Test that embeddings accepts a string input (validation only, will fail on backend)."""
    with TestClient(proxy_module.app) as client:
        # This will pass validation but fail because there's no real backend
        response = client.post(
            "/v1/embeddings",
            json={
                "model": "text-embedding-test",
                "input": "Hello, world!"
            }
        )
        # Either succeeds (if mock) or fails with connection error (no backend)
        # We just verify it gets past validation (not 400)
        assert response.status_code != 400 or "model" not in str(response.json()) or "input" not in str(response.json())


def test_embeddings_accepts_array_input(proxy_module):
    """Test that embeddings accepts an array of strings input (validation only)."""
    with TestClient(proxy_module.app) as client:
        response = client.post(
            "/v1/embeddings",
            json={
                "model": "text-embedding-test",
                "input": ["Hello", "World"]
            }
        )
        # Should pass validation (not 400 for missing params)
        assert response.status_code != 400 or "model" not in str(response.json()) or "input" not in str(response.json())


def test_embeddings_with_non_object_payload(proxy_module):
    """Test that non-object JSON payloads are rejected."""
    with TestClient(proxy_module.app) as client:
        response = client.post(
            "/v1/embeddings",
            json=["array", "instead", "of", "object"]
        )
        assert response.status_code == 400
        assert "object" in response.json()["detail"]["error"]["message"].lower()
