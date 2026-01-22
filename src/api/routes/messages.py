"""Anthropic-compatible Messages API endpoint."""

import json
import logging
import time
import uuid
from typing import Any, AsyncIterator, Mapping, Optional

from fastapi import Request, Response
from fastapi.responses import JSONResponse, StreamingResponse
from starlette.background import BackgroundTask, BackgroundTasks
from starlette.requests import ClientDisconnect

from ...auth.app_key import get_app_key_validator
from ...concurrency import (
    ConcurrencyClientDisconnected,
    ConcurrencyQueueTimeout,
    ConcurrencySlot,
    get_concurrency_manager,
    get_key_concurrency_config,
)
from ...core import normalize_request_model
from ...core.registry import get_router
from ...logging import RequestLogRecorder, resolve_db_log_target
from ...usage_metrics import USAGE_COUNTERS
from ...messages import (
    messages_to_chat_completions,
    chat_completion_to_messages,
    ChatToMessagesStreamAdapter,
)

logger = logging.getLogger("yallmp-proxy")


def _attach_finish_task(response: Response, finish) -> None:
    existing = getattr(response, "background", None)
    if existing is None:
        response.background = BackgroundTask(finish)
        return

    tasks = BackgroundTasks()
    if isinstance(existing, BackgroundTasks):
        for task in existing.tasks:
            tasks.add_task(task.func, *task.args, **task.kwargs)
    else:
        tasks.add_task(existing.func, *existing.args, **existing.kwargs)
    tasks.add_task(finish)
    response.background = tasks


def _wrap_stream_with_slot_release(
    response: StreamingResponse,
    slot: ConcurrencySlot,
) -> StreamingResponse:
    """Wrap a streaming response to release the concurrency slot when done."""
    original_iterator = response.body_iterator

    async def wrapped_iterator():
        try:
            async for chunk in original_iterator:
                yield chunk
        finally:
            await slot.release()

    return StreamingResponse(
        wrapped_iterator(),
        status_code=response.status_code,
        headers=dict(response.headers),
        media_type=response.media_type,
    )


def _anthropic_error_response(
    message: str,
    *,
    error_type: str = "invalid_request_error",
    status_code: int = 400,
    error_code: Optional[str] = None,
    param: Optional[str] = None,
) -> JSONResponse:
    error: dict[str, Any] = {"type": error_type, "message": message}
    if error_code:
        error["code"] = error_code
    if param:
        error["param"] = param
    payload = {"type": "error", "error": error}
    return JSONResponse(payload, status_code=status_code)


