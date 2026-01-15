"""Utilities for generating unparsed assistant messages from templates."""

from __future__ import annotations

import json
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Mapping, Optional

from ..parsers import response_pipeline as rp


@dataclass(frozen=True)
class TemplateMarkers:
    think_tag: str
    tool_format: str
    tool_tag: str
    k2_call_open: str
    k2_call_close: str
    k2_arg_open: str
    k2_section_open: str
    k2_section_close: str


def detect_template_markers(template_path: Optional[str]) -> TemplateMarkers:
    think_tag = "think"
    tool_format = "xml"
    tool_tag = "tool_call"
    text: Optional[str] = None

    if template_path:
        path = Path(template_path)
        try:
            text = path.read_text(encoding="utf-8")
        except FileNotFoundError:
            text = None
        except Exception:
            text = None

    if text:
        detected_format = rp._detect_tool_call_format(text)
        if detected_format:
            tool_format = detected_format
        detected_tag = rp._detect_tool_call_tag(text)
        if detected_tag:
            tool_tag = detected_tag
        detected_think = rp._detect_think_tag(text)
        if detected_think:
            think_tag = detected_think

    return TemplateMarkers(
        think_tag=think_tag,
        tool_format=tool_format,
        tool_tag=tool_tag,
        k2_call_open=rp.K2_TOOL_MARKERS["call_open"],
        k2_call_close=rp.K2_TOOL_MARKERS["call_close"],
        k2_arg_open=rp.K2_TOOL_MARKERS["arg_open"],
        k2_section_open=rp.K2_TOOL_MARKERS["section_open"],
        k2_section_close=rp.K2_TOOL_MARKERS["section_close"],
    )


def _coerce_args(value: Any) -> Any:
    if isinstance(value, str):
        try:
            return json.loads(value)
        except json.JSONDecodeError:
            return value
    return value


def _tool_name_and_args(tool_call: Mapping[str, Any]) -> tuple[str, Any]:
    name = ""
    args: Any = {}
    if isinstance(tool_call.get("function"), Mapping):
        func = tool_call["function"]
        name = str(func.get("name") or "") if func.get("name") is not None else ""
        args = _coerce_args(func.get("arguments"))
    else:
        name = str(tool_call.get("name") or "") if tool_call.get("name") is not None else ""
        args = _coerce_args(tool_call.get("arguments"))
    return name, args


def _render_xml_tool_calls(tool_calls: list[Mapping[str, Any]], tag: str) -> str:
    rendered: list[str] = []
    for tool_call in tool_calls:
        name, args = _tool_name_and_args(tool_call)
        if not name:
            continue
        if not isinstance(args, Mapping):
            if args is None:
                args = {}
            else:
                args = {"arguments": args}
        pieces = [f"<{tag}>{name}"]
        for key, value in args.items():
            if isinstance(value, str):
                arg_value = value
            else:
                arg_value = json.dumps(value, ensure_ascii=False)
            pieces.append(f"<arg_key>{key}</arg_key><arg_value>{arg_value}</arg_value>")
        pieces.append(f"</{tag}>")
        rendered.append("".join(pieces))
    return "".join(rendered)


def _render_k2_tool_calls(
    tool_calls: list[Mapping[str, Any]], markers: TemplateMarkers
) -> str:
    rendered: list[str] = [markers.k2_section_open]
    for tool_call in tool_calls:
        name, args = _tool_name_and_args(tool_call)
        if not name:
            continue
        if isinstance(args, (dict, list)):
            args_text = json.dumps(args, ensure_ascii=False)
        elif args is None:
            args_text = ""
        else:
            args_text = str(args)
        rendered.append(
            f"{markers.k2_call_open}{name}{markers.k2_arg_open}{args_text}{markers.k2_call_close}"
        )
    rendered.append(markers.k2_section_close)
    return "".join(rendered)


def render_unparsed_content(
    message: Mapping[str, Any],
    *,
    template_path: Optional[str],
    include_reasoning: bool,
    include_tool_calls: bool,
) -> str:
    markers = detect_template_markers(template_path)
    parts: list[str] = []
    reasoning = message.get("reasoning_content")
    if include_reasoning and isinstance(reasoning, str) and reasoning:
        parts.append(f"<{markers.think_tag}>{reasoning}</{markers.think_tag}>")

    content = message.get("content")
    if isinstance(content, str) and content:
        parts.append(content)

    tool_calls = message.get("tool_calls")
    if include_tool_calls and isinstance(tool_calls, list) and tool_calls:
        if markers.tool_format == "k2":
            parts.append(_render_k2_tool_calls(tool_calls, markers))
        else:
            parts.append(_render_xml_tool_calls(tool_calls, markers.tool_tag))

    return "".join(parts)


def unparse_assistant_message(
    message: Mapping[str, Any],
    *,
    template_path: Optional[str],
    unparse_reasoning: bool,
    unparse_tool_calls: bool,
) -> dict[str, Any]:
    """Return a new assistant message with reasoning/tool calls encoded into content."""
    new_message = dict(message)
    raw_content = render_unparsed_content(
        message,
        template_path=template_path,
        include_reasoning=unparse_reasoning,
        include_tool_calls=unparse_tool_calls,
    )
    new_message["role"] = str(new_message.get("role") or "assistant")
    new_message["content"] = raw_content if raw_content else None
    if unparse_reasoning:
        new_message.pop("reasoning_content", None)
    if unparse_tool_calls:
        new_message.pop("tool_calls", None)
    return new_message


def normalize_tool_calls(tool_calls: Any) -> list[dict[str, Any]]:
    if not isinstance(tool_calls, list):
        return []
    normalized: list[dict[str, Any]] = []
    for tool_call in tool_calls:
        if not isinstance(tool_call, Mapping):
            continue
        name, args = _tool_name_and_args(tool_call)
        normalized.append({"name": name, "arguments": args})
    return normalized


def normalize_message_for_compare(message: Mapping[str, Any]) -> dict[str, Any]:
    content = message.get("content")
    if content == "":
        content = None
    reasoning = message.get("reasoning_content")
    if reasoning == "":
        reasoning = None
    tool_calls = normalize_tool_calls(message.get("tool_calls"))
    return {
        "role": message.get("role") or "assistant",
        "content": content,
        "reasoning_content": reasoning,
        "tool_calls": tool_calls or None,
    }
