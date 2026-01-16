"""Bidirectional translation between Responses API and Chat Completions.

This module handles:
1. Converting Responses API requests to Chat Completions format
2. Converting Chat Completions responses to Responses API format
3. Tool/function call translation
4. Usage statistics translation
"""

import logging
import time
from typing import Any, Optional
from uuid import uuid4

from ..types.responses import (
    ResponseObject,
    ResponseUsage,
    OutputItem,
    MessageItem,
    FunctionCallItem,
    OutputText,
    InputItem,
)
from .state_store import ResponseStateStore

logger = logging.getLogger("yallmp-proxy")


def generate_response_id() -> str:
    """Generate a unique response ID."""
    return f"resp_{uuid4().hex[:32]}"


def generate_message_id() -> str:
    """Generate a unique message ID."""
    return f"msg_{uuid4().hex[:24]}"


def generate_call_id() -> str:
    """Generate a unique tool call ID."""
    return f"call_{uuid4().hex[:24]}"


# =============================================================================
# Responses API → Chat Completions
# =============================================================================


async def responses_to_chat_completions(
    input_: str | list[Any],
    model: str,
    instructions: Optional[str] = None,
    previous_response_id: Optional[str] = None,
    state_store: Optional[ResponseStateStore] = None,
    tools: Optional[list[dict]] = None,
    tool_choice: Optional[str | dict] = None,
    temperature: Optional[float] = None,
    top_p: Optional[float] = None,
    max_output_tokens: Optional[int] = None,
    stream: bool = False,
    **kwargs,
) -> dict[str, Any]:
    """Convert Responses API request to Chat Completions format.

    Args:
        input_: The input (string or list of items)
        model: Model name
        instructions: System/developer instructions
        previous_response_id: ID of previous response for conversation continuation
        state_store: State store for retrieving conversation history
        tools: Tool definitions
        tool_choice: Tool choice setting
        temperature: Sampling temperature
        top_p: Nucleus sampling
        max_output_tokens: Maximum output tokens
        stream: Whether to stream the response
        **kwargs: Additional parameters to pass through

    Returns:
        Chat Completions request body
    """
    messages: list[dict[str, Any]] = []

    # 1. Add instructions as system message
    if instructions:
        messages.append({
            "role": "system",
            "content": instructions,
        })

    # 2. If previous_response_id, reconstruct conversation history
    if previous_response_id and state_store:
        try:
            history = await state_store.get_conversation_history(previous_response_id)
            for item in history:
                msg = _convert_item_to_message(item)
                if msg:
                    messages.append(msg)
            logger.debug(
                f"Translator: Reconstructed {len(history)} items from "
                f"previous_response_id {previous_response_id}"
            )
        except Exception as e:
            logger.error(f"Translator: Failed to get history: {e}")

    # 3. Convert current input to messages
    if isinstance(input_, str):
        messages.append({
            "role": "user",
            "content": input_,
        })
    elif isinstance(input_, list):
        for item in input_:
            msg = _convert_item_to_message(item)
            if msg:
                messages.append(msg)

    # 4. Build the request
    request: dict[str, Any] = {
        "model": model,
        "messages": messages,
        "stream": stream,
    }

    # Add optional parameters
    if tools:
        request["tools"] = _convert_tools(tools)

    if tool_choice is not None:
        request["tool_choice"] = tool_choice

    if temperature is not None:
        request["temperature"] = temperature

    if top_p is not None:
        request["top_p"] = top_p

    if max_output_tokens is not None:
        request["max_tokens"] = max_output_tokens

    # Pass through other parameters
    for key in ["presence_penalty", "frequency_penalty", "stop", "logprobs", "top_logprobs"]:
        if key in kwargs and kwargs[key] is not None:
            request[key] = kwargs[key]

    return request


def _convert_item_to_message(item: dict[str, Any]) -> Optional[dict[str, Any]]:
    """Convert a Responses API item to a Chat Completions message."""
    item_type = item.get("type")

    if item_type == "message":
        role = item.get("role", "user")
        # Map "developer" role to "system"
        if role == "developer":
            role = "system"

        content = item.get("content", [])

        # Handle content array
        if isinstance(content, list):
            # Check if it's simple text content
            if len(content) == 1 and content[0].get("type") in ("input_text", "output_text"):
                text_content = content[0].get("text", "")
                return {"role": role, "content": text_content}

            # Multi-part content
            parts: list[dict[str, Any]] = []
            for part in content:
                part_type = part.get("type")
                if part_type in ("input_text", "output_text"):
                    parts.append({
                        "type": "text",
                        "text": part.get("text", ""),
                    })
                elif part_type == "input_image":
                    parts.append({
                        "type": "image_url",
                        "image_url": {
                            "url": part.get("image_url", ""),
                            "detail": part.get("detail", "auto"),
                        },
                    })
                # Add more content type conversions as needed

            if parts:
                return {"role": role, "content": parts}

        elif isinstance(content, str):
            return {"role": role, "content": content}

        return None

    elif item_type == "function_call":
        # Assistant message with tool call
        return {
            "role": "assistant",
            "content": None,
            "tool_calls": [{
                "id": item.get("call_id", item.get("id", generate_call_id())),
                "type": "function",
                "function": {
                    "name": item.get("name", ""),
                    "arguments": item.get("arguments", "{}"),
                },
            }],
        }

    elif item_type == "function_call_output":
        # Tool result message
        return {
            "role": "tool",
            "tool_call_id": item.get("call_id", ""),
            "content": item.get("output", ""),
        }

    else:
        logger.warning(f"Translator: Unknown item type: {item_type}")
        return None


