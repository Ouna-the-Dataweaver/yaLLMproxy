import importlib.util
import os
import sys
import uuid
from pathlib import Path

import pytest
import yaml
from fastapi.testclient import TestClient


def _load_proxy_with_config(config_path: Path):
    os.environ["YALLMP_CONFIG"] = str(config_path)
    module_name = f"proxy_test_{uuid.uuid4().hex}"
    proxy_path = Path(__file__).resolve().parents[1] / "proxy.py"
    spec = importlib.util.spec_from_file_location(module_name, proxy_path)
    module = importlib.util.module_from_spec(spec)
    sys.modules[module_name] = module
    assert spec.loader is not None
    spec.loader.exec_module(module)
    return module


@pytest.fixture()
def proxy_module(tmp_path):
    config = {
        "model_list": [
            {
                "model_name": "alpha",
                "litellm_params": {
                    "model": "openai/gpt-4o-mini",
                    "api_base": "http://alpha.local/v1",
                    "api_key": "alpha-key",
                },
            },
            {
                "model_name": "beta",
                "litellm_params": {
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
    config_path = tmp_path / "litellm_config.yaml"
    config_path.write_text(yaml.safe_dump(config), encoding="utf-8")
    return _load_proxy_with_config(config_path)


def test_models_endpoint_lists_configured_backends(proxy_module):
    expected_models = {"alpha", "beta"}
    with TestClient(proxy_module.app) as client:
        response = client.get("/v1/models")
        assert response.status_code == 200
        payload = response.json()
        assert payload["object"] == "list"
        returned_models = {entry["id"] for entry in payload["data"]}
        assert expected_models.issubset(returned_models)


def test_admin_register_adds_model_and_lists_it(proxy_module):
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
