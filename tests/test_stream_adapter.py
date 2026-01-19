"""Tests for the chat-to-responses stream adapter."""

import json
import sys
from pathlib import Path

import pytest

# Add src directory to path
sys.path.insert(0, str(Path(__file__).parent.parent / "src"))

from src.responses.stream_adapter import ChatToResponsesStreamAdapter
from src.types.responses import (
    EVENT_RESPONSE_COMPLETED,
    EVENT_RESPONSE_FAILED,
    EVENT_RESPONSE_INCOMPLETE,
)


async def _aiter(chunks: list[bytes]):
    for chunk in chunks:
        yield chunk


def _parse_event(raw: bytes) -> dict:
    text = raw.decode("utf-8")
    lines = [line for line in text.split("\n") if line]
    event_line = next(line for line in lines if line.startswith("event: "))
    data_line = next(line for line in lines if line.startswith("data: "))
    return {
        "event": event_line[len("event: "):],
        "data": json.loads(data_line[len("data: "):]),
    }


def _terminal_response(events: list[dict]) -> dict:
    terminal = events[-1]
    return terminal["data"]["response"]


@pytest.mark.asyncio
async def test_stream_adapter_emits_completed_on_done():
    adapter = ChatToResponsesStreamAdapter("resp_test", "test-model", {})
    chunks = [
        b'data: {"choices":[{"delta":{"role":"assistant"},"index":0}]}\n\n',
        b'data: {"choices":[{"delta":{"content":"Hello"},"index":0}]}\n\n',
        b'data: {"choices":[{"delta":{},"finish_reason":"stop","index":0}]}\n\n',
        b"data: [DONE]\n\n",
    ]

    events = [_parse_event(event) async for event in adapter.adapt_stream(_aiter(chunks))]
    terminal = events[-1]

    assert terminal["event"] == EVENT_RESPONSE_COMPLETED
    assert terminal["data"]["response"]["status"] == "completed"


@pytest.mark.asyncio
async def test_stream_adapter_emits_incomplete_on_length_without_done():
    adapter = ChatToResponsesStreamAdapter("resp_test", "test-model", {})
    chunks = [
        b'data: {"choices":[{"delta":{"content":"Hello"},"index":0}]}\n\n',
        b'data: {"choices":[{"delta":{},"finish_reason":"length","index":0}]}\n\n',
    ]

    events = [_parse_event(event) async for event in adapter.adapt_stream(_aiter(chunks))]
    terminal = events[-1]

    assert terminal["event"] == EVENT_RESPONSE_INCOMPLETE
    response = terminal["data"]["response"]
    assert response["status"] == "incomplete"
    assert response["incomplete_details"]["reason"] == "max_output_tokens"


@pytest.mark.asyncio
async def test_stream_adapter_emits_failed_on_missing_done_and_reason():
    adapter = ChatToResponsesStreamAdapter("resp_test", "test-model", {})
    chunks = [
        b'data: {"choices":[{"delta":{"content":"Hello"},"index":0}]}\n\n',
    ]

    events = [_parse_event(event) async for event in adapter.adapt_stream(_aiter(chunks))]
    terminal = events[-1]

    assert terminal["event"] == EVENT_RESPONSE_FAILED
    response = terminal["data"]["response"]
    assert response["status"] == "failed"
    assert response["error"]["code"] == "stream_ended_unexpectedly"


@pytest.mark.asyncio
async def test_stream_adapter_handles_list_content():
    adapter = ChatToResponsesStreamAdapter("resp_test", "test-model", {})
    chunks = [
        b'data: {"choices":[{"delta":{"content":[{"type":"text","text":"Hello"}]},"index":0}]}\n\n',
        b'data: {"choices":[{"delta":{},"finish_reason":"stop","index":0}]}\n\n',
        b"data: [DONE]\n\n",
    ]

    events = [_parse_event(event) async for event in adapter.adapt_stream(_aiter(chunks))]
    terminal = events[-1]

    assert terminal["event"] == EVENT_RESPONSE_COMPLETED
    assert terminal["data"]["response"]["output_text"] == "Hello"


@pytest.mark.asyncio
async def test_stream_adapter_handles_tool_call_only_stream():
    adapter = ChatToResponsesStreamAdapter("resp_tool", "test-model", {})
    chunks = [
        (
            b'data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1",'
            b'"type":"function","function":{"name":"lookup","arguments":"{\\"q\\":\\""}}]},'
            b'"index":0}]}\n\n'
        ),
        (
            b'data: {"choices":[{"delta":{"tool_calls":[{"index":0,'
            b'"function":{"arguments":"x\\"}"}}]},"index":0}]}\n\n'
        ),
        b'data: {"choices":[{"delta":{},"finish_reason":"tool_calls","index":0}]}\n\n',
        b"data: [DONE]\n\n",
    ]

    events = [_parse_event(event) async for event in adapter.adapt_stream(_aiter(chunks))]
    response = _terminal_response(events)

    assert response["status"] == "completed"
    assert response["output_text"] == ""
    assert response["output"][0]["type"] == "function_call"
    assert response["output"][0]["name"] == "lookup"
    assert response["output"][0]["arguments"] == "{\"q\":\"x\"}"
