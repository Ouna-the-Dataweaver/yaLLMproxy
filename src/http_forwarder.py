"""HTTP reverse proxy forwarder for yaLLMproxy.

Designed as a drop-in replacement for the TCP forwarder when large payloads
cause connection resets. This proxy preserves streaming and avoids buffering.
"""

from __future__ import annotations

import logging
import os
from contextlib import asynccontextmanager
from dataclasses import dataclass
from typing import Iterable, Optional

import httpx
from fastapi import FastAPI, Request
from fastapi.responses import PlainTextResponse, Response, StreamingResponse
from starlette.datastructures import Headers

from .config_loader import load_config

logger = logging.getLogger("yallmp-proxy.http_forwarder")

HOP_BY_HOP_HEADERS = {
    b"connection",
    b"keep-alive",
    b"proxy-authenticate",
    b"proxy-authorization",
    b"te",
    b"trailer",
    b"transfer-encoding",
    b"upgrade",
    b"proxy-connection",
}


@dataclass(frozen=True)
class ForwarderSettings:
    listen_host: str
    listen_port: int
    target_scheme: str
    target_host: str
    target_port: int
    preserve_host: bool
    timeout_seconds: Optional[float]

    @property
    def target_base(self) -> str:
        return f"{self.target_scheme}://{self.target_host}:{self.target_port}"


def _get(cfg: dict, *keys: str):
    cur = cfg
    for key in keys:
        if not isinstance(cur, dict):
            return None
        cur = cur.get(key)
    return cur


def _to_int(value) -> Optional[int]:
    if value is None:
        return None
    try:
        return int(value)
    except (TypeError, ValueError):
        return None


def _to_str(value) -> Optional[str]:
    if value is None:
        return None
    return str(value)


def _to_float(value) -> Optional[float]:
    if value is None:
        return None
    try:
        return float(value)
    except (TypeError, ValueError):
        return None


def _to_bool(value) -> Optional[bool]:
    if value is None:
        return None
    if isinstance(value, bool):
        return value
    value_str = str(value).strip().lower()
    if value_str in {"1", "true", "yes", "on"}:
        return True
    if value_str in {"0", "false", "no", "off"}:
        return False
    return None


def _default_target_host(proxy_host: str | None) -> str:
    if proxy_host and proxy_host not in {"0.0.0.0", "::"}:
        return proxy_host
    return "127.0.0.1"


def _load_settings() -> ForwarderSettings:
    cfg: dict = {}
    try:
        cfg = load_config(substitute_env=False)
    except Exception as exc:
        logger.warning("Failed to load config; using defaults. (%s)", exc)

    proxy_host = _to_str(_get(cfg, "proxy_settings", "server", "host")) or "127.0.0.1"
    proxy_port = _to_int(_get(cfg, "proxy_settings", "server", "port")) or 7978

    http_cfg = _get(cfg, "http_forwarder_settings") or {}
    listen_cfg = http_cfg.get("listen") or {}
    target_cfg = http_cfg.get("target") or {}

    listen_host = _to_str(listen_cfg.get("host")) or "0.0.0.0"
    listen_port = _to_int(listen_cfg.get("port")) or 6969

    target_scheme = _to_str(target_cfg.get("scheme")) or "http"
    target_host = _to_str(target_cfg.get("host")) or _default_target_host(proxy_host)
    target_port = _to_int(target_cfg.get("port")) or proxy_port

    preserve_host = _to_bool(_get(http_cfg, "preserve_host"))
    if preserve_host is None:
        preserve_host = True
    timeout_seconds = _to_float(_get(http_cfg, "timeout_seconds"))

    # Env overrides
    listen_host = os.getenv("HTTP_FORWARD_LISTEN_HOST", listen_host)
    listen_port = _to_int(os.getenv("HTTP_FORWARD_LISTEN_PORT")) or listen_port
    target_scheme = os.getenv("HTTP_FORWARD_TARGET_SCHEME", target_scheme)
    target_host = os.getenv("HTTP_FORWARD_TARGET_HOST", target_host)
    target_port = _to_int(os.getenv("HTTP_FORWARD_TARGET_PORT")) or target_port
    preserve_host = _to_bool(os.getenv("HTTP_FORWARD_PRESERVE_HOST")) or preserve_host

    timeout_env = os.getenv("HTTP_FORWARD_TIMEOUT")
    if timeout_env is not None:
        try:
            timeout_seconds = float(timeout_env)
        except ValueError:
            logger.warning("Invalid HTTP_FORWARD_TIMEOUT=%s", timeout_env)

    return ForwarderSettings(
        listen_host=listen_host,
        listen_port=listen_port,
        target_scheme=target_scheme,
        target_host=target_host,
        target_port=target_port,
        preserve_host=preserve_host,
        timeout_seconds=timeout_seconds if timeout_seconds and timeout_seconds > 0 else None,
    )


