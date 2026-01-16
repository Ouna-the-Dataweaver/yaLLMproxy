"""State store for Open Responses API.

Manages response history for `previous_response_id` lookups and `store` functionality.
Uses two-tier storage: in-memory LRU cache for performance + database persistence
for durability.
"""

import asyncio
import logging
from collections import OrderedDict
from typing import Any, Optional

from ..types.responses import ResponseObject, InputItem

logger = logging.getLogger("yallmp-proxy")

# Singleton instance
_state_store: Optional["ResponseStateStore"] = None


def get_state_store() -> "ResponseStateStore":
    """Get the global state store instance."""
    global _state_store
    if _state_store is None:
        _state_store = ResponseStateStore()
    return _state_store


def reset_state_store() -> None:
    """Reset the global state store (for testing)."""
    global _state_store
    _state_store = None


class ResponseStateStore:
    """Manages response history for previous_response_id lookups.

    Uses two-tier storage:
    1. In-memory LRU cache (fast, limited size)
    2. Database persistence (durable, unlimited)

    Read path: Check memory → fallback to DB → cache in memory
    Write path: Write to both memory and DB
    """

    MAX_MEMORY_ENTRIES = 1000  # Configurable

    def __init__(self, db_config: Optional[dict] = None, max_entries: int = 1000):
        """Initialize the state store.

        Args:
            db_config: Optional database configuration. If None, only memory storage is used.
            max_entries: Maximum entries in the memory cache.
        """
        self._memory_cache: OrderedDict[str, dict[str, Any]] = OrderedDict()
        self._db = None
        self._db_config = db_config
        self.MAX_MEMORY_ENTRIES = max_entries

        # Try to initialize database if config provided
        if db_config:
            self._init_database(db_config)

    def _init_database(self, db_config: dict) -> None:
        """Initialize database connection."""
        try:
            from ..database.factory import get_database
            self._db = get_database(db_config)
            logger.info("ResponseStateStore: Database storage enabled")
        except Exception as e:
            logger.warning(f"ResponseStateStore: Database init failed: {e}, using memory only")
            self._db = None

    async def store_response(
        self,
        response: ResponseObject,
        original_input: Any = None,
    ) -> str:
        """Store a response in both memory and database.

        Args:
            response: The response object to store.
            original_input: The original input (for conversation reconstruction).

        Returns:
            The response ID.
        """
        response_id = response.get("id", "")
        if not response_id:
            logger.warning("ResponseStateStore: Cannot store response without ID")
            return ""

        # Build storage record
        record = {
            "id": response_id,
            "previous_response_id": response.get("previous_response_id"),
            "model": response.get("model"),
            "status": response.get("status"),
            "input_data": original_input,
            "output_data": response.get("output", []),
            "usage": response.get("usage"),
            "created_at": response.get("created_at"),
            "metadata": response.get("metadata"),
            # Store full response for retrieval
            "_full_response": response,
        }

        # Write to memory (with LRU eviction)
        self._memory_cache[response_id] = record
        self._memory_cache.move_to_end(response_id)
        if len(self._memory_cache) > self.MAX_MEMORY_ENTRIES:
            evicted_id, _ = self._memory_cache.popitem(last=False)
            logger.debug(f"ResponseStateStore: Evicted {evicted_id} from memory cache")

        logger.debug(f"ResponseStateStore: Stored response {response_id} in memory")

        # Write to database (async, non-blocking)
        if self._db:
            asyncio.create_task(self._db_store(record))

        return response_id

    async def _db_store(self, record: dict) -> None:
        """Store record in database."""
        try:
            # TODO: Implement database storage when response_state model is added
            logger.debug(f"ResponseStateStore: DB store for {record.get('id')} (not implemented)")
        except Exception as e:
            logger.error(f"ResponseStateStore: DB store failed: {e}")

    async def get_response(self, response_id: str) -> Optional[ResponseObject]:
        """Retrieve a stored response by ID.

        Args:
            response_id: The response ID to look up.

        Returns:
            The response object, or None if not found.
        """
        if not response_id:
            return None

        # Check memory cache
        if response_id in self._memory_cache:
            self._memory_cache.move_to_end(response_id)  # LRU update
            record = self._memory_cache[response_id]
            logger.debug(f"ResponseStateStore: Found {response_id} in memory cache")
            return record.get("_full_response")

        # Fallback to database
        if self._db:
            record = await self._db_get(response_id)
            if record:
                # Populate memory cache for future reads
                self._memory_cache[response_id] = record
                logger.debug(f"ResponseStateStore: Found {response_id} in database, cached")
                return record.get("_full_response")

        logger.debug(f"ResponseStateStore: Response {response_id} not found")
        return None

    async def _db_get(self, response_id: str) -> Optional[dict]:
        """Get record from database."""
        try:
            # TODO: Implement database retrieval when response_state model is added
            logger.debug(f"ResponseStateStore: DB get for {response_id} (not implemented)")
            return None
        except Exception as e:
            logger.error(f"ResponseStateStore: DB get failed: {e}")
            return None

    async def get_stored_record(self, response_id: str) -> Optional[dict]:
        """Get the full stored record (not just the response).

        Args:
            response_id: The response ID to look up.

        Returns:
            The full storage record, or None if not found.
        """
        if not response_id:
            return None

        # Check memory cache
        if response_id in self._memory_cache:
            self._memory_cache.move_to_end(response_id)
            return self._memory_cache[response_id]

        # Fallback to database
        if self._db:
            record = await self._db_get(response_id)
            if record:
                self._memory_cache[response_id] = record
                return record

        return None

    async def get_conversation_history(
        self,
        response_id: str,
        max_depth: int = 100,
    ) -> list[InputItem]:
        """Walk the previous_response_id chain to build full conversation history.

        Reconstructs the conversation by following the chain of previous_response_id
        references. This is used when a new request includes previous_response_id
        to continue a conversation.

        Args:
            response_id: The response ID to start from (most recent).
            max_depth: Maximum number of responses to traverse.

        Returns:
            List of input items in chronological order (oldest first).
        """
        history: list[Any] = []
        current_id = response_id
        depth = 0

        while current_id and depth < max_depth:
            record = await self.get_stored_record(current_id)
            if not record:
                logger.warning(
                    f"ResponseStateStore: Broken chain at {current_id}, "
                    f"depth {depth}"
                )
                break

            # Get input and output from this turn
            input_data = record.get("input_data")
            output_data = record.get("output_data", [])

            # Build turn items
            turn_items: list[Any] = []

            # Add input items
            if input_data:
                if isinstance(input_data, str):
                    # Simple string input -> convert to message item
                    turn_items.append({
                        "type": "message",
                        "role": "user",
                        "content": [{"type": "input_text", "text": input_data}],
                    })
                elif isinstance(input_data, list):
                    # Already in item format
                    turn_items.extend(input_data)

            # Add output items
            if output_data:
                turn_items.extend(output_data)

            # Prepend to build chronological order
            history = turn_items + history

            # Move to previous response
            current_id = record.get("previous_response_id")
            depth += 1

        if depth >= max_depth:
            logger.warning(
                f"ResponseStateStore: Hit max depth {max_depth} while traversing chain"
            )

        logger.debug(
            f"ResponseStateStore: Built history with {len(history)} items "
            f"from {depth} responses"
        )
        return history

    def get_cache_stats(self) -> dict:
        """Get statistics about the memory cache.

        Returns:
            Dictionary with cache statistics.
        """
        return {
            "memory_entries": len(self._memory_cache),
            "max_entries": self.MAX_MEMORY_ENTRIES,
            "db_enabled": self._db is not None,
        }

    def clear_cache(self) -> None:
        """Clear the memory cache."""
        self._memory_cache.clear()
        logger.info("ResponseStateStore: Memory cache cleared")
