"""Full simulation tests for translator edge cases.

Tests translator behavior through the full proxy stack where interactions
are more realistic than isolated unit tests.
"""

from __future__ import annotations

import json
import sys
from pathlib import Path
from typing import Any

import httpx
import pytest

sys.path.insert(0, str(Path(__file__).parent.parent / "src"))

from conftest import build_messages_config, register_fake_upstream
from src.core.upstream_transport import clear_upstream_transports
from src.testing import (
    FakeUpstream,
    ProxyHarness,
    UpstreamResponse,
    assert_anthropic_message_valid,
    build_anthropic_request,
)


@pytest.fixture(autouse=True)
def _clear_transports():
    yield
    clear_upstream_transports()


# =============================================================================
# Image Content Block Tests
# =============================================================================


@pytest.mark.asyncio
async def test_translator_image_base64_url() -> None:
    """Test base64 image URL translates correctly to OpenAI."""
    upstream = FakeUpstream()
    upstream.enqueue_openai_chat_response("I see an image of a cat!")

    base_url = "http://upstream.local/v1"
    register_fake_upstream("upstream.local", upstream)

    config = build_messages_config(base_url)
    with ProxyHarness(config, enable_messages_endpoint=True) as harness:
        async with harness.make_async_client() as client:
            # Minimal valid base64 PNG header
            fake_base64 = "iVBORw0KGgoAAAANSUhEUg=="

            response = await client.post(
                "/v1/messages",
                json=build_anthropic_request(
                    messages=[
                        {
                            "role": "user",
                            "content": [
                                {"type": "text", "text": "What's in this image?"},
                                {
                                    "type": "image",
                                    "source": {
                                        "type": "base64",
                                        "media_type": "image/png",
                                        "data": fake_base64,
                                    },
                                },
                            ],
                        }
                    ],
                    max_tokens=100,
                ),
            )

    assert response.status_code == 200

    # Verify upstream received image in OpenAI format
    received = upstream.received[0]["json"]
    user_content = received["messages"][0]["content"]

    # Should be array with text and image_url
    assert isinstance(user_content, list)
    types = [c.get("type") for c in user_content]
    assert "text" in types
    assert "image_url" in types

    # Check image_url format
    image_parts = [c for c in user_content if c.get("type") == "image_url"]
    assert len(image_parts) == 1
    assert "url" in image_parts[0].get("image_url", {})
    assert image_parts[0]["image_url"]["url"].startswith("data:image/png;base64,")


@pytest.mark.asyncio
async def test_translator_image_url() -> None:
    """Test URL-based image reference translates correctly."""
    upstream = FakeUpstream()
    upstream.enqueue_openai_chat_response("I see a landscape.")

    base_url = "http://upstream.local/v1"
    register_fake_upstream("upstream.local", upstream)

    config = build_messages_config(base_url)
    with ProxyHarness(config, enable_messages_endpoint=True) as harness:
        async with harness.make_async_client() as client:
            response = await client.post(
                "/v1/messages",
                json=build_anthropic_request(
                    messages=[
                        {
                            "role": "user",
                            "content": [
                                {"type": "text", "text": "Describe this."},
                                {
                                    "type": "image",
                                    "source": {
                                        "type": "url",
                                        "url": "https://example.com/image.jpg",
                                    },
                                },
                            ],
                        }
                    ],
                    max_tokens=100,
                ),
            )

    assert response.status_code == 200

    # Verify URL passed through
    received = upstream.received[0]["json"]
    user_content = received["messages"][0]["content"]
    image_parts = [c for c in user_content if c.get("type") == "image_url"]
    assert len(image_parts) == 1
    assert image_parts[0]["image_url"]["url"] == "https://example.com/image.jpg"


# =============================================================================
# Multi-turn Conversation Tests
# =============================================================================


