"""Stream adapter for converting OpenAI Chat Completions SSE to Anthropic Messages SSE.

Converts the OpenAI chat completion streaming format to Anthropic Messages
streaming format with proper event types and lifecycle events.

OpenAI Chat Completion Events:
    data: {"choices":[{"delta":{"role":"assistant"},"index":0}]}
    data: {"choices":[{"delta":{"content":"Hello"},"index":0}]}
    data: {"choices":[{"delta":{"tool_calls":[...]},"index":0}]}
    data: {"choices":[{"delta":{},"finish_reason":"stop","index":0}]}
    data: [DONE]

Anthropic Messages Events:
    event: message_start
    data: {"type":"message_start","message":{...}}

    event: content_block_start
    data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

    event: content_block_delta
    data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}

    event: content_block_stop
    data: {"type":"content_block_stop","index":0}

    event: message_delta
    data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":10}}

    event: message_stop
    data: {"type":"message_stop"}
"""

import json
import logging
import uuid
from typing import Any, AsyncIterator, Optional

from .translator import _convert_stop_reason

logger = logging.getLogger("yallmp-proxy")


class ChatToMessagesStreamAdapter:
    """Converts OpenAI chat completion SSE stream to Anthropic Messages SSE events.

    This adapter maintains state during streaming to:
    - Track content blocks (text and tool_use)
    - Accumulate text and tool arguments
    - Generate proper event sequences
    - Build the final message with usage
    """

    def __init__(
        self,
        message_id: str,
        model: str,
    ):
        """Initialize the stream adapter.

        Args:
            message_id: The message ID to use (e.g., "msg_xxx")
            model: Model name for the response
        """
        self.message_id = message_id
        self.model = model

        # Content block tracking
        self.content_blocks: list[dict[str, Any]] = []
        self.current_text_index: Optional[int] = None
        self.accumulated_text = ""

        # Tool call tracking (by OpenAI tool_call index)
        self.tool_calls: dict[int, dict[str, Any]] = {}
        self.tool_call_block_indices: dict[int, int] = {}  # OpenAI index -> our block index

        # Usage tracking
        self.input_tokens = 0
        self.output_tokens = 0

        # State flags
        self.message_started = False
        self.saw_done = False
        self.finish_reason: Optional[str] = None

    async def adapt_stream(
        self,
        chat_stream: AsyncIterator[bytes],
    ) -> AsyncIterator[bytes]:
        """Transform OpenAI chat completion stream to Anthropic Messages SSE events.

        Args:
            chat_stream: The incoming OpenAI chat completion SSE stream

        Yields:
            Anthropic Messages API SSE events as bytes
        """
        async for chunk in chat_stream:
            async for event in self._process_chunk(chunk):
                yield event

        # Emit terminal events
        async for event in self._emit_terminal_events():
            yield event

    async def _process_chunk(self, chunk: bytes) -> AsyncIterator[bytes]:
        """Process a single chunk from the OpenAI chat completion stream.

        Args:
            chunk: Raw bytes from the stream

        Yields:
            Anthropic Messages SSE events
        """
        text = chunk.decode("utf-8", errors="replace")

        for line in text.split("\n"):
            line = line.strip()

            if not line or not line.startswith("data:"):
                continue

            data_str = line[5:].strip()

            if data_str == "[DONE]":
                self.saw_done = True
                continue

            try:
                data = json.loads(data_str)
            except json.JSONDecodeError:
                logger.debug(f"MessagesStreamAdapter: Failed to parse: {data_str[:100]}")
                continue

            async for event in self._process_chat_event(data):
                yield event

    async def _process_chat_event(self, data: dict[str, Any]) -> AsyncIterator[bytes]:
        """Process a parsed OpenAI chat completion event.

        Args:
            data: Parsed JSON from the chat completion stream

        Yields:
            Anthropic Messages SSE events
        """
        choices = data.get("choices", [])

        for choice in choices:
            delta = choice.get("delta", {})
            finish_reason = choice.get("finish_reason")

            # Emit message_start on first meaningful content
            if not self.message_started:
                if delta.get("role") or delta.get("content") or delta.get("tool_calls"):
                    yield self._emit_message_start()
                    self.message_started = True

            # Handle text content
            content = delta.get("content")
            if content is not None:
                # Start text block if needed
                if self.current_text_index is None:
                    self.current_text_index = len(self.content_blocks)
                    self.content_blocks.append({"type": "text", "text": ""})
                    yield self._emit_content_block_start(
                        self.current_text_index,
                        {"type": "text", "text": ""}
                    )

                # Emit text delta
                if content:
                    self.accumulated_text += content
                    yield self._emit_content_block_delta(
                        self.current_text_index,
                        {"type": "text_delta", "text": content}
                    )

            # Handle tool calls
            tool_calls = delta.get("tool_calls", [])
            for tc in tool_calls:
                async for event in self._process_tool_call_delta(tc):
                    yield event

            # Handle finish reason
            if finish_reason:
                self.finish_reason = finish_reason

        # Extract usage if present
        usage = data.get("usage")
        if usage:
            self.input_tokens = usage.get("prompt_tokens", 0)
            self.output_tokens = usage.get("completion_tokens", 0)

    async def _process_tool_call_delta(
        self,
        tc: dict[str, Any],
    ) -> AsyncIterator[bytes]:
        """Process an OpenAI tool call delta.

        Args:
            tc: Tool call delta object from OpenAI

        Yields:
            Anthropic Messages SSE events
        """
        tc_index = tc.get("index", 0)

        # Initialize tracking for this tool call
        if tc_index not in self.tool_calls:
            # First, close any open text block
            if self.current_text_index is not None:
                yield self._emit_content_block_stop(self.current_text_index)
                # Update the text in content_blocks
                if self.current_text_index < len(self.content_blocks):
                    self.content_blocks[self.current_text_index]["text"] = self.accumulated_text
                self.current_text_index = None

            # New tool call - extract info from first chunk
            tool_id = tc.get("id", f"toolu_{uuid.uuid4().hex[:12]}")
            function = tc.get("function", {})
            tool_name = function.get("name", "")
            block_index = len(self.content_blocks)

            self.tool_calls[tc_index] = {
                "id": tool_id,
                "name": tool_name,
                "arguments": "",
            }
            self.tool_call_block_indices[tc_index] = block_index
            self.content_blocks.append({
                "type": "tool_use",
                "id": tool_id,
                "name": tool_name,
                "input": {}
            })

            # Emit content_block_start for tool_use with name if available
            yield self._emit_content_block_start(
                block_index,
                {"type": "tool_use", "id": tool_id, "name": tool_name, "input": {}}
            )

            # Handle initial arguments if present
            if "arguments" in function:
                args_delta = function["arguments"]
                self.tool_calls[tc_index]["arguments"] += args_delta
                yield self._emit_content_block_delta(
                    block_index,
                    {"type": "input_json_delta", "partial_json": args_delta}
                )
            return

        tc_data = self.tool_calls[tc_index]
        block_index = self.tool_call_block_indices[tc_index]

        # Handle function info
        function = tc.get("function", {})

        # Handle function name (for subsequent chunks)
        if "name" in function:
            tc_data["name"] = function["name"]
            # Update content_blocks
            if block_index < len(self.content_blocks):
                self.content_blocks[block_index]["name"] = function["name"]

        # Handle arguments delta
        if "arguments" in function:
            args_delta = function["arguments"]
            tc_data["arguments"] += args_delta

            # Emit input_json_delta
            yield self._emit_content_block_delta(
                block_index,
                {"type": "input_json_delta", "partial_json": args_delta}
            )

    async def _emit_terminal_events(self) -> AsyncIterator[bytes]:
        """Emit the terminal events for the stream.

        Yields:
            Final Anthropic Messages SSE events
        """
        # If we never started, emit an empty message
        if not self.message_started:
            yield self._emit_message_start()
            self.message_started = True

        # Close any open text block
        if self.current_text_index is not None:
            if self.current_text_index < len(self.content_blocks):
                self.content_blocks[self.current_text_index]["text"] = self.accumulated_text
            yield self._emit_content_block_stop(self.current_text_index)
            self.current_text_index = None

        # Close any tool_use blocks
        for tc_index, block_index in sorted(self.tool_call_block_indices.items()):
            tc_data = self.tool_calls[tc_index]

            # Parse and update the input
            try:
                input_dict = json.loads(tc_data["arguments"]) if tc_data["arguments"] else {}
            except json.JSONDecodeError:
                input_dict = {"raw": tc_data["arguments"]}

            if block_index < len(self.content_blocks):
                self.content_blocks[block_index]["input"] = input_dict

            yield self._emit_content_block_stop(block_index)

        # Emit message_delta with stop_reason and usage
        stop_reason = _convert_stop_reason(self.finish_reason)
        yield self._emit_message_delta(stop_reason)

        # Emit message_stop
        yield self._emit_message_stop()

    def _emit_message_start(self) -> bytes:
        """Emit the message_start event.

        Returns:
            SSE formatted bytes
        """
        message = {
            "id": self.message_id,
            "type": "message",
            "role": "assistant",
            "content": [],
            "model": self.model,
            "stop_reason": None,
            "stop_sequence": None,
            "usage": {"input_tokens": self.input_tokens, "output_tokens": 0}
        }

        event_data = {"type": "message_start", "message": message}
        return self._format_sse_event("message_start", event_data)

    def _emit_content_block_start(
        self,
        index: int,
        content_block: dict[str, Any],
    ) -> bytes:
        """Emit a content_block_start event.

        Args:
            index: Content block index
            content_block: The content block data

        Returns:
            SSE formatted bytes
        """
        event_data = {
            "type": "content_block_start",
            "index": index,
            "content_block": content_block,
        }
        return self._format_sse_event("content_block_start", event_data)

    def _emit_content_block_delta(
        self,
        index: int,
        delta: dict[str, Any],
    ) -> bytes:
        """Emit a content_block_delta event.

        Args:
            index: Content block index
            delta: The delta data

        Returns:
            SSE formatted bytes
        """
        event_data = {
            "type": "content_block_delta",
            "index": index,
            "delta": delta,
        }
        return self._format_sse_event("content_block_delta", event_data)

    def _emit_content_block_stop(self, index: int) -> bytes:
        """Emit a content_block_stop event.

        Args:
            index: Content block index

        Returns:
            SSE formatted bytes
        """
        event_data = {"type": "content_block_stop", "index": index}
        return self._format_sse_event("content_block_stop", event_data)

    def _emit_message_delta(self, stop_reason: str) -> bytes:
        """Emit the message_delta event.

        Args:
            stop_reason: The stop reason

        Returns:
            SSE formatted bytes
        """
        event_data = {
            "type": "message_delta",
            "delta": {"stop_reason": stop_reason, "stop_sequence": None},
            "usage": {"output_tokens": self.output_tokens},
        }
        return self._format_sse_event("message_delta", event_data)

    def _emit_message_stop(self) -> bytes:
        """Emit the message_stop event.

        Returns:
            SSE formatted bytes
        """
        event_data = {"type": "message_stop"}
        return self._format_sse_event("message_stop", event_data)

    def _format_sse_event(self, event_type: str, data: dict[str, Any]) -> bytes:
        """Format an SSE event.

        Args:
            event_type: Event type name
            data: Event data

        Returns:
            SSE formatted bytes
        """
        json_str = json.dumps(data, ensure_ascii=False)
        return f"event: {event_type}\ndata: {json_str}\n\n".encode("utf-8")

    def build_final_message(self) -> dict[str, Any]:
        """Build the final message object (for non-streaming fallback).

        Returns:
            Complete Anthropic message object
        """
        stop_reason = _convert_stop_reason(self.finish_reason)

        # Update text block if present
        if self.current_text_index is not None and self.current_text_index < len(self.content_blocks):
            self.content_blocks[self.current_text_index]["text"] = self.accumulated_text

        # Parse tool arguments
        for tc_index, block_index in self.tool_call_block_indices.items():
            tc_data = self.tool_calls[tc_index]
            try:
                input_dict = json.loads(tc_data["arguments"]) if tc_data["arguments"] else {}
            except json.JSONDecodeError:
                input_dict = {"raw": tc_data["arguments"]}

            if block_index < len(self.content_blocks):
                self.content_blocks[block_index]["input"] = input_dict

        # Ensure at least one content block
        content = self.content_blocks if self.content_blocks else [{"type": "text", "text": ""}]

        return {
            "id": self.message_id,
            "type": "message",
            "role": "assistant",
            "content": content,
            "model": self.model,
            "stop_reason": stop_reason,
            "stop_sequence": None,
            "usage": {
                "input_tokens": self.input_tokens,
                "output_tokens": self.output_tokens,
            },
        }


async def adapt_chat_stream_to_messages(
    message_id: str,
    model: str,
    chat_stream: AsyncIterator[bytes],
) -> AsyncIterator[bytes]:
    """Convenience function to adapt an OpenAI chat stream to Anthropic Messages.

    Args:
        message_id: Message ID for the response
        model: Model name
        chat_stream: Input OpenAI chat completion stream

    Yields:
        Anthropic Messages API SSE events
    """
    adapter = ChatToMessagesStreamAdapter(message_id, model)
    async for event in adapter.adapt_stream(chat_stream):
        yield event
