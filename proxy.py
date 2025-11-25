import asyncio
import json
import logging
import os
import socket
import sys
import uuid
from dataclasses import dataclass
from datetime import datetime
from pathlib import Path
from typing import Any, Awaitable, Callable, Dict, List, Mapping, Optional

import httpx
import yaml
from fastapi import FastAPI, HTTPException, Request
from fastapi.responses import Response, StreamingResponse

# Load environment variables from .env file if it exists
try:
    from dotenv import load_dotenv
    # Try to load .env from the same directory as the script
    env_path = Path(__file__).with_name(".env")
    if env_path.exists():
        load_dotenv(env_path)
        print(f"Loaded environment variables from {env_path}")
except ImportError:
    print("python-dotenv not installed, environment variables will only be loaded from the system")


REQUEST_LOG_DIR = Path(__file__).with_name("logs").joinpath("requests")
_PENDING_LOG_TASKS: set[asyncio.Task] = set()


def _register_background_task(task: asyncio.Task) -> None:
    _PENDING_LOG_TASKS.add(task)

    def _cleanup(_task: asyncio.Task) -> None:
        _PENDING_LOG_TASKS.discard(_task)

    task.add_done_callback(_cleanup)


class RequestLogRecorder:
    """Capture request/response lifecycle data and flush asynchronously."""

    def __init__(self, model_name: str, is_stream: bool, path: str) -> None:
        safe_model = self._safe_fragment(model_name or "unknown")
        timestamp = datetime.utcnow().strftime("%Y%m%d_%H%M%S")
        short_id = uuid.uuid4().hex[:4]
        filename = f"{timestamp}-{short_id}_{safe_model}.log"
        self.log_path = REQUEST_LOG_DIR / filename
        self.model_name = model_name or "unknown"
        self.is_stream = is_stream
        self.request_path = path
        self._buffer = bytearray()
        self._finalized = False
        self._stream_chunks = 0
        self._started = datetime.utcnow().isoformat() + "Z"
        REQUEST_LOG_DIR.mkdir(parents=True, exist_ok=True)
        self._append_text(
            f"request_time={self._started}\nmodel={self.model_name}\nis_stream={self.is_stream}\npath={self.request_path}\n"
        )

    def _append_text(self, text: str) -> None:
        self._buffer.extend(text.encode("utf-8"))

    def _append_bytes(self, data: bytes) -> None:
        self._buffer.extend(data)

    def record_request(self, method: str, query: str, headers: Mapping[str, str], body: bytes) -> None:
        if self._finalized:
            return
        header_dump = self._safe_json_dict(headers)
        self._append_text(f"=== REQUEST ===\nmethod={method}\nquery={query or ''}\nheaders={header_dump}\n")
        self._append_text(f"body_len={len(body)}\n-- REQUEST BODY START --\n")
        if body:
            self._append_text(self._format_payload(body))
        self._append_text("-- REQUEST BODY END --\n")

    def record_route(self, route: List[str]) -> None:
        if self._finalized:
            return
        self._append_text(f"route={route}\n")

    def record_backend_attempt(self, backend_name: str, attempt: int, url: str) -> None:
        if self._finalized:
            return
        self._append_text(
            f"=== BACKEND ATTEMPT {attempt} ===\nbackend={backend_name}\nurl={url}\n"
        )

    def record_backend_response(self, status: int, headers: Mapping[str, str], body: bytes) -> None:
        if self._finalized:
            return
        header_dump = self._safe_json_dict(headers)
        self._append_text(f"status={status}\nresponse_headers={header_dump}\n")
        self._append_text(f"body_len={len(body)}\n-- RESPONSE BODY START --\n")
        if body:
            self._append_text(self._format_payload(body))
        self._append_text("-- RESPONSE BODY END --\n")

    def record_stream_headers(self, status: int, headers: Mapping[str, str]) -> None:
        if self._finalized:
            return
        header_dump = self._safe_json_dict(headers)
        self._append_text(
            f"=== STREAM RESPONSE ===\nstatus={status}\nresponse_headers={header_dump}\n"
        )

    def record_stream_chunk(self, chunk: bytes) -> None:
        if self._finalized:
            return
        self._stream_chunks += 1
        self._append_text(
            f"-- STREAM CHUNK {self._stream_chunks} len={len(chunk)} --\n"
        )
        self._append_text(self._format_payload(chunk))
        self._append_text("-- END STREAM CHUNK --\n")

    def record_error(self, message: str) -> None:
        if self._finalized:
            return
        self._append_text(f"ERROR: {message}\n")

    def finalize(self, outcome: str) -> None:
        if self._finalized:
            return
        self._finalized = True
        finished = datetime.utcnow().isoformat() + "Z"
        self._append_text(f"=== FINAL STATUS: {outcome} at {finished} ===\n")
        try:
            loop = asyncio.get_running_loop()
        except RuntimeError:
            self._write_to_disk()
            return
        task = loop.create_task(self._flush_async())
        _register_background_task(task)

    @property
    def finalized(self) -> bool:
        return self._finalized

    async def _flush_async(self) -> None:
        data = bytes(self._buffer)

        def _write() -> None:
            tmp_path = self.log_path.with_suffix(self.log_path.suffix + ".tmp")
            with tmp_path.open("wb") as fh:
                fh.write(data)
            os.replace(tmp_path, self.log_path)

        await asyncio.to_thread(_write)

    def _write_to_disk(self) -> None:
        tmp_path = self.log_path.with_suffix(self.log_path.suffix + ".tmp")
        with tmp_path.open("wb") as fh:
            fh.write(self._buffer)
        os.replace(tmp_path, self.log_path)

    @staticmethod
    def _safe_json_dict(data: Mapping[str, str]) -> str:
        try:
            normalized = {str(k): str(v) for k, v in data.items()}
            return json.dumps(normalized, sort_keys=True)
        except Exception:
            return str(data)

    @staticmethod
    def _safe_fragment(text: str) -> str:
        if not text:
            return "unknown"
        filtered = [
            ch if ch.isalnum() or ch in {"-", "_"} else "-"
            for ch in text.strip()
        ]
        collapsed = "".join(filtered).strip("-") or "model"
        return collapsed[:48]

    def _format_payload(self, data: bytes) -> str:
        if not data:
            return ""
        try:
            text = data.decode("utf-8")
        except UnicodeDecodeError:
            return "<non-utf8 binary data omitted>\n"

        pretty = self._attempt_pretty_json(text)
        if pretty is not None:
            text = pretty

        if text.endswith("\n"):
            return text
        return text + "\n"

    @staticmethod
    def _attempt_pretty_json(text: str) -> Optional[str]:
        stripped = text.strip()
        if not stripped or stripped[0] not in "{[":
            return None
        try:
            parsed = json.loads(stripped)
        except json.JSONDecodeError:
            return None
        return json.dumps(parsed, ensure_ascii=False, indent=2)


