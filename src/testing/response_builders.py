"""Response building utilities for simulation tests.

These utilities help construct properly-formatted API responses for testing
without needing to remember all the required fields and structure.
"""

from __future__ import annotations

import json
import uuid
from typing import Any


def build_openai_chat_response(
    content: str,
    *,
    tool_calls: list[dict[str, Any]] | None = None,
    finish_reason: str = "stop",
    usage: dict[str, int] | None = None,
    model: str = "fake-model",
    response_id: str | None = None,
) -> dict[str, Any]:
    """Build a valid OpenAI chat completion response.

    Args:
        content: The assistant message content
        tool_calls: Optional list of tool calls
        finish_reason: Finish reason (stop, length, tool_calls)
        usage: Token usage dict
        model: Model name
        response_id: Optional response ID (auto-generated if not provided)

    Returns:
        Complete OpenAI chat completion response dict
    """
    message: dict[str, Any] = {"role": "assistant", "content": content}

    if tool_calls:
        message["tool_calls"] = [
            {
                "id": tc.get("id", f"call_{uuid.uuid4().hex[:8]}"),
                "type": "function",
                "function": {
                    "name": tc.get("function", {}).get("name", tc.get("name", "unknown")),
                    "arguments": (
                        json.dumps(tc.get("function", {}).get("arguments", {}))
                        if isinstance(tc.get("function", {}).get("arguments"), dict)
                        else tc.get("function", {}).get("arguments", "{}")
                    ),
                },
            }
            for tc in tool_calls
        ]

    response: dict[str, Any] = {
        "id": response_id or f"chatcmpl-{uuid.uuid4().hex[:8]}",
        "object": "chat.completion",
        "model": model,
        "choices": [
            {
                "index": 0,
                "message": message,
                "finish_reason": finish_reason,
            }
        ],
    }

    if usage:
        response["usage"] = usage
    else:
        response["usage"] = {
            "prompt_tokens": 10,
            "completion_tokens": len(content.split()),
            "total_tokens": 10 + len(content.split()),
        }

    return response


def build_openai_stream_chunks(
    content: str,
    *,
    tool_calls: list[dict[str, Any]] | None = None,
    finish_reason: str = "stop",
    include_usage: bool = True,
    model: str = "fake-model",
    response_id: str | None = None,
) -> list[dict[str, Any]]:
    """Build sequence of OpenAI streaming chunks.

    Args:
        content: The assistant message content
        tool_calls: Optional list of tool calls
        finish_reason: Finish reason
        include_usage: Include usage in final chunk
        model: Model name
        response_id: Optional response ID

    Returns:
        List of streaming chunk dicts
    """
    completion_id = response_id or f"chatcmpl-{uuid.uuid4().hex[:8]}"
    chunks: list[dict[str, Any]] = []

    # Initial chunk with role
    chunks.append(
        {
            "id": completion_id,
            "object": "chat.completion.chunk",
            "model": model,
            "choices": [{"index": 0, "delta": {"role": "assistant", "content": ""}}],
        }
    )

    # Content chunks
    for char in content:
        chunks.append(
            {
                "id": completion_id,
                "object": "chat.completion.chunk",
                "model": model,
                "choices": [{"index": 0, "delta": {"content": char}}],
            }
        )

    # Tool call chunks
    if tool_calls:
        for i, tc in enumerate(tool_calls):
            func = tc.get("function", {})
            name = func.get("name", tc.get("name", "unknown"))
            args = func.get("arguments", {})
            args_str = json.dumps(args) if isinstance(args, dict) else str(args)

            # Tool call header
            chunks.append(
                {
                    "id": completion_id,
                    "object": "chat.completion.chunk",
                    "model": model,
                    "choices": [
                        {
                            "index": 0,
                            "delta": {
                                "tool_calls": [
                                    {
                                        "index": i,
                                        "id": tc.get("id", f"call_{uuid.uuid4().hex[:8]}"),
                                        "type": "function",
                                        "function": {"name": name, "arguments": ""},
                                    }
                                ]
                            },
                        }
                    ],
                }
            )
            # Arguments
            chunks.append(
                {
                    "id": completion_id,
                    "object": "chat.completion.chunk",
                    "model": model,
                    "choices": [
                        {
                            "index": 0,
                            "delta": {
                                "tool_calls": [{"index": i, "function": {"arguments": args_str}}]
                            },
                        }
                    ],
                }
            )

    # Final chunk
    final: dict[str, Any] = {
        "id": completion_id,
        "object": "chat.completion.chunk",
        "model": model,
        "choices": [{"index": 0, "delta": {}, "finish_reason": finish_reason}],
    }
    if include_usage:
        final["usage"] = {
            "prompt_tokens": 10,
            "completion_tokens": len(content.split()),
            "total_tokens": 10 + len(content.split()),
        }
    chunks.append(final)

    return chunks


