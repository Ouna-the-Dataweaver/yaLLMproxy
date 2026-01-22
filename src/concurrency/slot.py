"""Concurrency slot context manager for RAII-style slot management."""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import TYPE_CHECKING

if TYPE_CHECKING:
    from .manager import ConcurrencyManager


@dataclass
class ConcurrencySlot:
    """RAII-style slot holder that ensures release on exit.

    Usage:
        slot = await manager.acquire(key_id, limit, priority)
        async with slot:
            # Process request, including streaming
            pass
        # Slot automatically released
    """

    manager: "ConcurrencyManager"
    key_identifier: str
    request_id: str
    wait_time_ms: float
    _released: bool = field(default=False, init=False, repr=False)

    async def __aenter__(self) -> "ConcurrencySlot":
        return self

    async def __aexit__(self, exc_type, exc_val, exc_tb) -> None:
        await self.release()
        return None

    async def release(self) -> None:
        """Explicitly release the slot (idempotent)."""
        if not self._released:
            self._released = True
            await self.manager.release(self.key_identifier, self.request_id)

    @property
    def is_released(self) -> bool:
        """Check if the slot has been released."""
        return self._released
