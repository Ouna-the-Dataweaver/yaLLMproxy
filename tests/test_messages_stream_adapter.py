"""Tests for the OpenAI Chat -> Anthropic Messages stream adapter."""

import json
import sys
from pathlib import Path

import pytest

# Add src directory to path
sys.path.insert(0, str(Path(__file__).parent.parent / "src"))

from src.messages.stream_adapter import ChatToMessagesStreamAdapter


async def _aiter(chunks: list[bytes]):
    """Helper to create async iterator from list of bytes."""
    for chunk in chunks:
        yield chunk


def _parse_events(raw_events: list[bytes]) -> list[dict]:
    """Parse SSE events from raw bytes."""
    events = []
    for raw in raw_events:
        text = raw.decode("utf-8")
        lines = [line for line in text.split("\n") if line]
        event_line = next((line for line in lines if line.startswith("event: ")), None)
        data_line = next((line for line in lines if line.startswith("data: ")), None)
        if event_line and data_line:
            events.append({
                "event": event_line[len("event: "):],
                "data": json.loads(data_line[len("data: "):]),
            })
    return events


class TestChatToMessagesStreamAdapter:
    """Tests for ChatToMessagesStreamAdapter."""

    @pytest.mark.asyncio
    async def test_simple_text_stream(self):
        """Test basic text streaming."""
        adapter = ChatToMessagesStreamAdapter("msg_test123", "gpt-4")
        chunks = [
            b'data: {"choices":[{"delta":{"role":"assistant"},"index":0}]}\n\n',
            b'data: {"choices":[{"delta":{"content":"Hello"},"index":0}]}\n\n',
            b'data: {"choices":[{"delta":{"content":" world"},"index":0}]}\n\n',
            b'data: {"choices":[{"delta":{},"finish_reason":"stop","index":0}]}\n\n',
            b"data: [DONE]\n\n",
        ]

        raw_events = [event async for event in adapter.adapt_stream(_aiter(chunks))]
        events = _parse_events(raw_events)

        # Check event sequence
        event_types = [e["event"] for e in events]
        assert "message_start" in event_types
        assert "content_block_start" in event_types
        assert "content_block_delta" in event_types
        assert "content_block_stop" in event_types
        assert "message_delta" in event_types
        assert "message_stop" in event_types

        # Check message_start
        message_start = next(e for e in events if e["event"] == "message_start")
        assert message_start["data"]["message"]["id"] == "msg_test123"
        assert message_start["data"]["message"]["role"] == "assistant"
        assert message_start["data"]["message"]["model"] == "gpt-4"

        # Check content_block_start for text
        block_start = next(e for e in events if e["event"] == "content_block_start")
        assert block_start["data"]["index"] == 0
        assert block_start["data"]["content_block"]["type"] == "text"

        # Check text deltas
        text_deltas = [e for e in events if e["event"] == "content_block_delta"]
        assert len(text_deltas) == 2
        assert text_deltas[0]["data"]["delta"]["type"] == "text_delta"
        assert text_deltas[0]["data"]["delta"]["text"] == "Hello"
        assert text_deltas[1]["data"]["delta"]["text"] == " world"

        # Check message_delta has stop_reason
        message_delta = next(e for e in events if e["event"] == "message_delta")
        assert message_delta["data"]["delta"]["stop_reason"] == "end_turn"

    @pytest.mark.asyncio
    async def test_tool_call_stream(self):
        """Test streaming with tool calls."""
        adapter = ChatToMessagesStreamAdapter("msg_tool123", "gpt-4")
        chunks = [
            b'data: {"choices":[{"delta":{"role":"assistant"},"index":0}]}\n\n',
            b'data: {"choices":[{"delta":{"content":"Let me check"},"index":0}]}\n\n',
            (
                b'data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_abc",'
                b'"type":"function","function":{"name":"get_weather","arguments":"{\\"loc"}}]},"index":0}]}\n\n'
            ),
            (
                b'data: {"choices":[{"delta":{"tool_calls":[{"index":0,'
                b'"function":{"arguments":"ation\\":\\"Tokyo\\"}"}}]},"index":0}]}\n\n'
            ),
            b'data: {"choices":[{"delta":{},"finish_reason":"tool_calls","index":0}]}\n\n',
            b"data: [DONE]\n\n",
        ]

        raw_events = [event async for event in adapter.adapt_stream(_aiter(chunks))]
        events = _parse_events(raw_events)

        # Should have two content_block_start events (text + tool_use)
        block_starts = [e for e in events if e["event"] == "content_block_start"]
        assert len(block_starts) == 2

        # First should be text
        assert block_starts[0]["data"]["content_block"]["type"] == "text"
        assert block_starts[0]["data"]["index"] == 0

        # Second should be tool_use
        assert block_starts[1]["data"]["content_block"]["type"] == "tool_use"
        assert block_starts[1]["data"]["content_block"]["id"] == "call_abc"
        assert block_starts[1]["data"]["index"] == 1

        # Check input_json_delta events
        json_deltas = [
            e for e in events
            if e["event"] == "content_block_delta" and
            e["data"]["delta"].get("type") == "input_json_delta"
        ]
        assert len(json_deltas) == 2

        # Check message_delta has tool_use stop_reason
        message_delta = next(e for e in events if e["event"] == "message_delta")
        assert message_delta["data"]["delta"]["stop_reason"] == "tool_use"

    @pytest.mark.asyncio
    async def test_multiple_tool_calls(self):
        """Test streaming with multiple tool calls."""
        adapter = ChatToMessagesStreamAdapter("msg_multi", "gpt-4")
        chunks = [
            b'data: {"choices":[{"delta":{"role":"assistant"},"index":0}]}\n\n',
            (
                b'data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1",'
                b'"type":"function","function":{"name":"func1","arguments":"{}"}}]},"index":0}]}\n\n'
            ),
            (
                b'data: {"choices":[{"delta":{"tool_calls":[{"index":1,"id":"call_2",'
                b'"type":"function","function":{"name":"func2","arguments":"{}"}}]},"index":0}]}\n\n'
            ),
            b'data: {"choices":[{"delta":{},"finish_reason":"tool_calls","index":0}]}\n\n',
            b"data: [DONE]\n\n",
        ]

        raw_events = [event async for event in adapter.adapt_stream(_aiter(chunks))]
        events = _parse_events(raw_events)

        # Should have two tool_use block starts
        block_starts = [e for e in events if e["event"] == "content_block_start"]
        assert len(block_starts) == 2

        assert block_starts[0]["data"]["content_block"]["id"] == "call_1"
        assert block_starts[0]["data"]["content_block"]["name"] == "func1"
        assert block_starts[1]["data"]["content_block"]["id"] == "call_2"
        assert block_starts[1]["data"]["content_block"]["name"] == "func2"

    @pytest.mark.asyncio
    async def test_finish_reason_length(self):
        """Test length finish_reason maps to max_tokens."""
        adapter = ChatToMessagesStreamAdapter("msg_len", "gpt-4")
        chunks = [
            b'data: {"choices":[{"delta":{"content":"Hello"},"index":0}]}\n\n',
            b'data: {"choices":[{"delta":{},"finish_reason":"length","index":0}]}\n\n',
            b"data: [DONE]\n\n",
        ]

        raw_events = [event async for event in adapter.adapt_stream(_aiter(chunks))]
        events = _parse_events(raw_events)

        message_delta = next(e for e in events if e["event"] == "message_delta")
        assert message_delta["data"]["delta"]["stop_reason"] == "max_tokens"

    @pytest.mark.asyncio
    async def test_usage_tracking(self):
        """Test usage information is captured."""
        adapter = ChatToMessagesStreamAdapter("msg_usage", "gpt-4")
        chunks = [
            b'data: {"choices":[{"delta":{"content":"Hi"},"index":0}]}\n\n',
            b'data: {"choices":[{"delta":{},"finish_reason":"stop","index":0}],"usage":{"prompt_tokens":10,"completion_tokens":5}}\n\n',
            b"data: [DONE]\n\n",
        ]

        raw_events = [event async for event in adapter.adapt_stream(_aiter(chunks))]
        events = _parse_events(raw_events)

        message_delta = next(e for e in events if e["event"] == "message_delta")
        assert message_delta["data"]["usage"]["output_tokens"] == 5

    @pytest.mark.asyncio
    async def test_empty_stream(self):
        """Test handling of empty stream (just DONE)."""
        adapter = ChatToMessagesStreamAdapter("msg_empty", "gpt-4")
        chunks = [
            b"data: [DONE]\n\n",
        ]

        raw_events = [event async for event in adapter.adapt_stream(_aiter(chunks))]
        events = _parse_events(raw_events)

        # Should still emit message_start and message_stop
        event_types = [e["event"] for e in events]
        assert "message_start" in event_types
        assert "message_stop" in event_types

    @pytest.mark.asyncio
    async def test_build_final_message(self):
        """Test building the final message object."""
        adapter = ChatToMessagesStreamAdapter("msg_final", "gpt-4")
        chunks = [
            b'data: {"choices":[{"delta":{"role":"assistant"},"index":0}]}\n\n',
            b'data: {"choices":[{"delta":{"content":"Hello world"},"index":0}]}\n\n',
            b'data: {"choices":[{"delta":{},"finish_reason":"stop","index":0}],"usage":{"prompt_tokens":10,"completion_tokens":2}}\n\n',
            b"data: [DONE]\n\n",
        ]

        # Process the stream
        _ = [event async for event in adapter.adapt_stream(_aiter(chunks))]

        # Build final message
        message = adapter.build_final_message()

        assert message["id"] == "msg_final"
        assert message["type"] == "message"
        assert message["role"] == "assistant"
        assert message["model"] == "gpt-4"
        assert message["stop_reason"] == "end_turn"
        assert len(message["content"]) == 1
        assert message["content"][0]["type"] == "text"
        assert message["content"][0]["text"] == "Hello world"
        assert message["usage"]["input_tokens"] == 10
        assert message["usage"]["output_tokens"] == 2

    @pytest.mark.asyncio
    async def test_tool_use_in_final_message(self):
        """Test tool_use blocks in final message have parsed input."""
        adapter = ChatToMessagesStreamAdapter("msg_tool_final", "gpt-4")
        chunks = [
            (
                b'data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_xyz",'
                b'"type":"function","function":{"name":"test_func","arguments":"{\\"key\\":\\"value\\"}"}}]},"index":0}]}\n\n'
            ),
            b'data: {"choices":[{"delta":{},"finish_reason":"tool_calls","index":0}]}\n\n',
            b"data: [DONE]\n\n",
        ]

        _ = [event async for event in adapter.adapt_stream(_aiter(chunks))]
        message = adapter.build_final_message()

        assert len(message["content"]) == 1
        assert message["content"][0]["type"] == "tool_use"
        assert message["content"][0]["id"] == "call_xyz"
        assert message["content"][0]["name"] == "test_func"
        assert message["content"][0]["input"] == {"key": "value"}

    @pytest.mark.asyncio
    async def test_content_filter_maps_to_refusal(self):
        """Test content_filter finish_reason maps to refusal."""
        adapter = ChatToMessagesStreamAdapter("msg_filter", "gpt-4")
        chunks = [
            b'data: {"choices":[{"delta":{"content":""},"index":0}]}\n\n',
            b'data: {"choices":[{"delta":{},"finish_reason":"content_filter","index":0}]}\n\n',
            b"data: [DONE]\n\n",
        ]

        raw_events = [event async for event in adapter.adapt_stream(_aiter(chunks))]
        events = _parse_events(raw_events)

        message_delta = next(e for e in events if e["event"] == "message_delta")
        assert message_delta["data"]["delta"]["stop_reason"] == "refusal"

    @pytest.mark.asyncio
    async def test_stream_without_done(self):
        """Test stream that ends without [DONE]."""
        adapter = ChatToMessagesStreamAdapter("msg_no_done", "gpt-4")
        chunks = [
            b'data: {"choices":[{"delta":{"content":"Hi"},"index":0}]}\n\n',
            b'data: {"choices":[{"delta":{},"finish_reason":"stop","index":0}]}\n\n',
        ]

        raw_events = [event async for event in adapter.adapt_stream(_aiter(chunks))]
        events = _parse_events(raw_events)

        # Should still complete properly
        event_types = [e["event"] for e in events]
        assert "message_stop" in event_types

    @pytest.mark.asyncio
    async def test_interleaved_text_and_tool_calls(self):
        """Test interleaved text and tool call content."""
        adapter = ChatToMessagesStreamAdapter("msg_interleave", "gpt-4")
        chunks = [
            b'data: {"choices":[{"delta":{"role":"assistant"},"index":0}]}\n\n',
            b'data: {"choices":[{"delta":{"content":"I will help"},"index":0}]}\n\n',
            (
                b'data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1",'
                b'"type":"function","function":{"name":"helper","arguments":"{}"}}]},"index":0}]}\n\n'
            ),
            b'data: {"choices":[{"delta":{},"finish_reason":"tool_calls","index":0}]}\n\n',
            b"data: [DONE]\n\n",
        ]

        raw_events = [event async for event in adapter.adapt_stream(_aiter(chunks))]
        events = _parse_events(raw_events)

        # Should have text block stop before tool block start
        block_stops = [e for e in events if e["event"] == "content_block_stop"]
        block_starts = [e for e in events if e["event"] == "content_block_start"]

        # Two block starts: text and tool_use
        assert len(block_starts) == 2
        assert block_starts[0]["data"]["content_block"]["type"] == "text"
        assert block_starts[1]["data"]["content_block"]["type"] == "tool_use"

        # Two block stops
        assert len(block_stops) == 2
