"""Proxy harness for in-process simulation tests."""

from __future__ import annotations

from typing import Any, Mapping, Optional

import httpx
from fastapi import FastAPI

from ..core import ProxyRouter
from ..core.registry import get_router, set_router
from ..api.routes import chat_completions, responses, messages_endpoint


class ProxyHarness:
    """Build a minimal proxy app wired to a provided config."""

    def __init__(
        self,
        config: Mapping[str, Any],
        *,
        enable_responses_endpoint: bool = False,
        enable_messages_endpoint: bool = False,
    ) -> None:
        self.router = ProxyRouter(dict(config))
        self._previous_router: Optional[Any] = None
        try:
            self._previous_router = get_router()
        except Exception:
            self._previous_router = None
        set_router(self.router)

        self.app = FastAPI(title="ProxyHarness")
        self.app.post("/v1/chat/completions")(chat_completions)
        if enable_responses_endpoint:
            self.app.post("/v1/responses")(responses)
        if enable_messages_endpoint:
            self.app.post("/v1/messages")(messages_endpoint)

    def close(self) -> None:
        if self._previous_router is not None:
            set_router(self._previous_router)
        else:
            set_router(None)

    def __enter__(self) -> "ProxyHarness":
        return self

    def __exit__(self, exc_type, exc, tb) -> None:
        self.close()

    def make_async_client(self, base_url: str = "http://proxy.local") -> httpx.AsyncClient:
        return httpx.AsyncClient(
            transport=httpx.ASGITransport(app=self.app),
            base_url=base_url,
        )
