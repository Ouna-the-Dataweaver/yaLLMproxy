"""GigaChat backend adapter for yaLLMproxy's ProxyRouter."""

from __future__ import annotations

import json
import logging
from collections.abc import AsyncIterator, Mapping
from typing import Any

from fastapi import Response
from fastapi.responses import StreamingResponse
from starlette.background import BackgroundTask

from .client import GigaChatHTTPClient, UpstreamError
from .config import GigaChatBackendConfig

logger = logging.getLogger("yallmp-proxy")


class GigaChatBackendAdapter:
    """Adapter that lets ProxyRouter speak directly to GigaChat."""

    def __init__(self, config: GigaChatBackendConfig) -> None:
        self.config = config
        self._client = GigaChatHTTPClient(config)
        self._closed = False

    async def aclose(self) -> None:
        if not self._closed:
            self._closed = True
            await self._client.aclose()

    async def request(
        self,
        *,
        payload: Mapping[str, Any],
        is_stream: bool,
    ) -> Response | StreamingResponse:
        """Execute a request and return either a plain or streaming response.

        The returned object is already in OpenAI-compatible format.
        """
        if is_stream:
            return await self._stream_response(payload)
        return await self._plain_response(payload)

    async def _plain_response(self, payload: Mapping[str, Any]) -> Response:
        try:
            openai_response = await self._client.chat_completions(payload)
        except UpstreamError as exc:
            return self._error_response(payload, exc, is_stream=False)
        body = json.dumps(openai_response, ensure_ascii=False).encode("utf-8")
        return Response(
            content=body,
            status_code=200,
            media_type="application/json",
        )

    async def _stream_response(self, payload: Mapping[str, Any]) -> Response:
        stream = self._client.stream_chat_completions(payload)

        async def close_stream() -> None:
            try:
                await stream.aclose()
            finally:
                await self.aclose()

        # Start the upstream request inside the router's retry boundary. Once
        # StreamingResponse is returned, HTTP status is committed downstream
        # and retrying could duplicate already delivered model/tool output.
        try:
            first_chunk = await anext(stream, None)
        except UpstreamError as exc:
            await close_stream()
            return self._error_response(payload, exc, is_stream=True)
        except BaseException:
            # Includes cancellation and transport errors during stream startup.
            await close_stream()
            raise

        async def iterator() -> AsyncIterator[bytes]:
            try:
                if first_chunk is not None:
                    yield first_chunk.encode("utf-8")
                async for chunk in stream:
                    yield chunk.encode("utf-8")
            except UpstreamError as exc:
                error = self._error_response(payload, exc, is_stream=True)
                yield b"data: " + error.body + b"\n\n"
                yield b"data: [DONE]\n\n"
            finally:
                await close_stream()

        return StreamingResponse(
            iterator(),
            media_type="text/event-stream",
            headers={"Cache-Control": "no-cache", "Connection": "keep-alive"},
            background=BackgroundTask(close_stream),
        )

    def _error_response(
        self, payload: Mapping[str, Any], exc: UpstreamError, *, is_stream: bool
    ) -> Response:
        logger.error(
            "GigaChat request failed (model=%s, stream=%s, status=%s): %s",
            payload.get("model") or self.config.model_name,
            is_stream,
            exc.status_code,
            exc.body,
        )
        body = {
            "error": {
                "message": exc.body,
                "type": "upstream_error",
                "code": exc.status_code,
            }
        }
        return Response(
            content=json.dumps(body, ensure_ascii=False).encode("utf-8"),
            status_code=exc.status_code,
            media_type="application/json",
        )

    @staticmethod
    def build_url(base_url: str, path: str) -> str:
        """Build upstream URL. GigaChat backend ignores /v1 prefix handling."""
        base = base_url.rstrip("/")
        normalized_path = path or ""
        if normalized_path.startswith("/v1"):
            normalized_path = normalized_path[len("/v1") :]
        if not normalized_path.startswith("/"):
            normalized_path = f"/{normalized_path}"
        return f"{base}{normalized_path}"
