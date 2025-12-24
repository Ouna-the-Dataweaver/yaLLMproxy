"""Backend configuration and utilities."""

import json
import logging
from dataclasses import dataclass
from typing import Any, Mapping, Optional

from fastapi import HTTPException

from .exceptions import BackendRetryableError

logger = logging.getLogger("yallmp-proxy")

DEFAULT_TIMEOUT = 60
DEFAULT_RETRY_DELAY = 0.25
MAX_RETRY_DELAY = 2.0
RETRYABLE_STATUSES = {408, 409, 429, 500, 502, 503, 504}


@dataclass
class Backend:
    """Represents a backend LLM provider."""
    
    name: str
    base_url: str
    api_key: str
    timeout: Optional[float]
    target_model: Optional[str]
    api_type: str = "openai"
    supports_reasoning: bool = False

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
    incoming: Mapping[str, str], backend_api_key: str
) -> dict[str, str]:
    """Build headers for outbound requests to backends."""
    headers: dict[str, str] = {}
    normalized_keys: set[str] = set()
    for key, value in incoming.items():
        key_lower = key.lower()
        # Strip hop-by-hop headers and sensitive headers
        if key_lower in HOP_BY_HOP_HEADERS or key_lower in {
            "authorization", 
            "host", 
            "content-length", 
            "accept-encoding"
        }:
            continue
        if key_lower in normalized_keys:
            continue
        headers[key] = value
        normalized_keys.add(key_lower)

    if "content-type" not in normalized_keys:
        headers["Content-Type"] = incoming.get("content-type", "application/json")
        normalized_keys.add("content-type")
    if backend_api_key:
        headers["Authorization"] = f"Bearer {backend_api_key}"
        normalized_keys.add("authorization")
    # Explicitly request uncompressed responses
    headers["Accept-Encoding"] = "identity"
    return headers


def build_backend_body(
    payload: Mapping[str, Any], backend: Backend, original_body: bytes
) -> bytes:
    """Build the request body for a backend, rewriting model names as needed."""
    import asyncio
    
    target_model = backend.target_model
    needs_thinking = False
    if backend.supports_reasoning:
        thinking = payload.get("thinking")
        needs_thinking = not (
            isinstance(thinking, Mapping) and thinking.get("type")
        )

    if not target_model and not needs_thinking:
        return original_body

    try:
        updated_payload = dict(payload)
        if target_model:
            updated_payload["model"] = target_model
            logger.debug(
                "Rewrote model for backend %s to %s", backend.name, target_model
            )
        if needs_thinking:
            updated_payload["thinking"] = {"type": "enabled"}
            logger.debug("Enabled reasoning block for backend %s", backend.name)
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

