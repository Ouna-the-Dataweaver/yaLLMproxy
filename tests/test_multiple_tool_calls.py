"""Tests for multiple tool call parsing in both streaming and non-streaming modes."""

from __future__ import annotations

import json
import sys
from pathlib import Path
from typing import Any

import pytest

sys.path.insert(0, str(Path(__file__).parent.parent / "src"))

from src.parsers.response_pipeline import (
    ModuleContext,
    ModuleLogCollector,
    ParseTagsParser,
    TagScanner,
)


def _build_payload(content: str) -> dict[str, Any]:
    return {
        "choices": [
            {"index": 0, "message": {"role": "assistant", "content": content}}
        ]
    }


def _build_stream_event(content: str, finish_reason: str | None = None) -> dict[str, Any]:
    event: dict[str, Any] = {
        "choices": [{"index": 0, "delta": {"content": content}}]
    }
    if finish_reason is not None:
        event["choices"][0]["finish_reason"] = finish_reason
    return event


def _build_finish_event(finish_reason: str) -> dict[str, Any]:
    return {
        "choices": [{"index": 0, "delta": {}, "finish_reason": finish_reason}]
    }


def _apply_non_stream(parser: ParseTagsParser, content: str) -> dict[str, Any]:
    payload = _build_payload(content)
    ctx = ModuleContext(
        path="/chat/completions",
        model="test-model",
        backend="test-backend",
        is_stream=False,
    )
    return parser.apply_response(payload, ctx)


def _apply_stream(
    parser: ParseTagsParser,
    chunks: list[str],
    final_finish_reason: str | None = "stop"
) -> tuple[dict[str, Any], list[dict[str, Any]]]:
    """Apply streaming chunks and return (assembled_message, all_events)."""
    log_collector = ModuleLogCollector()
    state = parser.create_stream_state(log_collector)
    ctx = ModuleContext(
        path="/chat/completions",
        model="test-model",
        backend="test-backend",
        is_stream=True,
    )

    all_events: list[dict[str, Any]] = []

    # Process content chunks
    for chunk in chunks:
        event = _build_stream_event(chunk)
        result = parser.apply_stream_event(event, state, ctx)
        if isinstance(result, list):
            all_events.extend(result)
        else:
            all_events.append(result)

    # Send final finish_reason if provided
    if final_finish_reason:
        finish_event = _build_finish_event(final_finish_reason)
        result = parser.apply_stream_event(finish_event, state, ctx)
        if isinstance(result, list):
            all_events.extend(result)
        else:
            all_events.append(result)

    # Finalize
    final_events = parser.finalize_stream(state, ctx)
    all_events.extend(final_events)

    # Assemble message from events
    message = _assemble_message(all_events)
    return message, all_events


def _assemble_message(events: list[dict[str, Any]]) -> dict[str, Any]:
    """Assemble a message from streaming events."""
    content_parts: list[str] = []
    reasoning_parts: list[str] = []
    tool_calls: list[dict[str, Any]] = []
    finish_reason: str | None = None

    for event in events:
        choices = event.get("choices", [])
        for choice in choices:
            if choice.get("finish_reason"):
                finish_reason = choice["finish_reason"]
            delta = choice.get("delta", {})
            if delta.get("content"):
                content_parts.append(delta["content"])
            if delta.get("reasoning_content"):
                reasoning_parts.append(delta["reasoning_content"])
            if delta.get("tool_calls"):
                tool_calls.extend(delta["tool_calls"])

    return {
        "content": "".join(content_parts) if content_parts else None,
        "reasoning_content": "".join(reasoning_parts) if reasoning_parts else None,
        "tool_calls": tool_calls if tool_calls else None,
        "finish_reason": finish_reason,
    }


