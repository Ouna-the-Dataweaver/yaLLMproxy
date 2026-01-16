"""API routes for the proxy."""

from .admin import register_model
from .chat import chat_completions, handle_openai_request
from .embeddings import embeddings
from .models import list_models
from .responses import responses_endpoint
from . import config

__all__ = [
    "chat_completions",
    "embeddings",
    "handle_openai_request",
    "list_models",
    "register_model",
    "responses_endpoint",
    "config",
]

