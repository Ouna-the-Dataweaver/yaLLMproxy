"""OpenAI-compatible embeddings endpoint."""

import json
import logging
from typing import Any, Mapping, Optional

from fastapi import HTTPException, Request, Response

from ...core import normalize_request_model
from ...core.registry import get_router
from ...logging import RequestLogRecorder, resolve_db_log_target
from ...usage_metrics import USAGE_COUNTERS

logger = logging.getLogger("yallmp-proxy")


async def embeddings(request: Request) -> Response:
    """Embeddings endpoint - OpenAI compatible.

    POST /v1/embeddings

    Request body:
        - model: string (required) - Model ID to use for embeddings
        - input: string or array (required) - Text to embed
        - encoding_format: string (optional) - "float" or "base64", defaults to "float"
        - dimensions: integer (optional) - Output dimensions (model-dependent)
        - user: string (optional) - End-user identifier

    Response:
        {
            "object": "list",
            "data": [{"object": "embedding", "index": 0, "embedding": [...]}],
            "model": "model-name",
            "usage": {"prompt_tokens": N, "total_tokens": N}
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

    body = await request.body()
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

    # Validate input field for embeddings
    input_text = payload.get("input")
    if input_text is None:
        logger.error("Request missing input field")
        request_log = request_log or RequestLogRecorder(
            model_name,
            False,
            request.url.path,
            db_log_target=_db_log_target_for(model_name),
        )
        request_log.record_request(request.method, request.url.query, request.headers, body)
        request_log.record_error("missing input parameter")
        request_log.finalize("error")
        tracker.finish()
        raise HTTPException(
            status_code=400,
            detail={
                "error": {
                    "message": "You must provide an input parameter",
                    "type": "invalid_request_error",
                    "code": "missing_parameter",
                }
            },
        )

    # Validate input is string or array of strings
    if not isinstance(input_text, (str, list)):
        logger.error("Input must be a string or array of strings")
        request_log = request_log or RequestLogRecorder(
            model_name,
            False,
            request.url.path,
            db_log_target=_db_log_target_for(model_name),
        )
        request_log.record_request(request.method, request.url.query, request.headers, body)
        request_log.record_error("invalid input type")
        request_log.finalize("error")
        tracker.finish()
        raise HTTPException(
            status_code=400,
            detail={
                "error": {
                    "message": "Input must be a string or array of strings/token arrays",
                    "type": "invalid_request_error",
                    "code": "invalid_parameter",
                }
            },
        )

    # Embeddings are never streaming
    is_stream = False
    query = request.url.query or ""
    request_log = request_log or RequestLogRecorder(
        model_name,
        is_stream,
        request.url.path,
        db_log_target=_db_log_target_for(model_name),
    )
    request_log.record_request(request.method, query, request.headers, body)
    logger.info(f"Processing embeddings request for model {model_name}")

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
        logger.info(f"Embeddings request for model {model_name} completed successfully")
        if request_log and not request_log.finalized:
            request_log.finalize("success")
        tracker.finish()
        return response
    except Exception as e:
        logger.error(f"Error processing embeddings request for model {model_name}: {e}")
        if request_log and not request_log.finalized:
            request_log.record_error(str(e))
            request_log.finalize("error")
        tracker.finish()
        raise
