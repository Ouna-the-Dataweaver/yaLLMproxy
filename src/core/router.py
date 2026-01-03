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

from .sse import detect_sse_stream_error
from ..parsers import (
    ParserContext,
    build_response_parser_overrides,
    build_response_parser_pipeline,
)

DEFAULT_TIMEOUT = 30


class ProxyRouter:
    """Routes requests to backends with fallback support."""
    
    def __init__(self, config: Dict) -> None:
        self.backends = self._parse_backends(config.get("model_list", []))
        if not self.backends:
            raise RuntimeError("No backends found in config")
        self._lock = asyncio.Lock()
        self.response_parsers = build_response_parser_pipeline(config)
        self.response_parser_overrides = build_response_parser_overrides(config)

        proxy_settings = config.get("proxy_settings") or {}
        logging_cfg = proxy_settings.get("logging") or {}
        self.log_parsed_response = _parse_bool(logging_cfg.get("log_parsed_response"))
        log_parsed_stream_raw = logging_cfg.get("log_parsed_stream")
        if log_parsed_stream_raw is None:
            self.log_parsed_stream = self.log_parsed_response
        else:
            self.log_parsed_stream = _parse_bool(log_parsed_stream_raw)

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

    def _select_response_parsers(self, backend_name: str):
        if backend_name in self.response_parser_overrides:
            return self.response_parser_overrides[backend_name]
        return self.response_parsers

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
            headers, backend.api_key, is_stream=is_stream
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
            parser_context = ParserContext(
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
        async def _post(http2_enabled: bool) -> httpx.Response:
            async with httpx.AsyncClient(timeout=timeout, http2=http2_enabled) as client:
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
        parser_context = ParserContext(
            path=path,
            model=model_name,
            backend=backend.name,
            is_stream=is_stream,
        )
        parsed_body = pipeline.transform_response_body(
            resp.content, resp.headers.get("content-type"), parser_context
        )
        if parsed_body is not None:
            if request_log:
                request_log.record_parsed_response(
                    resp.status_code, resp.headers, parsed_body
                )
                # Extract and record usage stats from the parsed response
                try:
                    import json as json_module
                    payload = json_module.loads(parsed_body)
                    if isinstance(payload, dict) and "usage" in payload:
                        request_log.record_usage_stats(payload["usage"])
                except (json_module.JSONDecodeError, TypeError):
                    pass
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

            supports_reasoning = bool(params.get("supports_reasoning"))
            http2 = _parse_bool(params.get("http2"))
            editable = _parse_bool(entry.get("editable"))

            # Parse parameter overrides
            param_configs: Dict[str, ParameterConfig] = {}
            raw_params = entry.get("parameters") or {}
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
                supports_reasoning=supports_reasoning,
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
    parser_context: Optional[ParserContext] = None,
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
    client = httpx.AsyncClient(timeout=stream_timeout, http2=http2)
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
        try:
            chunk_count = 0
            
            # First yield the buffered chunks
            for chunk in buffered_chunks:
                chunk_count += 1
                for out_chunk in _process_chunk(chunk):
                    yield out_chunk
            
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
                        if chunk_count % 10 == 0:
                            logger.debug(f"Streamed {chunk_count} chunks from {url}")
                        if request_log:
                            request_log.record_stream_chunk(chunk)
                        for out_chunk in _process_chunk(chunk):
                            yield out_chunk
            if stream_parser:
                for out_chunk in stream_parser.finish():
                    if request_log:
                        request_log.record_parsed_stream_chunk(out_chunk)
                    yield out_chunk
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
