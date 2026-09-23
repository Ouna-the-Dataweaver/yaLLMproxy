from __future__ import annotations

import asyncio
import json
from collections.abc import AsyncIterator, Callable
from unittest.mock import AsyncMock

import httpx
import pytest
from fastapi import FastAPI
from openai import AsyncOpenAI, RateLimitError
from src.api.routes.chat import _wrap_stream_with_slot_release
from src.core import backend
from src.core.gigachat import client as gigachat_client
from src.core.router import ProxyRouter


def _chunk(text: str) -> bytes:
    data = {
        "id": "response-1",
        "choices": [{"index": 0, "delta": {"content": text}, "finish_reason": None}],
    }
    return f"data: {json.dumps(data)}\n\n".encode()


def _router(
    monkeypatch: pytest.MonkeyPatch,
    handler: Callable[[httpx.Request], httpx.Response],
) -> tuple[ProxyRouter, list[httpx.AsyncClient]]:
    clients: list[httpx.AsyncClient] = []

    def make_client(config):
        client = httpx.AsyncClient(
            base_url=config.base_url,
            transport=httpx.MockTransport(handler),
        )
        clients.append(client)
        return client

    monkeypatch.setattr(gigachat_client, "_make_http_client", make_client)
    monkeypatch.setattr(backend, "DEFAULT_RETRY_DELAY", 0)
    return ProxyRouter(
        {
            "model_list": [
                {
                    "model_name": "giga",
                    "model_params": {
                        "api_type": "gigachat",
                        "mode": "cloud",
                        "model": "GigaChat-2-Max",
                        "api_base": "https://gigachat.example/api/v1",
                        "auth_url": "https://auth.example/oauth",
                        "api_key": "fake-key",
                        "scope": "GIGACHAT_API_CORP",
                    },
                }
            ],
            "router_settings": {"num_retries": 3},
        }
    ), clients


async def _request(router: ProxyRouter):
    payload = {
        "model": "giga",
        "stream": True,
        "messages": [{"role": "user", "content": "Hello"}],
    }
    return await router.forward_request(
        model_name="giga",
        path="/v1/chat/completions",
        query="",
        body=json.dumps(payload).encode(),
        payload=payload,
        is_stream=True,
        headers={"content-type": "application/json"},
    )


def _token() -> httpx.Response:
    return httpx.Response(
        200, json={"access_token": "fake", "expires_at": 4_102_444_800_000}
    )


@pytest.mark.parametrize("stage", ["oauth", "chat/completions"])
@pytest.mark.parametrize("status", [429, 503])
async def test_gigachat_stream_retries_before_first_chunk(monkeypatch, stage, status):
    attempts = 0

    def handler(request):
        nonlocal attempts
        if request.url.path.endswith(stage):
            attempts += 1
            if attempts == 1:
                return httpx.Response(
                    status, json={"status": status, "message": "Try again"}
                )
        if request.url.path == "/oauth":
            return _token()
        return httpx.Response(200, content=_chunk("Hello") + b"data: [DONE]\n\n")

    router, clients = _router(monkeypatch, handler)
    response = await _request(router)
    body = b"".join([chunk async for chunk in response.body_iterator])

    assert attempts == 2
    assert response.status_code == 200
    assert body.count(b'"content": "Hello"') == 1
    assert b'"error"' not in body
    assert body.endswith(b"data: [DONE]\n\n")
    assert all(client.is_closed for client in clients)


@pytest.mark.parametrize("status, expected_attempts", [(429, 3), (422, 1)])
async def test_gigachat_stream_preserves_http_error_after_attempts(
    monkeypatch, status, expected_attempts
):
    attempts = 0
    error = {"status": status, "message": "Upstream rejected request"}

    def handler(request):
        nonlocal attempts
        if request.url.path == "/oauth":
            return _token()
        attempts += 1
        return httpx.Response(status, json=error)

    router, clients = _router(monkeypatch, handler)
    response = await _request(router)

    # Failed streaming startup must remain an HTTP error, not HTTP-200 SSE.
    assert response.status_code == status
    assert attempts == expected_attempts
    body = json.loads(response.body)
    assert body["error"]["code"] == status
    assert json.loads(body["error"]["message"]) == error
    assert all(client.is_closed for client in clients)