# Enhanced logging configuration
def setup_logging():
    """Set up logging with proper handlers and formatters."""
    logger = logging.getLogger("cllmp-proxy")
    logger.setLevel(logging.INFO)
    
    # Clear any existing handlers
    logger.handlers.clear()
    
    # Create console handler with proper formatting
    console_handler = logging.StreamHandler(sys.stdout)
    console_handler.setLevel(logging.INFO)
    
    # Create formatter with timestamp and level
    formatter = logging.Formatter(
        '%(asctime)s - %(name)s - %(levelname)s - %(message)s',
        datefmt='%Y-%m-%d %H:%M:%S'
    )
    console_handler.setFormatter(formatter)
    
    # Add handler to logger
    logger.addHandler(console_handler)
    
    # Set logger to propagate to root logger to ensure proper flushing
    logger.propagate = True
    
    return logger

logger = setup_logging()

DEFAULT_TIMEOUT = 30
DEFAULT_RETRY_DELAY = 0.25
MAX_RETRY_DELAY = 2.0
RETRYABLE_STATUSES = {408, 409, 429, 500, 502, 503, 504}
HOP_BY_HOP_HEADERS = {
    "connection",
    "keep-alive",
    "proxy-authenticate",
    "proxy-authorization",
    "te",
    "trailers",
    "transfer-encoding",
    "upgrade",
}


def _default_config_path() -> str:
    """Resolve the default path to the LiteLLM-style config file."""
    return str(Path(__file__).with_name("litellm_config.yaml"))


CONFIG_PATH = os.getenv("CLLMP_CONFIG", _default_config_path())