@pytest.mark.asyncio
async def test_translator_multi_turn_conversation() -> None:
    """Test multi-turn conversation translates correctly."""
    upstream = FakeUpstream()
    upstream.enqueue_openai_chat_response("42")

    base_url = "http://upstream.local/v1"
    register_fake_upstream("upstream.local", upstream)

    config = build_messages_config(base_url)
    with ProxyHarness(config, enable_messages_endpoint=True) as harness:
        async with harness.make_async_client() as client:
            response = await client.post(
                "/v1/messages",
                json=build_anthropic_request(
                    messages=[
                        {"role": "user", "content": "What is 6 times 7?"},
                        {"role": "assistant", "content": "Let me calculate..."},
                        {"role": "user", "content": "Just tell me the answer."},
                    ],
                    max_tokens=100,
                ),
            )

    assert response.status_code == 200

    # Verify all turns preserved
    received = upstream.received[0]["json"]
    messages = received["messages"]
    assert len(messages) == 3

    # Check roles alternate correctly
    assert messages[0]["role"] == "user"
    assert messages[1]["role"] == "assistant"
    assert messages[2]["role"] == "user"


@pytest.mark.asyncio
async def test_translator_multi_turn_with_tool_use() -> None:
    """Test multi-turn conversation with tool use/result."""
    upstream = FakeUpstream()
    upstream.enqueue_openai_chat_response("The weather is sunny.")

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
                                {"type": "text", "text": "Let me check."},
                                {
                                    "type": "tool_use",
                                    "id": "toolu_123",
                                    "name": "get_weather",
                                    "input": {"location": "NYC"},
                                },
                            ],
                        },
                        {
                            "role": "user",
                            "content": [
                                {
                                    "type": "tool_result",
                                    "tool_use_id": "toolu_123",
                                    "content": "Sunny, 75F",
                                }
                            ],
                        },
                    ],
                    max_tokens=100,
                ),
            )

    assert response.status_code == 200

    # Verify tool flow translated
    received = upstream.received[0]["json"]
    messages = received["messages"]

    # Should have: user, assistant (with tool_calls), tool
    assert len(messages) >= 3

    # Find assistant message with tool_calls
    assistant_msgs = [m for m in messages if m["role"] == "assistant"]
    assert any("tool_calls" in m for m in assistant_msgs)

    # Find tool message
    tool_msgs = [m for m in messages if m["role"] == "tool"]
    assert len(tool_msgs) == 1


# =============================================================================
# Content Block Simplification Tests
# =============================================================================


@pytest.mark.asyncio
async def test_translator_single_text_simplifies() -> None:
    """Test that single text content simplifies to string."""
    upstream = FakeUpstream()
    upstream.enqueue_openai_chat_response("Response")

    base_url = "http://upstream.local/v1"
    register_fake_upstream("upstream.local", upstream)

    config = build_messages_config(base_url)
    with ProxyHarness(config, enable_messages_endpoint=True) as harness:
        async with harness.make_async_client() as client:
            response = await client.post(
                "/v1/messages",
                json=build_anthropic_request(
                    messages=[
                        {
                            "role": "user",
                            "content": [{"type": "text", "text": "Hello"}],
                        }
                    ],
                    max_tokens=100,
                ),
            )

    assert response.status_code == 200

    # Verify content was simplified to string in upstream
    received = upstream.received[0]["json"]
    user_content = received["messages"][0]["content"]
    # Should be string, not array
    assert isinstance(user_content, str)
    assert user_content == "Hello"


@pytest.mark.asyncio
async def test_translator_multiple_text_blocks_preserved() -> None:
    """Test that multiple text blocks stay as array."""
    upstream = FakeUpstream()
    upstream.enqueue_openai_chat_response("Response")

    base_url = "http://upstream.local/v1"
    register_fake_upstream("upstream.local", upstream)

    config = build_messages_config(base_url)
    with ProxyHarness(config, enable_messages_endpoint=True) as harness:
        async with harness.make_async_client() as client:
            response = await client.post(
                "/v1/messages",
                json=build_anthropic_request(
                    messages=[
                        {
                            "role": "user",
                            "content": [
                                {"type": "text", "text": "First part."},
                                {"type": "text", "text": "Second part."},
                            ],
                        }
                    ],
                    max_tokens=100,
                ),
            )

    assert response.status_code == 200

    # Verify content stayed as array
    received = upstream.received[0]["json"]
    user_content = received["messages"][0]["content"]
    assert isinstance(user_content, list)
    assert len(user_content) == 2


