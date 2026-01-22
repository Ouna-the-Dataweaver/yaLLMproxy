"""Custom exceptions for concurrency control."""

from __future__ import annotations


class ConcurrencyError(Exception):
    """Base exception for concurrency-related errors."""

    pass


class ConcurrencyQueueTimeout(ConcurrencyError):
    """Raised when a request times out waiting in the concurrency queue."""

    pass


class ConcurrencyClientDisconnected(ConcurrencyError):
    """Raised when a client disconnects while waiting in the concurrency queue."""

    pass
