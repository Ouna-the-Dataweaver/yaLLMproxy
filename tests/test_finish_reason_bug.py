"""Tests for finish_reason preservation bug.

This test suite reproduces the bug where finish_reason is incorrectly
changed from "stop" to "tool_calls" even when no tool calls are present.

Bug description:
- User sends a simple request like "What day is today?"
- Model responds with normal text, no tool calls
- Unparsed log shows: "finish_reason": "stop"
- Parsed log shows: "finish_reason": "tool_calls" (WRONG!)
"""

from __future__ import annotations

import json
import sys
from pathlib import Path
from typing import Any, Iterable

import httpx
import pytest

sys.path.insert(0, str(Path(__file__).parent.parent / "src"))

from src.parsers.response_pipeline import (
    ModuleContext,
    ParseTagsParser,
    ResponseStreamParser,
    ResponseParserPipeline,
)
from src.core.upstream_transport import clear_upstream_transports, register_upstream_transport
from src.modules.response_pipeline import SSEDecoder
from src.testing import FakeUpstream, ProxyHarness, UpstreamResponse


def _build_stream_event(
    content: str | None = None,
    finish_reason: str | None = None,
    index: int = 0,
) -> dict[str, Any]:
    """Build a streaming event with optional content and finish_reason."""
    delta: dict[str, Any] = {}
    if content is not None:
        delta["content"] = content

    choice: dict[str, Any] = {"index": index, "delta": delta}
    if finish_reason is not None:
        choice["finish_reason"] = finish_reason

    return {"choices": [choice]}


def _apply_stream_events(
    parser: ParseTagsParser, events: Iterable[dict[str, Any]]
) -> tuple[list[dict[str, Any]], str | None]:
    """Apply stream events through parser and return output events + final finish_reason."""
    state = parser.create_stream_state()
    ctx = ModuleContext(
        path="/chat/completions",
        model="test-model",
        backend="test-backend",
        is_stream=True,
    )
    output_events: list[dict[str, Any]] = []

    for event in events:
        updated = parser.apply_stream_event(event, state, ctx)
        if isinstance(updated, list):
            output_events.extend(updated)
        else:
            output_events.append(updated)

    # Call finalize_stream to get any final events
    output_events.extend(parser.finalize_stream(state, ctx))

    # Extract final finish_reason
    finish_reason: str | None = None
    for event in output_events:
        choices = event.get("choices")
        if isinstance(choices, list):
            for choice in choices:
                if isinstance(choice, dict):
                    fr = choice.get("finish_reason")
                    if fr is not None:
                        finish_reason = fr

    return output_events, finish_reason


class TestFinishReasonPreservation:
    """Test that finish_reason is preserved when no tool calls are present."""

    def test_simple_text_response_preserves_stop(self) -> None:
        """Simple text response should keep finish_reason='stop'."""
        parser = ParseTagsParser({
            "parse_thinking": True,
            "parse_tool_calls": True,
        })

        # Simulate a simple streaming response: "Today is Monday."
        events = [
            _build_stream_event(content="Today "),
            _build_stream_event(content="is "),
            _build_stream_event(content="Monday."),
            _build_stream_event(content=None, finish_reason="stop"),
        ]

        output_events, finish_reason = _apply_stream_events(parser, events)

        # finish_reason should remain "stop", not "tool_calls"
        assert finish_reason == "stop", (
            f"Expected finish_reason='stop' but got '{finish_reason}'. "
            "The parser incorrectly changed finish_reason to 'tool_calls' "
            "even though no tool calls were present in the response."
        )

    def test_empty_delta_with_stop_preserves_stop(self) -> None:
        """Empty delta with finish_reason='stop' should keep 'stop'."""
        parser = ParseTagsParser({
            "parse_thinking": True,
            "parse_tool_calls": True,
        })

        events = [
            _build_stream_event(content="Hello world"),
            _build_stream_event(content="", finish_reason="stop"),
        ]

        output_events, finish_reason = _apply_stream_events(parser, events)
        assert finish_reason == "stop"

    def test_multiline_response_preserves_stop(self) -> None:
        """Multiline response without tool calls should keep finish_reason='stop'."""
        parser = ParseTagsParser({
            "parse_thinking": True,
            "parse_tool_calls": True,
        })

        events = [
            _build_stream_event(content="Line 1\n"),
            _build_stream_event(content="Line 2\n"),
            _build_stream_event(content="Line 3"),
            _build_stream_event(content=None, finish_reason="stop"),
        ]

        output_events, finish_reason = _apply_stream_events(parser, events)
        assert finish_reason == "stop"

    def test_response_with_angle_brackets_preserves_stop(self) -> None:
        """Response containing < and > characters but not tool tags should preserve 'stop'."""
        parser = ParseTagsParser({
            "parse_thinking": True,
            "parse_tool_calls": True,
        })

        events = [
            _build_stream_event(content="The formula is: x < 10 and y > 5"),
            _build_stream_event(content=None, finish_reason="stop"),
        ]

        output_events, finish_reason = _apply_stream_events(parser, events)
        assert finish_reason == "stop"

    def test_response_with_html_like_tags_preserves_stop(self) -> None:
        """Response with HTML-like tags (not tool_call) should preserve 'stop'."""
        parser = ParseTagsParser({
            "parse_thinking": True,
            "parse_tool_calls": True,
        })

        events = [
            _build_stream_event(content="Use <code>print(x)</code> to output."),
            _build_stream_event(content=None, finish_reason="stop"),
        ]

        output_events, finish_reason = _apply_stream_events(parser, events)
        assert finish_reason == "stop"

    def test_actual_tool_call_changes_to_tool_calls(self) -> None:
        """Response with actual tool_call tag should change finish_reason to 'tool_calls'."""
        parser = ParseTagsParser({
            "parse_thinking": True,
            "parse_tool_calls": True,
        })

        events = [
            _build_stream_event(content="<tool_call>get_weather"),
            _build_stream_event(content="<arg_key>city</arg_key>"),
            _build_stream_event(content="<arg_value>London</arg_value>"),
            _build_stream_event(content="</tool_call>"),
            _build_stream_event(content=None, finish_reason="stop"),
        ]

        output_events, finish_reason = _apply_stream_events(parser, events)

        # This SHOULD be tool_calls because we actually have a tool call
        assert finish_reason == "tool_calls"


