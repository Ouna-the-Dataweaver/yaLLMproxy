"""Regression tests for structured output requests through the proxy."""

from __future__ import annotations

import json
import sys
from pathlib import Path

import httpx
import pytest

sys.path.insert(0, str(Path(__file__).parent.parent / "src"))

from src.core.upstream_transport import clear_upstream_transports, register_upstream_transport
from src.logging.recorder import RequestLogRecorder
from src.testing import FakeUpstream, ProxyHarness, UpstreamResponse


def _build_reasoning_config(base_url: str) -> dict:
    return {
        "model_list": [
            {
                "model_name": "glm_local",
                "model_params": {
                    "model": "GLM_air_fp8",
                    "api_base": base_url,
                    "api_key": "test-key",
                    "api_type": "openai",
                    "supports_reasoning": True,
                },
            }
        ],
        "proxy_settings": {
            "modules": {
                "upstream": {
                    "enabled": False,
                    "response": ["parse_tags", "swap_reasoning_content"],
                }
            }
        },
    }


@pytest.fixture(autouse=True)
def _clear_transport_registry() -> None:
    yield
    clear_upstream_transports()


@pytest.mark.asyncio
async def test_structured_output_request_does_not_enable_thinking_upstream() -> None:
    upstream = FakeUpstream()
    upstream.enqueue(
        UpstreamResponse(
            json_body={
                "id": "cmpl-test",
                "object": "chat.completion",
                "model": "GLM_air_fp8",
                "choices": [
                    {
                        "index": 0,
                        "message": {
                            "role": "assistant",
                            "content": json.dumps({"name": "Jordan", "age": 34}),
                            "reasoning": "Pick a sample name and age.",
                            "reasoning_content": "Pick a sample name and age.",
                        },
                        "finish_reason": "stop",
                    }
                ],
            }
        )
    )

    base_url = "http://structured-upstream.local/v1"
    register_upstream_transport("structured-upstream.local", httpx.ASGITransport(app=upstream.app))

    response_format = {
        "type": "json_object",
        "schema": {
            "name": {"type": "string"},
            "age": {"type": "integer"},
        },
    }

    with ProxyHarness(_build_reasoning_config(base_url)) as proxy:
        async with proxy.make_async_client() as client:
            response = await client.post(
                "/v1/chat/completions",
                json={
                    "model": "glm_local",
                    "messages": [{"role": "user", "content": "Give me a JSON object with a random name and age"}],
                    "max_tokens": 1500,
                    "response_format": response_format,
                },
            )

    assert response.status_code == 200
    assert len(upstream.received) == 1

    upstream_payload = upstream.received[0]["json"]
    assert upstream_payload["model"] == "GLM_air_fp8"
    assert upstream_payload["response_format"] == response_format
    assert "thinking" not in upstream_payload

    message = response.json()["choices"][0]["message"]
    assert json.loads(message["content"]) == {"name": "Jordan", "age": 34}
    assert message["reasoning_content"] == "Pick a sample name and age."


@pytest.mark.asyncio
async def test_structured_output_nonstream_logging_keeps_native_reasoning() -> None:
    upstream = FakeUpstream()
    upstream.enqueue(
        UpstreamResponse(
            json_body={
                "id": "cmpl-test",
                "object": "chat.completion",
                "model": "GLM_air_fp8",
                "choices": [
                    {
                        "index": 0,
                        "message": {
                            "role": "assistant",
                            "content": json.dumps({"name": "Jordan", "age": 34}),
                            "reasoning": "Pick a sample name and age.",
                            "reasoning_content": "Pick a sample name and age.",
                        },
                        "finish_reason": "stop",
                    }
                ],
                "usage": {
                    "prompt_tokens": 12,
                    "completion_tokens": 250,
                    "total_tokens": 262,
                },
            }
        )
    )

    base_url = "http://structured-logging-upstream.local/v1"
    register_upstream_transport("structured-logging-upstream.local", httpx.ASGITransport(app=upstream.app))

    captured_log: RequestLogRecorder | None = None
    original_finalize = RequestLogRecorder.finalize

    def capturing_finalize(self: RequestLogRecorder, outcome: str) -> None:
        nonlocal captured_log
        captured_log = self
        self._finalized = True

    RequestLogRecorder.finalize = capturing_finalize

    try:
        with ProxyHarness(_build_reasoning_config(base_url)) as proxy:
            async with proxy.make_async_client() as client:
                response = await client.post(
                    "/v1/chat/completions",
                    json={
                        "model": "glm_local",
                        "messages": [{"role": "user", "content": "Give me a JSON object with a random name and age"}],
                        "max_tokens": 1500,
                        "response_format": {"type": "json_object"},
                    },
                )
    finally:
        RequestLogRecorder.finalize = original_finalize

    assert response.status_code == 200
    assert captured_log is not None
    assert captured_log._accumulated_reasoning_content == "Pick a sample name and age."
    assert captured_log._build_full_response() == (
        "<think>Pick a sample name and age.</think>\n"
        '{"name": "Jordan", "age": 34}'
    )