def build_anthropic_message_response(
    content: list[dict[str, Any]] | str,
    *,
    stop_reason: str = "end_turn",
    usage: dict[str, int] | None = None,
    model: str = "fake-model",
    message_id: str | None = None,
) -> dict[str, Any]:
    """Build a valid Anthropic message response.

    Args:
        content: Content blocks or simple text string
        stop_reason: Stop reason (end_turn, max_tokens, tool_use)
        usage: Token usage dict (input_tokens, output_tokens)
        model: Model name
        message_id: Optional message ID

    Returns:
        Complete Anthropic message response dict
    """
    # Normalize content to list of blocks
    if isinstance(content, str):
        content = [{"type": "text", "text": content}]

    return {
        "id": message_id or f"msg_{uuid.uuid4().hex[:16]}",
        "type": "message",
        "role": "assistant",
        "model": model,
        "content": content,
        "stop_reason": stop_reason,
        "stop_sequence": None,
        "usage": usage or {"input_tokens": 10, "output_tokens": 20},
    }


def build_anthropic_stream_events(
    content: list[dict[str, Any]] | str,
    *,
    stop_reason: str = "end_turn",
    usage: dict[str, int] | None = None,
    model: str = "fake-model",
    message_id: str | None = None,
) -> list[dict[str, Any]]:
    """Build sequence of Anthropic SSE events.

    Args:
        content: Content blocks or simple text string
        stop_reason: Stop reason
        usage: Token usage
        model: Model name
        message_id: Optional message ID

    Returns:
        List of Anthropic streaming event dicts
    """
    # Normalize content
    if isinstance(content, str):
        content = [{"type": "text", "text": content}]

    msg_id = message_id or f"msg_{uuid.uuid4().hex[:16]}"
    events: list[dict[str, Any]] = []

    # message_start
    events.append(
        {
            "type": "message_start",
            "message": {
                "id": msg_id,
                "type": "message",
                "role": "assistant",
                "model": model,
                "content": [],
                "stop_reason": None,
                "stop_sequence": None,
                "usage": {
                    "input_tokens": usage.get("input_tokens", 10) if usage else 10,
                    "output_tokens": 0,
                },
            },
        }
    )

    # Content blocks
    for i, block in enumerate(content):
        block_type = block.get("type", "text")

        if block_type == "text":
            # content_block_start
            events.append(
                {
                    "type": "content_block_start",
                    "index": i,
                    "content_block": {"type": "text", "text": ""},
                }
            )
            # Deltas
            text = block.get("text", "")
            for char in text:
                events.append(
                    {
                        "type": "content_block_delta",
                        "index": i,
                        "delta": {"type": "text_delta", "text": char},
                    }
                )
        elif block_type == "tool_use":
            events.append(
                {
                    "type": "content_block_start",
                    "index": i,
                    "content_block": {
                        "type": "tool_use",
                        "id": block.get("id", f"toolu_{uuid.uuid4().hex[:16]}"),
                        "name": block.get("name", "unknown"),
                        "input": {},
                    },
                }
            )
            # Input delta
            input_data = block.get("input", {})
            events.append(
                {
                    "type": "content_block_delta",
                    "index": i,
                    "delta": {"type": "input_json_delta", "partial_json": json.dumps(input_data)},
                }
            )

        # content_block_stop
        events.append({"type": "content_block_stop", "index": i})

    # message_delta
    events.append(
        {
            "type": "message_delta",
            "delta": {"stop_reason": stop_reason, "stop_sequence": None},
            "usage": {"output_tokens": usage.get("output_tokens", 20) if usage else 20},
        }
    )

    # message_stop
    events.append({"type": "message_stop"})

    return events