@dataclass
class Backend:
    name: str
    base_url: str
    api_key: str
    timeout: Optional[float]
    target_model: Optional[str]
    supports_reasoning: bool = False

    def build_url(self, path: str, query: str) -> str:
        base = self.base_url.rstrip("/")
        normalized_path = path or ""
        if not normalized_path.startswith("/"):
            normalized_path = f"/{normalized_path}"
        if base.endswith("/v1") and normalized_path.startswith("/v1"):
            normalized_path = normalized_path[len("/v1"):]
            if not normalized_path:
                normalized_path = "/"
        url = f"{base}{normalized_path}"
        if query:
            separator = "&" if "?" in url else "?"
            url = f"{url}{separator}{query}"
        return url


class BackendRetryableError(Exception):
    """Signals that another backend attempt should be made."""

    def __init__(self, message: str, response: Optional[Response] = None) -> None:
        super().__init__(message)
        self.response = response


class ProxyRouter:
    def __init__(self, config: Dict) -> None:
        self.backends = self._parse_backends(config.get("model_list", []))
        if not self.backends:
            raise RuntimeError("No backends found in config")
        self._lock = asyncio.Lock()

        router_cfg = config.get("router_settings") or {}
        self.num_retries = max(1, int(router_cfg.get("num_retries", 1)))
        self.fallbacks = self._parse_fallbacks(router_cfg.get("fallbacks", []))

    async def forward_request(
        self,
        model_name: str,
        path: str,
        query: str,
        body: bytes,
        payload: Mapping[str, Any],
        is_stream: bool,
        headers: Mapping[str, str],
        request_log: Optional[RequestLogRecorder] = None,
        disconnect_checker: Optional[Callable[[], Awaitable[bool]]] = None,
    ) -> Response:
        logger.info(f"Received request for model: {model_name}, path: {path}, stream: {is_stream}")
        
        try:
            route = await self._build_route(model_name)
            if request_log:
                request_log.record_route([b.name for b in route])
            logger.info(f"Built route for model {model_name} with backends: {[b.name for b in route]}")
        except KeyError as exc:
            logger.error(f"Failed to build route for model {model_name}: {str(exc)}")
            raise HTTPException(status_code=400, detail=str(exc)) from exc

        last_error_response: Optional[Response] = None
        last_error_message: Optional[str] = None

        for i, backend in enumerate(route):
            logger.info(f"Attempting backend {i+1}/{len(route)}: {backend.name}")
            try:
                response = await self._call_backend_with_retries(
                    backend,
                    path,
                    query,
                    body,
                    payload,
                    is_stream,
                    headers,
                    request_log,
                    disconnect_checker,
                )
                logger.info(f"Successfully served model {model_name} via backend {backend.name}")
                return response
            except BackendRetryableError as exc:
                last_error_message = str(exc)
                if exc.response is not None:
                    last_error_response = exc.response
                logger.warning(f"Backend {backend.name} failed: {exc}")

        if last_error_response is not None:
            logger.error(f"All backends failed for model {model_name}, returning last error response")
            return last_error_response

        detail = last_error_message or f"All backends failed for model '{model_name}'"
        logger.error(f"All backends failed for model {model_name}: {detail}")
        if request_log:
            request_log.record_error(detail)
            request_log.finalize("error")
        raise HTTPException(status_code=502, detail=detail)

    async def _build_route(self, model_name: str) -> List[Backend]:
        async with self._lock:
            seen: set[str] = set()
            order: List[Backend] = []

            def add_backend(name: Optional[str]) -> None:
                if not name or name in seen:
                    return
                seen.add(name)
                backend = self.backends.get(name)
                if backend:
                    order.append(backend)
                else:
                    logger.warning("Model '%s' referenced but not defined", name)

            add_backend(model_name)
            for fb in self.fallbacks.get(model_name, []):
                add_backend(fb)

            if not order:
                raise KeyError(f"Model '{model_name}' is not defined in config")
            return order

    async def register_backend(self, backend: Backend, fallbacks: Optional[List[str]]) -> bool:
        """Register or replace a backend at runtime. Returns True if replaced."""
        async with self._lock:
            replaced = backend.name in self.backends
            self.backends[backend.name] = backend
            if fallbacks is not None:
                self.fallbacks[backend.name] = fallbacks
            return replaced

    async def list_model_names(self) -> List[str]:
        async with self._lock:
            return list(self.backends.keys())

    async def _call_backend_with_retries(
        self,
        backend: Backend,
        path: str,
        query: str,
        body: bytes,
        payload: Mapping[str, Any],
        is_stream: bool,
        headers: Mapping[str, str],
        request_log: Optional[RequestLogRecorder] = None,
        disconnect_checker: Optional[Callable[[], Awaitable[bool]]] = None,
    ) -> Response:
        attempts = max(1, self.num_retries)
        delay = DEFAULT_RETRY_DELAY
        last_error: Optional[BackendRetryableError] = None
        
        logger.info(f"Calling backend {backend.name} with {attempts} max attempts")

        for attempt in range(attempts):
            logger.debug(f"Attempt {attempt + 1}/{attempts} for backend {backend.name}")
            try:
                response = await self._execute_backend_request(
                    backend,
                    path,
                    query,
                    body,
                    payload,
                    is_stream,
                    headers,
                    attempt + 1,
                    request_log,
                    disconnect_checker,
                )
                logger.info(f"Backend {backend.name} succeeded on attempt {attempt + 1}")
                return response
            except BackendRetryableError as exc:
                last_error = exc
                logger.warning(f"Backend {backend.name} attempt {attempt + 1} failed: {exc}")
            except httpx.HTTPError as exc:
                error_detail = _format_httpx_error(exc, backend, url=backend.build_url(path, query))
                last_error = BackendRetryableError(
                    f"{backend.name} request error: {error_detail}"
                )
                logger.exception(
                    "Backend %s HTTP error on attempt %d/%d: %s",
                    backend.name,
                    attempt + 1,
                    attempts,
                    error_detail,
                )

            if attempt + 1 < attempts:
                logger.info(f"Retrying backend {backend.name} in {delay:.2f}s...")
                await asyncio.sleep(delay)
                delay = min(delay * 2, MAX_RETRY_DELAY)
                continue

            if last_error is not None:
                logger.error(f"Backend {backend.name} exhausted all attempts")
                raise last_error

        raise BackendRetryableError(f"{backend.name} exhausted without success")

    async def _execute_backend_request(
        self,
        backend: Backend,
        path: str,
        query: str,
        body: bytes,
        payload: Mapping[str, Any],
        is_stream: bool,
        headers: Mapping[str, str],
        attempt_number: int,
        request_log: Optional[RequestLogRecorder] = None,
        disconnect_checker: Optional[Callable[[], Awaitable[bool]]] = None,
    ) -> Response:
        url = backend.build_url(path, query)
        outbound_headers = _build_outbound_headers(headers, backend.api_key)
        outbound_body = _build_backend_body(payload, backend, body)
        timeout = backend.timeout or DEFAULT_TIMEOUT
        if request_log:
            request_log.record_backend_attempt(backend.name, attempt_number, url)

        logger.debug(f"Executing request to {url} with timeout {timeout}s, stream: {is_stream}")

        if is_stream:
            logger.debug(f"Initiating streaming request to {url}")
            return await _streaming_request(
                url,
                outbound_headers,
                outbound_body,
                timeout,
                request_log,
                disconnect_checker,
            )

        logger.debug(f"Initiating non-streaming request to {url}")
        async with httpx.AsyncClient(timeout=timeout) as client:
            resp = await client.post(url, headers=outbound_headers, content=outbound_body)

        logger.debug(f"Received response from {url}: status {resp.status_code}")

        if request_log:
            request_log.record_backend_response(resp.status_code, resp.headers, resp.content)

        if resp.status_code in RETRYABLE_STATUSES:
            response = _build_response_from_httpx(resp)
            raise BackendRetryableError(
                f"{backend.name} returned status {resp.status_code}", response=response
            )

        return _build_response_from_httpx(resp)

    @staticmethod
    def _parse_backends(entries: List[Dict]) -> Dict[str, Backend]:
        backends: Dict[str, Backend] = {}
        for entry in entries:
            name = entry.get("model_name")
            params = entry.get("litellm_params") or {}
            base = (params.get("api_base") or "").strip()
            if not name or not base:
                continue
            api_key = str(params.get("api_key") or "")
            timeout = params.get("request_timeout")
            try:
                timeout_val = float(timeout) if timeout is not None else None
            except (TypeError, ValueError):
                timeout_val = None

            target_model = _extract_target_model(params)

            supports_reasoning = bool(params.get("supports_reasoning"))

            backends[name] = Backend(
                name=name,
                base_url=base,
                api_key=api_key,
                timeout=timeout_val,
                target_model=target_model,
                supports_reasoning=supports_reasoning,
            )
        return backends

    @staticmethod
    def _parse_fallbacks(entries: List[Dict]) -> Dict[str, List[str]]:
        fallbacks: Dict[str, List[str]] = {}
        for entry in entries:
            if not isinstance(entry, dict):
                continue
            for primary, targets in entry.items():
                if isinstance(targets, (list, tuple)):
                    names = [str(t) for t in targets if t]
                elif targets:
                    names = [str(targets)]
                else:
                    names = []
                fallbacks[str(primary)] = names
        return fallbacks


