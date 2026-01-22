"""Fake upstream ASGI app for simulating deterministic responses."""

from __future__ import annotations

import asyncio
import json
import uuid
from collections import deque
from dataclasses import dataclass, field
from typing import Any, Callable, Deque, Iterable, Optional

from fastapi import FastAPI, Request
from fastapi.responses import JSONResponse, Response, StreamingResponse

from ..parsers.response_pipeline import SSEEvent


class StreamError(Exception):
    """Raised to simulate mid-stream errors."""

    pass


@dataclass
class UpstreamResponse:
    """A queued response to return from the fake upstream.

    Standard fields:
        status_code: HTTP status code (default 200)
        headers: Response headers
        json_body: JSON response body (for non-streaming)
        body: Raw bytes/string body
        stream: Force streaming mode (None = follow request)
        stream_events: List of SSE events for streaming
        media_type: Response content type
        add_done: Add [DONE] sentinel at end of stream
        chunk_delay_s: Delay between stream chunks

    Error simulation fields:
        error_after_events: Raise error after N events (for mid-stream errors)
        error_type: Type of error ("connection_reset", "timeout", "malformed_sse")

    Chunk fragmentation fields:
        fragment_events: Split SSE events across multiple chunks
        chunk_sizes: Explicit byte boundaries for chunk splits

    Malformed data injection:
        inject_malformed_at: Event index to inject bad data
        malformed_data: The malformed data to inject

    Dynamic response:
        response_fn: Callable that receives request and returns UpstreamResponse
    """

    status_code: int = 200
    headers: dict[str, str] = field(default_factory=dict)
    json_body: dict[str, Any] | None = None
    body: bytes | str | None = None
    stream: bool | None = None
    stream_events: list[Any] | None = None
    media_type: str | None = None
    add_done: bool = True
    chunk_delay_s: float | None = None

    # Error simulation
    error_after_events: int | None = None
    error_type: str | None = None  # "connection_reset", "timeout", "malformed_sse"

    # Chunk fragmentation
    fragment_events: bool = False
    chunk_sizes: list[int] | None = None

    # Malformed data injection
    inject_malformed_at: int | None = None
    malformed_data: bytes | None = None

    # Dynamic response based on request
    response_fn: Callable[[dict], "UpstreamResponse"] | None = None

    def resolve_stream(self, request_stream: bool) -> bool:
        if self.stream is None:
            return request_stream
        return bool(self.stream)


def _encode_sse_event(event: Any) -> bytes:
    if isinstance(event, bytes):
        return event
    if isinstance(event, str):
        data = event
    else:
        data = json.dumps(event, ensure_ascii=False)
    return SSEEvent(data=data).encode()


