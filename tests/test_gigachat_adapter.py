from __future__ import annotations

import json
import sys
from typing import Any

import httpx
import pytest

from src.core.gigachat import GigaChatBackendAdapter, GigaChatHTTPClient, build_gigachat_config
from src.core.gigachat.config import DEFAULT_SCOPE
from src.core.gigachat.translator import (
    gigachat_response_to_openai,
    openai_chat_to_gigachat,
)
from src.core.router import ProxyRouter


def _gigachat_cloud_params(**overrides: Any) -> dict[str, Any]:
    params = {
        "api_type": "gigachat",
        "api_base": "https://gigachat.example/api/v1",
        "api_key": "encoded-key",
        "scope": DEFAULT_SCOPE,
        "auth_url": "https://auth.example/oauth",
        "model": "GigaChat-Max",
    }
    params.update(overrides)
    return params


def _gigachat_local_params(**overrides: Any) -> dict[str, Any]:
    params = {
        "api_type": "gigachat",
        "api_base": "https://gigachat.local/api/v1",
        "client_cert": "/certs/client.pem",
        "client_key": "/certs/client.key",
        "model": "GigaChat-Local",
    }
    params.update(overrides)
    return params


def test_build_cloud_config() -> None:
    cfg = build_gigachat_config(_gigachat_cloud_params())
    assert cfg.mode == "cloud"
    assert cfg.api_key == "encoded-key"
    assert cfg.scope == DEFAULT_SCOPE
    assert cfg.base_url == "https://gigachat.example/api/v1"
    assert cfg.auth_url == "https://auth.example/oauth"


def test_build_local_config() -> None:
    cfg = build_gigachat_config(_gigachat_local_params())
    assert cfg.mode == "local"
    assert cfg.client_cert_file == "/certs/client.pem"
    assert cfg.client_key_file == "/certs/client.key"
    assert cfg.api_key is None


def test_build_config_requires_credentials() -> None:
    with pytest.raises(ValueError):
        build_gigachat_config({"api_type": "gigachat", "api_base": "https://x"})


def test_translator_converts_openai_tools_to_gigachat_functions() -> None:
    payload = {
        "model": "GigaChat-Max",
        "messages": [{"role": "user", "content": "Read file"}],
        "tools": [
            {
                "type": "function",
                "function": {
                    "name": "read",
                    "description": "Read a file.",
                    "parameters": {
                        "type": "object",
                        "properties": {"filename": {"type": "string"}},
                    },
                },
            }
        ],
    }
    result = openai_chat_to_gigachat(payload, default_model="GigaChat-Max")
    assert result["function_call"] == "auto"
    assert result["functions"][0]["name"] == "read"


def test_translator_converts_gigachat_function_call_to_openai_tool_calls() -> None:
    response = {
        "id": "answer-1",
        "model": "GigaChat-Max",
        "choices": [
            {
                "message": {
                    "content": "",
                    "function_call": {"name": "show_files", "arguments": {"limit": 10}},
                },
                "finish_reason": "tool_call",
            }
        ],
    }
    result = gigachat_response_to_openai(response, request_model="GigaChat-Max")
    message = result["choices"][0]["message"]
    assert result["choices"][0]["finish_reason"] == "tool_calls"
    assert message["tool_calls"][0]["function"]["name"] == "show_files"
    assert json.loads(message["tool_calls"][0]["function"]["arguments"]) == {"limit": 10}


@pytest.mark.asyncio
async def test_client_gets_cloud_token_and_converts_chat_response() -> None:
    requests: list[httpx.Request] = []

    async def handler(request: httpx.Request) -> httpx.Response:
        requests.append(request)
        if str(request.url) == "https://auth.example/oauth":
            assert request.headers["Authorization"] == "Basic encoded-key"
            assert request.content == b"scope=GIGACHAT_API_CORP"
            return httpx.Response(200, json={"access_token": "token-1", "expires_at": 4_102_444_800_000})
        assert str(request.url) == "https://gigachat.example/api/v1/chat/completions"
        assert request.headers["Authorization"] == "Bearer token-1"
        body = json.loads(request.content)
        assert body["messages"] == [{"role": "user", "content": "Hello"}]
        return httpx.Response(
            200,
            json={
                "id": "resp-1",
                "model": "GigaChat-Max",
                "choices": [{"message": {"content": "Hi"}, "finish_reason": "stop"}],
            },
        )

    cfg = build_gigachat_config(_gigachat_cloud_params())
    http_client = httpx.AsyncClient(
        transport=httpx.MockTransport(handler),
        base_url="https://gigachat.example/api/v1",
    )
    client = GigaChatHTTPClient(cfg, http_client=http_client)

    result = await client.chat_completions({"messages": [{"role": "user", "content": "Hello"}]})

    assert result["choices"][0]["message"]["content"] == "Hi"
    assert len(requests) == 2
    await http_client.aclose()


