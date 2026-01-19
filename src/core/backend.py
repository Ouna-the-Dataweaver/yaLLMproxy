"""Backend configuration and utilities."""

import json
import logging
from dataclasses import dataclass, field
from typing import Any, Mapping, Optional

from fastapi import HTTPException

from .exceptions import BackendRetryableError

logger = logging.getLogger("yallmp-proxy")

DEFAULT_TIMEOUT = 60
DEFAULT_RETRY_DELAY = 0.25
MAX_RETRY_DELAY = 2.0
RETRYABLE_STATUSES = {408, 409, 429, 500, 502, 503, 504}


@dataclass
class ParameterConfig:
    """Configuration for a single LLM parameter.

    Attributes:
        default: The default value to use when the parameter is missing from
            the incoming request. Also the value used when allow_override is False.
        allow_override: If True, the proxy uses the request value if present,
            falling back to default. If False, the proxy always uses default,
            ignoring the request value.
    """
    default: Any
    allow_override: bool = True


@dataclass
class Backend:
    """Represents a backend LLM provider."""

    name: str
    base_url: str
    api_key: str
    timeout: Optional[float]
    target_model: Optional[str]
    api_type: str = "openai"
    anthropic_version: Optional[str] = None
    supports_reasoning: bool = False
    supports_responses_api: bool = False  # Backend natively supports /v1/responses
    http2: bool = False
    editable: bool = False
    parameters: dict[str, ParameterConfig] = field(default_factory=dict)

    def build_url(self, path: str, query: str) -> str:
        """Build the full URL for a backend request."""
        base = self.base_url.rstrip("/")
        normalized_path = path or ""
        if not normalized_path.startswith("/"):
            normalized_path = f"/{normalized_path}"

        if normalized_path.startswith("/v1"):
            normalized_path = normalized_path[len("/v1"):]
            if not normalized_path:
                normalized_path = "/"
        url = f"{base}{normalized_path}"
        if query:
            separator = "&" if "?" in url else "?"
            url = f"{url}{separator}{query}"
        return url


def _parse_bool(value: Any) -> bool:
    if isinstance(value, str):
        return value.strip().lower() in {"1", "true", "yes", "on"}
    return bool(value)


HOP_BY_HOP_HEADERS = {
    "connection",
    "keep-alive",
    "proxy-authenticate",
    "proxy-authorization",
    "te",
    "trailers",
    "transfer-encoding",
    "upgrade",
}


def _safe_headers_for_log(headers: Mapping[str, str]) -> dict[str, str]:
    try:
        from ..logging.recorder import RequestLogRecorder

        return RequestLogRecorder._safe_headers(headers)
    except Exception:
        masked: dict[str, str] = {}
        for key, value in headers.items():
            key_lower = str(key).lower()
            if key_lower in {"authorization", "proxy-connection", "x-api-key"}:
                masked[str(key)] = "****"
            else:
                masked[str(key)] = str(value)
        return masked


def format_httpx_error(exc: Any, backend: Backend, url: Optional[str] = None) -> str:
    """Produce a detailed, user-facing description of an httpx error."""
    import httpx
    
    parts = [exc.__class__.__name__]
    message = str(exc).strip()
    if message:
        parts.append(message)

    request = getattr(exc, "request", None)
    if request is not None:
        parts.append(f"request={request.method} {request.url}")
    elif url:
        parts.append(f"url={url}")

    if isinstance(exc, httpx.TimeoutException):
        timeout = backend.timeout or DEFAULT_TIMEOUT
        parts.append(f"timeout={timeout}s")

    return "; ".join(parts)


def build_outbound_headers(
    incoming: Mapping[str, str],
    backend_api_key: str,
    is_stream: bool = False,
    api_type: Optional[str] = None,
    anthropic_version: Optional[str] = None,
) -> dict[str, str]:
    """Build headers for outbound requests to backends."""
    headers: dict[str, str] = {}
    normalized_keys: set[str] = set()
    normalized_api_type = (api_type or "openai").strip().lower()
    
    if logger.isEnabledFor(logging.DEBUG):
        logger.debug("Incoming headers: %s", _safe_headers_for_log(incoming))
    
    for key, value in incoming.items():
        key_lower = key.lower()
        # Strip hop-by-hop headers and sensitive headers
        # Note: We preserve accept-encoding to allow compression negotiation
        if key_lower in HOP_BY_HOP_HEADERS or key_lower in {
            "authorization",
            "host",
            "content-length"
        }:
            logger.debug(f"Stripping header: {key}")
            continue
        if normalized_api_type == "anthropic" and key_lower == "x-api-key":
            logger.debug("Stripping client x-api-key for anthropic backend")
            continue
        if key_lower in normalized_keys:
            continue
        headers[key] = value
        normalized_keys.add(key_lower)

    if "content-type" not in normalized_keys:
        headers["Content-Type"] = incoming.get("content-type", "application/json")
        normalized_keys.add("content-type")
    if is_stream:
        headers["Accept"] = "text/event-stream"
        normalized_keys.add("accept")
        # Force identity encoding for streaming to avoid compressed SSE bytes
        for existing_key in list(headers.keys()):
            if existing_key.lower() == "accept-encoding":
                headers.pop(existing_key, None)
        headers["Accept-Encoding"] = "identity"
        normalized_keys.add("accept-encoding")
    if backend_api_key:
        if normalized_api_type == "anthropic":
            headers["x-api-key"] = backend_api_key
            normalized_keys.add("x-api-key")
        else:
            headers["Authorization"] = f"Bearer {backend_api_key}"
            normalized_keys.add("authorization")

    if normalized_api_type == "anthropic":
        if "anthropic-version" not in normalized_keys and anthropic_version:
            headers["anthropic-version"] = anthropic_version
            normalized_keys.add("anthropic-version")
    
    if logger.isEnabledFor(logging.DEBUG):
        logger.debug("Built outbound headers: %s", _safe_headers_for_log(headers))
        logger.debug("Outbound header count: %s", len(headers))
    return headers


