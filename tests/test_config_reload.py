"""Tests for config reload functionality."""

import importlib.util
import os
import sys
import uuid
from pathlib import Path

import pytest
import yaml
from fastapi.testclient import TestClient


def _load_proxy_with_config(default_path: Path, added_path: Path):
    """Load the proxy module with specific config files."""
    os.environ["YALLMP_CONFIG_DEFAULT"] = str(default_path)
    os.environ["YALLMP_CONFIG_ADDED"] = str(added_path)
    module_name = f"proxy_test_{uuid.uuid4().hex}"
    
    src_path = Path(__file__).resolve().parents[1] / "src"
    src_init_path = src_path / "__init__.py"
    
    spec = importlib.util.spec_from_file_location(module_name, src_init_path)
    module = importlib.util.module_from_spec(spec)
    sys.modules[module_name] = module
    assert spec.loader is not None
    spec.loader.exec_module(module)
    
    module._test_config_default = default_path
    module._test_config_added = added_path
    return module


@pytest.fixture()
def proxy_module(tmp_path):
    """Create a proxy module with initial test configuration."""
    initial_config = {
        "model_list": [
            {
                "model_name": "model-a",
                "model_params": {
                    "model": "openai/gpt-4o-mini",
                    "api_base": "http://model-a.local/v1",
                    "api_key": "model-a-key",
                },
            },
            {
                "model_name": "model-b",
                "model_params": {
                    "model": "openai/gpt-4o-mini",
                    "api_base": "http://model-b.local/v1",
                    "api_key": "model-b-key",
                },
            },
        ],
        "router_settings": {"num_retries": 1, "fallbacks": [{"model-a": ["model-b"]}]},
        "general_settings": {
            "server": {"host": "127.0.0.1", "port": 9999},
            "enable_responses_endpoint": False,
        },
    }
    default_path = tmp_path / "config_default.yaml"
    added_path = tmp_path / "config_added.yaml"
    default_path.write_text(yaml.safe_dump(initial_config), encoding="utf-8")
    added_path.write_text(yaml.safe_dump({"model_list": []}), encoding="utf-8")
    return _load_proxy_with_config(default_path, added_path)


class TestConfigReloadEndpoint:
    """Tests for POST /admin/config/reload endpoint."""

    def test_reload_endpoint_returns_ok(self, proxy_module):
        """Test that the reload endpoint returns success status."""
        with TestClient(proxy_module.app) as client:
            response = client.post("/admin/config/reload")
            assert response.status_code == 200
            payload = response.json()
            assert payload["status"] == "ok"
            assert payload["message"] == "Configuration reloaded successfully"

    def test_reload_endpoint_returns_model_count(self, proxy_module):
        """Test that the reload endpoint returns the number of models."""
        with TestClient(proxy_module.app) as client:
            response = client.post("/admin/config/reload")
            assert response.status_code == 200
            payload = response.json()
            assert "models_count" in payload
            assert payload["models_count"] == 2  # model-a and model-b

    def test_reload_updates_backends(self, proxy_module, tmp_path):
        """Test that reloading with new config updates the router's backends."""
        # Create new config with different models
        new_config = {
            "model_list": [
                {
                    "model_name": "new-model-1",
                    "model_params": {
                        "model": "openai/gpt-4o",
                        "api_base": "http://new-1.local/v1",
                        "api_key": "new-1-key",
                    },
                },
                {
                    "model_name": "new-model-2",
                    "model_params": {
                        "model": "anthropic/claude-3-5-sonnet",
                        "api_base": "http://new-2.local/v1",
                        "api_key": "new-2-key",
                    },
                },
            ],
        }
        new_config_path = tmp_path / "config_default.yaml"
        new_config_path.write_text(yaml.safe_dump(new_config), encoding="utf-8")

        # Replace the config file
        proxy_module._test_config_default = new_config_path

        with TestClient(proxy_module.app) as client:
            # Verify initial state
            models_before = client.get("/v1/models").json()
            model_ids_before = {entry["id"] for entry in models_before["data"]}
            assert "model-a" in model_ids_before
            assert "new-model-1" not in model_ids_before

            # Reload config
            response = client.post("/admin/config/reload")
            assert response.status_code == 200
            payload = response.json()
            assert payload["models_count"] == 2

            # Verify new models are available
            models_after = client.get("/v1/models").json()
            model_ids_after = {entry["id"] for entry in models_after["data"]}
            assert "new-model-1" in model_ids_after
            assert "new-model-2" in model_ids_after

    def test_reload_preserves_added_models(self, proxy_module, tmp_path):
        """Test that reloading preserves models from config_added.yaml."""
        # Add a model to the added config
        added_config = {
            "model_list": [
                {
                    "model_name": "custom-model",
                    "model_params": {
                        "model": "custom/model",
                        "api_base": "http://custom.local/v1",
                        "api_key": "custom-key",
                    },
                },
            ],
        }
        proxy_module._test_config_added.write_text(
            yaml.safe_dump(added_config), encoding="utf-8"
        )

        with TestClient(proxy_module.app) as client:
            # Reload config
            response = client.post("/admin/config/reload")
            assert response.status_code == 200

            # Verify both default and added models are available
            models = client.get("/v1/models").json()
            model_ids = {entry["id"] for entry in models["data"]}
            assert "model-a" in model_ids  # from default
            assert "custom-model" in model_ids  # from added

    def test_reload_handles_empty_config(self, proxy_module, tmp_path):
        """Test that reloading with empty config works correctly."""
        # Create empty config
        empty_config = {
            "model_list": [],
        }
        empty_config_path = tmp_path / "config_default.yaml"
        empty_config_path.write_text(yaml.safe_dump(empty_config), encoding="utf-8")
        proxy_module._test_config_default = empty_config_path

        with TestClient(proxy_module.app) as client:
            response = client.post("/admin/config/reload")
            # Should succeed - reload works even with empty config
            assert response.status_code == 200
            payload = response.json()
            assert payload["models_count"] == 0