@pytest.mark.asyncio
async def test_client_uses_native_gigachat_functions_when_tool_emulation_disabled() -> None:
    requests: list[httpx.Request] = []

    async def handler(request: httpx.Request) -> httpx.Response:
        requests.append(request)
        if str(request.url) == "https://auth.example/oauth":
            return httpx.Response(200, json={"access_token": "token-1", "expires_at": 4_102_444_800_000})
        body = json.loads(request.content)
        assert body["function_call"] == "auto"
        assert body["functions"][0]["name"] == "show_files"
        return httpx.Response(
            200,
            json={
                "id": "resp-1",
                "model": "GigaChat-Max",
                "choices": [
                    {
                        "message": {"content": "", "function_call": {"name": "show_files", "arguments": {}}},
                        "finish_reason": "tool_call",
                    }
                ],
            },
        )

    cfg = build_gigachat_config(_gigachat_cloud_params())
    http_client = httpx.AsyncClient(
        transport=httpx.MockTransport(handler),
        base_url="https://gigachat.example/api/v1",
    )
    client = GigaChatHTTPClient(cfg, http_client=http_client)

    result = await client.chat_completions(
        {
            "messages": [{"role": "user", "content": "Show files"}],
            "tools": [
                {
                    "type": "function",
                    "function": {"name": "show_files", "parameters": {"type": "object", "properties": {}}},
                }
            ],
            "tool_choice": "auto",
        }
    )

    assert result["choices"][0]["finish_reason"] == "tool_calls"
    assert result["choices"][0]["message"]["tool_calls"][0]["function"]["name"] == "show_files"
    assert len(requests) == 2
    await http_client.aclose()


