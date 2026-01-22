"""Proxy harness for in-process simulation tests."""

from __future__ import annotations

import asyncio
from typing import TYPE_CHECKING, Any, Mapping, Optional

import httpx
from fastapi import FastAPI

from ..core import ProxyRouter
from ..core.registry import get_router, set_router
from ..api.routes import chat_completions, responses, messages_endpoint

if TYPE_CHECKING:
    from ..concurrency import ConcurrencyManager
    from ..concurrency.manager import ConcurrencyMetrics


class ProxyHarness:
    """Build a minimal proxy app wired to a provided config.

    Features:
    - Creates an in-process proxy app
    - Manages global router state
    - Supports chat completions, responses, and messages endpoints
    - Optional concurrency management integration
    - Client disconnect simulation for testing

    Usage:
        with ProxyHarness(config) as proxy:
            async with proxy.make_async_client() as client:
                response = await client.post("/v1/chat/completions", json={...})
    """

    def __init__(
        self,
        config: Mapping[str, Any],
        *,
        enable_responses_endpoint: bool = False,
        enable_messages_endpoint: bool = False,
        enable_concurrency: bool = False,
        reset_concurrency: bool = True,
    ) -> None:
        """Initialize the proxy harness.

        Args:
            config: Proxy configuration dict
            enable_responses_endpoint: Enable /v1/responses endpoint
            enable_messages_endpoint: Enable /v1/messages endpoint
            enable_concurrency: Enable concurrency manager integration
            reset_concurrency: Reset concurrency manager state on close
        """
        self.router = ProxyRouter(dict(config))
        self._previous_router: Optional[Any] = None
        self._enable_concurrency = enable_concurrency
        self._reset_concurrency = reset_concurrency

        try:
            self._previous_router = get_router()
        except Exception:
            self._previous_router = None
        set_router(self.router)

        # Reset concurrency manager if requested
        if reset_concurrency:
            from ..concurrency import reset_concurrency_manager

            reset_concurrency_manager()

        self.app = FastAPI(title="ProxyHarness")
        self.app.post("/v1/chat/completions")(chat_completions)
        if enable_responses_endpoint:
            self.app.post("/v1/responses")(responses)
        if enable_messages_endpoint:
            self.app.post("/v1/messages")(messages_endpoint)

    def close(self) -> None:
        """Clean up harness state."""
        if self._previous_router is not None:
            set_router(self._previous_router)
        else:
            set_router(None)

        # Reset concurrency manager on close
        if self._reset_concurrency:
            from ..concurrency import reset_concurrency_manager

            reset_concurrency_manager()

    def __enter__(self) -> "ProxyHarness":
        return self

    def __exit__(self, exc_type, exc, tb) -> None:
        self.close()

    async def __aenter__(self) -> "ProxyHarness":
        return self

    async def __aexit__(self, exc_type, exc, tb) -> None:
        self.close()

    def make_async_client(
        self, base_url: str = "http://proxy.local"
    ) -> httpx.AsyncClient:
        """Create an async HTTP client for the proxy.

        Args:
            base_url: Base URL for requests

        Returns:
            AsyncClient configured to talk to this proxy
        """
        return httpx.AsyncClient(
            transport=httpx.ASGITransport(app=self.app),
            base_url=base_url,
        )

    # -------------------------------------------------------------------------
    # Concurrency integration
    # -------------------------------------------------------------------------

    def get_concurrency_manager(self) -> "ConcurrencyManager":
        """Get the concurrency manager instance.

        Returns:
            The singleton ConcurrencyManager

        Raises:
            RuntimeError: If concurrency is not enabled
        """
        from ..concurrency import get_concurrency_manager

        return get_concurrency_manager()

    async def get_concurrency_metrics(self) -> "ConcurrencyMetrics":
        """Get current concurrency metrics snapshot.

        Returns:
            ConcurrencyMetrics with current state
        """
        manager = self.get_concurrency_manager()
        return await manager.get_metrics()

    async def reset_concurrency_stats(self) -> None:
        """Reset concurrency statistics (for clean test state)."""
        manager = self.get_concurrency_manager()
        await manager.reset_stats()

    # -------------------------------------------------------------------------
    # Client disconnect simulation
    # -------------------------------------------------------------------------

    async def make_disconnecting_request(
        self,
        path: str,
        json: dict[str, Any],
        disconnect_after_ms: int,
        *,
        base_url: str = "http://proxy.local",
        headers: dict[str, str] | None = None,
    ) -> tuple[httpx.Response | None, bool, float]:
        """Make a request that simulates client disconnect after a delay.

        Args:
            path: Request path (e.g., "/v1/chat/completions")
            json: Request JSON body
            disconnect_after_ms: Milliseconds before simulating disconnect
            base_url: Base URL for the request
            headers: Optional request headers

        Returns:
            Tuple of (response or None, was_disconnected, elapsed_ms)
        """
        import time

        start = time.monotonic()
        response: httpx.Response | None = None
        was_disconnected = False

        async with self.make_async_client(base_url) as client:
            try:
                # Use asyncio.wait_for to simulate timeout/disconnect
                response = await asyncio.wait_for(
                    client.post(path, json=json, headers=headers),
                    timeout=disconnect_after_ms / 1000.0,
                )
            except asyncio.TimeoutError:
                was_disconnected = True

        elapsed_ms = (time.monotonic() - start) * 1000
        return response, was_disconnected, elapsed_ms

    async def make_streaming_request_with_disconnect(
        self,
        path: str,
        json: dict[str, Any],
        disconnect_after_chunks: int,
        *,
        base_url: str = "http://proxy.local",
        headers: dict[str, str] | None = None,
    ) -> tuple[list[bytes], bool]:
        """Make a streaming request that disconnects after N chunks.

        Args:
            path: Request path
            json: Request JSON body
            disconnect_after_chunks: Number of chunks to receive before disconnect
            base_url: Base URL
            headers: Optional headers

        Returns:
            Tuple of (received_chunks, was_disconnected)
        """
        chunks: list[bytes] = []
        was_disconnected = False

        async with self.make_async_client(base_url) as client:
            try:
                async with client.stream("POST", path, json=json, headers=headers) as response:
                    chunk_count = 0
                    async for chunk in response.aiter_raw():
                        chunks.append(chunk)
                        chunk_count += 1
                        if chunk_count >= disconnect_after_chunks:
                            was_disconnected = True
                            break
            except Exception:
                was_disconnected = True

        return chunks, was_disconnected
