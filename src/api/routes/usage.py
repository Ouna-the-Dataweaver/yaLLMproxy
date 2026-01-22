"""Usage endpoints for realtime counters and historical placeholders."""

from pathlib import Path
from typing import Any

from fastapi import APIRouter, HTTPException
from fastapi.responses import FileResponse

from ...concurrency import get_concurrency_manager
from ...usage_metrics import build_usage_snapshot

router = APIRouter(prefix="/api/usage", tags=["usage"])


@router.get("")
async def get_usage() -> dict[str, Any]:
    """Return realtime usage counters plus a historical placeholder."""
    return build_usage_snapshot()


@router.get("/page")
async def usage_page() -> FileResponse:
    """Serve the usage page HTML."""
    static_dir = Path(__file__).parent.parent.parent.parent / "static" / "admin"
    index_path = static_dir / "usage.html"

    if not index_path.exists():
        raise HTTPException(
            status_code=404,
            detail="Usage UI not found. Please ensure static/admin/usage.html exists.",
        )

    return FileResponse(index_path)


@router.get("/concurrency")
async def get_concurrency() -> dict[str, Any]:
    """Return current concurrency metrics.

    Returns:
        {
            "timestamp": "ISO timestamp",
            "global_queue_depth": int,
            "by_key": {
                "key_id": {
                    "active": int,
                    "queued": int,
                    "concurrency_limit": int,
                    "priority": int,
                    "total_requests": int,
                    "total_queued": int,
                    "max_queue_depth": int,
                    "avg_wait_time_ms": float
                }
            }
        }
    """
    manager = get_concurrency_manager()
    metrics = await manager.get_metrics()

    return {
        "timestamp": metrics.timestamp,
        "global_queue_depth": metrics.global_queue_depth,
        "by_key": {
            key: {
                "active": metrics.active_requests_by_key.get(key, 0),
                "queued": metrics.queued_requests_by_key.get(key, 0),
                **state,
            }
            for key, state in metrics.key_states.items()
        },
    }
