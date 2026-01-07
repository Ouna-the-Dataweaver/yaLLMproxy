"""Database logger for request and error logging.

Provides async database logging capabilities that integrate with the existing
file-based logging system.
"""

import asyncio
import logging
import uuid
from datetime import datetime, timezone
from typing import Any, Optional

from ..database.factory import get_database
from ..database.models import RequestLog, ErrorLog

logger = logging.getLogger("yallmp-proxy")

# Background tasks registry for cleanup
_PENDING_DB_TASKS: set[asyncio.Task] = set()


def _register_background_task(task: asyncio.Task) -> None:
    """Register a background task and set up cleanup."""
    _PENDING_DB_TASKS.add(task)

    def _cleanup(_task: asyncio.Task) -> None:
        _PENDING_DB_TASKS.discard(_task)

    task.add_done_callback(_cleanup)


class DatabaseLogRecorder:
    """Async database logger for request and error events.

    This class provides methods to log request and error events to the database
    asynchronously to avoid blocking the main request processing.
    """

    def __init__(self) -> None:
        """Initialize the database logger."""
        self._database = get_database()
        self._initialized = False

    def _ensure_initialized(self) -> None:
        """Ensure the database is initialized."""
        if self._initialized:
            return
        try:
            self._database.initialize()
            self._initialized = True
        except Exception as e:
            logger.error(f"Failed to initialize database logger: {e}")
            self._initialized = False

    def log_request(
        self,
        model_name: str,
        is_stream: bool,
        path: str,
        method: str,
        query: str,
        headers: dict[str, Any],
        body: dict[str, Any],
        route: list[str] | None = None,
        backend_attempts: list[dict[str, Any]] | None = None,
        stream_chunks: list[dict[str, Any]] | None = None,
        errors: list[dict[str, Any]] | None = None,
        usage_stats: dict[str, Any] | None = None,
        outcome: str | None = None,
        duration_ms: int | None = None,
        request_time: datetime | None = None,
    ) -> str:
        """Log a request event to the database.

        Args:
            model_name: The model name used for the request.
            is_stream: Whether the request was streaming.
            path: Request path.
            method: HTTP method.
            query: Query string.
            headers: Request headers.
            body: Request body.
            route: Routing information.
            backend_attempts: Backend attempts with responses.
            stream_chunks: Stream chunks data.
            errors: Error information.
            usage_stats: Usage statistics.
            outcome: Request outcome.
            duration_ms: Request duration in milliseconds.
            request_time: Request timestamp.

        Returns:
            The UUID of the created request log.
        """
        request_uuid = uuid.uuid4()
        self._ensure_initialized()
        if not self._initialized:
            logger.warning("Database not initialized, skipping database logging")
            return str(request_uuid)

        if request_time is None:
            request_time = datetime.now(timezone.utc)

        request_log = RequestLog(
            id=request_uuid,
            request_time=request_time,
            model_name=model_name,
            is_stream=is_stream,
            path=path,
            method=method,
            query=query or "",
            headers=headers,
            body=body,
            route=route,
            backend_attempts=backend_attempts,
            stream_chunks=stream_chunks,
            errors=errors,
            usage_stats=usage_stats,
            outcome=outcome,
            duration_ms=duration_ms,
        )

        # Capture the ID before starting async task
        request_id = str(request_uuid)

        try:
            loop = asyncio.get_running_loop()

            async def _save_request() -> None:
                def _save() -> None:
                    try:
                        with self._database.session() as sess:
                            sess.add(request_log)
                            sess.flush()  # Ensure it's persisted
                            sess.expunge(request_log)  # Detach from session
                    except Exception as e:
                        logger.error(f"Failed to save request log to database: {e}")

                await asyncio.to_thread(_save)

            task = loop.create_task(_save_request())
            _register_background_task(task)
            logger.debug(f"Scheduled async save for request log: {request_id}")

        except RuntimeError:
            # No event loop, save synchronously
            try:
                with self._database.session() as sess:
                    sess.add(request_log)
                    sess.flush()
                logger.debug(f"Saved request log synchronously: {request_id}")
            except Exception as e:
                logger.error(f"Failed to save request log to database: {e}")

        return request_id

    def log_error(
        self,
        model_name: str,
        error_type: str,
        error_message: str,
        backend_name: str | None = None,
        http_status: int | None = None,
        request_path: str | None = None,
        request_log_id: str | None = None,
        extra_context: dict[str, Any] | None = None,
        timestamp: datetime | None = None,
    ) -> str:
        """Log an error event to the database.

        Args:
            model_name: The model name associated with the error.
            error_type: Error type (e.g., sse_stream_error, http_error).
            error_message: Detailed error message.
            backend_name: Backend that produced the error.
            http_status: HTTP status code.
            request_path: Request path where error occurred.
            request_log_id: Reference to request log.
            extra_context: Additional error context.
            timestamp: Error timestamp.

        Returns:
            The UUID of the created error log.
        """
        error_uuid = uuid.uuid4()
        self._ensure_initialized()
        if not self._initialized:
            logger.warning("Database not initialized, skipping database error logging")
            return str(error_uuid)

        if timestamp is None:
            timestamp = datetime.now(timezone.utc)

        request_log_uuid: uuid.UUID | None = None
        if request_log_id:
            if isinstance(request_log_id, uuid.UUID):
                request_log_uuid = request_log_id
            else:
                request_log_uuid = uuid.UUID(str(request_log_id))

        error_log = ErrorLog(
            id=error_uuid,
            timestamp=timestamp,
            model_name=model_name,
            error_type=error_type,
            error_message=error_message,
            backend_name=backend_name,
            http_status=http_status,
            request_path=request_path,
            request_log_id=request_log_uuid,
            extra_context=extra_context,
        )

        # Capture the ID before starting async task
        error_id = str(error_uuid)

        try:
            loop = asyncio.get_running_loop()

            async def _save_error() -> None:
                def _save() -> None:
                    try:
                        with self._database.session() as sess:
                            sess.add(error_log)
                            sess.flush()  # Ensure it's persisted
                            sess.expunge(error_log)  # Detach from session
                    except Exception as e:
                        logger.error(f"Failed to save error log to database: {e}")

                await asyncio.to_thread(_save)

            task = loop.create_task(_save_error())
            _register_background_task(task)
            logger.debug(f"Scheduled async save for error log: {error_id}")

        except RuntimeError:
            # No event loop, save synchronously
            try:
                with self._database.session() as sess:
                    sess.add(error_log)
                    sess.flush()
                logger.debug(f"Saved error log synchronously: {error_id}")
            except Exception as e:
                logger.error(f"Failed to save error log to database: {e}")

        return error_id


# Global database logger instance
_db_logger: Optional[DatabaseLogRecorder] = None


def get_db_logger() -> DatabaseLogRecorder:
    """Get the global database logger instance.

    Returns:
        The global DatabaseLogRecorder instance.
    """
    global _db_logger
    if _db_logger is None:
        _db_logger = DatabaseLogRecorder()
    return _db_logger
