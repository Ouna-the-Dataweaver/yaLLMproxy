"""API routes for the proxy."""

from .admin import register_model
from .chat import chat_completions, handle_openai_request, responses
from .models import list_models

__all__ = [
    "chat_completions",
    "handle_openai_request",
    "list_models",
    "register_model",
    "responses",
]

