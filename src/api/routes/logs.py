"""API endpoints for browsing and querying request logs.

Provides endpoints for paginated log access, filtering, and analytics.
"""

import logging
from datetime import datetime, timedelta, timezone
from typing import Optional
from uuid import UUID

from fastapi import HTTPException, Query, Request
from fastapi.routing import APIRouter
from fastapi.responses import FileResponse, JSONResponse

from ...database.logs_repository import BODY_MAX_CHARS_DEFAULT, get_logs_repository
from ...database.repository import get_usage_repository

logger = logging.getLogger("yallmp-proxy")

router = APIRouter()


async def logs_page(request: Request) -> FileResponse:
    """Serve the logs viewer page."""
    from pathlib import Path
    # Go up from src/api/routes/ to project root, then to static/admin/
    project_root = Path(__file__).parent.parent.parent.parent
    return FileResponse(project_root / "static" / "admin" / "logs.html")


@router.get("/logs")
async def get_logs(
    model: Optional[str] = Query(None, description="Filter by model name (partial match)"),
    outcome: Optional[str] = Query(None, description="Filter by outcome: success, error, cancelled"),
    stop_reason: Optional[str] = Query(None, description="Filter by stop reason: stop, tool_calls, length, content_filter"),
    is_tool_call: Optional[bool] = Query(None, description="Filter by whether tool calls were made"),
    start_date: Optional[datetime] = Query(None, description="Start of time range"),
    end_date: Optional[datetime] = Query(None, description="End of time range"),
    search: Optional[str] = Query(None, description="Full-text search in request/response body"),
    limit: int = Query(50, ge=1, le=200, description="Maximum number of logs to return"),
    offset: int = Query(0, ge=0, description="Number of logs to skip"),
) -> JSONResponse:
    """Get paginated request logs with optional filters.

    Returns a paginated list of request logs with metadata including:
    - Request time, model name, outcome
    - Stop reason and tool call indicators
    - Duration and usage statistics
    """
    try:
        repository = get_logs_repository()
        result = repository.get_logs(
            limit=limit,
            offset=offset,
            model_name=model,
            outcome=outcome,
            stop_reason=stop_reason,
            is_tool_call=is_tool_call,
            start_time=start_date,
            end_time=end_date,
            search=search,
        )
        return JSONResponse(content=result)
    except Exception as e:
        logger.error(f"Failed to get logs: {e}")
        raise HTTPException(status_code=500, detail=str(e))


# Analytics endpoints - define these BEFORE the generic {log_id} route
@router.get("/stop-reasons")
async def get_stop_reasons(
    start_date: Optional[datetime] = Query(None, description="Start of time range"),
    end_date: Optional[datetime] = Query(None, description="End of time range"),
) -> JSONResponse:
    """Get statistics on stop reasons from logged requests.

    Returns a breakdown of stop reasons with counts and percentages:
    - stop: Normal completion
    - tool_calls: Model requested tool/function calls
    - length: Hit max tokens limit
    - content_filter: Content was filtered
    - function_call: Legacy tool call format
    """
    try:
        repository = get_logs_repository()
        stop_reasons = repository.get_stop_reason_counts(
            start_time=start_date,
            end_time=end_date,
        )

        return JSONResponse(content={
            "stop_reasons": stop_reasons,
            "time_range": {
                "start": (start_date or datetime.now(timezone.utc) - timedelta(days=1)).isoformat(),
                "end": (end_date or datetime.now(timezone.utc)).isoformat(),
            }
        })
    except Exception as e:
        logger.error(f"Failed to get stop reasons: {e}")
        raise HTTPException(status_code=500, detail=str(e))


@router.get("/tool-call-rate")
async def get_tool_call_rate(
    start_date: Optional[datetime] = Query(None, description="Start of time range"),
    end_date: Optional[datetime] = Query(None, description="End of time range"),
) -> JSONResponse:
    """Get the percentage of requests that resulted in tool calls.

    Returns statistics on tool call usage:
    - Total number of requests
    - Number of requests with tool calls
    - Percentage of tool call requests
    """
    try:
        repository = get_logs_repository()
        rate_stats = repository.get_tool_call_rate(
            start_time=start_date,
            end_time=end_date,
        )

        return JSONResponse(content=rate_stats)
    except Exception as e:
        logger.error(f"Failed to get tool call rate: {e}")
        raise HTTPException(status_code=500, detail=str(e))


@router.get("/models")
async def get_models_with_stop_reasons(
    start_date: Optional[datetime] = Query(None, description="Start of time range"),
    end_date: Optional[datetime] = Query(None, description="End of time range"),
    limit: int = Query(50, ge=1, le=100, description="Maximum number of models to return"),
) -> JSONResponse:
    """Get request counts per model with stop reason breakdown.

    Returns a list of models with their request counts and
    detailed stop reason distributions.
    """
    try:
        repository = get_logs_repository()
        models = repository.get_requests_per_model_with_stop_reason(
            start_time=start_date,
            end_time=end_date,
            limit=limit,
        )

        return JSONResponse(content={
            "models": models,
            "time_range": {
                "start": (start_date or datetime.now(timezone.utc) - timedelta(days=1)).isoformat(),
                "end": (end_date or datetime.now(timezone.utc)).isoformat(),
            }
        })
    except Exception as e:
        logger.error(f"Failed to get models with stop reasons: {e}")
        raise HTTPException(status_code=500, detail=str(e))


# Generic log ID endpoint - MUST be last to avoid catching specific routes
@router.get("/logs/{log_id}")
async def get_log_by_id(
    log_id: str,
    body_max_chars: int = Query(
        BODY_MAX_CHARS_DEFAULT,
        ge=0,
        le=1_000_000,
        description="Maximum size of the request body to include (0 disables truncation).",
    ),
) -> JSONResponse:
    """Get a single request log by ID.

    Returns full details of a request log including:
    - Complete request body and response
    - All stream chunks (for streaming requests)
    - Usage statistics
    - Linked error logs (if any)
    """
    try:
        # Validate UUID format
        try:
            uuid_id = UUID(log_id)
        except ValueError:
            raise HTTPException(status_code=400, detail="Invalid log ID format")

        repository = get_logs_repository()
        log = repository.get_log_by_id(uuid_id, body_max_chars=body_max_chars)

        if log is None:
            raise HTTPException(status_code=404, detail="Log not found")

        return JSONResponse(content=log)
    except HTTPException:
        raise
    except Exception as e:
        logger.error(f"Failed to get log by ID: {e}")
        raise HTTPException(status_code=500, detail=str(e))
