"""Placeholders for Anthropic <-> OpenAI Messages translation."""

from __future__ import annotations

from typing import Any, Mapping


def messages_to_chat_completions(payload: Mapping[str, Any]) -> dict[str, Any]:
    """Translate Anthropic Messages request to OpenAI Chat Completions.

    Not implemented yet. This is a placeholder for future translation logic.
    """
    raise NotImplementedError("Messages -> Chat Completions translation is not implemented yet.")


def chat_completion_to_messages(payload: Mapping[str, Any]) -> dict[str, Any]:
    """Translate OpenAI Chat Completions response to Anthropic Messages.

    Not implemented yet. This is a placeholder for future translation logic.
    """
    raise NotImplementedError("Chat Completions -> Messages translation is not implemented yet.")