def _normalize_timeout(value: Any) -> Optional[float]:
    if value is None:
        return None
    try:
        return float(value)
    except (TypeError, ValueError) as exc:
        raise ValueError("request_timeout must be numeric") from exc


def _normalize_fallbacks(value: Any) -> Optional[List[str]]:
    if value is None:
        return None
    if isinstance(value, (list, tuple)):
        return [str(v) for v in value if v]
    if isinstance(value, str):
        return [value]
    raise ValueError("fallbacks must be a string or list of strings")


def _backend_from_runtime_payload(payload: Mapping[str, Any]) -> tuple[Backend, Optional[List[str]]]:
    if not isinstance(payload, Mapping):
        raise ValueError("body must be a JSON object")

    model_name = str(payload.get("model_name") or payload.get("name") or "").strip()
    if not model_name:
        raise ValueError("model_name is required")

    params = payload.get("litellm_params") or payload
    api_base = str(params.get("api_base") or params.get("base_url") or "").strip()
    if not api_base:
        raise ValueError("api_base is required")

    api_key = str(params.get("api_key") or payload.get("api_key") or "")
    timeout_raw = (
        params.get("request_timeout")
        if "request_timeout" in params
        else payload.get("request_timeout")
    )
    if timeout_raw is None:
        timeout_raw = params.get("timeout")
    timeout = _normalize_timeout(timeout_raw)
    target_model = _extract_target_model(params)
    supports_reasoning = bool(params.get("supports_reasoning") or payload.get("supports_reasoning"))
    fallbacks = _normalize_fallbacks(payload.get("fallbacks"))

    backend = Backend(
        name=model_name,
        base_url=api_base,
        api_key=api_key,
        timeout=timeout,
        target_model=target_model,
        supports_reasoning=supports_reasoning,
    )
    return backend, fallbacks


