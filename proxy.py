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
ERROR_LOG_DIR = Path(__file__).with_name("logs").joinpath("errors")
_PENDING_LOG_TASKS: set[asyncio.Task] = set()


def _register_background_task(task: asyncio.Task) -> None:
    _PENDING_LOG_TASKS.add(task)

    def _cleanup(_task: asyncio.Task) -> None:
        _PENDING_LOG_TASKS.discard(_task)

    task.add_done_callback(_cleanup)


def _log_error_event(
    model_name: str,
    error_type: str,
    error_message: str,
    backend_name: Optional[str] = None,
    http_status: Optional[int] = None,
    request_path: Optional[str] = None,
    request_log_path: Optional[Path] = None,
    extra_context: Optional[Dict[str, Any]] = None,
) -> None:
    """
    Log an error event to the errors subdirectory for easy error tracking.
    
    This creates a separate, smaller log file per error for quick scanning.
    """
    ERROR_LOG_DIR.mkdir(parents=True, exist_ok=True)
    
    timestamp = datetime.utcnow()
    timestamp_str = timestamp.strftime("%Y%m%d_%H%M%S")
    short_id = uuid.uuid4().hex[:4]
    safe_model = "".join(c if c.isalnum() or c in "-_" else "-" for c in (model_name or "unknown"))[:48]
    filename = f"{timestamp_str}-{short_id}_{safe_model}.err"
    error_path = ERROR_LOG_DIR / filename
    
    lines = [
        f"timestamp={timestamp.isoformat()}Z",
        f"model={model_name or 'unknown'}",
        f"error_type={error_type}",
        f"error_message={error_message}",
    ]
    
    if backend_name:
        lines.append(f"backend={backend_name}")
    if http_status is not None:
        lines.append(f"http_status={http_status}")
    if request_path:
        lines.append(f"request_path={request_path}")
    if request_log_path:
        lines.append(f"full_log={request_log_path.name}")
    if extra_context:
        for key, value in extra_context.items():
            lines.append(f"{key}={value}")
    
    content = "\n".join(lines) + "\n"
    
    # Write async if possible, sync otherwise
    try:
        loop = asyncio.get_running_loop()
        
        async def _write_error_log():
            def _write():
                error_path.write_text(content, encoding="utf-8")
            await asyncio.to_thread(_write)
        
        task = loop.create_task(_write_error_log())
        _register_background_task(task)
    except RuntimeError:
        # No event loop, write synchronously
        error_path.write_text(content, encoding="utf-8")


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
        self._current_backend: Optional[str] = None
        self._last_http_status: Optional[int] = None
        self._error_logged = False
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
        self._current_backend = backend_name
        self._append_text(
            f"=== BACKEND ATTEMPT {attempt} ===\nbackend={backend_name}\nurl={url}\n"
        )

    def record_backend_response(self, status: int, headers: Mapping[str, str], body: bytes) -> None:
        if self._finalized:
            return
        self._last_http_status = status
        header_dump = self._safe_json_dict(headers)
        self._append_text(f"status={status}\nresponse_headers={header_dump}\n")
        self._append_text(f"body_len={len(body)}\n-- RESPONSE BODY START --\n")
        if body:
            self._append_text(self._format_payload(body))
        self._append_text("-- RESPONSE BODY END --\n")

    def record_stream_headers(self, status: int, headers: Mapping[str, str]) -> None:
        if self._finalized:
            return
        self._last_http_status = status
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

    def record_error(self, message: str, error_type: Optional[str] = None) -> None:
        if self._finalized:
            return
        self._append_text(f"ERROR: {message}\n")
        
        # Also log to the errors directory (only once per request)
        if not self._error_logged:
            self._error_logged = True
            # Infer error type if not provided
            if error_type is None:
                if "SSE" in message or "stream error" in message.lower():
                    error_type = "sse_stream_error"
                elif "status" in message.lower():
                    error_type = "http_error"
                elif "timeout" in message.lower():
                    error_type = "timeout"
                elif "cancelled" in message.lower() or "disconnect" in message.lower():
                    error_type = "client_disconnect"
                else:
                    error_type = "unknown"
            
            _log_error_event(
                model_name=self.model_name,
                error_type=error_type,
                error_message=message,
                backend_name=self._current_backend,
                http_status=self._last_http_status,
                request_path=self.request_path,
                request_log_path=self.log_path,
            )

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
            # Mask sensitive information
            masked_data = {}
            for key, value in normalized.items():
                key_lower = key.lower()
                if key_lower in {"authorization", "proxy-connection"}:
                    # Mask authorization headers: first 3 chars + **** 
                    if isinstance(value, str) and value.startswith("Bearer "):
                        bearer_token = value[7:]  # Remove "Bearer " prefix
                        if bearer_token and bearer_token != "empty":
                            masked_value = bearer_token[:3] + "****"
                            masked_data[key] = f"Bearer {masked_value}"
                        else:
                            masked_data[key] = value
                    else:
                        masked_data[key] = value[:3] + "****" if len(value) > 3 else "****"
                elif key_lower == "host":
                    # Replace host with proxy_host for privacy
                    masked_data[key] = "proxy_host"
                else:
                    masked_data[key] = value
            return json.dumps(masked_data, sort_keys=True)
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
    logger = logging.getLogger("yallmp-proxy")
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
# Max bytes to buffer when checking for streaming errors before committing to client
STREAM_ERROR_CHECK_BUFFER_SIZE = 4096


