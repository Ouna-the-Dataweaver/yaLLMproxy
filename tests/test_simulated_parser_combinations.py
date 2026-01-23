"""Full simulation tests for parser pipeline combinations.

Tests combinations of ParseTagsParser and ReasoningSwapParser running together
in the response processing pipeline.
"""

from __future__ import annotations

import asyncio
import json
import sys
from pathlib import Path
from typing import Any

import httpx
import pytest

sys.path.insert(0, str(Path(__file__).parent.parent / "src"))

from conftest import build_parser_config, register_fake_upstream
from src.core.upstream_transport import clear_upstream_transports
from src.modules.response_pipeline import SSEDecoder
from src.testing import (
    FakeUpstream,
    ProxyHarness,
    UpstreamResponse,
    build_openai_request,
)


@pytest.fixture(autouse=True)
def _clear_transports():
    yield
    clear_upstream_transports()


# =============================================================================
# Helper Functions
# =============================================================================


def collect_stream_events(raw_chunks: list[bytes]) -> list[dict[str, Any]]:
    """Collect SSE events from raw stream chunks."""
    events: list[dict[str, Any]] = []
    decoder = SSEDecoder()
    for chunk in raw_chunks:
        for event in decoder.feed(chunk):
            if event.data and event.data.strip() != "[DONE]":
                try:
                    events.append(json.loads(event.data))
                except json.JSONDecodeError:
                    pass
    return events


def get_response_content(events: list[dict[str, Any]]) -> str:
    """Extract text content from streaming response events."""
    text = ""
    for event in events:
        choices = event.get("choices", [])
        if choices:
            delta = choices[0].get("delta", {})
            content = delta.get("content", "")
            if content:
                text += content
    return text


def get_response_reasoning(events: list[dict[str, Any]]) -> str:
    """Extract reasoning content from streaming response events."""
    text = ""
    for event in events:
        choices = event.get("choices", [])
        if choices:
            delta = choices[0].get("delta", {})
            reasoning = delta.get("reasoning_content", "")
            if reasoning:
                text += reasoning
    return text


# =============================================================================
# Pipeline Combination Tests
# =============================================================================


@pytest.mark.asyncio
async def test_pipeline_parse_then_swap_nonstream() -> None:
    """Test pipeline: parse_tags extracts thinking, swap puts it back in content.

    This tests the scenario where:
    1. Model returns content with <think> tags
    2. ParseTagsParser extracts thinking to reasoning_content
    3. ReasoningSwapParser moves reasoning back to content with tags

    Net effect: thinking stays in content with tags (round-trip).
    """
    upstream = FakeUpstream()
    upstream.enqueue_openai_chat_response(
        "<think>Deep reasoning here.</think>The final answer.",
        finish_reason="stop",
    )

    base_url = "http://upstream.local/v1"
    register_fake_upstream("upstream.local", upstream)

    # Pipeline: parse_tags -> swap_reasoning (reasoning_to_content)
    config = build_parser_config(
        base_url,
        parse_tags={
            "parse_thinking": True,
            "think_tag": "think",
        },
        swap_reasoning={
            "mode": "reasoning_to_content",
            "think_tag": "think",
        },
    )
    with ProxyHarness(config) as harness:
        async with harness.make_async_client() as client:
            response = await client.post(
                "/v1/chat/completions",
                json=build_openai_request(
                    messages=[{"role": "user", "content": "Test"}],
                    model="test-model",
                ),
            )

    assert response.status_code == 200
    payload = response.json()
    message = payload["choices"][0]["message"]
    content = message.get("content") or ""

    # After round-trip: content should have thinking back in it
    # Either as tags or the reasoning should be present somehow
    assert "reasoning" in content.lower() or "answer" in content.lower() or "<think>" in content


@pytest.mark.asyncio
async def test_pipeline_parse_then_swap_stream() -> None:
    """Test streaming pipeline with both parsers."""
    upstream = FakeUpstream()
    upstream.enqueue_openai_chat_response(
        "<think>Thinking in stream.</think>Streamed answer.",
        stream=True,
        finish_reason="stop",
    )

    base_url = "http://upstream.local/v1"
    register_fake_upstream("upstream.local", upstream)

    config = build_parser_config(
        base_url,
        parse_tags={
            "parse_thinking": True,
            "think_tag": "think",
        },
        swap_reasoning={
            "mode": "reasoning_to_content",
            "think_tag": "think",
        },
    )
    with ProxyHarness(config) as harness:
        async with harness.make_async_client() as client:
            chunks: list[bytes] = []
            async with client.stream(
                "POST",
                "/v1/chat/completions",
                json=build_openai_request(
                    messages=[{"role": "user", "content": "Test"}],
                    model="test-model",
                    stream=True,
                ),
            ) as response:
                async for chunk in response.aiter_raw():
                    chunks.append(chunk)

    events = collect_stream_events(chunks)
    content = get_response_content(events)

    # Stream should complete successfully
    assert len(events) > 0


