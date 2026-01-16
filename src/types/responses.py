"""Types for the Open Responses API specification.

Based on the Open Responses specification (https://www.openresponses.org/).
These types define the request/response format for the /v1/responses endpoint,
supporting both pass-through mode (native backends) and simulation mode
(translation to/from chat completions).
"""

from typing import Any, Literal, Union
from typing_extensions import TypedDict


# =============================================================================
# Role Types
# =============================================================================

Role = Literal["user", "assistant", "system", "developer"]
"""Valid roles in the Responses API.

- user: User messages
- assistant: Model responses
- system: System instructions (deprecated, use developer)
- developer: Developer/system instructions
"""


# =============================================================================
# Status Types
# =============================================================================

ItemStatus = Literal["in_progress", "completed", "incomplete"]
"""Status lifecycle for output items.

- in_progress: Model actively emitting content
- completed: Finished sampling (terminal)
- incomplete: Token budget exhausted (terminal)
"""

ResponseStatus = Literal["in_progress", "completed", "failed", "incomplete", "queued"]
"""Status lifecycle for the overall response.

- queued: Background mode, waiting to start
- in_progress: Model processing
- completed: Finished successfully
- failed: Error occurred
- incomplete: Token budget exhausted
"""


# =============================================================================
# Input Content Types
# =============================================================================

class InputText(TypedDict):
    """Plain text input content."""
    type: Literal["input_text"]
    text: str


class InputImage(TypedDict, total=False):
    """Image input content."""
    type: Literal["input_image"]
    image_url: str  # URL or base64
    detail: Literal["low", "high", "auto"]


class InputFile(TypedDict, total=False):
    """File input content."""
    type: Literal["input_file"]
    filename: str
    file_data: str  # base64
    file_id: str  # Alternative: reference to uploaded file


class InputVideo(TypedDict, total=False):
    """Video input content."""
    type: Literal["input_video"]
    video_url: str


InputContent = Union[InputText, InputImage, InputFile, InputVideo]
"""Union of all input content types."""


# =============================================================================
# Output Content Types
# =============================================================================

class UrlCitation(TypedDict, total=False):
    """URL citation annotation."""
    type: Literal["url_citation"]
    start_index: int
    end_index: int
    url: str
    title: str


class OutputText(TypedDict, total=False):
    """Text output content."""
    type: Literal["output_text"]
    text: str
    annotations: list[UrlCitation]


class Refusal(TypedDict):
    """Model refusal content."""
    type: Literal["refusal"]
    refusal: str


class SummaryText(TypedDict):
    """Reasoning summary content."""
    type: Literal["summary_text"]
    text: str


class ReasoningText(TypedDict):
    """Intermediate reasoning content."""
    type: Literal["reasoning_text"]
    text: str


OutputContent = Union[OutputText, Refusal, SummaryText, ReasoningText]
"""Union of all output content types."""


# =============================================================================
# Item Types (all have id, type, status)
# =============================================================================

class MessageItem(TypedDict, total=False):
    """A message item in input or output.

    All items must have id, type, and status fields.
    """
    id: str
    type: Literal["message"]
    role: Role
    status: ItemStatus
    content: list[OutputContent]


class FunctionCallItem(TypedDict, total=False):
    """A function/tool call item in output.

    Represents a request to invoke an external tool.
    """
    id: str
    type: Literal["function_call"]
    call_id: str
    name: str
    arguments: str  # JSON string
    status: ItemStatus


class FunctionCallOutputItem(TypedDict, total=False):
    """A function call result item in input.

    Provides the result of a previously requested function call.
    """
    type: Literal["function_call_output"]
    call_id: str
    output: str


class ReasoningItem(TypedDict, total=False):
    """A reasoning item in output.

    Contains the model's reasoning process.
    """
    id: str
    type: Literal["reasoning"]
    content: str
    summary: str
    encrypted_content: str  # For cache rehydration