@pytest.mark.asyncio
async def test_client_emulates_tool_call_when_flag_enabled() -> None:
    requests: list[httpx.Request] = []

    async def handler(request: httpx.Request) -> httpx.Response:
        requests.append(request)
        if str(request.url) == "https://auth.example/oauth":
            return httpx.Response(200, json={"access_token": "token-1", "expires_at": 4_102_444_800_000})
        body = json.loads(request.content)
        assert "functions" not in body
        assert body["messages"][0]["role"] == "system"
        assert '"action":"call_tool"' in body["messages"][0]["content"]
        return httpx.Response(
            200,
            json={
                "id": "resp-1",
                "model": "GigaChat-Max",
                "choices": [
                    {
                        "message": {
                            "content": '{"action":"call_tool","tool":"show_files","arguments":{}}'
                        },
                        "finish_reason": "stop",
                    }
                ],
                "usage": {"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
            },
        )

    cfg = build_gigachat_config(_gigachat_cloud_params(emulate_tool_calls=True))
    http_client = httpx.AsyncClient(
        transport=httpx.MockTransport(handler),
        base_url="https://gigachat.example/api/v1",
    )
    client = GigaChatHTTPClient(cfg, http_client=http_client)

    result = await client.chat_completions(
        {
            "messages": [{"role": "user", "content": "Show files"}],
            "tools": [
                {
                    "type": "function",
                    "function": {"name": "show_files", "parameters": {"type": "object", "properties": {}}},
                }
            ],
        }
    )

    assert result["choices"][0]["finish_reason"] == "tool_calls"
    assert result["choices"][0]["message"]["tool_calls"][0]["function"]["name"] == "show_files"
    assert result["usage"] == {"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}
    assert len(requests) == 2
    await http_client.aclose()


@pytest.mark.asyncio
async def test_router_routes_to_gigachat_backend() -> None:
    async def handler(request: httpx.Request) -> httpx.Response:
        if str(request.url) == "https://auth.example/oauth":
            return httpx.Response(200, json={"access_token": "token-1", "expires_at": 4_102_444_800_000})
        assert str(request.url) == "https://gigachat.example/api/v1/chat/completions"
        body = json.loads(request.content)
        assert body["messages"] == [{"role": "user", "content": "Hi"}]
        return httpx.Response(
            200,
            json={
                "id": "resp-1",
                "model": "GigaChat-Max",
                "choices": [{"message": {"content": "Hello from GigaChat"}, "finish_reason": "stop"}],
            },
        )

    from src.core.upstream_transport import register_upstream_transport

    mock_transport = httpx.MockTransport(handler)
    register_upstream_transport("gigachat.example", mock_transport)
    register_upstream_transport("auth.example", mock_transport)

    config = {
        "model_list": [
            {
                "model_name": "GigaChat-Max",
                "model_params": _gigachat_cloud_params(),
            }
        ],
        "router_settings": {"num_retries": 1},
        "proxy_settings": {},
    }
    router = ProxyRouter(config)

    response = await router.forward_request(
        model_name="GigaChat-Max",
        path="/v1/chat/completions",
        query="",
        body=b'{"messages":[{"role":"user","content":"Hi"}]}',
        payload={"messages": [{"role": "user", "content": "Hi"}]},
        is_stream=False,
        headers={"content-type": "application/json"},
    )

    assert response.status_code == 200
    data = json.loads(response.body)
    assert data["choices"][0]["message"]["content"] == "Hello from GigaChat"


def test_admin_backend_from_runtime_payload_builds_gigachat_config() -> None:
    from src.api.routes.admin import _backend_from_runtime_payload

    payload = {
        "model_name": "GigaChat-Admin",
        "model_params": {
            "api_type": "gigachat",
            "api_base": "https://gigachat.example/api/v1",
            "api_key": "encoded-key",
            "scope": "GIGACHAT_API_CORP",
            "auth_url": "https://auth.example/oauth",
            "emulate_tool_calls": True,
        },
    }
    backend, fallbacks, model_entry = _backend_from_runtime_payload(payload)

    assert backend.api_type == "gigachat"
    assert backend.gigachat_config is not None
    assert backend.gigachat_config.mode == "cloud"
    assert backend.gigachat_config.scope == "GIGACHAT_API_CORP"
    assert backend.gigachat_config.emulate_tool_calls is True
    assert model_entry["model_params"]["scope"] == "GIGACHAT_API_CORP"
    assert model_entry["model_params"]["emulate_tool_calls"] is True


def test_admin_backend_generates_gigachat_api_key_from_credentials() -> None:
    import base64

    from src.api.routes.admin import _backend_from_runtime_payload

    payload = {
        "model_name": "GigaChat-Admin",
        "model_params": {
            "api_type": "gigachat",
            "api_base": "https://gigachat.example/api/v1",
            "client_id": "client-id-1",
            "client_secret": "client-secret-2",
            "scope": "GIGACHAT_API_CORP",
        },
    }
    backend, _fallbacks, model_entry = _backend_from_runtime_payload(payload)

    expected_key = base64.b64encode(b"client-id-1:client-secret-2").decode("ascii")
    assert backend.api_key == expected_key
    assert backend.gigachat_config is not None
    assert backend.gigachat_config.api_key == expected_key
    assert model_entry["model_params"]["api_key"] == expected_key
    assert model_entry["model_params"]["client_id"] == "client-id-1"
    assert model_entry["model_params"]["client_secret"] == "client-secret-2"


def test_admin_update_gigachat_credentials_replaces_api_key(tmp_path, monkeypatch) -> None:
    """Updating client_id/client_secret via UI must regenerate api_key in runtime and config."""
    import base64
    import importlib.util
    import os
    import uuid
    from pathlib import Path

    import yaml
    from fastapi.testclient import TestClient

    initial_config = {
        "model_list": [
            {
                "model_name": "GigaChat-Old",
                "protected": False,
                "model_params": {
                    "api_type": "gigachat",
                    "api_base": "https://gigachat.example/api/v1",
                    "api_key": "b2xkLWtleQ==",
                    "scope": "GIGACHAT_API_CORP",
                },
            }
        ],
        "router_settings": {"num_retries": 1},
        "general_settings": {"server": {"host": "127.0.0.1", "port": 9999}},
    }
    config_path = tmp_path / "config.yaml"
    config_path.write_text(yaml.safe_dump(initial_config), encoding="utf-8")
    monkeypatch.setenv("YALLMP_CONFIG", str(config_path))

    module_name = f"proxy_test_gigachat_{uuid.uuid4().hex}"
    src_init = Path(__file__).resolve().parents[1] / "src" / "__init__.py"
    spec = importlib.util.spec_from_file_location(module_name, src_init)
    module = importlib.util.module_from_spec(spec)
    sys.modules[module_name] = module
    assert spec.loader is not None
    spec.loader.exec_module(module)

    router = module.router
    expected_key = base64.b64encode(b"new-id:new-secret").decode("ascii")

    with TestClient(module.app) as client:
        payload = {
            "model_name": "GigaChat-Old",
            "model_params": {
                "api_type": "gigachat",
                "api_base": "https://gigachat.example/api/v1",
                "client_id": "new-id",
                "client_secret": "new-secret",
                "scope": "GIGACHAT_API_CORP",
            },
        }
        response = client.post("/admin/models", json=payload)
        assert response.status_code == 200

    assert router.backends["GigaChat-Old"].api_key == expected_key

    saved_config = yaml.safe_load(config_path.read_text(encoding="utf-8"))
    params = next(
        m["model_params"] for m in saved_config["model_list"] if m["model_name"] == "GigaChat-Old"
    )
    assert params["api_key"] == expected_key
    assert params["client_id"] == "new-id"
    assert params["client_secret"] == "new-secret"