def build_anthropic_request(
    messages: list[dict[str, Any]],
    *,
    model: str = "test-model",
    max_tokens: int = 1024,
    system: str | list[dict[str, Any]] | None = None,
    tools: list[dict[str, Any]] | None = None,
    tool_choice: dict[str, Any] | str | None = None,
    stream: bool = False,
    temperature: float | None = None,
    top_p: float | None = None,
    stop_sequences: list[str] | None = None,
) -> dict[str, Any]:
    """Build a valid Anthropic messages request.

    Args:
        messages: List of message dicts
        model: Model name
        max_tokens: Maximum tokens to generate
        system: Optional system prompt
        tools: Optional tools list
        tool_choice: Optional tool choice
        stream: Whether to stream
        temperature: Optional temperature
        top_p: Optional top_p
        stop_sequences: Optional stop sequences

    Returns:
        Complete Anthropic messages request dict
    """
    request: dict[str, Any] = {
        "model": model,
        "max_tokens": max_tokens,
        "messages": messages,
    }

    if system is not None:
        request["system"] = system
    if tools:
        request["tools"] = tools
    if tool_choice is not None:
        request["tool_choice"] = tool_choice
    if stream:
        request["stream"] = True
    if temperature is not None:
        request["temperature"] = temperature
    if top_p is not None:
        request["top_p"] = top_p
    if stop_sequences:
        request["stop_sequences"] = stop_sequences

    return request


def build_openai_request(
    messages: list[dict[str, Any]],
    *,
    model: str = "gpt-4",
    max_tokens: int | None = None,
    tools: list[dict[str, Any]] | None = None,
    tool_choice: dict[str, Any] | str | None = None,
    stream: bool = False,
    temperature: float | None = None,
    top_p: float | None = None,
    stop: list[str] | str | None = None,
) -> dict[str, Any]:
    """Build a valid OpenAI chat completions request.

    Args:
        messages: List of message dicts
        model: Model name
        max_tokens: Maximum tokens
        tools: Optional tools
        tool_choice: Optional tool choice
        stream: Whether to stream
        temperature: Optional temperature
        top_p: Optional top_p
        stop: Optional stop sequences

    Returns:
        Complete OpenAI chat completions request dict
    """
    request: dict[str, Any] = {
        "model": model,
        "messages": messages,
    }

    if max_tokens is not None:
        request["max_tokens"] = max_tokens
    if tools:
        request["tools"] = tools
    if tool_choice is not None:
        request["tool_choice"] = tool_choice
    if stream:
        request["stream"] = True
    if temperature is not None:
        request["temperature"] = temperature
    if top_p is not None:
        request["top_p"] = top_p
    if stop:
        request["stop"] = stop

    return request


# =============================================================================
# Responses API Builders
# =============================================================================


def build_responses_request(
    input_: str | list[dict[str, Any]],
    *,
    model: str = "test-model",
    stream: bool = False,
    store: bool = False,
    previous_response_id: str | None = None,
    instructions: str | None = None,
    tools: list[dict[str, Any]] | None = None,
    tool_choice: dict[str, Any] | str | None = None,
    max_output_tokens: int | None = None,
    temperature: float | None = None,
) -> dict[str, Any]:
    """Build a valid Responses API request.

    Args:
        input_: Input text or list of input items
        model: Model name
        stream: Whether to stream
        store: Whether to store response for chaining
        previous_response_id: Previous response ID for conversation chaining
        instructions: System instructions
        tools: Optional tools list
        tool_choice: Optional tool choice
        max_output_tokens: Maximum output tokens
        temperature: Optional temperature

    Returns:
        Complete Responses API request dict
    """
    request: dict[str, Any] = {
        "model": model,
        "input": input_,
    }

    if stream:
        request["stream"] = True
    if store:
        request["store"] = True
    if previous_response_id:
        request["previous_response_id"] = previous_response_id
    if instructions:
        request["instructions"] = instructions
    if tools:
        request["tools"] = tools
    if tool_choice is not None:
        request["tool_choice"] = tool_choice
    if max_output_tokens is not None:
        request["max_output_tokens"] = max_output_tokens
    if temperature is not None:
        request["temperature"] = temperature

    return request


