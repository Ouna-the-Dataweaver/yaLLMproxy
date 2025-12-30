"""Admin endpoints for runtime model registration."""

import json
import logging
from typing import Any, Optional

from fastapi import HTTPException, Request

from ...core import Backend, extract_api_type, extract_target_model
from ...core.registry import get_router

logger = logging.getLogger("yallmp-proxy")


def _normalize_timeout(value: Any) -> Optional[float]:
    """Normalize timeout value to float or None."""
    if value is None:
        return None
    try:
        return float(value)
    except (TypeError, ValueError) as exc:
        raise ValueError("request_timeout must be numeric") from exc


def _normalize_fallbacks(value: Any) -> Optional[list[str]]:
    """Normalize fallbacks value to list of strings or None."""
    if value is None:
        return None
    if isinstance(value, (list, tuple)):
        return [str(v) for v in value if v]
    if isinstance(value, str):
        return [value]
    raise ValueError("fallbacks must be a string or list of strings")


def _backend_from_runtime_payload(payload: dict[str, Any]) -> tuple[Backend, Optional[list[str]]]:
    """Create a Backend instance from runtime registration payload.
    
    Args:
        payload: The JSON payload from the registration request.
    
    Returns:
        A tuple of (Backend, fallbacks list).
    
    Raises:
        ValueError: If required fields are missing or invalid.
    """
    if not isinstance(payload, dict):
        raise ValueError("body must be a JSON object")

    model_name = str(payload.get("model_name") or payload.get("name") or "").strip()
    if not model_name:
        raise ValueError("model_name is required")

    params = payload.get("model_params") or payload
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
    api_type = extract_api_type(params)
    target_model = extract_target_model(params, api_type)
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


async def register_model(request: Request) -> dict:
    """Register a new backend at runtime without restarting the proxy.
    
    POST /admin/models
    
    Request body should contain:
    - model_name: The name to register this backend as
    - model_params: Dictionary with:
        - api_base: The base URL of the backend API
        - api_key: Optional API key for the backend
        - request_timeout: Optional timeout in seconds
        - api_type: Optional API type (default: "openai")
        - target_model: Optional target model name override
        - supports_reasoning: Optional bool for reasoning support
    
    Returns:
        A dictionary with status, model name, and whether it was replaced.
    """
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

    router = get_router()
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
