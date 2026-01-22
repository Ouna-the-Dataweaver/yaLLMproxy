"""Concurrency control module for per-key request limiting with priority queue.

This module provides:
- Per-key concurrency limits
- Priority-based request queuing
- Shared pool for unauthenticated requests

Usage:
    from src.concurrency import get_concurrency_manager, get_key_concurrency_config

    # Get concurrency settings for a key
    config = get_key_concurrency_config(key_id)

    # Acquire a slot (will queue if at limit)
    slot = await get_concurrency_manager().acquire(
        key_identifier=key_id,
        concurrency_limit=config.concurrency_limit,
        priority=config.priority,
        timeout=config.queue_timeout,
    )

    async with slot:
        # Process request
        response = await process_request()
"""

from __future__ import annotations

from typing import TYPE_CHECKING

from .config import KeyConcurrencyConfig, get_key_concurrency_config
from .exceptions import (
    ConcurrencyClientDisconnected,
    ConcurrencyError,
    ConcurrencyQueueTimeout,
)
from .slot import ConcurrencySlot

if TYPE_CHECKING:
    from .manager import ConcurrencyManager

__all__ = [
    "get_concurrency_manager",
    "reset_concurrency_manager",
    "get_key_concurrency_config",
    "KeyConcurrencyConfig",
    "ConcurrencySlot",
    "ConcurrencyError",
    "ConcurrencyQueueTimeout",
    "ConcurrencyClientDisconnected",
]


_manager: "ConcurrencyManager | None" = None


def get_concurrency_manager() -> "ConcurrencyManager":
    """Get the singleton ConcurrencyManager instance."""
    global _manager
    if _manager is None:
        from .manager import ConcurrencyManager

        _manager = ConcurrencyManager()
    return _manager


def reset_concurrency_manager() -> None:
    """Reset the singleton (for testing)."""
    global _manager
    _manager = None
