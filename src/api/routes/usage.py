"""Usage endpoints for realtime counters and historical placeholders."""

from pathlib import Path
from typing import Any

from fastapi import APIRouter, HTTPException
from fastapi.responses import FileResponse

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
