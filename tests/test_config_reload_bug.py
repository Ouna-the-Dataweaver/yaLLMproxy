"""Tests for config reload functionality - verifying parameter updates work correctly.

This test file verifies that the fixes for the bugs described in docs/config_reload_bug.md
are working correctly:

1. PUT /admin/config now properly updates the router's in-memory Backend objects
2. POST /admin/models now properly passes parameters to the Backend constructor
"""

import asyncio
import copy
import importlib.util
import os
import sys
import uuid
from pathlib import Path

import pytest
import yaml
from fastapi.testclient import TestClient


def _load_proxy_with_config(config_path: Path):
    """Load the proxy module with a specific config file."""
    os.environ["YALLMP_CONFIG"] = str(config_path)
    module_name = f"proxy_test_{uuid.uuid4().hex}"

    src_path = Path(__file__).resolve().parents[1] / "src"
    src_init_path = src_path / "__init__.py"

    spec = importlib.util.spec_from_file_location(module_name, src_init_path)
    module = importlib.util.module_from_spec(spec)
    sys.modules[module_name] = module
    assert spec.loader is not None
    spec.loader.exec_module(module)

    module._test_config = config_path
    return module


@pytest.fixture()
def proxy_module_with_parameters(tmp_path):
    """Create a proxy module with initial test configuration including parameters."""
    initial_config = {
        "model_list": [
            {
                "model_name": "test-model-with-params",
                "protected": False,
                "model_params": {
                    "model": "openai/gpt-4o-mini",
                    "api_base": "http://test-model.local/v1",
                    "api_key": "test-key",
                },
                "parameters": {
                    "tool_choice": {
                        "default": "none",
                        "allow_override": False,
                    },
                    "temperature": {
                        "default": 0.7,
                        "allow_override": True,
                    },
                },
            },
            {
                "model_name": "test-model-no-params",
                "protected": False,
                "model_params": {
                    "model": "openai/gpt-4o-mini",
                    "api_base": "http://test-model-2.local/v1",
                    "api_key": "test-key-2",
                },
            },
        ],
        "router_settings": {"num_retries": 1},
        "general_settings": {
            "server": {"host": "127.0.0.1", "port": 9999},
            "enable_responses_endpoint": False,
        },
    }
    config_path = tmp_path / "config.yaml"
    config_path.write_text(yaml.safe_dump(initial_config), encoding="utf-8")
    return _load_proxy_with_config(config_path)