class TestMultipleToolCallsNonStream:
    """Tests for multiple tool calls in non-streaming mode."""

    def test_two_tool_calls(self) -> None:
        """Two consecutive tool calls should both be parsed."""
        raw = (
            "<tool_call>first_tool<arg_key>a</arg_key><arg_value>1</arg_value></tool_call>"
            "<tool_call>second_tool<arg_key>b</arg_key><arg_value>2</arg_value></tool_call>"
        )
        parser = ParseTagsParser({
            "parse_thinking": True,
            "parse_tool_calls": True,
        })
        result = _apply_non_stream(parser, raw)
        message = result["choices"][0]["message"]

        assert message["tool_calls"] is not None
        assert len(message["tool_calls"]) == 2
        assert message["tool_calls"][0]["function"]["name"] == "first_tool"
        assert message["tool_calls"][1]["function"]["name"] == "second_tool"
        assert result["choices"][0]["finish_reason"] == "tool_calls"

    def test_three_tool_calls_with_thinking(self) -> None:
        """Multiple tool calls within thinking block."""
        raw = (
            "<think>Let me use multiple tools"
            "<tool_call>tool_a</tool_call>"
            "<tool_call>tool_b<arg_key>x</arg_key><arg_value>y</arg_value></tool_call>"
            "<tool_call>tool_c</tool_call>"
            "</think>"
        )
        parser = ParseTagsParser({
            "parse_thinking": True,
            "parse_tool_calls": True,
        })
        result = _apply_non_stream(parser, raw)
        message = result["choices"][0]["message"]

        assert message["reasoning_content"] == "Let me use multiple tools"
        assert len(message["tool_calls"]) == 3
        assert message["tool_calls"][0]["function"]["name"] == "tool_a"
        assert message["tool_calls"][1]["function"]["name"] == "tool_b"
        assert message["tool_calls"][2]["function"]["name"] == "tool_c"

    def test_tool_calls_with_content_after(self) -> None:
        """Content after tool calls should be dropped."""
        raw = (
            "<tool_call>my_tool</tool_call>"
            "This content should be dropped"
        )
        parser = ParseTagsParser({
            "parse_thinking": True,
            "parse_tool_calls": True,
        })
        result = _apply_non_stream(parser, raw)
        message = result["choices"][0]["message"]

        assert len(message["tool_calls"]) == 1
        assert message["tool_calls"][0]["function"]["name"] == "my_tool"
        # Content after tool calls is dropped
        assert message["content"] is None


class TestMultipleToolCallsStreaming:
    """Tests for multiple tool calls in streaming mode."""

    def test_two_tool_calls_single_chunk(self) -> None:
        """Two tool calls in a single chunk."""
        chunks = [
            "<tool_call>first</tool_call><tool_call>second</tool_call>"
        ]
        parser = ParseTagsParser({
            "parse_thinking": True,
            "parse_tool_calls": True,
        })
        message, events = _apply_stream(parser, chunks)

        assert message["tool_calls"] is not None
        assert len(message["tool_calls"]) == 2
        assert message["tool_calls"][0]["function"]["name"] == "first"
        assert message["tool_calls"][1]["function"]["name"] == "second"
        assert message["finish_reason"] == "tool_calls"

    def test_two_tool_calls_split_across_chunks(self) -> None:
        """Two tool calls split across multiple chunks."""
        chunks = [
            "<tool_call>fir",
            "st</tool_call>",
            "<tool_call>sec",
            "ond</tool_call>",
        ]
        parser = ParseTagsParser({
            "parse_thinking": True,
            "parse_tool_calls": True,
        })
        message, events = _apply_stream(parser, chunks)

        assert message["tool_calls"] is not None
        assert len(message["tool_calls"]) == 2
        assert message["tool_calls"][0]["function"]["name"] == "first"
        assert message["tool_calls"][1]["function"]["name"] == "second"
        assert message["finish_reason"] == "tool_calls"

    def test_three_tool_calls_with_args_streaming(self) -> None:
        """Three tool calls with arguments in streaming."""
        chunks = [
            "<tool_call>search<arg_key>query</arg_key><arg_value>hello</arg_value></tool_call>",
            "<tool_call>fetch<arg_key>url</arg_key><arg_value>http://example.com</arg_value></tool_call>",
            "<tool_call>save<arg_key>path</arg_key><arg_value>/tmp/file</arg_value></tool_call>",
        ]
        parser = ParseTagsParser({
            "parse_thinking": True,
            "parse_tool_calls": True,
        })
        message, events = _apply_stream(parser, chunks)

        assert len(message["tool_calls"]) == 3

        # Verify each tool call
        tools = {tc["function"]["name"]: tc for tc in message["tool_calls"]}
        assert "search" in tools
        assert "fetch" in tools
        assert "save" in tools

        # Verify arguments
        search_args = json.loads(tools["search"]["function"]["arguments"])
        assert search_args["query"] == "hello"

        fetch_args = json.loads(tools["fetch"]["function"]["arguments"])
        assert fetch_args["url"] == "http://example.com"

    def test_tool_calls_with_thinking_streaming(self) -> None:
        """Tool calls inside thinking block in streaming."""
        chunks = [
            "<think>Reasoning first",
            "<tool_call>tool_a</tool_call>",
            "<tool_call>tool_b</tool_call>",
            "</think>",
        ]
        parser = ParseTagsParser({
            "parse_thinking": True,
            "parse_tool_calls": True,
        })
        message, events = _apply_stream(parser, chunks)

        assert message["reasoning_content"] == "Reasoning first"
        assert len(message["tool_calls"]) == 2
        assert message["tool_calls"][0]["function"]["name"] == "tool_a"
        assert message["tool_calls"][1]["function"]["name"] == "tool_b"

    def test_finish_reason_from_upstream_converted(self) -> None:
        """Upstream 'stop' should be converted to 'tool_calls' when tools present."""
        chunks = [
            "<tool_call>my_tool</tool_call>",
        ]
        parser = ParseTagsParser({
            "parse_thinking": True,
            "parse_tool_calls": True,
        })
        message, events = _apply_stream(parser, chunks, final_finish_reason="stop")

        assert message["finish_reason"] == "tool_calls"

    def test_no_finish_reason_from_upstream(self) -> None:
        """When upstream sends no finish_reason, finalize should emit tool_calls."""
        chunks = [
            "<tool_call>my_tool</tool_call>",
        ]
        parser = ParseTagsParser({
            "parse_thinking": True,
            "parse_tool_calls": True,
        })
        # No final finish_reason
        message, events = _apply_stream(parser, chunks, final_finish_reason=None)

        # finalize_stream should emit finish_reason
        assert message["finish_reason"] == "tool_calls"


