"""Network chunk boundaries must not change streamed Unicode or tool arguments."""

from __future__ import annotations

import json

import pytest

from src.core.sse import SSEJSONDecoder, SSELineDecoder
from src.messages.stream_adapter import ChatToMessagesStreamAdapter
from src.parsers.response_pipeline import ParserContext, ResponseParserPipeline, SSEDecoder
from src.responses.stream_adapter import ChatToResponsesStreamAdapter

TEXT = "сотрудником не представлял — 中文 😀"
ARGUMENTS = json.dumps({"text": TEXT}, ensure_ascii=False)


def _event(delta: dict, finish_reason: str | None = None) -> bytes:
    payload = {"choices": [{"index": 0, "delta": delta, "finish_reason": finish_reason}]}
    return ("data: " + json.dumps(payload, ensure_ascii=False) + "\n\n").encode()


def _fragmentations(wire: bytes):
    yield [wire[index : index + 1] for index in range(len(wire))]
    for cut in range(1, len(wire)):
        yield [wire[:cut], b"", wire[cut:]]


async def _aiter(chunks: list[bytes]):
    for chunk in chunks:
        yield chunk


@pytest.mark.parametrize("field", ["content", "reasoning_content", "tool_calls"])
@pytest.mark.parametrize("newline", [b"\n", b"\r\n"])
def test_chat_pipeline_preserves_utf8_at_every_chunk_boundary(field, newline):
    value = TEXT if field != "tool_calls" else [{"index": 0, "id": "call_utf8", "type": "function", "function": {"name": "update_form", "arguments": ARGUMENTS}}]
    wire = _event({field: value})
    expected = json.loads(wire.decode()[6:])
    wire = wire.replace(b"\n", newline)
    for chunks in _fragmentations(wire):
        pipeline = ResponseParserPipeline([], [])
        parser = pipeline.create_stream_parser(ParserContext("/v1/chat/completions", "deepseek", "deepseek", True))
        output = [part for chunk in chunks for part in parser.feed_bytes(chunk)]
        output.extend(parser.finish())
        actual = json.loads(b"".join(output).decode().split("\n\n")[0][6:])
        assert actual == expected


@pytest.mark.parametrize("terminated", [True, False])
@pytest.mark.parametrize("newline", [b"\n", b"\r\n"])
def test_logging_decoder_preserves_utf8_at_every_chunk_boundary(terminated, newline):
    wire = _event({"content": TEXT})
    expected = json.loads(wire.decode()[6:])
    if not terminated:
        wire = wire.rstrip(b"\n")
    wire = wire.replace(b"\n", newline)
    for chunks in _fragmentations(wire):
        decoder = SSEJSONDecoder()
        actual = [event for chunk in chunks for event in decoder.feed(chunk)]
        actual.extend(decoder.flush())
        assert actual == [expected]
        assert decoder.flush() == []


def test_sse_decoder_replaces_truncated_utf8_only_at_end_of_stream():
    decoder = SSEDecoder()
    assert decoder.feed(b"data: \xf0\x9f") == []
    assert decoder.feed(b"") == []
    assert decoder.flush() == "data: �".encode()
    assert decoder.flush() is None


def test_sse_line_decoder_flushes_incomplete_utf8_and_keeps_streams_isolated():
    first = SSELineDecoder()
    second = SSELineDecoder()
    assert first.feed(b"data: \xf0\x9f") == []
    assert second.feed(b"data: other\r") == []
    assert second.feed(b"\n") == ["data: other"]
    assert first.feed(b"", final=True) == ["data: �"]
    assert first.feed(b"", final=True) == []


@pytest.mark.parametrize("adapter_type", [ChatToResponsesStreamAdapter, ChatToMessagesStreamAdapter])
@pytest.mark.parametrize("terminated", [True, False])
@pytest.mark.parametrize("newline", [b"\n", b"\r\n"])
@pytest.mark.asyncio
async def test_adapters_preserve_fragmented_utf8_text_and_tool_arguments(adapter_type, terminated, newline):
    wire = (
        _event({"role": "assistant", "content": TEXT})
        + _event({"tool_calls": [{"index": 0, "id": "call_utf8", "type": "function", "function": {"name": "update_form", "arguments": ARGUMENTS}}]})
        + _event({}, "tool_calls")
        + b"data: [DONE]\n\n"
    )
    if not terminated:
        wire = wire.rstrip(b"\n")
    wire = wire.replace(b"\n", newline)
    for chunks in _fragmentations(wire):
        adapter = adapter_type("utf8_test", "deepseek")
        events = [json.loads(line[6:]) async for raw in adapter.adapt_stream(_aiter(chunks)) for line in raw.decode().splitlines() if line.startswith("data: ")]
        if adapter_type is ChatToResponsesStreamAdapter:
            text = "".join(event["delta"] for event in events if event["type"] == "response.output_text.delta")
            arguments = "".join(event["delta"] for event in events if event["type"] == "response.function_call_arguments.delta")
            assert events[-1]["type"] == "response.completed"
        else:
            deltas = [event["delta"] for event in events if event["type"] == "content_block_delta"]
            text = "".join(delta["text"] for delta in deltas if delta["type"] == "text_delta")
            arguments = "".join(delta["partial_json"] for delta in deltas if delta["type"] == "input_json_delta")
            assert events[-1]["type"] == "message_stop"
        assert text == TEXT
        assert arguments == ARGUMENTS
        assert adapter.saw_done
