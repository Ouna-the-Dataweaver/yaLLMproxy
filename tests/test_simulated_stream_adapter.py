"""Full simulation tests for stream adapter edge cases.

Tests the stream adapter under realistic conditions that are hard to unit test,
including chunk fragmentation, corrupted data, and incomplete streams.
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

from conftest import build_messages_config, register_fake_upstream
from src.core.upstream_transport import clear_upstream_transports
from src.modules.response_pipeline import SSEDecoder
from src.testing import (
    FakeUpstream,
    ProxyHarness,
    UpstreamResponse,
    build_anthropic_request,
    build_openai_stream_chunks,
)


@pytest.fixture(autouse=True)
def _clear_transports():
    yield
    clear_upstream_transports()


# =============================================================================
# Chunk Fragmentation Tests
# =============================================================================


@pytest.mark.asyncio
async def test_stream_adapter_fragmented_sse() -> None:
    """Test when SSE events are split across chunk boundaries."""
    upstream = FakeUpstream()

    # Build chunks that will be fragmented
    chunks = build_openai_stream_chunks("Hello!", model="fake")

    upstream.enqueue(
        UpstreamResponse(
            stream=True,
            stream_events=chunks,
            fragment_events=True,  # Split events across chunks
        )
    )

    base_url = "http://upstream.local/v1"
    register_fake_upstream("upstream.local", upstream)

    config = build_messages_config(base_url)
    with ProxyHarness(config, enable_messages_endpoint=True) as harness:
        async with harness.make_async_client() as client:
            async with client.stream(
                "POST",
                "/v1/messages",
                json=build_anthropic_request(
                    messages=[{"role": "user", "content": "Hi"}],
                    max_tokens=100,
                    stream=True,
                ),
            ) as response:
                assert response.status_code == 200

                # Collect events
                events: list[dict[str, Any]] = []
                decoder = SSEDecoder()
                async for chunk in response.aiter_raw():
                    for event in decoder.feed(chunk):
                        if event.data and event.data.strip() != "[DONE]":
                            try:
                                events.append(json.loads(event.data))
                            except json.JSONDecodeError:
                                pass

    # Verify we got all content despite fragmentation
    text_deltas = [
        e.get("delta", {}).get("text", "")
        for e in events
        if e.get("type") == "content_block_delta"
        and e.get("delta", {}).get("type") == "text_delta"
    ]
    assert "".join(text_deltas) == "Hello!"


@pytest.mark.asyncio
async def test_stream_adapter_multiple_events_per_chunk() -> None:
    """Test when multiple SSE events arrive in a single chunk."""
    upstream = FakeUpstream()

    # Create a response with many small chunks
    chunks = build_openai_stream_chunks("Hi", model="fake")

    upstream.enqueue(
        UpstreamResponse(
            stream=True,
            stream_events=chunks,
            # Not fragmented - events may be batched
        )
    )

    base_url = "http://upstream.local/v1"
    register_fake_upstream("upstream.local", upstream)

    config = build_messages_config(base_url)
    with ProxyHarness(config, enable_messages_endpoint=True) as harness:
        async with harness.make_async_client() as client:
            async with client.stream(
                "POST",
                "/v1/messages",
                json=build_anthropic_request(
                    messages=[{"role": "user", "content": "Hi"}],
                    max_tokens=100,
                    stream=True,
                ),
            ) as response:
                assert response.status_code == 200

                events: list[dict[str, Any]] = []
                decoder = SSEDecoder()
                async for chunk in response.aiter_raw():
                    for event in decoder.feed(chunk):
                        if event.data and event.data.strip() != "[DONE]":
                            try:
                                events.append(json.loads(event.data))
                            except json.JSONDecodeError:
                                pass

    # Should have proper event sequence
    assert events[0]["type"] == "message_start"
    assert events[-1]["type"] == "message_stop"


# =============================================================================
# Stream End Scenarios
# =============================================================================


@pytest.mark.asyncio
async def test_stream_adapter_no_done() -> None:
    """Test handling when stream ends without [DONE] sentinel."""
    upstream = FakeUpstream()

    chunks = build_openai_stream_chunks("Complete", model="fake")

    upstream.enqueue(
        UpstreamResponse(
            stream=True,
            stream_events=chunks,
            add_done=False,  # No [DONE] at end
        )
    )

    base_url = "http://upstream.local/v1"
    register_fake_upstream("upstream.local", upstream)

    config = build_messages_config(base_url)
    with ProxyHarness(config, enable_messages_endpoint=True) as harness:
        async with harness.make_async_client() as client:
            async with client.stream(
                "POST",
                "/v1/messages",
                json=build_anthropic_request(
                    messages=[{"role": "user", "content": "Hi"}],
                    max_tokens=100,
                    stream=True,
                ),
            ) as response:
                assert response.status_code == 200

                events: list[dict[str, Any]] = []
                decoder = SSEDecoder()
                async for chunk in response.aiter_raw():
                    for event in decoder.feed(chunk):
                        if event.data and event.data.strip() != "[DONE]":
                            try:
                                events.append(json.loads(event.data))
                            except json.JSONDecodeError:
                                pass

    # Should still have complete event sequence
    event_types = [e.get("type") for e in events]
    assert "message_start" in event_types
    assert "message_stop" in event_types

    # Content should be complete
    text_deltas = [
        e.get("delta", {}).get("text", "")
        for e in events
        if e.get("type") == "content_block_delta"
        and e.get("delta", {}).get("type") == "text_delta"
    ]
    assert "".join(text_deltas) == "Complete"


@pytest.mark.asyncio
async def test_stream_with_delay_between_chunks() -> None:
    """Test stream with delays between chunks."""
    upstream = FakeUpstream()

    chunks = build_openai_stream_chunks("Slow", model="fake")

    upstream.enqueue(
        UpstreamResponse(
            stream=True,
            stream_events=chunks,
            chunk_delay_s=0.01,  # 10ms delay between chunks
        )
    )

    base_url = "http://upstream.local/v1"
    register_fake_upstream("upstream.local", upstream)

    config = build_messages_config(base_url)
    with ProxyHarness(config, enable_messages_endpoint=True) as harness:
        async with harness.make_async_client() as client:
            async with client.stream(
                "POST",
                "/v1/messages",
                json=build_anthropic_request(
                    messages=[{"role": "user", "content": "Hi"}],
                    max_tokens=100,
                    stream=True,
                ),
            ) as response:
                assert response.status_code == 200

                events: list[dict[str, Any]] = []
                decoder = SSEDecoder()
                async for chunk in response.aiter_raw():
                    for event in decoder.feed(chunk):
                        if event.data and event.data.strip() != "[DONE]":
                            try:
                                events.append(json.loads(event.data))
                            except json.JSONDecodeError:
                                pass

    # Content should be complete despite delays
    text_deltas = [
        e.get("delta", {}).get("text", "")
        for e in events
        if e.get("type") == "content_block_delta"
        and e.get("delta", {}).get("type") == "text_delta"
    ]
    assert "".join(text_deltas) == "Slow"


# =============================================================================
# Tool Call Streaming Tests
# =============================================================================


@pytest.mark.asyncio
async def test_stream_adapter_interleaved_text_tools() -> None:
    """Test text followed by multiple tool calls."""
    upstream = FakeUpstream()

    chunks = build_openai_stream_chunks(
        "Let me help.",
        tool_calls=[
            {"function": {"name": "search", "arguments": {"q": "test"}}},
            {"function": {"name": "calc", "arguments": {"x": 1}}},
        ],
        finish_reason="tool_calls",
        model="fake",
    )

    upstream.enqueue(
        UpstreamResponse(
            stream=True,
            stream_events=chunks,
        )
    )

    base_url = "http://upstream.local/v1"
    register_fake_upstream("upstream.local", upstream)

    config = build_messages_config(base_url)
    with ProxyHarness(config, enable_messages_endpoint=True) as harness:
        async with harness.make_async_client() as client:
            async with client.stream(
                "POST",
                "/v1/messages",
                json=build_anthropic_request(
                    messages=[{"role": "user", "content": "Search and calc"}],
                    max_tokens=100,
                    stream=True,
                    tools=[
                        {
                            "name": "search",
                            "description": "Search",
                            "input_schema": {"type": "object"},
                        },
                        {
                            "name": "calc",
                            "description": "Calculate",
                            "input_schema": {"type": "object"},
                        },
                    ],
                ),
            ) as response:
                assert response.status_code == 200

                events: list[dict[str, Any]] = []
                decoder = SSEDecoder()
                async for chunk in response.aiter_raw():
                    for event in decoder.feed(chunk):
                        if event.data and event.data.strip() != "[DONE]":
                            try:
                                events.append(json.loads(event.data))
                            except json.JSONDecodeError:
                                pass

    # Verify content blocks
    content_starts = [e for e in events if e.get("type") == "content_block_start"]
    # Should have text block + 2 tool_use blocks
    assert len(content_starts) >= 2  # At least text + one tool

    # Check for tool_use blocks
    tool_use_starts = [
        e for e in content_starts if e.get("content_block", {}).get("type") == "tool_use"
    ]
    assert len(tool_use_starts) >= 1


@pytest.mark.asyncio
async def test_stream_adapter_tool_arguments_streaming() -> None:
    """Test when tool argument JSON is streamed."""
    upstream = FakeUpstream()

    chunks = build_openai_stream_chunks(
        "",
        tool_calls=[
            {
                "function": {
                    "name": "complex_tool",
                    "arguments": {"nested": {"value": 123, "items": [1, 2, 3]}},
                }
            }
        ],
        finish_reason="tool_calls",
        model="fake",
    )

    upstream.enqueue(
        UpstreamResponse(
            stream=True,
            stream_events=chunks,
        )
    )

    base_url = "http://upstream.local/v1"
    register_fake_upstream("upstream.local", upstream)

    config = build_messages_config(base_url)
    with ProxyHarness(config, enable_messages_endpoint=True) as harness:
        async with harness.make_async_client() as client:
            async with client.stream(
                "POST",
                "/v1/messages",
                json=build_anthropic_request(
                    messages=[{"role": "user", "content": "Use complex tool"}],
                    max_tokens=100,
                    stream=True,
                    tools=[
                        {
                            "name": "complex_tool",
                            "description": "Complex",
                            "input_schema": {"type": "object"},
                        }
                    ],
                ),
            ) as response:
                assert response.status_code == 200

                events: list[dict[str, Any]] = []
                decoder = SSEDecoder()
                async for chunk in response.aiter_raw():
                    for event in decoder.feed(chunk):
                        if event.data and event.data.strip() != "[DONE]":
                            try:
                                events.append(json.loads(event.data))
                            except json.JSONDecodeError:
                                pass

    # Verify tool_use content block exists
    tool_starts = [
        e
        for e in events
        if e.get("type") == "content_block_start"
        and e.get("content_block", {}).get("type") == "tool_use"
    ]
    assert len(tool_starts) >= 1
    assert tool_starts[0]["content_block"]["name"] == "complex_tool"


# =============================================================================
# Stop Reason Mapping Tests
# =============================================================================


@pytest.mark.asyncio
async def test_stream_adapter_stop_reason_mapping() -> None:
    """Test finish_reason maps correctly in streaming."""
    test_cases = [
        ("stop", "end_turn"),
        ("length", "max_tokens"),
        ("tool_calls", "tool_use"),
    ]

    for openai_reason, expected_anthropic in test_cases:
        upstream = FakeUpstream()
        chunks = build_openai_stream_chunks(
            "Response",
            finish_reason=openai_reason,
            model="fake",
        )

        upstream.enqueue(
            UpstreamResponse(stream=True, stream_events=chunks)
        )

        base_url = "http://upstream.local/v1"
        register_fake_upstream("upstream.local", upstream)

        config = build_messages_config(base_url)
        with ProxyHarness(config, enable_messages_endpoint=True) as harness:
            async with harness.make_async_client() as client:
                async with client.stream(
                    "POST",
                    "/v1/messages",
                    json=build_anthropic_request(
                        messages=[{"role": "user", "content": "Hi"}],
                        max_tokens=100,
                        stream=True,
                    ),
                ) as response:
                    events: list[dict[str, Any]] = []
                    decoder = SSEDecoder()
                    async for chunk in response.aiter_raw():
                        for event in decoder.feed(chunk):
                            if event.data and event.data.strip() != "[DONE]":
                                try:
                                    events.append(json.loads(event.data))
                                except json.JSONDecodeError:
                                    pass

        # Find message_delta event
        message_deltas = [e for e in events if e.get("type") == "message_delta"]
        assert len(message_deltas) > 0, f"No message_delta for {openai_reason}"

        actual_reason = message_deltas[-1].get("delta", {}).get("stop_reason")
        assert actual_reason == expected_anthropic, (
            f"Expected {expected_anthropic} for {openai_reason}, got {actual_reason}"
        )

        clear_upstream_transports()


# =============================================================================
# Usage Token Streaming Tests
# =============================================================================


@pytest.mark.asyncio
async def test_stream_adapter_usage_tokens() -> None:
    """Test usage tokens are included in stream."""
    upstream = FakeUpstream()
    chunks = build_openai_stream_chunks(
        "Response",
        include_usage=True,
        model="fake",
    )

    upstream.enqueue(
        UpstreamResponse(stream=True, stream_events=chunks)
    )

    base_url = "http://upstream.local/v1"
    register_fake_upstream("upstream.local", upstream)

    config = build_messages_config(base_url)
    with ProxyHarness(config, enable_messages_endpoint=True) as harness:
        async with harness.make_async_client() as client:
            async with client.stream(
                "POST",
                "/v1/messages",
                json=build_anthropic_request(
                    messages=[{"role": "user", "content": "Hi"}],
                    max_tokens=100,
                    stream=True,
                ),
            ) as response:
                events: list[dict[str, Any]] = []
                decoder = SSEDecoder()
                async for chunk in response.aiter_raw():
                    for event in decoder.feed(chunk):
                        if event.data and event.data.strip() != "[DONE]":
                            try:
                                events.append(json.loads(event.data))
                            except json.JSONDecodeError:
                                pass

    # Check message_start has input_tokens
    message_starts = [e for e in events if e.get("type") == "message_start"]
    assert len(message_starts) > 0
    usage = message_starts[0].get("message", {}).get("usage", {})
    assert "input_tokens" in usage

    # Check message_delta has output_tokens
    message_deltas = [e for e in events if e.get("type") == "message_delta"]
    if message_deltas:
        delta_usage = message_deltas[-1].get("usage", {})
        assert "output_tokens" in delta_usage


# =============================================================================
# Empty Response Streaming Tests
# =============================================================================


@pytest.mark.asyncio
async def test_stream_adapter_empty_content() -> None:
    """Test streaming response with empty content."""
    upstream = FakeUpstream()
    chunks = build_openai_stream_chunks(
        "",  # Empty content
        finish_reason="stop",
        model="fake",
    )

    upstream.enqueue(
        UpstreamResponse(stream=True, stream_events=chunks)
    )

    base_url = "http://upstream.local/v1"
    register_fake_upstream("upstream.local", upstream)

    config = build_messages_config(base_url)
    with ProxyHarness(config, enable_messages_endpoint=True) as harness:
        async with harness.make_async_client() as client:
            async with client.stream(
                "POST",
                "/v1/messages",
                json=build_anthropic_request(
                    messages=[{"role": "user", "content": "Hi"}],
                    max_tokens=100,
                    stream=True,
                ),
            ) as response:
                events: list[dict[str, Any]] = []
                decoder = SSEDecoder()
                async for chunk in response.aiter_raw():
                    for event in decoder.feed(chunk):
                        if event.data and event.data.strip() != "[DONE]":
                            try:
                                events.append(json.loads(event.data))
                            except json.JSONDecodeError:
                                pass

    # Should still have proper event sequence
    event_types = [e.get("type") for e in events]
    assert "message_start" in event_types
    assert "message_stop" in event_types