class TestResponseStreamParserFinishReason:
    """Test finish_reason handling in the full ResponseStreamParser."""

    def _make_sse_chunk(self, data: dict[str, Any]) -> bytes:
        """Create an SSE chunk from a dict."""
        return f"data: {json.dumps(data)}\n\n".encode("utf-8")

    def _make_done_chunk(self) -> bytes:
        """Create the [DONE] SSE chunk."""
        return b"data: [DONE]\n\n"

    def test_stream_parser_preserves_stop(self) -> None:
        """ResponseStreamParser should preserve finish_reason='stop' from upstream."""
        parser = ParseTagsParser({
            "parse_thinking": True,
            "parse_tool_calls": True,
        })
        pipeline = ResponseParserPipeline([parser], ["/chat/completions"])
        ctx = ModuleContext(
            path="/chat/completions",
            model="test-model",
            backend="test-backend",
            is_stream=True,
        )
        stream_parser = pipeline.create_stream_parser(ctx)
        assert stream_parser is not None

        # Feed simple chunks
        chunks = [
            self._make_sse_chunk({
                "choices": [{"index": 0, "delta": {"content": "Hello"}}]
            }),
            self._make_sse_chunk({
                "choices": [{"index": 0, "delta": {"content": " world"}}]
            }),
            self._make_sse_chunk({
                "choices": [{"index": 0, "delta": {}, "finish_reason": "stop"}]
            }),
            self._make_done_chunk(),
        ]

        all_output: list[bytes] = []
        for chunk in chunks:
            all_output.extend(stream_parser.feed_bytes(chunk))
        all_output.extend(stream_parser.finish())

        # Parse output to find finish_reason
        finish_reasons: list[str] = []
        for output in all_output:
            text = output.decode("utf-8")
            for line in text.split("\n"):
                if line.startswith("data: ") and line.strip() != "data: [DONE]":
                    try:
                        data = json.loads(line[6:])
                        choices = data.get("choices", [])
                        for choice in choices:
                            fr = choice.get("finish_reason")
                            if fr:
                                finish_reasons.append(fr)
                    except json.JSONDecodeError:
                        pass

        # Should only have "stop", not "tool_calls"
        assert "tool_calls" not in finish_reasons, (
            f"Found 'tool_calls' in finish_reasons {finish_reasons} but no tool calls were present"
        )
        assert "stop" in finish_reasons, (
            f"Expected 'stop' in finish_reasons but got {finish_reasons}"
        )


class TestParseTagsParserState:
    """Test internal state tracking in ParseTagsParser."""

    def test_saw_tool_calls_false_for_simple_response(self) -> None:
        """saw_tool_calls should be False when no tool calls are parsed."""
        parser = ParseTagsParser({
            "parse_thinking": True,
            "parse_tool_calls": True,
        })

        state = parser.create_stream_state()
        ctx = ModuleContext(
            path="/chat/completions",
            model="test-model",
            backend="test-backend",
            is_stream=True,
        )

        # Process simple content
        events = [
            {"choices": [{"index": 0, "delta": {"content": "Simple response"}}]},
            {"choices": [{"index": 0, "delta": {}, "finish_reason": "stop"}]},
        ]

        for event in events:
            parser.apply_stream_event(event, state, ctx)

        # Check state - saw_tool_calls should be False
        for choice_index, choice_state in state.choices.items():
            assert not choice_state.saw_tool_calls, (
                f"Choice {choice_index} has saw_tool_calls=True but no tool calls were parsed"
            )

    def test_saw_tool_calls_true_only_with_actual_tool_calls(self) -> None:
        """saw_tool_calls should only be True when actual tool calls are parsed."""
        parser = ParseTagsParser({
            "parse_thinking": True,
            "parse_tool_calls": True,
        })

        state = parser.create_stream_state()
        ctx = ModuleContext(
            path="/chat/completions",
            model="test-model",
            backend="test-backend",
            is_stream=True,
        )

        # Process content with actual tool call
        events = [
            {"choices": [{"index": 0, "delta": {"content": "<tool_call>test_func</tool_call>"}}]},
            {"choices": [{"index": 0, "delta": {}, "finish_reason": "stop"}]},
        ]

        for event in events:
            parser.apply_stream_event(event, state, ctx)

        # Check state - saw_tool_calls should be True
        saw_any_tool_calls = any(cs.saw_tool_calls for cs in state.choices.values())
        assert saw_any_tool_calls, "Expected saw_tool_calls=True when tool_call tag is present"


