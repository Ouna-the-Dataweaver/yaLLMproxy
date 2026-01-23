"""OpenAI/Jina/Cohere-compatible rerank endpoint."""

import json
import logging
from typing import Any, Mapping, Optional

from fastapi import HTTPException, Request, Response
from starlette.requests import ClientDisconnect

from ...auth.app_key import get_app_key_validator
from ...concurrency import (
    ConcurrencyClientDisconnected,
    ConcurrencyQueueTimeout,
    get_concurrency_manager,
    get_key_concurrency_config,
)
from ...core import normalize_request_model
from ...core.registry import get_router
from ...logging import RequestLogRecorder, resolve_db_log_target
from ...usage_metrics import USAGE_COUNTERS

logger = logging.getLogger("yallmp-proxy")


async def rerank(request: Request) -> Response:
    """Rerank endpoint - Jina/Cohere/vLLM compatible.

    POST /v1/rerank

    Request body:
        - model: string (required) - Model ID for reranking
        - query: string (required) - Search query text
        - documents: array (required) - Documents to rank (strings)
        - top_n: integer (optional) - Max results to return
        - return_documents: boolean (optional) - Include document text in response

    Response:
        {
            "id": "rerank-abc123",
            "model": "model-name",
            "usage": {"total_tokens": N},
            "results": [
                {
                    "index": 1,
                    "relevance_score": 0.998,
                    "document": {"text": "..."}
                }
            ]
        }
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

    # Validate query parameter
    query = payload.get("query")
    if not isinstance(query, str) or not query.strip():
        logger.error("Request missing or empty query parameter")
        request_log = request_log or RequestLogRecorder(
            model_name,
            False,
            request.url.path,
            db_log_target=_db_log_target_for(model_name),
        )
        request_log.record_request(request.method, request.url.query, request.headers, body)
        request_log.record_error("missing or empty query parameter")
        request_log.finalize("error")
        await slot.release()
        tracker.finish()
        raise HTTPException(
            status_code=400,
            detail={
                "error": {
                    "message": "You must provide a non-empty query parameter",
                    "type": "invalid_request_error",
                    "code": "missing_parameter",
                }
            },
        )

    # Validate documents parameter
    documents = payload.get("documents")
    if not isinstance(documents, list) or len(documents) == 0:
        logger.error("Request missing or empty documents array")
        request_log = request_log or RequestLogRecorder(
            model_name,
            False,
            request.url.path,
            db_log_target=_db_log_target_for(model_name),
        )
        request_log.record_request(request.method, request.url.query, request.headers, body)
        request_log.record_error("missing or empty documents array")
        request_log.finalize("error")
        await slot.release()
        tracker.finish()
        raise HTTPException(
            status_code=400,
            detail={
                "error": {
                    "message": "You must provide a non-empty documents array",
                    "type": "invalid_request_error",
                    "code": "missing_parameter",
                }
            },
        )

    # Validate documents are strings
    for i, doc in enumerate(documents):
        if not isinstance(doc, str):
            logger.error(f"Document at index {i} is not a string")
            request_log = request_log or RequestLogRecorder(
                model_name,
                False,
                request.url.path,
                db_log_target=_db_log_target_for(model_name),
            )
            request_log.record_request(request.method, request.url.query, request.headers, body)
            request_log.record_error(f"document at index {i} is not a string")
            request_log.finalize("error")
            await slot.release()
            tracker.finish()
            raise HTTPException(
                status_code=400,
                detail={
                    "error": {
                        "message": f"All documents must be strings, document at index {i} is not",
                        "type": "invalid_request_error",
                        "code": "invalid_parameter",
                    }
                },
            )

    # Validate top_n if provided
    top_n = payload.get("top_n")
    if top_n is not None and (not isinstance(top_n, int) or top_n < 1):
        logger.error("Invalid top_n parameter")
        request_log = request_log or RequestLogRecorder(
            model_name,
            False,
            request.url.path,
            db_log_target=_db_log_target_for(model_name),
        )
        request_log.record_request(request.method, request.url.query, request.headers, body)
        request_log.record_error("invalid top_n parameter")
        request_log.finalize("error")
        await slot.release()
        tracker.finish()
        raise HTTPException(
            status_code=400,
            detail={
                "error": {
                    "message": "top_n must be a positive integer",
                    "type": "invalid_request_error",
                    "code": "invalid_parameter",
                }
            },
        )

    # Rerank is never streaming
    is_stream = False
    url_query = request.url.query or ""
    request_log = request_log or RequestLogRecorder(
        model_name,
        is_stream,
        request.url.path,
        db_log_target=_db_log_target_for(model_name),
    )
    request_log.record_request(request.method, url_query, request.headers, body)
    request_log.set_app_key(app_key_ctx.key_id)
    logger.info(f"Processing rerank request for model {model_name}")

    backend_path = request.url.path

    try:
        response = await router.forward_request(
            model_name=model_name,
            path=backend_path,
            query=url_query,
            body=body,
            payload=payload,
            is_stream=is_stream,
            headers=request.headers,
            request_log=request_log,
            disconnect_checker=request.is_disconnected,
        )
        logger.info(f"Rerank request for model {model_name} completed successfully")
        if request_log and not request_log.finalized:
            request_log.finalize("success")
        await slot.release()
        tracker.finish()
        return response
    except Exception as e:
        logger.error(f"Error processing rerank request for model {model_name}: {e}")
        if request_log and not request_log.finalized:
            request_log.record_error(str(e))
            request_log.finalize("error")
        await slot.release()
        tracker.finish()
        raise