def _format_httpx_error(exc: httpx.HTTPError, backend: Backend, url: Optional[str] = None) -> str:
    """Produce a detailed, user-facing description of an httpx error."""
    parts: List[str] = [exc.__class__.__name__]
    message = str(exc).strip()
    if message:
        parts.append(message)

    request = getattr(exc, "request", None)
    if request is not None:
        parts.append(f"request={request.method} {request.url}")
    elif url:
        parts.append(f"url={url}")

    if isinstance(exc, httpx.TimeoutException):
        timeout = backend.timeout or DEFAULT_TIMEOUT
        parts.append(f"timeout={timeout}s")

    return "; ".join(parts)


def _build_outbound_headers(
    incoming: Mapping[str, str], backend_api_key: str
) -> Dict[str, str]:
    headers: Dict[str, str] = {}
    normalized_keys: set[str] = set()
    for key, value in incoming.items():
        key_lower = key.lower()
        if key_lower in HOP_BY_HOP_HEADERS or key_lower in {"authorization", "host", "content-length"}:
            continue
        if key_lower in normalized_keys:
            continue
        headers[key] = value
        normalized_keys.add(key_lower)

    if "content-type" not in normalized_keys:
        headers["Content-Type"] = incoming.get("content-type", "application/json")
        normalized_keys.add("content-type")
    if backend_api_key:
        headers["Authorization"] = f"Bearer {backend_api_key}"
        normalized_keys.add("authorization")
    return headers


def _extract_target_model(params: Mapping[str, Any]) -> Optional[str]:
    override = params.get("target_model") or params.get("forward_model")
    if override:
        override_str = str(override).strip()
        if override_str:
            return override_str

    raw_model = str(params.get("model") or "").strip()
    if not raw_model:
        return None

    if raw_model.lower().startswith("openai/"):
        _, remainder = raw_model.split("/", 1)
        if remainder:
            return remainder

    return raw_model


