"""Response parser pipeline for transforming backend responses before returning to clients."""

from __future__ import annotations

import json
import logging
import re
from dataclasses import dataclass, field
from typing import Any, Iterable, Mapping, Optional


logger = logging.getLogger("yallmp-proxy")


@dataclass(frozen=True)
class ParserContext:
    path: str
    model: str
    backend: str
    is_stream: bool


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
    ) -> None:
        self.parse_thinking = parse_thinking
        self.parse_tool_calls = parse_tool_calls
        self.think_open = f"<{think_tag}>"
        self.think_close = f"</{think_tag}>"
        self.tool_open = f"<{tool_tag}>"
        self.tool_close = f"</{tool_tag}>"
        self._open_tags = [
            tag
            for tag, enabled in [
                (self.think_open, self.parse_thinking),
                (self.tool_open, self.parse_tool_calls),
            ]
            if enabled
        ]
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
                parsed = _parse_tool_call_block(self.tool_buffer)
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
    ) -> dict[str, Any]:
        return event

    def finalize_stream(self, state: Any, ctx: ParserContext) -> list[dict[str, Any]]:
        return []


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
        mode_raw = str(config.get("mode") or "reasoning_to_content").strip().lower()
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
        self.think_tag = str(config.get("think_tag") or "think")
        self.include_newline = _parse_bool(config.get("include_newline", True))

    def _wrap_reasoning(self, reasoning: str, content: Optional[str]) -> str:
        open_tag = f"<{self.think_tag}>"
        close_tag = f"</{self.think_tag}>"
        prefix = f"{open_tag}{reasoning}{close_tag}"
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
    ) -> dict[str, Any]:
        choices = event.get("choices")
        if not isinstance(choices, list):
            return event

        for choice in choices:
            delta = choice.get("delta")
            if not isinstance(delta, dict):
                continue
            choice_index = int(choice.get("index", 0))
            choice_state = self._get_choice_state(state, choice_index)

            mode = self.mode
            if mode == "auto":
                if choice_state.mode is None:
                    if isinstance(delta.get("reasoning_content"), str):
                        choice_state.mode = "reasoning_to_content"
                    else:
                        choice_state.mode = "content_to_reasoning"
                mode = choice_state.mode or "reasoning_to_content"

            if mode == "reasoning_to_content":
                reasoning = delta.get("reasoning_content")
                content = delta.get("content")
                new_content: Optional[str] = None

                if isinstance(reasoning, str) and reasoning:
                    open_tag = f"<{self.think_tag}>"
                    close_tag = f"</{self.think_tag}>"
                    prefix = "" if choice_state.inside_reasoning else open_tag
                    if isinstance(content, str) and content:
                        new_content = f"{prefix}{reasoning}{close_tag}{content}"
                        choice_state.inside_reasoning = False
                    else:
                        new_content = f"{prefix}{reasoning}"
                        choice_state.inside_reasoning = True
                    delta.pop("reasoning_content", None)

                if isinstance(content, str) and content and choice_state.inside_reasoning:
                    close_tag = f"</{self.think_tag}>"
                    new_content = f"{close_tag}{content}"
                    choice_state.inside_reasoning = False

                if new_content is not None:
                    delta["content"] = new_content

                if choice_state.inside_reasoning and choice.get("finish_reason") is not None:
                    close_tag = f"</{self.think_tag}>"
                    delta["content"] = (delta.get("content") or "") + close_tag
                    choice_state.inside_reasoning = False

                continue

            if mode == "content_to_reasoning":
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

        return event

    def finalize_stream(
        self, state: ReasoningSwapStreamState, ctx: ParserContext
    ) -> list[dict[str, Any]]:
        events: list[dict[str, Any]] = []
        if self.mode == "reasoning_to_content":
            close_tag = f"</{self.think_tag}>"
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

    def _apply_event(self, event: dict[str, Any]) -> dict[str, Any]:
        current = event
        for parser, state in zip(self.pipeline.parsers, self.states):
            current = parser.apply_stream_event(current, state, self.ctx)
        return current

    def _apply_event_from_index(self, event: dict[str, Any], start: int) -> dict[str, Any]:
        current = event
        for parser, state in zip(self.pipeline.parsers[start:], self.states[start:]):
            current = parser.apply_stream_event(current, state, self.ctx)
        return current

    def _finalize_events(self) -> list[dict[str, Any]]:
        extras: list[dict[str, Any]] = []
        for idx, parser in enumerate(self.pipeline.parsers):
            emitted = parser.finalize_stream(self.states[idx], self.ctx)
            for event in emitted:
                extras.append(self._apply_event_from_index(event, idx + 1))
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

        updated = self._apply_event(payload)
        if isinstance(updated, dict):
            envelope = dict(updated)
            envelope.pop("choices", None)
            self._last_envelope = envelope
        event.data = json.dumps(updated, ensure_ascii=False)
        return [event.encode()]


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


def build_response_parser_pipeline(
    config: Mapping[str, Any],
    *,
    enabled_default: bool = False,
    default_paths: Optional[Iterable[str]] = None,
) -> ResponseParserPipeline:
    if "proxy_settings" in config:
        proxy_settings = config.get("proxy_settings") or {}
        parsers_cfg = proxy_settings.get("parsers") or {}
        enabled_default = False
    else:
        parsers_cfg = config or {}

    if not parsers_cfg:
        return ResponseParserPipeline([], [])

    enabled_raw = parsers_cfg.get("enabled")
    enabled = enabled_default if enabled_raw is None else _parse_bool(enabled_raw)
    if not enabled:
        return ResponseParserPipeline([], [])

    parser_names = _ensure_list(parsers_cfg.get("response"))
    if not parser_names:
        return ResponseParserPipeline([], [])

    available = {
        "parse_unparsed": ParseTagsParser,
        "parse_unparsed_tags": ParseTagsParser,
        "parse_tags": ParseTagsParser,
        "swap_reasoning_content": ReasoningSwapParser,
        "swap_reasoning": ReasoningSwapParser,
    }

    parsed_parsers: list[ResponseParser] = []
    for name in parser_names:
        parser_cls = available.get(name)
        if not parser_cls:
            logger.warning("Unknown response parser '%s' configured; skipping", name)
            continue
        parser_config = parsers_cfg.get(name) or {}
        parsed_parsers.append(parser_cls(parser_config))

    # Enforce ordering: parse tags before swap if both enabled.
    names = [p.name for p in parsed_parsers]
    if "parse_unparsed" in names and "swap_reasoning_content" in names:
        parse_index = names.index("parse_unparsed")
        swap_index = names.index("swap_reasoning_content")
        if swap_index < parse_index:
            parsed_parsers.insert(parse_index, parsed_parsers.pop(swap_index))
            logger.info("Reordered response parsers to run parse_unparsed before swap_reasoning_content")

    paths = _ensure_list(parsers_cfg.get("paths"))
    if not paths:
        if default_paths:
            paths = list(default_paths)
        else:
            paths = ["/chat/completions"]
    return ResponseParserPipeline(parsed_parsers, paths)


def build_response_parser_overrides(
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
        parsers_cfg = entry.get("parsers")
        if parsers_cfg is None:
            model_params = entry.get("model_params") or {}
            if isinstance(model_params, Mapping):
                parsers_cfg = model_params.get("parsers")
        if parsers_cfg is None:
            continue
        overrides[str(name)] = build_response_parser_pipeline(
            parsers_cfg,
            enabled_default=True,
            default_paths=["/chat/completions"],
        )
    return overrides
