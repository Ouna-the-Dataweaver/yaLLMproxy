#!/usr/bin/env python3
"""Simulate proxy parsing using a fake upstream + in-process proxy app.

Usage:
    uv run python scripts/simulate_proxy.py --messages path/to/chat.json \
        --template configs/jinja_templates/template_example.jinja \
        --mode unparse_both --stream
"""

from __future__ import annotations

import argparse
import asyncio
import json
from pathlib import Path
from typing import Any, Mapping, Optional

import httpx

from src.core.upstream_transport import clear_upstream_transports, register_upstream_transport
from src.modules.response_pipeline import SSEDecoder
from src.testing import (
    FakeUpstream,
    ProxyHarness,
    UpstreamResponse,
    normalize_message_for_compare,
    unparse_assistant_message,
)


MODE_CHOICES = {
    "as_is": (False, False),
    "unparse_tool_calls": (False, True),
    "unparse_reasoning": (True, False),
    "unparse_both": (True, True),
}


def _load_messages(path: Path) -> list[dict[str, Any]]:
    data = json.loads(path.read_text(encoding="utf-8"))
    if isinstance(data, dict) and "messages" in data:
        messages = data["messages"]
    else:
        messages = data
    if not isinstance(messages, list):
        raise ValueError("messages must be a list or wrapped in {'messages': [...]}")
    return messages


def _find_last_assistant(messages: list[Mapping[str, Any]]) -> dict[str, Any]:
    for message in reversed(messages):
        if message.get("role") == "assistant":
            return dict(message)
    raise ValueError("No assistant message found in messages.")


def _build_config(base_url: str, parser: str, template_path: Optional[str]) -> dict:
    module_cfg: dict[str, Any] = {
        "enabled": True,
        "response": [parser],
        parser: {
            "parse_thinking": True,
            "parse_tool_calls": True,
        },
    }
    if template_path:
        module_cfg[parser]["template_path"] = template_path
    return {
        "model_list": [
            {
                "model_name": "alpha",
                "model_params": {
                    "model": "openai/fake",
                    "api_base": base_url,
                    "api_key": "test-key",
                },
            }
        ],
        "proxy_settings": {"modules": module_cfg},
    }


def _build_response_payload(message: Mapping[str, Any]) -> dict[str, Any]:
    return {
        "id": "cmpl-sim",
        "object": "chat.completion",
        "model": "fake",
        "choices": [{"index": 0, "message": dict(message)}],
    }


def _split_chunks(text: str, chunk_size: int) -> list[str]:
    if chunk_size <= 0:
        return [text]
    return [text[i:i + chunk_size] for i in range(0, len(text), chunk_size)]


def _build_stream_events(raw_content: str, chunk_size: int) -> list[dict[str, Any]]:
    chunks = _split_chunks(raw_content, chunk_size)
    events: list[dict[str, Any]] = []
    for idx, chunk in enumerate(chunks):
        delta: dict[str, Any] = {"content": chunk}
        if idx == 0:
            delta["role"] = "assistant"
        events.append({"choices": [{"index": 0, "delta": delta}]})
    return events


def _assemble_stream_message(events: list[dict[str, Any]]) -> dict[str, Any]:
    content_parts: list[str] = []
    reasoning_parts: list[str] = []
    tool_calls: list[dict[str, Any]] = []

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
                content_parts.append(content)
            reasoning = delta.get("reasoning_content")
            if isinstance(reasoning, str):
                reasoning_parts.append(reasoning)
            calls = delta.get("tool_calls")
            if isinstance(calls, list):
                tool_calls.extend(calls)

    message: dict[str, Any] = {"role": "assistant"}
    if content_parts:
        message["content"] = "".join(content_parts)
    else:
        message["content"] = None
    if reasoning_parts:
        message["reasoning_content"] = "".join(reasoning_parts)
    if tool_calls:
        message["tool_calls"] = tool_calls
    return message