def _build_target_url(base: str, path: str, query: str) -> str:
    base = base.rstrip("/")
    path = path.lstrip("/")
    url = f"{base}/{path}" if path else base
    if query:
        url = f"{url}?{query}"
    return url


def _connection_header_overrides(raw_headers: Iterable[tuple[bytes, bytes]]) -> set[bytes]:
    extra: set[bytes] = set()
    for key, value in raw_headers:
        if key.lower() == b"connection":
            try:
                header_value = value.decode("latin-1")
            except UnicodeDecodeError:
                continue
            for name in header_value.split(","):
                name = name.strip().lower()
                if name:
                    extra.add(name.encode("latin-1"))
    return extra


def _filter_headers(
    raw_headers: Iterable[tuple[bytes, bytes]],
    *,
    preserve_host: bool = False,
) -> list[tuple[bytes, bytes]]:
    hop_by_hop = set(HOP_BY_HOP_HEADERS)
    hop_by_hop.update(_connection_header_overrides(raw_headers))
    filtered: list[tuple[bytes, bytes]] = []
    for key, value in raw_headers:
        key_lower = key.lower()
        if key_lower in hop_by_hop:
            continue
        if not preserve_host and key_lower == b"host":
            continue
        filtered.append((key, value))
    return filtered


def _decode_headers(raw_headers: Iterable[tuple[bytes, bytes]]) -> list[tuple[str, str]]:
    return [(key.decode("latin-1"), value.decode("latin-1")) for key, value in raw_headers]


def _strip_headers(
    raw_headers: Iterable[tuple[bytes, bytes]],
    remove: set[bytes],
) -> list[tuple[bytes, bytes]]:
    remove_lower = {name.lower() for name in remove}
    return [(key, value) for key, value in raw_headers if key.lower() not in remove_lower]


@asynccontextmanager
async def lifespan(app: FastAPI):
    settings = _load_settings()
    if settings.timeout_seconds is None:
        timeout = httpx.Timeout(None)
    else:
        timeout = httpx.Timeout(settings.timeout_seconds)
    client = httpx.AsyncClient(timeout=timeout, follow_redirects=False)
    app.state.forwarder_settings = settings
    app.state.forwarder_client = client
    logger.info(
        "HTTP forwarder ready: %s -> %s",
        f"{settings.listen_host}:{settings.listen_port}",
        settings.target_base,
    )
    try:
        yield
    finally:
        await client.aclose()


app = FastAPI(title="yaLLMproxy HTTP Forwarder", lifespan=lifespan)


@app.api_route("/{path:path}", methods=["GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS", "HEAD"])
async def forward_request(path: str, request: Request):
    settings: ForwarderSettings = request.app.state.forwarder_settings
    client: httpx.AsyncClient = request.app.state.forwarder_client

    target_url = _build_target_url(settings.target_base, path, request.url.query)
    headers = _decode_headers(
        _filter_headers(request.headers.raw, preserve_host=settings.preserve_host)
    )

    try:
        upstream_request = client.build_request(
            request.method,
            target_url,
            headers=headers,
            content=request.stream(),
        )
        upstream_response = await client.send(upstream_request, stream=True)
    except httpx.RequestError as exc:
        logger.warning("Upstream connection failed: %s", exc)
        return PlainTextResponse("Upstream connection failed", status_code=502)

    response_raw_headers = _filter_headers(upstream_response.headers.raw, preserve_host=True)
    response_headers = Headers(raw=response_raw_headers)

    async def _iter_response():
        try:
            async for chunk in upstream_response.aiter_raw():
                yield chunk
        finally:
            await upstream_response.aclose()

    content_type = upstream_response.headers.get("content-type", "").lower()
    is_event_stream = "text/event-stream" in content_type
    if is_event_stream:
        return StreamingResponse(
            _iter_response(),
            status_code=upstream_response.status_code,
            headers=response_headers,
        )

    content = await upstream_response.aread()
    # httpx may decode compressed content; drop encoding/length headers to avoid mismatch.
    buffered_headers = Headers(
        raw=_strip_headers(
            response_raw_headers,
            remove={b"content-length", b"content-encoding"},
        )
    )
    return Response(
        content=content,
        status_code=upstream_response.status_code,
        headers=buffered_headers,
    )


__all__ = ["app"]
