"""Anthropic <-> OpenAI Messages translation.

This module translates between Anthropic Messages API format and OpenAI Chat
Completions API format, enabling the proxy to route Anthropic-format requests
to OpenAI-compatible backends.

Key mappings:
- Anthropic system (top-level) -> OpenAI system message
- Anthropic content blocks -> OpenAI content parts / tool_calls / tool messages
- Anthropic tools -> OpenAI functions/tools
- Anthropic tool_choice -> OpenAI tool_choice

Reference:
- Anthropic Messages API: https://docs.anthropic.com/en/api/messages
- OpenAI Chat Completions: https://platform.openai.com/docs/api-reference/chat
"""

from __future__ import annotations

import base64
import logging
import time
import uuid
from typing import Any, Mapping

logger = logging.getLogger("yallmp-proxy")


def _convert_anthropic_image_to_openai(block: Mapping[str, Any]) -> dict[str, Any]:
    """Convert Anthropic image block to OpenAI image_url content part.

    Anthropic format:
        {"type": "image", "source": {"type": "base64", "media_type": "image/png", "data": "..."}}
        {"type": "image", "source": {"type": "url", "url": "https://..."}}

    OpenAI format:
        {"type": "image_url", "image_url": {"url": "data:image/png;base64,..."}}
        {"type": "image_url", "image_url": {"url": "https://..."}}
    """
    source = block.get("source", {})
    source_type = source.get("type", "")

    if source_type == "base64":
        media_type = source.get("media_type", "image/png")
        data = source.get("data", "")
        url = f"data:{media_type};base64,{data}"
    elif source_type == "url":
        url = source.get("url", "")
    else:
        # Fallback for unknown source types
        url = source.get("url", source.get("data", ""))

    return {"type": "image_url", "image_url": {"url": url}}


def _convert_anthropic_document_to_openai(block: Mapping[str, Any]) -> dict[str, Any]:
    """Convert Anthropic document block to OpenAI content part.

    OpenAI doesn't have native document support, so we convert to text
    with a note about the document, or to image if it's a base64 PDF page.
    """
    source = block.get("source", {})
    source_type = source.get("type", "")
    media_type = source.get("media_type", "application/pdf")

    # For base64 documents, we can try to represent them, but OpenAI
    # doesn't have native PDF support. We'll include a placeholder.
    doc_name = block.get("name", "document")

    if source_type == "base64" and media_type.startswith("image/"):
        # If it's an image document, convert to image
        return _convert_anthropic_image_to_openai({
            "type": "image",
            "source": source
        })

    # For PDFs and other documents, include as text placeholder
    return {
        "type": "text",
        "text": f"[Document: {doc_name} ({media_type})]"
    }


def _convert_content_blocks_to_openai(
    blocks: list[Mapping[str, Any]],
    role: str,
) -> tuple[list[dict[str, Any]] | str | None, list[dict[str, Any]] | None]:
    """Convert Anthropic content blocks to OpenAI format.

    Returns:
        Tuple of (content, tool_calls) where:
        - content is either a string, list of content parts, or None
        - tool_calls is a list of tool call objects (for assistant role) or None
    """
    content_parts: list[dict[str, Any]] = []
    tool_calls: list[dict[str, Any]] = []

    for block in blocks:
        block_type = block.get("type", "")

        if block_type == "text":
            text = block.get("text", "")
            content_parts.append({"type": "text", "text": text})

        elif block_type == "image":
            content_parts.append(_convert_anthropic_image_to_openai(block))

        elif block_type == "document":
            content_parts.append(_convert_anthropic_document_to_openai(block))

        elif block_type == "tool_use":
            # Assistant's tool use -> OpenAI tool_calls
            tool_calls.append({
                "id": block.get("id", f"call_{uuid.uuid4().hex[:8]}"),
                "type": "function",
                "function": {
                    "name": block.get("name", ""),
                    "arguments": _serialize_tool_input(block.get("input", {})),
                }
            })

        elif block_type == "tool_result":
            # Tool results are handled separately in message conversion
            # This shouldn't appear in content_blocks for conversion
            pass

        elif block_type == "thinking":
            # Drop thinking blocks by default (they contain internal reasoning)
            # Could be made configurable in the future
            logger.debug("Dropping thinking block during translation")

        elif block_type == "redacted_thinking":
            # Drop redacted thinking blocks
            logger.debug("Dropping redacted_thinking block during translation")

        else:
            # Unknown block type - pass through as text if possible
            if "text" in block:
                content_parts.append({"type": "text", "text": block["text"]})
            else:
                logger.warning(f"Unknown content block type: {block_type}")

    # Simplify content if it's just text
    if len(content_parts) == 1 and content_parts[0].get("type") == "text":
        content: list[dict[str, Any]] | str | None = content_parts[0]["text"]
    elif len(content_parts) == 0:
        content = None
    else:
        content = content_parts

    return content, tool_calls if tool_calls else None


def _serialize_tool_input(input_data: Any) -> str:
    """Serialize tool input to JSON string for OpenAI format."""
    import json
    if isinstance(input_data, str):
        return input_data
    return json.dumps(input_data, ensure_ascii=False)