async def test_gigachat_stream_retries_transport_failure_before_first_chunk(
    monkeypatch,
):
    attempts = 0

    def handler(request):
        nonlocal attempts
        if request.url.path == "/oauth":
            return _token()
        attempts += 1
        if attempts == 1:
            raise httpx.ReadTimeout("No response", request=request)
        return httpx.Response(200, content=_chunk("Recovered") + b"data: [DONE]\n\n")

    router, clients = _router(monkeypatch, handler)
    response = await _request(router)
    body = b"".join([chunk async for chunk in response.body_iterator])

    assert attempts == 2
    assert b"Recovered" in body
    assert all(client.is_closed for client in clients)


async def test_gigachat_stream_never_replays_after_output(monkeypatch):
    attempts = 0
    closed = False

    class InterruptedStream(httpx.AsyncByteStream):
        async def __aiter__(self) -> AsyncIterator[bytes]:
            yield _chunk("Partial")
            raise httpx.ReadError("Connection lost")

        async def aclose(self) -> None:
            nonlocal closed
            closed = True

    def handler(request):
        nonlocal attempts
        if request.url.path == "/oauth":
            return _token()
        attempts += 1
        return httpx.Response(200, stream=InterruptedStream())

    router, clients = _router(monkeypatch, handler)
    response = await _request(router)
    chunks = []
    with pytest.raises(httpx.ReadError, match="Connection lost"):
        async for chunk in response.body_iterator:
            chunks.append(chunk)

    assert b"".join(chunks).count(b'"content": "Partial"') == 1
    assert attempts == 1
    assert closed
    assert all(client.is_closed for client in clients)


async def test_gigachat_stream_closes_on_cancel_during_startup(monkeypatch):
    reading = asyncio.Event()
    closed = False

    class WaitingStream(httpx.AsyncByteStream):
        async def __aiter__(self) -> AsyncIterator[bytes]:
            reading.set()
            await asyncio.Event().wait()
            yield b"unreachable"

        async def aclose(self) -> None:
            nonlocal closed
            closed = True

    def handler(request):
        if request.url.path == "/oauth":
            return _token()
        return httpx.Response(200, stream=WaitingStream())

    router, clients = _router(monkeypatch, handler)

    async def consume():
        response = await _request(router)
        async for _ in response.body_iterator:
            pass

    task = asyncio.create_task(consume())
    try:
        await asyncio.wait_for(reading.wait(), timeout=2)
    finally:
        task.cancel()
        with pytest.raises(asyncio.CancelledError):
            await task

    assert closed
    assert all(client.is_closed for client in clients)


async def test_gigachat_stream_exhausted_retries_raise_sdk_rate_limit_error(
    monkeypatch,
):
    attempts = 0

    def handler(request):
        nonlocal attempts
        if request.url.path == "/oauth":
            return _token()
        attempts += 1
        return httpx.Response(429, json={"status": 429, "message": "Too Many Requests"})

    router, clients = _router(monkeypatch, handler)
    app = FastAPI()

    @app.post("/v1/chat/completions")
    async def chat():
        return await _request(router)

    async with AsyncOpenAI(
        api_key="fake",
        base_url="http://proxy/v1",
        max_retries=0,
        http_client=httpx.AsyncClient(transport=httpx.ASGITransport(app=app)),
    ) as client:
        with pytest.raises(RateLimitError) as error:
            await client.chat.completions.create(
                model="giga",
                messages=[{"role": "user", "content": "Hello"}],
                stream=True,
            )

    assert error.value.status_code == 429
    assert attempts == 3
    assert all(client.is_closed for client in clients)


async def test_gigachat_primed_stream_cleanup_survives_slot_wrapper(monkeypatch):
    closed = False

    class UpstreamStream(httpx.AsyncByteStream):
        async def __aiter__(self) -> AsyncIterator[bytes]:
            yield _chunk("Hello")
            yield b"data: [DONE]\n\n"

        async def aclose(self) -> None:
            nonlocal closed
            closed = True

    def handler(request):
        if request.url.path == "/oauth":
            return _token()
        return httpx.Response(200, stream=UpstreamStream())

    router, clients = _router(monkeypatch, handler)
    response = await _request(router)
    assert not closed
    wrapped = _wrap_stream_with_slot_release(response, AsyncMock())
    # A disconnect can skip consuming the primed body. Response cleanup must
    # survive the route's concurrency wrapper and close the open upstream.
    assert wrapped.background is not None
    await wrapped.background()

    assert closed
    assert all(client.is_closed for client in clients)
