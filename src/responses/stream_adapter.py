"""Stream adapter for converting Chat Completions SSE to Responses API events.

Converts the chat completion streaming format to the Open Responses streaming
format with proper event types, sequence numbers, and lifecycle events.

Chat Completion Events:
    data: {"choices":[{"delta":{"role":"assistant"},"index":0}]}
    data: {"choices":[{"delta":{"content":"Hello"},"index":0}]}
    data: [DONE]

Responses API Events:
    event: response.created
    data: {"type":"response.created","response":{...}}

    event: response.output_text.delta
    data: {"type":"response.output_text.delta","delta":"Hello",...}

    event: response.completed
    data: {"type":"response.completed","response":{...}}
"""

import json
import logging
import time
from typing import Any, AsyncIterator, Optional
from uuid import uuid4

from ..types.responses import (
    ResponseObject,
    OutputItem,
    MessageItem,
    FunctionCallItem,
    OutputText,
    ResponseUsage,
    EVENT_RESPONSE_CREATED,
    EVENT_RESPONSE_IN_PROGRESS,
    EVENT_RESPONSE_COMPLETED,
    EVENT_RESPONSE_FAILED,
    EVENT_OUTPUT_ITEM_ADDED,
    EVENT_CONTENT_PART_ADDED,
    EVENT_OUTPUT_TEXT_DELTA,
    EVENT_FUNCTION_CALL_ARGS_DELTA,
    EVENT_CONTENT_PART_DONE,
    EVENT_OUTPUT_ITEM_DONE,
)
from .translator import convert_usage, generate_message_id, generate_call_id

logger = logging.getLogger("yallmp-proxy")