class TestReasoningSwapOnly:
    """Test swap_reasoning_content parser ONLY (like GLM-4.7 config).

    This reproduces the bug where finish_reason is changed to "tool_calls"
    when ONLY swap_reasoning_content is enabled (no parse_tags).
    """

    def test_swap_only_preserves_stop_reason(self) -> None:
        """With only swap_reasoning_content, stop_reason should NOT become tool_calls."""
        from src.parsers.response_pipeline import (
            ReasoningSwapParser,
            ResponseParserPipeline,
            ResponseStreamParser,
        )
        import json

        # GLM-4.7 config: only swap_reasoning_content, no parse_tags
        swap_parser = ReasoningSwapParser({
            "mode": "reasoning_to_content",
            "think_tag": "think",
            "include_newline": False,
        })

        pipeline = ResponseParserPipeline(
            [swap_parser],
            ["/chat/completions"]
        )

        ctx = ModuleContext(
            path="/chat/completions",
            model="GLM-4.7",
            backend="test-backend",
            is_stream=True,
        )
        stream_parser = pipeline.create_stream_parser(ctx)
        assert stream_parser is not None

        def make_sse(data: dict) -> bytes:
            return f"data: {json.dumps(data)}\n\n".encode()

        # Exact pattern from GLM-4.7 log
        chunks = [
            # reasoning_content chunks
            make_sse({
                "id": "test123",
                "object": "chat.completion.chunk",
                "model": "glm-4.7",
                "choices": [{"index": 0, "delta": {"role": "assistant", "reasoning_content": "The user is asking a simple question."}}]
            }),
            # content chunks (actual answer)
            make_sse({
                "id": "test123",
                "object": "chat.completion.chunk",
                "model": "glm-4.7",
                "choices": [{"index": 0, "delta": {"role": "assistant", "content": "Today is Tuesday."}}]
            }),
            # Final chunk with finish_reason="stop"
            make_sse({
                "id": "test123",
                "object": "chat.completion.chunk",
                "model": "glm-4.7",
                "choices": [{"index": 0, "finish_reason": "stop", "delta": {"role": "assistant", "content": ""}}],
                "usage": {"prompt_tokens": 100, "completion_tokens": 10, "total_tokens": 110}
            }),
            b"data: [DONE]\n\n",
        ]

        all_output: list[bytes] = []
        for chunk in chunks:
            all_output.extend(stream_parser.feed_bytes(chunk))
        all_output.extend(stream_parser.finish())

        # Check the stop_reason on the parser BEFORE it gets used by router
        assert stream_parser.stop_reason != "tool_calls", (
            f"BUG: stream_parser.stop_reason is '{stream_parser.stop_reason}' but should NOT be 'tool_calls'! "
            f"_saw_tool_calls={stream_parser._saw_tool_calls}, _last_finish_reason={stream_parser._last_finish_reason}"
        )

        # Parse all output to find finish_reasons
        finish_reasons: list[str] = []
        for output in all_output:
            text = output.decode("utf-8")
            for line in text.split("\n"):
                if line.startswith("data: ") and line.strip() != "data: [DONE]":
                    try:
                        data = json.loads(line[6:])
                        choices = data.get("choices", [])
                        for choice in choices:
                            fr = choice.get("finish_reason")
                            if fr:
                                finish_reasons.append(fr)
                    except json.JSONDecodeError:
                        pass

        assert "tool_calls" not in finish_reasons, (
            f"BUG REPRODUCED: Found 'tool_calls' in finish_reasons {finish_reasons} "
            "but no tool calls were present! Only swap_reasoning_content was used."
        )
        assert "stop" in finish_reasons, f"Expected 'stop' in {finish_reasons}"

    def test_swap_plus_parse_tags_with_think_content(self) -> None:
        """Test when swap outputs <think> content that parse_tags might try to re-parse.

        This is a potential edge case where the output of swap_reasoning_content
        contains <think> tags, and if parse_tags runs AFTER, it might try to
        parse these tags again.
        """
        from src.parsers.response_pipeline import (
            ReasoningSwapParser,
            ResponseParserPipeline,
        )
        import json

        # Pipeline: swap first, then parse_tags (this is the WRONG order,
        # but we test it to understand the behavior)
        swap_parser = ReasoningSwapParser({
            "mode": "reasoning_to_content",
            "think_tag": "think",
            "include_newline": False,
        })
        parse_tags_parser = ParseTagsParser({
            "parse_thinking": True,
            "parse_tool_calls": True,
            "think_tag": "think",
        })

        pipeline = ResponseParserPipeline(
            [swap_parser, parse_tags_parser],  # swap first, then parse_tags
            ["/chat/completions"]
        )

        ctx = ModuleContext(
            path="/chat/completions",
            model="test-model",
            backend="test-backend",
            is_stream=True,
        )
        stream_parser = pipeline.create_stream_parser(ctx)
        assert stream_parser is not None

        def make_sse(data: dict) -> bytes:
            return f"data: {json.dumps(data)}\n\n".encode()

        chunks = [
            # Reasoning content (swap will wrap in <think>)
            make_sse({
                "choices": [{"index": 0, "delta": {"reasoning_content": "Thinking..."}}]
            }),
            # Content (swap will close </think> and add this)
            make_sse({
                "choices": [{"index": 0, "delta": {"content": "Hello!"}}]
            }),
            # Finish
            make_sse({
                "choices": [{"index": 0, "delta": {}, "finish_reason": "stop"}]
            }),
            b"data: [DONE]\n\n",
        ]

        all_output: list[bytes] = []
        for chunk in chunks:
            all_output.extend(stream_parser.feed_bytes(chunk))
        all_output.extend(stream_parser.finish())

        # Debug: check internal state
        print(f"\nstream_parser.stop_reason = {stream_parser.stop_reason}")
        print(f"stream_parser._saw_tool_calls = {stream_parser._saw_tool_calls}")
        print(f"stream_parser._last_finish_reason = {stream_parser._last_finish_reason}")

        # Check for parser states
        for i, (parser, state) in enumerate(zip(pipeline.parsers, stream_parser.states)):
            print(f"\nParser {i}: {parser.name}")
            if hasattr(state, 'choices'):
                for choice_idx, choice_state in state.choices.items():
                    if hasattr(choice_state, 'saw_tool_calls'):
                        print(f"  choice {choice_idx}: saw_tool_calls = {choice_state.saw_tool_calls}")

        # Check the stop_reason
        assert stream_parser.stop_reason != "tool_calls", (
            f"BUG: stop_reason is '{stream_parser.stop_reason}' when no tool calls present"
        )