def _detect_sse_stream_error(data: bytes) -> Optional[str]:
    """
    Check if buffered SSE data contains an error event.
    
    Returns an error message if an error is detected, None otherwise.
    
    Detects patterns like:
    - MiniMax: data: {"type":"error","error":{...}}
    - Generic: data: {"error":{...}}
    """
    try:
        text = data.decode("utf-8", errors="replace")
    except Exception:
        return None
    
    # Look for SSE data lines
    for line in text.split("\n"):
        line = line.strip()
        if not line.startswith("data:"):
            continue
        
        # Extract the JSON part after "data:"
        json_part = line[5:].strip()
        if not json_part or json_part == "[DONE]":
            continue
        
        try:
            parsed = json.loads(json_part)
        except json.JSONDecodeError:
            continue
        
        if not isinstance(parsed, dict):
            continue
        
        # Pattern 1: MiniMax-style {"type":"error", "error":{...}}
        if parsed.get("type") == "error":
            error_obj = parsed.get("error", {})
            error_msg = error_obj.get("message") or str(error_obj) if error_obj else "unknown error"
            http_code = error_obj.get("http_code", "unknown")
            return f"SSE stream error: {error_msg} (http_code={http_code})"
        
        # Pattern 2: Generic OpenAI-style {"error":{...}} in stream
        error_obj = parsed.get("error")
        if isinstance(error_obj, dict):
            error_msg = error_obj.get("message") or str(error_obj)
            error_type = error_obj.get("type", "unknown")
            return f"SSE stream error: {error_msg} (type={error_type})"
    
    return None


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


CONFIG_PATH = os.getenv("YALLMP_CONFIG", _default_config_path())