class TestDroppedContentTracking:
    """Tests for content dropped after tool calls."""

    def test_dropped_content_tracked_in_scanner(self) -> None:
        """Content after tool calls should be tracked in scanner."""
        scanner = TagScanner(
            think_tag="think",
            tool_tag="tool_call",
            parse_thinking=True,
            parse_tool_calls=True,
            drop_after_tool_call=True,
        )

        text = "<tool_call>my_tool</tool_call>This should be dropped"
        result = scanner.feed(text)
        scanner.flush()

        assert len(result.tool_calls) == 1
        assert result.tool_calls[0]["name"] == "my_tool"
        assert scanner.get_dropped_content() == "This should be dropped"
        assert scanner.has_dropped_non_whitespace() is True

    def test_whitespace_only_dropped_content(self) -> None:
        """Whitespace-only dropped content should be detected."""
        scanner = TagScanner(
            think_tag="think",
            tool_tag="tool_call",
            parse_thinking=True,
            parse_tool_calls=True,
            drop_after_tool_call=True,
        )

        text = "<tool_call>my_tool</tool_call>   \n\t  "
        scanner.feed(text)
        scanner.flush()

        assert scanner.get_dropped_content() == "   \n\t  "
        assert scanner.has_dropped_non_whitespace() is False

    def test_no_dropped_content_when_no_tools(self) -> None:
        """No content should be dropped when there are no tool calls."""
        scanner = TagScanner(
            think_tag="think",
            tool_tag="tool_call",
            parse_thinking=True,
            parse_tool_calls=True,
            drop_after_tool_call=True,
        )

        text = "Just regular content without tools"
        result = scanner.feed(text)
        scanner.flush()

        assert result.content == "Just regular content without tools"
        assert scanner.get_dropped_content() == ""

    def test_dropped_content_between_tools_and_after(self) -> None:
        """Content between and after multiple tools is dropped."""
        scanner = TagScanner(
            think_tag="think",
            tool_tag="tool_call",
            parse_thinking=True,
            parse_tool_calls=True,
            drop_after_tool_call=True,
        )

        # Note: content BETWEEN tools is also dropped after first tool
        text = (
            "<tool_call>first</tool_call>"
            "between"
            "<tool_call>second</tool_call>"
            "after"
        )
        result = scanner.feed(text)
        scanner.flush()

        assert len(result.tool_calls) == 2
        # Both "between" and "after" should be dropped
        assert "between" in scanner.get_dropped_content()
        assert "after" in scanner.get_dropped_content()

    def test_dropped_content_logged_in_finalize(self) -> None:
        """Dropped content should be logged during finalize."""
        log_collector = ModuleLogCollector()
        parser = ParseTagsParser({
            "parse_thinking": True,
            "parse_tool_calls": True,
        })
        state = parser.create_stream_state(log_collector)
        ctx = ModuleContext(
            path="/chat/completions",
            model="test-model",
            backend="test-backend",
            is_stream=True,
        )

        # Feed content with tool call followed by dropped content
        event = _build_stream_event(
            "<tool_call>my_tool</tool_call>This will be dropped"
        )
        parser.apply_stream_event(event, state, ctx)
        parser.finalize_stream(state, ctx)

        # Check logs
        logs = log_collector.to_list()
        dropped_events = [e for e in logs if e["event"] == "content_dropped_after_tools"]

        assert len(dropped_events) == 1
        assert dropped_events[0]["details"]["dropped_length"] > 0
        assert "This will be dropped" in dropped_events[0]["details"]["dropped_preview"]