def build_openai_response_with_tags(
    content: str,
    *,
    stream: bool = False,
    finish_reason: str = "stop",
    model: str = "fake-model",
) -> dict[str, Any] | list[dict[str, Any]]:
    """Build OpenAI response with embedded tags for parser testing.

    Use this to test the parser with content containing <think>, <tool_call>, etc.

    Args:
        content: Content with embedded tags (e.g., "<think>Reasoning</think>Answer")
        stream: Whether to return streaming chunks
        finish_reason: Finish reason
        model: Model name

    Returns:
        Complete response dict or list of streaming chunks
    """
    if stream:
        return build_openai_stream_chunks(content, finish_reason=finish_reason, model=model)
    else:
        return build_openai_chat_response(content, finish_reason=finish_reason, model=model)


def build_openai_response_with_reasoning(
    reasoning: str,
    content: str,
    *,
    stream: bool = False,
    finish_reason: str = "stop",
    model: str = "fake-model",
) -> dict[str, Any] | list[dict[str, Any]]:
    """Build OpenAI response with reasoning_content field.

    Some backends return reasoning in a separate field rather than tags.

    Args:
        reasoning: Reasoning content
        content: Main content
        stream: Whether to return streaming chunks
        finish_reason: Finish reason
        model: Model name

    Returns:
        Complete response dict or list of streaming chunks
    """
    if stream:
        # For streaming, need to build chunks with reasoning_content in delta
        chunks = build_openai_stream_chunks(content, finish_reason=finish_reason, model=model)
        # Add reasoning to the initial chunk
        if chunks and "choices" in chunks[0]:
            chunks[0]["choices"][0]["delta"]["reasoning_content"] = reasoning
        return chunks
    else:
        response = build_openai_chat_response(content, finish_reason=finish_reason, model=model)
        response["choices"][0]["message"]["reasoning_content"] = reasoning
        return response


def build_openai_tool_call_response_xml(
    tool_name: str,
    tool_args: dict[str, Any],
    *,
    prefix_content: str = "",
    stream: bool = False,
    finish_reason: str = "stop",
    model: str = "fake-model",
) -> dict[str, Any] | list[dict[str, Any]]:
    """Build OpenAI response with XML-format tool call for parser testing.

    Args:
        tool_name: Name of the tool to call
        tool_args: Arguments dict
        prefix_content: Content before tool call (e.g., thinking)
        stream: Whether to return streaming chunks
        finish_reason: Finish reason
        model: Model name

    Returns:
        Response with XML tool call embedded in content
    """
    # Build XML tool call
    args_xml = "".join(
        f"<arg_key>{k}</arg_key><arg_value>{v}</arg_value>"
        for k, v in tool_args.items()
    )
    tool_xml = f"<tool_call>{tool_name}{args_xml}</tool_call>"
    content = prefix_content + tool_xml

    if stream:
        return build_openai_stream_chunks(content, finish_reason=finish_reason, model=model)
    else:
        return build_openai_chat_response(content, finish_reason=finish_reason, model=model)


def build_openai_tool_call_response_k2(
    tool_name: str,
    tool_args: dict[str, Any],
    *,
    prefix_content: str = "",
    stream: bool = False,
    finish_reason: str = "stop",
    model: str = "fake-model",
) -> dict[str, Any] | list[dict[str, Any]]:
    """Build OpenAI response with K2/JSON-format tool call for parser testing.

    K2 format uses special markers like <|tool_call_begin|> instead of XML tags.

    Args:
        tool_name: Name of the tool to call
        tool_args: Arguments dict
        prefix_content: Content before tool call
        stream: Whether to return streaming chunks
        finish_reason: Finish reason
        model: Model name

    Returns:
        Response with K2-format tool call embedded in content
    """
    # Build K2 format tool call
    args_json = json.dumps(tool_args)
    tool_k2 = f"<|tool_call_begin|>{tool_name}<|tool_call_argument_begin|>{args_json}<|tool_call_end|>"
    content = prefix_content + tool_k2

    if stream:
        return build_openai_stream_chunks(content, finish_reason=finish_reason, model=model)
    else:
        return build_openai_chat_response(content, finish_reason=finish_reason, model=model)
