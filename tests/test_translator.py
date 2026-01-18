"""Tests for responses translator."""

import sys
from pathlib import Path

import pytest

# Add src directory to path
sys.path.insert(0, str(Path(__file__).parent.parent / "src"))

from src.responses.state_store import ResponseStateStore
from src.responses.translator import chat_completion_to_response, responses_to_chat_completions


def test_chat_completion_to_response_handles_list_content():
    completion = {
        "model": "test-model",
        "choices": [{
            "message": {
                "content": [
                    {"type": "text", "text": "Hello "},
                    {"type": "text", "text": "world"},
                ],
            },
        }],
    }

    response = chat_completion_to_response(completion, {"model": "test-model"}, "resp_test")

    assert isinstance(response["output_text"], str)
    assert response["output_text"] == "Hello world"
    assert response["output"][0]["type"] == "message"
    assert [part["type"] for part in response["output"][0]["content"]] == [
        "output_text",
        "output_text",
    ]


@pytest.mark.asyncio
async def test_responses_to_chat_completions_preserves_non_text_history_content():
    state_store = ResponseStateStore()
    response_id = "resp_history_non_text"

    history_message = {
        "id": "msg_history",
        "type": "message",
        "role": "assistant",
        "status": "completed",
        "content": [
            {"type": "output_text", "text": "Hello"},
            {"type": "refusal", "refusal": "No thanks"},
            {"type": "summary_text", "text": "Summary line"},
            {"type": "reasoning_text", "text": "Reasoning line"},
        ],
    }

    await state_store.store_response(
        response={
            "id": response_id,
            "object": "response",
            "status": "completed",
            "model": "test-model",
            "previous_response_id": None,
            "output": [history_message],
            "output_text": "",
            "usage": {},
            "created_at": 0.0,
            "completed_at": 0.0,
            "metadata": {},
            "error": None,
        },
        original_input=None,
    )

    request = await responses_to_chat_completions(
        input_="Next prompt",
        model="test-model",
        previous_response_id=response_id,
        state_store=state_store,
    )

    messages = request["messages"]
    assert messages[0]["role"] == "assistant"
    assert messages[0]["content"] == [
        {"type": "text", "text": "Hello"},
        {"type": "text", "text": "No thanks"},
        {"type": "text", "text": "Summary line"},
        {"type": "text", "text": "Reasoning line"},
    ]
    assert messages[-1]["role"] == "user"
    assert messages[-1]["content"] == "Next prompt"
