"""OpenAI-compatible chat completions endpoint."""

import json
import logging
from typing import Any, Callable, Mapping, Optional

from fastapi import HTTPException, Request, Response
from fastapi.responses import StreamingResponse
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

logger = logging.getLogger("yallmp-proxy")


def _attach_finish_task(response: Response, finish: Callable[[], None]) -> None:
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


async def handle_openai_request(request: Request) -> Response:
    """Handle OpenAI-compatible chat completions requests.
    
    This function processes incoming requests, validates them, and routes
    them to the appropriate backend through the proxy router.
    
    Args:
        request: The FastAPI request object.
    
    Returns:
        A Response or StreamingResponse with the completion results.
    """
    logger.info(f"Handling {request.method} request to {request.url.path}")
    router = get_router()
    known_models = set(router.backends.keys())

    def _db_log_target_for(model: str) -> Any:
        return resolve_db_log_target(
            model_name=model or "unknown",
            headers=request.headers,
            known_models=known_models,
        )
    tracker = USAGE_COUNTERS.start_request()

    try:
        body = await request.body()
    except ClientDisconnect:
        logger.warning("Client disconnected before request body was fully read")
        tracker.finish()
        return Response(status_code=499)  # Client Closed Request

    request_log: Optional[RequestLogRecorder] = None
    try:
        payload = json.loads(body or b"{}")
    except json.JSONDecodeError as exc:
        logger.error(f"Invalid JSON payload: {exc}")
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
            "Concurrency queue timeout for key=%s, model=%s",
            app_key_ctx.key_id,
            model_name,
        )
        tracker.finish()
        raise HTTPException(
            status_code=429,
            detail={
                "error": {
                    "message": "Request queued too long - concurrency limit reached",
                    "type": "rate_limit_error",
                    "code": "concurrency_limit_exceeded",
                }
            },
        )
    except ConcurrencyClientDisconnected:
        logger.info(
            "Client disconnected while waiting in queue: key=%s, model=%s",
            app_key_ctx.key_id,
            model_name,
        )
        tracker.finish()
        return Response(status_code=499)

    # Basic validation for chat completions
    if "/chat/completions" in request.url.path:
        messages = payload.get("messages")
        if not messages or not isinstance(messages, list):
            logger.error("Request missing or invalid messages array")
            request_log = request_log or RequestLogRecorder(
                model_name,
                False,
                request.url.path,
                db_log_target=_db_log_target_for(model_name),
            )
            request_log.record_request(request.method, request.url.query, request.headers, body)
            request_log.record_error("missing messages array")
            request_log.finalize("error")
            await slot.release()
            tracker.finish()
            raise HTTPException(
                status_code=400,
                detail={
                    "error": {
                        "message": "You must provide a messages array",
                        "type": "invalid_request_error",
                        "code": "missing_parameter",
                    }
                },
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
        if isinstance(response, StreamingResponse):
            # Wrap stream to release slot after streaming completes
            response = _wrap_stream_with_slot_release(response, slot)
            _attach_finish_task(response, tracker.finish)
            return response
        # Non-streaming: release slot immediately
        await slot.release()
        tracker.finish()
        return response
    except Exception as e:
        logger.error(f"Error processing request for model {model_name}: {e}")
        if request_log and not request_log.finalized:
            request_log.record_error(str(e))
            request_log.finalize("error")
        await slot.release()
        tracker.finish()
        raise


async def chat_completions(request: Request) -> Response:
    """Chat completions endpoint - OpenAI compatible.

    POST /v1/chat/completions
    """
    logger.info("Received chat completions request")
    return await handle_openai_request(request)