class TestPutAdminConfig:
    """Tests for PUT /admin/config properly updating the router."""

    def test_initial_state_has_parameters(self, proxy_module_with_parameters):
        """Verify that initial config correctly loads parameters into router backends."""
        # Access the router directly from the loaded module
        router = proxy_module_with_parameters.router
        backend = router.backends.get("test-model-with-params")

        assert backend is not None, "Backend should exist"
        assert "tool_choice" in backend.parameters, "Backend should have tool_choice parameter"
        assert backend.parameters["tool_choice"].default == "none"
        assert backend.parameters["tool_choice"].allow_override is False
        assert "temperature" in backend.parameters, "Backend should have temperature parameter"
        assert backend.parameters["temperature"].default == 0.7
        assert backend.parameters["temperature"].allow_override is True

    def test_put_config_updates_router_parameters(self, proxy_module_with_parameters, tmp_path):
        """Test that PUT /admin/config correctly updates router backend parameters."""
        from src.config_store import CONFIG_STORE

        # Access the router directly from the loaded module
        router = proxy_module_with_parameters.router

        with TestClient(proxy_module_with_parameters.app) as client:
            # Get the current config
            response = client.get("/admin/config")
            assert response.status_code == 200
            config = response.json()

            # Modify parameters for the model
            for model in config.get("model_list", []):
                if model.get("model_name") == "test-model-with-params":
                    model["parameters"] = {
                        "tool_choice": {
                            "default": "auto",  # Changed from "none"
                            "allow_override": True,  # Changed from False
                        },
                    }
                    break

            # Update config via PUT /admin/config
            response = client.put("/admin/config", json=config)
            assert response.status_code == 200
            assert response.json()["status"] == "ok"

            # Verify config store has the new parameters
            raw_config = CONFIG_STORE.get_raw()
            for model in raw_config.get("model_list", []):
                if model.get("model_name") == "test-model-with-params":
                    assert model["parameters"]["tool_choice"]["default"] == "auto"
                    break

            # Router backend should have updated parameters immediately
            backend = router.backends.get("test-model-with-params")
            assert backend is not None
            assert "tool_choice" in backend.parameters
            assert backend.parameters["tool_choice"].default == "auto", \
                "Router backend should have new parameter value 'auto'"
            assert backend.parameters["tool_choice"].allow_override is True

    def test_reload_config_also_updates_router_parameters(self, proxy_module_with_parameters, tmp_path):
        """Verify that POST /admin/config/reload still works correctly."""
        from src.config_store import CONFIG_STORE

        # Access the router directly from the loaded module
        router = proxy_module_with_parameters.router

        with TestClient(proxy_module_with_parameters.app) as client:
            # Get the current config
            response = client.get("/admin/config")
            assert response.status_code == 200
            config = response.json()

            # Modify parameters for the model
            for model in config.get("model_list", []):
                if model.get("model_name") == "test-model-with-params":
                    model["parameters"] = {
                        "tool_choice": {
                            "default": "required",
                            "allow_override": False,
                        },
                    }
                    break

            # Save the config via PUT (should update router immediately now)
            response = client.put("/admin/config", json=config)
            assert response.status_code == 200

            # Calling reload should also work
            response = client.post("/admin/config/reload")
            assert response.status_code == 200

            # Verify router backend has updated parameters
            backend = router.backends.get("test-model-with-params")
            assert backend is not None
            assert "tool_choice" in backend.parameters
            assert backend.parameters["tool_choice"].default == "required"
            assert backend.parameters["tool_choice"].allow_override is False

    def test_new_parameters_added_via_put_config_are_applied(self, proxy_module_with_parameters, tmp_path):
        """Test that adding new parameters via PUT /admin/config applies them."""
        # Access the router directly from the loaded module
        router = proxy_module_with_parameters.router

        with TestClient(proxy_module_with_parameters.app) as client:
            # Get the current config
            response = client.get("/admin/config")
            assert response.status_code == 200
            config = response.json()

            # Add parameters to model that didn't have any
            for model in config.get("model_list", []):
                if model.get("model_name") == "test-model-no-params":
                    model["parameters"] = {
                        "max_tokens": {
                            "default": 1000,
                            "allow_override": True,
                        },
                    }
                    break

            # Update config via PUT /admin/config
            response = client.put("/admin/config", json=config)
            assert response.status_code == 200

            # Router backend should now have the new parameters
            backend = router.backends.get("test-model-no-params")
            assert backend is not None
            assert "max_tokens" in backend.parameters, \
                "Router backend should have new parameters after PUT /admin/config"
            assert backend.parameters["max_tokens"].default == 1000


class TestPostAdminModels:
    """Tests for POST /admin/models properly handling parameters."""

    def test_register_model_with_parameters_preserves_them(self, proxy_module_with_parameters):
        """Test that POST /admin/models correctly passes parameters to Backend."""
        # Access the router directly from the loaded module
        router = proxy_module_with_parameters.router

        with TestClient(proxy_module_with_parameters.app) as client:
            # Register a new model with parameters
            payload = {
                "model_name": "new-model-with-params",
                "model_params": {
                    "api_base": "http://new-model.local/v1",
                    "model": "openai/gpt-4o",
                    "api_key": "new-key",
                },
                "parameters": {
                    "tool_choice": {
                        "default": "none",
                        "allow_override": False,
                    },
                },
            }

            response = client.post("/admin/models", json=payload)
            assert response.status_code == 200
            assert response.json()["status"] == "ok"
            assert response.json()["model"] == "new-model-with-params"

            # Router backend should have parameters
            backend = router.backends.get("new-model-with-params")
            assert backend is not None
            assert "tool_choice" in backend.parameters, \
                "Backend created via POST /admin/models should have parameters"
            assert backend.parameters["tool_choice"].default == "none"
            assert backend.parameters["tool_choice"].allow_override is False

    def test_update_existing_model_preserves_new_parameters(self, proxy_module_with_parameters):
        """Test that updating a model via POST /admin/models handles parameters correctly."""
        # Access the router directly from the loaded module
        router = proxy_module_with_parameters.router

        # Verify initial state - model has parameters
        backend = router.backends.get("test-model-with-params")
        assert backend is not None
        assert "tool_choice" in backend.parameters, "Initial backend should have parameters"

        with TestClient(proxy_module_with_parameters.app) as client:
            # Update the existing model with new parameters
            payload = {
                "model_name": "test-model-with-params",
                "model_params": {
                    "api_base": "http://updated-model.local/v1",
                    "model": "openai/gpt-4o-mini",
                    "api_key": "test-key",
                },
                "parameters": {
                    "temperature": {
                        "default": 0.5,
                        "allow_override": True,
                    },
                },
            }

            response = client.post("/admin/models", json=payload)
            assert response.status_code == 200
            assert response.json()["replaced"] is True

            # Router backend should have the new parameters from the payload
            backend = router.backends.get("test-model-with-params")
            assert backend is not None
            assert "temperature" in backend.parameters, \
                "Backend updated via POST /admin/models should have new parameters"
            assert backend.parameters["temperature"].default == 0.5