OutputItem = Union[MessageItem, FunctionCallItem, ReasoningItem]
"""Union of all output item types."""

InputItem = Union[MessageItem, FunctionCallOutputItem]
"""Union of all input item types (when input is an array)."""


# =============================================================================
# Tool Definitions
# =============================================================================

class FunctionParameters(TypedDict, total=False):
    """JSON Schema for function parameters."""
    type: str
    properties: dict[str, Any]
    required: list[str]
    additionalProperties: bool


class FunctionTool(TypedDict, total=False):
    """A function tool definition."""
    type: Literal["function"]
    name: str
    description: str
    parameters: FunctionParameters
    strict: bool


class FunctionToolChoice(TypedDict):
    """Explicit function tool choice."""
    type: Literal["function"]
    name: str


ToolChoice = Union[
    Literal["none", "auto", "required"],
    FunctionToolChoice
]
"""Tool choice options.

- "none": Block all tools
- "auto": Model decides (default)
- "required": Must use a tool
- FunctionToolChoice: Force specific function
"""


# =============================================================================
# Usage Types
# =============================================================================

class InputTokensDetails(TypedDict, total=False):
    """Breakdown of input token usage."""
    cached_tokens: int
    reasoning_tokens: int


class OutputTokensDetails(TypedDict, total=False):
    """Breakdown of output token usage."""
    reasoning_tokens: int


class ResponseUsage(TypedDict, total=False):
    """Token usage information for a response."""
    input_tokens: int
    output_tokens: int
    total_tokens: int
    input_tokens_details: InputTokensDetails
    output_tokens_details: OutputTokensDetails


# =============================================================================
# Error Types
# =============================================================================

ErrorType = Literal[
    "server_error",
    "invalid_request",
    "not_found",
    "model_error",
    "too_many_requests"
]
"""Error type categories."""


class ResponseError(TypedDict, total=False):
    """Structured error object."""
    type: ErrorType
    code: str
    message: str
    param: str  # Related input parameter


# =============================================================================
# Request Types
# =============================================================================

class ReasoningConfig(TypedDict, total=False):
    """Reasoning configuration."""
    effort: Literal["low", "medium", "high"]
    summary: Literal["auto", "concise", "detailed"]


class TextConfig(TypedDict, total=False):
    """Text output configuration."""
    format: dict[str, Any]  # JSON schema or format type


class ResponseRequest(TypedDict, total=False):
    """Full request body for POST /v1/responses."""
    # Required
    model: str
    input: Union[str, list[InputItem]]

    # Context
    previous_response_id: str
    instructions: str

    # Tools
    tools: list[FunctionTool]
    tool_choice: ToolChoice
    parallel_tool_calls: bool
    max_tool_calls: int

    # Generation parameters
    temperature: float
    top_p: float
    presence_penalty: float
    frequency_penalty: float
    max_output_tokens: int

    # Output format
    text: TextConfig
    reasoning: ReasoningConfig

    # Streaming
    stream: bool

    # Storage
    store: bool
    metadata: dict[str, str]

    # Advanced
    truncation: Literal["auto", "disabled"]
    service_tier: Literal["auto", "default", "flex", "priority"]
    background: bool


# =============================================================================
# Response Types
# =============================================================================

class IncompleteDetails(TypedDict, total=False):
    """Details about incomplete response."""
    reason: str


class ResponseObject(TypedDict, total=False):
    """Full response object from POST /v1/responses."""
    # Identity
    id: str
    object: Literal["response"]

    # Timestamps
    created_at: float
    completed_at: float

    # Status
    status: ResponseStatus
    model: str

    # Context
    previous_response_id: str | None

    # Content
    output: list[OutputItem]
    output_text: str  # Convenience: concatenated output text

    # Error handling
    error: ResponseError | None
    incomplete_details: IncompleteDetails | None

    # Usage
    usage: ResponseUsage

    # Metadata
    metadata: dict[str, str]

    # Echo back configuration
    tools: list[FunctionTool]
    tool_choice: ToolChoice
    temperature: float
    top_p: float
    presence_penalty: float
    frequency_penalty: float
    max_output_tokens: int
    reasoning: ReasoningConfig
    text: TextConfig
    truncation: Literal["auto", "disabled"]
    parallel_tool_calls: bool


