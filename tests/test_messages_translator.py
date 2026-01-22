"""Tests for Anthropic Messages <-> OpenAI Chat Completions translator."""

import sys
from pathlib import Path

import pytest

# Add src directory to path
sys.path.insert(0, str(Path(__file__).parent.parent / "src"))

from src.messages.translator import (
    messages_to_chat_completions,
    chat_completion_to_messages,
    _convert_tool_choice,
    _convert_tools,
    _convert_system_to_openai,
    _convert_stop_reason,
)


# =============================================================================
# messages_to_chat_completions() tests
# =============================================================================

class TestMessagesToChatCompletions:
    """Tests for translating Anthropic Messages requests to OpenAI Chat Completions."""

    def test_simple_text_message(self):
        """Test simple text-only message conversion."""
        payload = {
            "model": "claude-3-opus",
            "max_tokens": 1024,
            "messages": [
                {"role": "user", "content": "Hello, how are you?"}
            ]
        }

        result = messages_to_chat_completions(payload)

        assert result["model"] == "claude-3-opus"
        assert result["max_tokens"] == 1024
        assert len(result["messages"]) == 1
        assert result["messages"][0] == {"role": "user", "content": "Hello, how are you?"}

    def test_system_as_string(self):
        """Test system parameter as string is converted to system message."""
        payload = {
            "model": "claude-3-opus",
            "max_tokens": 1024,
            "system": "You are a helpful assistant.",
            "messages": [
                {"role": "user", "content": "Hello"}
            ]
        }

        result = messages_to_chat_completions(payload)

        assert len(result["messages"]) == 2
        assert result["messages"][0] == {"role": "system", "content": "You are a helpful assistant."}
        assert result["messages"][1] == {"role": "user", "content": "Hello"}

    def test_system_as_content_blocks(self):
        """Test system parameter as content blocks array."""
        payload = {
            "model": "claude-3-opus",
            "max_tokens": 1024,
            "system": [
                {"type": "text", "text": "You are a helpful assistant."},
                {"type": "text", "text": "Be concise."}
            ],
            "messages": [
                {"role": "user", "content": "Hello"}
            ]
        }

        result = messages_to_chat_completions(payload)

        assert len(result["messages"]) == 2
        assert result["messages"][0] == {
            "role": "system",
            "content": "You are a helpful assistant.\nBe concise."
        }

    def test_content_blocks_text(self):
        """Test message with text content blocks."""
        payload = {
            "model": "claude-3-opus",
            "max_tokens": 1024,
            "messages": [
                {
                    "role": "user",
                    "content": [
                        {"type": "text", "text": "Hello "},
                        {"type": "text", "text": "world!"}
                    ]
                }
            ]
        }

        result = messages_to_chat_completions(payload)

        # Multiple text blocks should be preserved as array
        assert result["messages"][0]["content"] == [
            {"type": "text", "text": "Hello "},
            {"type": "text", "text": "world!"}
        ]

    def test_single_text_block_simplified(self):
        """Test single text block is simplified to string."""
        payload = {
            "model": "claude-3-opus",
            "max_tokens": 1024,
            "messages": [
                {
                    "role": "user",
                    "content": [{"type": "text", "text": "Hello world!"}]
                }
            ]
        }

        result = messages_to_chat_completions(payload)

        # Single text block should be simplified to string
        assert result["messages"][0]["content"] == "Hello world!"

    def test_image_base64(self):
        """Test base64 image conversion."""
        payload = {
            "model": "claude-3-opus",
            "max_tokens": 1024,
            "messages": [
                {
                    "role": "user",
                    "content": [
                        {"type": "text", "text": "What's in this image?"},
                        {
                            "type": "image",
                            "source": {
                                "type": "base64",
                                "media_type": "image/png",
                                "data": "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="
                            }
                        }
                    ]
                }
            ]
        }

        result = messages_to_chat_completions(payload)

        content = result["messages"][0]["content"]
        assert len(content) == 2
        assert content[0] == {"type": "text", "text": "What's in this image?"}
        assert content[1]["type"] == "image_url"
        assert content[1]["image_url"]["url"].startswith("data:image/png;base64,")

    def test_image_url(self):
        """Test URL image conversion."""
        payload = {
            "model": "claude-3-opus",
            "max_tokens": 1024,
            "messages": [
                {
                    "role": "user",
                    "content": [
                        {"type": "text", "text": "What's in this image?"},
                        {
                            "type": "image",
                            "source": {
                                "type": "url",
                                "url": "https://example.com/image.png"
                            }
                        }
                    ]
                }
            ]
        }

        result = messages_to_chat_completions(payload)

        content = result["messages"][0]["content"]
        assert content[1] == {
            "type": "image_url",
            "image_url": {"url": "https://example.com/image.png"}
        }

    def test_tool_use_blocks(self):
        """Test assistant message with tool_use blocks."""
        payload = {
            "model": "claude-3-opus",
            "max_tokens": 1024,
            "messages": [
                {"role": "user", "content": "What's the weather in Tokyo?"},
                {
                    "role": "assistant",
                    "content": [
                        {"type": "text", "text": "Let me check the weather."},
                        {
                            "type": "tool_use",
                            "id": "toolu_123",
                            "name": "get_weather",
                            "input": {"location": "Tokyo"}
                        }
                    ]
                }
            ]
        }

        result = messages_to_chat_completions(payload)

        assert len(result["messages"]) == 2
        assistant_msg = result["messages"][1]
        assert assistant_msg["role"] == "assistant"
        assert assistant_msg["content"] == "Let me check the weather."
        assert len(assistant_msg["tool_calls"]) == 1
        assert assistant_msg["tool_calls"][0] == {
            "id": "toolu_123",
            "type": "function",
            "function": {
                "name": "get_weather",
                "arguments": '{"location": "Tokyo"}'
            }
        }

    def test_tool_result_blocks(self):
        """Test user message with tool_result blocks."""
        payload = {
            "model": "claude-3-opus",
            "max_tokens": 1024,
            "messages": [
                {"role": "user", "content": "What's the weather?"},
                {
                    "role": "assistant",
                    "content": [
                        {
                            "type": "tool_use",
                            "id": "toolu_123",
                            "name": "get_weather",
                            "input": {"location": "Tokyo"}
                        }
                    ]
                },
                {
                    "role": "user",
                    "content": [
                        {
                            "type": "tool_result",
                            "tool_use_id": "toolu_123",
                            "content": "Sunny, 25°C"
                        },
                        {"type": "text", "text": "Thanks for checking!"}
                    ]
                }
            ]
        }

        result = messages_to_chat_completions(payload)

        # Should have 4 messages: user, assistant, tool, user
        assert len(result["messages"]) == 4
        assert result["messages"][2] == {
            "role": "tool",
            "tool_call_id": "toolu_123",
            "content": "Sunny, 25°C"
        }
        assert result["messages"][3] == {"role": "user", "content": "Thanks for checking!"}

    def test_tool_result_with_error(self):
        """Test tool_result with is_error flag."""
        payload = {
            "model": "claude-3-opus",
            "max_tokens": 1024,
            "messages": [
                {
                    "role": "user",
                    "content": [
                        {
                            "type": "tool_result",
                            "tool_use_id": "toolu_123",
                            "content": "API rate limit exceeded",
                            "is_error": True
                        }
                    ]
                }
            ]
        }

        result = messages_to_chat_completions(payload)

        assert result["messages"][0]["content"] == "[Error] API rate limit exceeded"

    def test_tool_result_with_content_blocks(self):
        """Test tool_result with content as array of blocks."""
        payload = {
            "model": "claude-3-opus",
            "max_tokens": 1024,
            "messages": [
                {
                    "role": "user",
                    "content": [
                        {
                            "type": "tool_result",
                            "tool_use_id": "toolu_123",
                            "content": [
                                {"type": "text", "text": "Result line 1"},
                                {"type": "text", "text": "Result line 2"}
                            ]
                        }
                    ]
                }
            ]
        }

        result = messages_to_chat_completions(payload)

        assert result["messages"][0]["content"] == "Result line 1\nResult line 2"

    def test_tools_conversion(self):
        """Test tools array conversion."""
        payload = {
            "model": "claude-3-opus",
            "max_tokens": 1024,
            "messages": [{"role": "user", "content": "Hello"}],
            "tools": [
                {
                    "name": "get_weather",
                    "description": "Get the weather for a location",
                    "input_schema": {
                        "type": "object",
                        "properties": {
                            "location": {"type": "string"}
                        },
                        "required": ["location"]
                    }
                }
            ]
        }

        result = messages_to_chat_completions(payload)

        assert "tools" in result
        assert len(result["tools"]) == 1
        assert result["tools"][0] == {
            "type": "function",
            "function": {
                "name": "get_weather",
                "description": "Get the weather for a location",
                "parameters": {
                    "type": "object",
                    "properties": {
                        "location": {"type": "string"}
                    },
                    "required": ["location"]
                }
            }
        }

    def test_tool_choice_auto(self):
        """Test tool_choice auto mapping."""
        payload = {
            "model": "claude-3-opus",
            "max_tokens": 1024,
            "messages": [{"role": "user", "content": "Hello"}],
            "tool_choice": "auto"
        }

        result = messages_to_chat_completions(payload)
        assert result["tool_choice"] == "auto"

    def test_tool_choice_any(self):
        """Test tool_choice any -> required mapping."""
        payload = {
            "model": "claude-3-opus",
            "max_tokens": 1024,
            "messages": [{"role": "user", "content": "Hello"}],
            "tool_choice": "any"
        }

        result = messages_to_chat_completions(payload)
        assert result["tool_choice"] == "required"

    def test_tool_choice_none(self):
        """Test tool_choice none mapping."""
        payload = {
            "model": "claude-3-opus",
            "max_tokens": 1024,
            "messages": [{"role": "user", "content": "Hello"}],
            "tool_choice": "none"
        }

        result = messages_to_chat_completions(payload)
        assert result["tool_choice"] == "none"

    def test_tool_choice_specific_tool(self):
        """Test tool_choice with specific tool."""
        payload = {
            "model": "claude-3-opus",
            "max_tokens": 1024,
            "messages": [{"role": "user", "content": "Hello"}],
            "tool_choice": {"type": "tool", "name": "get_weather"}
        }

        result = messages_to_chat_completions(payload)
        assert result["tool_choice"] == {
            "type": "function",
            "function": {"name": "get_weather"}
        }

    def test_stop_sequences_mapping(self):
        """Test stop_sequences -> stop mapping."""
        payload = {
            "model": "claude-3-opus",
            "max_tokens": 1024,
            "messages": [{"role": "user", "content": "Hello"}],
            "stop_sequences": ["STOP", "END"]
        }

        result = messages_to_chat_completions(payload)
        assert result["stop"] == ["STOP", "END"]

    def test_temperature_and_top_p(self):
        """Test temperature and top_p pass-through."""
        payload = {
            "model": "claude-3-opus",
            "max_tokens": 1024,
            "messages": [{"role": "user", "content": "Hello"}],
            "temperature": 0.7,
            "top_p": 0.9
        }

        result = messages_to_chat_completions(payload)
        assert result["temperature"] == 0.7
        assert result["top_p"] == 0.9

    def test_stream_parameter(self):
        """Test stream parameter pass-through."""
        payload = {
            "model": "claude-3-opus",
            "max_tokens": 1024,
            "messages": [{"role": "user", "content": "Hello"}],
            "stream": True
        }

        result = messages_to_chat_completions(payload)
        assert result["stream"] is True

    def test_metadata_user_id(self):
        """Test metadata.user_id -> user mapping."""
        payload = {
            "model": "claude-3-opus",
            "max_tokens": 1024,
            "messages": [{"role": "user", "content": "Hello"}],
            "metadata": {"user_id": "user_12345"}
        }

        result = messages_to_chat_completions(payload)
        assert result["user"] == "user_12345"

    def test_assistant_prefill(self):
        """Test assistant prefill (continuing a response)."""
        payload = {
            "model": "claude-3-opus",
            "max_tokens": 1024,
            "messages": [
                {"role": "user", "content": "Tell me a story."},
                {"role": "assistant", "content": "Once upon a time"}
            ]
        }

        result = messages_to_chat_completions(payload)

        assert len(result["messages"]) == 2
        assert result["messages"][1] == {"role": "assistant", "content": "Once upon a time"}

    def test_conversation_with_multiple_turns(self):
        """Test multi-turn conversation."""
        payload = {
            "model": "claude-3-opus",
            "max_tokens": 1024,
            "messages": [
                {"role": "user", "content": "Hello"},
                {"role": "assistant", "content": "Hi! How can I help?"},
                {"role": "user", "content": "What's 2+2?"},
                {"role": "assistant", "content": "2+2 equals 4."},
                {"role": "user", "content": "Thanks!"}
            ]
        }

        result = messages_to_chat_completions(payload)

        assert len(result["messages"]) == 5
        assert [m["role"] for m in result["messages"]] == [
            "user", "assistant", "user", "assistant", "user"
        ]


