"""Request logging and error tracking for the proxy."""

import asyncio
import base64
import json
import logging
import os
from datetime import datetime
from pathlib import Path
from typing import Any, Mapping, Optional

# Import database logger for integration
try:
    from ..database.logger import get_db_logger
except ImportError:
    get_db_logger = None  # type: ignore

logger = logging.getLogger("yallmp-proxy")

# Flag to disable database logging (useful during testing)
_DB_LOGGING_ENABLED = True


def set_db_logging_enabled(enabled: bool) -> None:
    """Enable or disable database logging globally.

    Args:
        enabled: True to enable database logging, False to disable.
    """
    global _DB_LOGGING_ENABLED
    _DB_LOGGING_ENABLED = enabled


def is_db_logging_enabled() -> bool:
    """Check if database logging is enabled.

    Returns:
        True if database logging is enabled, False otherwise.
    """
    return _DB_LOGGING_ENABLED

REQUEST_LOG_DIR = Path(__file__).resolve().parent.parent.parent.joinpath("logs").joinpath("requests")
ERROR_LOG_DIR = Path(__file__).resolve().parent.parent.parent.joinpath("logs").joinpath("errors")
_PENDING_LOG_TASKS: set[asyncio.Task] = set()


def _register_background_task(task: asyncio.Task) -> None:
    """Register a background task and set up cleanup."""
    _PENDING_LOG_TASKS.add(task)

    def _cleanup(_task: asyncio.Task) -> None:
        _PENDING_LOG_TASKS.discard(_task)

    task.add_done_callback(_cleanup)


def log_error_event(
    model_name: str,
    error_type: str,
    error_message: str,
    backend_name: Optional[str] = None,
    http_status: Optional[int] = None,
    request_path: Optional[str] = None,
    request_log_path: Optional[Path] = None,
    extra_context: Optional[dict[str, Any]] = None,
) -> None:
    """
    Log an error event to the errors subdirectory for easy error tracking.
    
    This creates a separate, smaller log file per error for quick scanning.
    """
    ERROR_LOG_DIR.mkdir(parents=True, exist_ok=True)
    
    import uuid
    
    timestamp = datetime.utcnow()
    timestamp_str = timestamp.strftime("%Y%m%d_%H%M%S")
    short_id = uuid.uuid4().hex[:4]
    safe_model = "".join(c if c.isalnum() or c in "-_" else "-" for c in (model_name or "unknown"))[:48]
    filename = f"{timestamp_str}-{short_id}_{safe_model}.err"
    error_path = ERROR_LOG_DIR / filename
    
    lines = [
        f"timestamp={timestamp.isoformat()}Z",
        f"model={model_name or 'unknown'}",
        f"error_type={error_type}",
        f"error_message={error_message}",
    ]
    
    if backend_name:
        lines.append(f"backend={backend_name}")
    if http_status is not None:
        lines.append(f"http_status={http_status}")
    if request_path:
        lines.append(f"request_path={request_path}")
    if request_log_path:
        lines.append(f"full_log={request_log_path.name}")
    if extra_context:
        for key, value in extra_context.items():
            lines.append(f"{key}={value}")
    
    content = "\n".join(lines) + "\n"
    
    # Write async if possible, sync otherwise
    try:
        loop = asyncio.get_running_loop()

        async def _write_error_log():
            def _write():
                error_path.write_text(content, encoding="utf-8")
            await asyncio.to_thread(_write)

        task = loop.create_task(_write_error_log())
        _register_background_task(task)
    except RuntimeError:
        # No event loop, write synchronously
        error_path.write_text(content, encoding="utf-8")

    # Also log to database if available and enabled
    if _DB_LOGGING_ENABLED and get_db_logger is not None:
        try:
            db_logger = get_db_logger()
            db_logger.log_error(
                model_name=model_name,
                error_type=error_type,
                error_message=error_message,
                backend_name=backend_name,
                http_status=http_status,
                request_path=request_path,
                request_log_id=None,  # Will be linked if available
                extra_context=extra_context,
            )
        except Exception as e:
            logger.warning(f"Failed to log error to database: {e}")


