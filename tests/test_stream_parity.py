"""Ensure streaming and non-stream parsing stay in sync."""

from __future__ import annotations

import sys
from pathlib import Path
from typing import Any, Iterable

import pytest

sys.path.insert(0, str(Path(__file__).parent.parent / "src"))

from src.modules.response_pipeline import (
    ModuleContext,
    ParseTagsParser,
    ReasoningSwapParser,
    TemplateParseParser,
)

PROJECT_ROOT = Path(__file__).parent.parent
XML_TEMPLATE = PROJECT_ROOT / "configs" / "jinja_templates" / "template_example.jinja"
K2_TEMPLATE = PROJECT_ROOT / "configs" / "jinja_templates" / "k2thinking.jinja"


def _build_payload(content: str) -> dict[str, Any]:
    return {
        "choices": [
            {"index": 0, "message": {"role": "assistant", "content": content}}
        ]
    }


def _apply_non_stream(parser: Any, content: str) -> tuple[dict[str, Any], str | None]:
    payload = _build_payload(content)
    updated = parser.apply_response(
        payload,
        ModuleContext(
            path="/chat/completions",
            model="test-model",
            backend="test-backend",
            is_stream=False,
        ),
    )
    choice = updated["choices"][0]
    return choice["message"], choice.get("finish_reason")


def _apply_stream_events(
    parser: Any, events: Iterable[dict[str, Any]]
) -> tuple[dict[str, Any], str | None]:
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
    output_events.extend(parser.finalize_stream(state, ctx))
    return _assemble_stream(output_events)


def _assemble_stream(events: Iterable[dict[str, Any]]) -> tuple[dict[str, Any], str | None]:
    content_parts: list[str] = []
    reasoning_parts: list[str] = []
    tool_calls: list[dict[str, Any]] = []
    finish_reason: str | None = None

    for event in events:
        choices = event.get("choices")
        if not isinstance(choices, list):
            continue
        for choice in choices:
            choice_finish = choice.get("finish_reason")
            if choice_finish is not None:
                finish_reason = choice_finish
            delta = choice.get("delta")
            if not isinstance(delta, dict):
                continue
            content = delta.get("content")
            if isinstance(content, str):
                content_parts.append(content)
            reasoning = delta.get("reasoning_content")
            if isinstance(reasoning, str):
                reasoning_parts.append(reasoning)
            calls = delta.get("tool_calls")
            if isinstance(calls, list):
                tool_calls.extend(calls)

    message: dict[str, Any] = {"role": "assistant"}
    message["content"] = "".join(content_parts) if content_parts else None
    if reasoning_parts:
        message["reasoning_content"] = "".join(reasoning_parts)
    if tool_calls:
        message["tool_calls"] = tool_calls
    return message, finish_reason


def _snapshot(message: dict[str, Any], finish_reason: str | None) -> dict[str, Any]:
    content = message.get("content")
    if isinstance(content, str) and content == "":
        content = None
    reasoning = message.get("reasoning_content")
    if isinstance(reasoning, str) and reasoning == "":
        reasoning = None
    tool_calls = message.get("tool_calls")
    if not tool_calls:
        tool_calls = None
    return {
        "content": content,
        "reasoning_content": reasoning,
        "tool_calls": tool_calls,
        "finish_reason": finish_reason,
    }


def _stream_events_from_chunks(chunks: Iterable[str]) -> list[dict[str, Any]]:
    return [
        {"choices": [{"index": 0, "delta": {"content": chunk}}]}
        for chunk in chunks
    ]


def test_parse_unparsed_stream_matches_non_stream() -> None:
    raw = (
        "<think>before<tool_call>lookup<arg_key>q</arg_key><arg_value>x</arg_value>"
        "</tool_call>after</think>Answer"
    )
    chunks = [
        "<think>before<tool",
        "_call>lookup<arg_key>q</arg_key><arg_value>x</arg_value></tool_call>after</thi",
        "nk>Answer",
    ]
    parser = ParseTagsParser(
        {
            "parse_thinking": True,
            "parse_tool_calls": True,
            "think_tag": "think",
            "tool_tag": "tool_call",
        }
    )
    non_stream_message, non_stream_finish = _apply_non_stream(parser, raw)
    stream_message, stream_finish = _apply_stream_events(
        parser, _stream_events_from_chunks(chunks)
    )
    assert _snapshot(stream_message, stream_finish) == _snapshot(
        non_stream_message, non_stream_finish
    )