# =============================================================================
# chat_completion_to_messages() tests
# =============================================================================

class TestChatCompletionToMessages:
    """Tests for translating OpenAI Chat Completions responses to Anthropic Messages."""

    def test_simple_text_response(self):
        """Test simple text response conversion."""
        payload = {
            "id": "chatcmpl-abc123",
            "object": "chat.completion",
            "model": "gpt-4",
            "choices": [
                {
                    "index": 0,
                    "message": {
                        "role": "assistant",
                        "content": "Hello! How can I help you today?"
                    },
                    "finish_reason": "stop"
                }
            ],
            "usage": {
                "prompt_tokens": 10,
                "completion_tokens": 8,
                "total_tokens": 18
            }
        }

        result = chat_completion_to_messages(payload)

        assert result["type"] == "message"
        assert result["role"] == "assistant"
        assert result["model"] == "gpt-4"
        assert result["stop_reason"] == "end_turn"
        assert len(result["content"]) == 1
        assert result["content"][0] == {"type": "text", "text": "Hello! How can I help you today?"}
        assert result["usage"]["input_tokens"] == 10
        assert result["usage"]["output_tokens"] == 8

    def test_tool_calls_response(self):
        """Test response with tool calls."""
        payload = {
            "id": "chatcmpl-abc123",
            "model": "gpt-4",
            "choices": [
                {
                    "index": 0,
                    "message": {
                        "role": "assistant",
                        "content": "Let me check the weather.",
                        "tool_calls": [
                            {
                                "id": "call_xyz789",
                                "type": "function",
                                "function": {
                                    "name": "get_weather",
                                    "arguments": '{"location": "Tokyo"}'
                                }
                            }
                        ]
                    },
                    "finish_reason": "tool_calls"
                }
            ],
            "usage": {"prompt_tokens": 10, "completion_tokens": 20, "total_tokens": 30}
        }

        result = chat_completion_to_messages(payload)

        assert result["stop_reason"] == "tool_use"
        assert len(result["content"]) == 2
        assert result["content"][0] == {"type": "text", "text": "Let me check the weather."}
        assert result["content"][1] == {
            "type": "tool_use",
            "id": "call_xyz789",
            "name": "get_weather",
            "input": {"location": "Tokyo"}
        }

    def test_multiple_tool_calls(self):
        """Test response with multiple tool calls."""
        payload = {
            "id": "chatcmpl-abc123",
            "model": "gpt-4",
            "choices": [
                {
                    "index": 0,
                    "message": {
                        "role": "assistant",
                        "content": None,
                        "tool_calls": [
                            {
                                "id": "call_1",
                                "type": "function",
                                "function": {
                                    "name": "get_weather",
                                    "arguments": '{"location": "Tokyo"}'
                                }
                            },
                            {
                                "id": "call_2",
                                "type": "function",
                                "function": {
                                    "name": "get_time",
                                    "arguments": '{"timezone": "JST"}'
                                }
                            }
                        ]
                    },
                    "finish_reason": "tool_calls"
                }
            ],
            "usage": {"prompt_tokens": 10, "completion_tokens": 30, "total_tokens": 40}
        }

        result = chat_completion_to_messages(payload)

        assert len(result["content"]) == 2
        assert result["content"][0]["type"] == "tool_use"
        assert result["content"][0]["name"] == "get_weather"
        assert result["content"][1]["type"] == "tool_use"
        assert result["content"][1]["name"] == "get_time"

    def test_finish_reason_length(self):
        """Test finish_reason length -> max_tokens mapping."""
        payload = {
            "id": "chatcmpl-abc123",
            "model": "gpt-4",
            "choices": [
                {
                    "index": 0,
                    "message": {"role": "assistant", "content": "This is a long..."},
                    "finish_reason": "length"
                }
            ],
            "usage": {"prompt_tokens": 10, "completion_tokens": 100, "total_tokens": 110}
        }

        result = chat_completion_to_messages(payload)
        assert result["stop_reason"] == "max_tokens"

    def test_finish_reason_content_filter(self):
        """Test finish_reason content_filter -> refusal mapping."""
        payload = {
            "id": "chatcmpl-abc123",
            "model": "gpt-4",
            "choices": [
                {
                    "index": 0,
                    "message": {"role": "assistant", "content": ""},
                    "finish_reason": "content_filter"
                }
            ],
            "usage": {"prompt_tokens": 10, "completion_tokens": 0, "total_tokens": 10}
        }

        result = chat_completion_to_messages(payload)
        assert result["stop_reason"] == "refusal"

    def test_id_format(self):
        """Test message ID is formatted correctly."""
        payload = {
            "id": "chatcmpl-abc123def456",
            "model": "gpt-4",
            "choices": [
                {
                    "index": 0,
                    "message": {"role": "assistant", "content": "Hello"},
                    "finish_reason": "stop"
                }
            ],
            "usage": {"prompt_tokens": 5, "completion_tokens": 1, "total_tokens": 6}
        }

        result = chat_completion_to_messages(payload)
        assert result["id"] == "msg_abc123def456"

    def test_empty_content(self):
        """Test empty content produces at least one empty text block."""
        payload = {
            "id": "chatcmpl-abc123",
            "model": "gpt-4",
            "choices": [
                {
                    "index": 0,
                    "message": {"role": "assistant", "content": None},
                    "finish_reason": "stop"
                }
            ],
            "usage": {"prompt_tokens": 5, "completion_tokens": 0, "total_tokens": 5}
        }

        result = chat_completion_to_messages(payload)
        assert len(result["content"]) == 1
        assert result["content"][0] == {"type": "text", "text": ""}


