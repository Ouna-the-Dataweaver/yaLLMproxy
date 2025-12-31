"""Admin endpoints for runtime model registration."""

import json
import logging
from typing import Any, Optional

from fastapi import HTTPException, Request

from ...config_store import CONFIG_STORE
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


def _backend_from_runtime_payload(
    payload: dict[str, Any]
) -> tuple[Backend, Optional[list[str]], dict[str, Any]]:
    """Create a Backend instance from runtime registration payload.
    
    Args:
        payload: The JSON payload from the registration request.
    
    Returns:
        A tuple of (Backend, fallbacks list, model config entry).
    
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
        editable=True,
    )
    model_entry = _build_model_entry(
        payload,
        model_name,
        api_base,
        api_key,
        timeout_raw,
        api_type,
        target_model,
        supports_reasoning,
    )
    return backend, fallbacks, model_entry


def _normalize_config_scope(value: Any) -> str:
    if value is None:
        return "added"
    scope = str(value).strip().lower()
    if scope in {"added", "default"}:
        return scope
    raise ValueError("config_scope must be 'added' or 'default'")


def _build_model_entry(
    payload: dict[str, Any],
    model_name: str,
    api_base: str,
    api_key: str,
    timeout_raw: Any,
    api_type: str,
    target_model: Optional[str],
    supports_reasoning: bool,
) -> dict[str, Any]:
    params = dict(payload.get("model_params") or {})
    if not params:
        params = dict(payload)
        for key in ("model_name", "name", "fallbacks", "config_scope"):
            params.pop(key, None)

    if "api_base" not in params and "base_url" in params:
        params["api_base"] = params.pop("base_url")
    if "api_base" not in params:
        params["api_base"] = api_base
    if api_key and "api_key" not in params:
        params["api_key"] = api_key
    if "request_timeout" not in params and "timeout" in params:
        params["request_timeout"] = params.pop("timeout")
    if "request_timeout" not in params and timeout_raw is not None:
        params["request_timeout"] = timeout_raw
    if "api_type" not in params:
        params["api_type"] = api_type
    if "supports_reasoning" not in params:
        params["supports_reasoning"] = supports_reasoning
    if "model" not in params and target_model:
        params["model"] = target_model
    params.pop("target_model", None)

    for key, value in list(params.items()):
        if value is None:
            params.pop(key, None)
        elif isinstance(value, str) and not value.strip():
            params.pop(key, None)

    entry = {
        "model_name": model_name,
        "model_params": params,
    }
    for key in ("parameters", "parsers"):
        if key in payload:
            entry[key] = payload[key]
    return entry


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
    - config_scope: Optional target config ("added" or "default", default: "added")
    
    Returns:
        A dictionary with status, model name, and whether it was replaced.
    """
    try:
        payload = await request.json()
    except json.JSONDecodeError as exc:
        logger.error("Invalid JSON when registering model: %s", exc)
        raise HTTPException(status_code=400, detail="Invalid JSON body") from exc

    try:
        backend, fallbacks, model_entry = _backend_from_runtime_payload(payload)
        config_scope = _normalize_config_scope(payload.get("config_scope"))
    except ValueError as exc:
        logger.error("Failed to register model: %s", exc)
        raise HTTPException(status_code=400, detail=str(exc)) from exc

    default_models = CONFIG_STORE.get_default_raw().get("model_list", []) or []
    added_models = CONFIG_STORE.get_added_raw().get("model_list", []) or []
    default_names = {m.get("model_name") for m in default_models}
    added_names = {m.get("model_name") for m in added_models}

    if config_scope == "added" and backend.name in default_names:
        raise HTTPException(
            status_code=409,
            detail=(
                f"Model '{backend.name}' exists in config_default.yaml. "
                "Use config_scope='default' or choose another name."
            ),
        )
    if config_scope == "default" and backend.name in added_names:
        raise HTTPException(
            status_code=409,
            detail=(
                f"Model '{backend.name}' exists in config_added.yaml. "
                "Delete it first or choose another name."
            ),
        )

    router = get_router()
    backend.editable = config_scope == "added"
    if config_scope == "default":
        replaced = CONFIG_STORE.upsert_default_model(model_entry, fallbacks)
    else:
        replaced = CONFIG_STORE.upsert_added_model(model_entry, fallbacks)

    await router.register_backend(backend, fallbacks)
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
        "config_scope": config_scope,
    }