class TestReasoningSwapWithParseTags:
    """Test the interaction between swap_reasoning_content and parse_tags parsers.

    This reproduces the bug where finish_reason is changed to "tool_calls"
    when using swap_reasoning_content to wrap reasoning into <think> tags.
    """

    def test_reasoning_swap_then_parse_tags_preserves_stop(self) -> None:
        """When swap wraps reasoning in <think> tags, parse_tags should NOT report tool_calls."""
        from src.parsers.response_pipeline import (
            ReasoningSwapParser,
            ResponseParserPipeline,
        )

        # Create pipeline with both parsers (order: swap first, then parse_tags)
        swap_parser = ReasoningSwapParser({
            "mode": "reasoning_to_content",
            "think_tag": "think",
        })
        parse_tags_parser = ParseTagsParser({
            "parse_thinking": True,
            "parse_tool_calls": True,
        })

        # Note: In the actual code, parse_tags runs BEFORE swap_reasoning_content
        # But let's test both orders to understand the behavior
        pipeline = ResponseParserPipeline(
            [swap_parser, parse_tags_parser],
            ["/chat/completions"]
        )

        ctx = ModuleContext(
            path="/chat/completions",
            model="test-model",
            backend="test-backend",
            is_stream=True,
        )
        stream_parser = pipeline.create_stream_parser(ctx)
        assert stream_parser is not None

        # Simulate the GLM-4.7 response pattern:
        # 1. reasoning_content chunks
        # 2. content chunks
        # 3. finish_reason="stop"
        import json

        def make_sse(data: dict) -> bytes:
            return f"data: {json.dumps(data)}\n\n".encode()

        chunks = [
            # Reasoning content chunks (like GLM-4.7 sends)
            make_sse({
                "choices": [{"index": 0, "delta": {"reasoning_content": "The user is asking"}}]
            }),
            make_sse({
                "choices": [{"index": 0, "delta": {"reasoning_content": " a simple question."}}]
            }),
            # Content chunks
            make_sse({
                "choices": [{"index": 0, "delta": {"content": "Today is Tuesday."}}]
            }),
            # Finish with stop
            make_sse({
                "choices": [{"index": 0, "delta": {}, "finish_reason": "stop"}]
            }),
            b"data: [DONE]\n\n",
        ]

        all_output: list[bytes] = []
        for chunk in chunks:
            all_output.extend(stream_parser.feed_bytes(chunk))
        all_output.extend(stream_parser.finish())

        # Parse all output to find finish_reasons
        finish_reasons: list[str] = []
        for output in all_output:
            text = output.decode("utf-8")
            for line in text.split("\n"):
                if line.startswith("data: ") and line.strip() != "data: [DONE]":
                    try:
                        data = json.loads(line[6:])
                        choices = data.get("choices", [])
                        for choice in choices:
                            fr = choice.get("finish_reason")
                            if fr:
                                finish_reasons.append(fr)
                    except json.JSONDecodeError:
                        pass

        # The bug: finish_reasons contains "tool_calls" when it shouldn't
        assert "tool_calls" not in finish_reasons, (
            f"BUG REPRODUCED: Found 'tool_calls' in finish_reasons {finish_reasons} "
            "but no tool calls were present! The reasoning content wrapped in <think> tags "
            "is incorrectly triggering tool_calls detection."
        )
        assert "stop" in finish_reasons

    def test_parse_tags_before_swap_preserves_stop(self) -> None:
        """With correct ordering (parse_tags before swap), finish_reason should be preserved."""
        from src.parsers.response_pipeline import ReasoningSwapParser

        # Correct order: parse_tags BEFORE swap_reasoning_content
        parse_tags_parser = ParseTagsParser({
            "parse_thinking": True,
            "parse_tool_calls": True,
        })
        swap_parser = ReasoningSwapParser({
            "mode": "reasoning_to_content",
            "think_tag": "think",
        })

        pipeline = ResponseParserPipeline(
            [parse_tags_parser, swap_parser],
            ["/chat/completions"]
        )

        ctx = ModuleContext(
            path="/chat/completions",
            model="test-model",
            backend="test-backend",
            is_stream=True,
        )
        stream_parser = pipeline.create_stream_parser(ctx)
        assert stream_parser is not None

        import json

        def make_sse(data: dict) -> bytes:
            return f"data: {json.dumps(data)}\n\n".encode()

        chunks = [
            make_sse({
                "choices": [{"index": 0, "delta": {"reasoning_content": "Thinking..."}}]
            }),
            make_sse({
                "choices": [{"index": 0, "delta": {"content": "Hello!"}}]
            }),
            make_sse({
                "choices": [{"index": 0, "delta": {}, "finish_reason": "stop"}]
            }),
            b"data: [DONE]\n\n",
        ]

        all_output: list[bytes] = []
        for chunk in chunks:
            all_output.extend(stream_parser.feed_bytes(chunk))
        all_output.extend(stream_parser.finish())

        finish_reasons: list[str] = []
        for output in all_output:
            text = output.decode("utf-8")
            for line in text.split("\n"):
                if line.startswith("data: ") and line.strip() != "data: [DONE]":
                    try:
                        data = json.loads(line[6:])
                        choices = data.get("choices", [])
                        for choice in choices:
                            fr = choice.get("finish_reason")
                            if fr:
                                finish_reasons.append(fr)
                    except json.JSONDecodeError:
                        pass

        assert "tool_calls" not in finish_reasons, (
            f"Found 'tool_calls' in finish_reasons {finish_reasons}"
        )


