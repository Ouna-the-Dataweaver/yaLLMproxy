"""Fake upstream ASGI app for simulating deterministic responses."""

from __future__ import annotations

import asyncio
import json
from collections import deque
from dataclasses import dataclass, field
from typing import Any, Deque, Iterable, Optional

from fastapi import FastAPI, Request
from fastapi.responses import JSONResponse, Response, StreamingResponse

from ..parsers.response_pipeline import SSEEvent


@dataclass
class UpstreamResponse:
    """A queued response to return from the fake upstream."""

    status_code: int = 200
    headers: dict[str, str] = field(default_factory=dict)
    json_body: dict[str, Any] | None = None
    body: bytes | str | None = None
    stream: bool | None = None
    stream_events: list[Any] | None = None
    media_type: str | None = None
    add_done: bool = True
    chunk_delay_s: float | None = None

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
    """ASGI app that replies with queued responses for /v1/chat/completions."""

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
        self._queue.append(response)

    def clear(self) -> None:
        self._queue.clear()
        self.received.clear()

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
        request_stream = bool(payload.get("stream")) if isinstance(payload, dict) else False
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
        events = response.stream_events or []
        for event in events:
            yield _encode_sse_event(event)
            if response.chunk_delay_s:
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