def test_parse_template_xml_stream_matches_non_stream() -> None:
    if not XML_TEMPLATE.exists():
        pytest.skip("template example missing")
    raw = (
        "<think>before<tool_call>lookup<arg_key>q</arg_key><arg_value>x</arg_value>"
        "</tool_call>after</think>Answer"
    )
    chunks = [
        "<think>before<tool",
        "_call>lookup<arg_key>q</arg_key><arg_value>x</arg_value></tool_call>after</thi",
        "nk>Answer",
    ]
    parser = TemplateParseParser(
        {
            "template_path": str(XML_TEMPLATE),
            "parse_thinking": True,
            "parse_tool_calls": True,
        }
    )
    non_stream_message, non_stream_finish = _apply_non_stream(parser, raw)
    stream_message, stream_finish = _apply_stream_events(
        parser, _stream_events_from_chunks(chunks)
    )
    assert _snapshot(stream_message, stream_finish) == _snapshot(
        non_stream_message, non_stream_finish
    )


def test_parse_template_k2_stream_matches_non_stream() -> None:
    if not K2_TEMPLATE.exists():
        pytest.skip("k2thinking template missing")
    raw = (
        "<think>Reason</think>Answer"
        "<|tool_calls_section_begin|>"
        "<|tool_call_begin|>lookup"
        "<|tool_call_argument_begin|>{\"query\":\"x\"}"
        "<|tool_call_end|>"
        "<|tool_calls_section_end|>"
    )
    chunks = [
        "<think>Reason</think>Answer<|tool_calls_section_begin|><|tool_call_begin|>lookup"
        "<|tool_call_argument_begin|>{\"query\":\"x\"}<|tool_call_en",
        "d|><|tool_calls_section_end|>",
    ]
    parser = TemplateParseParser(
        {
            "template_path": str(K2_TEMPLATE),
            "parse_thinking": True,
            "parse_tool_calls": True,
        }
    )
    non_stream_message, non_stream_finish = _apply_non_stream(parser, raw)
    stream_message, stream_finish = _apply_stream_events(
        parser, _stream_events_from_chunks(chunks)
    )
    assert _snapshot(stream_message, stream_finish) == _snapshot(
        non_stream_message, non_stream_finish
    )


def test_swap_reasoning_stream_matches_non_stream() -> None:
    if not XML_TEMPLATE.exists():
        pytest.skip("template example missing")
    parser = ReasoningSwapParser(
        {
            "template_path": str(XML_TEMPLATE),
        }
    )
    payload = {
        "choices": [
            {
                "index": 0,
                "message": {
                    "role": "assistant",
                    "content": "Final answer.",
                    "reasoning_content": "Reasoning line 1.",
                },
            }
        ]
    }
    updated = parser.apply_response(
        payload,
        ModuleContext(
            path="/chat/completions",
            model="test-model",
            backend="test-backend",
            is_stream=False,
        ),
    )
    non_stream_choice = updated["choices"][0]
    non_stream_message = non_stream_choice["message"]
    non_stream_finish = non_stream_choice.get("finish_reason")

    events = [
        {"choices": [{"index": 0, "delta": {"reasoning_content": "Reason"}}]},
        {"choices": [{"index": 0, "delta": {"reasoning_content": "ing line 1."}}]},
        {"choices": [{"index": 0, "delta": {"content": "Final answer."}}]},
    ]
    stream_message, stream_finish = _apply_stream_events(parser, events)

    assert _snapshot(stream_message, stream_finish) == _snapshot(
        non_stream_message, non_stream_finish
    )
