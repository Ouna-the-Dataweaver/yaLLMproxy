"""Tool-call parsers used by the response tag parser."""

from __future__ import annotations

import json
import re
from collections.abc import Callable
from dataclasses import dataclass
from typing import Any, Optional


def _split_tail_for_prefix(text: str, tag: str) -> tuple[str, str]:
    max_prefix = 0
    max_len = min(len(tag) - 1, len(text))
    for i in range(1, max_len + 1):
        if text.endswith(tag[:i]):
            max_prefix = i
    if max_prefix:
        return text[:-max_prefix], text[-max_prefix:]
    return text, ""


def _maybe_json(value: str) -> Any:
    try:
        return json.loads(value)
    except Exception:
        return value


ARG_PAIR_RE = re.compile(
    r"<arg_key>(?P<key>.*?)</arg_key>\s*<arg_value>(?P<value>.*?)</arg_value>",
    re.DOTALL,
)
PARAMETER_RE = re.compile(
    r"<parameter\s*=\s*(?P<key>[^>\s]+)\s*>(?P<value>.*?)</parameter>",
    re.DOTALL,
)
LEGACY_FUNCTION_EQUALS_RE = re.compile(
    r"^<function\s*=\s*(?P<name>[A-Za-z_][\w.-]*)\s*>(?P<body>.*?)</function>$",
    re.DOTALL,
)
LEGACY_FUNCTION_TAG_RE = re.compile(
    r"^<function>(?P<name>.*?)</function>(?P<body>.*)$",
    re.DOTALL,
)
FUNCTION_OPEN_ANYWHERE_RE = re.compile(
    r"<function\s*=\s*(?P<name>[A-Za-z_][\w.-]*)\s*>",
    re.DOTALL,
)


def _parse_xml_arg_pairs(args_text: str) -> Optional[dict[str, Any]]:
    args: dict[str, Any] = {}
    last_end = 0
    for match in ARG_PAIR_RE.finditer(args_text):
        gap = args_text[last_end:match.start()]
        if gap.strip():
            return None
        key = match.group("key").strip()
        value_text = match.group("value").strip()
        if not key:
            continue
        args[key] = _maybe_json(value_text)
        last_end = match.end()
    if args_text[last_end:].strip():
        return None
    return args


def _parse_xml_parameter_pairs(args_text: str) -> Optional[dict[str, Any]]:
    args: dict[str, Any] = {}
    last_end = 0
    for match in PARAMETER_RE.finditer(args_text):
        gap = args_text[last_end:match.start()]
        if gap.strip():
            return None
        key = match.group("key").strip()
        value_text = match.group("value").strip()
        if not key:
            continue
        args[key] = _maybe_json(value_text)
        last_end = match.end()
    if args_text[last_end:].strip():
        return None
    return args


def _parse_legacy_function_tool_call(stripped: str) -> Optional[dict[str, Any]]:
    for pattern in (LEGACY_FUNCTION_EQUALS_RE, LEGACY_FUNCTION_TAG_RE):
        match = pattern.match(stripped)
        if match is None:
            continue
        name = match.group("name").strip()
        if not name or any(ch.isspace() for ch in name):
            return None
        body = match.group("body").strip()
        if not body:
            return {"name": name, "arguments": {}}
        args = _parse_xml_arg_pairs(body)
        if args is None:
            args = _parse_xml_parameter_pairs(body)
        if args is None:
            return None
        return {"name": name, "arguments": args}
    return None


def parse_xmlish_tool_call_block(text: str) -> Optional[dict[str, Any]]:
    stripped = text.strip()
    if not stripped:
        return None
    legacy_function_call = _parse_legacy_function_tool_call(stripped)
    if legacy_function_call is not None:
        return legacy_function_call
    arg_start = stripped.find("<arg_key>")
    if arg_start == -1:
        name = stripped
        if not name or any(ch.isspace() for ch in name):
            return None
        args: dict[str, Any] = {}
    else:
        name = stripped[:arg_start].strip()
        if not name or any(ch.isspace() for ch in name):
            return None
        args_text = stripped[arg_start:]
        parsed_args = _parse_xml_arg_pairs(args_text)
        if parsed_args is None:
            return None
        args = parsed_args
    if not name:
        return None
    return {"name": name, "arguments": args}


