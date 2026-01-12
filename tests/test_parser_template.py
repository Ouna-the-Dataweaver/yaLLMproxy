"""Template-driven tests for the reasoning swap parser."""

from __future__ import annotations

import copy
import difflib
import importlib.util
import json
import os
import sys
from dataclasses import dataclass
from pathlib import Path
from typing import Any

import pytest

jinja2 = pytest.importorskip("jinja2")

sys.path.insert(0, str(Path(__file__).parent.parent / "src"))

from src.modules.response_pipeline import ModuleContext, ReasoningSwapParser

PROJECT_ROOT = Path(__file__).parent.parent
TEMPLATE_PATH = PROJECT_ROOT / "configs" / "jinja_templates" / "template_example.jinja"
TRACE_DIR = PROJECT_ROOT / "logs" / "tests"
TRACE_ENV = "YALLMP_TRACE_TESTS"


def _load_inspector():
    module_path = PROJECT_ROOT / "scripts" / "inspect_template.py"
    spec = importlib.util.spec_from_file_location("inspect_template", module_path)
    if spec is None or spec.loader is None:
        raise RuntimeError("Failed to load inspect_template module")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def _swap_config_from_template(template_path: Path, think_tag: str = "think") -> dict[str, Any]:
    inspector = _load_inspector()
    template_text = template_path.read_text(encoding="utf-8")
    if hasattr(inspector, "extract_think_config"):
        return inspector.extract_think_config(template_text, think_tag=think_tag)
    if hasattr(inspector, "analyze_template"):
        return inspector.analyze_template(template_text, think_tag=think_tag)["config"]
    raise RuntimeError("inspect_template is missing config helpers")


def _tojson_filter(value: Any, *, ensure_ascii: bool = True, **_kwargs: Any) -> str:
    return json.dumps(value, ensure_ascii=ensure_ascii)


def _render_chat_template(template_path: Path, messages: list[dict[str, Any]]) -> str:
    env = jinja2.Environment(autoescape=False, trim_blocks=False, lstrip_blocks=False)
    env.filters["tojson"] = _tojson_filter
    template_text = template_path.read_text(encoding="utf-8")
    template = env.from_string(template_text)
    return template.render(
        messages=messages,
        tools=[],
        add_generation_prompt=False,
    )


def _trace_enabled() -> bool:
    value = os.getenv(TRACE_ENV, "1").strip().lower()
    return value not in {"0", "false", "no", "off"}


def _slugify(value: str) -> str:
    return "".join(ch if ch.isalnum() or ch in {"-", "_"} else "_" for ch in value)


def _format_json(value: Any) -> str:
    return json.dumps(value, ensure_ascii=True, indent=2, sort_keys=True)


def _write_trace(
    *,
    case: TemplateCase,
    config: dict[str, Any],
    events: list[dict[str, Any]],
    parsed_events: list[dict[str, Any]],
    original_message: dict[str, Any],
    parsed_message: dict[str, Any],
    rendered_original: str,
    rendered_parsed: str,
) -> None:
    if not _trace_enabled():
        return
    TRACE_DIR.mkdir(parents=True, exist_ok=True)
    trace_path = TRACE_DIR / f"parser_template_{_slugify(case.name)}.log"
    rendered_equal = rendered_original == rendered_parsed
    lines = [
        f"case={case.name}",
        f"rendered_equal={rendered_equal}",
        "-- config --",
        _format_json(config),
        "-- input_events --",
        _format_json(events),
        "-- parsed_events --",
        _format_json(parsed_events),
        "-- original_message --",
        _format_json(original_message),
        "-- parsed_message --",
        _format_json(parsed_message),
        "-- rendered_original --",
        rendered_original,
        "-- rendered_parsed --",
        rendered_parsed,
    ]
    if not rendered_equal:
        diff = difflib.unified_diff(
            rendered_original.splitlines(),
            rendered_parsed.splitlines(),
            fromfile="rendered_original",
            tofile="rendered_parsed",
            lineterm="",
        )
        lines.append("-- rendered_diff --")
        lines.extend(diff)
    trace_path.write_text("\n".join(lines) + "\n", encoding="utf-8")