def _convert_system_to_openai(system: str | list[Mapping[str, Any]] | None) -> dict[str, Any] | None:
    """Convert Anthropic top-level system to OpenAI system message.

    Anthropic allows system as string or array of content blocks.
    OpenAI expects a single system message with string or content array.
    """
    if system is None:
        return None

    if isinstance(system, str):
        return {"role": "system", "content": system}

    # System is a list of content blocks
    text_parts: list[str] = []
    for block in system:
        if block.get("type") == "text":
            text_parts.append(block.get("text", ""))
        # Non-text blocks in system are unusual - log and skip
        elif block.get("type") not in ("text",):
            logger.warning(f"Non-text block in system parameter: {block.get('type')}")

    if text_parts:
        return {"role": "system", "content": "\n".join(text_parts)}

    return None


def _convert_tool_choice(tool_choice: str | Mapping[str, Any] | None) -> str | dict[str, Any] | None:
    """Convert Anthropic tool_choice to OpenAI format.

    Anthropic: "auto" | "any" | "none" | {"type": "tool", "name": "..."}
    OpenAI: "auto" | "required" | "none" | {"type": "function", "function": {"name": "..."}}
    """
    if tool_choice is None:
        return None

    if isinstance(tool_choice, str):
        if tool_choice == "any":
            return "required"  # any = must use one of the tools
        return tool_choice  # auto, none pass through

    # Object form with type
    choice_type = tool_choice.get("type", "")
    if choice_type == "tool":
        return {
            "type": "function",
            "function": {"name": tool_choice.get("name", "")}
        }
    elif choice_type == "auto":
        return "auto"
    elif choice_type == "any":
        return "required"
    elif choice_type == "none":
        return "none"

    return None


def _convert_tools(tools: list[Mapping[str, Any]] | None) -> list[dict[str, Any]] | None:
    """Convert Anthropic tools to OpenAI format.

    Anthropic: {"name": "...", "description": "...", "input_schema": {...}}
    OpenAI: {"type": "function", "function": {"name": "...", "description": "...", "parameters": {...}}}
    """
    if not tools:
        return None

    openai_tools = []
    for tool in tools:
        openai_tools.append({
            "type": "function",
            "function": {
                "name": tool.get("name", ""),
                "description": tool.get("description", ""),
                "parameters": tool.get("input_schema", {}),
            }
        })

    return openai_tools


def messages_to_chat_completions(payload: Mapping[str, Any]) -> dict[str, Any]:
    """Translate Anthropic Messages request to OpenAI Chat Completions.

    Handles:
    - Top-level system parameter -> system message
    - Content blocks (text, image, tool_use, tool_result)
    - Tools and tool_choice mapping
    - Parameter mapping (max_tokens, stop_sequences, temperature, etc.)

    Args:
        payload: Anthropic Messages API request body

    Returns:
        OpenAI Chat Completions API request body
    """
    openai_messages: list[dict[str, Any]] = []

    # Convert system parameter to system message
    system = payload.get("system")
    system_message = _convert_system_to_openai(system)
    if system_message:
        openai_messages.append(system_message)

    # Convert messages
    anthropic_messages = payload.get("messages", [])

    for msg in anthropic_messages:
        role = msg.get("role", "user")
        content = msg.get("content")

        # Content can be string or list of content blocks
        if isinstance(content, str):
            openai_messages.append({"role": role, "content": content})
            continue

        if not isinstance(content, list):
            # Fallback for unexpected content type
            openai_messages.append({"role": role, "content": str(content) if content else ""})
            continue

        # Handle content blocks
        # First, check for tool_result blocks (they need special handling)
        tool_results = [b for b in content if b.get("type") == "tool_result"]
        other_blocks = [b for b in content if b.get("type") != "tool_result"]

        # Add tool result messages first (Anthropic requires tool_result first in user turn)
        for tool_result in tool_results:
            tool_call_id = tool_result.get("tool_use_id", "")
            result_content = tool_result.get("content")
            is_error = tool_result.get("is_error", False)

            # Result content can be string or array of content blocks
            if isinstance(result_content, str):
                content_str = result_content
            elif isinstance(result_content, list):
                # Concatenate text from content blocks
                text_parts = []
                for block in result_content:
                    if block.get("type") == "text":
                        text_parts.append(block.get("text", ""))
                content_str = "\n".join(text_parts) if text_parts else ""
            else:
                content_str = str(result_content) if result_content else ""

            # Add error prefix if this is an error result
            if is_error:
                content_str = f"[Error] {content_str}"

            openai_messages.append({
                "role": "tool",
                "tool_call_id": tool_call_id,
                "content": content_str,
            })

        # Process remaining content blocks
        if other_blocks:
            converted_content, tool_calls = _convert_content_blocks_to_openai(other_blocks, role)

            if role == "assistant":
                msg_dict: dict[str, Any] = {"role": "assistant"}
                if converted_content is not None:
                    msg_dict["content"] = converted_content
                if tool_calls:
                    msg_dict["tool_calls"] = tool_calls
                if msg_dict.get("content") is not None or msg_dict.get("tool_calls"):
                    openai_messages.append(msg_dict)
            else:
                # User role
                if converted_content is not None:
                    openai_messages.append({"role": role, "content": converted_content})

    # Build the request
    result: dict[str, Any] = {
        "model": payload.get("model", ""),
        "messages": openai_messages,
    }

    # Map max_tokens
    if "max_tokens" in payload:
        result["max_tokens"] = payload["max_tokens"]

    # Map stop_sequences -> stop
    if "stop_sequences" in payload:
        result["stop"] = payload["stop_sequences"]

    # Pass through common parameters
    for param in ("temperature", "top_p"):
        if param in payload:
            result[param] = payload[param]

    # top_k is Anthropic-specific, OpenAI doesn't support it directly
    # We log a warning but don't fail
    if "top_k" in payload:
        logger.debug(f"top_k={payload['top_k']} is not supported by OpenAI, ignoring")

    # Convert tools
    tools = _convert_tools(payload.get("tools"))
    if tools:
        result["tools"] = tools

    # Convert tool_choice
    tool_choice = _convert_tool_choice(payload.get("tool_choice"))
    if tool_choice is not None:
        result["tool_choice"] = tool_choice

    # Stream parameter
    if "stream" in payload:
        result["stream"] = payload["stream"]

    # Metadata - OpenAI uses user field for tracking
    metadata = payload.get("metadata")
    if metadata and isinstance(metadata, dict):
        if "user_id" in metadata:
            result["user"] = metadata["user_id"]

    return result