class ChatToResponsesStreamAdapter:
    """Converts chat completion SSE stream to Responses API events.

    This adapter maintains state during streaming to:
    - Track output items and content parts
    - Accumulate text for final done events
    - Generate proper sequence numbers
    - Build the final response object
    """

    def __init__(
        self,
        response_id: str,
        model: str,
        original_request: Optional[dict[str, Any]] = None,
    ):
        """Initialize the stream adapter.

        Args:
            response_id: The response ID to use
            model: Model name
            original_request: The original request for echoing config
        """
        self.response_id = response_id
        self.model = model
        self.original_request = original_request or {}
        self.created_at = time.time()

        # Sequence tracking
        self.sequence_number = 0

        # Output tracking
        self.output_items: list[OutputItem] = []
        self.current_output_index = -1
        self.current_content_index = -1

        # Current item state
        self.current_item_id: Optional[str] = None
        self.current_item_type: Optional[str] = None
        self.accumulated_text = ""

        # Tool call tracking
        self.tool_calls: dict[int, dict[str, Any]] = {}  # index -> partial tool call

        # Usage tracking (from final chunk)
        self.usage: Optional[ResponseUsage] = None

        # Flags
        self.first_content = True
        self.finished = False

    async def adapt_stream(
        self,
        chat_stream: AsyncIterator[bytes],
    ) -> AsyncIterator[bytes]:
        """Transform chat completion stream to Responses API events.

        Args:
            chat_stream: The incoming chat completion SSE stream

        Yields:
            Responses API SSE events as bytes
        """
        # Emit initial lifecycle events
        yield self._emit_event(
            EVENT_RESPONSE_CREATED,
            {"response": self._build_response_object("in_progress")}
        )
        yield self._emit_event(
            EVENT_RESPONSE_IN_PROGRESS,
            {"response": self._build_response_object("in_progress")}
        )

        async for chunk in chat_stream:
            async for event in self._process_chunk(chunk):
                yield event

        # Emit completion events
        async for event in self._emit_completion_events():
            yield event

    async def _process_chunk(self, chunk: bytes) -> AsyncIterator[bytes]:
        """Process a single chunk from the chat completion stream.

        Args:
            chunk: Raw bytes from the stream

        Yields:
            Responses API events
        """
        # Parse SSE events from the chunk
        text = chunk.decode("utf-8", errors="replace")

        for line in text.split("\n"):
            line = line.strip()

            # Skip empty lines and non-data lines
            if not line or not line.startswith("data:"):
                continue

            data_str = line[5:].strip()

            # Handle [DONE] sentinel
            if data_str == "[DONE]":
                self.finished = True
                continue

            # Parse JSON
            try:
                data = json.loads(data_str)
            except json.JSONDecodeError:
                logger.debug(f"StreamAdapter: Failed to parse: {data_str[:100]}")
                continue

            # Process the chat completion chunk
            async for event in self._process_chat_event(data):
                yield event

    async def _process_chat_event(self, data: dict[str, Any]) -> AsyncIterator[bytes]:
        """Process a parsed chat completion event.

        Args:
            data: Parsed JSON from the chat completion stream

        Yields:
            Responses API events
        """
        choices = data.get("choices", [])

        for choice in choices:
            delta = choice.get("delta", {})
            finish_reason = choice.get("finish_reason")
            index = choice.get("index", 0)

            # Handle role (first chunk with content)
            if delta.get("role") == "assistant" and self.first_content:
                self.first_content = False
                self.current_output_index = 0
                self.current_content_index = 0
                self.current_item_id = generate_message_id()
                self.current_item_type = "message"

                # Emit output_item.added
                yield self._emit_event(EVENT_OUTPUT_ITEM_ADDED, {
                    "output_index": self.current_output_index,
                    "item": {
                        "type": "message",
                        "id": self.current_item_id,
                        "role": "assistant",
                        "status": "in_progress",
                        "content": [],
                    },
                })

                # Emit content_part.added
                yield self._emit_event(EVENT_CONTENT_PART_ADDED, {
                    "item_id": self.current_item_id,
                    "output_index": self.current_output_index,
                    "content_index": self.current_content_index,
                    "part": {
                        "type": "output_text",
                        "text": "",
                        "annotations": [],
                    },
                })

            # Handle content delta
            content = delta.get("content")
            if content:
                # Ensure we have an item started
                if self.first_content:
                    self.first_content = False
                    self.current_output_index = 0
                    self.current_content_index = 0
                    self.current_item_id = generate_message_id()
                    self.current_item_type = "message"

                    yield self._emit_event(EVENT_OUTPUT_ITEM_ADDED, {
                        "output_index": self.current_output_index,
                        "item": {
                            "type": "message",
                            "id": self.current_item_id,
                            "role": "assistant",
                            "status": "in_progress",
                            "content": [],
                        },
                    })

                    yield self._emit_event(EVENT_CONTENT_PART_ADDED, {
                        "item_id": self.current_item_id,
                        "output_index": self.current_output_index,
                        "content_index": self.current_content_index,
                        "part": {
                            "type": "output_text",
                            "text": "",
                            "annotations": [],
                        },
                    })

                self.accumulated_text += content

                # Emit text delta
                yield self._emit_event(EVENT_OUTPUT_TEXT_DELTA, {
                    "item_id": self.current_item_id,
                    "output_index": self.current_output_index,
                    "content_index": self.current_content_index,
                    "delta": content,
                })

            # Handle tool calls
            tool_calls = delta.get("tool_calls", [])
            for tc in tool_calls:
                async for event in self._process_tool_call_delta(tc):
                    yield event

            # Handle finish reason
            if finish_reason:
                async for event in self._finalize_current_item(finish_reason):
                    yield event

        # Extract usage from the chunk (usually in final chunk)
        usage = data.get("usage")
        if usage:
            self.usage = convert_usage(usage)

    async def _process_tool_call_delta(
        self,
        tc: dict[str, Any],
    ) -> AsyncIterator[bytes]:
        """Process a tool call delta.

        Args:
            tc: Tool call delta object

        Yields:
            Responses API events
        """
        tc_index = tc.get("index", 0)

        # Initialize tracking for this tool call
        if tc_index not in self.tool_calls:
            # New tool call
            self.tool_calls[tc_index] = {
                "id": tc.get("id", generate_call_id()),
                "type": tc.get("type", "function"),
                "function_name": "",
                "arguments": "",
                "output_index": len(self.output_items),
            }

            # Emit output_item.added for function_call
            item_id = self.tool_calls[tc_index]["id"]
            output_index = self.tool_calls[tc_index]["output_index"]

            yield self._emit_event(EVENT_OUTPUT_ITEM_ADDED, {
                "output_index": output_index,
                "item": {
                    "type": "function_call",
                    "id": item_id,
                    "call_id": item_id,
                    "name": "",
                    "arguments": "",
                    "status": "in_progress",
                },
            })

        tc_data = self.tool_calls[tc_index]

        # Handle function info
        function = tc.get("function", {})

        # Handle function name
        if "name" in function:
            tc_data["function_name"] = function["name"]

        # Handle arguments delta
        if "arguments" in function:
            args_delta = function["arguments"]
            tc_data["arguments"] += args_delta

            yield self._emit_event(EVENT_FUNCTION_CALL_ARGS_DELTA, {
                "item_id": tc_data["id"],
                "output_index": tc_data["output_index"],
                "call_id": tc_data["id"],
                "delta": args_delta,
            })

    async def _finalize_current_item(
        self,
        finish_reason: str,
    ) -> AsyncIterator[bytes]:
        """Finalize the current item with done events.

        Args:
            finish_reason: The finish reason from chat completions

        Yields:
            Responses API done events
        """
        # Finalize message content if we have one
        if self.current_item_id and self.current_item_type == "message":
            # content_part.done
            yield self._emit_event(EVENT_CONTENT_PART_DONE, {
                "item_id": self.current_item_id,
                "output_index": self.current_output_index,
                "content_index": self.current_content_index,
                "part": {
                    "type": "output_text",
                    "text": self.accumulated_text,
                    "annotations": [],
                },
            })

            # output_item.done
            message_item: MessageItem = {
                "id": self.current_item_id,
                "type": "message",
                "role": "assistant",
                "status": "completed",
                "content": [{
                    "type": "output_text",
                    "text": self.accumulated_text,
                    "annotations": [],
                }],
            }
            self.output_items.append(message_item)

            yield self._emit_event(EVENT_OUTPUT_ITEM_DONE, {
                "output_index": self.current_output_index,
                "item": message_item,
            })

        # Finalize any tool calls
        for tc_index, tc_data in sorted(self.tool_calls.items()):
            function_call_item: FunctionCallItem = {
                "id": tc_data["id"],
                "type": "function_call",
                "call_id": tc_data["id"],
                "name": tc_data["function_name"],
                "arguments": tc_data["arguments"],
                "status": "completed",
            }
            self.output_items.append(function_call_item)

            yield self._emit_event(EVENT_OUTPUT_ITEM_DONE, {
                "output_index": tc_data["output_index"],
                "item": function_call_item,
            })

    async def _emit_completion_events(self) -> AsyncIterator[bytes]:
        """Emit the final completion event.

        Yields:
            response.completed event
        """
        yield self._emit_event(
            EVENT_RESPONSE_COMPLETED,
            {"response": self._build_response_object("completed")}
        )

    def _emit_event(self, event_type: str, data: dict[str, Any]) -> bytes:
        """Emit a single SSE event.

        Args:
            event_type: The event type string
            data: The event data

        Returns:
            SSE formatted bytes
        """
        self.sequence_number += 1
        payload = {
            "type": event_type,
            "sequence_number": self.sequence_number,
            **data,
        }
        json_str = json.dumps(payload, ensure_ascii=False)
        return f"event: {event_type}\ndata: {json_str}\n\n".encode("utf-8")

    def _build_response_object(self, status: str) -> ResponseObject:
        """Build the response object for lifecycle events.

        Args:
            status: Current response status

        Returns:
            Response object
        """
        now = time.time()
        response: ResponseObject = {
            "id": self.response_id,
            "object": "response",
            "created_at": self.created_at,
            "status": status,
            "model": self.model,
            "previous_response_id": self.original_request.get("previous_response_id"),
            "output": self.output_items,
            "output_text": self.accumulated_text,
            "error": None,
            "incomplete_details": None,
            "metadata": self.original_request.get("metadata", {}),
        }

        if status == "completed":
            response["completed_at"] = now
            if self.usage:
                response["usage"] = self.usage

        # Echo back configuration
        for key in ["temperature", "top_p", "max_output_tokens", "tools", "tool_choice"]:
            if key in self.original_request:
                response[key] = self.original_request[key]

        return response

    def build_final_response(self) -> ResponseObject:
        """Build the final response object for storage.

        Returns:
            Complete response object
        """
        return self._build_response_object("completed")


async def adapt_chat_stream_to_responses(
    response_id: str,
    model: str,
    chat_stream: AsyncIterator[bytes],
    original_request: Optional[dict[str, Any]] = None,
) -> AsyncIterator[bytes]:
    """Convenience function to adapt a chat stream.

    Args:
        response_id: Response ID
        model: Model name
        chat_stream: Input chat completion stream
        original_request: Original request for config echoing

    Yields:
        Responses API SSE events
    """
    adapter = ChatToResponsesStreamAdapter(response_id, model, original_request)
    async for event in adapter.adapt_stream(chat_stream):
        yield event