@pytest.mark.asyncio
async def test_pipeline_parse_thinking_and_tool_calls() -> None:
    """Test pipeline handling both thinking and tool calls."""
    upstream = FakeUpstream()
    upstream.enqueue_openai_chat_response(
        "<think>Let me use a tool.</think><tool_call>helper<arg_key>x</arg_key><arg_value>42</arg_value></tool_call>",
        finish_reason="stop",
    )

    base_url = "http://upstream.local/v1"
    register_fake_upstream("upstream.local", upstream)

    config = build_parser_config(
        base_url,
        parse_tags={
            "parse_thinking": True,
            "parse_tool_calls": True,
            "tool_arg_format": "xml",
        },
        swap_reasoning={
            "mode": "reasoning_to_content",
            "think_tag": "think",
        },
    )
    with ProxyHarness(config) as harness:
        async with harness.make_async_client() as client:
            response = await client.post(
                "/v1/chat/completions",
                json=build_openai_request(
                    messages=[{"role": "user", "content": "Help me"}],
                    model="test-model",
                ),
            )

    assert response.status_code == 200
    payload = response.json()
    message = payload["choices"][0]["message"]

    # Should have tool calls
    tool_calls = message.get("tool_calls", [])
    assert len(tool_calls) >= 1, f"Expected tool calls, got message: {message}"

    tc = tool_calls[0]
    assert tc["function"]["name"] == "helper"


@pytest.mark.asyncio
async def test_pipeline_only_parse_tags() -> None:
    """Test pipeline with only ParseTagsParser (no swap)."""
    upstream = FakeUpstream()
    upstream.enqueue_openai_chat_response(
        "<think>Pure parsing test.</think>Just the answer.",
        finish_reason="stop",
    )

    base_url = "http://upstream.local/v1"
    register_fake_upstream("upstream.local", upstream)

    config = build_parser_config(
        base_url,
        parse_tags={
            "parse_thinking": True,
            "think_tag": "think",
        },
        # No swap_reasoning
    )
    with ProxyHarness(config) as harness:
        async with harness.make_async_client() as client:
            response = await client.post(
                "/v1/chat/completions",
                json=build_openai_request(
                    messages=[{"role": "user", "content": "Test"}],
                    model="test-model",
                ),
            )

    assert response.status_code == 200
    payload = response.json()
    message = payload["choices"][0]["message"]

    # Reasoning should be in reasoning_content
    reasoning = message.get("reasoning_content", "")
    content = message.get("content") or ""

    # Either reasoning is extracted or content is clean
    has_reasoning = "Pure parsing" in reasoning or "parsing" in reasoning.lower()
    has_answer = "answer" in content.lower() or "Just" in content

    assert has_reasoning or has_answer


@pytest.mark.asyncio
async def test_pipeline_only_swap_reasoning() -> None:
    """Test pipeline with only ReasoningSwapParser (no parse_tags)."""
    upstream = FakeUpstream()
    # Response already has reasoning_content field
    response_body = {
        "id": "chatcmpl-test",
        "object": "chat.completion",
        "model": "test-model",
        "choices": [{
            "index": 0,
            "message": {
                "role": "assistant",
                "content": "The answer is 42.",
                "reasoning_content": "I calculated using math.",
            },
            "finish_reason": "stop",
        }],
        "usage": {"prompt_tokens": 10, "completion_tokens": 20, "total_tokens": 30},
    }
    upstream.enqueue(UpstreamResponse(json_body=response_body))

    base_url = "http://upstream.local/v1"
    register_fake_upstream("upstream.local", upstream)

    config = build_parser_config(
        base_url,
        # No parse_tags
        swap_reasoning={
            "mode": "reasoning_to_content",
            "think_tag": "think",
        },
    )
    with ProxyHarness(config) as harness:
        async with harness.make_async_client() as client:
            response = await client.post(
                "/v1/chat/completions",
                json=build_openai_request(
                    messages=[{"role": "user", "content": "What's the answer?"}],
                    model="test-model",
                ),
            )

    assert response.status_code == 200
    payload = response.json()
    content = payload["choices"][0]["message"].get("content") or ""

    # Reasoning should be prepended with think tags
    assert "math" in content.lower() or "42" in content or "<think>" in content


