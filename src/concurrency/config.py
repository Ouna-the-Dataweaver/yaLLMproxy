"""Configuration helpers for concurrency control settings."""

from __future__ import annotations

from dataclasses import dataclass
from typing import Any


# Default values
DEFAULT_CONCURRENCY_LIMIT = 0  # 0 means no limit
DEFAULT_PRIORITY = 100
DEFAULT_UNAUTHENTICATED_LIMIT = 5
DEFAULT_UNAUTHENTICATED_PRIORITY = 1000


@dataclass
class KeyConcurrencyConfig:
    """Resolved concurrency configuration for a key."""

    concurrency_limit: int  # 0 means no limit
    priority: int
    queue_timeout: float | None  # seconds, None = no timeout


def get_key_concurrency_config(key_id: str | None) -> KeyConcurrencyConfig:
    """Get concurrency configuration for a specific key or unauthenticated.

    Resolution order:
    1. Key-specific settings (if key_id provided)
    2. app_keys.defaults settings
    3. Built-in defaults

    For unauthenticated (key_id=None):
    1. app_keys.unauthenticated settings
    2. Built-in defaults

    Args:
        key_id: The key ID to get config for, or None for unauthenticated.

    Returns:
        KeyConcurrencyConfig with resolved settings.
    """
    # Lazy import to avoid circular dependencies
    from ..config_store import CONFIG_STORE

    app_keys_config = CONFIG_STORE.get_app_keys_config()
    defaults = app_keys_config.get("defaults", {})
    if not isinstance(defaults, dict):
        defaults = {}

    if key_id is None:
        # Unauthenticated request
        unauth_config = app_keys_config.get("unauthenticated", {})
        if not isinstance(unauth_config, dict):
            unauth_config = {}
        return KeyConcurrencyConfig(
            concurrency_limit=_get_int(
                unauth_config, "concurrency_limit", DEFAULT_UNAUTHENTICATED_LIMIT
            ),
            priority=_get_int(
                unauth_config, "priority", DEFAULT_UNAUTHENTICATED_PRIORITY
            ),
            queue_timeout=_get_optional_float(unauth_config, "queue_timeout"),
        )

    # Find the specific key config
    keys = app_keys_config.get("keys", [])
    if not isinstance(keys, list):
        keys = []

    key_config: dict[str, Any] | None = None
    for k in keys:
        if isinstance(k, dict) and k.get("key_id") == key_id:
            key_config = k
            break

    if key_config:
        return KeyConcurrencyConfig(
            concurrency_limit=_get_int(
                key_config,
                "concurrency_limit",
                _get_int(defaults, "concurrency_limit", DEFAULT_CONCURRENCY_LIMIT),
            ),
            priority=_get_int(
                key_config,
                "priority",
                _get_int(defaults, "priority", DEFAULT_PRIORITY),
            ),
            queue_timeout=_get_optional_float(
                key_config,
                "queue_timeout",
                _get_optional_float(defaults, "queue_timeout"),
            ),
        )

    # Key not found in config - use defaults
    return KeyConcurrencyConfig(
        concurrency_limit=_get_int(defaults, "concurrency_limit", DEFAULT_CONCURRENCY_LIMIT),
        priority=_get_int(defaults, "priority", DEFAULT_PRIORITY),
        queue_timeout=_get_optional_float(defaults, "queue_timeout"),
    )


def _get_int(config: dict[str, Any], key: str, default: int) -> int:
    """Get an integer value from config with fallback."""
    value = config.get(key)
    if value is None:
        return default
    try:
        return int(value)
    except (ValueError, TypeError):
        return default


def _get_optional_float(
    config: dict[str, Any], key: str, default: float | None = None
) -> float | None:
    """Get an optional float value from config with fallback."""
    value = config.get(key)
    if value is None:
        return default
    try:
        return float(value)
    except (ValueError, TypeError):
        return default
