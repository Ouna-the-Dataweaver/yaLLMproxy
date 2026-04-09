"""Tests for template-driven parsing of raw tool call/thinking output."""

from __future__ import annotations

import sys
from pathlib import Path

import pytest

jinja2 = pytest.importorskip("jinja2")

# Add src directory to path
sys.path.insert(0, str(Path(__file__).parent.parent / "src"))

from src.modules.response_pipeline import ModuleContext, TemplateParseParser

PROJECT_ROOT = Path(__file__).parent.parent
K2_TEMPLATE = PROJECT_ROOT / "configs" / "jinja_templates" / "k2thinking.jinja"
XML_TEMPLATE = PROJECT_ROOT / "configs" / "jinja_templates" / "template_example.jinja"
QWEN_TEMPLATE = PROJECT_ROOT / "configs" / "jinja_templates" / "qwen.jinja"


def _build_payload(content: str) -> dict:
    return {
        "choices": [
            {"index": 0, "message": {"role": "assistant", "content": content}}
        ]
    }


def _tojson_filter(value: object, *, ensure_ascii: bool = True, **_kwargs: object) -> str:
    import json

    return json.dumps(value, ensure_ascii=ensure_ascii)


def _render_chat_template(template_path: Path, messages: list[dict]) -> str:
    env = jinja2.Environment(autoescape=False, trim_blocks=False, lstrip_blocks=False)
    env.filters["tojson"] = _tojson_filter
    template = env.from_string(template_path.read_text(encoding="utf-8"))
    return template.render(
        messages=messages,
        tools=[],
        add_generation_prompt=False,
        add_vision_id=False,
    )


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


def test_parse_template_tool_inside_think_non_stream() -> None:
    if not XML_TEMPLATE.exists():
        pytest.skip("template example missing")

    raw = (
        "<think>before"
        "<tool_call>lookup<arg_key>q</arg_key><arg_value>x</arg_value></tool_call>"
        "after</think>Answer"
    )
    parser = TemplateParseParser(
        {
            "template_path": str(XML_TEMPLATE),
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

    assert message["reasoning_content"] == "before"
    assert message["content"] is None
    assert message["tool_calls"][0]["function"]["name"] == "lookup"
    assert message["tool_calls"][0]["function"]["arguments"] == "{\"q\": \"x\"}"


def test_parse_template_tool_tag_literal_non_stream() -> None:
    parser = TemplateParseParser(
        {
            "parse_thinking": True,
            "parse_tool_calls": True,
            "tool_tag": "tool_call",
        }
    )
    payload = _build_payload("Mention <tool_call>not a call</tool_call> ok")
    updated = parser.apply_response(payload, ModuleContext(
        path="/chat/completions",
        model="test-model",
        backend="test-backend",
        is_stream=False,
    ))
    message = updated["choices"][0]["message"]

    assert "tool_calls" not in message
    assert message["content"] == "Mention <tool_call>not a call</tool_call> ok"


def test_parse_template_tool_buffer_limit_stream() -> None:
    parser = TemplateParseParser(
        {
            "parse_thinking": False,
            "parse_tool_calls": True,
            "tool_tag": "tool_call",
            "tool_buffer_limit": 5,
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
                    "content": "<tool_call>abcdef"
                },
            }
        ]
    }
    updated = parser.apply_stream_event(event, state, ctx)
    delta = updated["choices"][0]["delta"]
    assert delta.get("content") == "<tool_call>abcdef"
    assert "tool_calls" not in delta


def test_parse_template_autodetect_think_tag(tmp_path: Path) -> None:
    template_path = tmp_path / "custom_template.jinja"
    template_path.write_text("{{ '<analysis>' + reasoning_content + '</analysis>' }}", encoding="utf-8")

    parser = TemplateParseParser(
        {
            "template_path": str(template_path),
            "parse_thinking": True,
            "parse_tool_calls": False,
        }
    )
    payload = _build_payload("<analysis>Reason</analysis>Answer")
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

    assert message["reasoning_content"] == "Reason"
    assert message["content"] == "Answer"


