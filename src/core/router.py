"""Router for handling model routing and fallback logic."""

import asyncio
import json
import logging
from typing import Any, Awaitable, Callable, Dict, List, Mapping, Optional

import httpx
from fastapi import HTTPException, Response

from .backend import (
    Backend,
    build_backend_body,
    build_outbound_headers,
    filter_response_headers,
    format_httpx_error,
    _safe_headers_for_log,
    _parse_bool,
)
from .exceptions import BackendRetryableError

logger = logging.getLogger("yallmp-proxy")

from .sse import SSEJSONDecoder, detect_sse_stream_error
from ..modules import (
    ModuleContext,
    build_request_module_overrides,
    build_request_module_pipeline,
    build_response_module_overrides,
    build_response_module_pipeline,
)
from .upstream_transport import get_upstream_transport

DEFAULT_TIMEOUT = 30


class ProxyRouter:
    """Routes requests to backends with fallback support."""
    
    def __init__(self, config: Dict) -> None:
        self.backends = self._parse_backends(config.get("model_list", []))
        if not self.backends:
            raise RuntimeError("No backends found in config")
        self._lock = asyncio.Lock()
        self.response_modules = build_response_module_pipeline(config)
        self.response_module_overrides = build_response_module_overrides(config)
        # Backwards-compatible aliases (parsers -> modules)
        self.response_parsers = self.response_modules
        self.response_parser_overrides = self.response_module_overrides
        self.request_modules = build_request_module_pipeline(config)
        self.request_module_overrides = build_request_module_overrides(config)

        proxy_settings = config.get("proxy_settings") or {}
        logging_cfg = proxy_settings.get("logging") or {}
        self.log_parsed_response = _parse_bool(logging_cfg.get("log_parsed_response"))
        log_parsed_stream_raw = logging_cfg.get("log_parsed_stream")
        if log_parsed_stream_raw is None:
            self.log_parsed_stream = self.log_parsed_response
        else:
            self.log_parsed_stream = _parse_bool(log_parsed_stream_raw)
        log_to_disk_raw = logging_cfg.get("log_to_disk")
        if log_to_disk_raw is None:
            self.log_to_disk = True
        else:
            self.log_to_disk = _parse_bool(log_to_disk_raw)

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
        request_log: Optional[Any] = None,
        disconnect_checker: Optional[Callable[[], Awaitable[bool]]] = None,
    ) -> Response:
        logger.info(
            f"Received request for model: {model_name}, path: {path}, stream: {is_stream}"
        )

        if request_log:
            request_log.configure_parsed_logging(
                self.log_parsed_response, self.log_parsed_stream
            )
            request_log.configure_disk_logging(self.log_to_disk)
        
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
                    model_name=model_name,
                    request_log=request_log,
                    disconnect_checker=disconnect_checker,
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

    async def register_backend(
        self, backend: Backend, fallbacks: Optional[List[str]]
    ) -> bool:
        """Register or replace a backend at runtime. Returns True if replaced."""
        async with self._lock:
            replaced = backend.name in self.backends
            self.backends[backend.name] = backend
            if fallbacks is not None:
                self.fallbacks[backend.name] = fallbacks
            return replaced

    async def unregister_backend(self, backend_name: str) -> bool:
        """Unregister a backend at runtime. Returns True if backend existed and was removed."""
        async with self._lock:
            if backend_name in self.backends:
                del self.backends[backend_name]
                self.fallbacks.pop(backend_name, None)
                return True
            return False

    async def reload_config(self, new_config: dict) -> None:
        """Reload router with new configuration atomically.

        Re-parses backends, fallbacks, and response modules from the config.
        This method is safe to call while requests are being processed.

        Args:
            new_config: The new configuration dictionary.
        """
        async with self._lock:
            self.backends = self._parse_backends(new_config.get("model_list", []))
            self.response_modules = build_response_module_pipeline(new_config)
            self.response_module_overrides = build_response_module_overrides(new_config)
            # Backwards-compatible aliases (parsers -> modules)
            self.response_parsers = self.response_modules
            self.response_parser_overrides = self.response_module_overrides
            self.request_modules = build_request_module_pipeline(new_config)
            self.request_module_overrides = build_request_module_overrides(new_config)
            router_cfg = new_config.get("router_settings") or {}
            self.fallbacks = self._parse_fallbacks(router_cfg.get("fallbacks", []))

            proxy_settings = new_config.get("proxy_settings") or {}
            logging_cfg = proxy_settings.get("logging") or {}
            self.log_parsed_response = _parse_bool(logging_cfg.get("log_parsed_response"))
            log_parsed_stream_raw = logging_cfg.get("log_parsed_stream")
            if log_parsed_stream_raw is None:
                self.log_parsed_stream = self.log_parsed_response
            else:
                self.log_parsed_stream = _parse_bool(log_parsed_stream_raw)
            log_to_disk_raw = logging_cfg.get("log_to_disk")
            if log_to_disk_raw is None:
                self.log_to_disk = True
            else:
                self.log_to_disk = _parse_bool(log_to_disk_raw)

            router_cfg = new_config.get("router_settings") or {}
            self.num_retries = max(1, int(router_cfg.get("num_retries", 1)))

    def _select_response_modules(self, backend_name: str):
        if backend_name in self.response_module_overrides:
            return self.response_module_overrides[backend_name]
        return self.response_modules

    def _select_response_parsers(self, backend_name: str):
        return self._select_response_modules(backend_name)

    def _select_request_modules(self, backend_name: str):
        if backend_name in self.request_module_overrides:
            return self.request_module_overrides[backend_name]
        return self.request_modules

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
        model_name: str,
        request_log: Optional[Any] = None,
        disconnect_checker: Optional[Callable[[], Awaitable[bool]]] = None,
    ) -> Response:
        from .backend import DEFAULT_RETRY_DELAY, MAX_RETRY_DELAY
        
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
                    model_name,
                    request_log,
                    disconnect_checker,
                )
                logger.info(f"Backend {backend.name} succeeded on attempt {attempt + 1}")
                return response
            except BackendRetryableError as exc:
                last_error = exc
                logger.warning(f"Backend {backend.name} attempt {attempt + 1} failed: {exc}")
            except httpx.HTTPError as exc:
                error_detail = format_httpx_error(
                    exc, backend, url=backend.build_url(path, query)
                )
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
        model_name: str,
        request_log: Optional[Any] = None,
        disconnect_checker: Optional[Callable[[], Awaitable[bool]]] = None,
    ) -> Response:
        from .backend import DEFAULT_TIMEOUT
        
        url = backend.build_url(path, query)
        outbound_headers = build_outbound_headers(
            headers,
            backend.api_key,
            is_stream=is_stream,
            api_type=backend.api_type,
            anthropic_version=backend.anthropic_version,
        )
        request_pipeline = self._select_request_modules(backend.name)
        if request_pipeline:
            request_context = ModuleContext(
                path=path,
                model=model_name,
                backend=backend.name,
                is_stream=is_stream,
            )
            content_type = (
                headers.get("content-type")
                or headers.get("Content-Type")
                or ""
            )
            if not content_type or "application/json" in content_type.lower():
                updated_payload = request_pipeline.transform_request_payload(
                    payload, request_context
                )
                if updated_payload is not None:
                    payload = updated_payload
                    try:
                        body = json.dumps(updated_payload, ensure_ascii=False).encode("utf-8")
                    except (TypeError, ValueError):
                        logger.warning(
                            "Failed to encode updated request payload from modules; "
                            "falling back to original body."
                        )
        outbound_body = build_backend_body(
            payload, backend, body, is_stream=is_stream
        )
        timeout = backend.timeout or DEFAULT_TIMEOUT
        if request_log:
            request_log.record_backend_attempt(backend.name, attempt_number, url)

        logger.debug(
            f"Executing request to {url} with timeout {timeout}s, stream: {is_stream}"
        )
        if logger.isEnabledFor(logging.DEBUG):
            logger.debug(
                "Outbound headers for %s: %s",
                backend.name,
                _safe_headers_for_log(outbound_headers),
            )
        logger.debug(
            f"HTTP/2 enabled for {backend.name}: {backend.http2}"
        )

        if is_stream:
            logger.debug(f"Initiating streaming request to {url}")
            logger.debug(f"Body size for {backend.name}: {len(outbound_body)} bytes")
            pipeline = self._select_response_parsers(backend.name)
            parser_context = ModuleContext(
                path=path,
                model=model_name,
                backend=backend.name,
                is_stream=is_stream,
            )
            try:
                return await _streaming_request(
                    url,
                    outbound_headers,
                    outbound_body,
                    timeout,
                    http2=backend.http2,
                    request_log=request_log,
                    disconnect_checker=disconnect_checker,
                    parser_pipeline=pipeline,
                    parser_context=parser_context,
                )
            except httpx.HTTPError as exc:
                logger.error(
                    "HTTP error during streaming to %s: %s (type: %s)",
                    url,
                    str(exc),
                    exc.__class__.__name__,
                )
                if backend.http2:
                    logger.warning(
                        "HTTP/2 stream to %s failed (%s); retrying with HTTP/1.1",
                        url,
                        exc.__class__.__name__,
                    )
                    return await _streaming_request(
                        url,
                        outbound_headers,
                        outbound_body,
                        timeout,
                        http2=False,
                        request_log=request_log,
                        disconnect_checker=disconnect_checker,
                        parser_pipeline=pipeline,
                        parser_context=parser_context,
                    )
                raise

        logger.debug(f"Initiating non-streaming request to {url}")
        transport = get_upstream_transport(url)

        async def _post(http2_enabled: bool) -> httpx.Response:
            async with httpx.AsyncClient(
                timeout=timeout, http2=http2_enabled, transport=transport, follow_redirects=True
            ) as client:
                return await client.post(
                    url, headers=outbound_headers, content=outbound_body
                )

        try:
            resp = await _post(backend.http2)
        except httpx.HTTPError as exc:
            if backend.http2:
                logger.warning(
                    "HTTP/2 request to %s failed (%s); retrying with HTTP/1.1",
                    url,
                    exc.__class__.__name__,
                )
                resp = await _post(False)
            else:
                raise

        logger.debug(f"Received response from {url}: status {resp.status_code}")

        if request_log:
            request_log.record_backend_response(resp.status_code, resp.headers, resp.content)

        if resp.status_code in {408, 409, 429, 500, 502, 503, 504}:
            if request_log:
                request_log.record_error(
                    f"{backend.name} returned retryable status {resp.status_code}",
                    error_type="http_retryable"
                )
            response = _build_response_from_httpx(resp)
            raise BackendRetryableError(
                f"{backend.name} returned status {resp.status_code}", response=response
            )

        pipeline = self._select_response_parsers(backend.name)
        parser_context = ModuleContext(
            path=path,
            model=model_name,
            backend=backend.name,
            is_stream=is_stream,
        )
        parsed_body = pipeline.transform_response_body(
            resp.content, resp.headers.get("content-type"), parser_context
        )

        # Extract logging data from response (works with or without parsed_body)
        if request_log:
            # Use parsed_body if available, otherwise fall back to raw content
            body_to_log = parsed_body if parsed_body is not None else resp.content
            if parsed_body is not None:
                request_log.record_parsed_response(
                    resp.status_code, resp.headers, parsed_body
                )
            # Extract usage stats and response content for logging
            try:
                import json as json_module
                payload = json_module.loads(body_to_log)
                if isinstance(payload, dict):
                    if "usage" in payload:
                        request_log.record_usage_stats(payload["usage"])

                    # Detect anthropic format: has "type": "message" or
                    # has "content" + "stop_reason" at top level without "choices"
                    is_anthropic = (
                        payload.get("type") == "message"
                        or (
                            "content" in payload
                            and "stop_reason" in payload
                            and "choices" not in payload
                        )
                    )

                    if is_anthropic:
                        # Anthropic format: extract stop_reason directly
                        stop_reason = payload.get("stop_reason")
                        if isinstance(stop_reason, str) and stop_reason:
                            request_log.record_stop_reason(stop_reason)
                        # Extract content for full_response accumulation
                        content = payload.get("content", [])
                        if isinstance(content, list):
                            for block in content:
                                if isinstance(block, dict) and block.get("type") == "text":
                                    text = block.get("text", "")
                                    if text:
                                        request_log._accumulated_response_parts.append(text)
                    else:
                        # OpenAI format: extract from choices[0]
                        choices = payload.get("choices")
                        if isinstance(choices, list) and choices:
                            choice = choices[0]
                            if isinstance(choice, dict):
                                finish_reason = (
                                    choice.get("finish_reason")
                                    or choice.get("stop_reason")
                                    or choice.get("reason")
                                )
                                if isinstance(finish_reason, str) and finish_reason:
                                    request_log.record_stop_reason(finish_reason)
                                # Extract message content for full_response accumulation
                                message = choice.get("message")
                                if isinstance(message, dict):
                                    content = message.get("content")
                                    if isinstance(content, str) and content:
                                        request_log._accumulated_response_parts.append(content)
            except (json_module.JSONDecodeError, TypeError):
                pass

        if parsed_body is not None:
            return _build_response_from_httpx(resp, parsed_body)
        return _build_response_from_httpx(resp)

    @staticmethod
    def _parse_backends(entries: List[Dict]) -> Dict[str, Backend]:
        backends: Dict[str, Backend] = {}
        for entry in entries:
            name = entry.get("model_name")
            params = entry.get("model_params") or {}
            base = (params.get("api_base") or "").strip()
            if not name or not base:
                continue
            api_key = str(params.get("api_key") or "")
            timeout = params.get("request_timeout")
            try:
                timeout_val = float(timeout) if timeout is not None else None
            except (TypeError, ValueError):
                timeout_val = None

            from .backend import (
                extract_api_type,
                extract_target_model,
                _parse_bool,
                ParameterConfig,
            )

            api_type = extract_api_type(params)
            target_model = extract_target_model(params, api_type)
            anthropic_version = params.get("anthropic_version")
            if isinstance(anthropic_version, str):
                anthropic_version = anthropic_version.strip() or None
            elif anthropic_version is not None:
                anthropic_version = str(anthropic_version).strip() or None

            supports_reasoning = bool(params.get("supports_reasoning"))
            supports_responses_api = _parse_bool(params.get("supports_responses_api"))
            http2 = _parse_bool(params.get("http2"))
            editable = _parse_bool(entry.get("editable"))

            # Parse parameter overrides (support top-level or model_params.parameters)
            param_configs: Dict[str, ParameterConfig] = {}
            if "parameters" in entry:
                raw_params = entry.get("parameters") or {}
            else:
                raw_params = params.get("parameters") or {}
            if not isinstance(raw_params, dict):
                raw_params = {}
            for param_name, param_config in raw_params.items():
                if isinstance(param_config, dict):
                    default = param_config.get("default")
                    allow_override = _parse_bool(param_config.get("allow_override", True))
                    param_configs[param_name] = ParameterConfig(
                        default=default,
                        allow_override=allow_override,
                    )

            backends[name] = Backend(
                name=name,
                base_url=base,
                api_key=api_key,
                timeout=timeout_val,
                target_model=target_model,
                api_type=api_type,
                anthropic_version=anthropic_version,
                supports_reasoning=supports_reasoning,
                supports_responses_api=supports_responses_api,
                http2=http2,
                editable=editable,
                parameters=param_configs,
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


def _build_response_from_httpx(resp: httpx.Response, content: Optional[bytes] = None) -> Response:
    from .backend import filter_response_headers
    
    body = content if content is not None else resp.content
    headers = filter_response_headers(resp.headers)
    media_type = headers.get("content-type")
    return Response(content=body, status_code=resp.status_code, headers=headers, media_type=media_type)


async def _streaming_request(
    url: str,
    headers: dict[str, str],
    body: bytes,
    timeout: float,
    http2: bool = False,
    request_log: Optional[Any] = None,
    disconnect_checker: Optional[Callable[[], Awaitable[bool]]] = None,
    parser_pipeline: Optional[Any] = None,
    parser_context: Optional[ModuleContext] = None,
) -> Response:
    from .backend import (
        DEFAULT_RETRY_DELAY,
        MAX_RETRY_DELAY,
        RETRYABLE_STATUSES,
        filter_response_headers,
    )
    
    from fastapi.responses import StreamingResponse
    
    logger.debug(f"Setting up streaming client for {url}")
    logger.debug(f"Stream timeout config - connect={timeout}s, read=None, write={timeout}s, pool={timeout}s")
    logger.debug(f"HTTP/2 client: {http2}")
    logger.debug(f"Request body size: {len(body)} bytes")
    stream_timeout = httpx.Timeout(
        connect=timeout, read=None, write=timeout, pool=timeout
    )
    transport = get_upstream_transport(url)
    client = httpx.AsyncClient(timeout=stream_timeout, http2=http2, transport=transport, follow_redirects=True)
    try:  # TODO: use context manager 'with httpx.AsyncClient'
        request = client.build_request("POST", url, headers=headers, content=body)

        logger.debug(f"Sending streaming request to {url}")
        logger.debug(f"Request method: {request.method}")
        logger.debug(f"Request URL: {request.url}")
        if logger.isEnabledFor(logging.DEBUG):
            logger.debug("Request headers: %s", _safe_headers_for_log(request.headers))
        logger.debug(f"Request content-length: {request.headers.get('content-length', 'not set')}")
        logger.debug(f"About to send request, stream=True")
        resp = await client.send(request, stream=True)
        if logger.isEnabledFor(logging.DEBUG):
            logger.debug(
                "Received initial response from %s: status=%s, headers=%s",
                url,
                resp.status_code,
                _safe_headers_for_log(resp.headers),
            )
    except Exception as exc:
        logger.error(f"Failed to send streaming request to {url}: {exc} (type: {exc.__class__.__name__})")
        await client.aclose()
        raise
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
        logger.warning(
            f"Streaming request to {url} returned retryable status {resp.status_code}"
        )
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
        logger.warning(
            f"Streaming request to {url} returned error status {resp.status_code}"
        )
        data = await resp.aread()
        await close_stream()
        if request_log:
            request_log.record_backend_response(resp.status_code, resp.headers, data)
            if not request_log.finalized:
                request_log.record_error(f"stream response status {resp.status_code}")
                request_log.finalize("error")
        return _build_response_from_httpx(resp, data)

    # Buffer initial chunks to detect SSE errors before committing to stream
    from .sse import STREAM_ERROR_CHECK_BUFFER_SIZE
    
    initial_buffer = bytearray()
    buffered_chunks: List[bytes] = []
    # Use decoded bytes so content-encoding doesn't leak compressed data to clients.
    stream = resp.aiter_bytes()
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
    sse_error = detect_sse_stream_error(bytes(initial_buffer))
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
            headers=filter_response_headers(resp.headers),
            media_type=resp.headers.get("content-type", "text/event-stream"),
        )
        raise BackendRetryableError(sse_error, response=response)

    logger.info(f"Streaming request to {url} successful, status {resp.status_code}")
    headers_to_client = filter_response_headers(resp.headers)
    # Force connection close after streaming to prevent connection reuse issues
    # This avoids ClientDisconnect errors on subsequent requests using the same connection
    headers_to_client["connection"] = "close"
    media_type = headers_to_client.pop("content-type", None)
    stream_parser = None
    if parser_pipeline and parser_context:
        stream_parser = parser_pipeline.create_stream_parser(parser_context)
    if request_log and stream_parser:
        request_log.record_parsed_stream_headers(resp.status_code, resp.headers)

    def _process_chunk(chunk: bytes) -> list[bytes]:
        if not stream_parser:
            return [chunk]
        parsed_chunks = stream_parser.feed_bytes(chunk)
        if not parsed_chunks:
            return []
        combined = b"".join(parsed_chunks)
        if request_log:
            request_log.record_parsed_stream_chunk(combined)
        return [combined]

    async def iterator():
        nonlocal stream_exhausted
        # Track final chunk data for stop_reason and usage extraction
        last_chunk_data: Optional[dict[str, Any]] = None
        last_choice_data: Optional[dict[str, Any]] = None
        last_finish_reason: Optional[str] = None
        last_usage_stats: Optional[dict[str, Any]] = None
        sse_decoder = SSEJSONDecoder() if request_log else None
        stop_early = False
        stop_reason: Optional[str] = None
        first_content_detected = False

        def _extract_finish_reason(choice: Mapping[str, Any]) -> Optional[str]:
            finish_reason = (
                choice.get("finish_reason")
                or choice.get("stop_reason")
                or choice.get("reason")
            )
            if isinstance(finish_reason, str) and finish_reason:
                return finish_reason
            return None

        def _parse_payloads(chunk: bytes) -> list[dict[str, Any]]:
            payloads: list[dict[str, Any]] = []
            if sse_decoder:
                payloads.extend(sse_decoder.feed(chunk))
            if payloads:
                return payloads
            # Fallback for non-SSE streaming providers
            stripped = chunk.lstrip()
            if stripped.startswith(b"{") or stripped.startswith(b"["):
                try:
                    parsed = json.loads(stripped.decode("utf-8"))
                except (json.JSONDecodeError, UnicodeDecodeError):
                    return []
                if isinstance(parsed, dict):
                    return [parsed]
            return []

        def _handle_payload(payload: dict[str, Any], chunk_index: int) -> None:
            nonlocal last_chunk_data, last_choice_data, last_finish_reason, last_usage_stats, first_content_detected
            last_chunk_data = payload

            if not isinstance(payload, dict):
                return

            # Track usage stats from payloads (usually only in final chunk)
            if "usage" in payload:
                usage = payload["usage"]
                if isinstance(usage, dict):
                    last_usage_stats = usage

            # Anthropic streaming format detection
            # Known Anthropic SSE event types:
            # - message_start: initial message metadata
            # - content_block_start: start of a content block
            # - content_block_delta: content update (text_delta or input_json_delta)
            # - content_block_stop: end of content block
            # - message_delta: final stop_reason and usage
            # - message_stop: end of message
            event_type = payload.get("type")
            is_anthropic_event = False
            if event_type == "content_block_delta":
                is_anthropic_event = True
                delta = payload.get("delta", {})
                if isinstance(delta, dict):
                    delta_type = delta.get("type")
                    if delta_type == "text_delta":
                        text = delta.get("text", "")
                        if text:
                            # Record first content time for TPS calculation
                            if not first_content_detected and request_log:
                                first_content_detected = True
                                request_log.record_first_content_time()
                            if request_log:
                                request_log._accumulated_response_parts.append(text)
                    elif delta_type == "input_json_delta":
                        # Tool use input streaming - could accumulate if needed
                        pass
            elif event_type == "message_delta":
                is_anthropic_event = True
                delta = payload.get("delta", {})
                if isinstance(delta, dict):
                    stop_reason = delta.get("stop_reason")
                    if isinstance(stop_reason, str) and stop_reason:
                        last_finish_reason = stop_reason
                # Usage is at top level of message_delta, not inside delta
                # (already captured above)
            elif event_type in ("message_start", "content_block_start", "content_block_stop", "message_stop"):
                is_anthropic_event = True
                # Could extract message id/model from payload["message"] if needed

            # Return early only for actual Anthropic events - they don't have "choices"
            # Other providers may use "type" field for different purposes
            if is_anthropic_event:
                return

            # OpenAI streaming format: extract from choices array
            choices = payload.get("choices")
            if not (choices and isinstance(choices, list)):
                return
            last_choice_data = payload
            for choice in choices:
                if not isinstance(choice, dict):
                    continue
                delta = choice.get("delta")
                if isinstance(delta, dict):
                    # Detect first content for TPS calculation
                    if not first_content_detected and request_log:
                        content = delta.get("content")
                        reasoning_content = delta.get("reasoning_content")
                        if content or reasoning_content:
                            first_content_detected = True
                            request_log.record_first_content_time()
                    request_log.record_stream_delta(delta, chunk_index)
                finish_reason = _extract_finish_reason(choice)
                if finish_reason:
                    last_finish_reason = finish_reason

        def _stop_chunks(reason: str) -> list[bytes]:
            chunks: list[bytes] = []
            if stream_parser and stream_parser.should_emit_finish_event(reason):
                finish_chunk = stream_parser.build_finish_event(reason)
                chunks.append(finish_chunk)
            chunks.append(b"data: [DONE]\n\n")
            return chunks

        try:
            chunk_count = 0

            # First yield the buffered chunks
            for chunk in buffered_chunks:
                chunk_count += 1
                # Parse the chunk to extract delta for accumulation
                if request_log:
                    for payload in _parse_payloads(chunk):
                        _handle_payload(payload, chunk_count)
                for out_chunk in _process_chunk(chunk):
                    yield out_chunk
                if stream_parser and stream_parser.stop_requested and not stop_early:
                    stop_reason = stream_parser.stop_reason or stream_parser._last_finish_reason or "stop"
                    stop_early = True
                    last_finish_reason = stop_reason
                    logger.debug(
                        "Stream stop triggered. stop_source=%s, stop_reason=%s",
                        stream_parser.stop_source,
                        stop_reason,
                    )
                    if request_log:
                        request_log.record_stop_reason(stop_reason)
                        if stop_reason in ("tool_calls", "function_call", "tool_use"):
                            request_log.mark_as_tool_call()
                        # Record usage stats on early stop from buffered chunks
                        if last_usage_stats:
                            logger.debug(
                                "Recording usage stats from buffered early stop: %s",
                                last_usage_stats,
                            )
                            request_log.record_usage_stats(last_usage_stats)
                        elif sse_decoder:
                            for payload in sse_decoder.flush():
                                _handle_payload(payload, chunk_count)
                            if last_usage_stats:
                                request_log.record_usage_stats(last_usage_stats)
                    await close_stream()
                    for out_chunk in _stop_chunks(stop_reason):
                        if request_log:
                            request_log.record_parsed_stream_chunk(out_chunk)
                        yield out_chunk
                    break

            # Then continue with the rest of the stream
            if not stream_exhausted and not stop_early:
                while True:
                    if disconnect_checker and await disconnect_checker():
                        raise asyncio.CancelledError("client disconnected")
                    try:
                        chunk = await stream.__anext__()
                    except StopAsyncIteration:
                        break
                    if chunk:
                        chunk_count += 1
                        if chunk_count % 10 == 0:
                            logger.debug(f"Streamed {chunk_count} chunks from {url}")

                        # Parse the chunk to extract delta for accumulation
                        if request_log:
                            for payload in _parse_payloads(chunk):
                                _handle_payload(payload, chunk_count)

                        if request_log:
                            request_log.record_stream_chunk(chunk)
                        for out_chunk in _process_chunk(chunk):
                            yield out_chunk
                        if stream_parser and stream_parser.stop_requested and not stop_early:
                            stop_reason = stream_parser.stop_reason or stream_parser._last_finish_reason or "stop"
                            stop_early = True
                            last_finish_reason = stop_reason
                            logger.debug(
                                "Stream stop triggered. stop_source=%s, stop_reason=%s",
                                stream_parser.stop_source,
                                stop_reason,
                            )
                            if request_log:
                                request_log.record_stop_reason(stop_reason)
                                if stop_reason in ("tool_calls", "function_call", "tool_use"):
                                    request_log.mark_as_tool_call()
                                # Record module logs on early stop
                                if stream_parser:
                                    modules_log = stream_parser.get_module_logs()
                                    if modules_log:
                                        request_log.record_modules_log(modules_log)
                                # Record usage stats on early stop (may have been captured in final chunk)
                                if last_usage_stats:
                                    logger.debug(
                                        "Recording usage stats from early stop: %s",
                                        last_usage_stats,
                                    )
                                    request_log.record_usage_stats(last_usage_stats)
                                elif sse_decoder:
                                    # Flush SSE decoder to get any remaining buffered payloads
                                    for payload in sse_decoder.flush():
                                        _handle_payload(payload, chunk_count)
                                    if last_usage_stats:
                                        logger.debug(
                                            "Recording usage stats from flush on early stop: %s",
                                            last_usage_stats,
                                        )
                                        request_log.record_usage_stats(last_usage_stats)
                            await close_stream()
                            for out_chunk in _stop_chunks(stop_reason):
                                if request_log:
                                    request_log.record_parsed_stream_chunk(out_chunk)
                                yield out_chunk
                            break
                    if stop_early:
                        break

            if not stop_early:
                # Finalize stream parser and get any final events
                if stream_parser:
                    for out_chunk in stream_parser.finish():
                        # Parse final events to extract stop_reason
                        if request_log and last_chunk_data is None:
                            try:
                                event_data = json.loads(out_chunk.decode("utf-8"))
                                if isinstance(event_data, dict):
                                    last_chunk_data = event_data
                            except (json.JSONDecodeError, UnicodeDecodeError):
                                pass
                        if request_log:
                            request_log.record_parsed_stream_chunk(out_chunk)
                        yield out_chunk

                    # If we saw tool calls but finish_reason wasn't already emitted,
                    # emit a finish event with tool_calls
                    if stream_parser.stop_reason == "tool_calls":
                        if stream_parser.should_emit_finish_event("tool_calls"):
                            finish_chunk = stream_parser.build_finish_event("tool_calls")
                            if request_log:
                                request_log.record_parsed_stream_chunk(finish_chunk)
                            yield finish_chunk
                        # Emit [DONE] if upstream didn't send it
                        if not stream_parser._saw_done:
                            done_chunk = b"data: [DONE]\n\n"
                            if request_log:
                                request_log.record_parsed_stream_chunk(done_chunk)
                            yield done_chunk
                        # Update last_finish_reason for logging
                        last_finish_reason = "tool_calls"

                    # Log stop_source for debugging
                    if stream_parser.stop_source:
                        logger.debug(
                            "Stream ended. stop_source=%s, stop_reason=%s",
                            stream_parser.stop_source,
                            stream_parser.stop_reason,
                        )

                # Extract stop_reason and usage from the final chunk
                if request_log:
                    finish_reason = last_finish_reason
                    choice_for_debug: Optional[Mapping[str, Any]] = None
                    if not finish_reason:
                        source_payload = last_choice_data or last_chunk_data
                        choices = (
                            source_payload.get("choices")
                            if isinstance(source_payload, dict)
                            else None
                        )
                        if choices and isinstance(choices, list) and len(choices) > 0:
                            choice = choices[0]
                            if isinstance(choice, dict):
                                choice_for_debug = choice
                                finish_reason = _extract_finish_reason(choice)

                    if finish_reason:
                        if choice_for_debug:
                            logger.debug(
                                "Extracted stop_reason: %s from choice fields: %s",
                                finish_reason,
                                list(choice_for_debug.keys()),
                            )
                        request_log.record_stop_reason(finish_reason)
                        # Also mark as tool call if the reason indicates tool usage
                        if finish_reason in ("tool_calls", "function_call", "tool_use"):
                            request_log.mark_as_tool_call()
                    elif choice_for_debug:
                        # Log all available fields when no stop reason found
                        logger.debug(
                            "No stop_reason found in choice. Available fields: %s",
                            list(choice_for_debug.keys()),
                        )
                        for key in choice_for_debug.keys():
                            if "stop" in key.lower() or "finish" in key.lower():
                                logger.debug(
                                    "Field '%s' has value: %s",
                                    key,
                                    choice_for_debug.get(key),
                                )

                    # Flush SSE decoder to get any remaining buffered payloads
                    # (final chunk may not end with \n\n)
                    if sse_decoder and not last_usage_stats:
                        for payload in sse_decoder.flush():
                            _handle_payload(payload, chunk_count)

                    # Record usage stats from the final chunk
                    if last_usage_stats:
                        logger.debug(
                            "Recording usage stats from stream: %s",
                            last_usage_stats,
                        )
                        request_log.record_usage_stats(last_usage_stats)
                    else:
                        # Try to extract usage from the last payload as fallback
                        if last_chunk_data and isinstance(last_chunk_data, dict) and "usage" in last_chunk_data:
                            usage = last_chunk_data.get("usage")
                            if isinstance(usage, dict):
                                logger.debug(
                                    "Recording usage stats from last chunk: %s",
                                    usage,
                                )
                                request_log.record_usage_stats(usage)

                    # Record module processing logs
                    if stream_parser:
                        modules_log = stream_parser.get_module_logs()
                        if modules_log:
                            logger.debug(
                                "Recording module logs: %s events",
                                modules_log.get("total_events", 0),
                            )
                            request_log.record_modules_log(modules_log)

        except asyncio.CancelledError as cancel_exc:
            logger.info(f"Streaming request to {url} cancelled by client")
            if request_log and not request_log.finalized:
                request_log.record_generation_end_time()
                request_log.record_error("stream cancelled by client")
                request_log.finalize("cancelled")
            raise cancel_exc
        except Exception as e:
            logger.error(f"Error during streaming from {url}: {e}")
            if request_log:
                request_log.record_generation_end_time()
                request_log.record_error(f"streaming error: {e}")
                request_log.finalize("error")
            raise
        finally:
            logger.debug(f"Stream completed for {url}, total chunks: {chunk_count}")
            await close_stream()
            if request_log and not request_log.finalized:
                request_log.record_generation_end_time()
                request_log.finalize("success")

    return StreamingResponse(
        iterator(),
        status_code=resp.status_code,
        headers=headers_to_client,
        media_type=media_type or "text/event-stream",
    )
