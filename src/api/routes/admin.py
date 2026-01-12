"""Admin endpoints for runtime model registration."""

import hmac
import json
import logging
from typing import Any, Optional

from fastapi import HTTPException, Request

from ...config_store import CONFIG_STORE, _normalize_protected
from ...core import Backend, extract_api_type, extract_target_model
from ...core.registry import get_router

logger = logging.getLogger("yallmp-proxy")
ADMIN_PASSWORD_HEADER = "x-admin-password"


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


def _extract_admin_password(request: Request, payload: dict[str, Any]) -> str | None:
    header = request.headers.get(ADMIN_PASSWORD_HEADER)
    if header and header.strip():
        return header.strip()
    query = request.query_params.get("admin_password")
    if query and query.strip():
        return query.strip()
    value = payload.get("admin_password")
    if isinstance(value, str) and value.strip():
        return value.strip()
    return None


def _require_admin_password(request: Request, payload: dict[str, Any], detail: str) -> None:
    expected = CONFIG_STORE.get_admin_password()
    if not expected:
        raise HTTPException(
            status_code=403,
            detail="Admin password is not configured in .env",
        )
    provided = _extract_admin_password(request, payload)
    if not provided or not hmac.compare_digest(provided, expected):
        raise HTTPException(status_code=403, detail=detail)


def _payload_has_api_key(payload: dict[str, Any]) -> bool:
    if "api_key" in payload:
        return True
    params = payload.get("model_params")
    if isinstance(params, dict) and "api_key" in params:
        return True
    return False


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
        for key in ("model_name", "name", "fallbacks", "protected", "admin_password"):
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
    if "extends" in payload:
        entry["extends"] = payload.get("extends")
    if "parameters" in payload:
        entry["parameters"] = payload["parameters"]

    modules_payload = None
    if "modules" in payload:
        modules_payload = payload.get("modules")
    elif "parsers" in payload:
        modules_payload = payload.get("parsers")
    if modules_payload is not None:
        entry["modules"] = modules_payload
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
    - protected: Optional bool to mark the model as password-protected
    
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
    except ValueError as exc:
        logger.error("Failed to register model: %s", exc)
        raise HTTPException(status_code=400, detail=str(exc)) from exc

    existing_model = CONFIG_STORE.find_model(backend.name)
    existing_protected = False
    if existing_model:
        existing_protected = _normalize_protected(
            existing_model.get("protected"), default=True
        )

    requested_protected_raw = payload.get("protected")
    if requested_protected_raw is None:
        effective_protected = existing_protected if existing_model else False
    else:
        effective_protected = _normalize_protected(
            requested_protected_raw,
            default=existing_protected if existing_model else False,
        )

    if existing_protected or effective_protected:
        _require_admin_password(
            request,
            payload,
            detail=f"Admin password required to modify protected model '{backend.name}'",
        )

    router = get_router()
    backend.editable = not effective_protected

    payload_has_key = _payload_has_api_key(payload)
    if not payload_has_key and existing_model:
        existing_params = existing_model.get("model_params") or {}
        if "api_key" in existing_params:
            model_entry.setdefault("model_params", {})["api_key"] = existing_params["api_key"]

    if not payload_has_key:
        existing_backend = router.backends.get(backend.name)
        if existing_backend and existing_backend.api_key:
            backend.api_key = existing_backend.api_key
        else:
            runtime_cfg = CONFIG_STORE.get_runtime_config()
            for model in runtime_cfg.get("model_list", []):
                if model.get("model_name") == backend.name:
                    params = model.get("model_params") or {}
                    api_key = params.get("api_key")
                    if isinstance(api_key, str) and api_key:
                        backend.api_key = api_key
                    break

    model_entry["protected"] = effective_protected
    replaced = CONFIG_STORE.upsert_model(model_entry, fallbacks)

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
        "protected": effective_protected,
    }
