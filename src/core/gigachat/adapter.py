"""GigaChat backend adapter for yaLLMproxy's ProxyRouter."""

from __future__ import annotations

import json
import logging
from collections.abc import AsyncIterator, Mapping
from typing import Any

from fastapi import Response
from fastapi.responses import StreamingResponse

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
            error_body = json.dumps(
                {
                    "error": {
                        "message": exc.body,
                        "type": "upstream_error",
                        "code": exc.status_code,
                    }
                },
                ensure_ascii=False,
            ).encode("utf-8")
            return Response(
                content=error_body,
                status_code=exc.status_code,
                media_type="application/json",
            )
        body = json.dumps(openai_response, ensure_ascii=False).encode("utf-8")
        return Response(
            content=body,
            status_code=200,
            media_type="application/json",
        )

    async def _stream_response(
        self, payload: Mapping[str, Any]
    ) -> StreamingResponse:
        async def iterator() -> AsyncIterator[bytes]:
            try:
                async for chunk in self._client.stream_chat_completions(payload):
                    yield chunk.encode("utf-8")
            except UpstreamError as exc:
                error = {
                    "error": {
                        "message": exc.body,
                        "type": "upstream_error",
                        "code": exc.status_code,
                    }
                }
                yield f"data: {json.dumps(error, ensure_ascii=False)}\n\n".encode("utf-8")
                yield b"data: [DONE]\n\n"
            finally:
                await self.aclose()

        return StreamingResponse(
            iterator(),
            media_type="text/event-stream",
            headers={"Cache-Control": "no-cache", "Connection": "keep-alive"},
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
