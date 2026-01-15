"""Integration tests for fake upstream + proxy harness."""

from __future__ import annotations

import json
import sys
from pathlib import Path

import httpx
import pytest

sys.path.insert(0, str(Path(__file__).parent.parent / "src"))

from src.core.upstream_transport import clear_upstream_transports, register_upstream_transport
from src.modules.response_pipeline import SSEDecoder
from src.testing import (
    FakeUpstream,
    ProxyHarness,
    UpstreamResponse,
    normalize_message_for_compare,
    unparse_assistant_message,
)

PROJECT_ROOT = Path(__file__).parent.parent
XML_TEMPLATE = PROJECT_ROOT / "configs" / "jinja_templates" / "template_example.jinja"
K2_TEMPLATE = PROJECT_ROOT / "configs" / "jinja_templates" / "k2thinking.jinja"


def _build_config(base_url: str) -> dict:
    return {
        "model_list": [
            {
                "model_name": "alpha",
                "model_params": {
                    "model": "openai/fake",
                    "api_base": base_url,
                    "api_key": "test-key",
                },
            }
        ],
        "proxy_settings": {
            "modules": {
                "enabled": True,
                "response": ["parse_unparsed"],
                "parse_unparsed": {
                    "parse_thinking": True,
                    "parse_tool_calls": False,
                    "think_tag": "think",
                    "tool_tag": "tool_call",
                },
            }
        },
    }


def _build_template_config(base_url: str, template_path: str) -> dict:
    return {
        "model_list": [
            {
                "model_name": "alpha",
                "model_params": {
                    "model": "openai/fake",
                    "api_base": base_url,
                    "api_key": "test-key",
                },
            }
        ],
        "proxy_settings": {
            "modules": {
                "enabled": True,
                "response": ["parse_template"],
                "parse_template": {
                    "parse_thinking": True,
                    "parse_tool_calls": True,
                    "template_path": template_path,
                },
            }
        },
    }


@pytest.fixture(autouse=True)
def _clear_transport_registry():
    yield
    clear_upstream_transports()


@pytest.mark.asyncio
async def test_fake_upstream_non_stream_parsed() -> None:
    upstream = FakeUpstream()
    upstream.enqueue(
        UpstreamResponse(
            json_body={
                "id": "cmpl-test",
                "object": "chat.completion",
                "model": "fake",
                "choices": [
                    {
                        "index": 0,
                        "message": {
                            "role": "assistant",
                            "content": "<think>Reason</think>Answer",
                        },
                    }
                ],
            }
        )
    )

    base_url = "http://upstream.local/v1"
    register_upstream_transport("upstream.local", httpx.ASGITransport(app=upstream.app))

    with ProxyHarness(_build_config(base_url)) as proxy:
        async with proxy.make_async_client() as client:
            response = await client.post(
                "/v1/chat/completions",
                json={
                    "model": "alpha",
                    "messages": [{"role": "user", "content": "hello"}],
                },
            )

    assert response.status_code == 200
    payload = response.json()
    message = payload["choices"][0]["message"]
    assert message["content"] == "Answer"
    assert message["reasoning_content"] == "Reason"


@pytest.mark.asyncio
async def test_fake_upstream_stream_parsed() -> None:
    upstream = FakeUpstream()
    upstream.enqueue(
        UpstreamResponse(
            stream=True,
            stream_events=[
                {
                    "id": "cmpl-test",
                    "object": "chat.completion.chunk",
                    "choices": [
                        {
                            "index": 0,
                            "delta": {"role": "assistant", "content": "<think>Reason</think>"},
                        }
                    ],
                },
                {
                    "choices": [
                        {"index": 0, "delta": {"content": "Answer"}}
                    ]
                },
            ],
        )
    )

    base_url = "http://upstream.local/v1"
    register_upstream_transport("upstream.local", httpx.ASGITransport(app=upstream.app))

    with ProxyHarness(_build_config(base_url)) as proxy:
        async with proxy.make_async_client() as client:
            async with client.stream(
                "POST",
                "/v1/chat/completions",
                json={
                    "model": "alpha",
                    "messages": [{"role": "user", "content": "hello"}],
                    "stream": True,
                },
            ) as response:
                assert response.status_code == 200
                decoder = SSEDecoder()
                events: list[dict] = []
                async for chunk in response.aiter_raw():
                    for event in decoder.feed(chunk):
                        if event.data is None:
                            continue
                        if event.data.strip() == "[DONE]":
                            continue
                        events.append(json.loads(event.data))

    content_parts: list[str] = []
    reasoning_parts: list[str] = []
    for event in events:
        choices = event.get("choices") or []
        for choice in choices:
            delta = choice.get("delta") or {}
            content = delta.get("content")
            reasoning = delta.get("reasoning_content")
            if isinstance(content, str):
                content_parts.append(content)
            if isinstance(reasoning, str):
                reasoning_parts.append(reasoning)

    assert "".join(content_parts) == "Answer"
    assert "".join(reasoning_parts) == "Reason"


@pytest.mark.asyncio
@pytest.mark.parametrize("template_path", [XML_TEMPLATE, K2_TEMPLATE])
async def test_template_unparse_roundtrip(template_path: Path) -> None:
    if not template_path.exists():
        pytest.skip("template missing")

    messages = [
        {"role": "user", "content": "Hi"},
        {
            "role": "assistant",
            "content": "Answer",
            "reasoning_content": "Reason",
            "tool_calls": [
                {
                    "type": "function",
                    "function": {"name": "lookup", "arguments": {"q": "x"}},
                }
            ],
        },
    ]
    expected = messages[-1]
    upstream_message = unparse_assistant_message(
        expected,
        template_path=str(template_path),
        unparse_reasoning=True,
        unparse_tool_calls=True,
    )

    upstream = FakeUpstream()
    upstream.enqueue(
        UpstreamResponse(
            json_body={
                "id": "cmpl-test",
                "object": "chat.completion",
                "model": "fake",
                "choices": [{"index": 0, "message": upstream_message}],
            }
        )
    )

    base_url = "http://upstream.local/v1"
    register_upstream_transport("upstream.local", httpx.ASGITransport(app=upstream.app))

    with ProxyHarness(_build_template_config(base_url, str(template_path))) as proxy:
        async with proxy.make_async_client() as client:
            response = await client.post(
                "/v1/chat/completions",
                json={
                    "model": "alpha",
                    "messages": messages,
                },
            )

    assert response.status_code == 200
    payload = response.json()
    message = payload["choices"][0]["message"]
    assert normalize_message_for_compare(message) == normalize_message_for_compare(expected)