def _build_backend_body(
    payload: Mapping[str, Any], backend: Backend, original_body: bytes
) -> bytes:
    target_model = backend.target_model
    needs_thinking = False
    if backend.supports_reasoning:
        thinking = payload.get("thinking")
        needs_thinking = not (
            isinstance(thinking, Mapping) and thinking.get("type")
        )

    if not target_model and not needs_thinking:
        return original_body

    try:
        updated_payload = dict(payload)
        if target_model:
            updated_payload["model"] = target_model
            logger.debug(
                "Rewrote model for backend %s to %s", backend.name, target_model
            )
        if needs_thinking:
            updated_payload["thinking"] = {"type": "enabled"}
            logger.debug("Enabled reasoning block for backend %s", backend.name)
        rewritten = json.dumps(updated_payload).encode("utf-8")
        return rewritten
    except (TypeError, ValueError) as exc:
        logger.warning(
            "Failed to rewrite payload for backend %s: %s", backend.name, exc
        )
        return original_body


def _filter_response_headers(headers: Mapping[str, str]) -> Dict[str, str]:
    filtered: Dict[str, str] = {}
    for key, value in headers.items():
        key_lower = key.lower()
        # Drop headers FastAPI will recompute or that no longer match the payload we send
        if key_lower in HOP_BY_HOP_HEADERS or key_lower in {"content-length", "transfer-encoding", "content-encoding"}:
            continue
        filtered[key] = value
    return filtered


def _build_response_from_httpx(resp: httpx.Response, content: Optional[bytes] = None) -> Response:
    body = content if content is not None else resp.content
    headers = _filter_response_headers(resp.headers)
    media_type = headers.get("content-type")
    return Response(content=body, status_code=resp.status_code, headers=headers, media_type=media_type)


async def _streaming_request(
    url: str,
    headers: Dict[str, str],
    body: bytes,
    timeout: float,
    request_log: Optional[RequestLogRecorder] = None,
    disconnect_checker: Optional[Callable[[], Awaitable[bool]]] = None,
) -> Response:
    logger.debug(f"Setting up streaming client for {url}")
    client = httpx.AsyncClient(timeout=timeout)
    request = client.build_request("POST", url, headers=headers, content=body)

    logger.debug(f"Sending streaming request to {url}")
    resp = await client.send(request, stream=True)
    if request_log:
        request_log.record_stream_headers(resp.status_code, resp.headers)

    stream_closed = False

    async def close_stream() -> None:
        nonlocal stream_closed
        if stream_closed:
            return
        stream_closed = True
        logger.debug(f"Closing stream for {url}")
        await resp.aclose()
        await client.aclose()

    if resp.status_code in RETRYABLE_STATUSES:
        logger.warning(f"Streaming request to {url} returned retryable status {resp.status_code}")
        data = await resp.aread()
        await close_stream()
        if request_log:
            request_log.record_backend_response(resp.status_code, resp.headers, data)
        response = _build_response_from_httpx(resp, data)
        raise BackendRetryableError(
            f"stream request returned status {resp.status_code}", response=response
        )

    if resp.status_code >= 400:
        logger.warning(f"Streaming request to {url} returned error status {resp.status_code}")
        data = await resp.aread()
        await close_stream()
        if request_log:
            request_log.record_backend_response(resp.status_code, resp.headers, data)
            if not request_log.finalized:
                request_log.record_error(f"stream response status {resp.status_code}")
                request_log.finalize("error")
        return _build_response_from_httpx(resp, data)

    logger.info(f"Streaming request to {url} successful, status {resp.status_code}")
    headers_to_client = _filter_response_headers(resp.headers)
    media_type = headers_to_client.pop("content-type", None)

    async def iterator():
        try:
            chunk_count = 0
            stream = resp.aiter_raw()
            while True:
                if disconnect_checker and await disconnect_checker():
                    raise asyncio.CancelledError("client disconnected")
                try:
                    chunk = await stream.__anext__()
                except StopAsyncIteration:
                    break
                if chunk:
                    chunk_count += 1
                    if chunk_count % 10 == 0:  # Log every 10th chunk to avoid spam
                        logger.debug(f"Streamed {chunk_count} chunks from {url}")
                    if request_log:
                        request_log.record_stream_chunk(chunk)
                    yield chunk
        except asyncio.CancelledError as cancel_exc:
            logger.info(f"Streaming request to {url} cancelled by client")
            if request_log and not request_log.finalized:
                request_log.record_error("stream cancelled by client")
                request_log.finalize("cancelled")
            raise cancel_exc
        except Exception as e:
            logger.error(f"Error during streaming from {url}: {e}")
            if request_log:
                request_log.record_error(f"streaming error: {e}")
                request_log.finalize("error")
            raise
        finally:
            logger.debug(f"Stream completed for {url}, total chunks: {chunk_count}")
            await close_stream()
            if request_log and not request_log.finalized:
                request_log.finalize("success")

    return StreamingResponse(
        iterator(),
        status_code=resp.status_code,
        headers=headers_to_client,
        media_type=media_type or "text/event-stream",
    )


