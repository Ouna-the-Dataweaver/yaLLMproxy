"""Admin endpoints for app key management."""

import hmac
import json
import logging
import uuid
from typing import Any

from fastapi import HTTPException, Request

from ...auth.app_key import generate_app_key_secret
from ...config_store import CONFIG_STORE

logger = logging.getLogger("yallmp-proxy")
ADMIN_PASSWORD_HEADER = "x-admin-password"


def _extract_admin_password(request: Request, payload: dict[str, Any]) -> str | None:
    """Extract admin password from header, query param, or body."""
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
    """Require admin password for protected operations."""
    expected = CONFIG_STORE.get_admin_password()
    if not expected:
        raise HTTPException(
            status_code=403,
            detail="Admin password is not configured in .env",
        )
    provided = _extract_admin_password(request, payload)
    if not provided or not hmac.compare_digest(provided, expected):
        raise HTTPException(status_code=403, detail=detail)


async def list_app_keys(request: Request) -> dict[str, Any]:
    """List all configured app keys with secrets masked.

    GET /admin/keys

    Returns:
        Dictionary with enabled status and list of keys (without secrets).
    """
    app_keys_config = CONFIG_STORE.get_app_keys_config()
    keys = CONFIG_STORE.list_app_keys(mask_secrets=True)

    return {
        "enabled": app_keys_config.get("enabled", False),
        "header_name": app_keys_config.get("header_name", "x-api-key"),
        "allow_unauthenticated": app_keys_config.get("allow_unauthenticated", False),
        "keys": keys,
    }


async def get_app_key(key_id: str, request: Request) -> dict[str, Any]:
    """Get a specific app key by ID.

    GET /admin/keys/{key_id}

    Args:
        key_id: The key ID to retrieve.

    Returns:
        The key configuration (without secret).
    """
    key = CONFIG_STORE.get_app_key(key_id, mask_secret=True)
    if not key:
        raise HTTPException(status_code=404, detail=f"App key '{key_id}' not found")
    return key


async def create_app_key(request: Request) -> dict[str, Any]:
    """Create a new app key.

    POST /admin/keys

    Request body:
        - key_id: Optional string, will generate UUID if omitted
        - secret: Optional string, will generate secure random if omitted
        - name: Optional display name
        - description: Optional description
        - enabled: Optional bool (default: true)

    Returns:
        The created key with secret (shown only once).
    """
    try:
        payload = await request.json()
    except json.JSONDecodeError as exc:
        raise HTTPException(status_code=400, detail="Invalid JSON body") from exc

    if not isinstance(payload, dict):
        raise HTTPException(status_code=400, detail="Request body must be a JSON object")

    _require_admin_password(request, payload, "Admin password required to create app keys")

    # Generate key_id if not provided
    key_id = payload.get("key_id")
    if not key_id:
        key_id = f"key-{uuid.uuid4().hex[:12]}"

    # Check if key already exists
    existing = CONFIG_STORE.get_app_key(key_id)
    if existing:
        raise HTTPException(
            status_code=409,
            detail=f"App key '{key_id}' already exists. Use PUT to update.",
        )

    # Generate secret if not provided
    secret = payload.get("secret")
    if not secret:
        secret = generate_app_key_secret()

    key_entry = {
        "key_id": key_id,
        "secret": secret,
        "name": payload.get("name", ""),
        "description": payload.get("description", ""),
        "enabled": payload.get("enabled", True),
    }

    # Remove empty optional fields
    if not key_entry["name"]:
        del key_entry["name"]
    if not key_entry["description"]:
        del key_entry["description"]

    CONFIG_STORE.upsert_app_key(key_entry)
    logger.info("Created app key '%s'", key_id)

    return {
        "status": "ok",
        "key_id": key_id,
        "secret": secret,
        "message": "Store this secret securely - it will not be shown again",
    }