class TestStreamingParity:
    """Tests ensuring streaming and non-streaming produce same results."""

    def test_multiple_tools_parity(self) -> None:
        """Multiple tool calls should parse identically in stream/non-stream."""
        raw = (
            "<tool_call>tool_a<arg_key>x</arg_key><arg_value>1</arg_value></tool_call>"
            "<tool_call>tool_b<arg_key>y</arg_key><arg_value>2</arg_value></tool_call>"
        )
        chunks = [
            "<tool_call>tool_a<arg_key>x</arg_key>",
            "<arg_value>1</arg_value></tool_call>",
            "<tool_call>tool_b<arg_key>y</arg_key>",
            "<arg_value>2</arg_value></tool_call>",
        ]

        parser = ParseTagsParser({
            "parse_thinking": True,
            "parse_tool_calls": True,
        })

        # Non-stream
        non_stream_result = _apply_non_stream(parser, raw)
        non_stream_msg = non_stream_result["choices"][0]["message"]
        non_stream_finish = non_stream_result["choices"][0].get("finish_reason")

        # Stream
        stream_msg, _ = _apply_stream(parser, chunks)

        # Compare
        assert len(non_stream_msg["tool_calls"]) == len(stream_msg["tool_calls"])
        assert non_stream_msg["tool_calls"][0]["function"]["name"] == stream_msg["tool_calls"][0]["function"]["name"]
        assert non_stream_msg["tool_calls"][1]["function"]["name"] == stream_msg["tool_calls"][1]["function"]["name"]
        assert non_stream_finish == stream_msg["finish_reason"]

    def test_thinking_with_tools_parity(self) -> None:
        """Thinking with multiple tools should parse identically."""
        raw = (
            "<think>Analysis"
            "<tool_call>analyze</tool_call>"
            "<tool_call>summarize</tool_call>"
            "</think>"
        )
        chunks = [
            "<think>Analy",
            "sis<tool_call>ana",
            "lyze</tool_call><tool_call>summ",
            "arize</tool_call></think>",
        ]

        parser = ParseTagsParser({
            "parse_thinking": True,
            "parse_tool_calls": True,
        })

        # Non-stream
        non_stream_result = _apply_non_stream(parser, raw)
        non_stream_msg = non_stream_result["choices"][0]["message"]

        # Stream
        stream_msg, _ = _apply_stream(parser, chunks)

        # Compare
        assert non_stream_msg["reasoning_content"] == stream_msg["reasoning_content"]
        assert len(non_stream_msg["tool_calls"]) == len(stream_msg["tool_calls"])


class TestK2FormatMultipleTools:
    """Tests for K2 format with multiple tool calls."""

    def test_k2_multiple_tools_non_stream(self) -> None:
        """K2 format with multiple tool calls in non-stream."""
        raw = (
            "<|tool_calls_section_begin|>"
            "<|tool_call_begin|>search<|tool_call_argument_begin|>{\"q\":\"test\"}<|tool_call_end|>"
            "<|tool_call_begin|>fetch<|tool_call_argument_begin|>{\"url\":\"http://x\"}<|tool_call_end|>"
            "<|tool_calls_section_end|>"
        )
        parser = ParseTagsParser({
            "parse_thinking": True,
            "parse_tool_calls": True,
            "tool_arg_format": "json",
            "tool_open": "<|tool_call_begin|>",
            "tool_close": "<|tool_call_end|>",
            "tool_arg_separator": "<|tool_call_argument_begin|>",
            "drop_tags": ["<|tool_calls_section_begin|>", "<|tool_calls_section_end|>"],
        })
        result = _apply_non_stream(parser, raw)
        message = result["choices"][0]["message"]

        assert len(message["tool_calls"]) == 2
        assert message["tool_calls"][0]["function"]["name"] == "search"
        assert message["tool_calls"][1]["function"]["name"] == "fetch"

    def test_k2_multiple_tools_streaming(self) -> None:
        """K2 format with multiple tool calls in streaming."""
        chunks = [
            "<|tool_calls_section_begin|><|tool_call_begin|>search",
            "<|tool_call_argument_begin|>{\"q\":\"test\"}<|tool_call_end|>",
            "<|tool_call_begin|>fetch<|tool_call_argument_begin|>",
            "{\"url\":\"http://x\"}<|tool_call_end|><|tool_calls_section_end|>",
        ]
        parser = ParseTagsParser({
            "parse_thinking": True,
            "parse_tool_calls": True,
            "tool_arg_format": "json",
            "tool_open": "<|tool_call_begin|>",
            "tool_close": "<|tool_call_end|>",
            "tool_arg_separator": "<|tool_call_argument_begin|>",
            "drop_tags": ["<|tool_calls_section_begin|>", "<|tool_calls_section_end|>"],
        })
        message, _ = _apply_stream(parser, chunks)

        assert len(message["tool_calls"]) == 2
        assert message["tool_calls"][0]["function"]["name"] == "search"
        assert message["tool_calls"][1]["function"]["name"] == "fetch"
        assert message["finish_reason"] == "tool_calls"
