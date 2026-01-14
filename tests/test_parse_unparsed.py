"""Tests for parse_unparsed non-stream behavior."""

from __future__ import annotations

import sys
from pathlib import Path

# Add src directory to path
sys.path.insert(0, str(Path(__file__).parent.parent / "src"))

from src.modules.response_pipeline import ModuleContext, ParseTagsParser


def _build_payload(content: str) -> dict:
    return {
        "choices": [
            {"index": 0, "message": {"role": "assistant", "content": content}}
        ]
    }


def test_parse_unparsed_tool_inside_think_non_stream() -> None:
    raw = (
        "<think>before"
        "<tool_call>lookup<arg_key>q</arg_key><arg_value>x</arg_value></tool_call>"
        "after</think>Answer"
    )
    parser = ParseTagsParser(
        {
            "parse_thinking": True,
            "parse_tool_calls": True,
            "think_tag": "think",
            "tool_tag": "tool_call",
        }
    )
    payload = _build_payload(raw)
    updated = parser.apply_response(
        payload,
        ModuleContext(
            path="/chat/completions",
            model="test-model",
            backend="test-backend",
            is_stream=False,
        ),
    )
    message = updated["choices"][0]["message"]

    assert message["reasoning_content"] == "beforeafter"
    assert message["content"] == "Answer"
    assert message["tool_calls"][0]["function"]["name"] == "lookup"
    assert message["tool_calls"][0]["function"]["arguments"] == "{\"q\": \"x\"}"