# =============================================================================
# Tool Choice Translation Tests
# =============================================================================


@pytest.mark.asyncio
async def test_translator_tool_choice_auto() -> None:
    """Test tool_choice auto translates."""
    upstream = FakeUpstream()
    upstream.enqueue_openai_chat_response("Using auto")

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
                    tools=[{"name": "test", "description": "Test", "input_schema": {"type": "object"}}],
                    tool_choice={"type": "auto"},
                ),
            )

    assert response.status_code == 200

    received = upstream.received[0]["json"]
    assert received.get("tool_choice") == "auto"


@pytest.mark.asyncio
async def test_translator_tool_choice_any_to_required() -> None:
    """Test tool_choice 'any' translates to 'required'."""
    upstream = FakeUpstream()
    upstream.enqueue_openai_chat_response(
        "",
        tool_calls=[{"function": {"name": "test", "arguments": {}}}],
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
                    messages=[{"role": "user", "content": "Hi"}],
                    max_tokens=100,
                    tools=[{"name": "test", "description": "Test", "input_schema": {"type": "object"}}],
                    tool_choice={"type": "any"},
                ),
            )

    assert response.status_code == 200

    received = upstream.received[0]["json"]
    # "any" should map to "required"
    assert received.get("tool_choice") == "required"


@pytest.mark.asyncio
async def test_translator_tool_choice_specific_tool() -> None:
    """Test tool_choice with specific tool name."""
    upstream = FakeUpstream()
    upstream.enqueue_openai_chat_response(
        "",
        tool_calls=[{"function": {"name": "specific_tool", "arguments": {}}}],
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
                    messages=[{"role": "user", "content": "Hi"}],
                    max_tokens=100,
                    tools=[
                        {"name": "specific_tool", "description": "Specific", "input_schema": {"type": "object"}},
                        {"name": "other_tool", "description": "Other", "input_schema": {"type": "object"}},
                    ],
                    tool_choice={"type": "tool", "name": "specific_tool"},
                ),
            )

    assert response.status_code == 200

    received = upstream.received[0]["json"]
    tool_choice = received.get("tool_choice")
    assert isinstance(tool_choice, dict)
    assert tool_choice.get("type") == "function"
    assert tool_choice.get("function", {}).get("name") == "specific_tool"


# =============================================================================
# Error Response Translation Tests
# =============================================================================


@pytest.mark.asyncio
async def test_translator_openai_error_to_anthropic() -> None:
    """Test OpenAI error response format."""
    upstream = FakeUpstream()
    upstream.enqueue_error_response(
        status_code=400,
        error_type="invalid_request_error",
        message="Invalid request",
        anthropic_format=False,  # OpenAI format
    )

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
                ),
            )

    # Should return error
    assert response.status_code == 400


@pytest.mark.asyncio
async def test_translator_rate_limit_error() -> None:
    """Test 429 error handling."""
    upstream = FakeUpstream()
    upstream.enqueue_error_response(
        status_code=429,
        error_type="rate_limit_error",
        message="Rate limit exceeded",
        anthropic_format=False,
    )

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
                ),
            )

    assert response.status_code == 429


# =============================================================================
# Edge Case Tests
# =============================================================================


