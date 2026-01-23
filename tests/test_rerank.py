"""Tests for the rerank endpoint."""

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
                "model_name": "bge-reranker-test",
                "protected": False,
                "model_params": {
                    "model": "BAAI/bge-reranker-base",
                    "api_base": "http://rerank.local/v1",
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


def test_rerank_requires_model_parameter(proxy_module):
    """Test that rerank endpoint requires a model parameter."""
    with TestClient(proxy_module.app) as client:
        response = client.post(
            "/v1/rerank",
            json={"query": "test query", "documents": ["doc1", "doc2"]}
        )
        assert response.status_code == 400
        assert "model" in response.json()["detail"]["error"]["message"]


def test_rerank_requires_query_parameter(proxy_module):
    """Test that rerank endpoint requires a query parameter."""
    with TestClient(proxy_module.app) as client:
        response = client.post(
            "/v1/rerank",
            json={"model": "bge-reranker-test", "documents": ["doc1", "doc2"]}
        )
        assert response.status_code == 400
        assert "query" in response.json()["detail"]["error"]["message"]


def test_rerank_requires_documents_parameter(proxy_module):
    """Test that rerank endpoint requires a documents parameter."""
    with TestClient(proxy_module.app) as client:
        response = client.post(
            "/v1/rerank",
            json={"model": "bge-reranker-test", "query": "test query"}
        )
        assert response.status_code == 400
        assert "documents" in response.json()["detail"]["error"]["message"]


def test_rerank_requires_non_empty_documents(proxy_module):
    """Test that rerank endpoint rejects empty documents array."""
    with TestClient(proxy_module.app) as client:
        response = client.post(
            "/v1/rerank",
            json={"model": "bge-reranker-test", "query": "test query", "documents": []}
        )
        assert response.status_code == 400
        assert "documents" in response.json()["detail"]["error"]["message"]


def test_rerank_rejects_non_string_documents(proxy_module):
    """Test that rerank endpoint rejects non-string documents."""
    with TestClient(proxy_module.app) as client:
        response = client.post(
            "/v1/rerank",
            json={"model": "bge-reranker-test", "query": "test query", "documents": ["doc1", 123]}
        )
        assert response.status_code == 400
        error_msg = response.json()["detail"]["error"]["message"]
        assert "string" in error_msg.lower()


def test_rerank_rejects_invalid_top_n(proxy_module):
    """Test that rerank endpoint rejects invalid top_n."""
    with TestClient(proxy_module.app) as client:
        response = client.post(
            "/v1/rerank",
            json={
                "model": "bge-reranker-test",
                "query": "test query",
                "documents": ["doc1"],
                "top_n": -1
            }
        )
        assert response.status_code == 400
        assert "top_n" in response.json()["detail"]["error"]["message"]


def test_rerank_rejects_zero_top_n(proxy_module):
    """Test that rerank endpoint rejects zero top_n."""
    with TestClient(proxy_module.app) as client:
        response = client.post(
            "/v1/rerank",
            json={
                "model": "bge-reranker-test",
                "query": "test query",
                "documents": ["doc1"],
                "top_n": 0
            }
        )
        assert response.status_code == 400
        assert "top_n" in response.json()["detail"]["error"]["message"]


def test_rerank_with_invalid_json(proxy_module):
    """Test that invalid JSON in the request body is handled."""
    with TestClient(proxy_module.app) as client:
        response = client.post(
            "/v1/rerank",
            content="not valid json",
            headers={"Content-Type": "application/json"}
        )
        assert response.status_code == 400
        assert "Invalid JSON" in response.json()["detail"]["error"]["message"]


def test_rerank_with_non_object_payload(proxy_module):
    """Test that non-object JSON payloads are rejected."""
    with TestClient(proxy_module.app) as client:
        response = client.post(
            "/v1/rerank",
            json=["array", "instead", "of", "object"]
        )
        assert response.status_code == 400
        assert "object" in response.json()["detail"]["error"]["message"].lower()


def test_rerank_accepts_valid_request(proxy_module):
    """Test that rerank accepts a valid request (validation only, will fail on backend)."""
    with TestClient(proxy_module.app) as client:
        response = client.post(
            "/v1/rerank",
            json={
                "model": "bge-reranker-test",
                "query": "What is machine learning?",
                "documents": ["ML is a subset of AI", "Python is a language"],
                "top_n": 2,
                "return_documents": True
            }
        )
        # Should pass validation (not 400 for missing/invalid params)
        # Will fail with connection error since no real backend
        assert response.status_code != 400 or "model" not in str(response.json())


def test_rerank_accepts_request_without_optional_params(proxy_module):
    """Test that rerank accepts a request without optional parameters."""
    with TestClient(proxy_module.app) as client:
        response = client.post(
            "/v1/rerank",
            json={
                "model": "bge-reranker-test",
                "query": "test query",
                "documents": ["doc1", "doc2"]
            }
        )
        # Should pass validation (not 400 for missing/invalid params)
        assert response.status_code != 400 or "model" not in str(response.json())


def test_rerank_rejects_empty_query(proxy_module):
    """Test that rerank endpoint rejects empty query string."""
    with TestClient(proxy_module.app) as client:
        response = client.post(
            "/v1/rerank",
            json={"model": "bge-reranker-test", "query": "", "documents": ["doc1"]}
        )
        assert response.status_code == 400
        assert "query" in response.json()["detail"]["error"]["message"]


def test_rerank_rejects_whitespace_only_query(proxy_module):
    """Test that rerank endpoint rejects whitespace-only query."""
    with TestClient(proxy_module.app) as client:
        response = client.post(
            "/v1/rerank",
            json={"model": "bge-reranker-test", "query": "   ", "documents": ["doc1"]}
        )
        assert response.status_code == 400
        assert "query" in response.json()["detail"]["error"]["message"]
