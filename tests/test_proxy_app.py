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
    os.environ["YALLMP_CONFIG"] = str(default_path)
    module_name = f"proxy_test_{uuid.uuid4().hex}"
    
    # Import from src package instead of proxy.py
    src_path = Path(__file__).resolve().parents[1] / "src"
    src_init_path = src_path / "__init__.py"
    
    # Create a spec for the src package
    spec = importlib.util.spec_from_file_location(module_name, src_init_path)
    module = importlib.util.module_from_spec(spec)
    sys.modules[module_name] = module
    assert spec.loader is not None
    spec.loader.exec_module(module)
    
    module._test_config_default = default_path
    module._test_config_added = added_path
    # Return the app and router from the module
    return module


@pytest.fixture()
def proxy_module(tmp_path):
    """Create a proxy module with test configuration."""
    config = {
        "model_list": [
            {
                "model_name": "alpha",
                "model_params": {
                    "model": "openai/gpt-4o-mini",
                    "api_base": "http://alpha.local/v1",
                    "api_key": "alpha-key",
                },
            },
            {
                "model_name": "beta",
                "model_params": {
                    "model": "openai/gpt-4o-mini",
                    "api_base": "http://beta.local/v1",
                    "api_key": "beta-key",
                },
            },
        ],
        "router_settings": {"num_retries": 1, "fallbacks": [{"alpha": ["beta"]}]},
        "general_settings": {
            "server": {"host": "127.0.0.1", "port": 9999},
            "enable_responses_endpoint": False,
        },
    }
    default_path = tmp_path / "config_default.yaml"
    added_path = tmp_path / "config_added.yaml"
    default_path.write_text(yaml.safe_dump(config), encoding="utf-8")
    added_path.write_text(yaml.safe_dump({"model_list": []}), encoding="utf-8")
    return _load_proxy_with_config(default_path, added_path)


def test_models_endpoint_lists_configured_backends(proxy_module):
    """Test that the models endpoint lists all configured backends."""
    expected_models = {"alpha", "beta"}
    with TestClient(proxy_module.app) as client:
        response = client.get("/v1/models")
        assert response.status_code == 200
        payload = response.json()
        assert payload["object"] == "list"
        returned_models = {entry["id"] for entry in payload["data"]}
        assert expected_models.issubset(returned_models)


def test_admin_register_adds_model_and_lists_it(proxy_module):
    """Test that a new model can be registered at runtime."""
    new_model = {
        "model_name": "gamma",
        "api_base": "http://gamma.local/v1",
        "api_key": "gamma-key",
        "fallbacks": ["alpha"],
    }

    with TestClient(proxy_module.app) as client:
        initial = client.get("/v1/models").json()
        initial_ids = {entry["id"] for entry in initial["data"]}
        assert "gamma" not in initial_ids

        register_resp = client.post("/admin/models", json=new_model)
        assert register_resp.status_code == 200
        register_body = register_resp.json()
        assert register_body["status"] == "ok"
        assert register_body["model"] == "gamma"
        assert register_body["replaced"] is False

        models = client.get("/v1/models").json()
        model_ids = {entry["id"] for entry in models["data"]}
        assert "gamma" in model_ids

    added_cfg = yaml.safe_load(proxy_module._test_config_added.read_text(encoding="utf-8"))
    added_names = {entry.get("model_name") for entry in added_cfg.get("model_list", [])}
    assert "gamma" in added_names


def test_admin_register_replaces_added_model(proxy_module):
    """Test that registering an existing added model replaces it."""
    new_model = {
        "model_name": "gamma",
        "api_base": "http://gamma.local/v1",
        "api_key": "gamma-key",
    }
    updated_model = {
        "model_name": "gamma",
        "api_base": "http://gamma-new.local/v1",
        "api_key": "gamma-new-key",
    }

    with TestClient(proxy_module.app) as client:
        register_resp = client.post("/admin/models", json=new_model)
        assert register_resp.status_code == 200

        replace_resp = client.post("/admin/models", json=updated_model)
        assert replace_resp.status_code == 200
        register_body = replace_resp.json()
        assert register_body["status"] == "ok"
        assert register_body["model"] == "gamma"
        assert register_body["replaced"] is True


def test_admin_register_rejects_default_name(proxy_module):
    """Test that added models cannot override default config names."""
    new_model = {
        "model_name": "alpha",
        "api_base": "http://alpha-new.local/v1",
        "api_key": "alpha-new-key",
    }

    with TestClient(proxy_module.app) as client:
        register_resp = client.post("/admin/models", json=new_model)
        assert register_resp.status_code == 409


def test_chat_completions_requires_model_parameter(proxy_module):
    """Test that chat completions endpoint requires a model parameter."""
    with TestClient(proxy_module.app) as client:
        response = client.post(
            "/v1/chat/completions",
            json={"messages": [{"role": "user", "content": "Hello"}]}
        )
        assert response.status_code == 400
        assert "model" in response.json()["detail"]["error"]["message"]


def test_chat_completions_requires_messages(proxy_module):
    """Test that chat completions endpoint requires messages."""
    with TestClient(proxy_module.app) as client:
        response = client.post(
            "/v1/chat/completions",
            json={"model": "alpha"}
        )
        assert response.status_code == 400
        assert "messages" in response.json()["detail"]["error"]["message"]


def test_chat_completions_with_invalid_json(proxy_module):
    """Test that invalid JSON in the request body is handled."""
    with TestClient(proxy_module.app) as client:
        response = client.post(
            "/v1/chat/completions",
            content="not valid json",
            headers={"Content-Type": "application/json"}
        )
        assert response.status_code == 400
        assert "Invalid JSON" in response.json()["detail"]["error"]["message"]
