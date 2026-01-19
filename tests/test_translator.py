"""Tests for responses translator."""

import sys
from pathlib import Path

import pytest

# Add src directory to path
sys.path.insert(0, str(Path(__file__).parent.parent / "src"))

from src.responses.state_store import ResponseStateStore
from src.responses.translator import (
    build_error_response,
    chat_completion_to_response,
    convert_usage,
    responses_to_chat_completions,
)


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


def test_chat_completion_to_response_includes_tool_calls():
    completion = {
        "model": "test-model",
        "choices": [{
            "message": {
                "content": "Hello",
                "tool_calls": [
                    {
                        "id": "call_1",
                        "type": "function",
                        "function": {
                            "name": "lookup",
                            "arguments": "{\"q\":\"x\"}",
                        },
                    }
                ],
            },
        }],
    }

    response = chat_completion_to_response(completion, {"model": "test-model"}, "resp_test")

    assert response["output_text"] == "Hello"
    assert [item["type"] for item in response["output"]] == [
        "message",
        "function_call",
    ]
    tool_item = response["output"][1]
    assert tool_item["call_id"] == "call_1"
    assert tool_item["name"] == "lookup"
    assert tool_item["arguments"] == "{\"q\":\"x\"}"


def test_chat_completion_to_response_converts_summary_reasoning_and_refusal_parts():
    completion = {
        "model": "test-model",
        "choices": [{
            "message": {
                "content": [
                    {"type": "summary_text", "text": "Summary line"},
                    {"type": "reasoning_text", "text": "Reasoning line"},
                    {"type": "refusal", "refusal": "No thanks"},
                    {"type": "text", "text": "Final answer"},
                ],
            },
        }],
    }

    response = chat_completion_to_response(completion, {"model": "test-model"}, "resp_test")

    content = response["output"][0]["content"]
    assert [part["type"] for part in content] == [
        "summary_text",
        "reasoning_text",
        "refusal",
        "output_text",
    ]
    assert content[-1] == {
        "type": "output_text",
        "text": "Final answer",
        "annotations": [],
    }
    assert response["output_text"] == "Final answer"


def test_chat_completion_to_response_adds_refusal_from_message_field():
    completion = {
        "model": "test-model",
        "choices": [{
            "message": {
                "content": "Safe response",
                "refusal": "Policy refusal",
            },
        }],
    }

    response = chat_completion_to_response(completion, {"model": "test-model"}, "resp_test")

    content = response["output"][0]["content"]
    assert [part["type"] for part in content] == ["output_text", "refusal"]
    assert content[0] == {
        "type": "output_text",
        "text": "Safe response",
        "annotations": [],
    }
    assert content[1]["refusal"] == "Policy refusal"
    assert response["output_text"] == "Safe response"


@pytest.mark.asyncio
async def test_responses_to_chat_completions_converts_input_list_with_tool_output():
    request = await responses_to_chat_completions(
        input_=[
            {
                "type": "message",
                "role": "user",
                "content": [{"type": "input_text", "text": "Ask"}],
            },
            {
                "type": "function_call_output",
                "call_id": "call_42",
                "output": "Result text",
            },
        ],
        model="test-model",
    )

    messages = request["messages"]
    assert messages[0]["role"] == "user"
    assert messages[0]["content"] == "Ask"
    assert messages[1]["role"] == "tool"
    assert messages[1]["tool_call_id"] == "call_42"
    assert messages[1]["content"] == "Result text"


@pytest.mark.asyncio
async def test_responses_to_chat_completions_handles_multimodal_and_refusal_parts():
    request = await responses_to_chat_completions(
        input_=[{
            "type": "message",
            "role": "user",
            "content": [
                {"type": "input_text", "text": "Describe this"},
                {
                    "type": "input_image",
                    "image_url": "https://example.com/image.png",
                    "detail": "high",
                },
                {"type": "refusal", "refusal": "No thanks"},
                {"type": "summary_text", "text": "Summary line"},
                {"type": "reasoning_text", "text": "Reasoning line"},
            ],
        }],
        model="test-model",
    )

    assert request["messages"][0]["role"] == "user"
    assert request["messages"][0]["content"] == [
        {"type": "text", "text": "Describe this"},
        {
            "type": "image_url",
            "image_url": {"url": "https://example.com/image.png", "detail": "high"},
        },
        {"type": "text", "text": "No thanks"},
        {"type": "text", "text": "Summary line"},
        {"type": "text", "text": "Reasoning line"},
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


@pytest.mark.asyncio
async def test_responses_to_chat_completions_replays_previous_response_chain():
    state_store = ResponseStateStore()

    await state_store.store_response(
        response={
            "id": "resp_a",
            "object": "response",
            "status": "completed",
            "model": "test-model",
            "previous_response_id": None,
            "output": [{
                "id": "msg_a",
                "type": "message",
                "role": "assistant",
                "status": "completed",
                "content": [{"type": "output_text", "text": "Hello"}],
            }],
            "output_text": "Hello",
            "usage": {},
            "created_at": 0.0,
            "completed_at": 0.0,
            "metadata": {},
            "error": None,
        },
        original_input="First input",
    )

    await state_store.store_response(
        response={
            "id": "resp_b",
            "object": "response",
            "status": "completed",
            "model": "test-model",
            "previous_response_id": "resp_a",
            "output": [{
                "id": "msg_b",
                "type": "message",
                "role": "assistant",
                "status": "completed",
                "content": [{"type": "output_text", "text": "Second reply"}],
            }],
            "output_text": "Second reply",
            "usage": {},
            "created_at": 0.0,
            "completed_at": 0.0,
            "metadata": {},
            "error": None,
        },
        original_input="Second input",
    )

    request = await responses_to_chat_completions(
        input_="Third input",
        model="test-model",
        previous_response_id="resp_b",
        state_store=state_store,
    )

    messages = request["messages"]
    assert [msg["role"] for msg in messages] == [
        "user",
        "assistant",
        "user",
        "assistant",
        "user",
    ]
    assert [msg["content"] for msg in messages] == [
        "First input",
        "Hello",
        "Second input",
        "Second reply",
        "Third input",
    ]


def test_convert_usage_maps_token_details():
    usage = {
        "prompt_tokens": 12,
        "completion_tokens": 7,
        "total_tokens": 19,
        "prompt_tokens_details": {"cached_tokens": 2, "other": 1},
        "completion_tokens_details": {"reasoning_tokens": 3, "other": 9},
    }

    result = convert_usage(usage)

    assert result == {
        "input_tokens": 12,
        "output_tokens": 7,
        "total_tokens": 19,
        "input_tokens_details": {"cached_tokens": 2},
        "output_tokens_details": {"reasoning_tokens": 3},
    }


def test_build_error_response_includes_param_and_failed_status():
    response = build_error_response(
        response_id="resp_error",
        error_type="invalid_request",
        error_code="bad_param",
        message="Bad parameter",
        model="test-model",
        param="max_output_tokens",
    )

    assert response["status"] == "failed"
    assert response["model"] == "test-model"
    assert response["output"] == []
    assert response["error"] == {
        "type": "invalid_request",
        "code": "bad_param",
        "message": "Bad parameter",
        "param": "max_output_tokens",
    }