def _convert_tools(tools: list[dict]) -> list[dict]:
    """Convert Responses API tool definitions to Chat Completions format."""
    converted = []
    for tool in tools:
        tool_type = tool.get("type", "function")
        if tool_type == "function":
            converted.append({
                "type": "function",
                "function": {
                    "name": tool.get("name", ""),
                    "description": tool.get("description", ""),
                    "parameters": tool.get("parameters", {}),
                },
            })
        # Add more tool type conversions as needed
    return converted


# =============================================================================
# Chat Completions → Responses API
# =============================================================================


def chat_completion_to_response(
    completion: dict[str, Any],
    original_request: dict[str, Any],
    response_id: str,
    input_data: Any = None,
) -> ResponseObject:
    """Convert Chat Completions response to Responses API format.

    Args:
        completion: The Chat Completions response
        original_request: The original Responses API request
        response_id: The response ID to use
        input_data: The original input for storage

    Returns:
        Responses API response object
    """
    output: list[OutputItem] = []
    output_text_parts: list[str] = []

    choices = completion.get("choices", [])

    for choice in choices:
        message = choice.get("message", {})

        # Handle text content
        content = message.get("content")
        if content:
            msg_id = generate_message_id()
            message_item: MessageItem = {
                "id": msg_id,
                "type": "message",
                "role": "assistant",
                "status": "completed",
                "content": [{
                    "type": "output_text",
                    "text": content,
                    "annotations": [],
                }],
            }
            output.append(message_item)
            output_text_parts.append(content)

        # Handle tool calls
        tool_calls = message.get("tool_calls", [])
        for tool_call in tool_calls:
            tc_id = tool_call.get("id", generate_call_id())
            function = tool_call.get("function", {})
            function_call_item: FunctionCallItem = {
                "id": tc_id,
                "type": "function_call",
                "call_id": tc_id,
                "name": function.get("name", ""),
                "arguments": function.get("arguments", "{}"),
                "status": "completed",
            }
            output.append(function_call_item)

    # Build the response object
    now = time.time()
    response: ResponseObject = {
        "id": response_id,
        "object": "response",
        "created_at": now,
        "completed_at": now,
        "status": "completed",
        "model": completion.get("model", original_request.get("model", "")),
        "previous_response_id": original_request.get("previous_response_id"),
        "output": output,
        "output_text": "".join(output_text_parts),
        "usage": convert_usage(completion.get("usage")),
        "error": None,
        "incomplete_details": None,
        "metadata": original_request.get("metadata", {}),
    }

    # Echo back configuration if provided
    if "temperature" in original_request:
        response["temperature"] = original_request["temperature"]
    if "top_p" in original_request:
        response["top_p"] = original_request["top_p"]
    if "max_output_tokens" in original_request:
        response["max_output_tokens"] = original_request["max_output_tokens"]
    if "tools" in original_request:
        response["tools"] = original_request["tools"]
    if "tool_choice" in original_request:
        response["tool_choice"] = original_request["tool_choice"]

    return response


def convert_usage(usage: Optional[dict[str, Any]]) -> ResponseUsage:
    """Convert Chat Completions usage to Responses API format.

    Args:
        usage: Chat Completions usage object

    Returns:
        Responses API usage object
    """
    if not usage:
        return {}

    result: ResponseUsage = {}

    # Map token fields
    if "prompt_tokens" in usage:
        result["input_tokens"] = usage["prompt_tokens"]
    if "completion_tokens" in usage:
        result["output_tokens"] = usage["completion_tokens"]
    if "total_tokens" in usage:
        result["total_tokens"] = usage["total_tokens"]

    # Map token details if present
    prompt_details = usage.get("prompt_tokens_details")
    if prompt_details:
        result["input_tokens_details"] = {
            "cached_tokens": prompt_details.get("cached_tokens", 0),
        }

    completion_details = usage.get("completion_tokens_details")
    if completion_details:
        result["output_tokens_details"] = {
            "reasoning_tokens": completion_details.get("reasoning_tokens", 0),
        }

    return result


def build_error_response(
    response_id: str,
    error_type: str,
    error_code: str,
    message: str,
    model: str = "",
    param: Optional[str] = None,
) -> ResponseObject:
    """Build an error response object.

    Args:
        response_id: The response ID
        error_type: Error type (server_error, invalid_request, etc.)
        error_code: Error code
        message: Human-readable error message
        model: Model name
        param: Related parameter (optional)

    Returns:
        Responses API error response
    """
    now = time.time()
    response: ResponseObject = {
        "id": response_id,
        "object": "response",
        "created_at": now,
        "status": "failed",
        "model": model,
        "output": [],
        "error": {
            "type": error_type,
            "code": error_code,
            "message": message,
        },
    }

    if param:
        response["error"]["param"] = param

    return response
