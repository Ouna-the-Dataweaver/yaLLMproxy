"""Tests for router streaming request edge cases."""

import sys
from pathlib import Path

import httpx
import pytest

# Add src directory to path
sys.path.insert(0, str(Path(__file__).parent.parent / "src"))

from src.core import router as router_module


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