def _convert_stop_reason(finish_reason: str | None) -> str:
    """Convert OpenAI finish_reason to Anthropic stop_reason.

    OpenAI: stop, length, tool_calls, content_filter, function_call
    Anthropic: end_turn, max_tokens, stop_sequence, tool_use, refusal
    """
    if finish_reason is None:
        return "end_turn"

    mapping = {
        "stop": "end_turn",
        "length": "max_tokens",
        "tool_calls": "tool_use",
        "function_call": "tool_use",
        "content_filter": "refusal",
    }

    return mapping.get(finish_reason, "end_turn")


def _convert_openai_content_to_blocks(
    content: str | list[Mapping[str, Any]] | None,
    tool_calls: list[Mapping[str, Any]] | None = None,
) -> list[dict[str, Any]]:
    """Convert OpenAI assistant content to Anthropic content blocks.

    Returns list of content blocks for the Anthropic response.
    """
    blocks: list[dict[str, Any]] = []

    # Handle text content
    if isinstance(content, str) and content:
        blocks.append({"type": "text", "text": content})
    elif isinstance(content, list):
        for part in content:
            part_type = part.get("type", "")
            if part_type == "text":
                blocks.append({"type": "text", "text": part.get("text", "")})
            # Other part types (refusal, etc.) can be added as needed

    # Handle tool calls
    if tool_calls:
        import json
        for call in tool_calls:
            call_id = call.get("id", f"toolu_{uuid.uuid4().hex[:8]}")
            function = call.get("function", {})
            name = function.get("name", "")
            arguments = function.get("arguments", "{}")

            # Parse arguments string to dict
            try:
                input_dict = json.loads(arguments) if isinstance(arguments, str) else arguments
            except json.JSONDecodeError:
                input_dict = {"raw": arguments}

            blocks.append({
                "type": "tool_use",
                "id": call_id,
                "name": name,
                "input": input_dict,
            })

    return blocks


def chat_completion_to_messages(payload: Mapping[str, Any]) -> dict[str, Any]:
    """Translate OpenAI Chat Completions response to Anthropic Messages.

    Handles:
    - Response envelope (id, type, role, model, usage, stop_reason)
    - Content blocks (text, tool_use)
    - Usage mapping
    - Stop reason mapping

    Args:
        payload: OpenAI Chat Completions API response body

    Returns:
        Anthropic Messages API response body
    """
    import json

    # Extract from OpenAI response
    openai_id = payload.get("id", f"chatcmpl-{uuid.uuid4().hex[:12]}")
    model = payload.get("model", "")
    choices = payload.get("choices", [])
    usage = payload.get("usage", {})

    # Get the first choice (Anthropic only supports n=1)
    choice = choices[0] if choices else {}
    message = choice.get("message", {})
    finish_reason = choice.get("finish_reason")

    # Convert content and tool calls to blocks
    content = message.get("content")
    tool_calls = message.get("tool_calls")
    content_blocks = _convert_openai_content_to_blocks(content, tool_calls)

    # If no content blocks, add empty text block
    if not content_blocks:
        content_blocks = [{"type": "text", "text": ""}]

    # Build Anthropic response
    response: dict[str, Any] = {
        "id": f"msg_{openai_id.replace('chatcmpl-', '')}",
        "type": "message",
        "role": "assistant",
        "content": content_blocks,
        "model": model,
        "stop_reason": _convert_stop_reason(finish_reason),
        "stop_sequence": None,  # OpenAI doesn't provide this
        "usage": {
            "input_tokens": usage.get("prompt_tokens", 0),
            "output_tokens": usage.get("completion_tokens", 0),
        },
    }

    return response
