"""Full simulation tests for the /v1/responses endpoint.

Tests the complete flow: Responses API request -> translation -> OpenAI backend ->
translation back -> Responses API response. Covers statefulness, conversation
chaining, stream adapter states, and error handling.
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

from conftest import build_responses_config, register_fake_upstream
from src.core.upstream_transport import clear_upstream_transports
from src.modules.response_pipeline import SSEDecoder
from src.responses.state_store import get_state_store, reset_state_store
from src.testing import (
    FakeUpstream,
    ProxyHarness,
    UpstreamResponse,
    assert_responses_api_valid,
    assert_responses_has_tool_calls,
    assert_responses_output_text_equals,
    assert_responses_sse_valid,
    assert_no_slot_leak,
    build_responses_request,
)


@pytest.fixture(autouse=True)
def _clear_transports():
    yield
    clear_upstream_transports()


@pytest.fixture(autouse=True)
def _reset_state_store():
    """Reset state store before and after each test."""
    reset_state_store()
    yield
    reset_state_store()


# =============================================================================
# Basic Lifecycle Tests
# =============================================================================


@pytest.mark.asyncio
async def test_responses_basic_nonstream() -> None:
    """Full non-streaming flow: Responses request -> translate -> OpenAI -> translate back."""
    upstream = FakeUpstream()
    upstream.enqueue_openai_chat_response(
        "Hello, I'm an assistant!",
        finish_reason="stop",
    )

    base_url = "http://upstream.local/v1"
    register_fake_upstream("upstream.local", upstream)

    config = build_responses_config(base_url)
    with ProxyHarness(config, enable_responses_endpoint=True) as harness:
        async with harness.make_async_client() as client:
            response = await client.post(
                "/v1/responses",
                json=build_responses_request(
                    input_="Hello",
                    model="test-model",
                    stream=False,
                ),
            )

    assert response.status_code == 200
    payload = response.json()

    # Validate structure
    assert_responses_api_valid(payload)
    assert payload["status"] == "completed"

    # Validate content
    assert_responses_output_text_equals(payload, "Hello, I'm an assistant!")

    # Verify output items
    assert len(payload["output"]) >= 1
    message_item = payload["output"][0]
    assert message_item["type"] == "message"
    assert message_item["role"] == "assistant"

    # Verify upstream received translated request
    assert len(upstream.received) == 1
    received = upstream.received[0]["json"]
    assert "messages" in received
    assert received["messages"][0]["role"] == "user"


@pytest.mark.asyncio
async def test_responses_basic_stream() -> None:
    """Full streaming flow with SSE adaptation."""
    upstream = FakeUpstream()
    upstream.enqueue_openai_chat_response(
        "Hello!",
        stream=True,
        finish_reason="stop",
    )

    base_url = "http://upstream.local/v1"
    register_fake_upstream("upstream.local", upstream)

    config = build_responses_config(base_url)
    with ProxyHarness(config, enable_responses_endpoint=True) as harness:
        async with harness.make_async_client() as client:
            async with client.stream(
                "POST",
                "/v1/responses",
                json=build_responses_request(
                    input_="Hi",
                    model="test-model",
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

    # Validate event sequence
    assert_responses_sse_valid(events)

    # Check event types present
    event_types = [e.get("type") for e in events]
    assert "response.created" in event_types
    assert "response.in_progress" in event_types
    assert "response.output_item.added" in event_types
    assert "response.output_text.delta" in event_types
    assert "response.completed" in event_types

    # Verify accumulated text from deltas
    text_deltas = [
        e.get("delta", "")
        for e in events
        if e.get("type") == "response.output_text.delta"
    ]
    assert "".join(text_deltas) == "Hello!"


@pytest.mark.asyncio
async def test_responses_with_tools_nonstream() -> None:
    """Test tool_calls in response are translated to function_call items."""
    upstream = FakeUpstream()
    upstream.enqueue_openai_chat_response(
        "",
        tool_calls=[
            {
                "id": "call_123",
                "function": {
                    "name": "get_weather",
                    "arguments": {"location": "NYC"},
                },
            }
        ],
        finish_reason="tool_calls",
    )

    base_url = "http://upstream.local/v1"
    register_fake_upstream("upstream.local", upstream)

    config = build_responses_config(base_url)
    with ProxyHarness(config, enable_responses_endpoint=True) as harness:
        async with harness.make_async_client() as client:
            response = await client.post(
                "/v1/responses",
                json=build_responses_request(
                    input_="What's the weather?",
                    model="test-model",
                    tools=[
                        {
                            "type": "function",
                            "function": {
                                "name": "get_weather",
                                "description": "Get weather for a location",
                                "parameters": {
                                    "type": "object",
                                    "properties": {
                                        "location": {"type": "string"}
                                    },
                                },
                            },
                        }
                    ],
                ),
            )

    assert response.status_code == 200
    payload = response.json()

    # Validate structure
    assert_responses_api_valid(payload)
    assert payload["status"] == "completed"

    # Validate function call items
    assert_responses_has_tool_calls(payload, expected_count=1, expected_names=["get_weather"])

    function_call = next(
        item for item in payload["output"] if item["type"] == "function_call"
    )
    assert function_call["name"] == "get_weather"
    assert "location" in function_call["arguments"]


@pytest.mark.asyncio
async def test_responses_with_tools_stream() -> None:
    """Test streaming response with tool calls."""
    upstream = FakeUpstream()
    upstream.enqueue_openai_chat_response(
        "",
        tool_calls=[
            {
                "id": "call_abc",
                "function": {
                    "name": "search",
                    "arguments": {"query": "test"},
                },
            }
        ],
        stream=True,
        finish_reason="tool_calls",
    )

    base_url = "http://upstream.local/v1"
    register_fake_upstream("upstream.local", upstream)

    config = build_responses_config(base_url)
    with ProxyHarness(config, enable_responses_endpoint=True) as harness:
        async with harness.make_async_client() as client:
            async with client.stream(
                "POST",
                "/v1/responses",
                json=build_responses_request(
                    input_="Search for test",
                    model="test-model",
                    stream=True,
                    tools=[
                        {
                            "type": "function",
                            "function": {
                                "name": "search",
                                "description": "Search for something",
                                "parameters": {"type": "object", "properties": {"query": {"type": "string"}}},
                            },
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

    # Check for function call events
    event_types = [e.get("type") for e in events]
    assert "response.completed" in event_types

    # Final event should be completed
    final_event = events[-1]
    assert final_event["type"] == "response.completed"


# =============================================================================
# ResponseStateStore Tests (Statefulness)
# =============================================================================


@pytest.mark.asyncio
async def test_responses_store_saves_response() -> None:
    """Verify store=True persists response to state store."""
    upstream = FakeUpstream()
    upstream.enqueue_openai_chat_response("Stored response!", finish_reason="stop")

    base_url = "http://upstream.local/v1"
    register_fake_upstream("upstream.local", upstream)

    config = build_responses_config(base_url)
    with ProxyHarness(config, enable_responses_endpoint=True) as harness:
        async with harness.make_async_client() as client:
            response = await client.post(
                "/v1/responses",
                json=build_responses_request(
                    input_="Hello",
                    model="test-model",
                    store=True,
                ),
            )

    assert response.status_code == 200
    payload = response.json()
    response_id = payload["id"]

    # Check state store contains the response
    state_store = get_state_store()
    stored = await state_store.get_response(response_id)

    assert stored is not None, f"Response {response_id} not found in state store"
    assert stored["id"] == response_id
    assert stored["status"] == "completed"


@pytest.mark.asyncio
async def test_responses_store_lru_eviction() -> None:
    """Verify LRU eviction works correctly with small cache."""
    from src.responses.state_store import ResponseStateStore

    # Create state store with small cache
    small_store = ResponseStateStore(max_entries=3)

    # Store 5 responses
    for i in range(5):
        response = {
            "id": f"resp_{i}",
            "status": "completed",
            "output": [],
        }
        await small_store.store_response(response, f"input_{i}")

    # First 2 should be evicted
    assert await small_store.get_response("resp_0") is None
    assert await small_store.get_response("resp_1") is None

    # Last 3 should still be present
    assert await small_store.get_response("resp_2") is not None
    assert await small_store.get_response("resp_3") is not None
    assert await small_store.get_response("resp_4") is not None

    # Check cache stats
    stats = small_store.get_cache_stats()
    assert stats["memory_entries"] == 3
    assert stats["max_entries"] == 3


@pytest.mark.asyncio
async def test_responses_previous_response_id_retrieval() -> None:
    """Test chaining 2 requests via previous_response_id."""
    upstream = FakeUpstream()
    upstream.enqueue_openai_chat_response("First response!", finish_reason="stop")
    upstream.enqueue_openai_chat_response("Second response with context!", finish_reason="stop")

    base_url = "http://upstream.local/v1"
    register_fake_upstream("upstream.local", upstream)

    config = build_responses_config(base_url)
    with ProxyHarness(config, enable_responses_endpoint=True) as harness:
        async with harness.make_async_client() as client:
            # First request with store=True
            response1 = await client.post(
                "/v1/responses",
                json=build_responses_request(
                    input_="Hello",
                    model="test-model",
                    store=True,
                ),
            )
            payload1 = response1.json()
            resp1_id = payload1["id"]

            # Second request with previous_response_id
            response2 = await client.post(
                "/v1/responses",
                json=build_responses_request(
                    input_="How are you?",
                    model="test-model",
                    previous_response_id=resp1_id,
                ),
            )

    assert response2.status_code == 200
    payload2 = response2.json()

    # Verify second response is valid
    assert_responses_api_valid(payload2)
    assert_responses_output_text_equals(payload2, "Second response with context!")

    # Verify upstream received 2 requests
    assert len(upstream.received) == 2

    # Second request should have conversation history
    second_request = upstream.received[1]["json"]
    messages = second_request.get("messages", [])

    # Should have: user (from first), assistant (first response), user (from second)
    assert len(messages) >= 2, f"Expected at least 2 messages, got {len(messages)}"


@pytest.mark.asyncio
async def test_responses_conversation_chain_3_turns() -> None:
    """Test 3-turn conversation chain with previous_response_id."""
    upstream = FakeUpstream()
    upstream.enqueue_openai_chat_response("Hi there!", finish_reason="stop")
    upstream.enqueue_openai_chat_response("I'm good, thanks!", finish_reason="stop")
    upstream.enqueue_openai_chat_response("Goodbye!", finish_reason="stop")

    base_url = "http://upstream.local/v1"
    register_fake_upstream("upstream.local", upstream)

    config = build_responses_config(base_url)
    with ProxyHarness(config, enable_responses_endpoint=True) as harness:
        async with harness.make_async_client() as client:
            # Turn 1
            r1 = await client.post(
                "/v1/responses",
                json=build_responses_request(
                    input_="Hello",
                    model="test-model",
                    store=True,
                ),
            )
            id1 = r1.json()["id"]

            # Turn 2
            r2 = await client.post(
                "/v1/responses",
                json=build_responses_request(
                    input_="How are you?",
                    model="test-model",
                    previous_response_id=id1,
                    store=True,
                ),
            )
            id2 = r2.json()["id"]

            # Turn 3
            r3 = await client.post(
                "/v1/responses",
                json=build_responses_request(
                    input_="Bye!",
                    model="test-model",
                    previous_response_id=id2,
                ),
            )

    assert r3.status_code == 200

    # Verify third request has full history
    third_request = upstream.received[2]["json"]
    messages = third_request.get("messages", [])

    # Should have: user1, assistant1, user2, assistant2, user3 = 5 messages
    assert len(messages) >= 4, f"Expected at least 4 messages in history, got {len(messages)}"


@pytest.mark.asyncio
async def test_responses_previous_response_id_not_found() -> None:
    """Test graceful handling of nonexistent previous_response_id."""
    upstream = FakeUpstream()
    upstream.enqueue_openai_chat_response("Still works!", finish_reason="stop")

    base_url = "http://upstream.local/v1"
    register_fake_upstream("upstream.local", upstream)

    config = build_responses_config(base_url)
    with ProxyHarness(config, enable_responses_endpoint=True) as harness:
        async with harness.make_async_client() as client:
            response = await client.post(
                "/v1/responses",
                json=build_responses_request(
                    input_="Hello",
                    model="test-model",
                    previous_response_id="resp_nonexistent_12345",
                ),
            )

    # Should still work, just without history
    assert response.status_code == 200
    payload = response.json()
    assert_responses_api_valid(payload)


@pytest.mark.asyncio
async def test_responses_store_with_streaming() -> None:
    """Test that store=True works correctly with streaming responses."""
    upstream = FakeUpstream()
    upstream.enqueue_openai_chat_response(
        "Streamed and stored!",
        stream=True,
        finish_reason="stop",
    )

    base_url = "http://upstream.local/v1"
    register_fake_upstream("upstream.local", upstream)

    config = build_responses_config(base_url)
    response_id = None

    with ProxyHarness(config, enable_responses_endpoint=True) as harness:
        async with harness.make_async_client() as client:
            async with client.stream(
                "POST",
                "/v1/responses",
                json=build_responses_request(
                    input_="Hello",
                    model="test-model",
                    stream=True,
                    store=True,
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

                # Get response ID from created event
                for event in events:
                    if event.get("type") == "response.created":
                        response_id = event.get("response", {}).get("id")
                        break

    # Wait a tiny bit for async store to complete
    await asyncio.sleep(0.1)

    # Verify response was stored
    assert response_id is not None
    state_store = get_state_store()
    stored = await state_store.get_response(response_id)

    assert stored is not None, f"Streamed response {response_id} not found in state store"
    assert stored["status"] == "completed"


# =============================================================================
# ChatToResponsesStreamAdapter State Tests
# =============================================================================


@pytest.mark.asyncio
async def test_stream_adapter_finish_reason_stop() -> None:
    """Test status='completed' when finish_reason='stop'."""
    upstream = FakeUpstream()
    upstream.enqueue_openai_chat_response(
        "Complete!",
        stream=True,
        finish_reason="stop",
    )

    base_url = "http://upstream.local/v1"
    register_fake_upstream("upstream.local", upstream)

    config = build_responses_config(base_url)
    with ProxyHarness(config, enable_responses_endpoint=True) as harness:
        async with harness.make_async_client() as client:
            async with client.stream(
                "POST",
                "/v1/responses",
                json=build_responses_request(
                    input_="Test",
                    model="test-model",
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

    # Final event should be completed
    final_event = events[-1]
    assert final_event["type"] == "response.completed"
    assert final_event.get("response", {}).get("status") == "completed"


@pytest.mark.asyncio
async def test_stream_adapter_finish_reason_length() -> None:
    """Test status='incomplete' when finish_reason='length'."""
    upstream = FakeUpstream()
    upstream.enqueue_openai_chat_response(
        "Truncated output...",
        stream=True,
        finish_reason="length",
    )

    base_url = "http://upstream.local/v1"
    register_fake_upstream("upstream.local", upstream)

    config = build_responses_config(base_url)
    with ProxyHarness(config, enable_responses_endpoint=True) as harness:
        async with harness.make_async_client() as client:
            async with client.stream(
                "POST",
                "/v1/responses",
                json=build_responses_request(
                    input_="Test",
                    model="test-model",
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

    # Final event should be incomplete
    final_event = events[-1]
    assert final_event["type"] == "response.incomplete"
    assert final_event.get("response", {}).get("status") == "incomplete"


@pytest.mark.asyncio
async def test_stream_adapter_finish_reason_content_filter() -> None:
    """Test status='failed' when finish_reason='content_filter'."""
    upstream = FakeUpstream()
    upstream.enqueue_openai_chat_response(
        "",
        stream=True,
        finish_reason="content_filter",
    )

    base_url = "http://upstream.local/v1"
    register_fake_upstream("upstream.local", upstream)

    config = build_responses_config(base_url)
    with ProxyHarness(config, enable_responses_endpoint=True) as harness:
        async with harness.make_async_client() as client:
            async with client.stream(
                "POST",
                "/v1/responses",
                json=build_responses_request(
                    input_="Test",
                    model="test-model",
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

    # Final event should be failed
    final_event = events[-1]
    assert final_event["type"] == "response.failed"
    assert final_event.get("response", {}).get("status") == "failed"


@pytest.mark.asyncio
async def test_stream_adapter_sequence_numbers() -> None:
    """Verify sequence numbers are monotonically increasing."""
    upstream = FakeUpstream()
    upstream.enqueue_openai_chat_response(
        "Testing sequence numbers",
        stream=True,
        finish_reason="stop",
    )

    base_url = "http://upstream.local/v1"
    register_fake_upstream("upstream.local", upstream)

    config = build_responses_config(base_url)
    with ProxyHarness(config, enable_responses_endpoint=True) as harness:
        async with harness.make_async_client() as client:
            async with client.stream(
                "POST",
                "/v1/responses",
                json=build_responses_request(
                    input_="Test",
                    model="test-model",
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

    # Extract sequence numbers
    sequence_numbers = [
        e.get("sequence_number")
        for e in events
        if "sequence_number" in e
    ]

    # Verify monotonically increasing
    assert len(sequence_numbers) > 0, "No sequence numbers found"
    for i in range(1, len(sequence_numbers)):
        assert sequence_numbers[i] > sequence_numbers[i - 1], (
            f"Sequence not monotonic at index {i}: "
            f"{sequence_numbers[i - 1]} -> {sequence_numbers[i]}"
        )


@pytest.mark.asyncio
async def test_stream_adapter_multiple_tool_calls() -> None:
    """Test streaming response with multiple tool calls."""
    upstream = FakeUpstream()
    upstream.enqueue_openai_chat_response(
        "",
        tool_calls=[
            {"id": "call_1", "function": {"name": "search", "arguments": {"q": "a"}}},
            {"id": "call_2", "function": {"name": "get_info", "arguments": {"id": 1}}},
            {"id": "call_3", "function": {"name": "compute", "arguments": {"x": 5}}},
        ],
        stream=True,
        finish_reason="tool_calls",
    )

    base_url = "http://upstream.local/v1"
    register_fake_upstream("upstream.local", upstream)

    config = build_responses_config(base_url)
    with ProxyHarness(config, enable_responses_endpoint=True) as harness:
        async with harness.make_async_client() as client:
            async with client.stream(
                "POST",
                "/v1/responses",
                json=build_responses_request(
                    input_="Do multiple things",
                    model="test-model",
                    stream=True,
                    tools=[
                        {"type": "function", "function": {"name": "search", "parameters": {}}},
                        {"type": "function", "function": {"name": "get_info", "parameters": {}}},
                        {"type": "function", "function": {"name": "compute", "parameters": {}}},
                    ],
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

    # Should complete successfully
    final_event = events[-1]
    assert final_event["type"] == "response.completed"


# =============================================================================
# Error Handling Tests
# =============================================================================


@pytest.mark.asyncio
async def test_responses_invalid_model() -> None:
    """Test 400 error for invalid/unknown model."""
    upstream = FakeUpstream()
    # No response queued - should fail before reaching upstream

    base_url = "http://upstream.local/v1"
    register_fake_upstream("upstream.local", upstream)

    config = build_responses_config(base_url)
    with ProxyHarness(config, enable_responses_endpoint=True) as harness:
        async with harness.make_async_client() as client:
            response = await client.post(
                "/v1/responses",
                json=build_responses_request(
                    input_="Hello",
                    model="nonexistent-model-12345",
                ),
            )

    # Should return error
    assert response.status_code in (400, 404, 422)
    payload = response.json()
    # Error can be in "error" or "detail.error"
    assert "error" in payload or ("detail" in payload and "error" in payload.get("detail", {}))


@pytest.mark.asyncio
async def test_responses_backend_error() -> None:
    """Test backend 500 error is translated to Responses API error."""
    upstream = FakeUpstream()
    upstream.enqueue(
        UpstreamResponse(
            status_code=500,
            json_body={"error": {"message": "Internal server error", "type": "server_error"}},
        )
    )

    base_url = "http://upstream.local/v1"
    register_fake_upstream("upstream.local", upstream)

    config = build_responses_config(base_url)
    with ProxyHarness(config, enable_responses_endpoint=True) as harness:
        async with harness.make_async_client() as client:
            response = await client.post(
                "/v1/responses",
                json=build_responses_request(
                    input_="Hello",
                    model="test-model",
                ),
            )

    # Should return error response
    assert response.status_code >= 400
    payload = response.json()
    # Either error format or failed response status
    assert "error" in payload or payload.get("status") == "failed"


@pytest.mark.asyncio
async def test_responses_slot_release_on_error(
    reset_concurrency: None,  # From conftest.py
) -> None:
    """Test that concurrency slot is released on error."""
    upstream = FakeUpstream()
    upstream.enqueue(
        UpstreamResponse(
            status_code=500,
            json_body={"error": {"message": "Error"}},
        )
    )

    base_url = "http://upstream.local/v1"
    register_fake_upstream("upstream.local", upstream)

    config = build_responses_config(base_url, concurrency_limit=5)
    with ProxyHarness(
        config,
        enable_responses_endpoint=True,
        enable_concurrency=True,
        reset_concurrency=True,
    ) as harness:
        async with harness.make_async_client() as client:
            response = await client.post(
                "/v1/responses",
                json=build_responses_request(
                    input_="Hello",
                    model="test-model",
                ),
            )

        # Should return error
        assert response.status_code >= 400

        # Verify no slot leak
        await assert_no_slot_leak(harness)
