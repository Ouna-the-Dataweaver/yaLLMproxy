"""SSE (Server-Sent Events) stream utilities and error detection."""

import json
from typing import Optional


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

