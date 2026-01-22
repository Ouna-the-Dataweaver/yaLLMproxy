"""HTTP reverse proxy forwarder for yaLLMproxy.

Designed as a drop-in replacement for the TCP forwarder when large payloads
cause connection resets. This proxy preserves streaming and avoids buffering.
"""

from __future__ import annotations

import logging
import os
import time
from contextlib import asynccontextmanager
from dataclasses import dataclass
from pathlib import Path
from typing import Iterable, Optional

import httpx
from fastapi import FastAPI, Request
from fastapi.responses import PlainTextResponse, Response, StreamingResponse
from starlette.datastructures import Headers
from starlette.requests import ClientDisconnect

from .config_loader import load_config
from .logging.setup import LOG_FORMAT, LOG_DATE_FORMAT, HTTP_FORWARDER_LOG_PATH

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
    debug: bool = False
    ssl_enabled: bool = False
    ssl_cert_file: Optional[str] = None
    ssl_key_file: Optional[str] = None

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
    debug = _to_bool(_get(http_cfg, "debug")) or False

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

    debug_env = _to_bool(os.getenv("HTTP_FORWARD_DEBUG"))
    if debug_env is not None:
        debug = debug_env

    # SSL settings
    ssl_cfg = http_cfg.get("ssl") or {}
    ssl_enabled = _to_bool(ssl_cfg.get("enabled")) or False
    ssl_cert_file = _to_str(ssl_cfg.get("cert_file"))
    ssl_key_file = _to_str(ssl_cfg.get("key_file"))

    # Env overrides for SSL
    ssl_enabled_env = _to_bool(os.getenv("HTTP_FORWARD_SSL_ENABLED"))
    if ssl_enabled_env is not None:
        ssl_enabled = ssl_enabled_env
    ssl_cert_file = os.getenv("HTTP_FORWARD_SSL_CERT", ssl_cert_file)
    ssl_key_file = os.getenv("HTTP_FORWARD_SSL_KEY", ssl_key_file)

    return ForwarderSettings(
        listen_host=listen_host,
        listen_port=listen_port,
        target_scheme=target_scheme,
        target_host=target_host,
        target_port=target_port,
        preserve_host=preserve_host,
        timeout_seconds=timeout_seconds if timeout_seconds and timeout_seconds > 0 else None,
        debug=debug,
        ssl_enabled=ssl_enabled,
        ssl_cert_file=ssl_cert_file,
        ssl_key_file=ssl_key_file,
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
    decoded: list[tuple[str, str]] = []
    for key, value in raw_headers:
        key_str = key.decode("latin-1")
        value_str = value.decode("latin-1").strip()
        decoded.append((key_str, value_str))
    return decoded


def _strip_headers(
    raw_headers: Iterable[tuple[bytes, bytes]],
    remove: set[bytes],
) -> list[tuple[bytes, bytes]]:
    remove_lower = {name.lower() for name in remove}
    return [(key, value) for key, value in raw_headers if key.lower() not in remove_lower]


@asynccontextmanager
async def lifespan(app: FastAPI):
    settings = _load_settings()

    # Configure debug logging if enabled
    if settings.debug:
        logger.setLevel(logging.DEBUG)
        # Add file handler for debug logs
        log_path = Path(HTTP_FORWARDER_LOG_PATH)
        log_path.parent.mkdir(parents=True, exist_ok=True)
        file_handler = logging.FileHandler(log_path, mode='w', encoding='utf-8')
        file_handler.setLevel(logging.DEBUG)
        formatter = logging.Formatter(LOG_FORMAT, datefmt=LOG_DATE_FORMAT)
        file_handler.setFormatter(formatter)
        logger.addHandler(file_handler)
        # Also set console handler to DEBUG if exists
        for handler in logger.handlers:
            if isinstance(handler, logging.StreamHandler):
                handler.setLevel(logging.DEBUG)
        logger.debug("Debug mode enabled - logging to %s", HTTP_FORWARDER_LOG_PATH)

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
    start_time = time.perf_counter()

    # Extract connection info for logging
    client_host = request.client.host if request.client else "unknown"
    client_port = request.client.port if request.client else "unknown"
    content_length = request.headers.get("content-length", "not-set")

    target_url = _build_target_url(settings.target_base, path, request.url.query)
    headers = _decode_headers(
        _filter_headers(request.headers.raw, preserve_host=settings.preserve_host)
    )

    # Debug: log request received with headers
    if logger.isEnabledFor(logging.DEBUG):
        logger.debug(
            "Request received: %s %s from %s:%s, Content-Length: %s",
            request.method,
            path,
            client_host,
            client_port,
            content_length,
        )
        logger.debug(
            "Original request headers: %s",
            dict(request.headers),
        )
        logger.debug(
            "Forwarding to %s with headers: %s",
            target_url,
            dict(headers),
        )

    # Read the full body first to avoid streaming issues
    # This is necessary because httpx streaming from request.stream() can cause
    # connection issues when the upstream reads slower than we receive
    try:
        if logger.isEnabledFor(logging.DEBUG):
            logger.debug(
                "Reading request body, Content-Length: %s",
                content_length,
            )
        body_start = time.perf_counter()
        request_body = await request.body()
        body_elapsed = time.perf_counter() - body_start
        if logger.isEnabledFor(logging.DEBUG):
            logger.debug(
                "Request body read complete: %d bytes in %.3fs",
                len(request_body),
                body_elapsed,
            )
    except ClientDisconnect as cd_exc:
        elapsed = time.perf_counter() - start_time
        # Try to get more info about the disconnect
        scope_info = {
            "http_version": request.scope.get("http_version"),
            "scheme": request.scope.get("scheme"),
            "method": request.scope.get("method"),
            "root_path": request.scope.get("root_path"),
        }
        logger.warning(
            "Client disconnected while reading body after %.3fs "
            "(client=%s:%s, Content-Length=%s, scope=%s, exc=%r)",
            elapsed,
            client_host,
            client_port,
            content_length,
            scope_info,
            cd_exc,
        )
        return Response(status_code=499)  # Client Closed Request
    except Exception as body_exc:
        elapsed = time.perf_counter() - start_time
        logger.warning(
            "Failed to read request body after %.3fs: %s (type=%s, client=%s:%s, Content-Length=%s)",
            elapsed,
            body_exc,
            type(body_exc).__name__,
            client_host,
            client_port,
            content_length,
        )
        return PlainTextResponse("Failed to read request body", status_code=502)

    try:
        if logger.isEnabledFor(logging.DEBUG):
            logger.debug(
                "Building upstream request to %s with %d bytes body",
                target_url,
                len(request_body),
            )
        upstream_request = client.build_request(
            request.method,
            target_url,
            headers=headers,
            content=request_body,
        )
        if logger.isEnabledFor(logging.DEBUG):
            logger.debug(
                "Sending upstream request to %s",
                target_url,
            )
        upstream_response = await client.send(upstream_request, stream=True)
        if logger.isEnabledFor(logging.DEBUG):
            logger.debug(
                "Upstream request sent successfully, awaiting response",
            )
    except httpx.RequestError as exc:
        elapsed = time.perf_counter() - start_time
        logger.warning(
            "Upstream connection failed after %.3fs: %s (target=%s, client=%s:%s, method=%s)",
            elapsed,
            exc,
            target_url,
            client_host,
            client_port,
            request.method,
        )
        if logger.isEnabledFor(logging.DEBUG):
            logger.debug(
                "Failed request headers: %s, error_type=%s",
                dict(request.headers),
                type(exc).__name__,
            )
        return PlainTextResponse("Upstream connection failed", status_code=502)

    response_raw_headers = _filter_headers(upstream_response.headers.raw, preserve_host=True)
    response_headers = Headers(raw=response_raw_headers)

    # Debug: log upstream response received
    if logger.isEnabledFor(logging.DEBUG):
        elapsed = time.perf_counter() - start_time
        logger.debug(
            "Upstream response received in %.3fs: status=%s, headers=%s",
            elapsed,
            upstream_response.status_code,
            dict(upstream_response.headers),
        )

    async def _iter_response():
        chunk_count = 0
        total_bytes = 0
        try:
            async for chunk in upstream_response.aiter_raw():
                chunk_count += 1
                total_bytes += len(chunk)
                yield chunk
        finally:
            await upstream_response.aclose()
            if logger.isEnabledFor(logging.DEBUG):
                elapsed = time.perf_counter() - start_time
                logger.debug(
                    "Streaming response complete: %d chunks, %d bytes in %.3fs (client=%s:%s)",
                    chunk_count,
                    total_bytes,
                    elapsed,
                    client_host,
                    client_port,
                )

    content_type = upstream_response.headers.get("content-type", "").lower()
    is_event_stream = "text/event-stream" in content_type
    if is_event_stream:
        if logger.isEnabledFor(logging.DEBUG):
            logger.debug(
                "Starting streaming response: status=%s, content-type=%s",
                upstream_response.status_code,
                content_type,
            )
        # Force Connection: close for streaming responses to avoid keep-alive race conditions
        # where the client sends a new request before the stream is fully processed
        streaming_headers = dict(response_headers.items())
        streaming_headers["Connection"] = "close"
        return StreamingResponse(
            _iter_response(),
            status_code=upstream_response.status_code,
            headers=streaming_headers,
        )

    content = await upstream_response.aread()

    # Debug: log buffered response complete
    if logger.isEnabledFor(logging.DEBUG):
        elapsed = time.perf_counter() - start_time
        logger.debug(
            "Buffered response complete in %.3fs: status=%s, content_length=%d bytes",
            elapsed,
            upstream_response.status_code,
            len(content),
        )

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