def test_parse_template_qwen_start_in_think_stream() -> None:
    parser = TemplateParseParser(
        {
            "template_path": str(QWEN_TEMPLATE),
            "parse_thinking": True,
            "parse_tool_calls": True,
            "start_in_think": True,
        }
    )
    state = parser.create_stream_state()
    ctx = ModuleContext(
        path="/chat/completions",
        model="test-model",
        backend="test-backend",
        is_stream=True,
    )

    events = [
        {"choices": [{"index": 0, "delta": {"content": "Need to inspect files"}}]},
        {
            "choices": [
                {
                    "index": 0,
                    "delta": {
                        "content": (
                            "</think>\n\n<tool_call>\nshow_files"
                            "\n<arg_key>path</arg_key>\n<arg_value>/tmp</arg_value>"
                        )
                    },
                }
            ]
        },
        {"choices": [{"index": 0, "delta": {"content": "\n</tool_call>"}}]},
    ]

    parsed_events: list[dict] = []
    for event in events:
        updated = parser.apply_stream_event(event, state, ctx)
        parsed_events.extend(updated if isinstance(updated, list) else [updated])
    parsed_events.extend(parser.finalize_stream(state, ctx))

    reasoning_parts: list[str] = []
    content_parts: list[str] = []
    tool_calls: list[dict] = []
    finish_reasons: list[str | None] = []
    for event in parsed_events:
        choice = event["choices"][0]
        finish_reasons.append(choice.get("finish_reason"))
        delta = choice.get("delta", {})
        reasoning = delta.get("reasoning_content")
        if isinstance(reasoning, str):
            reasoning_parts.append(reasoning)
        content = delta.get("content")
        if isinstance(content, str):
            content_parts.append(content)
        delta_tool_calls = delta.get("tool_calls")
        if isinstance(delta_tool_calls, list):
            tool_calls.extend(delta_tool_calls)

    assert "".join(reasoning_parts) == "Need to inspect files"
    assert "".join(content_parts).strip() == ""
    assert tool_calls[0]["function"]["name"] == "show_files"
    assert tool_calls[0]["function"]["arguments"] == "{\"path\": \"/tmp\"}"
    assert "tool_calls" in finish_reasons


def test_parse_template_qwen_legacy_function_equals_non_stream() -> None:
    parser = TemplateParseParser(
        {
            "template_path": str(QWEN_TEMPLATE),
            "parse_thinking": True,
            "parse_tool_calls": True,
        }
    )
    payload = _build_payload(
        "<think>Need a simple tool.</think>\n\n<tool_call>\n<function=show_files>\n</function>\n</tool_call>"
    )
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
    assert message["reasoning_content"] == "Need a simple tool."
    assert message["content"] is None
    assert message["tool_calls"][0]["function"]["name"] == "show_files"
    assert message["tool_calls"][0]["function"]["arguments"] == "{}"
    assert updated["choices"][0]["finish_reason"] == "tool_calls"


def test_parse_template_qwen_parameter_tags_non_stream() -> None:
    parser = TemplateParseParser(
        {
            "template_path": str(QWEN_TEMPLATE),
            "parse_thinking": True,
            "parse_tool_calls": True,
        }
    )
    payload = _build_payload(
        "<think>Need template fields.</think>\n\n"
        "<tool_call>\n"
        "<function=get_template_keys>\n"
        "<parameter=template_name>\n"
        "Рапорт об обнаружении признаков преступления.docx\n"
        "</parameter>\n"
        "</function>\n"
        "</tool_call>"
    )
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
    assert message["reasoning_content"] == "Need template fields."
    assert message["content"] is None
    assert message["tool_calls"][0]["function"]["name"] == "get_template_keys"
    assert (
        message["tool_calls"][0]["function"]["arguments"]
        == '{"template_name": "Рапорт об обнаружении признаков преступления.docx"}'
    )
    assert updated["choices"][0]["finish_reason"] == "tool_calls"


def test_parse_template_qwen_non_stream_preserves_plain_json_without_closing_think() -> None:
    parser = TemplateParseParser(
        {
            "template_path": str(QWEN_TEMPLATE),
            "parse_thinking": True,
            "parse_tool_calls": True,
            "start_in_think": True,
        }
    )
    payload = _build_payload('{"entities": [], "relationships": []}')
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
    assert message["content"] == '{"entities": [], "relationships": []}'
    assert "reasoning_content" not in message