async def messages_endpoint(request: Request) -> Response:
    """POST /v1/messages - Anthropic Messages API compatible endpoint."""
    # Generate request ID for log correlation
    req_id = uuid.uuid4().hex[:8]
    start_time = time.perf_counter()

    # Extract connection info for debugging
    client_host = request.client.host if request.client else "unknown"
    client_port = request.client.port if request.client else "unknown"
    content_length = request.headers.get("content-length", "not-set")
    content_type = request.headers.get("content-type", "not-set")

    # Extract ASGI scope info for connection debugging
    scope = request.scope
    http_version = scope.get("http_version", "unknown")
    server_info = scope.get("server", ("unknown", 0))
    asgi_spec = scope.get("asgi", {})

    logger.info(
        f"[{req_id}] Messages API request from {client_host}:{client_port}, "
        f"Content-Length: {content_length}, Content-Type: {content_type}, "
        f"HTTP/{http_version}"
    )

    # Log additional connection details at debug level
    if logger.isEnabledFor(logging.DEBUG):
        headers_str = ", ".join(f"{k}: {v}" for k, v in request.headers.items())
        logger.debug(f"[{req_id}] Request headers: {headers_str}")
        logger.debug(
            f"[{req_id}] ASGI scope: server={server_info}, asgi={asgi_spec}, "
            f"type={scope.get('type')}, scheme={scope.get('scheme')}"
        )

    router = get_router()
    known_models = set(router.backends.keys())

    def _db_log_target_for(model: str) -> Any:
        return resolve_db_log_target(
            model_name=model or "unknown",
            headers=request.headers,
            known_models=known_models,
        )

    tracker = USAGE_COUNTERS.start_request()
    request_log: Optional[RequestLogRecorder] = None

    try:
        if logger.isEnabledFor(logging.DEBUG):
            logger.debug(
                f"[{req_id}] Starting body read, Content-Length: {content_length}"
            )
        body = await request.body()
        elapsed = time.perf_counter() - start_time
        if logger.isEnabledFor(logging.DEBUG):
            logger.debug(
                f"[{req_id}] Body read complete: {len(body)} bytes in {elapsed:.3f}s "
                f"(expected: {content_length})"
            )
        payload = json.loads(body or b"{}")
    except ClientDisconnect:
        elapsed = time.perf_counter() - start_time
        logger.warning(
            f"[{req_id}] ClientDisconnect after {elapsed:.3f}s - "
            f"client {client_host}:{client_port}, Content-Length: {content_length}"
        )
        tracker.finish()
        return Response(status_code=499)  # Client Closed Request
    except json.JSONDecodeError as exc:
        request_log = RequestLogRecorder(
            "unknown",
            False,
            request.url.path,
            db_log_target=_db_log_target_for("unknown"),
        )
        request_log.record_request(request.method, request.url.query, request.headers, body)
        request_log.record_error(f"invalid json: {exc}")
        request_log.finalize("error")
        tracker.finish()
        return _anthropic_error_response(
            "Invalid JSON payload",
            error_code="invalid_json",
        )

    if not isinstance(payload, Mapping):
        request_log = request_log or RequestLogRecorder(
            "unknown",
            False,
            request.url.path,
            db_log_target=_db_log_target_for("unknown"),
        )
        request_log.record_request(request.method, request.url.query, request.headers, body)
        request_log.record_error("payload must be a JSON object")
        request_log.finalize("error")
        tracker.finish()
        return _anthropic_error_response(
            "Request body must be a JSON object",
            error_code="invalid_json_shape",
        )

    raw_model_name = payload.get("model")
    if not isinstance(raw_model_name, str) or not raw_model_name:
        request_log = request_log or RequestLogRecorder(
            "unknown",
            False,
            request.url.path,
            db_log_target=_db_log_target_for("unknown"),
        )
        request_log.record_request(request.method, request.url.query, request.headers, body)
        request_log.record_error("missing model parameter")
        request_log.finalize("error")
        tracker.finish()
        return _anthropic_error_response(
            "You must provide a model parameter",
            error_code="missing_parameter",
            param="model",
        )

    model_name = normalize_request_model(raw_model_name)

    # Validate app key authentication and model access
    app_key_ctx = get_app_key_validator().validate_request(request, model_name)

    # Acquire concurrency slot (will queue if at limit)
    concurrency_config = get_key_concurrency_config(app_key_ctx.key_id)
    try:
        slot = await get_concurrency_manager().acquire(
            key_identifier=app_key_ctx.key_id,
            concurrency_limit=concurrency_config.concurrency_limit,
            priority=concurrency_config.priority,
            timeout=concurrency_config.queue_timeout,
            disconnect_checker=request.is_disconnected,
        )
    except ConcurrencyQueueTimeout:
        logger.warning(
            f"[{req_id}] Concurrency queue timeout for key={app_key_ctx.key_id}, model={model_name}"
        )
        tracker.finish()
        return _anthropic_error_response(
            "Request queued too long - concurrency limit reached",
            error_type="rate_limit_error",
            status_code=429,
            error_code="concurrency_limit_exceeded",
        )
    except ConcurrencyClientDisconnected:
        logger.info(
            f"[{req_id}] Client disconnected while waiting in queue: key={app_key_ctx.key_id}"
        )
        tracker.finish()
        return Response(status_code=499)

    backend = router.backends.get(model_name)
    if not backend:
        request_log = request_log or RequestLogRecorder(
            model_name,
            False,
            request.url.path,
            db_log_target=_db_log_target_for(model_name),
        )
        request_log.record_request(request.method, request.url.query, request.headers, body)
        request_log.record_error("model not found")
        request_log.finalize("error")
        await slot.release()
        tracker.finish()
        return _anthropic_error_response(
            f"Model '{model_name}' is not defined in config",
            error_type="not_found_error",
            error_code="model_not_found",
            param="model",
        )

    is_stream = bool(payload.get("stream"))
    query = request.url.query or ""

    request_log = request_log or RequestLogRecorder(
        model_name,
        is_stream,
        request.url.path,
        db_log_target=_db_log_target_for(model_name),
    )
    request_log.record_request(request.method, query, request.headers, body)
    request_log.set_app_key(app_key_ctx.key_id)

    if backend.api_type == "anthropic":
        if logger.isEnabledFor(logging.DEBUG):
            logger.debug(
                f"[{req_id}] Forwarding to backend: name={model_name}, "
                f"api_type={backend.api_type}, base_url={backend.base_url}, "
                f"path=/v1/messages, stream={is_stream}"
            )
        try:
            response = await router.forward_request(
                model_name=model_name,
                path="/v1/messages",
                query=query,
                body=body,
                payload=payload,
                is_stream=is_stream,
                headers=request.headers,
                request_log=request_log,
                disconnect_checker=request.is_disconnected,
            )
        except Exception as exc:
            elapsed = time.perf_counter() - start_time
            logger.error(f"[{req_id}] Messages backend error after {elapsed:.3f}s: {exc}")
            if request_log and not request_log.finalized:
                request_log.record_error(str(exc))
                request_log.finalize("error")
            await slot.release()
            tracker.finish()
            return _anthropic_error_response(
                str(exc),
                error_type="server_error",
                status_code=502,
                error_code="backend_error",
            )

        if not is_stream and request_log and not request_log.finalized:
            if response.status_code >= 400:
                request_log.record_error(f"messages error status {response.status_code}")
                request_log.finalize("error")
            else:
                request_log.finalize("success")

        if isinstance(response, StreamingResponse):
            elapsed = time.perf_counter() - start_time
            logger.info(
                f"[{req_id}] Starting streaming response for {model_name}, "
                f"setup took {elapsed:.3f}s"
            )
            response = _wrap_stream_with_slot_release(response, slot)
            _attach_finish_task(response, tracker.finish)
            return response

        elapsed = time.perf_counter() - start_time
        logger.info(
            f"[{req_id}] Completed non-streaming response for {model_name}, "
            f"status={response.status_code}, took {elapsed:.3f}s"
        )
        await slot.release()
        tracker.finish()
        return response

    # Translate Anthropic Messages request to OpenAI Chat Completions
    try:
        openai_payload = messages_to_chat_completions(payload)
    except Exception as exc:
        logger.error(f"[{req_id}] Failed to translate messages request: {exc}")
        if request_log and not request_log.finalized:
            request_log.record_error(f"translation error: {exc}")
            request_log.finalize("error")
        await slot.release()
        tracker.finish()
        return _anthropic_error_response(
            f"Failed to translate request: {exc}",
            error_type="invalid_request_error",
            status_code=400,
            error_code="translation_error",
        )

    # Build the translated request body
    openai_body = json.dumps(openai_payload, ensure_ascii=False).encode("utf-8")

    if logger.isEnabledFor(logging.DEBUG):
        logger.debug(
            f"[{req_id}] Translated to OpenAI format: model={openai_payload.get('model')}, "
            f"messages_count={len(openai_payload.get('messages', []))}, stream={is_stream}"
        )

    # Generate message ID for the response
    message_id = f"msg_{uuid.uuid4().hex[:24]}"

    try:
        response = await router.forward_request(
            model_name=model_name,
            path="/v1/chat/completions",
            query=query,
            body=openai_body,
            payload=openai_payload,
            is_stream=is_stream,
            headers=request.headers,
            request_log=request_log,
            disconnect_checker=request.is_disconnected,
        )
    except Exception as exc:
        elapsed = time.perf_counter() - start_time
        logger.error(f"[{req_id}] Backend error during translation after {elapsed:.3f}s: {exc}")
        if request_log and not request_log.finalized:
            request_log.record_error(str(exc))
            request_log.finalize("error")
        await slot.release()
        tracker.finish()
        return _anthropic_error_response(
            str(exc),
            error_type="server_error",
            status_code=502,
            error_code="backend_error",
        )

    # Handle streaming response - wrap with adapter
    if isinstance(response, StreamingResponse):
        elapsed = time.perf_counter() - start_time
        logger.info(
            f"[{req_id}] Starting translated streaming response for {model_name}, "
            f"setup took {elapsed:.3f}s"
        )

        # Create the stream adapter to convert OpenAI SSE to Anthropic SSE
        adapter = ChatToMessagesStreamAdapter(message_id, model_name)

        async def adapted_stream() -> AsyncIterator[bytes]:
            """Wrap the OpenAI stream and convert to Anthropic format."""
            try:
                async for event in adapter.adapt_stream(response.body_iterator):
                    yield event
            finally:
                await slot.release()

        adapted_response = StreamingResponse(
            adapted_stream(),
            status_code=response.status_code,
            headers={"content-type": "text/event-stream"},
            media_type="text/event-stream",
        )
        _attach_finish_task(adapted_response, tracker.finish)
        return adapted_response

    # Handle non-streaming response - translate the response body
    try:
        # Parse the OpenAI response
        openai_response = json.loads(response.body)

        # Translate to Anthropic format
        anthropic_response = chat_completion_to_messages(openai_response)

        # Use our generated message ID
        anthropic_response["id"] = message_id

        elapsed = time.perf_counter() - start_time
        logger.info(
            f"[{req_id}] Completed translated non-streaming response for {model_name}, "
            f"status={response.status_code}, took {elapsed:.3f}s"
        )

        if request_log and not request_log.finalized:
            if response.status_code >= 400:
                request_log.record_error(f"translated response status {response.status_code}")
                request_log.finalize("error")
            else:
                request_log.finalize("success")

        await slot.release()
        tracker.finish()
        return JSONResponse(
            anthropic_response,
            status_code=response.status_code,
        )
    except json.JSONDecodeError as exc:
        logger.error(f"[{req_id}] Failed to parse OpenAI response: {exc}")
        if request_log and not request_log.finalized:
            request_log.record_error(f"response parse error: {exc}")
            request_log.finalize("error")
        await slot.release()
        tracker.finish()
        # Return the original response if we can't parse it
        return response
    except Exception as exc:
        logger.error(f"[{req_id}] Failed to translate response: {exc}")
        if request_log and not request_log.finalized:
            request_log.record_error(f"response translation error: {exc}")
            request_log.finalize("error")
        await slot.release()
        tracker.finish()
        return _anthropic_error_response(
            f"Failed to translate response: {exc}",
            error_type="server_error",
            status_code=500,
            error_code="translation_error",
        )
