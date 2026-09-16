"""Tests for router streaming request edge cases."""

import json
import sys
from collections.abc import AsyncIterator
from pathlib import Path
from unittest.mock import Mock

import httpx
import pytest

# Add src directory to path
sys.path.insert(0, str(Path(__file__).parent.parent / "src"))

from src.core import router as router_module, sse as sse_module
from src.core.upstream_transport import register_upstream_transport
from src.parsers.response_pipeline import ParserContext, ResponseParserPipeline


class _ChunkedStream(httpx.AsyncByteStream):
    def __init__(self, chunks: list[bytes]) -> None:
        self._chunks = chunks
        self.closed = False

    async def __aiter__(self) -> AsyncIterator[bytes]:
        for chunk in self._chunks:
            yield chunk

    async def aclose(self) -> None:
        self.closed = True


class _BaseStubClient:
    last_instance = None

    def __init__(self, *args, **kwargs):
        self.closed = False
        type(self).last_instance = self

    async def aclose(self):
        self.closed = True

    async def __aenter__(self):
        return self

    async def __aexit__(self, exc_type, exc, tb):
        await self.aclose()
        return False


class _BuildRequestErrorClient(_BaseStubClient):
    def build_request(self, *args, **kwargs):
        raise RuntimeError("build_request failed")


class _SendErrorClient(_BaseStubClient):
    def build_request(self, method, url, headers=None, content=None):
        return httpx.Request(method, url, headers=headers, content=content)

    async def send(self, request, stream=False):
        raise RuntimeError("send failed")


@pytest.mark.asyncio
async def test_streaming_request_closes_client_when_send_fails(monkeypatch):
    monkeypatch.setattr(router_module.httpx, "AsyncClient", _SendErrorClient)

    with pytest.raises(RuntimeError, match="send failed"):
        await router_module._streaming_request(
            url="http://example.com/v1/stream",
            headers={"content-type": "application/json"},
            body=b"{}",
            timeout=1.0,
        )

    client = _SendErrorClient.last_instance
    assert client is not None
    assert client.closed is True


@pytest.mark.asyncio
async def test_streaming_request_closes_client_when_build_request_fails(monkeypatch):
    monkeypatch.setattr(router_module.httpx, "AsyncClient", _BuildRequestErrorClient)

    with pytest.raises(RuntimeError, match="build_request failed"):
        await router_module._streaming_request(
            url="http://example.com/v1/stream",
            headers={"content-type": "application/json"},
            body=b"{}",
            timeout=1.0,
        )

    client = _BuildRequestErrorClient.last_instance
    assert client is not None
    assert client.closed is True


@pytest.mark.asyncio
@pytest.mark.parametrize("buffer_size", [1, 4096], ids=["live", "buffered"])
@pytest.mark.parametrize("finish_reason", ["stop", "length", "tool_calls"])
@pytest.mark.parametrize("with_request_log", [False, True])
async def test_streaming_request_forwards_usage_after_finish_reason(
    monkeypatch,
    clear_transport_registry,
    buffer_size: int,
    finish_reason: str,
    with_request_log: bool,
) -> None:
    chunks = [
        b'data: {"choices":[{"index":0,"delta":{"content":"test"},"finish_reason":null}]}\n\n',
        (
            "data: "
            + json.dumps(
                {"choices": [{"index": 0, "delta": {}, "finish_reason": finish_reason}]}
            )
            + "\n\n"
        ).encode(),
        b'data: {"choices":[],"usage":{"prompt_tokens":12,"completion_tokens":1,"total_tokens":13}}\n\n',
        b"data: [DONE]\n\n",
    ]
    upstream_stream = _ChunkedStream(chunks)

    async def handler(_request: httpx.Request) -> httpx.Response:
        return httpx.Response(
            200,
            headers={"content-type": "text/event-stream"},
            stream=upstream_stream,
        )

    register_upstream_transport("upstream.local", httpx.MockTransport(handler))
    monkeypatch.setattr(sse_module, "STREAM_ERROR_CHECK_BUFFER_SIZE", buffer_size)
    request_log = Mock()
    request_log.finalized = False
    request_log._accumulated_response_parts = []

    response = await router_module._streaming_request(
        url="http://upstream.local/v1/chat/completions",
        headers={"content-type": "application/json"},
        body=b"{}",
        timeout=1.0,
        request_log=request_log if with_request_log else None,
        parser_pipeline=ResponseParserPipeline([], []),
        parser_context=ParserContext(
            path="/v1/chat/completions",
            model="test-model",
            backend="test-backend",
            is_stream=True,
        ),
    )

    body = b"".join([chunk async for chunk in response.body_iterator])

    payloads = sse_module.SSEJSONDecoder().feed(body)
    usage = {"prompt_tokens": 12, "completion_tokens": 1, "total_tokens": 13}
    assert payloads[-1]["usage"] == usage
    assert payloads[-2]["choices"][0]["finish_reason"] == finish_reason
    assert body.count(b"[DONE]") == 1
    assert body.endswith(b"data: [DONE]\n\n")
    assert upstream_stream.closed
    if with_request_log:
        request_log.record_usage_stats.assert_called_once_with(usage)
