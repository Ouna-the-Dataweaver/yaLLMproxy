"""Open Responses API support for yaLLMproxy.

This package provides the implementation for the /v1/responses endpoint,
supporting both pass-through mode (for backends that natively support the
Responses API) and simulation mode (translating to/from chat completions).

Key components:
- state_store: LRU + database storage for response history
- translator: Bidirectional translation between Responses and Chat Completions
- stream_adapter: Convert chat completion SSE to Responses API events
"""

from .state_store import ResponseStateStore, get_state_store
from .translator import (
    responses_to_chat_completions,
    chat_completion_to_response,
    convert_usage,
)
from .stream_adapter import ChatToResponsesStreamAdapter

__all__ = [
    "ResponseStateStore",
    "get_state_store",
    "responses_to_chat_completions",
    "chat_completion_to_response",
    "convert_usage",
    "ChatToResponsesStreamAdapter",
]