# =============================================================================
# Helper function tests
# =============================================================================

class TestHelperFunctions:
    """Tests for helper functions."""

    def test_convert_tool_choice_auto(self):
        assert _convert_tool_choice("auto") == "auto"

    def test_convert_tool_choice_any(self):
        assert _convert_tool_choice("any") == "required"

    def test_convert_tool_choice_none(self):
        assert _convert_tool_choice("none") == "none"

    def test_convert_tool_choice_object_tool(self):
        result = _convert_tool_choice({"type": "tool", "name": "my_func"})
        assert result == {"type": "function", "function": {"name": "my_func"}}

    def test_convert_tool_choice_object_auto(self):
        result = _convert_tool_choice({"type": "auto"})
        assert result == "auto"

    def test_convert_tool_choice_none_value(self):
        assert _convert_tool_choice(None) is None

    def test_convert_tools_empty(self):
        assert _convert_tools(None) is None
        assert _convert_tools([]) is None

    def test_convert_tools_basic(self):
        tools = [
            {
                "name": "test_func",
                "description": "A test function",
                "input_schema": {"type": "object"}
            }
        ]
        result = _convert_tools(tools)
        assert result == [
            {
                "type": "function",
                "function": {
                    "name": "test_func",
                    "description": "A test function",
                    "parameters": {"type": "object"}
                }
            }
        ]

    def test_convert_system_to_openai_string(self):
        result = _convert_system_to_openai("You are helpful.")
        assert result == {"role": "system", "content": "You are helpful."}

    def test_convert_system_to_openai_blocks(self):
        result = _convert_system_to_openai([
            {"type": "text", "text": "Line 1"},
            {"type": "text", "text": "Line 2"}
        ])
        assert result == {"role": "system", "content": "Line 1\nLine 2"}

    def test_convert_system_to_openai_none(self):
        assert _convert_system_to_openai(None) is None

    def test_convert_stop_reason_stop(self):
        assert _convert_stop_reason("stop") == "end_turn"

    def test_convert_stop_reason_length(self):
        assert _convert_stop_reason("length") == "max_tokens"

    def test_convert_stop_reason_tool_calls(self):
        assert _convert_stop_reason("tool_calls") == "tool_use"

    def test_convert_stop_reason_content_filter(self):
        assert _convert_stop_reason("content_filter") == "refusal"

    def test_convert_stop_reason_unknown(self):
        assert _convert_stop_reason("unknown_reason") == "end_turn"

    def test_convert_stop_reason_none(self):
        assert _convert_stop_reason(None) == "end_turn"
