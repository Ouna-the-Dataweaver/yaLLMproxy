"""Low-level HTTP client for GigaChat cloud/local backends."""

from __future__ import annotations

import json
import logging
import time
from collections.abc import AsyncIterator, Mapping
from dataclasses import dataclass
from typing import Any
from uuid import uuid4

import httpx

from .config import GigaChatBackendConfig
from ..upstream_transport import get_upstream_transport
from .translator import (
    gigachat_chunk_to_openai,
    gigachat_response_to_openai,
    openai_chat_to_gigachat,
    openai_chat_to_gigachat_tool_emulation,
    openai_response_to_sse,
    parse_tool_emulation_response,
    tool_emulation_result_to_openai_response,
)

logger = logging.getLogger("yallmp-proxy")


@dataclass(slots=True)
class UpstreamError(Exception):
    status_code: int
    body: str


TOOL_EMULATION_ATTEMPTS = 3


class GigaChatHTTPClient:
    """Async HTTP client that speaks native GigaChat API."""

    def __init__(
        self,
        config: GigaChatBackendConfig,
        *,
        http_client: httpx.AsyncClient | None = None,
    ) -> None:
        self.config = config
        self._client = http_client or _make_http_client(config)
        self._owns_client = http_client is None
        self._access_token: str | None = None
        self._access_token_expires_at = 0.0

    async def aclose(self) -> None:
        if self._owns_client:
            await self._client.aclose()

    async def chat_completions(self, payload: Mapping[str, Any]) -> dict[str, Any]:
        request_model = str(payload.get("model") or self.config.model_name)
        if _should_emulate_tools(self.config, payload):
            return await self._chat_completions_with_tool_emulation(
                payload, request_model=request_model
            )

        gigachat_payload = openai_chat_to_gigachat(
            payload, default_model=self.config.model_name
        )
        headers = await self._headers()
        response = await self._client.post(
            "/chat/completions", json=gigachat_payload, headers=headers
        )
        if response.status_code >= 400:
            raise UpstreamError(response.status_code, response.text)
        data = response.json()
        return gigachat_response_to_openai(data, request_model=request_model)

    async def stream_chat_completions(
        self, payload: Mapping[str, Any]
    ) -> AsyncIterator[str]:
        request_model = str(payload.get("model") or self.config.model_name)
        if _should_emulate_tools(self.config, payload):
            response = await self._chat_completions_with_tool_emulation(
                payload, request_model=request_model
            )
            for chunk in openai_response_to_sse(response):
                yield chunk
            return

        gigachat_payload = openai_chat_to_gigachat(
            payload, default_model=self.config.model_name
        )
        gigachat_payload["stream"] = True
        headers = await self._headers()
        headers["Accept"] = "text/event-stream"

        async with self._client.stream(
            "POST", "/chat/completions", json=gigachat_payload, headers=headers
        ) as response:
            if response.status_code >= 400:
                body = (await response.aread()).decode(errors="replace")
                raise UpstreamError(response.status_code, body)
            async for data in _iter_sse_data(response):
                if data == "[DONE]":
                    yield "data: [DONE]\n\n"
                    return
                chunk = gigachat_chunk_to_openai(
                    json.loads(data), request_model=request_model
                )
                yield f"data: {json.dumps(chunk, ensure_ascii=False)}\n\n"
            yield "data: [DONE]\n\n"

    async def _chat_completions_with_tool_emulation(
        self, payload: Mapping[str, Any], *, request_model: str
    ) -> dict[str, Any]:
        headers = await self._headers()
        retry_error: str | None = None
        last_error: Exception | None = None
        for attempt in range(1, TOOL_EMULATION_ATTEMPTS + 1):
            gigachat_payload = openai_chat_to_gigachat_tool_emulation(
                payload,
                default_model=self.config.model_name,
                retry_error=retry_error,
            )
            response = await self._client.post(
                "/chat/completions", json=gigachat_payload, headers=headers
            )
            if response.status_code >= 400:
                raise UpstreamError(response.status_code, response.text)

            data: Mapping[str, Any] | None = None
            content: str | None = None
            try:
                data = response.json()
                content = _extract_first_message_content(data)
                result = parse_tool_emulation_response(content, payload.get("tools"))
                usage = data.get("usage") if isinstance(data.get("usage"), Mapping) else None
                return tool_emulation_result_to_openai_response(
                    result, request_model=request_model, usage=usage
                )
            except (json.JSONDecodeError, ValueError) as exc:
                last_error = exc
                retry_error = str(exc)
                logger.debug(
                    "GigaChat tool emulation attempt %d failed parsing: %s", attempt, exc
                )

        raise UpstreamError(
            502,
            f"GigaChat tool emulation failed after {TOOL_EMULATION_ATTEMPTS} attempts: {last_error}",
        )

    async def _headers(self) -> dict[str, str]:
        if self.config.mode != "cloud":
            return {}
        return {"Authorization": f"Bearer {await self._get_access_token()}"}

    async def _get_access_token(self) -> str:
        if self._access_token and time.time() < self._access_token_expires_at - 30:
            return self._access_token
        if not self.config.api_key:
            raise RuntimeError("api_key is required for GigaChat cloud mode.")

        response = await self._client.post(
            self.config.auth_url,
            data={"scope": self.config.scope},
            headers={
                "Accept": "application/json",
                "Content-Type": "application/x-www-form-urlencoded",
                "RqUID": str(uuid4()),
                "Authorization": f"Basic {self.config.api_key}",
            },
        )
        if response.status_code >= 400:
            raise UpstreamError(response.status_code, response.text)
        data = response.json()
        self._access_token = data["access_token"]
        self._access_token_expires_at = _normalize_expires_at(data.get("expires_at"))
        return self._access_token


