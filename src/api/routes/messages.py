"""Anthropic-compatible Messages API endpoint."""

import json
import logging
from typing import Any, Mapping, Optional

from fastapi import Request, Response
from fastapi.responses import JSONResponse, StreamingResponse
from starlette.background import BackgroundTask, BackgroundTasks

from ...auth.app_key import get_app_key_validator
from ...core import normalize_request_model
from ...core.registry import get_router
from ...logging import RequestLogRecorder, resolve_db_log_target
from ...usage_metrics import USAGE_COUNTERS
from ...messages import (
    messages_to_chat_completions,
    chat_completion_to_messages,
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
    logger.info("Received Messages API request")
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
        body = await request.body()
        payload = json.loads(body or b"{}")
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
            logger.error(f"Messages backend error: {exc}")
            if request_log and not request_log.finalized:
                request_log.record_error(str(exc))
                request_log.finalize("error")
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
            _attach_finish_task(response, tracker.finish)
            return response

        tracker.finish()
        return response

    # Placeholder for Anthropic -> OpenAI translation (not implemented yet)
    _ = messages_to_chat_completions, chat_completion_to_messages
    if request_log and not request_log.finalized:
        request_log.record_error("messages translation not implemented")
        request_log.finalize("error")
    tracker.finish()
    return _anthropic_error_response(
        "Messages API translation for non-anthropic backends is not implemented yet.",
        error_type="unsupported_feature",
        status_code=501,
        error_code="messages_translation_not_implemented",
    )
