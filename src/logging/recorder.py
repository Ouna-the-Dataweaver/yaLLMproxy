"""Request logging and error tracking for the proxy."""

import asyncio
import base64
import copy
import json
import logging
import os
from dataclasses import dataclass
from datetime import datetime
from pathlib import Path
from typing import Any, Collection, Mapping, Optional

# Import database logger for integration
try:
    from ..database.logger import get_db_logger
except ImportError:
    get_db_logger = None  # type: ignore

logger = logging.getLogger("yallmp-proxy")

# Flag to disable database logging (useful during testing)
_DB_LOGGING_ENABLED = True


@dataclass(frozen=True)
class DbLogTarget:
    """Database logging target for a request."""

    instance_key: str = "default"
    config: Optional[dict[str, Any]] = None
    enabled: bool = True


_DEFAULT_TEST_HEADER = "x-yallmp-test"
_DEFAULT_TEST_MODEL_NAMES = {"unknown"}
_DEFAULT_TEST_MODEL_PREFIXES = ("unknown/",)
_TESTING_INSTANCE_KEY = "testing"
_TESTING_DETECTION_KEYS = {
    "enabled",
    "header",
    "headers",
    "model_prefixes",
    "model_names",
    "treat_unknown_models_as_test",
}


def _parse_bool(value: Any, default: bool = False) -> bool:
    if value is None:
        return default
    if isinstance(value, str):
        return value.strip().lower() in {"1", "true", "yes", "on"}
    return bool(value)


def _normalize_str_list(value: Any) -> list[str]:
    if value is None:
        return []
    if isinstance(value, (list, tuple, set)):
        return [str(item).strip() for item in value if str(item).strip()]
    text = str(value).strip()
    return [text] if text else []


def _truthy_header_value(value: Any) -> bool:
    if value is None:
        return False
    return _parse_bool(str(value), default=False)


def _deep_merge_dicts(base: dict[str, Any], override: dict[str, Any]) -> dict[str, Any]:
    """Deep merge two dictionaries with override values taking precedence."""
    merged = copy.deepcopy(base)
    for key, value in override.items():
        if (
            key in merged
            and isinstance(merged[key], dict)
            and isinstance(value, dict)
        ):
            merged[key] = _deep_merge_dicts(merged[key], value)
        else:
            merged[key] = copy.deepcopy(value)
    return merged


def _derive_test_sqlite_path(path: str) -> str:
    if path == ":memory:":
        return path
    suffix = Path(path).suffix
    if suffix:
        return str(Path(path).with_name(f"{Path(path).stem}.test{suffix}"))
    return f"{path}.test"


def _resolve_base_db_config(db_config: Optional[dict[str, Any]]) -> dict[str, Any]:
    if db_config is not None:
        return db_config
    try:
        from ..database.factory import get_database

        db_instance = get_database()
        if getattr(db_instance, "config", None):
            return db_instance.config
    except Exception:
        pass
    return {
        "backend": "sqlite",
        "connection": {"sqlite": {"path": "logs/yaLLM.db"}},
        "pool_size": 5,
        "max_overflow": 10,
    }


def _extract_testing_overrides(testing_cfg: dict[str, Any]) -> dict[str, Any]:
    return {
        key: value
        for key, value in testing_cfg.items()
        if key not in _TESTING_DETECTION_KEYS
    }


def _build_test_db_config(
    base_config: dict[str, Any],
    testing_cfg: dict[str, Any],
) -> Optional[dict[str, Any]]:
    overrides = _extract_testing_overrides(testing_cfg)
    base_backend = str(base_config.get("backend", "sqlite")).lower()

    if base_backend in {"postgres", "postgresql"} and not overrides:
        return None

    merged = _deep_merge_dicts({k: v for k, v in base_config.items() if k != "testing"}, overrides)
    backend = str(merged.get("backend", base_backend)).lower()

    if backend == "sqlite":
        connection = merged.get("connection") or {}
        sqlite_cfg = connection.get("sqlite") or {}
        override_path = (
            overrides.get("connection", {})
            .get("sqlite", {})
            .get("path")
        )
        if not override_path:
            base_path = (
                base_config.get("connection", {})
                .get("sqlite", {})
                .get("path", "logs/yaLLM.db")
            )
            sqlite_cfg["path"] = _derive_test_sqlite_path(str(base_path))
        connection["sqlite"] = sqlite_cfg
        merged["connection"] = connection

    return merged


