"""In-memory usage counters for realtime usage reporting."""

from __future__ import annotations

from dataclasses import dataclass, field
from datetime import datetime, timezone
from threading import Lock
from typing import Any


class RequestTracker:
    """Track a single request lifecycle for in-memory counters."""

    def __init__(self, counters: "UsageCounters") -> None:
        self._counters = counters
        self._finished = False

    def finish(self) -> None:
        if self._finished:
            return
        self._finished = True
        self._counters.finish_request()


@dataclass
class UsageCounters:
    """Thread-safe counters for request lifecycle tracking."""

    _lock: Lock = field(default_factory=Lock, repr=False)
    _started_at: str = field(
        default_factory=lambda: datetime.now(timezone.utc).isoformat()
    )
    _received: int = 0
    _served: int = 0
    _ongoing: int = 0

    def start_request(self) -> RequestTracker:
        with self._lock:
            self._received += 1
            self._ongoing += 1
        return RequestTracker(self)

    def finish_request(self) -> None:
        with self._lock:
            self._served += 1
            if self._ongoing > 0:
                self._ongoing -= 1
            else:
                self._ongoing = 0

    def snapshot(self) -> dict[str, Any]:
        with self._lock:
            return {
                "started_at": self._started_at,
                "received": self._received,
                "served": self._served,
                "ongoing": self._ongoing,
            }


USAGE_COUNTERS = UsageCounters()


def build_usage_snapshot() -> dict[str, Any]:
    """Build the usage payload with realtime counters and a history placeholder."""
    return {
        "generated_at": datetime.now(timezone.utc).isoformat(),
        "realtime": USAGE_COUNTERS.snapshot(),
        "historical": {
            "enabled": False,
            "status": "not_configured",
            "provider": None,
            "message": "Database logging is not configured yet.",
        },
    }
