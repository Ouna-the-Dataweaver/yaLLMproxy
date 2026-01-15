"""Registry for per-host HTTPX transports (test/in-process upstreams)."""

from __future__ import annotations

import logging
from typing import Optional
from urllib.parse import urlparse

import httpx

logger = logging.getLogger("yallmp-proxy")

_TRANSPORTS: dict[str, httpx.AsyncBaseTransport] = {}


def _normalize_host(host: str) -> str:
    return host.strip().lower()


def register_upstream_transport(host: str, transport: httpx.AsyncBaseTransport) -> None:
    """Register a transport for a host (netloc, e.g. 'upstream.local:8000')."""
    if not host:
        raise ValueError("host is required")
    normalized = _normalize_host(host)
    _TRANSPORTS[normalized] = transport
    logger.debug("Registered upstream transport for host '%s'", normalized)


def register_upstream_transport_for_url(
    url: str, transport: httpx.AsyncBaseTransport
) -> None:
    """Register a transport for the netloc extracted from a URL."""
    host = urlparse(url).netloc
    register_upstream_transport(host, transport)


def unregister_upstream_transport(host: str) -> None:
    """Remove a transport registration for the given host."""
    if not host:
        return
    _TRANSPORTS.pop(_normalize_host(host), None)


def clear_upstream_transports() -> None:
    """Clear all registered transports (useful for tests)."""
    _TRANSPORTS.clear()


def get_upstream_transport(url: str) -> Optional[httpx.AsyncBaseTransport]:
    """Return a registered transport for the URL's netloc (if any)."""
    if not url:
        return None
    host = urlparse(url).netloc
    if not host:
        return None
    return _TRANSPORTS.get(_normalize_host(host))