# =============================================================================
# Streaming Event Types
# =============================================================================

class StreamEventBase(TypedDict):
    """Base fields for all streaming events."""
    type: str
    sequence_number: int


class ResponseCreatedEvent(StreamEventBase):
    """response.created - Initial response created."""
    response: ResponseObject


class ResponseInProgressEvent(StreamEventBase):
    """response.in_progress - Processing started."""
    response: ResponseObject


class ResponseCompletedEvent(StreamEventBase):
    """response.completed - Full response complete."""
    response: ResponseObject


class ResponseFailedEvent(StreamEventBase):
    """response.failed - Error occurred."""
    response: ResponseObject


class ResponseIncompleteEvent(StreamEventBase):
    """response.incomplete - Token budget exhausted."""
    response: ResponseObject


class OutputItemAddedEvent(StreamEventBase, total=False):
    """response.output_item.added - New output item started."""
    output_index: int
    item: OutputItem


class ContentPartAddedEvent(StreamEventBase, total=False):
    """response.content_part.added - New content part within item."""
    item_id: str
    output_index: int
    content_index: int
    part: OutputContent


class OutputTextDeltaEvent(StreamEventBase, total=False):
    """response.output_text.delta - Text content streaming."""
    item_id: str
    output_index: int
    content_index: int
    delta: str


class ReasoningDeltaEvent(StreamEventBase, total=False):
    """response.reasoning.delta - Reasoning content streaming."""
    item_id: str
    output_index: int
    content_index: int
    delta: str
    obfuscation: str  # For encrypted reasoning


class FunctionCallArgumentsDeltaEvent(StreamEventBase, total=False):
    """response.function_call_arguments.delta - Tool call argument streaming."""
    item_id: str
    output_index: int
    call_id: str
    delta: str


class ContentPartDoneEvent(StreamEventBase, total=False):
    """response.content_part.done - Content part complete."""
    item_id: str
    output_index: int
    content_index: int
    part: OutputContent


class OutputItemDoneEvent(StreamEventBase, total=False):
    """response.output_item.done - Output item complete."""
    output_index: int
    item: OutputItem


StreamEvent = Union[
    ResponseCreatedEvent,
    ResponseInProgressEvent,
    ResponseCompletedEvent,
    ResponseFailedEvent,
    ResponseIncompleteEvent,
    OutputItemAddedEvent,
    ContentPartAddedEvent,
    OutputTextDeltaEvent,
    ReasoningDeltaEvent,
    FunctionCallArgumentsDeltaEvent,
    ContentPartDoneEvent,
    OutputItemDoneEvent,
]
"""Union of all streaming event types."""


# =============================================================================
# Event Type Constants
# =============================================================================

# State machine events (lifecycle transitions)
EVENT_RESPONSE_CREATED = "response.created"
EVENT_RESPONSE_IN_PROGRESS = "response.in_progress"
EVENT_RESPONSE_COMPLETED = "response.completed"
EVENT_RESPONSE_FAILED = "response.failed"
EVENT_RESPONSE_INCOMPLETE = "response.incomplete"

# Delta events (incremental content)
EVENT_OUTPUT_ITEM_ADDED = "response.output_item.added"
EVENT_CONTENT_PART_ADDED = "response.content_part.added"
EVENT_OUTPUT_TEXT_DELTA = "response.output_text.delta"
EVENT_REASONING_DELTA = "response.reasoning.delta"
EVENT_FUNCTION_CALL_ARGS_DELTA = "response.function_call_arguments.delta"
EVENT_CONTENT_PART_DONE = "response.content_part.done"
EVENT_OUTPUT_ITEM_DONE = "response.output_item.done"
