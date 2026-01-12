"""Response module pipeline for transforming backend responses before returning to clients."""

from __future__ import annotations

import json
import logging
import re
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any, Callable, Iterable, Mapping, Optional


logger = logging.getLogger("yallmp-proxy")


@dataclass(frozen=True)
class ParserContext:
    path: str
    model: str
    backend: str
    is_stream: bool


ModuleContext = ParserContext


def _parse_bool(value: Any) -> bool:
    if isinstance(value, str):
        return value.strip().lower() in {"1", "true", "yes", "on"}
    return bool(value)


def _ensure_list(value: Any) -> list[str]:
    if value is None:
        return []
    if isinstance(value, (list, tuple)):
        return [str(item) for item in value if item is not None]
    return [str(value)]


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


def _parse_tool_call_block(text: str) -> Optional[dict[str, Any]]:
    stripped = text.strip()
    if not stripped:
        return None
    arg_start = stripped.find("<arg_key>")
    if arg_start == -1:
        name = stripped.split()[0] if stripped else ""
        args: dict[str, Any] = {}
    else:
        name = stripped[:arg_start].strip()
        args_text = stripped[arg_start:]
        args = {}
        for match in ARG_PAIR_RE.finditer(args_text):
            key = match.group("key").strip()
            value_text = match.group("value").strip()
            if not key:
                continue
            args[key] = _maybe_json(value_text)
    if not name:
        return None
    return {"name": name, "arguments": args}


K2_TOOL_MARKERS = {
    "call_open": "<|tool_call_begin|>",
    "arg_open": "<|tool_call_argument_begin|>",
    "call_close": "<|tool_call_end|>",
    "section_open": "<|tool_calls_section_begin|>",
    "section_close": "<|tool_calls_section_end|>",
}


def _detect_tool_call_format(template_text: str) -> Optional[str]:
    if "<|tool_call_begin|>" in template_text and "<|tool_call_end|>" in template_text:
        return "k2"
    if "<tool_call>" in template_text and "</tool_call>" in template_text:
        return "xml"
    return None


def _detect_tool_call_tag(template_text: str) -> Optional[str]:
    for tag in ("tool_call", "function_call", "tool"):
        if f"<{tag}>" in template_text and f"</{tag}>" in template_text:
            return tag
    return None