class TestBuildBackendBodyWithParameters:
    """Tests verifying that build_backend_body correctly applies parameter overrides."""

    def test_parameter_override_applied_when_allow_override_false(self):
        """Test that parameters with allow_override=False always use the default value."""
        from src.core.backend import Backend, ParameterConfig, build_backend_body

        backend = Backend(
            name="test-backend",
            base_url="http://test.local/v1",
            api_key="test-key",
            timeout=30.0,
            target_model="gpt-4o",
            parameters={
                "tool_choice": ParameterConfig(default="none", allow_override=False),
            },
        )

        # Request tries to set tool_choice to "auto", but config says no override
        payload = {
            "model": "test-model",
            "messages": [{"role": "user", "content": "test"}],
            "tool_choice": "auto",  # This should be ignored
        }

        body = build_backend_body(payload, backend, b"{}", is_stream=False)
        import json
        result = json.loads(body)

        # Parameter should be forced to "none" even though request said "auto"
        assert result["tool_choice"] == "none", \
            "Parameter with allow_override=False should force the default value"

    def test_parameter_default_applied_when_missing_from_request(self):
        """Test that parameters with allow_override=True use default when not in request."""
        from src.core.backend import Backend, ParameterConfig, build_backend_body

        backend = Backend(
            name="test-backend",
            base_url="http://test.local/v1",
            api_key="test-key",
            timeout=30.0,
            target_model="gpt-4o",
            parameters={
                "temperature": ParameterConfig(default=0.7, allow_override=True),
            },
        )

        # Request doesn't include temperature
        payload = {
            "model": "test-model",
            "messages": [{"role": "user", "content": "test"}],
        }

        body = build_backend_body(payload, backend, b"{}", is_stream=False)
        import json
        result = json.loads(body)

        # Parameter should get the default value
        assert result["temperature"] == 0.7, \
            "Parameter with allow_override=True should use default when not in request"

    def test_parameter_request_value_used_when_allow_override_true(self):
        """Test that parameters with allow_override=True use request value when present."""
        from src.core.backend import Backend, ParameterConfig, build_backend_body

        backend = Backend(
            name="test-backend",
            base_url="http://test.local/v1",
            api_key="test-key",
            timeout=30.0,
            target_model="gpt-4o",
            parameters={
                "temperature": ParameterConfig(default=0.7, allow_override=True),
            },
        )

        # Request includes temperature
        payload = {
            "model": "test-model",
            "messages": [{"role": "user", "content": "test"}],
            "temperature": 0.9,  # This should be used
        }

        body = build_backend_body(payload, backend, b"{}", is_stream=False)
        import json
        result = json.loads(body)

        # Parameter should use the request value
        assert result["temperature"] == 0.9, \
            "Parameter with allow_override=True should use request value when present"


class TestIntegration:
    """Integration tests showing the full workflow."""

    def test_full_scenario_parameters_work_after_config_update(
        self, proxy_module_with_parameters, tmp_path
    ):
        """Full integration test demonstrating correct behavior.

        1. Initial config has tool_choice="none" with allow_override=False
        2. Router correctly applies this parameter
        3. User updates config via PUT /admin/config to change to "auto"
        4. Router immediately applies the NEW value "auto"
        """
        from src.core.backend import build_backend_body

        # Access the router directly from the loaded module
        router = proxy_module_with_parameters.router

        with TestClient(proxy_module_with_parameters.app) as client:
            # Step 1: Verify initial state
            backend = router.backends.get("test-model-with-params")
            assert backend.parameters["tool_choice"].default == "none"

            # Step 2: Initial parameter is applied correctly
            payload = {"model": "test-model", "messages": [], "tool_choice": "auto"}
            body = build_backend_body(payload, backend, b"{}")
            import json
            result = json.loads(body)
            assert result["tool_choice"] == "none", "Initial config correctly forces tool_choice=none"

            # Step 3: Update config via PUT /admin/config
            response = client.get("/admin/config")
            config = response.json()
            for model in config.get("model_list", []):
                if model.get("model_name") == "test-model-with-params":
                    model["parameters"]["tool_choice"]["default"] = "auto"
                    model["parameters"]["tool_choice"]["allow_override"] = False
                    break
            response = client.put("/admin/config", json=config)
            assert response.status_code == 200

            # Step 4: Router immediately uses the NEW value
            backend = router.backends.get("test-model-with-params")
            body = build_backend_body(payload, backend, b"{}")
            result = json.loads(body)
            assert result["tool_choice"] == "auto", \
                "After PUT /admin/config, router immediately uses new parameter value"
