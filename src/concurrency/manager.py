"""Concurrency manager for per-key request limiting with priority queue."""

from __future__ import annotations

import asyncio
import heapq
import logging
import time
import uuid
from dataclasses import dataclass, field
from datetime import datetime, timezone
from typing import Any, Callable

from .exceptions import ConcurrencyClientDisconnected, ConcurrencyQueueTimeout
from .slot import ConcurrencySlot

logger = logging.getLogger("yallmp-proxy")


@dataclass(order=True)
class QueuedRequest:
    """Represents a request waiting in the priority queue.

    Ordering: (priority, timestamp, request_id) ensures:
    - Lower priority number processed first
    - FIFO within same priority (earlier timestamp wins)
    - Unique request_id breaks any remaining ties
    """

    priority: int
    timestamp: float = field(compare=True)
    request_id: str = field(compare=True)

    # Non-comparison fields
    key_identifier: str = field(compare=False)
    ready_event: asyncio.Event = field(compare=False, default_factory=asyncio.Event)
    cancelled: bool = field(compare=False, default=False)


@dataclass
class KeyConcurrencyState:
    """Tracks concurrency state for a single key or the unauthenticated pool."""

    key_identifier: str
    concurrency_limit: int
    priority: int

    # Active request tracking
    active_count: int = 0
    active_request_ids: set[str] = field(default_factory=set)

    # Statistics
    total_requests: int = 0
    total_queued: int = 0
    total_wait_time_ms: float = 0.0
    max_queue_depth: int = 0


@dataclass
class ConcurrencyMetrics:
    """Snapshot of concurrency metrics for observability."""

    timestamp: str
    global_queue_depth: int
    active_requests_by_key: dict[str, int]
    queued_requests_by_key: dict[str, int]
    average_wait_time_ms_by_key: dict[str, float]
    key_states: dict[str, dict[str, Any]]