def _apply_stream(
    parser: ReasoningSwapParser, events: list[dict[str, Any]]
) -> list[dict[str, Any]]:
    state = parser.create_stream_state()
    ctx = ModuleContext(
        path="/chat/completions",
        model="test-model",
        backend="test-backend",
        is_stream=True,
    )
    output: list[dict[str, Any]] = []
    for event in events:
        updated = parser.apply_stream_event(event, state, ctx)
        if isinstance(updated, list):
            output.extend(updated)
        else:
            output.append(updated)
    output.extend(parser.finalize_stream(state, ctx))
    return output


def _assemble_message(events: list[dict[str, Any]]) -> dict[str, Any]:
    message: dict[str, Any] = {"role": "assistant", "content": "", "tool_calls": []}
    for event in events:
        choices = event.get("choices")
        if not isinstance(choices, list):
            continue
        for choice in choices:
            delta = choice.get("delta")
            if not isinstance(delta, dict):
                continue
            content = delta.get("content")
            if isinstance(content, str):
                message["content"] += content
            tool_calls = delta.get("tool_calls")
            if isinstance(tool_calls, list):
                message["tool_calls"].extend(tool_calls)
    if not message["tool_calls"]:
        message.pop("tool_calls", None)
    return message


def _build_stream_events(
    reasoning: str, content: str, tool_calls: list[dict[str, Any]] | None
) -> list[dict[str, Any]]:
    events: list[dict[str, Any]] = []
    if reasoning:
        events.append(
            {"choices": [{"index": 0, "delta": {"reasoning_content": reasoning}}]}
        )
    if content:
        events.append({"choices": [{"index": 0, "delta": {"content": content}}]})
    if tool_calls:
        events.append({"choices": [{"index": 0, "delta": {"tool_calls": tool_calls}}]})
    return events


def _tool_calls() -> list[dict[str, Any]]:
    return [
        {
            "id": "call_0_0",
            "type": "function",
            "function": {
                "name": "lookup",
                "arguments": {
                    "query": "parser test",
                    "limit": 1,
                },
            },
            "index": 0,
        }
    ]


@dataclass(frozen=True)
class TemplateCase:
    name: str
    reasoning: str
    content: str
    tool_calls: list[dict[str, Any]] | None


CASES = [
    TemplateCase(
        name="reasoning_content",
        reasoning="Check reasoning path.",
        content="Final answer.",
        tool_calls=None,
    ),
    TemplateCase(
        name="reasoning_tool_call",
        reasoning="Need a tool first.",
        content="",
        tool_calls=_tool_calls(),
    ),
    TemplateCase(
        name="reasoning_content_tool_call",
        reasoning="Include reasoning and tool call.",
        content="Answer plus tool.",
        tool_calls=_tool_calls(),
    ),
]


@pytest.mark.parametrize("case", CASES, ids=[case.name for case in CASES])
def test_swap_reasoning_content_matches_template(case: TemplateCase) -> None:
    if not TEMPLATE_PATH.exists():
        pytest.skip("Template example missing")
    config = _swap_config_from_template(TEMPLATE_PATH)
    parser = ReasoningSwapParser(config)

    events = _build_stream_events(
        case.reasoning,
        case.content,
        copy.deepcopy(case.tool_calls) if case.tool_calls else None,
    )
    parsed_events = _apply_stream(parser, events)
    parsed_message = _assemble_message(parsed_events)

    user_message = {"role": "user", "content": "Run parser checks."}
    original_message = {
        "role": "assistant",
        "content": case.content,
        "reasoning_content": case.reasoning,
    }
    if case.tool_calls:
        original_message["tool_calls"] = copy.deepcopy(case.tool_calls)

    rendered_original = _render_chat_template(
        TEMPLATE_PATH, [user_message, original_message]
    )
    rendered_parsed = _render_chat_template(
        TEMPLATE_PATH, [user_message, parsed_message]
    )
    _write_trace(
        case=case,
        config=config,
        events=events,
        parsed_events=parsed_events,
        original_message=original_message,
        parsed_message=parsed_message,
        rendered_original=rendered_original,
        rendered_parsed=rendered_parsed,
    )
    assert rendered_original == rendered_parsed
