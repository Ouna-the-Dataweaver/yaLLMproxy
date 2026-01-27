"""Tests for the SSE module."""

import sys
from pathlib import Path

# Add src directory to path
sys.path.insert(0, str(Path(__file__).parent.parent / "src"))

from src.core.sse import detect_sse_stream_error, STREAM_ERROR_CHECK_BUFFER_SIZE, SSEJSONDecoder
from src.modules.response_pipeline import SSEDecoder


class TestDetectSseStreamError:
    """Tests for SSE stream error detection."""

    def test_returns_none_for_empty_data(self):
        """Test that None is returned for empty data."""
        result = detect_sse_stream_error(b"")
        assert result is None

    def test_returns_none_for_non_json_data(self):
        """Test that None is returned for non-JSON data."""
        result = detect_sse_stream_error(b"just some text")
        assert result is None

    def test_returns_none_for_valid_stream_start(self):
        """Test that None is returned for valid stream start."""
        result = detect_sse_stream_error(b'data: {"id": "test"}')
        assert result is None

    def test_returns_none_for_done_signal(self):
        """Test that None is returned for [DONE] signal."""
        result = detect_sse_stream_error(b"data: [DONE]")
        assert result is None

    def test_detects_minimax_style_error(self):
        """Test that MiniMax-style error is detected."""
        error_data = b'data: {"type":"error","error":{"message":"test error","http_code":500}}'
        result = detect_sse_stream_error(error_data)
        assert result is not None
        assert "test error" in result
        assert "http_code=500" in result

    def test_detects_generic_openai_error(self):
        """Test that generic OpenAI-style error is detected."""
        error_data = b'data: {"error":{"message":"rate limited","type":"rate_limit"}}'
        result = detect_sse_stream_error(error_data)
        assert result is not None
        assert "rate limited" in result
        assert "type=rate_limit" in result

    def test_returns_none_for_non_dict_error(self):
        """Test that None is returned for non-dict error."""
        result = detect_sse_stream_error(b'data: "just a string"')
        assert result is None

    def test_handles_unicode_errors(self):
        """Test that Unicode errors are handled gracefully."""
        result = detect_sse_stream_error("data: ÿðøÿ".encode("utf-8"))
        assert result is None

    def test_detects_error_in_mixed_stream(self):
        """Test that errors are detected in mixed valid/invalid stream."""
        stream = b'{"id": "valid"}\ndata: {"error":{"message":"fail"}}\ndata: [DONE]'
        result = detect_sse_stream_error(stream)
        assert result is not None
        assert "fail" in result

    def test_buffer_size_constant(self):
        """Test that STREAM_ERROR_CHECK_BUFFER_SIZE is defined."""
        assert STREAM_ERROR_CHECK_BUFFER_SIZE == 4096


def test_sse_decoder_emits_event_for_complete_chunk():
    """Test that SSEDecoder emits an event for a complete SSE chunk."""
    decoder = SSEDecoder()
    events = decoder.feed(b"data: hello\n\n")
    assert len(events) == 1
    assert events[0].data == "hello"
    assert events[0].other_lines == []


def test_sse_decoder_buffers_until_flush():
    """Test that SSEDecoder buffers incomplete events until flush is called."""
    decoder = SSEDecoder()
    events = decoder.feed(b"data: hello")
    assert events == []
    assert decoder.flush() == b"data: hello"


class TestSSEJSONDecoderFlush:
    """Tests for SSEJSONDecoder flush functionality."""

    def test_flush_empty_buffer(self):
        """Test that flush returns empty list for empty buffer."""
        decoder = SSEJSONDecoder()
        assert decoder.flush() == []

    def test_flush_whitespace_only(self):
        """Test that flush returns empty list for whitespace-only buffer."""
        decoder = SSEJSONDecoder()
        decoder.feed(b"   \n  ")
        assert decoder.flush() == []

    def test_flush_done_signal(self):
        """Test that flush returns empty list for [DONE] signal."""
        decoder = SSEJSONDecoder()
        decoder.feed(b"data: [DONE]")
        assert decoder.flush() == []

    def test_flush_extracts_json_payload(self):
        """Test that flush extracts JSON payload from incomplete event."""
        decoder = SSEJSONDecoder()
        # Feed incomplete event (no trailing \n\n)
        decoder.feed(b'data: {"usage": {"prompt_tokens": 100}}')
        result = decoder.flush()
        assert len(result) == 1
        assert result[0] == {"usage": {"prompt_tokens": 100}}

    def test_flush_handles_complex_usage_payload(self):
        """Test that flush handles complex usage payload like GLM returns."""
        decoder = SSEJSONDecoder()
        payload = b'data: {"choices": [{"finish_reason": "stop"}], "usage": {"prompt_tokens": 17293, "completion_tokens": 97}}'
        decoder.feed(payload)
        result = decoder.flush()
        assert len(result) == 1
        assert result[0]["usage"]["prompt_tokens"] == 17293
        assert result[0]["usage"]["completion_tokens"] == 97

    def test_flush_clears_buffer(self):
        """Test that flush clears the buffer."""
        decoder = SSEJSONDecoder()
        decoder.feed(b'data: {"test": 1}')
        decoder.flush()
        # Second flush should return empty
        assert decoder.flush() == []

    def test_flush_handles_invalid_json(self):
        """Test that flush handles invalid JSON gracefully."""
        decoder = SSEJSONDecoder()
        decoder.feed(b'data: {invalid json}')
        assert decoder.flush() == []

    def test_flush_handles_non_dict_json(self):
        """Test that flush handles non-dict JSON gracefully."""
        decoder = SSEJSONDecoder()
        decoder.feed(b'data: ["array", "not", "dict"]')
        assert decoder.flush() == []

    def test_normal_feed_followed_by_flush(self):
        """Test that normal feed with flush catches remaining data."""
        decoder = SSEJSONDecoder()
        # First event complete
        payloads = decoder.feed(b'data: {"id": 1}\n\ndata: {"id": 2}')
        assert len(payloads) == 1
        assert payloads[0]["id"] == 1
        # Second event incomplete, need flush
        remaining = decoder.flush()
        assert len(remaining) == 1
        assert remaining[0]["id"] == 2
