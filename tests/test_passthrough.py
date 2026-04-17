"""Tests for passthrough functionality."""

import pytest

from src.core import ProxyRouter


def test_passthrough_backend_separation():
    """Test that passthrough backends are separated from regular backends."""
    config = {
        "model_list": [
            {
                "model_name": "regular-model",
                "model_params": {
                    "api_base": "http://localhost:8001",
                    "api_type": "openai",
                },
            },
            {
                "model_name": "passthrough-model",
                "model_params": {
                    "api_base": "http://localhost:8002",
                    "api_type": "passthrough",
                },
            },
        ]
    }

    router = ProxyRouter(config)

    # Regular model should be in backends
    assert "regular-model" in router.backends
    assert "regular-model" not in router.passthrough_backends
    assert router.backends["regular-model"].passthrough is False

    # Passthrough model should be in passthrough_backends
    assert "passthrough-model" in router.passthrough_backends
    assert "passthrough-model" not in router.backends
    assert router.passthrough_backends["passthrough-model"].passthrough is True


@pytest.mark.asyncio
async def test_list_passthrough_model_names():
    """Test that list_passthrough_model_names returns only passthrough models."""
    config = {
        "model_list": [
            {
                "model_name": "model-1",
                "model_params": {"api_base": "http://localhost:8001", "api_type": "openai"},
            },
            {
                "model_name": "model-2",
                "model_params": {"api_base": "http://localhost:8002", "api_type": "passthrough"},
            },
            {
                "model_name": "model-3",
                "model_params": {"api_base": "http://localhost:8003", "api_type": "anthropic"},
            },
        ]
    }

    router = ProxyRouter(config)

    regular_models = await router.list_model_names()
    passthrough_models = await router.list_passthrough_model_names()

    assert "model-1" in regular_models
    assert "model-2" not in regular_models
    assert "model-3" in regular_models

    assert "model-2" in passthrough_models
    assert "model-1" not in passthrough_models
    assert "model-3" not in passthrough_models


@pytest.mark.asyncio
async def test_get_passthrough_backend():
    """Test get_passthrough_backend method."""
    config = {
        "model_list": [
            {
                "model_name": "speechpro-embeddings",
                "model_params": {
                    "api_base": "http://nid-sc-03.ad.speechpro.com:33500",
                    "api_type": "passthrough",
                    "request_timeout": 60,
                },
            },
        ]
    }

    router = ProxyRouter(config)

    backend = router.get_passthrough_backend("speechpro-embeddings")
    assert backend is not None
    assert backend.name == "speechpro-embeddings"
    assert backend.base_url == "http://nid-sc-03.ad.speechpro.com:33500"
    assert backend.api_type == "passthrough"
    assert backend.passthrough is True
    assert backend.timeout == 60.0

    # Non-existent model should return None
    assert router.get_passthrough_backend("non-existent") is None


@pytest.mark.asyncio
async def test_passthrough_backend_attributes():
    """Test that passthrough backend has all expected attributes."""
    config = {
        "model_list": [
            {
                "model_name": "test-passthrough",
                "model_params": {
                    "api_base": "http://example.com:8080",
                    "api_type": "passthrough",
                    "api_key": "test-key",
                    "request_timeout": 30,
                    "http2": True,
                },
            },
        ]
    }

    router = ProxyRouter(config)
    backend = router.passthrough_backends["test-passthrough"]

    assert backend.name == "test-passthrough"
    assert backend.base_url == "http://example.com:8080"
    assert backend.api_type == "passthrough"
    assert backend.api_key == "test-key"
    assert backend.timeout == 30.0
    assert backend.http2 is True
    assert backend.passthrough is True
