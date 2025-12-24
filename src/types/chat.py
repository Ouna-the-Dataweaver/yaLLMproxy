"""Types for chat representation across different LLM providers.

This module defines type schemas for chat completions, streaming responses,
and tool/function calling. Types are separated into:
- OpenAI-compatible types: Used for internal state and OpenAI-compatible APIs
- Anthropic types: Used for parsing Anthropic Claude API responses
"""

from typing import Any
from typing_extensions import TypedDict


# =============================================================================
# OpenAI-Compatible Types
# =============================================================================
# These types follow the OpenAI API format and are used for:
# - Internal state storage
# - OpenAI API requests/responses
# - Any OpenAI-compatible provider (Groq, Together, etc.)


class FunctionCall(TypedDict, total=False):
    """A function call within a tool call (OpenAI format).
    
    Attributes:
        name: Name of the function to call. Can be None for streamed 
            follow-up chunks where the name was already stated.
        arguments: JSON string containing the arguments to pass to the 
            function. Can be None for streamed chunks where arguments 
            are incrementally built.
    """
    name: str | None
    arguments: str | None


class ToolCall(TypedDict, total=False):
    """A tool call in a chat response (OpenAI format).
    
    The tool call indicates the model wants to invoke a tool/function.
    
    Attributes:
        id: Unique identifier for this tool call. Used to match with 
            tool results in subsequent requests.
        type: Type of tool call. Typically "function".
        function: The function to call with its arguments.
        index: Index for streaming, tracks position in tool_calls array.
    """
    id: str
    type: str
    function: FunctionCall
    index: int


class ContentPart(TypedDict, total=False):
    """A content part for multi-modal messages (OpenAI format).
    
    Used for messages containing text, images, or audio.
    
    Attributes:
        type: Type of content part:
            - "text": Plain text content
            - "image_url": Image from URL or base64
            - "input_audio": Audio input
        text: Text content (for "text" type).
        image_url: Image URL object (for "image_url" type).
            Contains "url" and optionally "detail" fields.
        input_audio: Audio data object (for "input_audio" type).
            Contains "data" (base64) and "format" fields.
    """
    type: str
    text: str | None
    image_url: dict[str, Any] | None
    input_audio: dict[str, Any] | None


class ChatMessage(TypedDict, total=False):
    """A message in a chat conversation (OpenAI format).
    
    This is the primary message type for internal state storage.
    
    Attributes:
        role: Role of the message sender:
            - "user": User message
            - "assistant": Model response
            - "system": System instruction
            - "tool": Tool result
            - "function": Legacy name for tool results
        content: Text content of the message. Can be:
            - A string (simple text messages)
            - An array of ContentPart for multi-modal inputs
            - None when only tool_calls is present
        name: Optional name for the speaker.
        tool_calls: Array of tool calls from the assistant.
        tool_call_id: ID referencing a previous tool call (for tool results).
    """
    role: str
    content: str | list[ContentPart] | None
    name: str | None
    tool_calls: list[ToolCall] | None
    tool_call_id: str | None


class Delta(TypedDict, total=False):
    """A streamed delta of a choice in a chat completion (OpenAI format).
    
    Represents incremental updates to the assistant's response.
    
    Attributes:
        role: Role indicator, typically "assistant" for first chunk.
        content: Incremental text content. Accumulates to form the 
            complete response over multiple chunks.
        tool_calls: List of tool calls being built. May be partial
            and updated across multiple chunks.
        audio: Audio data if audio output is enabled.
    """
    role: str | None
    content: str | None
    tool_calls: list[ToolCall] | None
    audio: dict[str, Any] | None


class Choice(TypedDict, total=False):
    """A choice in a chat completion response (OpenAI format).
    
    Attributes:
        index: Zero-based index of this choice in the choices array.
        delta: The incremental content for streaming responses.
        message: The complete message for non-streaming responses.
        finish_reason: Reason why the model stopped generating:
            - "stop": Model hit a natural stopping point
            - "length": Hit max tokens limit
            - "tool_calls": Model requested tool calls
            - "content_filter": Content was filtered
            - "function_call": Legacy name for tool calls
        logprobs: Log probability information.
    """
    index: int
    delta: Delta | None
    message: ChatMessage | None
    finish_reason: str | None
    logprobs: dict[str, Any] | None
    


class Usage(TypedDict, total=False):
    """Token usage information from a completion response (OpenAI format).
    
    Attributes:
        prompt_tokens: Number of tokens in the prompt.
        completion_tokens: Number of tokens in the completion.
        total_tokens: Total number of tokens used.
        prompt_tokens_details: Breakdown of prompt tokens.
        completion_tokens_details: Breakdown of completion tokens.
    """
    prompt_tokens: int
    completion_tokens: int
    total_tokens: int
    prompt_tokens_details: dict[str, int] | None
    completion_tokens_details: dict[str, int] | None