def _load_config(path: str) -> Dict:
    logger.info(f"Loading configuration from {path}")
    config_path = Path(path)
    if not config_path.exists():
        logger.error(f"Config file not found: {config_path}")
        raise RuntimeError(f"Config file not found: {config_path}")
    with config_path.open("r", encoding="utf-8") as fh:
        data = yaml.safe_load(fh) or {}
    
    # Substitute environment variables in the configuration
    data = _substitute_env_vars(data)
    
    logger.info(f"Configuration loaded successfully from {path}")
    return data


def _substitute_env_vars(obj: Any) -> Any:
    """Recursively substitute environment variables in configuration values."""
    if isinstance(obj, dict):
        return {k: _substitute_env_vars(v) for k, v in obj.items()}
    elif isinstance(obj, list):
        return [_substitute_env_vars(item) for item in obj]
    elif isinstance(obj, str):
        # Replace ${VAR_NAME} or $VAR_NAME with environment variable value
        import re
        pattern = re.compile(r'\$\{([^}]+)\}|\$([A-Za-z_][A-Za-z0-9_]*)')
        
        def replace_var(match):
            var_name = match.group(1) or match.group(2)
            return os.getenv(var_name, match.group(0))  # Return original if not found
        
        return pattern.sub(replace_var, obj)
    else:
        return obj


logger.info("Initializing cLLMp Proxy")
config = _load_config(CONFIG_PATH)
router = ProxyRouter(config)
logger.info(f"Proxy router initialized with {len(router.backends)} backends")
app = FastAPI(title="cLLMp Proxy")
logger.info("FastAPI application created")

# Check if responses endpoint should be enabled
general_settings = config.get("general_settings") or {}
enable_responses_endpoint = bool(general_settings.get("enable_responses_endpoint", False))
server_cfg = general_settings.get("server") or {}
SERVER_HOST = str(server_cfg.get("host", "127.0.0.1"))
try:
    SERVER_PORT = int(server_cfg.get("port", 8000))
except (TypeError, ValueError):
    SERVER_PORT = 8000
logger.info(f"Responses endpoint enabled: {enable_responses_endpoint}")


@app.on_event("startup")
async def startup_event():
    logger.info("cLLMp Proxy server starting up...")
    logger.info("Configured bind address %s:%s", SERVER_HOST, SERVER_PORT)
    if SERVER_HOST == "0.0.0.0":
        hostname = socket.gethostname()
        logger.info("Reachable on local network at http://%s:%s", hostname, SERVER_PORT)
        try:
            lan_ip = socket.gethostbyname(hostname)
        except socket.gaierror:
            lan_ip = None
        if lan_ip and not lan_ip.startswith("127."):
            logger.info("Resolved LAN IP:  http://%s:%s", lan_ip, SERVER_PORT)
    logger.info(f"Available backends: {list(router.backends.keys())}")
    for name, backend in router.backends.items():
        logger.info(f"  - {name}: {backend.base_url}")
    logger.info("cLLMp Proxy server ready to handle requests")


@app.on_event("shutdown")
async def shutdown_event():
    if not _PENDING_LOG_TASKS:
        return
    logger.info("Waiting for %d pending log flush tasks", len(_PENDING_LOG_TASKS))
    pending = list(_PENDING_LOG_TASKS)
    await asyncio.gather(*pending, return_exceptions=True)
    logger.info("All log flush tasks completed")