def _k2_tool_call_parser_factory(
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
        if not name:
            return None
        args_text = args_part.strip()
        arguments: Any = {}
        if args_text:
            arguments = _maybe_json(args_text)
        return {"name": name, "arguments": arguments}

    return _parse


@dataclass
class TagScanResult:
    content: str = ""
    reasoning: str = ""
    tool_calls: list[dict[str, Any]] = field(default_factory=list)


class TagScanner:
    """Incremental tag scanner for <think> and <tool_call> blocks."""

    def __init__(
        self,
        *,
        think_tag: str = "think",
        tool_tag: str = "tool_call",
        parse_thinking: bool = True,
        parse_tool_calls: bool = True,
        tool_open: Optional[str] = None,
        tool_close: Optional[str] = None,
        tool_parser: Optional[Callable[[str], Optional[dict[str, Any]]]] = None,
        drop_tags: Optional[Iterable[str]] = None,
    ) -> None:
        self.parse_thinking = parse_thinking
        self.parse_tool_calls = parse_tool_calls
        self.think_open = f"<{think_tag}>"
        self.think_close = f"</{think_tag}>"
        self.tool_open = tool_open or f"<{tool_tag}>"
        self.tool_close = tool_close or f"</{tool_tag}>"
        self.tool_parser = tool_parser or _parse_tool_call_block
        self.drop_tags = [tag for tag in (drop_tags or []) if tag]
        self._open_tags = [
            tag
            for tag, enabled in [
                (self.think_open, self.parse_thinking),
                (self.tool_open, self.parse_tool_calls),
            ]
            if enabled
        ]
        if self.drop_tags:
            self._open_tags.extend(self.drop_tags)
        self.mode = "text"
        self.buffer = ""
        self.tool_buffer = ""

    def feed(self, text: str) -> TagScanResult:
        if not text:
            return TagScanResult()
        self.buffer += text
        out_content: list[str] = []
        out_reasoning: list[str] = []
        out_tool_calls: list[dict[str, Any]] = []

        while self.buffer:
            if self.mode == "text":
                idx = self.buffer.find("<")
                if idx == -1:
                    out_content.append(self.buffer)
                    self.buffer = ""
                    break
                if idx > 0:
                    out_content.append(self.buffer[:idx])
                    self.buffer = self.buffer[idx:]
                if self.parse_thinking and self.buffer.startswith(self.think_open):
                    self.buffer = self.buffer[len(self.think_open):]
                    self.mode = "think"
                    continue
                if self.parse_tool_calls and self.buffer.startswith(self.tool_open):
                    self.buffer = self.buffer[len(self.tool_open):]
                    self.tool_buffer = ""
                    self.mode = "tool"
                    continue
                if self.drop_tags:
                    matched_drop = False
                    for drop_tag in self.drop_tags:
                        if self.buffer.startswith(drop_tag):
                            self.buffer = self.buffer[len(drop_tag):]
                            matched_drop = True
                            break
                    if matched_drop:
                        continue
                if self._open_tags and any(tag.startswith(self.buffer) for tag in self._open_tags):
                    break
                out_content.append(self.buffer[0])
                self.buffer = self.buffer[1:]
                continue

            if self.mode == "think":
                idx = self.buffer.find(self.think_close)
                if idx == -1:
                    head, tail = _split_tail_for_prefix(self.buffer, self.think_close)
                    if head:
                        out_reasoning.append(head)
                    self.buffer = tail
                    break
                out_reasoning.append(self.buffer[:idx])
                self.buffer = self.buffer[idx + len(self.think_close):]
                self.mode = "text"
                continue

            if self.mode == "tool":
                idx = self.buffer.find(self.tool_close)
                if idx == -1:
                    head, tail = _split_tail_for_prefix(self.buffer, self.tool_close)
                    if head:
                        self.tool_buffer += head
                    self.buffer = tail
                    break
                self.tool_buffer += self.buffer[:idx]
                self.buffer = self.buffer[idx + len(self.tool_close):]
                parsed = self.tool_parser(self.tool_buffer)
                if parsed:
                    out_tool_calls.append(parsed)
                else:
                    out_content.append(self.tool_open + self.tool_buffer + self.tool_close)
                self.tool_buffer = ""
                self.mode = "text"
                continue

        return TagScanResult(
            content="".join(out_content),
            reasoning="".join(out_reasoning),
            tool_calls=out_tool_calls,
        )

    def flush(self) -> TagScanResult:
        out = TagScanResult()
        if self.mode == "text":
            out.content = self.buffer
        elif self.mode == "think":
            out.reasoning = self.buffer
        elif self.mode == "tool":
            out.content = f"{self.tool_open}{self.tool_buffer}{self.buffer}"
        self.buffer = ""
        self.tool_buffer = ""
        self.mode = "text"
        return out


def _extract_think_block(text: str, think_tag: str) -> tuple[Optional[str], str]:
    open_tag = f"<{think_tag}>"
    close_tag = f"</{think_tag}>"
    start = text.find(open_tag)
    if start == -1:
        return None, text
    end = text.find(close_tag, start + len(open_tag))
    if end == -1:
        return None, text
    reasoning = text[start + len(open_tag):end]
    content = text[:start] + text[end + len(close_tag):]
    return reasoning, content


def _extract_tool_calls(text: str, tool_tag: str) -> tuple[list[dict[str, Any]], str]:
    open_tag = f"<{tool_tag}>"
    close_tag = f"</{tool_tag}>"
    pattern = re.compile(
        re.escape(open_tag) + r"(.*?)" + re.escape(close_tag), re.DOTALL
    )
    tool_calls: list[dict[str, Any]] = []

    def _replace(match: re.Match[str]) -> str:
        parsed = _parse_tool_call_block(match.group(1))
        if parsed:
            tool_calls.append(parsed)
        return ""

    content = pattern.sub(_replace, text)
    return tool_calls, content


def _build_tool_call(
    parsed: Mapping[str, Any],
    *,
    index: int,
    id_prefix: str,
) -> dict[str, Any]:
    arguments = parsed.get("arguments") or {}
    try:
        args_json = json.dumps(arguments, ensure_ascii=False)
    except (TypeError, ValueError):
        args_json = "{}"
    return {
        "id": f"{id_prefix}{index}",
        "type": "function",
        "function": {
            "name": parsed.get("name"),
            "arguments": args_json,
        },
        "index": index,
    }


class ResponseParser:
    name = "base"

    def apply_response(self, payload: dict[str, Any], ctx: ParserContext) -> dict[str, Any]:
        return payload

    def create_stream_state(self) -> Any:
        return None

    def apply_stream_event(
        self, event: dict[str, Any], state: Any, ctx: ParserContext
    ) -> dict[str, Any] | list[dict[str, Any]]:
        return event

    def finalize_stream(self, state: Any, ctx: ParserContext) -> list[dict[str, Any]]:
        return []


ResponseModule = ResponseParser


@dataclass
class ChoiceTagState:
    scanner: TagScanner
    next_tool_index: int = 0
    saw_tool_calls: bool = False


@dataclass
class ParseTagsStreamState:
    choices: dict[int, ChoiceTagState] = field(default_factory=dict)


class ParseTagsParser(ResponseParser):
    name = "parse_unparsed"

    def __init__(self, config: Mapping[str, Any]) -> None:
        self.parse_thinking = _parse_bool(config.get("parse_thinking", True))
        self.parse_tool_calls = _parse_bool(config.get("parse_tool_calls", True))
        self.think_tag = str(config.get("think_tag") or "think")
        self.tool_tag = str(config.get("tool_tag") or "tool_call")

    def apply_response(self, payload: dict[str, Any], ctx: ParserContext) -> dict[str, Any]:
        choices = payload.get("choices")
        if not isinstance(choices, list):
            return payload
        for choice in choices:
            message = choice.get("message")
            if not isinstance(message, dict):
                continue
            if message.get("role") not in {None, "assistant"}:
                continue
            content = message.get("content")
            if not isinstance(content, str) or not content:
                continue

            if self.parse_thinking and not message.get("reasoning_content"):
                reasoning, content = _extract_think_block(content, self.think_tag)
                if reasoning is not None:
                    message["reasoning_content"] = reasoning

            tool_calls: list[dict[str, Any]] = []
            if self.parse_tool_calls and not message.get("tool_calls"):
                tool_calls, content = _extract_tool_calls(content, self.tool_tag)

            if tool_calls:
                id_prefix = f"call_{choice.get('index', 0)}_"
                tool_payload = [
                    _build_tool_call(parsed, index=i, id_prefix=id_prefix)
                    for i, parsed in enumerate(tool_calls)
                ]
                message["tool_calls"] = tool_payload
                finish_reason = choice.get("finish_reason")
                if finish_reason in {None, "stop"}:
                    choice["finish_reason"] = "tool_calls"

            if isinstance(content, str):
                if content.strip():
                    message["content"] = content
                else:
                    message["content"] = None

        return payload

    def create_stream_state(self) -> ParseTagsStreamState:
        return ParseTagsStreamState()

    def _get_choice_state(self, state: ParseTagsStreamState, choice_index: int) -> ChoiceTagState:
        choice_state = state.choices.get(choice_index)
        if choice_state is None:
            scanner = TagScanner(
                think_tag=self.think_tag,
                tool_tag=self.tool_tag,
                parse_thinking=self.parse_thinking,
                parse_tool_calls=self.parse_tool_calls,
            )
            choice_state = ChoiceTagState(scanner=scanner)
            state.choices[choice_index] = choice_state
        return choice_state

    def apply_stream_event(
        self, event: dict[str, Any], state: ParseTagsStreamState, ctx: ParserContext
    ) -> dict[str, Any]:
        choices = event.get("choices")
        if not isinstance(choices, list):
            return event

        for choice in choices:
            delta = choice.get("delta")
            if not isinstance(delta, dict):
                continue
            content = delta.get("content")
            if not isinstance(content, str) or not content:
                continue

            choice_index = int(choice.get("index", 0))
            choice_state = self._get_choice_state(state, choice_index)
            result = choice_state.scanner.feed(content)

            if result.content:
                delta["content"] = result.content
            else:
                delta.pop("content", None)

            if result.reasoning:
                existing = delta.get("reasoning_content")
                if isinstance(existing, str):
                    delta["reasoning_content"] = existing + result.reasoning
                else:
                    delta["reasoning_content"] = result.reasoning

            if result.tool_calls:
                id_prefix = f"call_{choice_index}_"
                tool_payload = []
                for parsed in result.tool_calls:
                    tool_payload.append(
                        _build_tool_call(
                            parsed,
                            index=choice_state.next_tool_index,
                            id_prefix=id_prefix,
                        )
                    )
                    choice_state.next_tool_index += 1
                if tool_payload:
                    delta["tool_calls"] = tool_payload
                    choice_state.saw_tool_calls = True

            if choice_state.saw_tool_calls and choice.get("finish_reason") in {None, "stop"}:
                choice["finish_reason"] = "tool_calls"

        return event

    def finalize_stream(
        self, state: ParseTagsStreamState, ctx: ParserContext
    ) -> list[dict[str, Any]]:
        events: list[dict[str, Any]] = []
        for choice_index, choice_state in state.choices.items():
            flushed = choice_state.scanner.flush()
            if not (flushed.content or flushed.reasoning or flushed.tool_calls):
                continue
            delta: dict[str, Any] = {}
            if flushed.content:
                delta["content"] = flushed.content
            if flushed.reasoning:
                delta["reasoning_content"] = flushed.reasoning
            if flushed.tool_calls:
                id_prefix = f"call_{choice_index}_"
                delta["tool_calls"] = [
                    _build_tool_call(
                        parsed, index=i + choice_state.next_tool_index, id_prefix=id_prefix
                    )
                    for i, parsed in enumerate(flushed.tool_calls)
                ]
            events.append({"choices": [{"index": choice_index, "delta": delta}]})
        return events


@dataclass
class TemplateTagsStreamState:
    choices: dict[int, ChoiceTagState] = field(default_factory=dict)


class TemplateParseParser(ResponseParser):
    name = "parse_template"

    def __init__(self, config: Mapping[str, Any]) -> None:
        self.parse_thinking = _parse_bool(config.get("parse_thinking", True))
        self.parse_tool_calls = _parse_bool(config.get("parse_tool_calls", True))
        self.think_tag = str(config.get("think_tag") or "think")
        self.tool_tag = str(config.get("tool_tag") or "tool_call")
        self.tool_format = str(config.get("tool_format") or "auto").strip().lower()
        self.template_path = str(config.get("template_path") or "").strip()

        self.k2_call_open = str(config.get("tool_call_open") or K2_TOOL_MARKERS["call_open"])
        self.k2_call_close = str(config.get("tool_call_close") or K2_TOOL_MARKERS["call_close"])
        self.k2_arg_open = str(config.get("tool_call_arg_open") or K2_TOOL_MARKERS["arg_open"])
        self.k2_section_open = str(
            config.get("tool_call_section_open") or K2_TOOL_MARKERS["section_open"]
        )
        self.k2_section_close = str(
            config.get("tool_call_section_close") or K2_TOOL_MARKERS["section_close"]
        )

        tool_tag_explicit = "tool_tag" in config and config.get("tool_tag") is not None
        if self.template_path:
            self._load_template_overrides(tool_tag_explicit)

        if self.tool_format == "auto":
            self.tool_format = "xml"
        if self.tool_format not in {"xml", "k2"}:
            logger.warning(
                "Unknown tool_format '%s' for parse_template; defaulting to xml",
                self.tool_format,
            )
            self.tool_format = "xml"

    def _load_template_overrides(self, tool_tag_explicit: bool) -> None:
        path = Path(self.template_path)
        try:
            template_text = path.read_text(encoding="utf-8")
        except FileNotFoundError:
            logger.warning(
                "Template file not found for parse_template: %s. Using defaults.",
                self.template_path,
            )
            return
        except Exception as exc:
            logger.warning(
                "Failed to load template for parse_template: %s. Error: %s.",
                self.template_path,
                exc,
            )
            return

        detected_format = _detect_tool_call_format(template_text)
        if self.tool_format == "auto" and detected_format:
            self.tool_format = detected_format
        if self.tool_format == "xml" and not tool_tag_explicit:
            detected_tag = _detect_tool_call_tag(template_text)
            if detected_tag:
                self.tool_tag = detected_tag

    def _build_scanner(self) -> TagScanner:
        tool_open = None
        tool_close = None
        tool_parser = None
        drop_tags: list[str] = []

        if self.parse_tool_calls and self.tool_format == "k2":
            tool_open = self.k2_call_open
            tool_close = self.k2_call_close
            tool_parser = _k2_tool_call_parser_factory(self.k2_arg_open)
            drop_tags = [self.k2_section_open, self.k2_section_close]

        return TagScanner(
            think_tag=self.think_tag,
            tool_tag=self.tool_tag,
            parse_thinking=self.parse_thinking,
            parse_tool_calls=self.parse_tool_calls,
            tool_open=tool_open,
            tool_close=tool_close,
            tool_parser=tool_parser,
            drop_tags=drop_tags,
        )

    def _scan_text(self, content: str) -> TagScanResult:
        scanner = self._build_scanner()
        result = scanner.feed(content)
        flushed = scanner.flush()
        return TagScanResult(
            content=result.content + flushed.content,
            reasoning=result.reasoning + flushed.reasoning,
            tool_calls=result.tool_calls + flushed.tool_calls,
        )

    def apply_response(self, payload: dict[str, Any], ctx: ParserContext) -> dict[str, Any]:
        choices = payload.get("choices")
        if not isinstance(choices, list):
            return payload
        for choice in choices:
            message = choice.get("message")
            if not isinstance(message, dict):
                continue
            if message.get("role") not in {None, "assistant"}:
                continue
            content = message.get("content")
            if not isinstance(content, str) or not content:
                continue

            parsed = self._scan_text(content)

            if (
                self.parse_thinking
                and parsed.reasoning
                and not message.get("reasoning_content")
            ):
                message["reasoning_content"] = parsed.reasoning

            tool_calls: list[dict[str, Any]] = []
            if self.parse_tool_calls and not message.get("tool_calls"):
                tool_calls = parsed.tool_calls

            if tool_calls:
                id_prefix = f"call_{choice.get('index', 0)}_"
                tool_payload = [
                    _build_tool_call(parsed_call, index=i, id_prefix=id_prefix)
                    for i, parsed_call in enumerate(tool_calls)
                ]
                message["tool_calls"] = tool_payload
                finish_reason = choice.get("finish_reason")
                if finish_reason in {None, "stop"}:
                    choice["finish_reason"] = "tool_calls"

            if isinstance(parsed.content, str):
                if parsed.content.strip():
                    message["content"] = parsed.content
                else:
                    message["content"] = None

        return payload

    def create_stream_state(self) -> TemplateTagsStreamState:
        return TemplateTagsStreamState()

    def _get_choice_state(
        self, state: TemplateTagsStreamState, choice_index: int
    ) -> ChoiceTagState:
        choice_state = state.choices.get(choice_index)
        if choice_state is None:
            scanner = self._build_scanner()
            choice_state = ChoiceTagState(scanner=scanner)
            state.choices[choice_index] = choice_state
        return choice_state

    def apply_stream_event(
        self, event: dict[str, Any], state: TemplateTagsStreamState, ctx: ParserContext
    ) -> dict[str, Any]:
        choices = event.get("choices")
        if not isinstance(choices, list):
            return event

        for choice in choices:
            delta = choice.get("delta")
            if not isinstance(delta, dict):
                continue
            content = delta.get("content")
            if not isinstance(content, str) or not content:
                continue

            choice_index = int(choice.get("index", 0))
            choice_state = self._get_choice_state(state, choice_index)
            result = choice_state.scanner.feed(content)

            if result.content:
                delta["content"] = result.content
            else:
                delta.pop("content", None)

            if result.reasoning:
                existing = delta.get("reasoning_content")
                if isinstance(existing, str):
                    delta["reasoning_content"] = existing + result.reasoning
                else:
                    delta["reasoning_content"] = result.reasoning

            if result.tool_calls:
                id_prefix = f"call_{choice_index}_"
                tool_payload = []
                for parsed_call in result.tool_calls:
                    tool_payload.append(
                        _build_tool_call(
                            parsed_call,
                            index=choice_state.next_tool_index,
                            id_prefix=id_prefix,
                        )
                    )
                    choice_state.next_tool_index += 1
                if tool_payload:
                    delta["tool_calls"] = tool_payload
                    choice_state.saw_tool_calls = True

            if choice_state.saw_tool_calls and choice.get("finish_reason") in {None, "stop"}:
                choice["finish_reason"] = "tool_calls"

        return event

    def finalize_stream(
        self, state: TemplateTagsStreamState, ctx: ParserContext
    ) -> list[dict[str, Any]]:
        events: list[dict[str, Any]] = []
        for choice_index, choice_state in state.choices.items():
            flushed = choice_state.scanner.flush()
            if not (flushed.content or flushed.reasoning or flushed.tool_calls):
                continue
            delta: dict[str, Any] = {}
            if flushed.content:
                delta["content"] = flushed.content
            if flushed.reasoning:
                delta["reasoning_content"] = flushed.reasoning
            if flushed.tool_calls:
                id_prefix = f"call_{choice_index}_"
                delta["tool_calls"] = [
                    _build_tool_call(
                        parsed_call, index=i + choice_state.next_tool_index, id_prefix=id_prefix
                    )
                    for i, parsed_call in enumerate(flushed.tool_calls)
                ]
            events.append({"choices": [{"index": choice_index, "delta": delta}]})
        return events


@dataclass
class ReasoningChoiceState:
    inside_reasoning: bool = False
    scanner: Optional[TagScanner] = None
    mode: Optional[str] = None


@dataclass
class ReasoningSwapStreamState:
    choices: dict[int, ReasoningChoiceState] = field(default_factory=dict)


class ReasoningSwapParser(ResponseParser):
    name = "swap_reasoning_content"

    def __init__(self, config: Mapping[str, Any]) -> None:
        # Load template-derived config if template_path is provided
        template_config = self._load_template_config(config)

        # Merge configs: manual config takes precedence over template-derived
        effective_config = {**template_config, **{k: v for k, v in config.items() if v is not None}}

        mode_raw = str(effective_config.get("mode") or "reasoning_to_content").strip().lower()
        mode_aliases = {
            "to_content": "reasoning_to_content",
            "reasoning_to_content": "reasoning_to_content",
            "reasoning-to-content": "reasoning_to_content",
            "to_reasoning": "content_to_reasoning",
            "content_to_reasoning": "content_to_reasoning",
            "content-to-reasoning": "content_to_reasoning",
            "auto": "auto",
        }
        self.mode = mode_aliases.get(mode_raw, "reasoning_to_content")
        self.think_tag = str(effective_config.get("think_tag") or "think")
        self.include_newline = _parse_bool(effective_config.get("include_newline", True))
        think_open_cfg = effective_config.get("think_open") or {}
        think_close_cfg = effective_config.get("think_close") or {}
        if isinstance(think_open_cfg, str):
            self.think_open_prefix = think_open_cfg
            self.think_open_suffix = ""
        elif isinstance(think_open_cfg, Mapping):
            self.think_open_prefix = str(think_open_cfg.get("prefix") or "")
            self.think_open_suffix = str(think_open_cfg.get("suffix") or "")
        else:
            self.think_open_prefix = ""
            self.think_open_suffix = ""
        if isinstance(think_close_cfg, str):
            self.think_close_prefix = think_close_cfg
            self.think_close_suffix = ""
        elif isinstance(think_close_cfg, Mapping):
            self.think_close_prefix = str(think_close_cfg.get("prefix") or "")
            self.think_close_suffix = str(think_close_cfg.get("suffix") or "")
        else:
            self.think_close_prefix = ""
            self.think_close_suffix = ""

    def _load_template_config(self, config: Mapping[str, Any]) -> dict[str, Any]:
        """Load configuration from template file if template_path is specified."""
        template_path = config.get("template_path")
        if not template_path:
            return {}

        think_tag = config.get("think_tag") or "think"
        try:
            from src.parsers.template_analyzer import load_template_config
            template_config = load_template_config(template_path, think_tag=think_tag)
            logger.info(
                "Loaded swap_reasoning_content config from template: %s "
                "(think_tag=%s, include_newline=%s)",
                template_path,
                template_config.get("think_tag"),
                template_config.get("include_newline"),
            )
            return template_config
        except FileNotFoundError:
            logger.warning(
                "Template file not found for swap_reasoning_content parser: %s. "
                "Falling back to default configuration.",
                template_path,
            )
            return {}
        except Exception as e:
            logger.warning(
                "Failed to load template for swap_reasoning_content parser: %s. "
                "Error: %s. Falling back to default configuration.",
                template_path,
                e,
            )
            return {}

    def _think_open(self) -> str:
        return f"{self.think_open_prefix}<{self.think_tag}>{self.think_open_suffix}"

    def _think_close(self) -> str:
        return f"{self.think_close_prefix}</{self.think_tag}>{self.think_close_suffix}"

    def _wrap_reasoning(self, reasoning: str, content: Optional[str]) -> str:
        open_block = self._think_open()
        close_block = self._think_close()
        prefix = f"{open_block}{reasoning}{close_block}"
        if content:
            sep = "\n" if self.include_newline else ""
            return prefix + sep + content
        return prefix

    def apply_response(self, payload: dict[str, Any], ctx: ParserContext) -> dict[str, Any]:
        choices = payload.get("choices")
        if not isinstance(choices, list):
            return payload

        for choice in choices:
            message = choice.get("message")
            if not isinstance(message, dict):
                continue
            if message.get("role") not in {None, "assistant"}:
                continue
            content = message.get("content")
            reasoning = message.get("reasoning_content")

            if self.mode == "reasoning_to_content":
                if isinstance(reasoning, str) and reasoning:
                    if isinstance(content, str) or content is None:
                        message["content"] = self._wrap_reasoning(reasoning, content)
                        message.pop("reasoning_content", None)
            elif self.mode == "content_to_reasoning":
                if isinstance(content, str) and content:
                    extracted, content = _extract_think_block(content, self.think_tag)
                    if extracted is not None:
                        message["reasoning_content"] = extracted
                        if content.strip():
                            message["content"] = content
                        else:
                            message["content"] = None
            else:
                if isinstance(reasoning, str) and reasoning:
                    if isinstance(content, str) or content is None:
                        message["content"] = self._wrap_reasoning(reasoning, content)
                        message.pop("reasoning_content", None)
                elif isinstance(content, str) and content:
                    extracted, content = _extract_think_block(content, self.think_tag)
                    if extracted is not None:
                        message["reasoning_content"] = extracted
                        if content.strip():
                            message["content"] = content
                        else:
                            message["content"] = None

        return payload

    def create_stream_state(self) -> ReasoningSwapStreamState:
        return ReasoningSwapStreamState()

    def _get_choice_state(
        self, state: ReasoningSwapStreamState, choice_index: int
    ) -> ReasoningChoiceState:
        choice_state = state.choices.get(choice_index)
        if choice_state is None:
            choice_state = ReasoningChoiceState()
            state.choices[choice_index] = choice_state
        return choice_state

    def apply_stream_event(
        self, event: dict[str, Any], state: ReasoningSwapStreamState, ctx: ParserContext
    ) -> dict[str, Any] | list[dict[str, Any]]:
        choices = event.get("choices")
        if not isinstance(choices, list):
            return event

        split_choices: list[dict[str, Any]] = []

        for choice in choices:
            delta = choice.get("delta")
            delta_is_dict = isinstance(delta, dict)
            if not delta_is_dict:
                delta = None
            choice_index = int(choice.get("index", 0))
            choice_state = self._get_choice_state(state, choice_index)

            mode = self.mode
            if mode == "auto":
                if choice_state.mode is None:
                    if delta and isinstance(delta.get("reasoning_content"), str):
                        choice_state.mode = "reasoning_to_content"
                    elif delta:
                        choice_state.mode = "content_to_reasoning"
                    else:
                        continue
                mode = choice_state.mode or "reasoning_to_content"

            if mode == "reasoning_to_content":
                reasoning = delta.get("reasoning_content") if delta else None
                content = delta.get("content") if delta else None
                tool_calls = None
                if delta and "tool_calls" in delta:
                    tool_calls = delta.get("tool_calls")
                elif isinstance(choice.get("tool_calls"), list):
                    tool_calls = choice.get("tool_calls")
                new_content: Optional[str] = None

                if isinstance(reasoning, str) and reasoning:
                    open_block = self._think_open()
                    close_block = self._think_close()
                    prefix = "" if choice_state.inside_reasoning else open_block
                    if isinstance(content, str) and content:
                        sep = "\n" if self.include_newline else ""
                        new_content = f"{prefix}{reasoning}{close_block}{sep}{content}"
                        choice_state.inside_reasoning = False
                    else:
                        new_content = f"{prefix}{reasoning}"
                        choice_state.inside_reasoning = True
                    delta.pop("reasoning_content", None)

                if isinstance(content, str) and content and choice_state.inside_reasoning:
                    close_block = self._think_close()
                    sep = "\n" if self.include_newline else ""
                    new_content = f"{close_block}{sep}{content}"
                    choice_state.inside_reasoning = False

                if new_content is not None:
                    delta["content"] = new_content

                split_content: Optional[str] = None
                if tool_calls:
                    if delta_is_dict:
                        existing_content = delta.get("content")
                        if isinstance(existing_content, str) and existing_content:
                            split_content = existing_content
                            delta.pop("content", None)
                    if choice_state.inside_reasoning:
                        close_block = self._think_close()
                        split_content = (split_content or "") + close_block
                        choice_state.inside_reasoning = False
                    if split_content:
                        split_choices.append(
                            {"index": choice_index, "delta": {"content": split_content}}
                        )

                if choice_state.inside_reasoning and choice.get("finish_reason") is not None:
                    close_block = self._think_close()
                    if not delta:
                        delta = {}
                        choice["delta"] = delta
                    delta["content"] = (delta.get("content") or "") + close_block
                    choice_state.inside_reasoning = False

                continue

            if mode == "content_to_reasoning":
                if not delta:
                    continue
                content = delta.get("content")
                if not isinstance(content, str) or not content:
                    continue
                if choice_state.scanner is None:
                    choice_state.scanner = TagScanner(
                        think_tag=self.think_tag,
                        tool_tag="tool_call",
                        parse_thinking=True,
                        parse_tool_calls=False,
                    )
                result = choice_state.scanner.feed(content)
                if result.content:
                    delta["content"] = result.content
                else:
                    delta.pop("content", None)
                if result.reasoning:
                    existing = delta.get("reasoning_content")
                    if isinstance(existing, str):
                        delta["reasoning_content"] = existing + result.reasoning
                    else:
                        delta["reasoning_content"] = result.reasoning
                continue

        if split_choices:
            close_event = {key: value for key, value in event.items() if key != "choices"}
            close_event["choices"] = split_choices
            return [close_event, event]
        return event

    def finalize_stream(
        self, state: ReasoningSwapStreamState, ctx: ParserContext
    ) -> list[dict[str, Any]]:
        events: list[dict[str, Any]] = []
        if self.mode == "reasoning_to_content":
            close_tag = self._think_close()
            for choice_index, choice_state in state.choices.items():
                if choice_state.inside_reasoning:
                    events.append(
                        {
                            "choices": [
                                {"index": choice_index, "delta": {"content": close_tag}}
                            ]
                        }
                    )
                    choice_state.inside_reasoning = False
            return events

        if self.mode in {"content_to_reasoning", "auto"}:
            for choice_index, choice_state in state.choices.items():
                if choice_state.scanner is None:
                    continue
                flushed = choice_state.scanner.flush()
                if not (flushed.content or flushed.reasoning):
                    continue
                delta: dict[str, Any] = {}
                if flushed.content:
                    delta["content"] = flushed.content
                if flushed.reasoning:
                    delta["reasoning_content"] = flushed.reasoning
                events.append({"choices": [{"index": choice_index, "delta": delta}]})
        return events


@dataclass
class SSEEvent:
    data: Optional[str]
    other_lines: list[str] = field(default_factory=list)

    def encode(self) -> bytes:
        lines: list[str] = []
        lines.extend(self.other_lines)
        if self.data is not None:
            for item in self.data.split("\n"):
                if item:
                    lines.append(f"data: {item}")
                else:
                    lines.append("data:")
        text = "\n".join(lines) + "\n\n"
        return text.encode("utf-8")


class SSEDecoder:
    def __init__(self) -> None:
        self._buffer = ""

    def feed(self, chunk: bytes) -> list[SSEEvent]:
        if not chunk:
            return []
        text = chunk.decode("utf-8", errors="replace")
        text = text.replace("\r\n", "\n").replace("\r", "\n")
        self._buffer += text
        events: list[SSEEvent] = []

        while True:
            sep_index = self._buffer.find("\n\n")
            if sep_index == -1:
                break
            raw_event = self._buffer[:sep_index]
            self._buffer = self._buffer[sep_index + 2:]
            if not raw_event.strip():
                continue
            events.append(self._parse_event(raw_event))

        return events

    def flush(self) -> Optional[bytes]:
        if not self._buffer:
            return None
        leftover = self._buffer
        self._buffer = ""
        return leftover.encode("utf-8")

    @staticmethod
    def _parse_event(raw: str) -> SSEEvent:
        data_lines: list[str] = []
        other_lines: list[str] = []
        for line in raw.split("\n"):
            if line.startswith("data:"):
                data_lines.append(line[5:].lstrip())
            else:
                other_lines.append(line)
        data = "\n".join(data_lines) if data_lines else None
        return SSEEvent(data=data, other_lines=other_lines)


class ResponseStreamParser:
    def __init__(self, pipeline: "ResponseParserPipeline", ctx: ParserContext) -> None:
        self.pipeline = pipeline
        self.ctx = ctx
        self.decoder = SSEDecoder()
        self.states = [parser.create_stream_state() for parser in pipeline.parsers]
        self._last_envelope: Optional[dict[str, Any]] = None
        self._saw_done = False

    def feed_bytes(self, chunk: bytes) -> list[bytes]:
        output: list[bytes] = []
        for event in self.decoder.feed(chunk):
            output.extend(self._process_event(event))
        return output

    def finish(self) -> list[bytes]:
        output: list[bytes] = []
        if not self._saw_done:
            for event in self._finalize_events():
                output.append(self._encode_event_json(event))
        leftover = self.decoder.flush()
        if leftover:
            output.append(leftover)
        return output

    def _apply_event(self, event: dict[str, Any]) -> list[dict[str, Any]]:
        current_events = [event]
        for parser, state in zip(self.pipeline.parsers, self.states):
            next_events: list[dict[str, Any]] = []
            for current in current_events:
                updated = parser.apply_stream_event(current, state, self.ctx)
                if isinstance(updated, list):
                    next_events.extend(updated)
                else:
                    next_events.append(updated)
            current_events = next_events
        return current_events

    def _apply_event_from_index(self, event: dict[str, Any], start: int) -> list[dict[str, Any]]:
        current_events = [event]
        for parser, state in zip(self.pipeline.parsers[start:], self.states[start:]):
            next_events: list[dict[str, Any]] = []
            for current in current_events:
                updated = parser.apply_stream_event(current, state, self.ctx)
                if isinstance(updated, list):
                    next_events.extend(updated)
                else:
                    next_events.append(updated)
            current_events = next_events
        return current_events

    def _finalize_events(self) -> list[dict[str, Any]]:
        extras: list[dict[str, Any]] = []
        for idx, parser in enumerate(self.pipeline.parsers):
            emitted = parser.finalize_stream(self.states[idx], self.ctx)
            for event in emitted:
                extras.extend(self._apply_event_from_index(event, idx + 1))
        return [self._merge_envelope(event) for event in extras]

    def _merge_envelope(self, event: dict[str, Any]) -> dict[str, Any]:
        if self._last_envelope:
            merged = dict(self._last_envelope)
            merged.update(event)
            return merged
        return event

    def _encode_event_json(self, event: dict[str, Any]) -> bytes:
        data = json.dumps(event, ensure_ascii=False)
        sse_event = SSEEvent(data=data)
        return sse_event.encode()

    def _process_event(self, event: SSEEvent) -> list[bytes]:
        data = event.data
        if data is None:
            return [event.encode()]
        if data.strip() == "[DONE]":
            self._saw_done = True
            output: list[bytes] = []
            for extra in self._finalize_events():
                output.append(self._encode_event_json(extra))
            output.append(event.encode())
            return output

        try:
            payload = json.loads(data)
        except json.JSONDecodeError:
            return [event.encode()]
        if not isinstance(payload, dict):
            return [event.encode()]

        updated_events = self._apply_event(payload)
        if not updated_events:
            return [event.encode()]

        output: list[bytes] = []
        for updated in updated_events:
            if not isinstance(updated, dict):
                output.append(event.encode())
                continue
            merged = self._merge_envelope(updated)
            envelope = dict(merged)
            envelope.pop("choices", None)
            if envelope:
                self._last_envelope = envelope
            data = json.dumps(merged, ensure_ascii=False)
            output.append(SSEEvent(data=data, other_lines=event.other_lines).encode())
        return output


ResponseStreamModule = ResponseStreamParser


class ResponseParserPipeline:
    def __init__(self, parsers: Iterable[ResponseParser], paths: Iterable[str]) -> None:
        self.parsers = list(parsers)
        self.paths = [p for p in paths if p]

    def applies(self, ctx: ParserContext) -> bool:
        if not self.parsers:
            return False
        if not self.paths:
            return True
        return any(path in ctx.path for path in self.paths)

    def transform_response_body(
        self, body: bytes, content_type: Optional[str], ctx: ParserContext
    ) -> Optional[bytes]:
        if not self.applies(ctx):
            return None
        if not content_type or "application/json" not in content_type.lower():
            return None
        try:
            payload = json.loads(body)
        except json.JSONDecodeError:
            return None
        if not isinstance(payload, dict):
            return None
        for parser in self.parsers:
            payload = parser.apply_response(payload, ctx)
        return json.dumps(payload, ensure_ascii=False).encode("utf-8")

    def create_stream_parser(self, ctx: ParserContext) -> Optional[ResponseStreamParser]:
        if not self.applies(ctx):
            return None
        return ResponseStreamParser(self, ctx)


ResponseModulePipeline = ResponseParserPipeline


def _select_upstream_modules_config(
    root_cfg: Mapping[str, Any]
) -> dict[str, Any]:
    modules_cfg = root_cfg.get("modules")
    parsers_cfg = root_cfg.get("parsers")
    if isinstance(modules_cfg, Mapping) and isinstance(parsers_cfg, Mapping):
        logger.warning(
            "Both 'modules' and legacy 'parsers' configs are present; using 'modules'."
        )
    if isinstance(modules_cfg, Mapping):
        if "upstream" in modules_cfg or "downstream" in modules_cfg:
            upstream_cfg = modules_cfg.get("upstream")
            if not isinstance(upstream_cfg, Mapping):
                upstream_cfg = {}
            if "enabled" not in upstream_cfg and "enabled" in modules_cfg:
                upstream_cfg = dict(upstream_cfg)
                upstream_cfg["enabled"] = modules_cfg.get("enabled")
            return dict(upstream_cfg)
        return dict(modules_cfg)
    if isinstance(parsers_cfg, Mapping):
        return dict(parsers_cfg)
    # Support direct upstream config (e.g., per-model override payloads)
    if "upstream" in root_cfg or "downstream" in root_cfg:
        upstream_cfg = root_cfg.get("upstream")
        if not isinstance(upstream_cfg, Mapping):
            upstream_cfg = {}
        if "enabled" not in upstream_cfg and "enabled" in root_cfg:
            upstream_cfg = dict(upstream_cfg)
            upstream_cfg["enabled"] = root_cfg.get("enabled")
        return dict(upstream_cfg)
    if any(
        key in root_cfg
        for key in (
            "response",
            "paths",
            "parse_unparsed",
            "parse_template",
            "swap_reasoning_content",
        )
    ):
        return dict(root_cfg)
    return {}


def build_response_module_pipeline(
    config: Mapping[str, Any],
    *,
    enabled_default: bool = False,
    default_paths: Optional[Iterable[str]] = None,
) -> ResponseParserPipeline:
    if "proxy_settings" in config:
        proxy_settings = config.get("proxy_settings") or {}
        modules_cfg = _select_upstream_modules_config(proxy_settings)
        enabled_default = False
    else:
        modules_cfg = _select_upstream_modules_config(config or {})

    if not modules_cfg:
        return ResponseParserPipeline([], [])

    enabled_raw = modules_cfg.get("enabled")
    enabled = enabled_default if enabled_raw is None else _parse_bool(enabled_raw)
    if not enabled:
        return ResponseParserPipeline([], [])

    module_names = _ensure_list(modules_cfg.get("response"))
    if not module_names:
        return ResponseParserPipeline([], [])

    available = {
        "parse_unparsed": ParseTagsParser,
        "parse_unparsed_tags": ParseTagsParser,
        "parse_tags": ParseTagsParser,
        "parse_template": TemplateParseParser,
        "parse_unparsed_template": TemplateParseParser,
        "swap_reasoning_content": ReasoningSwapParser,
        "swap_reasoning": ReasoningSwapParser,
    }

    parsed_parsers: list[ResponseParser] = []
    for name in module_names:
        parser_cls = available.get(name)
        if not parser_cls:
            logger.warning("Unknown response module '%s' configured; skipping", name)
            continue
        parser_config = modules_cfg.get(name) or {}
        parsed_parsers.append(parser_cls(parser_config))

    # Enforce ordering: parse tags before swap if both enabled.
    names = [p.name for p in parsed_parsers]
    if "swap_reasoning_content" in names:
        parse_candidates = [
            idx
            for idx, name in enumerate(names)
            if name in {"parse_unparsed", "parse_template"}
        ]
        if parse_candidates:
            swap_index = names.index("swap_reasoning_content")
            last_parse_index = max(parse_candidates)
            if swap_index < last_parse_index:
                parsed_parsers.insert(last_parse_index, parsed_parsers.pop(swap_index))
                logger.info(
                    "Reordered response modules to run parse_unparsed/parse_template before swap_reasoning_content"
                )

    paths = _ensure_list(modules_cfg.get("paths"))
    if not paths:
        if default_paths:
            paths = list(default_paths)
        else:
            paths = ["/chat/completions"]
    return ResponseParserPipeline(parsed_parsers, paths)


def build_response_module_overrides(
    config: Mapping[str, Any],
) -> dict[str, ResponseParserPipeline]:
    overrides: dict[str, ResponseParserPipeline] = {}
    model_list = config.get("model_list") or []
    for entry in model_list:
        if not isinstance(entry, Mapping):
            continue
        name = entry.get("model_name")
        if not name:
            continue
        modules_cfg = entry.get("modules")
        if modules_cfg is None:
            modules_cfg = entry.get("parsers")
        if modules_cfg is None:
            model_params = entry.get("model_params") or {}
            if isinstance(model_params, Mapping):
                modules_cfg = model_params.get("modules")
                if modules_cfg is None:
                    modules_cfg = model_params.get("parsers")
        if modules_cfg is None:
            continue
        overrides[str(name)] = build_response_module_pipeline(
            modules_cfg,
            enabled_default=True,
            default_paths=["/chat/completions"],
        )
    return overrides


def build_response_parser_pipeline(
    config: Mapping[str, Any],
    *,
    enabled_default: bool = False,
    default_paths: Optional[Iterable[str]] = None,
) -> ResponseParserPipeline:
    return build_response_module_pipeline(
        config,
        enabled_default=enabled_default,
        default_paths=default_paths,
    )


def build_response_parser_overrides(
    config: Mapping[str, Any],
) -> dict[str, ResponseParserPipeline]:
    return build_response_module_overrides(config)