class RequestLogRecorder:
    """Capture request/response lifecycle data and flush asynchronously."""
    
    def __init__(
        self,
        model_name: str,
        is_stream: bool,
        path: str,
        log_parsed_response: bool = False,
        log_parsed_stream: Optional[bool] = None,
    ) -> None:
        safe_model = self._safe_fragment(model_name or "unknown")
        timestamp = datetime.utcnow().strftime("%Y%m%d_%H%M%S")
        import uuid
        short_id = uuid.uuid4().hex[:4]
        filename = f"{timestamp}-{short_id}_{safe_model}.log"
        self.log_path = REQUEST_LOG_DIR / filename
        self.parsed_log_path = self.log_path.with_name(
            f"{self.log_path.stem}.parsed.log"
        )
        self.model_name = model_name or "unknown"
        self.is_stream = is_stream
        self.request_path = path
        self._buffer = bytearray()
        self._parsed_buffer = bytearray()
        self._finalized = False
        self._stream_chunks = 0
        self._parsed_stream_chunks = 0
        self._started = datetime.utcnow().isoformat() + "Z"
        self._current_backend: Optional[str] = None
        self._last_http_status: Optional[int] = None
        self._error_logged = False
        self._request_json: Optional[dict[str, Any]] = None
        self._usage_stats: Optional[dict[str, Any]] = None
        self._log_parsed_response = bool(log_parsed_response)
        if log_parsed_stream is None:
            self._log_parsed_stream = self._log_parsed_response
        else:
            self._log_parsed_stream = bool(log_parsed_stream)
        self._parsed_initialized = False
        REQUEST_LOG_DIR.mkdir(parents=True, exist_ok=True)
        self._append_text(f"log_start={self._started}\n")

        # Initialize database logger (if available and enabled)
        self._db_logger: Optional[Any] = None
        self._db_log_id: Optional[str] = None
        if _DB_LOGGING_ENABLED and get_db_logger is not None:
            try:
                self._db_logger = get_db_logger()
            except Exception as e:
                logger.debug(f"Database logger not available: {e}")

    def _append_text(self, text: str) -> None:
        self._buffer.extend(text.encode("utf-8"))

    def _append_bytes(self, data: bytes) -> None:
        self._buffer.extend(data)

    def _append_parsed_text(self, text: str) -> None:
        self._parsed_buffer.extend(text.encode("utf-8"))

    def _append_parsed_bytes(self, data: bytes) -> None:
        self._parsed_buffer.extend(data)

    def _init_parsed_log(self) -> None:
        if self._parsed_initialized:
            return
        self._parsed_initialized = True
        self._append_parsed_text(f"log_start={self._started}\n")

    def configure_parsed_logging(
        self, log_parsed_response: bool, log_parsed_stream: Optional[bool] = None
    ) -> None:
        if self._finalized:
            return
        self._log_parsed_response = bool(log_parsed_response)
        if log_parsed_stream is None:
            self._log_parsed_stream = self._log_parsed_response
        else:
            self._log_parsed_stream = bool(log_parsed_stream)

    def record_request(
        self, method: str, query: str, headers: Mapping[str, str], body: bytes
    ) -> None:
        if self._finalized:
            return
        safe_headers = self._safe_headers(headers)
        body_text: Optional[str] = None
        body_json: Optional[Any] = None
        body_base64: Optional[str] = None
        body_is_json = False
        if body:
            try:
                body_text = body.decode("utf-8")
                try:
                    body_json = json.loads(body_text)
                    body_is_json = True
                except json.JSONDecodeError:
                    body_is_json = False
            except UnicodeDecodeError:
                body_base64 = base64.b64encode(body).decode("ascii")
        self._request_json = {
            "request_time": self._started,
            "model": self.model_name,
            "is_stream": self.is_stream,
            "path": self.request_path,
            "method": method,
            "query": query or "",
            "headers": safe_headers,
            "body_len": len(body),
            "body_is_json": body_is_json,
            "body": body_json if body_is_json else body_text,
            "body_base64": body_base64,
        }

    def record_route(self, route: list[str]) -> None:
        if self._finalized:
            return
        self._append_text(f"route={route}\n")

    def record_backend_attempt(self, backend_name: str, attempt: int, url: str) -> None:
        if self._finalized:
            return
        self._current_backend = backend_name
        self._append_text(
            f"=== BACKEND ATTEMPT {attempt} ===\nbackend={backend_name}\nurl={url}\n"
        )

    def record_backend_response(
        self, status: int, headers: Mapping[str, str], body: bytes
    ) -> None:
        if self._finalized:
            return
        self._last_http_status = status
        header_dump = self._safe_json_dict(headers)
        self._append_text(f"status={status}\nresponse_headers={header_dump}\n")
        self._append_text(f"body_len={len(body)}\n-- RESPONSE BODY START --\n")
        if body:
            self._append_text(self._format_payload(body))
        self._append_text("-- RESPONSE BODY END --\n")

    def record_parsed_response(
        self, status: int, headers: Mapping[str, str], body: bytes
    ) -> None:
        if self._finalized or not self._log_parsed_response:
            return
        self._init_parsed_log()
        header_dump = self._safe_json_dict(headers)
        self._append_parsed_text(
            f"=== PARSED RESPONSE ===\nstatus={status}\nresponse_headers={header_dump}\n"
        )
        self._append_parsed_text(f"body_len={len(body)}\n-- PARSED BODY START --\n")
        if body:
            self._append_parsed_text(self._format_payload(body))
        self._append_parsed_text("-- PARSED BODY END --\n")

    def record_stream_headers(self, status: int, headers: Mapping[str, str]) -> None:
        if self._finalized:
            return
        self._last_http_status = status
        header_dump = self._safe_json_dict(headers)
        self._append_text(
            f"=== STREAM RESPONSE ===\nstatus={status}\nresponse_headers={header_dump}\n"
        )

    def record_parsed_stream_headers(
        self, status: int, headers: Mapping[str, str]
    ) -> None:
        if self._finalized or not self._log_parsed_stream:
            return
        self._init_parsed_log()
        header_dump = self._safe_json_dict(headers)
        self._append_parsed_text(
            f"=== PARSED STREAM RESPONSE ===\nstatus={status}\nresponse_headers={header_dump}\n"
        )

    def record_stream_chunk(self, chunk: bytes) -> None:
        if self._finalized:
            return
        self._stream_chunks += 1
        self._append_text(
            f"-- STREAM CHUNK {self._stream_chunks} len={len(chunk)} --\n"
        )
        self._append_text(self._format_payload(chunk))
        self._append_text("-- END STREAM CHUNK --\n")

    def record_parsed_stream_chunk(self, chunk: bytes) -> None:
        if self._finalized or not self._log_parsed_stream:
            return
        self._init_parsed_log()
        self._parsed_stream_chunks += 1
        self._append_parsed_text(
            f"-- PARSED STREAM CHUNK {self._parsed_stream_chunks} len={len(chunk)} --\n"
        )
        self._append_parsed_text(self._format_payload(chunk))
        self._append_parsed_text("-- END PARSED STREAM CHUNK --\n")

    def record_error(self, message: str, error_type: Optional[str] = None) -> None:
        if self._finalized:
            return
        self._append_text(f"ERROR: {message}\n")
        
        # Also log to the errors directory (only once per request)
        if not self._error_logged:
            self._error_logged = True
            # Infer error type if not provided
            if error_type is None:
                if "SSE" in message or "stream error" in message.lower():
                    error_type = "sse_stream_error"
                elif "status" in message.lower():
                    error_type = "http_error"
                elif "timeout" in message.lower():
                    error_type = "timeout"
                elif "cancelled" in message.lower() or "disconnect" in message.lower():
                    error_type = "client_disconnect"
                else:
                    error_type = "unknown"
            
            log_error_event(
                model_name=self.model_name,
                error_type=error_type,
                error_message=message,
                backend_name=self._current_backend,
                http_status=self._last_http_status,
                request_path=self.request_path,
                request_log_path=self.log_path,
            )

    def record_usage_stats(self, usage: dict[str, Any]) -> None:
        """Record usage statistics from the response."""
        if self._finalized:
            return
        if usage:
            self._usage_stats = dict(usage)

    def finalize(self, outcome: str) -> None:
        if self._finalized:
            return
        self._finalized = True
        finished = datetime.utcnow().isoformat() + "Z"
        self._append_text(f"=== FINAL STATUS: {outcome} at {finished} ===\n")

        # Calculate duration
        try:
            started_dt = datetime.fromisoformat(self._started.replace("Z", "+00:00"))
            finished_dt = datetime.fromisoformat(finished.replace("Z", "+00:00"))
            duration_ms = int((finished_dt - started_dt).total_seconds() * 1000)
        except Exception:
            duration_ms = None

        # Save to database (if available)
        if self._db_logger is not None:
            try:
                # Build backend attempts data from route
                backend_attempts = None
                if self._current_backend:
                    backend_attempts = [{
                        "backend": self._current_backend,
                        "status": self._last_http_status,
                    }]

                # Log to database
                self._db_log_id = self._db_logger.log_request(
                    model_name=self.model_name,
                    is_stream=self.is_stream,
                    path=self.request_path,
                    method=self._request_json.get("method") if self._request_json else None,
                    query=self._request_json.get("query") if self._request_json else None,
                    headers=self._request_json.get("headers") if self._request_json else None,
                    body=self._request_json.get("body") if self._request_json else None,
                    backend_attempts=backend_attempts,
                    usage_stats=self._usage_stats,
                    outcome=outcome,
                    duration_ms=duration_ms,
                    request_time=datetime.fromisoformat(self._started.replace("Z", "+00:00")),
                )
            except Exception as e:
                logger.debug(f"Failed to save request to database: {e}")

        try:
            loop = asyncio.get_running_loop()
        except RuntimeError:
            self._write_to_disk()
            return
        task = loop.create_task(self._flush_async())
        _register_background_task(task)

    @property
    def finalized(self) -> bool:
        return self._finalized

    async def _flush_async(self) -> None:
        data = bytes(self._buffer)

        def _write() -> None:
            tmp_path = self.log_path.with_suffix(self.log_path.suffix + ".tmp")
            with tmp_path.open("wb") as fh:
                fh.write(data)
            os.replace(tmp_path, self.log_path)
            self._write_parsed_log()
            self._write_request_json()

        await asyncio.to_thread(_write)

    def _write_to_disk(self) -> None:
        tmp_path = self.log_path.with_suffix(self.log_path.suffix + ".tmp")
        with tmp_path.open("wb") as fh:
            fh.write(self._buffer)
        os.replace(tmp_path, self.log_path)
        self._write_parsed_log()
        self._write_request_json()

    def _write_parsed_log(self) -> None:
        if not self._parsed_buffer:
            return
        tmp_path = self.parsed_log_path.with_suffix(self.parsed_log_path.suffix + ".tmp")
        with tmp_path.open("wb") as fh:
            fh.write(self._parsed_buffer)
        os.replace(tmp_path, self.parsed_log_path)

    def _write_request_json(self) -> None:
        if not self._request_json:
            return
        json_path = self.log_path.with_suffix(".json")
        tmp_path = json_path.with_suffix(json_path.suffix + ".tmp")
        output_data = dict(self._request_json)
        if self._usage_stats:
            output_data["usage"] = self._usage_stats
        content = json.dumps(output_data, ensure_ascii=True, indent=2)
        tmp_path.write_text(content + "\n", encoding="utf-8")
        os.replace(tmp_path, json_path)

    @staticmethod
    def _safe_json_dict(data: Mapping[str, str]) -> str:
        try:
            return json.dumps(RequestLogRecorder._safe_headers(data), sort_keys=True)
        except Exception:
            return str(data)

    @staticmethod
    def _safe_headers(data: Mapping[str, str]) -> dict[str, str]:
        normalized = {str(k): str(v) for k, v in data.items()}
        # Mask sensitive information
        masked_data: dict[str, str] = {}
        for key, value in normalized.items():
            key_lower = key.lower()
            if key_lower in {"authorization", "proxy-connection"}:
                # Mask authorization headers: first 3 chars + ****
                if isinstance(value, str) and value.startswith("Bearer "):
                    bearer_token = value[7:]  # Remove "Bearer " prefix
                    if bearer_token and bearer_token != "empty":
                        masked_value = bearer_token[:3] + "****"
                        masked_data[key] = f"Bearer {masked_value}"
                    else:
                        masked_data[key] = value
                else:
                    masked_data[key] = value[:3] + "****" if len(value) > 3 else "****"
            elif key_lower == "host":
                # Replace host with proxy_host for privacy
                masked_data[key] = "proxy_host"
            else:
                masked_data[key] = value
        return masked_data

    @staticmethod
    def _safe_fragment(text: str) -> str:
        if not text:
            return "unknown"
        filtered = [
            ch if ch.isalnum() or ch in {"-", "_"} else "-"
            for ch in text.strip()
        ]
        collapsed = "".join(filtered).strip("-") or "model"
        return collapsed[:48]

    def _format_payload(self, data: bytes) -> str:
        if not data:
            return ""
        try:
            text = data.decode("utf-8")
        except UnicodeDecodeError:
            return "<non-utf8 binary data omitted>\n"

        pretty = self._attempt_pretty_json(text)
        if pretty is not None:
            text = pretty

        if text.endswith("\n"):
            return text
        return text + "\n"

    @staticmethod
    def _attempt_pretty_json(text: str) -> Optional[str]:
        stripped = text.strip()
        if not stripped or stripped[0] not in "{[":
            return None
        try:
            parsed = json.loads(stripped)
        except json.JSONDecodeError:
            return None
        return json.dumps(parsed, ensure_ascii=False, indent=2)