async def _run_once(
    *,
    messages: list[dict[str, Any]],
    template_path: Optional[str],
    mode: str,
    parser: str,
    stream: bool,
    chunk_size: int,
) -> tuple[bool, dict[str, Any], dict[str, Any]]:
    last_assistant = _find_last_assistant(messages)
    unparse_reasoning, unparse_tool_calls = MODE_CHOICES[mode]

    if mode == "as_is":
        upstream_message = dict(last_assistant)
    else:
        upstream_message = unparse_assistant_message(
            last_assistant,
            template_path=template_path,
            unparse_reasoning=unparse_reasoning,
            unparse_tool_calls=unparse_tool_calls,
        )

    upstream = FakeUpstream()
    if stream:
        raw_content = upstream_message.get("content") or ""
        stream_events = _build_stream_events(str(raw_content), chunk_size)
        upstream.enqueue(
            UpstreamResponse(stream=True, stream_events=stream_events)
        )
    else:
        upstream.enqueue(UpstreamResponse(json_body=_build_response_payload(upstream_message)))

    base_url = "http://upstream.local/v1"
    register_upstream_transport("upstream.local", httpx.ASGITransport(app=upstream.app))

    proxy_config = _build_config(base_url, parser, template_path)
    with ProxyHarness(proxy_config) as proxy:
        async with proxy.make_async_client() as client:
            if stream:
                async with client.stream(
                    "POST",
                    "/v1/chat/completions",
                    json={
                        "model": "alpha",
                        "messages": messages,
                        "stream": True,
                    },
                ) as resp:
                    decoder = SSEDecoder()
                    events: list[dict[str, Any]] = []
                    async for chunk in resp.aiter_raw():
                        for event in decoder.feed(chunk):
                            if event.data is None:
                                continue
                            if event.data.strip() == "[DONE]":
                                continue
                            events.append(json.loads(event.data))
                actual_message = _assemble_stream_message(events)
            else:
                resp = await client.post(
                    "/v1/chat/completions",
                    json={
                        "model": "alpha",
                        "messages": messages,
                    },
                )
                payload = resp.json()
                actual_message = payload["choices"][0]["message"]

    expected_norm = normalize_message_for_compare(last_assistant)
    actual_norm = normalize_message_for_compare(actual_message)
    return expected_norm == actual_norm, expected_norm, actual_norm


async def main() -> int:
    parser = argparse.ArgumentParser(description="Simulate proxy parsing with fake upstream")
    parser.add_argument("--messages", required=True, help="Path to messages JSON file")
    parser.add_argument(
        "--template",
        action="append",
        default=[],
        help="Template path(s) to test (repeatable)",
    )
    parser.add_argument(
        "--template-dir",
        help="Directory to load *.jinja templates from",
    )
    parser.add_argument(
        "--mode",
        choices=sorted(MODE_CHOICES.keys()),
        default="unparse_both",
        help="How to build the upstream response content",
    )
    parser.add_argument(
        "--parser",
        choices=["parse_template", "parse_unparsed"],
        default="parse_template",
        help="Parser module to enable in the proxy",
    )
    parser.add_argument("--stream", action="store_true", help="Use streaming responses")
    parser.add_argument(
        "--chunk-size",
        type=int,
        default=24,
        help="Chunk size for streaming content",
    )
    args = parser.parse_args()

    messages = _load_messages(Path(args.messages))

    templates = list(args.template)
    if args.template_dir:
        template_dir = Path(args.template_dir)
        templates.extend(str(p) for p in sorted(template_dir.glob("*.jinja")))
    if not templates:
        templates = [None]

    overall_ok = True
    for template_path in templates:
        clear_upstream_transports()
        ok, expected, actual = await _run_once(
            messages=messages,
            template_path=template_path,
            mode=args.mode,
            parser=args.parser,
            stream=args.stream,
            chunk_size=args.chunk_size,
        )
        label = template_path or "(no template)"
        if ok:
            print(f"[PASS] {label}")
        else:
            overall_ok = False
            print(f"[FAIL] {label}")
            print("Expected:", json.dumps(expected, indent=2, ensure_ascii=False))
            print("Actual:  ", json.dumps(actual, indent=2, ensure_ascii=False))

    clear_upstream_transports()
    return 0 if overall_ok else 1


if __name__ == "__main__":
    raise SystemExit(asyncio.run(main()))
