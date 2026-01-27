"""SSE (Server-Sent Events) stream utilities and error detection."""

import json
from typing import Any, Optional


# Max bytes to buffer when checking for streaming errors before committing to client
STREAM_ERROR_CHECK_BUFFER_SIZE = 4096


def detect_sse_stream_error(data: bytes) -> Optional[str]:
    """
    Check if buffered SSE data contains an error event.
    
    Returns an error message if an error is detected, None otherwise.
    
    Detects patterns like:
    - MiniMax: data: {"type":"error","error":{...}}
    - Generic: data: {"error":{...}}
    """
    try:
        text = data.decode("utf-8", errors="replace")
    except Exception:
        return None
    
    # Look for SSE data lines
    for line in text.split("\n"):
        line = line.strip()
        if not line.startswith("data:"):
            continue
        
        # Extract the JSON part after "data:"
        json_part = line[5:].strip()
        if not json_part or json_part == "[DONE]":
            continue
        
        try:
            parsed = json.loads(json_part)
        except json.JSONDecodeError:
            continue
        
        if not isinstance(parsed, dict):
            continue
        
        # Pattern 1: MiniMax-style {"type":"error", "error":{...}}
        if parsed.get("type") == "error":
            error_obj = parsed.get("error", {})
            error_msg = error_obj.get("message") or str(error_obj) if error_obj else "unknown error"
            http_code = error_obj.get("http_code", "unknown")
            return f"SSE stream error: {error_msg} (http_code={http_code})"
        
        # Pattern 2: Generic OpenAI-style {"error":{...}} in stream
        error_obj = parsed.get("error")
        if isinstance(error_obj, dict):
            error_msg = error_obj.get("message") or str(error_obj)
            error_type = error_obj.get("type", "unknown")
            return f"SSE stream error: {error_msg} (type={error_type})"
    
    return None


class SSEJSONDecoder:
    """Incremental SSE decoder that extracts JSON payloads from data events."""

    def __init__(self) -> None:
        self._buffer = ""

    def feed(self, chunk: bytes) -> list[dict[str, Any]]:
        if not chunk:
            return []
        text = chunk.decode("utf-8", errors="replace")
        text = text.replace("\r\n", "\n").replace("\r", "\n")
        self._buffer += text

        payloads: list[dict[str, Any]] = []
        while True:
            sep_index = self._buffer.find("\n\n")
            if sep_index == -1:
                break
            raw_event = self._buffer[:sep_index]
            self._buffer = self._buffer[sep_index + 2:]
            if not raw_event.strip():
                continue

            data_lines: list[str] = []
            for line in raw_event.split("\n"):
                if line.startswith("data:"):
                    data_lines.append(line[5:].lstrip())
            if not data_lines:
                continue
            data = "\n".join(data_lines)
            if data.strip() == "[DONE]":
                continue

            try:
                parsed = json.loads(data)
            except json.JSONDecodeError:
                continue
            if isinstance(parsed, dict):
                payloads.append(parsed)

        return payloads

    def flush(self) -> list[dict[str, Any]]:
        """Flush any remaining data in the buffer.

        Call this at the end of the stream to process any final events
        that may not have been followed by a double newline.
        """
        if not self._buffer.strip():
            return []

        payloads: list[dict[str, Any]] = []

        # Process remaining buffer as potential events
        remaining = self._buffer.strip()
        self._buffer = ""

        if not remaining:
            return []

        # Try to parse remaining data as SSE events
        data_lines: list[str] = []
        for line in remaining.split("\n"):
            line = line.strip()
            if line.startswith("data:"):
                data_lines.append(line[5:].lstrip())

        if not data_lines:
            return []

        data = "\n".join(data_lines)
        if data.strip() == "[DONE]":
            return []

        try:
            parsed = json.loads(data)
        except json.JSONDecodeError:
            return []

        if isinstance(parsed, dict):
            payloads.append(parsed)

        return payloads
