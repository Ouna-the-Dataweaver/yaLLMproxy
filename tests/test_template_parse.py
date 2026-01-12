"""Tests for template-driven parsing of raw tool call/thinking output."""

from __future__ import annotations

import sys
from pathlib import Path

import pytest

# Add src directory to path
sys.path.insert(0, str(Path(__file__).parent.parent / "src"))

from src.modules.response_pipeline import ModuleContext, TemplateParseParser

PROJECT_ROOT = Path(__file__).parent.parent
K2_TEMPLATE = PROJECT_ROOT / "configs" / "jinja_templates" / "k2thinking.jinja"


def _build_payload(content: str) -> dict:
    return {
        "choices": [
            {"index": 0, "message": {"role": "assistant", "content": content}}
        ]
    }


def test_parse_template_k2_non_stream() -> None:
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
    parser = TemplateParseParser(
        {
            "template_path": str(K2_TEMPLATE),
            "parse_thinking": True,
            "parse_tool_calls": True,
        }
    )
    payload = _build_payload(raw)
    updated = parser.apply_response(payload, ModuleContext(
        path="/chat/completions",
        model="test-model",
        backend="test-backend",
        is_stream=False,
    ))
    message = updated["choices"][0]["message"]

    assert message["reasoning_content"] == "Reason"
    assert message["content"] == "Answer"
    assert message["tool_calls"][0]["function"]["name"] == "lookup"
    assert message["tool_calls"][0]["function"]["arguments"] == "{\"query\": \"x\"}"
    assert updated["choices"][0]["finish_reason"] == "tool_calls"


def test_parse_template_k2_stream() -> None:
    if not K2_TEMPLATE.exists():
        pytest.skip("k2thinking template missing")

    parser = TemplateParseParser(
        {
            "template_path": str(K2_TEMPLATE),
            "parse_thinking": True,
            "parse_tool_calls": True,
        }
    )
    state = parser.create_stream_state()
    ctx = ModuleContext(
        path="/chat/completions",
        model="test-model",
        backend="test-backend",
        is_stream=True,
    )

    event = {
        "choices": [
            {
                "index": 0,
                "delta": {
                    "content": (
                        "<think>Reason</think>Answer"
                        "<|tool_call_begin|>lookup"
                        "<|tool_call_argument_begin|>{\"query\":\"x\"}"
                        "<|tool_call_end|>"
                    )
                },
            }
        ]
    }
    updated = parser.apply_stream_event(event, state, ctx)
    events = updated if isinstance(updated, list) else [updated]
    events.extend(parser.finalize_stream(state, ctx))

    delta = events[0]["choices"][0]["delta"]
    assert delta.get("content") == "Answer"
    assert delta.get("reasoning_content") == "Reason"
    assert delta["tool_calls"][0]["function"]["name"] == "lookup"
