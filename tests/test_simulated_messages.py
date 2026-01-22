"""Full simulation tests for the /v1/messages endpoint.

Tests the complete flow: Anthropic request -> translation -> OpenAI backend ->
translation back -> Anthropic response. Covers slot management, error handling,
and streaming scenarios.
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
    assert_anthropic_message_valid,
    assert_no_slot_leak,
    build_anthropic_request,
)


@pytest.fixture(autouse=True)
def _clear_transports():
    yield
    clear_upstream_transports()


# =============================================================================
# Happy Path Tests
# =============================================================================


@pytest.mark.asyncio
async def test_messages_to_openai_translation_nonstream() -> None:
    """Full non-streaming flow: Anthropic request -> translate -> OpenAI -> translate back."""
    upstream = FakeUpstream()
    upstream.enqueue_openai_chat_response(
        "Hello, I'm an assistant!",
        finish_reason="stop",
    )

    base_url = "http://upstream.local/v1"
    register_fake_upstream("upstream.local", upstream)

    config = build_messages_config(base_url)
    with ProxyHarness(config, enable_messages_endpoint=True) as harness:
        async with harness.make_async_client() as client:
            response = await client.post(
                "/v1/messages",
                json=build_anthropic_request(
                    messages=[{"role": "user", "content": "Hello"}],
                    max_tokens=100,
                ),
            )

    assert response.status_code == 200
    payload = response.json()

    # Validate structure
    assert_anthropic_message_valid(payload)
    assert payload["stop_reason"] == "end_turn"

    # Validate content
    content = payload["content"]
    assert len(content) == 1
    assert content[0]["type"] == "text"
    assert content[0]["text"] == "Hello, I'm an assistant!"

    # Verify upstream received translated request
    assert len(upstream.received) == 1
    received = upstream.received[0]["json"]
    assert "messages" in received
    assert received["messages"][0]["role"] == "user"


@pytest.mark.asyncio
async def test_messages_to_openai_translation_streaming() -> None:
    """Full streaming flow with SSE adaptation."""
    upstream = FakeUpstream()
    upstream.enqueue_openai_chat_response(
        "Hello!",
        stream=True,
        finish_reason="stop",
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

    # Verify event sequence
    event_types = [e.get("type") for e in events]
    assert event_types[0] == "message_start"
    assert "content_block_start" in event_types
    assert "content_block_delta" in event_types
    assert "message_delta" in event_types
    assert event_types[-1] == "message_stop"

    # Verify content accumulated
    text_deltas = [
        e.get("delta", {}).get("text", "")
        for e in events
        if e.get("type") == "content_block_delta"
        and e.get("delta", {}).get("type") == "text_delta"
    ]
    assert "".join(text_deltas) == "Hello!"


@pytest.mark.asyncio
async def test_messages_tool_use_roundtrip() -> None:
    """Test tool_use blocks translate correctly both directions."""
    upstream = FakeUpstream()
    upstream.enqueue_openai_chat_response(
        "",
        tool_calls=[
            {
                "id": "call_test123",
                "function": {"name": "get_weather", "arguments": {"location": "NYC"}},
            }
        ],
        finish_reason="tool_calls",
    )

    base_url = "http://upstream.local/v1"
    register_fake_upstream("upstream.local", upstream)

    config = build_messages_config(base_url)
    with ProxyHarness(config, enable_messages_endpoint=True) as harness:
        async with harness.make_async_client() as client:
            response = await client.post(
                "/v1/messages",
                json=build_anthropic_request(
                    messages=[{"role": "user", "content": "What's the weather?"}],
                    max_tokens=100,
                    tools=[
                        {
                            "name": "get_weather",
                            "description": "Get weather",
                            "input_schema": {
                                "type": "object",
                                "properties": {"location": {"type": "string"}},
                            },
                        }
                    ],
                ),
            )

    assert response.status_code == 200
    payload = response.json()

    # Verify tool_use in response
    assert payload["stop_reason"] == "tool_use"
    content = payload["content"]
    tool_uses = [b for b in content if b["type"] == "tool_use"]
    assert len(tool_uses) == 1
    assert tool_uses[0]["name"] == "get_weather"
    assert tool_uses[0]["input"] == {"location": "NYC"}


@pytest.mark.asyncio
async def test_messages_tool_result_translation() -> None:
    """Test tool_result in request translates to tool message."""
    upstream = FakeUpstream()
    upstream.enqueue_openai_chat_response(
        "The weather in NYC is sunny!",
        finish_reason="stop",
    )

    base_url = "http://upstream.local/v1"
    register_fake_upstream("upstream.local", upstream)

    config = build_messages_config(base_url)
    with ProxyHarness(config, enable_messages_endpoint=True) as harness:
        async with harness.make_async_client() as client:
            response = await client.post(
                "/v1/messages",
                json=build_anthropic_request(
                    messages=[
                        {"role": "user", "content": "What's the weather?"},
                        {
                            "role": "assistant",
                            "content": [
                                {
                                    "type": "tool_use",
                                    "id": "toolu_123",
                                    "name": "get_weather",
                                    "input": {"location": "NYC"},
                                }
                            ],
                        },
                        {
                            "role": "user",
                            "content": [
                                {
                                    "type": "tool_result",
                                    "tool_use_id": "toolu_123",
                                    "content": "Sunny, 72F",
                                }
                            ],
                        },
                    ],
                    max_tokens=100,
                ),
            )

    assert response.status_code == 200

    # Verify upstream received tool message
    received = upstream.received[0]["json"]
    messages = received["messages"]
    # Should have: user, assistant (with tool_calls), tool (result)
    assert len(messages) >= 3


# =============================================================================
# Error Path Tests - Slot Release Verification
# =============================================================================


@pytest.mark.asyncio
async def test_messages_backend_error_releases_slot() -> None:
    """Verify slot released when backend returns error."""
    from src.concurrency import get_concurrency_manager, reset_concurrency_manager

    reset_concurrency_manager()

    upstream = FakeUpstream()
    upstream.enqueue_error_response(
        status_code=500,
        error_type="internal_error",
        message="Backend exploded",
        anthropic_format=False,
    )

    base_url = "http://upstream.local/v1"
    register_fake_upstream("upstream.local", upstream)

    config = build_messages_config(base_url, concurrency_limit=5)
    with ProxyHarness(
        config,
        enable_messages_endpoint=True,
        enable_concurrency=True,
        reset_concurrency=False,  # We want to inspect state
    ) as harness:
        async with harness.make_async_client() as client:
            response = await client.post(
                "/v1/messages",
                json=build_anthropic_request(
                    messages=[{"role": "user", "content": "Hello"}],
                    max_tokens=100,
                ),
            )

        # Request should fail
        assert response.status_code == 500

        # Verify slot was released
        await assert_no_slot_leak(harness, timeout=2.0)

    reset_concurrency_manager()


@pytest.mark.asyncio
async def test_messages_invalid_model_releases_slot() -> None:
    """Verify slot released when model not found."""
    from src.concurrency import reset_concurrency_manager

    reset_concurrency_manager()

    upstream = FakeUpstream()
    # No response queued - shouldn't reach upstream

    base_url = "http://upstream.local/v1"
    register_fake_upstream("upstream.local", upstream)

    config = build_messages_config(base_url, concurrency_limit=5)
    with ProxyHarness(
        config,
        enable_messages_endpoint=True,
        enable_concurrency=True,
        reset_concurrency=False,
    ) as harness:
        async with harness.make_async_client() as client:
            response = await client.post(
                "/v1/messages",
                json=build_anthropic_request(
                    messages=[{"role": "user", "content": "Hello"}],
                    max_tokens=100,
                    model="nonexistent-model",
                ),
            )

        # Should get error
        assert response.status_code in (400, 404)

        # Verify no slot leak
        await assert_no_slot_leak(harness, timeout=2.0)

    reset_concurrency_manager()


@pytest.mark.asyncio
async def test_messages_stream_slot_release_on_completion() -> None:
    """Verify slot released only after stream fully consumed."""
    from src.concurrency import reset_concurrency_manager

    reset_concurrency_manager()

    upstream = FakeUpstream()
    upstream.enqueue_openai_chat_response(
        "Hello!",
        stream=True,
        chunk_delay_s=0.01,  # Small delay to extend stream duration
    )

    base_url = "http://upstream.local/v1"
    register_fake_upstream("upstream.local", upstream)

    config = build_messages_config(base_url, concurrency_limit=5)
    with ProxyHarness(
        config,
        enable_messages_endpoint=True,
        enable_concurrency=True,
        reset_concurrency=False,
    ) as harness:
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

                # Consume stream
                async for _ in response.aiter_raw():
                    pass

        # After stream complete, slot should be released
        await assert_no_slot_leak(harness, timeout=2.0)

    reset_concurrency_manager()


@pytest.mark.asyncio
async def test_messages_slot_release_on_all_error_paths() -> None:
    """Systematic test of slot release on every error path."""
    from src.concurrency import reset_concurrency_manager

    test_cases = [
        # (description, request_modifier, expected_status_range)
        # Note: Some errors may propagate as 500 from backend failures
        ("invalid_json_body", lambda r: {"broken": "request"}, (400, 500)),
        ("missing_messages", lambda r: {"model": "test-model", "max_tokens": 100}, (400, 500)),
        ("missing_max_tokens", lambda r: {"model": "test-model", "messages": []}, (400, 500)),
    ]

    for description, modifier, status_range in test_cases:
        reset_concurrency_manager()

        upstream = FakeUpstream()
        base_url = "http://upstream.local/v1"
        register_fake_upstream("upstream.local", upstream)

        config = build_messages_config(base_url, concurrency_limit=5)
        with ProxyHarness(
            config,
            enable_messages_endpoint=True,
            enable_concurrency=True,
            reset_concurrency=False,
        ) as harness:
            async with harness.make_async_client() as client:
                base_request = build_anthropic_request(
                    messages=[{"role": "user", "content": "Hi"}],
                    max_tokens=100,
                )
                request_body = modifier(base_request)

                response = await client.post("/v1/messages", json=request_body)

            # Should get error in expected range
            assert status_range[0] <= response.status_code <= status_range[1], (
                f"{description}: expected status {status_range}, got {response.status_code}"
            )

            # Verify no slot leak
            try:
                await assert_no_slot_leak(harness, timeout=2.0)
            except AssertionError as e:
                raise AssertionError(f"{description}: {e}") from e

        reset_concurrency_manager()
        clear_upstream_transports()


# =============================================================================
# Anthropic Backend Passthrough Test
# =============================================================================


@pytest.mark.asyncio
@pytest.mark.skip(reason="Anthropic backend passthrough not yet implemented - requires /v1/messages route support")
async def test_messages_anthropic_backend_passthrough() -> None:
    """When backend is anthropic type, request passes through without translation."""
    # TODO: Implement when anthropic backend passthrough is supported
    # This test verifies that requests to anthropic-type backends go directly
    # to /v1/messages without OpenAI translation
    upstream = FakeUpstream(route="/v1/messages")
    upstream.enqueue(
        UpstreamResponse(
            json_body={
                "id": "msg_test123",
                "type": "message",
                "role": "assistant",
                "model": "claude-3-sonnet",
                "content": [{"type": "text", "text": "Hello from Anthropic!"}],
                "stop_reason": "end_turn",
                "stop_sequence": None,
                "usage": {"input_tokens": 10, "output_tokens": 5},
            }
        )
    )

    base_url = "http://upstream.local/v1"
    register_fake_upstream("upstream.local", upstream)

    # Use anthropic backend type
    config = build_messages_config(base_url, backend_type="anthropic")
    with ProxyHarness(config, enable_messages_endpoint=True) as harness:
        async with harness.make_async_client() as client:
            response = await client.post(
                "/v1/messages",
                json=build_anthropic_request(
                    messages=[{"role": "user", "content": "Hello"}],
                    max_tokens=100,
                ),
            )

    assert response.status_code == 200
    payload = response.json()

    # Should pass through unchanged
    assert payload["content"][0]["text"] == "Hello from Anthropic!"


# =============================================================================
# System Message Translation Tests
# =============================================================================


@pytest.mark.asyncio
async def test_messages_system_string_translation() -> None:
    """Test system prompt as string translates correctly."""
    upstream = FakeUpstream()
    upstream.enqueue_openai_chat_response("I am a helpful assistant!")

    base_url = "http://upstream.local/v1"
    register_fake_upstream("upstream.local", upstream)

    config = build_messages_config(base_url)
    with ProxyHarness(config, enable_messages_endpoint=True) as harness:
        async with harness.make_async_client() as client:
            response = await client.post(
                "/v1/messages",
                json=build_anthropic_request(
                    messages=[{"role": "user", "content": "Who are you?"}],
                    max_tokens=100,
                    system="You are a helpful assistant.",
                ),
            )

    assert response.status_code == 200

    # Verify upstream received system message
    received = upstream.received[0]["json"]
    messages = received["messages"]
    # First message should be system
    assert messages[0]["role"] == "system"
    assert messages[0]["content"] == "You are a helpful assistant."


@pytest.mark.asyncio
async def test_messages_system_blocks_translation() -> None:
    """Test system prompt as content blocks translates correctly."""
    upstream = FakeUpstream()
    upstream.enqueue_openai_chat_response("I am a code expert!")

    base_url = "http://upstream.local/v1"
    register_fake_upstream("upstream.local", upstream)

    config = build_messages_config(base_url)
    with ProxyHarness(config, enable_messages_endpoint=True) as harness:
        async with harness.make_async_client() as client:
            response = await client.post(
                "/v1/messages",
                json=build_anthropic_request(
                    messages=[{"role": "user", "content": "Help me code"}],
                    max_tokens=100,
                    system=[
                        {"type": "text", "text": "You are a code expert."},
                        {"type": "text", "text": "Always explain your code."},
                    ],
                ),
            )

    assert response.status_code == 200

    # Verify upstream received joined system message
    received = upstream.received[0]["json"]
    messages = received["messages"]
    assert messages[0]["role"] == "system"
    # Should be joined with newlines or space
    assert "code expert" in messages[0]["content"].lower()


# =============================================================================
# Parameter Translation Tests
# =============================================================================


@pytest.mark.asyncio
async def test_messages_parameters_translation() -> None:
    """Test temperature, top_p, stop_sequences translate correctly."""
    upstream = FakeUpstream()
    upstream.enqueue_openai_chat_response("Response with params")

    base_url = "http://upstream.local/v1"
    register_fake_upstream("upstream.local", upstream)

    config = build_messages_config(base_url)
    with ProxyHarness(config, enable_messages_endpoint=True) as harness:
        async with harness.make_async_client() as client:
            response = await client.post(
                "/v1/messages",
                json=build_anthropic_request(
                    messages=[{"role": "user", "content": "Hi"}],
                    max_tokens=100,
                    temperature=0.7,
                    top_p=0.9,
                    stop_sequences=["STOP", "END"],
                ),
            )

    assert response.status_code == 200

    # Verify parameters translated
    received = upstream.received[0]["json"]
    assert received.get("temperature") == 0.7
    assert received.get("top_p") == 0.9
    assert received.get("stop") == ["STOP", "END"]


@pytest.mark.asyncio
async def test_messages_max_tokens_translation() -> None:
    """Test max_tokens translates and length stop_reason maps correctly."""
    upstream = FakeUpstream()
    upstream.enqueue_openai_chat_response(
        "Truncated response...",
        finish_reason="length",
    )

    base_url = "http://upstream.local/v1"
    register_fake_upstream("upstream.local", upstream)

    config = build_messages_config(base_url)
    with ProxyHarness(config, enable_messages_endpoint=True) as harness:
        async with harness.make_async_client() as client:
            response = await client.post(
                "/v1/messages",
                json=build_anthropic_request(
                    messages=[{"role": "user", "content": "Write a long story"}],
                    max_tokens=50,
                ),
            )

    assert response.status_code == 200
    payload = response.json()

    # Verify max_tokens was sent
    received = upstream.received[0]["json"]
    assert received.get("max_tokens") == 50

    # Verify length -> max_tokens mapping
    assert payload["stop_reason"] == "max_tokens"


# =============================================================================
# Content Filter Tests
# =============================================================================


@pytest.mark.asyncio
async def test_messages_content_filter_translation() -> None:
    """Test content_filter finish reason maps correctly."""
    upstream = FakeUpstream()
    upstream.enqueue_openai_chat_response(
        "",
        finish_reason="content_filter",
    )

    base_url = "http://upstream.local/v1"
    register_fake_upstream("upstream.local", upstream)

    config = build_messages_config(base_url)
    with ProxyHarness(config, enable_messages_endpoint=True) as harness:
        async with harness.make_async_client() as client:
            response = await client.post(
                "/v1/messages",
                json=build_anthropic_request(
                    messages=[{"role": "user", "content": "Filtered content"}],
                    max_tokens=100,
                ),
            )

    assert response.status_code == 200
    payload = response.json()

    # content_filter may map to refusal, end_turn, or content_filter depending on implementation
    assert payload["stop_reason"] in ("end_turn", "content_filter", "refusal")