class FakeUpstream:
    """ASGI app that replies with queued responses for /v1/chat/completions.

    Supports:
    - Deterministic response queueing
    - Request tracking/inspection
    - Streaming with SSE events
    - Error simulation (mid-stream errors, timeouts)
    - Chunk fragmentation for edge case testing
    - Malformed data injection
    - Dynamic responses based on request content
    """

    def __init__(
        self,
        responses: Optional[Iterable[UpstreamResponse]] = None,
        *,
        route: str = "/v1/chat/completions",
    ) -> None:
        self.app = FastAPI(title="FakeUpstream")
        self._queue: Deque[UpstreamResponse] = deque(responses or [])
        self.received: list[dict[str, Any]] = []
        self.route = route
        self.app.post(route)(self._handle_chat)

    def enqueue(self, response: UpstreamResponse) -> None:
        """Add a response to the queue."""
        self._queue.append(response)

    def clear(self) -> None:
        """Clear all queued responses and received requests."""
        self._queue.clear()
        self.received.clear()

    # -------------------------------------------------------------------------
    # Convenience methods for common response types
    # -------------------------------------------------------------------------

    def enqueue_openai_chat_response(
        self,
        content: str,
        *,
        tool_calls: list[dict[str, Any]] | None = None,
        finish_reason: str = "stop",
        usage: dict[str, int] | None = None,
        model: str = "fake-model",
        stream: bool = False,
        chunk_delay_s: float | None = None,
    ) -> None:
        """Enqueue a properly-formatted OpenAI chat completion response.

        Args:
            content: The assistant message content
            tool_calls: Optional list of tool calls
            finish_reason: Finish reason (stop, length, tool_calls)
            usage: Token usage dict (prompt_tokens, completion_tokens, total_tokens)
            model: Model name
            stream: Whether to stream the response
            chunk_delay_s: Delay between chunks (for streaming)
        """
        if stream:
            chunks = self._build_openai_stream_chunks(
                content, tool_calls, finish_reason, usage, model
            )
            self.enqueue(
                UpstreamResponse(
                    stream=True,
                    stream_events=chunks,
                    chunk_delay_s=chunk_delay_s,
                )
            )
        else:
            response_body = self._build_openai_response(
                content, tool_calls, finish_reason, usage, model
            )
            self.enqueue(UpstreamResponse(json_body=response_body))

    def enqueue_anthropic_message_response(
        self,
        content: list[dict[str, Any]],
        *,
        stop_reason: str = "end_turn",
        usage: dict[str, int] | None = None,
        model: str = "fake-model",
        stream: bool = False,
        chunk_delay_s: float | None = None,
    ) -> None:
        """Enqueue a properly-formatted Anthropic message response.

        Args:
            content: List of content blocks
            stop_reason: Stop reason (end_turn, max_tokens, tool_use)
            usage: Token usage dict (input_tokens, output_tokens)
            model: Model name
            stream: Whether to stream the response
            chunk_delay_s: Delay between chunks (for streaming)
        """
        if stream:
            events = self._build_anthropic_stream_events(
                content, stop_reason, usage, model
            )
            self.enqueue(
                UpstreamResponse(
                    stream=True,
                    stream_events=events,
                    chunk_delay_s=chunk_delay_s,
                )
            )
        else:
            response_body = self._build_anthropic_response(
                content, stop_reason, usage, model
            )
            self.enqueue(UpstreamResponse(json_body=response_body))

    def enqueue_error_response(
        self,
        status_code: int,
        error_type: str,
        message: str,
        *,
        anthropic_format: bool = True,
    ) -> None:
        """Enqueue an error response.

        Args:
            status_code: HTTP status code
            error_type: Error type (e.g., "invalid_request_error", "rate_limit_error")
            message: Error message
            anthropic_format: Use Anthropic error format (vs OpenAI format)
        """
        if anthropic_format:
            body = {
                "type": "error",
                "error": {
                    "type": error_type,
                    "message": message,
                },
            }
        else:
            body = {
                "error": {
                    "type": error_type,
                    "message": message,
                    "code": error_type,
                },
            }
        self.enqueue(UpstreamResponse(status_code=status_code, json_body=body))

    def enqueue_mid_stream_error(
        self,
        events_before_error: int,
        *,
        error_type: str = "connection_reset",
        partial_content: str = "Partial response",
        model: str = "fake-model",
    ) -> None:
        """Queue a stream that fails partway through.

        Args:
            events_before_error: Number of events to send before error
            error_type: Type of error to simulate
            partial_content: Content to send before error
            model: Model name
        """
        # Build partial chunks
        chunks = []
        # First chunk with role
        chunks.append(
            {
                "id": f"chatcmpl-{uuid.uuid4().hex[:8]}",
                "object": "chat.completion.chunk",
                "model": model,
                "choices": [
                    {"index": 0, "delta": {"role": "assistant", "content": ""}}
                ],
            }
        )
        # Content chunks
        for i, char in enumerate(partial_content):
            if i >= events_before_error - 1:
                break
            chunks.append(
                {
                    "object": "chat.completion.chunk",
                    "choices": [{"index": 0, "delta": {"content": char}}],
                }
            )

        self.enqueue(
            UpstreamResponse(
                stream=True,
                stream_events=chunks,
                error_after_events=events_before_error,
                error_type=error_type,
                add_done=False,  # Error prevents [DONE]
            )
        )

    # -------------------------------------------------------------------------
    # Request handling
    # -------------------------------------------------------------------------

    async def _handle_chat(self, request: Request) -> Response:
        payload: Any = None
        try:
            payload = await request.json()
        except Exception:
            payload = None

        self.received.append(
            {
                "path": request.url.path,
                "query": request.url.query,
                "headers": dict(request.headers),
                "json": payload,
            }
        )

        if not self._queue:
            return JSONResponse(
                {"error": {"message": "No upstream responses queued"}},
                status_code=500,
            )

        response = self._queue.popleft()

        # Handle dynamic response
        if response.response_fn is not None:
            response = response.response_fn(payload)

        request_stream = (
            bool(payload.get("stream")) if isinstance(payload, dict) else False
        )
        is_stream = response.resolve_stream(request_stream)

        if is_stream:
            return StreamingResponse(
                self._stream_events(response),
                status_code=response.status_code,
                headers=response.headers,
                media_type=response.media_type or "text/event-stream",
            )

        body = self._build_body(response)
        return Response(
            content=body,
            status_code=response.status_code,
            headers=response.headers,
            media_type=response.media_type or "application/json",
        )

    async def _stream_events(self, response: UpstreamResponse):
        """Generate SSE events with error/fragmentation support."""
        events = response.stream_events or []
        event_count = 0

        for i, event in enumerate(events):
            # Check for malformed data injection
            if (
                response.inject_malformed_at is not None
                and i == response.inject_malformed_at
            ):
                if response.malformed_data:
                    yield response.malformed_data
                else:
                    yield b"data: {invalid json\n\n"
                continue

            # Check for mid-stream error
            if (
                response.error_after_events is not None
                and event_count >= response.error_after_events
            ):
                error_type = response.error_type or "connection_reset"
                if error_type == "timeout":
                    # Simulate timeout by hanging
                    await asyncio.sleep(60)
                elif error_type == "malformed_sse":
                    yield b"data: {broken\n\ndata: json}\n\n"
                else:  # connection_reset
                    raise StreamError(f"Simulated {error_type}")

            encoded = _encode_sse_event(event)

            # Handle fragmentation
            if response.fragment_events:
                # Split event into multiple chunks
                if response.chunk_sizes:
                    offset = 0
                    for size in response.chunk_sizes:
                        chunk = encoded[offset : offset + size]
                        if chunk:
                            yield chunk
                            if response.chunk_delay_s:
                                await asyncio.sleep(response.chunk_delay_s)
                        offset += size
                    # Remaining bytes
                    if offset < len(encoded):
                        yield encoded[offset:]
                else:
                    # Default: split in half
                    mid = len(encoded) // 2
                    yield encoded[:mid]
                    if response.chunk_delay_s:
                        await asyncio.sleep(response.chunk_delay_s)
                    yield encoded[mid:]
            else:
                yield encoded

            event_count += 1

            if response.chunk_delay_s and not response.fragment_events:
                await asyncio.sleep(response.chunk_delay_s)

        if response.add_done:
            yield _encode_sse_event("[DONE]")

    @staticmethod
    def _build_body(response: UpstreamResponse) -> bytes:
        if response.json_body is not None:
            return json.dumps(response.json_body, ensure_ascii=False).encode("utf-8")
        if isinstance(response.body, str):
            return response.body.encode("utf-8")
        if isinstance(response.body, bytes):
            return response.body
        return b""

    # -------------------------------------------------------------------------
    # Response builders
    # -------------------------------------------------------------------------

    @staticmethod
    def _build_openai_response(
        content: str,
        tool_calls: list[dict[str, Any]] | None,
        finish_reason: str,
        usage: dict[str, int] | None,
        model: str,
    ) -> dict[str, Any]:
        """Build a complete OpenAI chat completion response."""
        message: dict[str, Any] = {"role": "assistant", "content": content}
        if tool_calls:
            message["tool_calls"] = [
                {
                    "id": tc.get("id", f"call_{uuid.uuid4().hex[:8]}"),
                    "type": "function",
                    "function": {
                        "name": tc.get("function", {}).get(
                            "name", tc.get("name", "unknown")
                        ),
                        "arguments": (
                            json.dumps(tc.get("function", {}).get("arguments", {}))
                            if isinstance(
                                tc.get("function", {}).get("arguments"), dict
                            )
                            else tc.get("function", {}).get("arguments", "{}")
                        ),
                    },
                }
                for tc in tool_calls
            ]

        response: dict[str, Any] = {
            "id": f"chatcmpl-{uuid.uuid4().hex[:8]}",
            "object": "chat.completion",
            "model": model,
            "choices": [
                {
                    "index": 0,
                    "message": message,
                    "finish_reason": finish_reason,
                }
            ],
        }
        if usage:
            response["usage"] = usage
        return response

    @staticmethod
    def _build_openai_stream_chunks(
        content: str,
        tool_calls: list[dict[str, Any]] | None,
        finish_reason: str,
        usage: dict[str, int] | None,
        model: str,
    ) -> list[dict[str, Any]]:
        """Build OpenAI streaming chunks."""
        completion_id = f"chatcmpl-{uuid.uuid4().hex[:8]}"
        chunks: list[dict[str, Any]] = []

        # Initial chunk with role
        chunks.append(
            {
                "id": completion_id,
                "object": "chat.completion.chunk",
                "model": model,
                "choices": [
                    {"index": 0, "delta": {"role": "assistant", "content": ""}}
                ],
            }
        )

        # Content chunks (one per character for fine-grained testing)
        for char in content:
            chunks.append(
                {
                    "id": completion_id,
                    "object": "chat.completion.chunk",
                    "model": model,
                    "choices": [{"index": 0, "delta": {"content": char}}],
                }
            )

        # Tool call chunks
        if tool_calls:
            for i, tc in enumerate(tool_calls):
                func = tc.get("function", {})
                name = func.get("name", tc.get("name", "unknown"))
                args = func.get("arguments", {})
                args_str = json.dumps(args) if isinstance(args, dict) else str(args)

                # Tool call start
                chunks.append(
                    {
                        "id": completion_id,
                        "object": "chat.completion.chunk",
                        "model": model,
                        "choices": [
                            {
                                "index": 0,
                                "delta": {
                                    "tool_calls": [
                                        {
                                            "index": i,
                                            "id": tc.get(
                                                "id", f"call_{uuid.uuid4().hex[:8]}"
                                            ),
                                            "type": "function",
                                            "function": {"name": name, "arguments": ""},
                                        }
                                    ]
                                },
                            }
                        ],
                    }
                )
                # Arguments chunk
                chunks.append(
                    {
                        "id": completion_id,
                        "object": "chat.completion.chunk",
                        "model": model,
                        "choices": [
                            {
                                "index": 0,
                                "delta": {
                                    "tool_calls": [
                                        {"index": i, "function": {"arguments": args_str}}
                                    ]
                                },
                            }
                        ],
                    }
                )

        # Final chunk with finish_reason
        final_chunk: dict[str, Any] = {
            "id": completion_id,
            "object": "chat.completion.chunk",
            "model": model,
            "choices": [{"index": 0, "delta": {}, "finish_reason": finish_reason}],
        }
        if usage:
            final_chunk["usage"] = usage
        chunks.append(final_chunk)

        return chunks

    @staticmethod
    def _build_anthropic_response(
        content: list[dict[str, Any]],
        stop_reason: str,
        usage: dict[str, int] | None,
        model: str,
    ) -> dict[str, Any]:
        """Build a complete Anthropic message response."""
        return {
            "id": f"msg_{uuid.uuid4().hex[:16]}",
            "type": "message",
            "role": "assistant",
            "model": model,
            "content": content,
            "stop_reason": stop_reason,
            "stop_sequence": None,
            "usage": usage or {"input_tokens": 10, "output_tokens": 20},
        }

    @staticmethod
    def _build_anthropic_stream_events(
        content: list[dict[str, Any]],
        stop_reason: str,
        usage: dict[str, int] | None,
        model: str,
    ) -> list[dict[str, Any]]:
        """Build Anthropic streaming events."""
        message_id = f"msg_{uuid.uuid4().hex[:16]}"
        events: list[dict[str, Any]] = []

        # message_start
        events.append(
            {
                "type": "message_start",
                "message": {
                    "id": message_id,
                    "type": "message",
                    "role": "assistant",
                    "model": model,
                    "content": [],
                    "stop_reason": None,
                    "stop_sequence": None,
                    "usage": {"input_tokens": usage.get("input_tokens", 10) if usage else 10, "output_tokens": 0},
                },
            }
        )

        # Content blocks
        for i, block in enumerate(content):
            block_type = block.get("type", "text")

            # content_block_start
            if block_type == "text":
                events.append(
                    {
                        "type": "content_block_start",
                        "index": i,
                        "content_block": {"type": "text", "text": ""},
                    }
                )
                # content_block_delta for text
                text = block.get("text", "")
                for char in text:
                    events.append(
                        {
                            "type": "content_block_delta",
                            "index": i,
                            "delta": {"type": "text_delta", "text": char},
                        }
                    )
            elif block_type == "tool_use":
                events.append(
                    {
                        "type": "content_block_start",
                        "index": i,
                        "content_block": {
                            "type": "tool_use",
                            "id": block.get("id", f"toolu_{uuid.uuid4().hex[:16]}"),
                            "name": block.get("name", "unknown"),
                            "input": {},
                        },
                    }
                )
                # input_json_delta
                input_data = block.get("input", {})
                input_str = json.dumps(input_data)
                events.append(
                    {
                        "type": "content_block_delta",
                        "index": i,
                        "delta": {"type": "input_json_delta", "partial_json": input_str},
                    }
                )

            # content_block_stop
            events.append({"type": "content_block_stop", "index": i})

        # message_delta
        events.append(
            {
                "type": "message_delta",
                "delta": {"stop_reason": stop_reason, "stop_sequence": None},
                "usage": {"output_tokens": usage.get("output_tokens", 20) if usage else 20},
            }
        )

        # message_stop
        events.append({"type": "message_stop"})

        return events