async def _handle_openai_request(request: Request) -> Response:
    logger.info(f"Handling {request.method} request to {request.url.path}")
    
    body = await request.body()
    request_log: Optional[RequestLogRecorder] = None
    try:
        payload = json.loads(body or b"{}")
    except json.JSONDecodeError as exc:
        logger.error(f"Invalid JSON payload: {exc}")
        request_log = RequestLogRecorder("unknown", False, request.url.path)
        request_log.record_request(request.method, request.url.query, request.headers, body)
        request_log.record_error(f"invalid json: {exc}")
        request_log.finalize("error")
        raise HTTPException(
            status_code=400, 
            detail={
                "error": {
                    "message": "Invalid JSON payload",
                    "type": "invalid_request_error",
                    "code": "invalid_json"
                }
            }
        ) from exc

    if not isinstance(payload, Mapping):
        logger.error("Payload must be a JSON object")
        request_log = request_log or RequestLogRecorder("unknown", False, request.url.path)
        request_log.record_request(request.method, request.url.query, request.headers, body)
        request_log.record_error("payload must be a JSON object")
        request_log.finalize("error")
        raise HTTPException(
            status_code=400,
            detail={
                "error": {
                    "message": "Request body must be a JSON object",
                    "type": "invalid_request_error",
                    "code": "invalid_json_shape",
                }
            },
        )

    model_name = payload.get("model")
    if not isinstance(model_name, str) or not model_name:
        logger.error("Request missing model name")
        request_log = request_log or RequestLogRecorder("unknown", False, request.url.path)
        request_log.record_request(request.method, request.url.query, request.headers, body)
        request_log.record_error("missing model parameter")
        request_log.finalize("error")
        raise HTTPException(
            status_code=400, 
            detail={
                "error": {
                    "message": "You must provide a model parameter",
                    "type": "invalid_request_error",
                    "code": "missing_parameter"
                }
            }
        )

    # Basic validation for chat completions
    if "/chat/completions" in request.url.path:
        messages = payload.get("messages")
        if not messages or not isinstance(messages, list):
            logger.error("Request missing or invalid messages array")
            request_log = request_log or RequestLogRecorder(model_name, False, request.url.path)
            request_log.record_request(request.method, request.url.query, request.headers, body)
            request_log.record_error("missing messages array")
            request_log.finalize("error")
            raise HTTPException(
                status_code=400, 
                detail={
                    "error": {
                        "message": "You must provide a messages array",
                        "type": "invalid_request_error",
                        "code": "missing_parameter"
                    }
                }
            )

    is_stream = bool(payload.get("stream"))
    query = request.url.query or ""
    request_log = request_log or RequestLogRecorder(model_name, is_stream, request.url.path)
    request_log.record_request(request.method, query, request.headers, body)
    logger.info(f"Processing request for model {model_name}, stream={is_stream}")

    backend_path = request.url.path
    
    try:
        response = await router.forward_request(
            model_name=model_name,
            path=backend_path,
            query=query,
            body=body,
            payload=payload,
            is_stream=is_stream,
            headers=request.headers,
            request_log=request_log,
            disconnect_checker=request.is_disconnected,
        )
        logger.info(f"Request for model {model_name} completed successfully")
        if not is_stream and request_log and not request_log.finalized:
            request_log.finalize("success")
        return response
    except Exception as e:
        logger.error(f"Error processing request for model {model_name}: {e}")
        if request_log and not request_log.finalized:
            request_log.record_error(str(e))
            request_log.finalize("error")
        raise


@app.post("/v1/chat/completions")
async def chat_completions(request: Request):
    logger.info("Received chat completions request")
    return await _handle_openai_request(request)


# Only register responses endpoint if enabled in config
if enable_responses_endpoint:
    @app.post("/v1/responses")
    async def responses(request: Request):
        logger.info("Received responses request")
        return await _handle_openai_request(request)
else:
    logger.info("Responses endpoint is disabled in configuration")


@app.get("/v1/models")
async def list_models():
    """List available models in OpenAI API format."""
    logger.info("Received models list request")
    
    models = []
    for model_name in await router.list_model_names():
        models.append({
            "id": model_name,
            "object": "model",
            "created": int(Path(__file__).stat().st_ctime),
            "owned_by": "cllmp-proxy"
        })
    
    return {
        "object": "list",
        "data": models
    }


@app.post("/admin/models")
async def register_model(request: Request):
    """Register a new backend at runtime without restarting the proxy."""
    try:
        payload = await request.json()
    except json.JSONDecodeError as exc:
        logger.error("Invalid JSON when registering model: %s", exc)
        raise HTTPException(status_code=400, detail="Invalid JSON body") from exc

    try:
        backend, fallbacks = _backend_from_runtime_payload(payload)
    except ValueError as exc:
        logger.error("Failed to register model: %s", exc)
        raise HTTPException(status_code=400, detail=str(exc)) from exc

    replaced = await router.register_backend(backend, fallbacks)
    logger.info(
        "Registered model '%s' (replaced=%s) base=%s fallbacks=%s",
        backend.name,
        replaced,
        backend.base_url,
        fallbacks or [],
    )
    return {
        "status": "ok",
        "model": backend.name,
        "replaced": replaced,
        "fallbacks": fallbacks or [],
    }
