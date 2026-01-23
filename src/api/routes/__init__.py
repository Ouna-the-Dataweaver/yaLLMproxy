"""API routes for the proxy."""

from .admin import register_model
from .chat import chat_completions, handle_openai_request
from .embeddings import embeddings
from .models import list_models
from .messages import messages_endpoint
from .rerank import rerank
from .responses import responses_endpoint
from . import config

__all__ = [
    "chat_completions",
    "embeddings",
    "handle_openai_request",
    "list_models",
    "messages_endpoint",
    "register_model",
    "rerank",
    "responses_endpoint",
    "config",
]