def _parse_qwen_function_body(function_name: str, body: str) -> Optional[dict[str, Any]]:
    body = body.strip()
    while True:
        duplicate = FUNCTION_OPEN_RE.match(body)
        if duplicate is None or duplicate.group("name") != function_name:
            break
        body = body[duplicate.end() :].strip()

    if not body:
        return {"name": function_name, "arguments": {}}
    args = _parse_xml_parameter_pairs(body)
    if args is None:
        args = _parse_xml_arg_pairs(body)
    if args is None:
        return None
    return {"name": function_name, "arguments": args}


def _parse_qwen_json_tool_call(stripped: str) -> Optional[dict[str, Any]]:
    try:
        payload = json.loads(stripped)
    except json.JSONDecodeError:
        return None
    if not isinstance(payload, dict):
        return None

    name = payload.get("name")
    if not isinstance(name, str):
        function = payload.get("function")
        if isinstance(function, dict):
            name = function.get("name")
    if not isinstance(name, str):
        return None
    name = name.strip()
    if not name or any(ch.isspace() for ch in name):
        return None

    arguments = payload.get("parameters")
    if arguments is None:
        arguments = payload.get("arguments")
    if arguments is None:
        function = payload.get("function")
        if isinstance(function, dict):
            arguments = function.get("parameters")
            if arguments is None:
                arguments = function.get("arguments")
    if arguments is None:
        arguments = {}
    if isinstance(arguments, str):
        arguments = _maybe_json(arguments)
    if not isinstance(arguments, dict):
        return None
    return {"name": name, "arguments": arguments}


def parse_qwen_xml_tool_call_block(text: str) -> Optional[dict[str, Any]]:
    stripped = text.strip()
    json_call = _parse_qwen_json_tool_call(stripped)
    if json_call is not None:
        return json_call

    parsed = parse_xmlish_tool_call_block(text)
    if parsed is not None:
        return parsed

    match = FUNCTION_OPEN_ANYWHERE_RE.search(stripped)
    if match is None:
        return None
    name = match.group("name").strip()
    if not name or any(ch.isspace() for ch in name):
        return None

    body_start = match.end()
    body_end = stripped.rfind("</function>")
    if body_end == -1 or body_end < body_start:
        body_end = len(stripped)
    return _parse_qwen_function_body(name, stripped[body_start:body_end])


def k2_tool_call_parser_factory(
    arg_open: str,
) -> Callable[[str], Optional[dict[str, Any]]]:
    def _parse(text: str) -> Optional[dict[str, Any]]:
        stripped = text.strip()
        if not stripped:
            return None
        if arg_open in stripped:
            name_part, args_part = stripped.split(arg_open, 1)
        else:
            name_part, args_part = stripped, ""
        name = name_part.strip()
        if not name or any(ch.isspace() for ch in name):
            return None
        args_text = args_part.strip()
        arguments: Any = {}
        if args_text:
            arguments = _maybe_json(args_text)
        return {"name": name, "arguments": arguments}

    return _parse


@dataclass
class XmlToolCallStreamState:
    phase: str = "await_function"
    buffer: str = ""
    name: Optional[str] = None
    first_parameter: bool = True
    param_tail: str = ""
    param_value_started: bool = False
    param_value_mode: Optional[str] = None
    disabled: bool = False
    emitted: bool = False


FUNCTION_OPEN_RE = re.compile(
    r"^\s*<function\s*=\s*(?P<name>[A-Za-z_][\w.-]*)\s*>",
    re.DOTALL,
)
PARAMETER_OPEN_RE = re.compile(
    r"^\s*<parameter\s*=\s*(?P<key>[^>\s]+)\s*>",
    re.DOTALL,
)


def _json_string_fragment(value: str) -> str:
    encoded = json.dumps(value, ensure_ascii=False)
    return encoded[1:-1]