def _make_http_client(config: GigaChatBackendConfig) -> httpx.AsyncClient:
    cert: str | tuple[str, str] | None = None
    if config.client_cert_file and config.client_key_file:
        cert = (config.client_cert_file, config.client_key_file)
    elif config.client_cert_file:
        cert = config.client_cert_file
    transport = get_upstream_transport(config.base_url)
    return httpx.AsyncClient(
        base_url=config.base_url.rstrip("/"),
        timeout=config.timeout,
        verify=config.verify_ssl,
        cert=cert,
        transport=transport,
    )


def _should_emulate_tools(
    config: GigaChatBackendConfig, payload: Mapping[str, Any]
) -> bool:
    tools = payload.get("tools")
    return (
        config.emulate_tool_calls
        and payload.get("tool_choice") != "none"
        and isinstance(tools, list)
        and bool(tools)
    )


def _extract_first_message_content(data: Mapping[str, Any]) -> str:
    choices = data.get("choices")
    if not isinstance(choices, list) or not choices:
        raise ValueError("GigaChat response does not contain choices.")
    first_choice = choices[0]
    if not isinstance(first_choice, Mapping):
        raise ValueError("GigaChat response choice must be an object.")
    message = first_choice.get("message")
    if not isinstance(message, Mapping):
        raise ValueError("GigaChat response choice does not contain message.")
    content = message.get("content")
    if not isinstance(content, str):
        raise ValueError("GigaChat response message content must be a string.")
    return content


async def _iter_sse_data(response: httpx.Response) -> AsyncIterator[str]:
    data_lines: list[str] = []
    async for line in response.aiter_lines():
        line = line.rstrip("\r")
        if not line:
            if data_lines:
                yield "\n".join(data_lines)
                data_lines = []
            continue
        if line.startswith(":"):
            continue
        if line.startswith("data:"):
            data_lines.append(line[5:].lstrip())
    if data_lines:
        yield "\n".join(data_lines)


def _normalize_expires_at(value: Any) -> float:
    if isinstance(value, (int, float)):
        timestamp = float(value)
        if timestamp > 10_000_000_000:
            timestamp /= 1000
        return timestamp
    return time.time() + 25 * 60