class ChatCompletionChunk(TypedDict, total=False):
    """A streamed chunk of a chat completion response (OpenAI format).
    
    Attributes:
        id: Unique identifier for this completion.
        object: Object type, typically "chat.completion.chunk".
        created: Unix timestamp of when the chunk was created.
        model: Name of the model that generated this response.
        choices: Array of choice deltas.
        usage: Token usage information (may be in final chunk only).
        system_fingerprint: Server-side fingerprint for debugging.
        service_tier: Service tier information.
    """
    id: str
    object: str
    created: int
    model: str
    choices: list[Choice]
    usage: Usage | None
    system_fingerprint: str | None
    service_tier: str | None


class ChatCompletionResponse(TypedDict, total=False):
    """A complete (non-streaming) chat completion response (OpenAI format).
    
    Attributes:
        id: Unique identifier for this completion.
        object: Object type, typically "chat.completion".
        created: Unix timestamp of when the response was created.
        model: Name of the model that generated this response.
        choices: Array of completion choices.
        usage: Token usage information.
        system_fingerprint: Server-side fingerprint for debugging.
        service_tier: Service tier information.
    """
    id: str
    object: str
    created: int
    model: str
    choices: list[Choice]
    usage: Usage | None
    system_fingerprint: str | None
    service_tier: str | None


class Chat(TypedDict, total=False):
    """Chat state object representing a conversation.
    
    Used for application-level chat management and internal state.
    
    Attributes:
        messages: List of messages in the conversation, in order.
        metadata: Optional metadata about the chat (created_at, 
            model, temperature, etc.).
    """
    messages: list[ChatMessage]
    metadata: dict[str, Any] | None


# =============================================================================
# Anthropic Types
# =============================================================================
# These types are used for parsing Anthropic Claude API responses. (v1/messages)
# Convert to OpenAI-compatible types before storing internally.


class AnthropicContentBlock(TypedDict, total=False):
    """A content block in an Anthropic Claude message.
    
    Attributes:
        type: Type of content block:
            - "text": Plain text content
            - "thinking": Claude's thinking/thought process
            - "tool_use": Request to use a tool
            - "tool_result": Result from a tool
            - "image": Image content (base64 or URL)
        text: Text content (for "text" blocks).
        thinking: Thinking content (for "thinking" blocks).
        id: Block identifier (for "tool_use" blocks).
        name: Tool name (for "tool_use" blocks).
        input: Tool input/arguments (for "tool_use" blocks).
        content: Result content (for "tool_result" blocks).
        is_error: Whether the tool result was an error.
    """
    type: str
    text: str | None
    thinking: str | None
    id: str | None
    name: str | None
    input: dict[str, Any] | None
    content: str | None
    is_error: bool | None


class AnthropicMessage(TypedDict, total=False):
    """A message in Anthropic Claude format.
    
    Attributes:
        id: Unique message identifier.
        type: Object type, typically "message".
        role: Role of the message sender ("user" or "assistant").
        content: Array of content blocks.
        model: Model that generated the response.
        stop_reason: Why generation stopped:
            - "end_turn": Natural stopping point
            - "max_tokens": Hit token limit
            - "tool_use": Model wants to use a tool
            - "stop_sequence": Hit a stop sequence
        stop_sequence: The stop sequence that was hit, if any.
        usage: Token usage information.
    """
    id: str
    type: str
    role: str
    content: list[AnthropicContentBlock]
    model: str
    stop_reason: str | None
    stop_sequence: str | None
    usage: "AnthropicUsage"


class AnthropicUsage(TypedDict, total=False):
    """Token usage information from Anthropic API.
    
    Attributes:
        input_tokens: Number of tokens in the input.
        output_tokens: Number of tokens in the output.
        cache_creation_input_tokens: Tokens created for prompt cache.
        cache_read_input_tokens: Tokens read from prompt cache.
    """
    input_tokens: int
    output_tokens: int
    cache_creation_input_tokens: int | None
    cache_read_input_tokens: int | None


class AnthropicStreamEvent(TypedDict, total=False):
    """A streaming event from Anthropic Claude API.
    
    Attributes:
        type: Event type:
            - "message_start": Start of message
            - "content_block_start": Start of content block
            - "content_block_delta": Content block update
            - "content_block_stop": End of content block
            - "message_delta": Message-level update
            - "message_stop": End of message
        index: Index of the content block (for block events).
        message: Message object (for "message_start").
        content_block: Content block (for "content_block_start").
        delta: Delta update (for delta events).
        usage: Usage information (for "message_delta").
    """
    type: str
    index: int | None
    message: AnthropicMessage | None
    content_block: AnthropicContentBlock | None
    delta: dict[str, Any] | None
    usage: AnthropicUsage | None