class XmlToolCallStreamer:
    """Incrementally converts Qwen XML tool calls to OpenAI argument deltas."""

    def __init__(self) -> None:
        self.state = XmlToolCallStreamState()

    def feed(self, text: str) -> list[dict[str, Any]]:
        if not text or self.state.disabled:
            return []
        self.state.buffer += text
        chunks: list[dict[str, Any]] = []

        while self.state.buffer and not self.state.disabled:
            if self.state.phase == "await_function":
                match = FUNCTION_OPEN_RE.match(self.state.buffer)
                if match is None:
                    if "<function" in self.state.buffer and ">" not in self.state.buffer:
                        break
                    if self.state.buffer.strip() and not "<function".startswith(self.state.buffer.strip()):
                        self.state.disabled = True
                    break
                self.state.name = match.group("name")
                chunks.append({"name": self.state.name, "arguments": "{"})
                self.state.emitted = True
                self.state.buffer = self.state.buffer[match.end():]
                self.state.phase = "between_parameters"
                continue

            if self.state.phase == "between_parameters":
                stripped = self.state.buffer.lstrip()
                if not stripped:
                    self.state.buffer = ""
                    break
                if stripped.startswith("</function>"):
                    self.state.buffer = stripped[len("</function>"):]
                    chunks.append({"arguments": "}", "finished": True})
                    self.state.phase = "done"
                    continue
                if not stripped.startswith("<parameter"):
                    if "<parameter".startswith(stripped) or "</function>".startswith(stripped):
                        self.state.buffer = stripped
                        break
                    self.state.disabled = True
                    break
                match = PARAMETER_OPEN_RE.match(self.state.buffer)
                if match is None:
                    if ">" not in self.state.buffer:
                        break
                    self.state.disabled = True
                    break
                key = match.group("key").strip()
                prefix = "" if self.state.first_parameter else ","
                chunks.append({"arguments": f"{prefix}{json.dumps(key, ensure_ascii=False)}:"})
                self.state.first_parameter = False
                self.state.buffer = self.state.buffer[match.end():]
                self.state.param_tail = ""
                self.state.param_value_started = False
                self.state.param_value_mode = None
                self.state.phase = "parameter_value"
                continue

            if self.state.phase == "parameter_value":
                close = "</parameter>"
                close_idx = self.state.buffer.find(close)
                if close_idx == -1:
                    head, tail = _split_tail_for_prefix(self.state.buffer, close)
                    self._append_parameter_text(head, chunks)
                    self.state.buffer = tail
                    break
                self._append_parameter_text(self.state.buffer[:close_idx], chunks)
                final_value = self.state.param_tail.rstrip()
                if self.state.param_value_mode == "json":
                    value_text = final_value.strip()
                    try:
                        chunks.append({"arguments": json.dumps(json.loads(value_text), ensure_ascii=False)})
                    except (TypeError, ValueError, json.JSONDecodeError):
                        chunks.append({"arguments": '"' + _json_string_fragment(value_text) + '"'})
                elif not self.state.param_value_started:
                    chunks.append({"arguments": '"'})
                    chunks.append({"arguments": _json_string_fragment(final_value) + '"'})
                else:
                    chunks.append({"arguments": _json_string_fragment(final_value) + '"'})
                self.state.param_tail = ""
                self.state.buffer = self.state.buffer[close_idx + len(close):]
                self.state.phase = "between_parameters"
                continue

            if self.state.phase == "done":
                self.state.buffer = ""
                break

        return chunks

    def _append_parameter_text(self, text: str, chunks: list[dict[str, Any]]) -> None:
        if not text:
            return
        if self.state.param_value_mode == "json":
            self.state.param_tail += text
            return
        if not self.state.param_value_started:
            text = self.state.param_tail + text
            self.state.param_tail = ""
            stripped = text.lstrip()
            if not stripped:
                self.state.param_tail = text
                return
            if stripped[0] in "{[":
                self.state.param_value_mode = "json"
                self.state.param_value_started = True
                self.state.param_tail = text
                return
            text = stripped
            chunks.append({"arguments": '"'})
            self.state.param_value_started = True
        combined = self.state.param_tail + text
        if len(combined) <= 32:
            self.state.param_tail = combined
            return
        emit_text = combined[:-32]
        self.state.param_tail = combined[-32:]
        chunks.append({"arguments": _json_string_fragment(emit_text)})

    def emitted(self) -> bool:
        return self.state.emitted

    def disabled(self) -> bool:
        return self.state.disabled