def build_backend_body(
    payload: Mapping[str, Any],
    backend: Backend,
    original_body: bytes,
    is_stream: bool = False,
) -> bytes:
    """Build the request body for a backend, rewriting model names and applying parameter overrides as needed."""
    import asyncio

    target_model = backend.target_model
    needs_thinking = False
    thinking_type_to_set = None
    
    if backend.supports_reasoning:
        thinking = payload.get("thinking")
        if isinstance(thinking, Mapping):
            # User explicitly specified thinking parameter - respect their choice
            thinking_type = thinking.get("type")
            if thinking_type == "enabled":
                needs_thinking = True
                thinking_type_to_set = "enabled"
            elif thinking_type == "disabled":
                needs_thinking = False
                # Don't add anything - let the backend use its default (usually disabled)
            else:
                # Unknown type, enable thinking as default for reasoning-enabled backends
                needs_thinking = True
                thinking_type_to_set = "enabled"
        else:
            # No thinking parameter specified, enable thinking by default for this backend
            needs_thinking = True
            thinking_type_to_set = "enabled"

    # Check if any parameter transformation is needed
    needs_param_override = bool(backend.parameters)

    if not target_model and not needs_thinking and not is_stream and not needs_param_override:
        return original_body

    try:
        updated_payload = dict(payload)
        if is_stream and updated_payload.get("stream") is not True:
            updated_payload["stream"] = True
        if target_model:
            updated_payload["model"] = target_model
            logger.debug(
                "Rewrote model for backend %s to %s", backend.name, target_model
            )
        if needs_thinking and thinking_type_to_set:
            updated_payload["thinking"] = {"type": thinking_type_to_set}
            logger.debug("Set thinking type to '%s' for backend %s", thinking_type_to_set, backend.name)

        # Apply parameter overrides
        for param_name, config in backend.parameters.items():
            if config.allow_override:
                # Use request value if present, else default
                if param_name not in updated_payload:
                    updated_payload[param_name] = config.default
                    logger.debug(
                        "Applied default %s=%s for backend %s (request missing)",
                        param_name, config.default, backend.name
                    )
            else:
                # Always use configured value, ignoring request
                updated_payload[param_name] = config.default
                logger.debug(
                    "Forced %s=%s for backend %s (override disabled)",
                    param_name, config.default, backend.name
                )

        rewritten = json.dumps(updated_payload).encode("utf-8")
        return rewritten
    except (TypeError, ValueError) as exc:
        logger.warning(
            "Failed to rewrite payload for backend %s: %s", backend.name, exc
        )
        return original_body


def filter_response_headers(headers: Mapping[str, str]) -> dict[str, str]:
    """Filter response headers, removing hop-by-hop headers."""
    filtered: dict[str, str] = {}
    for key, value in headers.items():
        key_lower = key.lower()
        # Drop headers FastAPI will recompute or that no longer match the payload
        if key_lower in HOP_BY_HOP_HEADERS or key_lower in {
            "content-length", 
            "transfer-encoding", 
            "content-encoding"
        }:
            continue
        filtered[key] = value
    return filtered


def normalize_request_model(model_name: str) -> str:
    """Normalize client-supplied model name for routing."""
    if not isinstance(model_name, str):
        return ""
    stripped = model_name.strip()
    if not stripped:
        return ""

    lower = stripped.lower()
    if "/" in stripped:
        prefix, remainder = stripped.split("/", 1)
        if remainder and prefix.lower() in {"openai"}:
            return remainder
    return stripped


def extract_target_model(
    params: Mapping[str, Any], api_type: Optional[str] = None
) -> Optional[str]:
    """Extract the target model name from parameters."""
    override = params.get("target_model") or params.get("forward_model")
    if override:
        override_str = str(override).strip()
        if override_str:
            return override_str

    raw_model = str(params.get("model") or "").strip()
    if not raw_model:
        return None

    normalized_api_type = str(
        api_type or params.get("api_type") or "openai"
    ).strip().lower() or "openai"
    expected_prefix = f"{normalized_api_type}/"
    lower_model = raw_model.lower()

    if lower_model.startswith(expected_prefix):
        remainder = raw_model[len(expected_prefix):]
        if remainder:
            return remainder

    if lower_model.startswith("openai/"):
        _, remainder = raw_model.split("/", 1)
        if remainder:
            return remainder

    return raw_model


def extract_api_type(params: Mapping[str, Any]) -> str:
    """Extract and normalize the API type from parameters."""
    raw_api_type = params.get("api_type")
    if raw_api_type is None:
        return "openai"
    normalized = str(raw_api_type).strip().lower()
    return normalized or "openai"