@pytest.mark.asyncio
async def test_translator_empty_assistant_content() -> None:
    """Test handling assistant message with empty content."""
    upstream = FakeUpstream()
    upstream.enqueue_openai_chat_response("Following up.")

    base_url = "http://upstream.local/v1"
    register_fake_upstream("upstream.local", upstream)

    config = build_messages_config(base_url)
    with ProxyHarness(config, enable_messages_endpoint=True) as harness:
        async with harness.make_async_client() as client:
            response = await client.post(
                "/v1/messages",
                json=build_anthropic_request(
                    messages=[
                        {"role": "user", "content": "Hi"},
                        {"role": "assistant", "content": []},  # Empty content
                        {"role": "user", "content": "Continue"},
                    ],
                    max_tokens=100,
                ),
            )

    # Should handle gracefully
    assert response.status_code == 200


@pytest.mark.asyncio
async def test_translator_tool_result_with_array_content() -> None:
    """Test tool_result with array content blocks."""
    upstream = FakeUpstream()
    upstream.enqueue_openai_chat_response("Based on the results...")

    base_url = "http://upstream.local/v1"
    register_fake_upstream("upstream.local", upstream)

    config = build_messages_config(base_url)
    with ProxyHarness(config, enable_messages_endpoint=True) as harness:
        async with harness.make_async_client() as client:
            response = await client.post(
                "/v1/messages",
                json=build_anthropic_request(
                    messages=[
                        {"role": "user", "content": "Search for X"},
                        {
                            "role": "assistant",
                            "content": [
                                {
                                    "type": "tool_use",
                                    "id": "toolu_abc",
                                    "name": "search",
                                    "input": {"query": "X"},
                                }
                            ],
                        },
                        {
                            "role": "user",
                            "content": [
                                {
                                    "type": "tool_result",
                                    "tool_use_id": "toolu_abc",
                                    "content": [
                                        {"type": "text", "text": "Result 1"},
                                        {"type": "text", "text": "Result 2"},
                                    ],
                                }
                            ],
                        },
                    ],
                    max_tokens=100,
                ),
            )

    assert response.status_code == 200


@pytest.mark.asyncio
async def test_translator_tool_result_error_flag() -> None:
    """Test tool_result with is_error flag."""
    upstream = FakeUpstream()
    upstream.enqueue_openai_chat_response("I see there was an error.")

    base_url = "http://upstream.local/v1"
    register_fake_upstream("upstream.local", upstream)

    config = build_messages_config(base_url)
    with ProxyHarness(config, enable_messages_endpoint=True) as harness:
        async with harness.make_async_client() as client:
            response = await client.post(
                "/v1/messages",
                json=build_anthropic_request(
                    messages=[
                        {"role": "user", "content": "Run dangerous command"},
                        {
                            "role": "assistant",
                            "content": [
                                {
                                    "type": "tool_use",
                                    "id": "toolu_xyz",
                                    "name": "run_command",
                                    "input": {"cmd": "rm -rf /"},
                                }
                            ],
                        },
                        {
                            "role": "user",
                            "content": [
                                {
                                    "type": "tool_result",
                                    "tool_use_id": "toolu_xyz",
                                    "is_error": True,
                                    "content": "Permission denied",
                                }
                            ],
                        },
                    ],
                    max_tokens=100,
                ),
            )

    assert response.status_code == 200


# =============================================================================
# Metadata Translation Tests
# =============================================================================


@pytest.mark.asyncio
async def test_translator_metadata_user_id() -> None:
    """Test metadata.user_id maps to user parameter."""
    upstream = FakeUpstream()
    upstream.enqueue_openai_chat_response("Hello user!")

    base_url = "http://upstream.local/v1"
    register_fake_upstream("upstream.local", upstream)

    config = build_messages_config(base_url)
    with ProxyHarness(config, enable_messages_endpoint=True) as harness:
        async with harness.make_async_client() as client:
            request = build_anthropic_request(
                messages=[{"role": "user", "content": "Hi"}],
                max_tokens=100,
            )
            request["metadata"] = {"user_id": "user-123"}

            response = await client.post("/v1/messages", json=request)

    assert response.status_code == 200

    # Verify user parameter sent to upstream
    received = upstream.received[0]["json"]
    assert received.get("user") == "user-123"