def _is_test_request(
    model_name: str,
    headers: Optional[Mapping[str, str]],
    known_models: Optional[Collection[str]],
    testing_cfg: dict[str, Any],
) -> bool:
    header_names = _normalize_str_list(
        testing_cfg.get("headers") or testing_cfg.get("header") or _DEFAULT_TEST_HEADER
    )
    if headers and header_names:
        normalized = {str(k).lower(): v for k, v in headers.items()}
        for header_name in header_names:
            if _truthy_header_value(normalized.get(header_name.lower())):
                return True

    model_names = _normalize_str_list(
        testing_cfg.get("model_names", _DEFAULT_TEST_MODEL_NAMES)
    )
    model_prefixes = _normalize_str_list(
        testing_cfg.get("model_prefixes", _DEFAULT_TEST_MODEL_PREFIXES)
    )

    model_lower = (model_name or "").strip().lower()
    if model_lower:
        if model_names:
            name_set = {name.lower() for name in model_names}
            if model_lower in name_set:
                return True
        for prefix in model_prefixes:
            if model_lower.startswith(prefix.lower()):
                return True

    treat_unknown = _parse_bool(
        testing_cfg.get("treat_unknown_models_as_test", True), default=True
    )
    if treat_unknown:
        if not model_lower:
            return True
        if known_models is not None and model_name not in known_models:
            return True

    return False


