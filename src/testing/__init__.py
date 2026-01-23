"""Testing utilities for in-process proxy simulations."""

from .assertions import (
    assert_anthropic_message_valid,
    assert_anthropic_sse_valid,
    assert_messages_equal,
    assert_no_slot_leak,
    assert_openai_chat_valid,
    assert_response_content_equals,
    assert_responses_api_valid,
    assert_responses_has_tool_calls,
    assert_responses_output_text_equals,
    assert_responses_sse_valid,
    assert_slot_released,
    assert_sse_event_sequence_valid,
)
from .fake_upstream import FakeUpstream, StreamError, UpstreamResponse
from .proxy_harness import ProxyHarness
from .response_builders import (
    build_anthropic_message_response,
    build_anthropic_request,
    build_anthropic_stream_events,
    build_openai_chat_response,
    build_openai_request,
    build_openai_response_with_reasoning,
    build_openai_response_with_tags,
    build_openai_stream_chunks,
    build_openai_tool_call_response_k2,
    build_openai_tool_call_response_xml,
    build_responses_request,
)
from .template_unparse import (
    TemplateMarkers,
    detect_template_markers,
    normalize_message_for_compare,
    normalize_tool_calls,
    render_unparsed_content,
    unparse_assistant_message,
)

__all__ = [
    # Core simulation classes
    "FakeUpstream",
    "UpstreamResponse",
    "StreamError",
    "ProxyHarness",
    # Response builders
    "build_openai_chat_response",
    "build_openai_stream_chunks",
    "build_anthropic_message_response",
    "build_anthropic_stream_events",
    "build_anthropic_request",
    "build_openai_request",
    "build_responses_request",
    "build_openai_response_with_tags",
    "build_openai_response_with_reasoning",
    "build_openai_tool_call_response_xml",
    "build_openai_tool_call_response_k2",
    # Assertions
    "assert_anthropic_message_valid",
    "assert_openai_chat_valid",
    "assert_sse_event_sequence_valid",
    "assert_anthropic_sse_valid",
    "assert_slot_released",
    "assert_no_slot_leak",
    "assert_messages_equal",
    "assert_response_content_equals",
    "assert_responses_api_valid",
    "assert_responses_sse_valid",
    "assert_responses_output_text_equals",
    "assert_responses_has_tool_calls",
    # Template utilities
    "TemplateMarkers",
    "detect_template_markers",
    "normalize_message_for_compare",
    "normalize_tool_calls",
    "render_unparsed_content",
    "unparse_assistant_message",
]
