"""Tests for the concurrency module."""

import asyncio
import sys
from pathlib import Path

import pytest

# Add src directory to path
sys.path.insert(0, str(Path(__file__).parent.parent / "src"))

from src.concurrency import (
    ConcurrencyClientDisconnected,
    ConcurrencyQueueTimeout,
    ConcurrencySlot,
    get_concurrency_manager,
    reset_concurrency_manager,
)
from src.concurrency.manager import ConcurrencyManager


@pytest.fixture
def manager():
    """Create a fresh ConcurrencyManager for each test."""
    reset_concurrency_manager()
    return ConcurrencyManager()


class TestConcurrencySlot:
    """Tests for ConcurrencySlot."""

    @pytest.mark.asyncio
    async def test_slot_context_manager(self, manager):
        """Test that slot releases when used as context manager."""
        slot = await manager.acquire(
            key_identifier="test-key",
            concurrency_limit=10,
            priority=100,
        )
        assert not slot.is_released

        async with slot:
            assert not slot.is_released

        assert slot.is_released

    @pytest.mark.asyncio
    async def test_slot_release_is_idempotent(self, manager):
        """Test that calling release multiple times is safe."""
        slot = await manager.acquire(
            key_identifier="test-key",
            concurrency_limit=10,
            priority=100,
        )

        await slot.release()
        assert slot.is_released

        # Second release should not raise
        await slot.release()
        assert slot.is_released


class TestConcurrencyManager:
    """Tests for ConcurrencyManager."""

    @pytest.mark.asyncio
    async def test_immediate_acquisition_under_limit(self, manager):
        """Test that slots are acquired immediately when under limit."""
        slot = await manager.acquire(
            key_identifier="test-key",
            concurrency_limit=5,
            priority=100,
        )
        assert slot.wait_time_ms == 0.0
        assert slot.key_identifier == "test-key"
        await slot.release()

    @pytest.mark.asyncio
    async def test_no_limit_means_unlimited(self, manager):
        """Test that limit=0 means no limit."""
        slots = []
        for _ in range(100):
            slot = await manager.acquire(
                key_identifier="test-key",
                concurrency_limit=0,  # No limit
                priority=100,
            )
            slots.append(slot)
            assert slot.wait_time_ms == 0.0

        for slot in slots:
            await slot.release()

    @pytest.mark.asyncio
    async def test_queue_when_at_limit(self, manager):
        """Test that requests queue when at concurrency limit."""
        # Acquire 2 slots, using limit of 2
        slot1 = await manager.acquire("key", 2, 100)
        slot2 = await manager.acquire("key", 2, 100)

        # Third should queue, so use a short timeout
        with pytest.raises(ConcurrencyQueueTimeout):
            await manager.acquire("key", 2, 100, timeout=0.1)

        await slot1.release()
        await slot2.release()

    @pytest.mark.asyncio
    async def test_priority_ordering(self, manager):
        """Test that higher priority requests are processed first."""
        results = []

        # Fill up the limit
        slot1 = await manager.acquire("key", 1, 100)

        # Queue two requests with different priorities
        async def acquire_and_record(priority, name):
            slot = await manager.acquire("key", 1, priority)
            results.append(name)
            await slot.release()

        # Start both tasks (they will queue)
        task_low = asyncio.create_task(acquire_and_record(500, "low"))
        task_high = asyncio.create_task(acquire_and_record(10, "high"))

        # Give them time to queue
        await asyncio.sleep(0.05)

        # Release the slot - high priority should be signaled first
        await slot1.release()

        # Wait for both to complete
        await task_high
        await task_low

        # High priority should have been processed first
        assert results[0] == "high"

    @pytest.mark.asyncio
    async def test_timeout(self, manager):
        """Test that timeout raises ConcurrencyQueueTimeout."""
        # Fill up the limit
        slot = await manager.acquire("key", 1, 100)

        # Try to acquire with short timeout
        with pytest.raises(ConcurrencyQueueTimeout):
            await manager.acquire("key", 1, 100, timeout=0.1)

        await slot.release()

    @pytest.mark.asyncio
    async def test_client_disconnect(self, manager):
        """Test that disconnect checker raises ConcurrencyClientDisconnected."""
        # Fill up the limit
        slot = await manager.acquire("key", 1, 100)

        # Try to acquire with disconnect checker that returns True
        with pytest.raises(ConcurrencyClientDisconnected):
            await manager.acquire(
                "key", 1, 100,
                disconnect_checker=lambda: True,
            )

        await slot.release()

    @pytest.mark.asyncio
    async def test_unauthenticated_key(self, manager):
        """Test that None key_identifier uses unauthenticated pool."""
        slot = await manager.acquire(
            key_identifier=None,
            concurrency_limit=5,
            priority=1000,
        )
        assert slot.key_identifier == ConcurrencyManager.UNAUTHENTICATED_KEY
        await slot.release()

    @pytest.mark.asyncio
    async def test_separate_key_limits(self, manager):
        """Test that different keys have separate limits."""
        # Fill up key1
        key1_slots = []
        for _ in range(2):
            slot = await manager.acquire("key1", 2, 100)
            key1_slots.append(slot)

        # key2 should still be able to acquire
        key2_slot = await manager.acquire("key2", 2, 100)
        assert key2_slot.wait_time_ms == 0.0

        for slot in key1_slots:
            await slot.release()
        await key2_slot.release()

    @pytest.mark.asyncio
    async def test_metrics(self, manager):
        """Test that metrics are reported correctly."""
        slot = await manager.acquire("test-key", 5, 100)

        metrics = await manager.get_metrics()
        assert "test-key" in metrics.key_states
        assert metrics.active_requests_by_key.get("test-key") == 1
        assert metrics.key_states["test-key"]["total_requests"] == 1

        await slot.release()

        metrics = await manager.get_metrics()
        assert metrics.active_requests_by_key.get("test-key") == 0


class TestSingleton:
    """Tests for singleton pattern."""

    def test_get_manager_returns_same_instance(self):
        """Test that get_concurrency_manager returns the same instance."""
        reset_concurrency_manager()
        manager1 = get_concurrency_manager()
        manager2 = get_concurrency_manager()
        assert manager1 is manager2

    def test_reset_clears_singleton(self):
        """Test that reset_concurrency_manager clears the singleton."""
        reset_concurrency_manager()
        manager1 = get_concurrency_manager()
        reset_concurrency_manager()
        manager2 = get_concurrency_manager()
        assert manager1 is not manager2