def resolve_db_log_target(
    model_name: str,
    headers: Optional[Mapping[str, str]] = None,
    known_models: Optional[Collection[str]] = None,
    db_config: Optional[dict[str, Any]] = None,
) -> DbLogTarget:
    """Determine which database logger should receive a request."""
    if not _DB_LOGGING_ENABLED or get_db_logger is None:
        return DbLogTarget(enabled=False)
    base_config = _resolve_base_db_config(db_config)
    testing_cfg = base_config.get("testing") or {}
    testing_enabled = _parse_bool(testing_cfg.get("enabled"), default=False)
    if not testing_enabled:
        return DbLogTarget()

    if not _is_test_request(model_name, headers, known_models, testing_cfg):
        return DbLogTarget()

    test_config = _build_test_db_config(base_config, testing_cfg)
    if test_config is None:
        return DbLogTarget(instance_key=_TESTING_INSTANCE_KEY, enabled=False)

    return DbLogTarget(instance_key=_TESTING_INSTANCE_KEY, config=test_config, enabled=True)


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
    db_log_target: Optional[DbLogTarget] = None,
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
    target = db_log_target or DbLogTarget()
    if _DB_LOGGING_ENABLED and target.enabled and get_db_logger is not None:
        try:
            db_logger = get_db_logger(
                instance_key=target.instance_key,
                config=target.config,
            )
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
        log_to_disk: bool = True,
        db_log_target: Optional[DbLogTarget] = None,
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

        # Enhanced logging fields for stop_reason and agentic workflows
        self._stop_reason: Optional[str] = None
        self._accumulated_response_parts: list[str] = []
        self._accumulated_tool_calls: list[dict[str, Any]] = []
        self._accumulated_reasoning_content: Optional[str] = None
        self._conversation_turn: Optional[int] = None
        self._is_tool_call: bool = False
        self._modules_log: Optional[dict[str, Any]] = None

        self._log_parsed_response = bool(log_parsed_response)
        if log_parsed_stream is None:
            self._log_parsed_stream = self._log_parsed_response
        else:
            self._log_parsed_stream = bool(log_parsed_stream)
        self._log_to_disk = bool(log_to_disk)
        self._parsed_initialized = False

        # App key tracking
        self._app_key_id: Optional[str] = None

        REQUEST_LOG_DIR.mkdir(parents=True, exist_ok=True)
        self._append_text(f"log_start={self._started}\n")

        # Initialize database logger (if available and enabled)
        self._db_logger: Optional[Any] = None
        self._db_log_id: Optional[str] = None
        self._db_log_target = db_log_target or DbLogTarget()
        if _DB_LOGGING_ENABLED and self._db_log_target.enabled and get_db_logger is not None:
            try:
                self._db_logger = get_db_logger(
                    instance_key=self._db_log_target.instance_key,
                    config=self._db_log_target.config,
                )
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

    def configure_disk_logging(self, log_to_disk: bool) -> None:
        if self._finalized:
            return
        self._log_to_disk = bool(log_to_disk)

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
                db_log_target=self._db_log_target,
            )

    def record_usage_stats(self, usage: dict[str, Any]) -> None:
        """Record usage statistics from the response."""
        if self._finalized:
            return
        if usage:
            self._usage_stats = self.normalize_usage_stats(usage)

    @staticmethod
    def normalize_usage_stats(usage: dict[str, Any]) -> dict[str, Any]:
        """Normalize usage statistics to a provider-agnostic format.

        Extracts and standardizes token metrics from different LLM providers.
        Handles variations in field names and nested structures.

        Args:
            usage: Raw usage statistics from the LLM provider response.

        Returns:
            Normalized usage object with standard fields:
            - prompt_tokens: Input tokens
            - completion_tokens: Output tokens
            - total_tokens: Total tokens (or computed sum)
            - prompt_tokens_details: Normalized input details
            - completion_tokens_details: Normalized output details
        """
        if not usage:
            return {}

        # Extract core token counts with fallback handling
        # OpenAI uses prompt_tokens/completion_tokens, Anthropic uses input_tokens/output_tokens
        prompt_tokens = usage.get("prompt_tokens") or usage.get("input_tokens") or 0
        completion_tokens = usage.get("completion_tokens") or usage.get("output_tokens") or 0
        total_tokens = usage.get("total_tokens") or (prompt_tokens + completion_tokens)

        # Normalize prompt tokens details
        prompt_details = usage.get("prompt_tokens_details") or {}
        # Anthropic uses cache_creation_input_tokens and cache_read_input_tokens
        cached_tokens = (
            prompt_details.get("cached_tokens")
            or usage.get("cached_tokens")
            or usage.get("cache_read_input_tokens")
            or 0
        )
        cache_creation_tokens = usage.get("cache_creation_input_tokens") or 0

        # Normalize completion tokens details
        completion_details = usage.get("completion_tokens_details") or {}
        reasoning_tokens = completion_details.get("reasoning_tokens") or 0

        # Build normalized structure
        normalized = {
            "prompt_tokens": prompt_tokens,
            "completion_tokens": completion_tokens,
            "total_tokens": total_tokens,
        }

        # Add details if any are present
        prompt_details_normalized: dict[str, Any] = {}
        if cached_tokens > 0:
            prompt_details_normalized["cached_tokens"] = cached_tokens
        if cache_creation_tokens > 0:
            prompt_details_normalized["cache_creation_tokens"] = cache_creation_tokens
        if prompt_details_normalized:
            normalized["prompt_tokens_details"] = prompt_details_normalized

        if reasoning_tokens > 0:
            normalized["completion_tokens_details"] = {"reasoning_tokens": reasoning_tokens}

        # Preserve any additional provider-specific fields
        # Skip fields we've already normalized
        skip_fields = {
            "prompt_tokens", "completion_tokens", "total_tokens",
            "prompt_tokens_details", "completion_tokens_details",
            "input_tokens", "output_tokens",  # Anthropic equivalents
            "cache_creation_input_tokens", "cache_read_input_tokens",  # Anthropic cache fields
        }
        for key, value in usage.items():
            if key not in skip_fields and key not in normalized:
                normalized[key] = value

        return normalized

    def record_stop_reason(self, reason: Optional[str]) -> None:
        """Record the stop reason from the response.

        Args:
            reason: The finish reason (stop, tool_calls, length, content_filter, etc.)
        """
        if self._finalized:
            return
        if reason:
            self._stop_reason = reason
            # Also mark as tool call if the reason indicates tool usage
            # Different providers use different naming conventions
            if reason in ("tool_calls", "function_call", "tool_use"):
                self._is_tool_call = True
            self._append_text(f"stop_reason={reason}\n")

    def record_stream_delta(
        self,
        delta: dict[str, Any],
        chunk_index: int,
    ) -> None:
        """Record a streaming delta for response accumulation.

        Accumulates content, tool calls, and reasoning content from stream chunks
        to build the complete response.

        Args:
            delta: The delta object from a streaming chunk.
            chunk_index: Index of this chunk for logging.
        """
        if self._finalized:
            return

        # Accumulate content
        content = delta.get("content")
        if isinstance(content, str) and content:
            self._accumulated_response_parts.append(content)

        # Accumulate tool calls - merge by index to avoid duplicates from streaming
        tool_calls = delta.get("tool_calls")
        if isinstance(tool_calls, list):
            for tc in tool_calls:
                if isinstance(tc, dict):
                    index = tc.get("index")
                    # Check if we already have a tool call with this index
                    existing = None
                    for i, existing_tc in enumerate(self._accumulated_tool_calls):
                        if existing_tc.get("index") == index:
                            existing = i
                            break

                    if existing is not None:
                        # Merge with existing tool call - specifically merge function.arguments
                        existing_tc = self._accumulated_tool_calls[existing]
                        fn = tc.get("function") or tc.get("function_call")
                        existing_fn = existing_tc.get("function") or existing_tc.get("function_call")
                        if fn and existing_fn and "arguments" in fn:
                            # Append arguments to existing tool call
                            existing_args = existing_fn.get("arguments") or ""
                            new_args = fn.get("arguments") or ""
                            existing_fn["arguments"] = existing_args + new_args
                            # Also merge other fields that might be updated
                            if "name" in fn and fn["name"] != existing_fn.get("name"):
                                existing_fn["name"] = fn["name"]
                        # Also merge other top-level fields
                        for key, value in tc.items():
                            if key != "function" and key != "function_call" and key not in existing_tc:
                                existing_tc[key] = value
                    else:
                        # Add as new tool call
                        self._accumulated_tool_calls.append(tc.copy())

        # Accumulate reasoning content
        reasoning = delta.get("reasoning_content")
        if isinstance(reasoning, str) and reasoning:
            if self._accumulated_reasoning_content is None:
                self._accumulated_reasoning_content = ""
            self._accumulated_reasoning_content += reasoning

    def record_conversation_turn(self, turn: int) -> None:
        """Record the conversation turn number for agentic workflows.

        Args:
            turn: The turn number in the conversation sequence.
        """
        if self._finalized:
            return
        self._conversation_turn = turn
        self._append_text(f"conversation_turn={turn}\n")

    def mark_as_tool_call(self) -> None:
        """Mark this request as resulting in tool/function calls."""
        if self._finalized:
            return
        self._is_tool_call = True
        self._append_text("is_tool_call=true\n")

    def record_modules_log(self, modules_log: dict[str, Any]) -> None:
        """Record debug logs from response modules.

        Args:
            modules_log: Summary of module processing events (reasoning detection,
                        tool calls, swaps, etc.)
        """
        if self._finalized:
            return
        if modules_log:
            self._modules_log = modules_log
            # Also append a summary to the text log
            event_counts = modules_log.get("event_counts", {})
            if event_counts:
                self._append_text(f"modules_events={event_counts}\n")

    def set_app_key(self, key_id: Optional[str]) -> None:
        """Set the app key ID for this request.

        Args:
            key_id: The app key ID used for authentication, or None if unauthenticated.
        """
        if self._finalized:
            return
        self._app_key_id = key_id
        if key_id:
            self._append_text(f"app_key_id={key_id}\n")

    @property
    def app_key_id(self) -> Optional[str]:
        """Get the app key ID for this request."""
        return self._app_key_id

    def record_first_content_time(self) -> None:
        """No-op, kept for API compatibility. Throughput is now calculated from duration."""
        pass

    def record_generation_end_time(self) -> None:
        """No-op, kept for API compatibility. Throughput is now calculated from duration."""
        pass

    def _calculate_throughput_metrics(self, duration_ms: Optional[int]) -> Optional[dict[str, Any]]:
        """Calculate throughput metrics based on total duration.

        Uses a weighted formula that accounts for different processing speeds:
        - Output (decode) tokens: baseline (1x)
        - Input (prefill) tokens: ~10x faster than decode, so /10
        - Cached tokens: ~100x faster than decode, so /100

        Formula: (input - cached) / 10 + cached / 100 + output

        Returns:
            Dictionary with tokens_per_second (throughput), or None if cannot be calculated.
        """
        if not self._usage_stats or not duration_ms or duration_ms <= 0:
            return None

        prompt_tokens = self._usage_stats.get("prompt_tokens", 0)
        completion_tokens = self._usage_stats.get("completion_tokens", 0)

        # Get cached tokens from prompt_tokens_details if available
        cached_tokens = 0
        prompt_details = self._usage_stats.get("prompt_tokens_details")
        if isinstance(prompt_details, dict):
            cached_tokens = prompt_details.get("cached_tokens", 0)

        # Calculate weighted tokens:
        # - non-cached input: 10x faster than decode -> /10
        # - cached input: 100x faster than decode -> /100
        # - output: baseline
        non_cached_input = prompt_tokens - cached_tokens
        weighted_tokens = (non_cached_input / 10) + (cached_tokens / 100) + completion_tokens

        if weighted_tokens <= 0:
            return None

        # Calculate throughput
        duration_seconds = duration_ms / 1000.0
        tokens_per_second = weighted_tokens / duration_seconds

        return {
            "tokens_per_second": round(tokens_per_second, 2),
            "weighted_tokens": round(weighted_tokens, 2),
        }

    def _build_full_response(self) -> Optional[str]:
        """Build the complete concatenated response from accumulated parts.

        Returns:
            The concatenated response text, or None if no content.
        """
        if not self._accumulated_response_parts:
            return None

        full_response = "".join(self._accumulated_response_parts)

        # Include reasoning content if present
        if self._accumulated_reasoning_content:
            full_response = (
                f"<think>{self._accumulated_reasoning_content}</think>\n{full_response}"
            )

        return full_response

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

        # Calculate and merge throughput metrics into usage_stats
        throughput_metrics = self._calculate_throughput_metrics(duration_ms)
        if throughput_metrics:
            if self._usage_stats is None:
                self._usage_stats = {}
            self._usage_stats.update(throughput_metrics)
            self._append_text(
                f"throughput={throughput_metrics['tokens_per_second']} tok/s "
                f"weighted_tokens={throughput_metrics['weighted_tokens']}\n"
            )

        # Build the full concatenated response
        full_response = self._build_full_response()

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
                    # Enhanced logging fields
                    stop_reason=self._stop_reason,
                    full_response=full_response,
                    is_tool_call=self._is_tool_call,
                    tool_calls=self._accumulated_tool_calls if self._accumulated_tool_calls else None,
                    conversation_turn=self._conversation_turn,
                    modules_log=self._modules_log,
                    # App key tracking
                    app_key_id=self._app_key_id,
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
            if not self._log_to_disk:
                return
            tmp_path = self.log_path.with_suffix(self.log_path.suffix + ".tmp")
            with tmp_path.open("wb") as fh:
                fh.write(data)
            os.replace(tmp_path, self.log_path)
            self._write_parsed_log()
            self._write_request_json()

        await asyncio.to_thread(_write)

    def _write_to_disk(self) -> None:
        if not self._log_to_disk:
            return
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
            if key_lower in {"authorization", "proxy-connection", "x-api-key"}:
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