async def update_app_key(key_id: str, request: Request) -> dict[str, Any]:
    """Update an app key's metadata (not the secret).

    PUT /admin/keys/{key_id}

    Args:
        key_id: The key ID to update.

    Request body:
        - name: Optional new display name
        - description: Optional new description
        - enabled: Optional bool

    Returns:
        Success status and updated key (without secret).
    """
    try:
        payload = await request.json()
    except json.JSONDecodeError as exc:
        raise HTTPException(status_code=400, detail="Invalid JSON body") from exc

    if not isinstance(payload, dict):
        raise HTTPException(status_code=400, detail="Request body must be a JSON object")

    _require_admin_password(request, payload, "Admin password required to update app keys")

    existing = CONFIG_STORE.get_app_key(key_id, mask_secret=False)
    if not existing:
        raise HTTPException(status_code=404, detail=f"App key '{key_id}' not found")

    # Update allowed fields only (not secret)
    updated_entry = {
        "key_id": key_id,
        "secret": existing.get("secret"),  # Preserve existing secret
    }

    # Update fields if provided
    if "name" in payload:
        updated_entry["name"] = payload["name"]
    elif "name" in existing:
        updated_entry["name"] = existing["name"]

    if "description" in payload:
        updated_entry["description"] = payload["description"]
    elif "description" in existing:
        updated_entry["description"] = existing["description"]

    if "enabled" in payload:
        updated_entry["enabled"] = bool(payload["enabled"])
    else:
        updated_entry["enabled"] = existing.get("enabled", True)

    CONFIG_STORE.upsert_app_key(updated_entry)
    logger.info("Updated app key '%s'", key_id)

    # Return without secret
    result = {k: v for k, v in updated_entry.items() if k != "secret"}
    return {
        "status": "ok",
        "key": result,
    }


async def delete_app_key(key_id: str, request: Request) -> dict[str, Any]:
    """Delete an app key.

    DELETE /admin/keys/{key_id}

    Args:
        key_id: The key ID to delete.

    Returns:
        Success status.
    """
    # Try to parse body for admin password, but allow empty body
    try:
        body = await request.body()
        payload = json.loads(body) if body else {}
    except json.JSONDecodeError:
        payload = {}

    if not isinstance(payload, dict):
        payload = {}

    _require_admin_password(request, payload, "Admin password required to delete app keys")

    deleted = CONFIG_STORE.delete_app_key(key_id)
    if not deleted:
        raise HTTPException(status_code=404, detail=f"App key '{key_id}' not found")

    logger.info("Deleted app key '%s'", key_id)
    return {
        "status": "ok",
        "key_id": key_id,
        "message": f"App key '{key_id}' deleted",
    }


async def regenerate_app_key(key_id: str, request: Request) -> dict[str, Any]:
    """Regenerate the secret for an existing app key.

    POST /admin/keys/{key_id}/regenerate

    Args:
        key_id: The key ID to regenerate.

    Returns:
        The new secret (shown only once).
    """
    try:
        body = await request.body()
        payload = json.loads(body) if body else {}
    except json.JSONDecodeError:
        payload = {}

    if not isinstance(payload, dict):
        payload = {}

    _require_admin_password(request, payload, "Admin password required to regenerate app keys")

    existing = CONFIG_STORE.get_app_key(key_id, mask_secret=False)
    if not existing:
        raise HTTPException(status_code=404, detail=f"App key '{key_id}' not found")

    # Generate new secret
    new_secret = generate_app_key_secret()

    # Update with new secret
    existing["secret"] = new_secret
    CONFIG_STORE.upsert_app_key(existing)

    logger.info("Regenerated secret for app key '%s'", key_id)
    return {
        "status": "ok",
        "key_id": key_id,
        "secret": new_secret,
        "message": "Store this secret securely - it will not be shown again",
    }


async def set_app_keys_enabled(request: Request) -> dict[str, Any]:
    """Enable or disable app key authentication.

    POST /admin/keys/config

    Request body:
        - enabled: bool - Enable or disable app key authentication
        - header_name: Optional string - Header name for API key
        - allow_unauthenticated: Optional bool - Allow requests without key

    Returns:
        Success status and current config.
    """
    try:
        payload = await request.json()
    except json.JSONDecodeError as exc:
        raise HTTPException(status_code=400, detail="Invalid JSON body") from exc

    if not isinstance(payload, dict):
        raise HTTPException(status_code=400, detail="Request body must be a JSON object")

    _require_admin_password(request, payload, "Admin password required to configure app keys")

    if "enabled" in payload:
        CONFIG_STORE.set_app_keys_enabled(bool(payload["enabled"]))

    # Return current config
    app_keys_config = CONFIG_STORE.get_app_keys_config()
    return {
        "status": "ok",
        "enabled": app_keys_config.get("enabled", False),
        "header_name": app_keys_config.get("header_name", "x-api-key"),
        "allow_unauthenticated": app_keys_config.get("allow_unauthenticated", False),
    }
