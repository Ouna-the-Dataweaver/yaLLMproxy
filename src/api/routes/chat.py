"""OpenAI-compatible chat completions endpoint."""

import json
import logging
from typing import Any, Callable, Mapping, Optional

from fastapi import HTTPException, Request, Response
from fastapi.responses import StreamingResponse
from starlette.background import BackgroundTask, BackgroundTasks

from ...core import normalize_request_model
from ...core.registry import get_router
from ...logging import RequestLogRecorder
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
    tracker = USAGE_COUNTERS.start_request()
    
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
        request_log = request_log or RequestLogRecorder("unknown", False, request.url.path)
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
        request_log = request_log or RequestLogRecorder("unknown", False, request.url.path)
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

    # Basic validation for chat completions
    if "/chat/completions" in request.url.path:
        messages = payload.get("messages")
        if not messages or not isinstance(messages, list):
            logger.error("Request missing or invalid messages array")
            request_log = request_log or RequestLogRecorder(model_name, False, request.url.path)
            request_log.record_request(request.method, request.url.query, request.headers, body)
            request_log.record_error("missing messages array")
            request_log.finalize("error")
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
    request_log = request_log or RequestLogRecorder(model_name, is_stream, request.url.path)
    request_log.record_request(request.method, query, request.headers, body)
    logger.info(f"Processing request for model {model_name}, stream={is_stream}")

    backend_path = request.url.path
    
    try:
        router = get_router()
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
            _attach_finish_task(response, tracker.finish)
            return response
        tracker.finish()
        return response
    except Exception as e:
        logger.error(f"Error processing request for model {model_name}: {e}")
        if request_log and not request_log.finalized:
            request_log.record_error(str(e))
            request_log.finalize("error")
        tracker.finish()
        raise


async def chat_completions(request: Request) -> Response:
    """Chat completions endpoint - OpenAI compatible.
    
    POST /v1/chat/completions
    """
    logger.info("Received chat completions request")
    return await handle_openai_request(request)


async def responses(request: Request) -> Response:
    """Responses endpoint - OpenAI compatible (if enabled).
    
    POST /v1/responses
    """
    logger.info("Received responses request")
    return await handle_openai_request(request)