class ConcurrencyManager:
    """Manages per-key concurrency limits with priority-based queuing.

    Thread Safety:
    - All state mutations happen within asyncio.Lock
    - Event-based signaling for queue wakeups
    - Safe for concurrent access from multiple async tasks

    Design Principles:
    - Each key has its own concurrency limit
    - When at limit, new requests queue
    - Queue is globally ordered by (priority, timestamp)
    - Released slots wake the highest-priority waiting request for that key
    """

    # Sentinel for unauthenticated requests
    UNAUTHENTICATED_KEY = "__unauthenticated__"

    def __init__(self) -> None:
        self._lock = asyncio.Lock()

        # Per-key state tracking
        self._key_states: dict[str, KeyConcurrencyState] = {}

        # Global priority queue for all waiting requests
        # Heap invariant: min-heap ordered by (priority, timestamp, request_id)
        self._wait_queue: list[QueuedRequest] = []

        # Quick lookup: request_id -> QueuedRequest
        self._pending_requests: dict[str, QueuedRequest] = {}

    def _get_or_create_key_state(
        self,
        key_identifier: str,
        concurrency_limit: int,
        priority: int,
    ) -> KeyConcurrencyState:
        """Get existing key state or create new one.

        Must be called while holding self._lock.
        """
        if key_identifier not in self._key_states:
            self._key_states[key_identifier] = KeyConcurrencyState(
                key_identifier=key_identifier,
                concurrency_limit=concurrency_limit,
                priority=priority,
            )
        else:
            # Update limits if config changed (hot reload support)
            state = self._key_states[key_identifier]
            state.concurrency_limit = concurrency_limit
            state.priority = priority
        return self._key_states[key_identifier]

    def _has_available_slot(self, state: KeyConcurrencyState) -> bool:
        """Check if key has available concurrency slot.

        Returns True if:
        - No limit configured (limit <= 0)
        - Active count is below limit
        """
        if state.concurrency_limit <= 0:
            return True  # No limit
        return state.active_count < state.concurrency_limit

    async def acquire(
        self,
        key_identifier: str | None,
        concurrency_limit: int,
        priority: int,
        timeout: float | None = None,
        disconnect_checker: Callable[[], bool] | None = None,
    ) -> ConcurrencySlot:
        """Acquire a concurrency slot for a request.

        Args:
            key_identifier: The app key ID, or None for unauthenticated
            concurrency_limit: Max concurrent requests for this key
            priority: Queue priority (lower = higher priority)
            timeout: Max seconds to wait in queue (None = wait forever)
            disconnect_checker: Optional callable returning True if client disconnected

        Returns:
            ConcurrencySlot context manager

        Raises:
            ConcurrencyQueueTimeout: If timeout exceeded
            ConcurrencyClientDisconnected: If client disconnected while waiting
        """
        effective_key = key_identifier or self.UNAUTHENTICATED_KEY
        request_id = uuid.uuid4().hex
        enqueue_time = time.monotonic()

        async with self._lock:
            state = self._get_or_create_key_state(
                effective_key, concurrency_limit, priority
            )
            state.total_requests += 1

            # Fast path: slot available immediately
            if self._has_available_slot(state):
                state.active_count += 1
                state.active_request_ids.add(request_id)
                if logger.isEnabledFor(logging.DEBUG):
                    logger.debug(
                        "Concurrency slot acquired immediately: key=%s, active=%d/%d",
                        effective_key,
                        state.active_count,
                        state.concurrency_limit,
                    )
                return ConcurrencySlot(
                    manager=self,
                    key_identifier=effective_key,
                    request_id=request_id,
                    wait_time_ms=0.0,
                )

            # Slow path: need to queue
            queued = QueuedRequest(
                priority=priority,
                timestamp=enqueue_time,
                request_id=request_id,
                key_identifier=effective_key,
            )
            heapq.heappush(self._wait_queue, queued)
            self._pending_requests[request_id] = queued
            state.total_queued += 1

            current_queue_depth = sum(
                1
                for q in self._wait_queue
                if q.key_identifier == effective_key and not q.cancelled
            )
            state.max_queue_depth = max(state.max_queue_depth, current_queue_depth)

            logger.info(
                "Request queued: key=%s, priority=%d, queue_depth=%d, limit=%d",
                effective_key,
                priority,
                current_queue_depth,
                state.concurrency_limit,
            )

        # Wait outside lock for the slot to become available
        try:
            await self._wait_for_slot(queued, timeout, disconnect_checker)
            wait_time_ms = (time.monotonic() - enqueue_time) * 1000

            async with self._lock:
                state = self._key_states.get(effective_key)
                if state:
                    state.total_wait_time_ms += wait_time_ms

            logger.info(
                "Request dequeued after %.1fms wait: key=%s, priority=%d",
                wait_time_ms,
                effective_key,
                priority,
            )

            return ConcurrencySlot(
                manager=self,
                key_identifier=effective_key,
                request_id=request_id,
                wait_time_ms=wait_time_ms,
            )
        except Exception:
            # Clean up on any failure
            await self._cancel_queued_request(request_id)
            raise

    async def _wait_for_slot(
        self,
        queued: QueuedRequest,
        timeout: float | None,
        disconnect_checker: Callable[[], bool] | None,
    ) -> None:
        """Wait for a concurrency slot to become available.

        Args:
            queued: The queued request to wait for
            timeout: Max seconds to wait
            disconnect_checker: Callable to check client disconnect

        Raises:
            ConcurrencyQueueTimeout: If timeout exceeded
            ConcurrencyClientDisconnected: If client disconnected
        """
        deadline = time.monotonic() + timeout if timeout else None

        while True:
            # Calculate remaining wait time
            if deadline:
                remaining = deadline - time.monotonic()
                if remaining <= 0:
                    raise ConcurrencyQueueTimeout(
                        f"Timeout waiting for concurrency slot: key={queued.key_identifier}"
                    )
                wait_timeout = min(remaining, 0.5)  # Check disconnect every 500ms
            else:
                wait_timeout = 0.5

            # Check for client disconnect
            if disconnect_checker and disconnect_checker():
                raise ConcurrencyClientDisconnected(
                    f"Client disconnected while waiting: key={queued.key_identifier}"
                )

            # Wait for ready signal
            try:
                await asyncio.wait_for(
                    queued.ready_event.wait(),
                    timeout=wait_timeout,
                )
                return
            except asyncio.TimeoutError:
                # Continue loop to check disconnect and timeout
                continue

    async def release(self, key_identifier: str, request_id: str) -> None:
        """Release a concurrency slot and wake next queued request.

        Args:
            key_identifier: The key that held the slot
            request_id: The request ID releasing the slot
        """
        async with self._lock:
            state = self._key_states.get(key_identifier)
            if not state:
                logger.warning("Release called for unknown key: %s", key_identifier)
                return

            if request_id not in state.active_request_ids:
                # This can happen if release is called multiple times (idempotent)
                if logger.isEnabledFor(logging.DEBUG):
                    logger.debug(
                        "Release called for unknown/already-released request: key=%s, request_id=%s",
                        key_identifier,
                        request_id,
                    )
                return

            state.active_request_ids.discard(request_id)
            state.active_count = len(state.active_request_ids)

            if logger.isEnabledFor(logging.DEBUG):
                logger.debug(
                    "Concurrency slot released: key=%s, active=%d/%d",
                    key_identifier,
                    state.active_count,
                    state.concurrency_limit,
                )

            # Find next queued request for this key and signal it
            self._signal_next_queued_request(key_identifier, state)

    def _signal_next_queued_request(
        self,
        key_identifier: str,
        state: KeyConcurrencyState,
    ) -> None:
        """Find and signal the next queued request for a key.

        Must be called while holding self._lock.
        Uses heap property to find highest priority waiting request.
        """
        if not self._has_available_slot(state):
            return

        # Scan heap for first non-cancelled request matching this key
        # Note: We don't remove from heap here to maintain heap invariant
        # Cancelled requests are cleaned up lazily
        for queued in self._wait_queue:
            if queued.cancelled:
                continue
            if queued.key_identifier != key_identifier:
                continue
            if queued.ready_event.is_set():
                continue

            # Found a waiting request - activate it
            state.active_count += 1
            state.active_request_ids.add(queued.request_id)
            queued.ready_event.set()
            self._pending_requests.pop(queued.request_id, None)

            if logger.isEnabledFor(logging.DEBUG):
                logger.debug(
                    "Signaled queued request: key=%s, request_id=%s, active=%d/%d",
                    key_identifier,
                    queued.request_id,
                    state.active_count,
                    state.concurrency_limit,
                )
            return

    async def _cancel_queued_request(self, request_id: str) -> None:
        """Mark a queued request as cancelled."""
        async with self._lock:
            queued = self._pending_requests.pop(request_id, None)
            if queued:
                queued.cancelled = True
                if logger.isEnabledFor(logging.DEBUG):
                    logger.debug("Cancelled queued request: %s", request_id)

    def _cleanup_cancelled_requests(self) -> None:
        """Remove cancelled requests from the queue.

        Must be called while holding self._lock.
        Called periodically to prevent queue growth.
        """
        # Rebuild heap without cancelled requests
        new_queue = [q for q in self._wait_queue if not q.cancelled]
        if len(new_queue) < len(self._wait_queue):
            heapq.heapify(new_queue)
            self._wait_queue = new_queue

    async def get_metrics(self) -> ConcurrencyMetrics:
        """Get current concurrency metrics snapshot."""
        async with self._lock:
            # Cleanup stale cancelled requests periodically
            if len(self._wait_queue) > 100:
                self._cleanup_cancelled_requests()

            active_by_key: dict[str, int] = {}
            queued_by_key: dict[str, int] = {}
            avg_wait_by_key: dict[str, float] = {}
            key_states_snapshot: dict[str, dict[str, Any]] = {}

            for key, state in self._key_states.items():
                active_by_key[key] = state.active_count
                queued_by_key[key] = sum(
                    1
                    for q in self._wait_queue
                    if q.key_identifier == key and not q.cancelled
                )
                if state.total_queued > 0:
                    avg_wait_by_key[key] = state.total_wait_time_ms / state.total_queued
                else:
                    avg_wait_by_key[key] = 0.0

                key_states_snapshot[key] = {
                    "concurrency_limit": state.concurrency_limit,
                    "priority": state.priority,
                    "active_count": state.active_count,
                    "total_requests": state.total_requests,
                    "total_queued": state.total_queued,
                    "max_queue_depth": state.max_queue_depth,
                    "avg_wait_time_ms": avg_wait_by_key[key],
                }

            global_queue_depth = sum(
                1 for q in self._wait_queue if not q.cancelled
            )

            return ConcurrencyMetrics(
                timestamp=datetime.now(timezone.utc).isoformat(),
                global_queue_depth=global_queue_depth,
                active_requests_by_key=active_by_key,
                queued_requests_by_key=queued_by_key,
                average_wait_time_ms_by_key=avg_wait_by_key,
                key_states=key_states_snapshot,
            )

    async def reset_stats(self) -> None:
        """Reset all statistics (for testing)."""
        async with self._lock:
            for state in self._key_states.values():
                state.total_requests = 0
                state.total_queued = 0
                state.total_wait_time_ms = 0.0
                state.max_queue_depth = 0