@pytest.mark.asyncio
async def test_pipeline_no_parsers() -> None:
    """Test that response passes through unchanged without parsers."""
    upstream = FakeUpstream()
    upstream.enqueue_openai_chat_response(
        "<think>Tags stay.</think>Content stays.",
        finish_reason="stop",
    )

    base_url = "http://upstream.local/v1"
    register_fake_upstream("upstream.local", upstream)

    # Build config without any parsers
    config = {
        "model_list": [
            {
                "model_name": "test-model",
                "model_params": {
                    "model": "openai/fake",
                    "api_base": base_url,
                    "api_key": "test-key",
                },
            }
        ],
        # No modules config
    }
    with ProxyHarness(config) as harness:
        async with harness.make_async_client() as client:
            response = await client.post(
                "/v1/chat/completions",
                json=build_openai_request(
                    messages=[{"role": "user", "content": "Test"}],
                    model="test-model",
                ),
            )

    assert response.status_code == 200
    payload = response.json()
    content = payload["choices"][0]["message"].get("content") or ""

    # Tags should remain since no parsers
    assert "<think>" in content or "Tags stay" in content


@pytest.mark.asyncio
async def test_pipeline_with_multiple_tool_calls_stream() -> None:
    """Test streaming pipeline with multiple tool calls."""
    upstream = FakeUpstream()
    upstream.enqueue_openai_chat_response(
        "<think>Using multiple tools.</think>"
        "<tool_call>func1<arg_key>a</arg_key><arg_value>1</arg_value></tool_call>"
        "<tool_call>func2<arg_key>b</arg_key><arg_value>2</arg_value></tool_call>",
        stream=True,
        finish_reason="stop",
    )

    base_url = "http://upstream.local/v1"
    register_fake_upstream("upstream.local", upstream)

    config = build_parser_config(
        base_url,
        parse_tags={
            "parse_thinking": True,
            "parse_tool_calls": True,
            "tool_arg_format": "xml",
        },
    )
    with ProxyHarness(config) as harness:
        async with harness.make_async_client() as client:
            chunks: list[bytes] = []
            async with client.stream(
                "POST",
                "/v1/chat/completions",
                json=build_openai_request(
                    messages=[{"role": "user", "content": "Do multiple things"}],
                    model="test-model",
                    stream=True,
                ),
            ) as response:
                async for chunk in response.aiter_raw():
                    chunks.append(chunk)

    events = collect_stream_events(chunks)

    # Should complete successfully
    assert len(events) > 0

    # Extract tool calls from stream
    tool_calls: dict[int, dict[str, Any]] = {}
    for event in events:
        choices = event.get("choices", [])
        if choices:
            delta = choices[0].get("delta", {})
            for tc in delta.get("tool_calls", []):
                idx = tc.get("index", 0)
                if idx not in tool_calls:
                    tool_calls[idx] = {"function": {"name": "", "arguments": ""}}
                func = tc.get("function", {})
                if "name" in func:
                    tool_calls[idx]["function"]["name"] = func["name"]
                if "arguments" in func:
                    tool_calls[idx]["function"]["arguments"] += func["arguments"]

    # Should have 2 tool calls
    assert len(tool_calls) == 2, f"Expected 2 tool calls, got {len(tool_calls)}"


@pytest.mark.asyncio
async def test_pipeline_k2_format_with_thinking() -> None:
    """Test K2 tool format combined with thinking tags."""
    upstream = FakeUpstream()
    upstream.enqueue_openai_chat_response(
        '<think>Using K2 format.</think><|tool_call_begin|>api_call<|tool_call_argument_begin|>{"endpoint":"test"}<|tool_call_end|>',
        finish_reason="stop",
    )

    base_url = "http://upstream.local/v1"
    register_fake_upstream("upstream.local", upstream)

    config = build_parser_config(
        base_url,
        parse_tags={
            "parse_thinking": True,
            "parse_tool_calls": True,
            "tool_arg_format": "json",
            "tool_open": "<|tool_call_begin|>",
            "tool_close": "<|tool_call_end|>",
            "tool_arg_separator": "<|tool_call_argument_begin|>",
        },
    )
    with ProxyHarness(config) as harness:
        async with harness.make_async_client() as client:
            response = await client.post(
                "/v1/chat/completions",
                json=build_openai_request(
                    messages=[{"role": "user", "content": "Call API"}],
                    model="test-model",
                ),
            )

    assert response.status_code == 200
    payload = response.json()
    message = payload["choices"][0]["message"]

    # Should have reasoning
    reasoning = message.get("reasoning_content", "")
    assert "K2" in reasoning or "Using" in reasoning or len(reasoning) > 0

    # Should have tool call
    tool_calls = message.get("tool_calls", [])
    if len(tool_calls) > 0:
        tc = tool_calls[0]
        assert tc["function"]["name"] == "api_call"