class TestRouterReloadConfig:
    """Tests for ProxyRouter.reload_config method."""

    def test_router_reload_updates_backends(self, proxy_module):
        """Test that router's reload_config method updates backends."""
        from src.core.registry import get_router

        router = get_router()

        # First, set up initial state with model-a and model-b
        initial_config = {
            "model_list": [
                {
                    "model_name": "model-a",
                    "model_params": {
                        "model": "test/model",
                        "api_base": "http://model-a.local/v1",
                        "api_key": "model-a-key",
                    },
                },
                {
                    "model_name": "model-b",
                    "model_params": {
                        "model": "test/model",
                        "api_base": "http://model-b.local/v1",
                        "api_key": "model-b-key",
                    },
                },
            ],
        }

        import asyncio
        asyncio.run(router.reload_config(initial_config))

        # Verify initial state
        assert "model-a" in router.backends
        assert "model-b" in router.backends

        # Create new config with different models
        new_config = {
            "model_list": [
                {
                    "model_name": "reloaded-model",
                    "model_params": {
                        "model": "test/model",
                        "api_base": "http://reloaded.local/v1",
                        "api_key": "reloaded-key",
                    },
                },
            ],
        }

        asyncio.run(router.reload_config(new_config))

        # Verify backends are updated
        assert "reloaded-model" in router.backends
        assert "model-a" not in router.backends
        assert "model-b" not in router.backends

    def test_router_reload_updates_fallbacks(self, proxy_module):
        """Test that router's reload_config method updates fallbacks."""
        from src.core.registry import get_router

        router = get_router()

        # First, add model-a and model-b to the router so fallbacks can be checked
        import asyncio
        initial_config = {
            "model_list": [
                {
                    "model_name": "model-a",
                    "model_params": {
                        "model": "test/model",
                        "api_base": "http://model-a.local/v1",
                        "api_key": "model-a-key",
                    },
                },
                {
                    "model_name": "model-b",
                    "model_params": {
                        "model": "test/model",
                        "api_base": "http://model-b.local/v1",
                        "api_key": "model-b-key",
                    },
                },
            ],
            "router_settings": {
                "fallbacks": [{"model-a": ["model-b"]}],
            },
        }
        asyncio.run(router.reload_config(initial_config))

        # Verify initial fallbacks
        assert router.fallbacks.get("model-a") == ["model-b"]

        # Create new config with different fallbacks
        new_config = {
            "model_list": [
                {
                    "model_name": "test-model",
                    "model_params": {
                        "model": "test/model",
                        "api_base": "http://test.local/v1",
                        "api_key": "test-key",
                    },
                },
            ],
            "router_settings": {
                "fallbacks": [{"test-model": ["other-model"]}],
            },
        }

        asyncio.run(router.reload_config(new_config))

        # Verify fallbacks are updated
        assert router.fallbacks.get("test-model") == ["other-model"]
        assert router.fallbacks.get("model-a") is None  # old model removed

    def test_router_reload_with_empty_config(self, proxy_module):
        """Test that router's reload_config handles empty config gracefully."""
        from src.core.registry import get_router

        router = get_router()

        # Create empty config
        new_config = {
            "model_list": [],
            "router_settings": {},
        }

        import asyncio
        # This should work but router will have no backends
        asyncio.run(router.reload_config(new_config))

        assert len(router.backends) == 0
