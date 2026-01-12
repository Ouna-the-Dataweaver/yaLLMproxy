"""Tests for HTTP forwarder behavior."""

from __future__ import annotations

from contextlib import asynccontextmanager

import httpx
import pytest
from fastapi import FastAPI, Request
from fastapi.responses import PlainTextResponse, Response, StreamingResponse

from src.http_forwarder import ForwarderSettings, forward_request


def _build_forwarder_app(
    upstream_app: FastAPI,
    *,
    preserve_host: bool = True,
    target_base: str = "http://target.local:1234",
) -> FastAPI:
    app = FastAPI()
    app.state.forwarder_settings = ForwarderSettings(
        listen_host="0.0.0.0",
        listen_port=6969,
        target_scheme="http",
        target_host="target.local",
        target_port=1234,
        preserve_host=preserve_host,
        timeout_seconds=None,
    )
    app.state.forwarder_client = httpx.AsyncClient(
        transport=httpx.ASGITransport(app=upstream_app),
        base_url=target_base,
    )
    app.api_route(
        "/{path:path}",
        methods=["GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS", "HEAD"],
    )(forward_request)
    return app


@asynccontextmanager
async def _forwarder_client(app: FastAPI):
    async with httpx.AsyncClient(
        transport=httpx.ASGITransport(app=app),
        base_url="http://forwarder.local",
    ) as client:
        yield client
    await app.state.forwarder_client.aclose()


@pytest.mark.asyncio
async def test_preserve_host_forwards_original_host_header() -> None:
    upstream = FastAPI()

    @upstream.get("/echo-host")
    async def echo_host(request: Request) -> PlainTextResponse:
        return PlainTextResponse(request.headers.get("host", ""))

    app = _build_forwarder_app(upstream, preserve_host=True)

    async with _forwarder_client(app) as client:
        response = await client.get("/echo-host", headers={"Host": "public.test"})

    assert response.text == "public.test"


@pytest.mark.asyncio
async def test_strip_host_header_when_preserve_host_disabled() -> None:
    upstream = FastAPI()

    @upstream.get("/echo-host")
    async def echo_host(request: Request) -> PlainTextResponse:
        return PlainTextResponse(request.headers.get("host", ""))

    app = _build_forwarder_app(upstream, preserve_host=False)

    async with _forwarder_client(app) as client:
        response = await client.get("/echo-host", headers={"Host": "public.test"})

    assert response.text == "target.local:1234"


@pytest.mark.asyncio
async def test_connection_header_overrides_are_removed() -> None:
    upstream = FastAPI()

    @upstream.get("/headers")
    async def headers(request: Request) -> Response:
        value = request.headers.get("x-test")
        return Response(content=value or "", media_type="text/plain")

    app = _build_forwarder_app(upstream, preserve_host=True)

    async with _forwarder_client(app) as client:
        response = await client.get(
            "/headers",
            headers={
                "Connection": "X-Test",
                "X-Test": "secret",
            },
        )

    assert response.text == ""


@pytest.mark.asyncio
async def test_non_stream_response_strips_encoding_and_length() -> None:
    upstream = FastAPI()
    import gzip

    raw = b"hello"
    compressed = gzip.compress(raw, mtime=0)

    @upstream.get("/payload")
    async def payload() -> Response:
        return Response(
            content=compressed,
            media_type="application/json",
            headers={
                "content-encoding": "gzip",
                "content-length": str(len(compressed)),
            },
        )

    app = _build_forwarder_app(upstream, preserve_host=True)

    async with _forwarder_client(app) as client:
        response = await client.get("/payload")

    assert response.headers.get("content-encoding") is None
    assert response.headers.get("content-length") != str(len(compressed))
    assert response.content == raw


@pytest.mark.asyncio
async def test_event_stream_is_forwarded_without_buffering() -> None:
    upstream = FastAPI()

    async def event_stream():
        yield b"data: one\n\n"
        yield b"data: two\n\n"

    @upstream.get("/events")
    async def events() -> StreamingResponse:
        return StreamingResponse(event_stream(), media_type="text/event-stream")

    app = _build_forwarder_app(upstream, preserve_host=True)

    async with _forwarder_client(app) as client:
        response = await client.get("/events")

    assert "text/event-stream" in response.headers.get("content-type", "")
    assert "data: one" in response.text
    assert "data: two" in response.text
