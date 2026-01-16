"""App key authentication and authorization for yaLLMproxy."""

from __future__ import annotations

import hmac
import logging
import secrets
from dataclasses import dataclass
from typing import Any

from fastapi import HTTPException, Request

logger = logging.getLogger("yallmp-proxy")

DEFAULT_HEADER_NAME = "x-api-key"


@dataclass
class AppKeyContext:
    """Context object for authenticated app key requests."""

    key_id: str | None
    key_name: str | None
    authenticated: bool
    # Future fields for rate limiting and priority
    # priority: str | None = None
    # rate_limits: dict[str, Any] | None = None


class AppKeyValidator:
    """Validates app keys and model access permissions."""

    def __init__(self) -> None:
        self._config_store: Any = None

    def _get_config_store(self) -> Any:
        """Lazy-load config store to avoid circular imports."""
        if self._config_store is None:
            from ..config_store import CONFIG_STORE

            self._config_store = CONFIG_STORE
        return self._config_store

    def get_app_keys_config(self) -> dict[str, Any]:
        """Get the app_keys configuration section."""
        config = self._get_config_store().get_runtime_config()
        return config.get("app_keys", {})

    def is_enabled(self) -> bool:
        """Check if app key authentication is enabled."""
        app_keys_config = self.get_app_keys_config()
        return bool(app_keys_config.get("enabled", False))

    def validate_request(
        self,
        request: Request,
        model_name: str,
    ) -> AppKeyContext:
        """Validate an incoming request and return auth context.

        Args:
            request: The FastAPI request object.
            model_name: The model being requested.

        Returns:
            AppKeyContext with authentication details.

        Raises:
            HTTPException: 401 for invalid/missing key, 403 for unauthorized model.
        """
        app_keys_config = self.get_app_keys_config()

        # If app keys not enabled, return unauthenticated context
        if not app_keys_config.get("enabled", False):
            return AppKeyContext(
                key_id=None,
                key_name=None,
                authenticated=False,
            )

        header_name = app_keys_config.get("header_name", DEFAULT_HEADER_NAME)
        provided_key = request.headers.get(header_name)

        # Also check Authorization header with Bearer prefix
        if not provided_key:
            auth_header = request.headers.get("Authorization", "")
            if auth_header.lower().startswith("bearer "):
                provided_key = auth_header[7:].strip()

        if not provided_key:
            if app_keys_config.get("allow_unauthenticated", False):
                return AppKeyContext(
                    key_id=None,
                    key_name=None,
                    authenticated=False,
                )
            logger.warning("Request rejected: missing API key")
            raise HTTPException(
                status_code=401,
                detail={
                    "error": {
                        "message": "API key required",
                        "type": "authentication_error",
                        "code": "missing_api_key",
                    }
                },
            )

        # Find and validate the key
        key_config = self._find_key_by_secret(provided_key, app_keys_config)
        if not key_config:
            logger.warning("Request rejected: invalid API key")
            raise HTTPException(
                status_code=401,
                detail={
                    "error": {
                        "message": "Invalid API key",
                        "type": "authentication_error",
                        "code": "invalid_api_key",
                    }
                },
            )

        # Check if key is enabled
        if not key_config.get("enabled", True):
            logger.warning(
                "Request rejected: disabled API key '%s'", key_config.get("key_id")
            )
            raise HTTPException(
                status_code=401,
                detail={
                    "error": {
                        "message": "API key is disabled",
                        "type": "authentication_error",
                        "code": "disabled_api_key",
                    }
                },
            )

        key_id = key_config.get("key_id", "unknown")

        # Check model access
        if not self._check_model_access(key_id, model_name):
            logger.warning(
                "Request rejected: key '%s' not authorized for model '%s'",
                key_id,
                model_name,
            )
            raise HTTPException(
                status_code=403,
                detail={
                    "error": {
                        "message": f"API key not authorized for model '{model_name}'",
                        "type": "permission_error",
                        "code": "model_access_denied",
                    }
                },
            )

        logger.debug("Request authenticated with key '%s' for model '%s'", key_id, model_name)
        return AppKeyContext(
            key_id=key_id,
            key_name=key_config.get("name"),
            authenticated=True,
        )

    def _find_key_by_secret(
        self, provided_secret: str, app_keys_config: dict[str, Any]
    ) -> dict[str, Any] | None:
        """Find a key by its secret value using constant-time comparison.

        Args:
            provided_secret: The secret provided by the client.
            app_keys_config: The app_keys configuration section.

        Returns:
            The key configuration if found, None otherwise.
        """
        keys = app_keys_config.get("keys", [])
        if not isinstance(keys, list):
            return None

        for key_entry in keys:
            if not isinstance(key_entry, dict):
                continue
            stored_secret = key_entry.get("secret", "")
            if not stored_secret:
                continue
            # Use constant-time comparison to prevent timing attacks
            if hmac.compare_digest(provided_secret, str(stored_secret)):
                return key_entry

        return None

    def _check_model_access(self, key_id: str, model_name: str) -> bool:
        """Check if a key has access to a specific model.

        Args:
            key_id: The key ID to check.
            model_name: The model name to check access for.

        Returns:
            True if access is allowed, False otherwise.
        """
        config = self._get_config_store().get_runtime_config()
        model_list = config.get("model_list", [])

        for model in model_list:
            if not isinstance(model, dict):
                continue
            if model.get("model_name") != model_name:
                continue

            access_control = model.get("access_control", {})
            if not isinstance(access_control, dict):
                # No access control defined, allow all
                return True

            allowed_keys = access_control.get("allowed_keys", "all")

            if allowed_keys == "all":
                return True
            if allowed_keys == "none":
                return False
            if isinstance(allowed_keys, list):
                return key_id in allowed_keys

            # Default: allow access
            return True

        # Model not found in config - let router handle the error
        return True


# Singleton instance
_validator: AppKeyValidator | None = None


def get_app_key_validator() -> AppKeyValidator:
    """Get the singleton AppKeyValidator instance."""
    global _validator
    if _validator is None:
        _validator = AppKeyValidator()
    return _validator


def generate_app_key_secret(length: int = 32) -> str:
    """Generate a secure random secret for an app key.

    Args:
        length: The number of random bytes (default 32, resulting in 64 hex chars).

    Returns:
        A URL-safe base64-encoded secret string.
    """
    return secrets.token_urlsafe(length)
