"""Open Responses API endpoint handler.

Implements the POST /v1/responses endpoint supporting:
- Pass-through mode: Forward to backends that natively support Responses API
- Simulation mode: Translate to/from Chat Completions for other backends
- Stateful conversations: store + previous_response_id support
- Streaming: Native Responses API event format
"""

import json
import logging
from typing import Any, AsyncIterator, Optional

from fastapi import HTTPException, Request, Response
from fastapi.responses import JSONResponse, StreamingResponse
from starlette.requests import ClientDisconnect

from ...auth.app_key import get_app_key_validator
from ...core import normalize_request_model
from ...core.registry import get_router
from ...logging import RequestLogRecorder, resolve_db_log_target
from ...responses import (
    ResponseStateStore,
    get_state_store,
    responses_to_chat_completions,
    chat_completion_to_response,
    ChatToResponsesStreamAdapter,
)
from ...responses.translator import generate_response_id, build_error_response

logger = logging.getLogger("yallmp-proxy")


async def responses_endpoint(request: Request) -> Response:
    """POST /v1/responses - Open Responses API endpoint.

    Supports both pass-through mode (for backends that natively support the
    Responses API) and simulation mode (translating to/from Chat Completions).

    Args:
        request: The FastAPI request object

    Returns:
        JSONResponse for non-streaming, StreamingResponse for streaming
    """
    logger.info("Received Responses API request")
    router = get_router()
    known_models = set(router.backends.keys())

    def _db_log_target_for(model: str) -> Any:
        return resolve_db_log_target(
            model_name=model or "unknown",
            headers=request.headers,
            known_models=known_models,
        )

    request_log: Optional[RequestLogRecorder] = None

    # Parse request body
    try:
        body = await request.body()
        payload = json.loads(body or b"{}")
    except ClientDisconnect:
        logger.warning("Client disconnected before request body was fully read")
        return Response(status_code=499)  # Client Closed Request
    except json.JSONDecodeError as exc:
        logger.error(f"Invalid JSON in Responses request: {exc}")
        request_log = RequestLogRecorder(
            "unknown",
            False,
            request.url.path,
            db_log_target=_db_log_target_for("unknown"),
        )
        request_log.record_request(request.method, request.url.query, request.headers, body)
        request_log.record_error(f"invalid json: {exc}")
        request_log.finalize("error")
        raise HTTPException(
            status_code=400,
            detail={
                "error": {
                    "type": "invalid_request",
                    "code": "invalid_json",
                    "message": "Invalid JSON payload",
                }
            }
        ) from exc

    # Validate required fields
    if not isinstance(payload, dict):
        request_log = request_log or RequestLogRecorder(
            "unknown",
            False,
            request.url.path,
            db_log_target=_db_log_target_for("unknown"),
        )
        request_log.record_request(request.method, request.url.query, request.headers, body)
        request_log.record_error("payload must be a JSON object")
        request_log.finalize("error")
        raise HTTPException(
            status_code=400,
            detail={
                "error": {
                    "type": "invalid_request",
                    "code": "invalid_request_body",
                    "message": "Request body must be a JSON object",
                }
            }
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
        raise HTTPException(
            status_code=400,
            detail={
                "error": {
                    "type": "invalid_request",
                    "code": "missing_parameter",
                    "message": "You must provide a model parameter",
                    "param": "model",
                }
            }
        )

    input_data = payload.get("input")
    if input_data is None:
        request_log = request_log or RequestLogRecorder(
            "unknown",
            False,
            request.url.path,
            db_log_target=_db_log_target_for("unknown"),
        )
        request_log.record_request(request.method, request.url.query, request.headers, body)
        request_log.record_error("missing input parameter")
        request_log.finalize("error")
        raise HTTPException(
            status_code=400,
            detail={
                "error": {
                    "type": "invalid_request",
                    "code": "missing_parameter",
                    "message": "You must provide an input parameter",
                    "param": "input",
                }
            }
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
        raise HTTPException(
            status_code=400,
            detail={
                "error": {
                    "type": "not_found",
                    "code": "model_not_found",
                    "message": f"Model '{model_name}' is not defined in config",
                    "param": "model",
                }
            }
        )

    is_stream = bool(payload.get("stream", False))
    store = bool(payload.get("store", False))
    previous_response_id = payload.get("previous_response_id")

    request_log = request_log or RequestLogRecorder(
        model_name,
        is_stream,
        request.url.path,
        db_log_target=_db_log_target_for(model_name),
    )
    request_log.record_request(request.method, request.url.query, request.headers, body)
    request_log.set_app_key(app_key_ctx.key_id)

    # Generate response ID upfront
    response_id = generate_response_id()
    logger.info(
        f"Processing Responses request: model={model_name}, "
        f"stream={is_stream}, store={store}, id={response_id}"
    )

    # Check if backend natively supports Responses API
    if getattr(backend, "supports_responses_api", False):
        logger.info(f"Using pass-through mode for {model_name}")
        response = await _forward_responses_request(
            request=request,
            router=router,
            model_name=model_name,
            payload=payload,
            body=body,
            is_stream=is_stream,
            store=store,
            response_id=response_id,
            request_log=request_log,
        )
        if request_log and not request_log.finalized and not isinstance(response, StreamingResponse):
            if response.status_code >= 400:
                request_log.record_error(f"responses error status {response.status_code}")
                request_log.finalize("error")
            else:
                request_log.finalize("success")
        return response

    # Simulation mode: translate to chat completions
    logger.info(f"Using simulation mode for {model_name}")
    response = await _simulate_responses_request(
        request=request,
        router=router,
        model_name=model_name,
        payload=payload,
        is_stream=is_stream,
        store=store,
        response_id=response_id,
        previous_response_id=previous_response_id,
        request_log=request_log,
    )
    if request_log and not request_log.finalized and not isinstance(response, StreamingResponse):
        if response.status_code >= 400:
            request_log.record_error(f"responses error status {response.status_code}")
            request_log.finalize("error")
        else:
            request_log.finalize("success")
    return response


async def _forward_responses_request(
    request: Request,
    router: Any,
    model_name: str,
    payload: dict,
    body: bytes,
    is_stream: bool,
    store: bool,
    response_id: str,
    request_log: Optional[RequestLogRecorder] = None,
) -> Response:
    """Forward request to backend that natively supports Responses API.

    Args:
        request: Original request
        router: Proxy router
        model_name: Model name
        payload: Request payload
        body: Raw request body
        is_stream: Whether streaming
        store: Whether to store response
        response_id: Response ID

    Returns:
        Response from backend
    """
    try:
        response = await router.forward_request(
            model_name=model_name,
            path="/v1/responses",
            query=request.url.query or "",
            body=body,
            payload=payload,
            is_stream=is_stream,
            headers=request.headers,
            request_log=request_log,
        )

        # TODO: Handle store=True for pass-through mode
        # Would need to intercept the response and store it

        return response

    except HTTPException:
        raise
    except Exception as e:
        logger.error(f"Error forwarding Responses request: {e}")
        error_response = build_error_response(
            response_id=response_id,
            error_type="server_error",
            error_code="backend_error",
            message=str(e),
            model=model_name,
        )
        return JSONResponse(error_response, status_code=502)


async def _simulate_responses_request(
    request: Request,
    router: Any,
    model_name: str,
    payload: dict,
    is_stream: bool,
    store: bool,
    response_id: str,
    previous_response_id: Optional[str],
    request_log: Optional[RequestLogRecorder] = None,
) -> Response:
    """Simulate Responses API by translating to/from Chat Completions.

    Args:
        request: Original request
        router: Proxy router
        model_name: Model name
        payload: Request payload
        is_stream: Whether streaming
        store: Whether to store response
        response_id: Response ID
        previous_response_id: Previous response ID for conversation

    Returns:
        Translated response
    """
    state_store = get_state_store()

    # Build chat completions request
    try:
        chat_request = await responses_to_chat_completions(
            input_=payload.get("input"),
            model=model_name,
            instructions=payload.get("instructions"),
            previous_response_id=previous_response_id,
            state_store=state_store if previous_response_id else None,
            tools=payload.get("tools"),
            tool_choice=payload.get("tool_choice"),
            temperature=payload.get("temperature"),
            top_p=payload.get("top_p"),
            max_output_tokens=payload.get("max_output_tokens"),
            stream=is_stream,
            presence_penalty=payload.get("presence_penalty"),
            frequency_penalty=payload.get("frequency_penalty"),
        )
    except Exception as e:
        logger.error(f"Error translating Responses request: {e}")
        if request_log and not request_log.finalized:
            request_log.record_error(f"translation error: {e}")
            request_log.finalize("error")
        error_response = build_error_response(
            response_id=response_id,
            error_type="server_error",
            error_code="translation_error",
            message=f"Failed to translate request: {e}",
            model=model_name,
        )
        return JSONResponse(error_response, status_code=500)

    chat_body = json.dumps(chat_request, ensure_ascii=False).encode("utf-8")

    if is_stream:
        return await _handle_streaming_simulation(
            request=request,
            router=router,
            model_name=model_name,
            chat_request=chat_request,
            chat_body=chat_body,
            payload=payload,
            response_id=response_id,
            store=store,
            state_store=state_store,
            request_log=request_log,
        )
    else:
        return await _handle_non_streaming_simulation(
            request=request,
            router=router,
            model_name=model_name,
            chat_request=chat_request,
            chat_body=chat_body,
            payload=payload,
            response_id=response_id,
            store=store,
            state_store=state_store,
            request_log=request_log,
        )


async def _handle_non_streaming_simulation(
    request: Request,
    router: Any,
    model_name: str,
    chat_request: dict,
    chat_body: bytes,
    payload: dict,
    response_id: str,
    store: bool,
    state_store: ResponseStateStore,
    request_log: Optional[RequestLogRecorder] = None,
) -> Response:
    """Handle non-streaming simulation mode.

    Args:
        request: Original request
        router: Proxy router
        model_name: Model name
        chat_request: Translated chat request
        chat_body: Encoded chat request
        payload: Original payload
        response_id: Response ID
        store: Whether to store
        state_store: State store

    Returns:
        JSONResponse with translated response
    """
    try:
        chat_response = await router.forward_request(
            model_name=model_name,
            path="/v1/chat/completions",
            query="",
            body=chat_body,
            payload=chat_request,
            is_stream=False,
            headers=request.headers,
            request_log=request_log,
        )

        # Parse the chat completion response
        try:
            chat_completion = json.loads(chat_response.body)
        except (json.JSONDecodeError, AttributeError) as e:
            logger.error(f"Failed to parse chat completion response: {e}")
            if request_log and not request_log.finalized:
                request_log.record_error(f"parse error: {e}")
                request_log.finalize("error")
            error_response = build_error_response(
                response_id=response_id,
                error_type="server_error",
                error_code="parse_error",
                message="Failed to parse backend response",
                model=model_name,
            )
            return JSONResponse(error_response, status_code=500)

        # Check for error response from backend
        if "error" in chat_completion:
            error = chat_completion["error"]
            if request_log and not request_log.finalized:
                request_log.record_error(error.get("message", "backend error"))
                request_log.finalize("error")
            error_response = build_error_response(
                response_id=response_id,
                error_type="model_error",
                error_code=error.get("code", "backend_error"),
                message=error.get("message", "Backend error"),
                model=model_name,
            )
            return JSONResponse(error_response, status_code=chat_response.status_code)

        # Convert to Responses format
        response_obj = chat_completion_to_response(
            completion=chat_completion,
            original_request=payload,
            response_id=response_id,
            input_data=payload.get("input"),
        )

        # Store if requested
        if store:
            await state_store.store_response(
                response=response_obj,
                original_input=payload.get("input"),
            )
            logger.debug(f"Stored response {response_id}")

        return JSONResponse(response_obj)

    except HTTPException:
        raise
    except Exception as e:
        logger.error(f"Error in non-streaming simulation: {e}")
        if request_log and not request_log.finalized:
            request_log.record_error(f"simulation error: {e}")
            request_log.finalize("error")
        error_response = build_error_response(
            response_id=response_id,
            error_type="server_error",
            error_code="simulation_error",
            message=str(e),
            model=model_name,
        )
        return JSONResponse(error_response, status_code=500)


async def _handle_streaming_simulation(
    request: Request,
    router: Any,
    model_name: str,
    chat_request: dict,
    chat_body: bytes,
    payload: dict,
    response_id: str,
    store: bool,
    state_store: ResponseStateStore,
    request_log: Optional[RequestLogRecorder] = None,
) -> Response:
    """Handle streaming simulation mode.

    Args:
        request: Original request
        router: Proxy router
        model_name: Model name
        chat_request: Translated chat request
        chat_body: Encoded chat request
        payload: Original payload
        response_id: Response ID
        store: Whether to store
        state_store: State store

    Returns:
        StreamingResponse with adapted events
    """
    try:
        # Get streaming response from chat completions
        chat_response = await router.forward_request(
            model_name=model_name,
            path="/v1/chat/completions",
            query="",
            body=chat_body,
            payload=chat_request,
            is_stream=True,
            headers=request.headers,
            disconnect_checker=request.is_disconnected,
            request_log=request_log,
        )

        if not isinstance(chat_response, StreamingResponse):
            # Non-streaming response (error?)
            logger.warning("Expected StreamingResponse but got regular response")
            return chat_response

        # Create stream adapter
        adapter = ChatToResponsesStreamAdapter(
            response_id=response_id,
            model=model_name,
            original_request=payload,
        )

        async def streaming_with_store() -> AsyncIterator[bytes]:
            """Wrap the adapted stream to handle storage."""
            async for chunk in adapter.adapt_stream(_extract_stream_iterator(chat_response)):
                yield chunk

            # Store after stream completes
            if store:
                try:
                    await state_store.store_response(
                        response=adapter.build_final_response(),
                        original_input=payload.get("input"),
                    )
                    logger.debug(f"Stored streamed response {response_id}")
                except Exception as e:
                    logger.error(f"Failed to store streamed response: {e}")

        return StreamingResponse(
            streaming_with_store(),
            media_type="text/event-stream",
            headers={
                "Cache-Control": "no-cache",
                "Connection": "keep-alive",
                "X-Accel-Buffering": "no",
            },
        )

    except HTTPException:
        raise
    except Exception as e:
        logger.error(f"Error in streaming simulation: {e}")
        if request_log and not request_log.finalized:
            request_log.record_error(f"simulation error: {e}")
            request_log.finalize("error")
        error_response = build_error_response(
            response_id=response_id,
            error_type="server_error",
            error_code="simulation_error",
            message=str(e),
            model=model_name,
        )
        return JSONResponse(error_response, status_code=500)


async def _extract_stream_iterator(
    streaming_response: StreamingResponse,
) -> AsyncIterator[bytes]:
    """Extract the async iterator from a StreamingResponse.

    Args:
        streaming_response: The StreamingResponse object

    Yields:
        Chunks from the stream
    """
    body_iterator = streaming_response.body_iterator
    async for chunk in body_iterator:
        if isinstance(chunk, bytes):
            yield chunk
        else:
            yield chunk.encode("utf-8")