class QwenXmlToolCallStreamer(XmlToolCallStreamer):
    """Qwen XML streamer with duplicate-function repair before first emission."""

    def feed(self, text: str) -> list[dict[str, Any]]:
        if not text or self.state.disabled:
            return []
        self.state.buffer += text
        chunks: list[dict[str, Any]] = []

        while self.state.buffer and not self.state.disabled:
            if self.state.phase == "await_function":
                match = FUNCTION_OPEN_RE.match(self.state.buffer)
                if match is None:
                    if "<function" in self.state.buffer and ">" not in self.state.buffer:
                        break
                    if self.state.buffer.strip() and not "<function".startswith(
                        self.state.buffer.strip()
                    ):
                        self.state.disabled = True
                    break
                self.state.name = match.group("name")
                self.state.buffer = self.state.buffer[match.end() :]
                self.state.phase = "between_parameters"
                continue

            if self.state.phase == "between_parameters":
                stripped = self.state.buffer.lstrip()
                if not stripped:
                    self.state.buffer = ""
                    break
                duplicate = FUNCTION_OPEN_RE.match(stripped)
                if duplicate is not None:
                    if duplicate.group("name") != self.state.name:
                        self.state.disabled = True
                        break
                    self.state.buffer = stripped[duplicate.end() :]
                    continue
                if stripped.startswith("</function>"):
                    self.state.buffer = stripped[len("</function>") :]
                    if not self.state.emitted and self.state.name:
                        chunks.append(
                            {
                                "name": self.state.name,
                                "arguments": "{}",
                                "finished": True,
                            }
                        )
                        self.state.emitted = True
                    elif self.state.emitted:
                        chunks.append({"arguments": "}", "finished": True})
                    self.state.phase = "done"
                    continue
                if not stripped.startswith("<parameter"):
                    if "<parameter".startswith(stripped) or "</function>".startswith(
                        stripped
                    ):
                        self.state.buffer = stripped
                        break
                    self.state.disabled = True
                    break
                match = PARAMETER_OPEN_RE.match(self.state.buffer)
                if match is None:
                    if ">" not in self.state.buffer:
                        break
                    self.state.disabled = True
                    break
                if not self.state.emitted and self.state.name:
                    chunks.append({"name": self.state.name, "arguments": "{"})
                    self.state.emitted = True
                key = match.group("key").strip()
                prefix = "" if self.state.first_parameter else ","
                chunks.append(
                    {"arguments": f"{prefix}{json.dumps(key, ensure_ascii=False)}:"}
                )
                self.state.first_parameter = False
                self.state.buffer = self.state.buffer[match.end() :]
                self.state.param_tail = ""
                self.state.param_value_started = False
                self.state.param_value_mode = None
                self.state.phase = "parameter_value"
                continue

            if self.state.phase == "parameter_value":
                close = "</parameter>"
                close_idx = self.state.buffer.find(close)
                if close_idx == -1:
                    head, tail = _split_tail_for_prefix(self.state.buffer, close)
                    self._append_parameter_text(head, chunks)
                    self.state.buffer = tail
                    break
                self._append_parameter_text(self.state.buffer[:close_idx], chunks)
                final_value = self.state.param_tail.rstrip()
                if self.state.param_value_mode == "json":
                    value_text = final_value.strip()
                    try:
                        chunks.append(
                            {
                                "arguments": json.dumps(
                                    json.loads(value_text), ensure_ascii=False
                                )
                            }
                        )
                    except (TypeError, ValueError, json.JSONDecodeError):
                        chunks.append(
                            {"arguments": '"' + _json_string_fragment(value_text) + '"'}
                        )
                elif not self.state.param_value_started:
                    chunks.append({"arguments": '"'})
                    chunks.append({"arguments": _json_string_fragment(final_value) + '"'})
                else:
                    chunks.append({"arguments": _json_string_fragment(final_value) + '"'})
                self.state.param_tail = ""
                self.state.buffer = self.state.buffer[close_idx + len(close) :]
                self.state.phase = "between_parameters"
                continue

            if self.state.phase == "done":
                self.state.buffer = ""
                break

        return chunks


TOOL_CALL_PARSERS: dict[str, Callable[[str], Optional[dict[str, Any]]]] = {
    "xml": parse_xmlish_tool_call_block,
    "xmlish": parse_xmlish_tool_call_block,
    "legacy_xml": parse_xmlish_tool_call_block,
    "qwen_xml": parse_qwen_xml_tool_call_block,
}

TOOL_CALL_STREAMERS: dict[str, type[XmlToolCallStreamer]] = {
    "xml": XmlToolCallStreamer,
    "xmlish": XmlToolCallStreamer,
    "legacy_xml": XmlToolCallStreamer,
    "qwen_xml": QwenXmlToolCallStreamer,
}


def get_tool_call_parser(name: str) -> Callable[[str], Optional[dict[str, Any]]]:
    return TOOL_CALL_PARSERS.get(name, parse_xmlish_tool_call_block)


def get_tool_call_streamer(name: str) -> Optional[type[XmlToolCallStreamer]]:
    return TOOL_CALL_STREAMERS.get(name)