@dataclass
class Backend:
    name: str
    base_url: str
    api_key: str
    timeout: Optional[float]
    target_model: Optional[str]
    api_type: str = "openai"
    supports_reasoning: bool = False

    def build_url(self, path: str, query: str) -> str:
        base = self.base_url.rstrip("/")
        normalized_path = path or ""
        if not normalized_path.startswith("/"):
            normalized_path = f"/{normalized_path}"

        if normalized_path.startswith("/v1"):
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
                # Log connection/timeout errors
                if request_log:
                    error_type = "timeout" if isinstance(exc, httpx.TimeoutException) else "connection_error"
                    request_log.record_error(error_detail, error_type=error_type)

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
            if request_log:
                request_log.record_error(
                    f"{backend.name} returned retryable status {resp.status_code}",
                    error_type="http_retryable"
                )
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

            api_type = _extract_api_type(params)
            target_model = _extract_target_model(params, api_type)

            supports_reasoning = bool(params.get("supports_reasoning"))

            backends[name] = Backend(
                name=name,
                base_url=base,
                api_key=api_key,
                timeout=timeout_val,
                target_model=target_model,
                api_type=api_type,
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


def _extract_api_type(params: Mapping[str, Any]) -> str:
    raw_api_type = params.get("api_type")
    if raw_api_type is None:
        return "openai"
    normalized = str(raw_api_type).strip().lower()
    return normalized or "openai"


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
    api_type = _extract_api_type(params)
    target_model = _extract_target_model(params, api_type)
    supports_reasoning = bool(params.get("supports_reasoning") or payload.get("supports_reasoning"))
    fallbacks = _normalize_fallbacks(payload.get("fallbacks"))

    backend = Backend(
        name=model_name,
        base_url=api_base,
        api_key=api_key,
        timeout=timeout,
        target_model=target_model,
        api_type=api_type,
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
        # Strip accept-encoding so backends return uncompressed data - we filter
        # content-encoding on the response, so passing compressed data through
        # would break clients that can't decode it without the header.
        if key_lower in HOP_BY_HOP_HEADERS or key_lower in {"authorization", "host", "content-length", "accept-encoding"}:
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
    # Explicitly request uncompressed responses - httpx adds Accept-Encoding by
    # default, and we strip content-encoding from responses, so compressed data
    # would be passed through without the client knowing how to decode it.
    headers["Accept-Encoding"] = "identity"
    return headers


def _normalize_request_model(model_name: str) -> str:
    """Normalize client-supplied model name for routing (accepts legacy openai/ prefix)."""
    if not isinstance(model_name, str):
        return ""
    stripped = model_name.strip()
    if not stripped:
        return ""

    lower = stripped.lower()
    if "/" in stripped:
        prefix, remainder = stripped.split("/", 1)
        if remainder and prefix.lower() in {"openai"}:
            return remainder
    return stripped


def _extract_target_model(params: Mapping[str, Any], api_type: Optional[str] = None) -> Optional[str]:
    override = params.get("target_model") or params.get("forward_model")
    if override:
        override_str = str(override).strip()
        if override_str:
            return override_str

    raw_model = str(params.get("model") or "").strip()
    if not raw_model:
        return None

    normalized_api_type = str(api_type or params.get("api_type") or "openai").strip().lower() or "openai"
    expected_prefix = f"{normalized_api_type}/"
    lower_model = raw_model.lower()

    if lower_model.startswith(expected_prefix):
        remainder = raw_model[len(expected_prefix):]
        if remainder:
            return remainder

    if lower_model.startswith("openai/"):
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
            request_log.record_error(
                f"stream request returned retryable status {resp.status_code}",
                error_type="http_retryable"
            )
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

    # Buffer initial chunks to detect SSE errors before committing to stream
    # This catches APIs that return HTTP 200 but send error payloads in the stream
    initial_buffer = bytearray()
    buffered_chunks: List[bytes] = []
    stream = resp.aiter_raw()
    stream_exhausted = False
    
    while len(initial_buffer) < STREAM_ERROR_CHECK_BUFFER_SIZE:
        try:
            chunk = await stream.__anext__()
        except StopAsyncIteration:
            stream_exhausted = True
            break
        if chunk:
            initial_buffer.extend(chunk)
            buffered_chunks.append(chunk)
            if request_log:
                request_log.record_stream_chunk(chunk)
    
    # Check for SSE error patterns in the buffered data
    sse_error = _detect_sse_stream_error(bytes(initial_buffer))
    if sse_error:
        logger.warning(f"Detected SSE error in stream from {url}: {sse_error}")
        
        # Log the SSE error event
        if request_log:
            request_log.record_error(sse_error, error_type="sse_stream_error")
        
        # Read the rest of the stream for logging purposes
        remaining_data = bytearray(initial_buffer)
        if not stream_exhausted:
            try:
                async for chunk in stream:
                    if chunk:
                        remaining_data.extend(chunk)
                        if request_log:
                            request_log.record_stream_chunk(chunk)
            except Exception:
                pass  # Best effort to capture remaining data
        await close_stream()
        
        # Build a response to return if all backends fail
        response = Response(
            content=bytes(remaining_data),
            status_code=resp.status_code,
            headers=_filter_response_headers(resp.headers),
            media_type=resp.headers.get("content-type", "text/event-stream"),
        )
        raise BackendRetryableError(sse_error, response=response)

    logger.info(f"Streaming request to {url} successful, status {resp.status_code}")
    headers_to_client = _filter_response_headers(resp.headers)
    media_type = headers_to_client.pop("content-type", None)

    async def iterator():
        nonlocal stream_exhausted
        try:
            chunk_count = 0
            
            # First yield the buffered chunks
            for chunk in buffered_chunks:
                chunk_count += 1
                yield chunk
            
            # Then continue with the rest of the stream
            if not stream_exhausted:
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


logger.info("Initializing yaLLMp Proxy")
config = _load_config(CONFIG_PATH)
router = ProxyRouter(config)
logger.info(f"Proxy router initialized with {len(router.backends)} backends")
app = FastAPI(title="yaLLMp Proxy")
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
    # Print awesome ASCII art banner
    print("""
╔═════════════════════════════════════════╗
║                                         ║
║    ||  Y(et) A(nother) LLM proxy ||     ║
║                                         ║
║   =(^_^)=                    =(^_^)=    ║
║                    =(^_^)=              ║
║        =(^_^)=                =(^_^)=   ║
╚═════════════════════════════════════════╝
    """)
    
    logger.info("yaLLMp Proxy server starting up...")
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
    logger.info("yaLLMp Proxy server ready to handle requests")


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

    raw_model_name = payload.get("model")
    if not isinstance(raw_model_name, str) or not raw_model_name:
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

    model_name = _normalize_request_model(raw_model_name)

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
            "owned_by": "yallmp-proxy"
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