def test_parse_template_qwen_legacy_function_equals_stream() -> None:
    parser = TemplateParseParser(
        {
            "template_path": str(QWEN_TEMPLATE),
            "parse_thinking": True,
            "parse_tool_calls": True,
            "start_in_think": True,
        }
    )
    state = parser.create_stream_state()
    ctx = ModuleContext(
        path="/chat/completions",
        model="test-model",
        backend="test-backend",
        is_stream=True,
    )

    events = [
        {"choices": [{"index": 0, "delta": {"content": "Need a simple tool."}}]},
        {
            "choices": [
                {
                    "index": 0,
                    "delta": {"content": "</think>\n\n<tool_call>\n<function=show_files>\n"},
                }
            ]
        },
        {"choices": [{"index": 0, "delta": {"content": "</function>\n</tool_call>"}}]},
    ]

    parsed_events: list[dict] = []
    for event in events:
        updated = parser.apply_stream_event(event, state, ctx)
        parsed_events.extend(updated if isinstance(updated, list) else [updated])
    parsed_events.extend(parser.finalize_stream(state, ctx))

    reasoning_parts: list[str] = []
    content_parts: list[str] = []
    tool_calls: list[dict] = []
    finish_reasons: list[str | None] = []
    for event in parsed_events:
        choice = event["choices"][0]
        finish_reasons.append(choice.get("finish_reason"))
        delta = choice.get("delta", {})
        reasoning = delta.get("reasoning_content")
        if isinstance(reasoning, str):
            reasoning_parts.append(reasoning)
        content = delta.get("content")
        if isinstance(content, str):
            content_parts.append(content)
        delta_tool_calls = delta.get("tool_calls")
        if isinstance(delta_tool_calls, list):
            tool_calls.extend(delta_tool_calls)

    assert "".join(reasoning_parts) == "Need a simple tool."
    assert "".join(content_parts).strip() == ""
    assert tool_calls[0]["function"]["name"] == "show_files"
    assert tool_calls[0]["function"]["arguments"] == "{}"
    assert "tool_calls" in finish_reasons


def test_parse_template_qwen_parameter_tags_stream() -> None:
    parser = TemplateParseParser(
        {
            "template_path": str(QWEN_TEMPLATE),
            "parse_thinking": True,
            "parse_tool_calls": True,
            "start_in_think": True,
        }
    )
    state = parser.create_stream_state()
    ctx = ModuleContext(
        path="/chat/completions",
        model="test-model",
        backend="test-backend",
        is_stream=True,
    )

    events = [
        {"choices": [{"index": 0, "delta": {"content": "Need template fields."}}]},
        {
            "choices": [
                {
                    "index": 0,
                    "delta": {
                        "content": "</think>\n\n<tool_call>\n<function=get_template_keys>\n"
                    },
                }
            ]
        },
        {
            "choices": [
                {
                    "index": 0,
                    "delta": {
                        "content": "<parameter=template_name>\nРапорт об обнаружении признаков "
                    },
                }
            ]
        },
        {
            "choices": [
                {
                    "index": 0,
                    "delta": {"content": "преступления.docx\n</parameter>\n</function>\n</tool_call>"},
                }
            ]
        },
    ]

    parsed_events: list[dict] = []
    for event in events:
        updated = parser.apply_stream_event(event, state, ctx)
        parsed_events.extend(updated if isinstance(updated, list) else [updated])
    parsed_events.extend(parser.finalize_stream(state, ctx))

    reasoning_parts: list[str] = []
    content_parts: list[str] = []
    tool_calls: list[dict] = []
    finish_reasons: list[str | None] = []
    for event in parsed_events:
        choice = event["choices"][0]
        finish_reasons.append(choice.get("finish_reason"))
        delta = choice.get("delta", {})
        reasoning = delta.get("reasoning_content")
        if isinstance(reasoning, str):
            reasoning_parts.append(reasoning)
        content = delta.get("content")
        if isinstance(content, str):
            content_parts.append(content)
        delta_tool_calls = delta.get("tool_calls")
        if isinstance(delta_tool_calls, list):
            tool_calls.extend(delta_tool_calls)

    assert "".join(reasoning_parts) == "Need template fields."
    assert "".join(content_parts).strip() == ""
    assert tool_calls[0]["function"]["name"] == "get_template_keys"
    assert (
        tool_calls[0]["function"]["arguments"]
        == '{"template_name": "Рапорт об обнаружении признаков преступления.docx"}'
    )
    assert "tool_calls" in finish_reasons


def test_qwen_template_renders_repo_xml_tool_calls() -> None:
    rendered = _render_chat_template(
        QWEN_TEMPLATE,
        [
            {"role": "user", "content": "List files."},
            {
                "role": "assistant",
                "content": "",
                "tool_calls": [
                    {
                        "function": {
                            "name": "show_files",
                            "arguments": {"path": "/tmp"},
                        }
                    }
                ],
            },
        ],
    )

    assert "<tool_call>\n<function=show_files>\n" in rendered
    assert "<parameter=path>\n/tmp\n</parameter>" in rendered
    assert "<arg_key>" not in rendered
    assert "<arg_value>" not in rendered