class TestNonStreamFinishReason:
    """Test finish_reason in non-streaming responses."""

    def test_non_stream_preserves_stop_without_tool_calls(self) -> None:
        """Non-stream response without tool calls should keep finish_reason unchanged."""
        parser = ParseTagsParser({
            "parse_thinking": True,
            "parse_tool_calls": True,
        })

        payload = {
            "choices": [{
                "index": 0,
                "message": {"role": "assistant", "content": "Simple answer"},
                "finish_reason": "stop",
            }]
        }

        ctx = ModuleContext(
            path="/chat/completions",
            model="test-model",
            backend="test-backend",
            is_stream=False,
        )

        updated = parser.apply_response(payload, ctx)

        # finish_reason should remain "stop"
        assert updated["choices"][0]["finish_reason"] == "stop"

    def test_non_stream_changes_to_tool_calls_with_tool_call(self) -> None:
        """Non-stream response with tool_call tag should change finish_reason to 'tool_calls'."""
        parser = ParseTagsParser({
            "parse_thinking": True,
            "parse_tool_calls": True,
        })

        payload = {
            "choices": [{
                "index": 0,
                "message": {
                    "role": "assistant",
                    "content": "<tool_call>my_function</tool_call>",
                },
                "finish_reason": "stop",
            }]
        }

        ctx = ModuleContext(
            path="/chat/completions",
            model="test-model",
            backend="test-backend",
            is_stream=False,
        )

        updated = parser.apply_response(payload, ctx)

        # finish_reason should be changed to "tool_calls"
        assert updated["choices"][0]["finish_reason"] == "tool_calls"


@pytest.fixture(autouse=True)
def _clear_transport_registry():
    yield
    clear_upstream_transports()


def _build_glm47_config(base_url: str) -> dict:
    """Build config that matches GLM-4.7 setup: only swap_reasoning_content."""
    return {
        "model_list": [
            {
                "model_name": "GLM-4.7",
                "model_params": {
                    "model": "glm-4.7",
                    "api_base": base_url,
                    "api_key": "test-key",
                },
                "modules": {
                    "upstream": {
                        "enabled": True,
                        "response": ["swap_reasoning_content"],
                        "swap_reasoning_content": {
                            "mode": "reasoning_to_content",
                            "think_tag": "think",
                            "include_newline": False,
                        },
                    }
                },
            }
        ],
        "proxy_settings": {
            "logging": {
                "log_parsed_response": True,
                "log_parsed_stream": True,
            }
        },
    }


