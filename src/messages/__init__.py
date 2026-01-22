"""Anthropic Messages API translation helpers.

Provides translation between Anthropic Messages API format and OpenAI Chat
Completions API format, enabling the proxy to route Anthropic-format requests
to OpenAI-compatible backends.
"""

from .translator import chat_completion_to_messages, messages_to_chat_completions
from .stream_adapter import (
    ChatToMessagesStreamAdapter,
    adapt_chat_stream_to_messages,
)

__all__ = [
    "messages_to_chat_completions",
    "chat_completion_to_messages",
    "ChatToMessagesStreamAdapter",
    "adapt_chat_stream_to_messages",
]