class TestFullSimulationGLM47:
    """Full simulation tests using FakeUpstream to reproduce the GLM-4.7 bug."""

    @pytest.mark.asyncio
    async def test_glm47_reasoning_content_stream_finish_reason(self) -> None:
        """Reproduce the GLM-4.7 bug: reasoning_content + content stream should keep finish_reason='stop'.

        This simulates the exact pattern from the log:
        1. Stream chunks with reasoning_content
        2. Stream chunks with content
        3. Final chunk with finish_reason="stop"
        4. [DONE]

        The bug: finish_reason incorrectly becomes "tool_calls" after processing.
        """
        # Build stream events matching the GLM-4.7 log pattern
        stream_events = [
            # Reasoning content chunks
            {
                "id": "test123",
                "created": 1769471923,
                "object": "chat.completion.chunk",
                "model": "glm-4.7",
                "choices": [{"index": 0, "delta": {"role": "assistant", "reasoning_content": "The user is asking"}}]
            },
            {
                "id": "test123",
                "created": 1769471923,
                "object": "chat.completion.chunk",
                "model": "glm-4.7",
                "choices": [{"index": 0, "delta": {"role": "assistant", "reasoning_content": " a simple question."}}]
            },
            # Content chunks (the actual answer)
            {
                "id": "test123",
                "created": 1769471923,
                "object": "chat.completion.chunk",
                "model": "glm-4.7",
                "choices": [{"index": 0, "delta": {"role": "assistant", "content": "Today"}}]
            },
            {
                "id": "test123",
                "created": 1769471923,
                "object": "chat.completion.chunk",
                "model": "glm-4.7",
                "choices": [{"index": 0, "delta": {"role": "assistant", "content": " is Tuesday."}}]
            },
            # Final chunk with finish_reason="stop" - THIS IS KEY
            {
                "id": "test123",
                "created": 1769471923,
                "object": "chat.completion.chunk",
                "model": "glm-4.7",
                "choices": [{"index": 0, "finish_reason": "stop", "delta": {"role": "assistant", "content": ""}}],
                "usage": {"prompt_tokens": 100, "completion_tokens": 10, "total_tokens": 110}
            },
        ]

        upstream = FakeUpstream()
        upstream.enqueue(UpstreamResponse(stream=True, stream_events=stream_events))

        base_url = "http://upstream.local/v1"
        register_upstream_transport("upstream.local", httpx.ASGITransport(app=upstream.app))

        with ProxyHarness(_build_glm47_config(base_url)) as proxy:
            async with proxy.make_async_client() as client:
                async with client.stream(
                    "POST",
                    "/v1/chat/completions",
                    json={
                        "model": "GLM-4.7",
                        "messages": [{"role": "user", "content": "What day is today?"}],
                        "stream": True,
                    },
                ) as response:
                    assert response.status_code == 200
                    decoder = SSEDecoder()
                    events: list[dict] = []
                    finish_reasons: list[str] = []

                    async for chunk in response.aiter_raw():
                        for event in decoder.feed(chunk):
                            if event.data is None:
                                continue
                            if event.data.strip() == "[DONE]":
                                continue
                            try:
                                parsed = json.loads(event.data)
                                events.append(parsed)
                                # Extract finish_reason from each event
                                for choice in parsed.get("choices", []):
                                    fr = choice.get("finish_reason")
                                    if fr:
                                        finish_reasons.append(fr)
                            except json.JSONDecodeError:
                                pass

        # The bug: "tool_calls" appears in finish_reasons when it shouldn't
        assert "tool_calls" not in finish_reasons, (
            f"BUG REPRODUCED: finish_reasons contains 'tool_calls' but no tool calls were present! "
            f"finish_reasons={finish_reasons}"
        )
        assert "stop" in finish_reasons, f"Expected 'stop' in finish_reasons, got {finish_reasons}"

    @pytest.mark.asyncio
    async def test_stop_reason_correctly_uses_last_finish_reason(self) -> None:
        """Test that router correctly uses _last_finish_reason when stop_reason is None.

        Fixed in router.py line 952:
            stop_reason = stream_parser.stop_reason or stream_parser._last_finish_reason or "stop"

        When upstream sends finish_reason="stop":
        - _track_event sets stop_requested=True
        - stop_reason stays None (only set for tool_calls finish_reason)
        - Router fallback correctly uses _last_finish_reason ("stop") instead of "tool_calls"

        This tests the ResponseStreamParser's state and verifies the fix.
        """
        from src.parsers.response_pipeline import (
            ReasoningSwapParser,
            ResponseParserPipeline,
        )

        swap_parser = ReasoningSwapParser({
            "mode": "reasoning_to_content",
            "think_tag": "think",
            "include_newline": False,
        })

        pipeline = ResponseParserPipeline([swap_parser], ["/chat/completions"])
        ctx = ModuleContext(
            path="/chat/completions",
            model="test",
            backend="test",
            is_stream=True,
        )
        stream_parser = pipeline.create_stream_parser(ctx)
        assert stream_parser is not None

        # Simulate upstream sending finish_reason="stop"
        chunk = (
            b'data: {"choices":[{"index":0,"delta":{"content":"Hello"}}]}\n\n'
            b'data: {"choices":[{"index":0,"finish_reason":"stop","delta":{}}]}\n\n'
            b'data: [DONE]\n\n'
        )

        stream_parser.feed_bytes(chunk)
        stream_parser.finish()

        # Check state
        print(f"\nstop_requested: {stream_parser.stop_requested}")
        print(f"stop_reason: {stream_parser.stop_reason}")
        print(f"_last_finish_reason: {stream_parser._last_finish_reason}")

        # The FIXED router logic:
        # stop_reason = stream_parser.stop_reason or stream_parser._last_finish_reason or "stop"
        # This now correctly uses _last_finish_reason when stop_reason is None

        if stream_parser.stop_requested:
            # This is the FIXED logic from router.py line 952
            router_stop_reason = stream_parser.stop_reason or stream_parser._last_finish_reason or "stop"
            print(f"Router uses stop_reason: {router_stop_reason}")

            # Verify the fix: should NOT be "tool_calls" when no tool calls present
            assert router_stop_reason != "tool_calls" or stream_parser._saw_tool_calls, (
                f"REGRESSION: stop_reason should not be 'tool_calls' without tool calls! "
                f"Got router_stop_reason='{router_stop_reason}', "
                f"_last_finish_reason='{stream_parser._last_finish_reason}'"
            )

            # Verify it correctly uses the upstream finish_reason
            assert router_stop_reason == "stop", (
                f"Expected router_stop_reason='stop' (from _last_finish_reason), "
                f"but got '{router_stop_reason}'"
            )

    @pytest.mark.asyncio
    async def test_glm47_chunked_boundaries(self) -> None:
        """Test with chunk boundaries that split JSON - like real network delivery.

        In the actual log, chunks are split at arbitrary points:
        - STREAM CHUNK 2 ends with: `data: {"i`
        - STREAM CHUNK 3 starts with: `d":"20260127...`

        This tests if partial JSON handling could cause issues.
        """
        from src.parsers.response_pipeline import ResponseStreamParser, ResponseParserPipeline, ReasoningSwapParser

        swap_parser = ReasoningSwapParser({
            "mode": "reasoning_to_content",
            "think_tag": "think",
            "include_newline": False,
        })

        pipeline = ResponseParserPipeline([swap_parser], ["/chat/completions"])
        ctx = ModuleContext(
            path="/chat/completions",
            model="GLM-4.7",
            backend="test",
            is_stream=True,
        )
        stream_parser = pipeline.create_stream_parser(ctx)
        assert stream_parser is not None

        # Simulate chunked delivery like the actual network
        # First chunk: reasoning content
        chunk1 = (
            b'data: {"id":"test","choices":[{"index":0,"delta":{"reasoning_content":"Thinking"}}]}\n\n'
            b'data: {"i'  # Partial JSON at end
        )
        # Second chunk: completes the partial JSON
        chunk2 = (
            b'd":"test","choices":[{"index":0,"delta":{"reasoning_content":" more"}}]}\n\n'
        )
        # Third chunk: content
        chunk3 = (
            b'data: {"id":"test","choices":[{"index":0,"delta":{"content":"Answer"}}]}\n\n'
        )
        # Fourth chunk: finish_reason="stop" and [DONE]
        chunk4 = (
            b'data: {"id":"test","choices":[{"index":0,"finish_reason":"stop","delta":{}}]}\n\n'
            b'data: [DONE]\n\n'
        )

        all_output = []
        for chunk in [chunk1, chunk2, chunk3, chunk4]:
            all_output.extend(stream_parser.feed_bytes(chunk))
        all_output.extend(stream_parser.finish())

        print(f"\nstream_parser.stop_reason = {stream_parser.stop_reason}")
        print(f"stream_parser._saw_tool_calls = {stream_parser._saw_tool_calls}")
        print(f"stream_parser._saw_done = {stream_parser._saw_done}")
        print(f"stream_parser._last_finish_reason = {stream_parser._last_finish_reason}")

        # Parse output to find finish_reasons
        finish_reasons = []
        for out in all_output:
            text = out.decode("utf-8")
            for line in text.split("\n"):
                if line.startswith("data: ") and "[DONE]" not in line:
                    try:
                        data = json.loads(line[6:])
                        for choice in data.get("choices", []):
                            fr = choice.get("finish_reason")
                            if fr:
                                finish_reasons.append(fr)
                    except:
                        pass

        print(f"finish_reasons in output: {finish_reasons}")

        assert stream_parser.stop_reason != "tool_calls", (
            f"stop_reason should NOT be 'tool_calls' but is '{stream_parser.stop_reason}'"
        )
        assert "tool_calls" not in finish_reasons

    @pytest.mark.asyncio
    async def test_glm47_with_tools_in_request(self) -> None:
        """Test with tools defined in the request - this might trigger the bug.

        In the actual logs, the request has 18-19 tools defined, but the model
        responds with just <think> reasoning and text, no tool calls.
        Yet stop_reason incorrectly becomes "tool_calls".
        """
        stream_events = [
            # Reasoning content
            {
                "id": "test123",
                "object": "chat.completion.chunk",
                "model": "glm-4.7",
                "choices": [{"index": 0, "delta": {"role": "assistant", "reasoning_content": "The user is asking a simple question."}}]
            },
            # Content (the answer)
            {
                "id": "test123",
                "object": "chat.completion.chunk",
                "model": "glm-4.7",
                "choices": [{"index": 0, "delta": {"role": "assistant", "content": "Today is Tuesday."}}]
            },
            # Final chunk with finish_reason="stop"
            {
                "id": "test123",
                "object": "chat.completion.chunk",
                "model": "glm-4.7",
                "choices": [{"index": 0, "finish_reason": "stop", "delta": {"role": "assistant", "content": ""}}],
                "usage": {"prompt_tokens": 100, "completion_tokens": 10, "total_tokens": 110}
            },
        ]

        upstream = FakeUpstream()
        upstream.enqueue(UpstreamResponse(stream=True, stream_events=stream_events))

        base_url = "http://upstream.local/v1"
        register_upstream_transport("upstream.local", httpx.ASGITransport(app=upstream.app))

        # Define tools in the request (like the actual client does)
        tools = [
            {
                "type": "function",
                "function": {
                    "name": f"tool_{i}",
                    "description": f"Test tool {i}",
                    "parameters": {"type": "object", "properties": {}}
                }
            }
            for i in range(18)  # 18 tools like in the actual request
        ]

        with ProxyHarness(_build_glm47_config(base_url)) as proxy:
            async with proxy.make_async_client() as client:
                async with client.stream(
                    "POST",
                    "/v1/chat/completions",
                    json={
                        "model": "GLM-4.7",
                        "messages": [{"role": "user", "content": "What day is today?"}],
                        "stream": True,
                        "tools": tools,  # KEY: Include tools in request
                    },
                ) as response:
                    assert response.status_code == 200
                    decoder = SSEDecoder()
                    finish_reasons: list[str] = []

                    async for chunk in response.aiter_raw():
                        for event in decoder.feed(chunk):
                            if event.data is None:
                                continue
                            if event.data.strip() == "[DONE]":
                                continue
                            try:
                                parsed = json.loads(event.data)
                                for choice in parsed.get("choices", []):
                                    fr = choice.get("finish_reason")
                                    if fr:
                                        finish_reasons.append(fr)
                            except json.JSONDecodeError:
                                pass

        print(f"\nFinish reasons with tools in request: {finish_reasons}")

        # The bug: "tool_calls" appears when it shouldn't
        assert "tool_calls" not in finish_reasons, (
            f"BUG REPRODUCED: 'tool_calls' found in finish_reasons {finish_reasons} "
            "but the model didn't make any tool calls! Request had tools defined but response was just text."
        )

    @pytest.mark.asyncio
    async def test_glm47_exact_log_replay(self) -> None:
        """Replay the exact events from the GLM-4.7 log file."""
        # These are the exact events extracted from the unparsed log
        # (simplified - just the key structure)
        stream_events = []

        # Add reasoning_content chunks (simplified version of the log)
        reasoning_chunks = [
            "The", " user", " is", " asking", " a", " simple", " question", ":",
            " \"", "What", " day", " is", " today", "?\"\n\n", "Looking", " at",
            " the", " user", "_info", " section", ",", " I", " can", " see", ":\n",
            "\"", "Today", "'s", " date", ":", " Tuesday", " Jan", " ", "27", ",",
            " ", "202", "6", "\"\n\n", "So", " I", " can", " answer", " this",
            " directly", " without", " needing", " to", " use", " any", " tools",
            ".", " This", " is", " a", " straightforward", " informational",
            " question", " that", " doesn", "'t", " require", " exploring", " the",
            " code", "base", " or", " making", " any", " changes", ".\n\n", "Since",
            " I", "'m", " in", " Ask", " mode", ",", " I", " should", " just",
            " provide", " a", " clear", ",", " direct", " answer", " to", " the",
            " user", "'s", " question", "."
        ]

        for chunk in reasoning_chunks:
            stream_events.append({
                "id": "20260127075843dc50d2a606b2480e",
                "created": 1769471923,
                "object": "chat.completion.chunk",
                "model": "glm-4.7",
                "choices": [{"index": 0, "delta": {"role": "assistant", "reasoning_content": chunk}}]
            })

        # Add content chunks
        content_chunks = ["Today", " is", " Tuesday", ",", " January", " ", "27", ",", " ", "202", "6", "."]
        for chunk in content_chunks:
            stream_events.append({
                "id": "20260127075843dc50d2a606b2480e",
                "created": 1769471923,
                "object": "chat.completion.chunk",
                "model": "glm-4.7",
                "choices": [{"index": 0, "delta": {"role": "assistant", "content": chunk}}]
            })

        # Final chunk with finish_reason="stop"
        stream_events.append({
            "id": "20260127075843dc50d2a606b2480e",
            "created": 1769471923,
            "object": "chat.completion.chunk",
            "model": "glm-4.7",
            "choices": [{"index": 0, "finish_reason": "stop", "delta": {"role": "assistant", "content": ""}}],
            "usage": {
                "prompt_tokens": 17121,
                "completion_tokens": 107,
                "total_tokens": 17228,
                "prompt_tokens_details": {"cached_tokens": 46},
                "completion_tokens_details": {"reasoning_tokens": 93}
            }
        })

        upstream = FakeUpstream()
        upstream.enqueue(UpstreamResponse(stream=True, stream_events=stream_events))

        base_url = "http://upstream.local/v1"
        register_upstream_transport("upstream.local", httpx.ASGITransport(app=upstream.app))

        with ProxyHarness(_build_glm47_config(base_url)) as proxy:
            async with proxy.make_async_client() as client:
                async with client.stream(
                    "POST",
                    "/v1/chat/completions",
                    json={
                        "model": "GLM-4.7",
                        "messages": [{"role": "user", "content": "What day is today?"}],
                        "stream": True,
                    },
                ) as response:
                    assert response.status_code == 200
                    decoder = SSEDecoder()
                    finish_reasons: list[str] = []
                    all_events: list[dict] = []

                    async for chunk in response.aiter_raw():
                        for event in decoder.feed(chunk):
                            if event.data is None:
                                continue
                            if event.data.strip() == "[DONE]":
                                continue
                            try:
                                parsed = json.loads(event.data)
                                all_events.append(parsed)
                                for choice in parsed.get("choices", []):
                                    fr = choice.get("finish_reason")
                                    if fr:
                                        finish_reasons.append(fr)
                            except json.JSONDecodeError:
                                pass

        print(f"\nTotal events received: {len(all_events)}")
        print(f"Finish reasons found: {finish_reasons}")

        # The bug: "tool_calls" appears when it shouldn't
        assert "tool_calls" not in finish_reasons, (
            f"BUG REPRODUCED: 'tool_calls' found in finish_reasons but no tool calls present! "
            f"All finish_reasons: {finish_reasons}"
        )
