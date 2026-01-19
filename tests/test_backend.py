"""Tests for the core backend module."""

import sys
from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest

# Add src directory to path
sys.path.insert(0, str(Path(__file__).parent.parent / "src"))

from src.core.backend import (
    Backend,
    build_outbound_headers,
    build_backend_body,
    filter_response_headers,
    normalize_request_model,
    extract_target_model,
    extract_api_type,
)


class TestBackend:
    """Tests for the Backend dataclass."""

    def test_backend_creation(self):
        """Test basic backend creation."""
        backend = Backend(
            name="test-model",
            base_url="https://api.example.com/v1",
            api_key="test-key",
            timeout=30.0,
            target_model="gpt-4",
            api_type="openai",
            supports_reasoning=False,
        )
        assert backend.name == "test-model"
        assert backend.base_url == "https://api.example.com/v1"
        assert backend.api_key == "test-key"
        assert backend.timeout == 30.0
        assert backend.target_model == "gpt-4"
        assert backend.api_type == "openai"
        assert backend.supports_reasoning is False

    def test_backend_build_url_simple(self):
        """Test URL building with simple path."""
        backend = Backend(
            name="test",
            base_url="https://api.example.com",
            api_key="key",
            timeout=None,
            target_model=None,
        )
        url = backend.build_url("/chat/completions", "")
        assert url == "https://api.example.com/chat/completions"

    def test_backend_build_url_with_query(self):
        """Test URL building with query parameters."""
        backend = Backend(
            name="test",
            base_url="https://api.example.com",
            api_key="key",
            timeout=None,
            target_model=None,
        )
        url = backend.build_url("/chat/completions", "stream=true")
        assert url == "https://api.example.com/chat/completions?stream=true"

    def test_backend_build_url_strips_v1_prefix(self):
        """Test that /v1 prefix is stripped from path."""
        backend = Backend(
            name="test",
            base_url="https://api.example.com",
            api_key="key",
            timeout=None,
            target_model=None,
        )
        url = backend.build_url("/v1/chat/completions", "")
        assert url == "https://api.example.com/chat/completions"

    def test_backend_build_url_with_empty_path(self):
        """Test URL building with empty path."""
        backend = Backend(
            name="test",
            base_url="https://api.example.com/",
            api_key="key",
            timeout=None,
            target_model=None,
        )
        url = backend.build_url("", "")
        assert url == "https://api.example.com/"

    def test_backend_defaults(self):
        """Test that default values are set correctly."""
        backend = Backend(
            name="test",
            base_url="https://api.example.com",
            api_key="key",
            timeout=None,
            target_model=None,
        )
        assert backend.api_type == "openai"
        assert backend.supports_reasoning is False


class TestBuildOutboundHeaders:
    """Tests for building outbound request headers."""

    def test_strips_incoming_authorization_header(self):
        """Test that incoming authorization header is stripped."""
        incoming = {
            "Authorization": "Bearer secret",
            "Content-Type": "application/json",
        }
        result = build_outbound_headers(incoming, "new-key")
        # The incoming Authorization is stripped, but a new one is added with backend key
        assert result.get("Authorization") == "Bearer new-key"

    def test_adds_backend_api_key(self):
        """Test that backend API key is added."""
        incoming = {"Content-Type": "application/json"}
        result = build_outbound_headers(incoming, "backend-key")
        assert result.get("Authorization") == "Bearer backend-key"

    def test_adds_content_type_if_missing(self):
        """Test that content-type is added if missing."""
        incoming = {"Accept": "application/json"}
        result = build_outbound_headers(incoming, "")
        assert result.get("Content-Type") == "application/json"

    def test_preserves_incoming_content_type(self):
        """Test that incoming content-type is preserved."""
        incoming = {"Content-Type": "application/json; charset=utf-8"}
        result = build_outbound_headers(incoming, "")
        assert result.get("Content-Type") == "application/json; charset=utf-8"

    def test_strips_hop_by_hop_headers(self):
        """Test that hop-by-hop headers are stripped."""
        incoming = {
            "Connection": "keep-alive",
            "Transfer-Encoding": "chunked",
            "Content-Type": "application/json",
        }
        result = build_outbound_headers(incoming, "")
        assert "Connection" not in result
        assert "Transfer-Encoding" not in result
        assert result.get("Content-Type") == "application/json"

    def test_preserves_accept_encoding(self):
        """Test that accept-encoding is preserved."""
        incoming = {
            "Accept-Encoding": "gzip, deflate",
            "Content-Type": "application/json",
        }
        result = build_outbound_headers(incoming, "")
        assert result.get("Accept-Encoding") == "gzip, deflate"

    def test_does_not_add_accept_encoding_if_missing(self):
        """Test that accept-encoding is not added if missing."""
        incoming = {"Content-Type": "application/json"}
        result = build_outbound_headers(incoming, "")
        assert "Accept-Encoding" not in result

    def test_preserves_custom_headers(self):
        """Test that custom headers are preserved."""
        incoming = {
            "X-Custom-Header": "custom-value",
            "User-Agent": "test-agent",
            "Content-Type": "application/json",
        }
        result = build_outbound_headers(incoming, "")
        assert result.get("X-Custom-Header") == "custom-value"
        assert result.get("User-Agent") == "test-agent"

    def test_anthropic_uses_x_api_key(self):
        """Test that anthropic backends use x-api-key and add version."""
        incoming = {
            "Content-Type": "application/json",
            "x-api-key": "client-key",
        }
        result = build_outbound_headers(
            incoming,
            "backend-key",
            api_type="anthropic",
            anthropic_version="2023-06-01",
        )
        assert "Authorization" not in result
        assert result.get("x-api-key") == "backend-key"
        assert result.get("anthropic-version") == "2023-06-01"

    def test_anthropic_preserves_client_version(self):
        """Test that client-provided anthropic-version is preserved."""
        incoming = {
            "Content-Type": "application/json",
            "anthropic-version": "2024-01-01",
        }
        result = build_outbound_headers(
            incoming,
            "backend-key",
            api_type="anthropic",
            anthropic_version="2023-06-01",
        )
        assert result.get("anthropic-version") == "2024-01-01"


class TestBuildBackendBody:
    """Tests for building backend request body."""

    def test_returns_original_body_when_no_changes(self):
        """Test that original body is returned when no changes needed."""
        payload = {"model": "test", "messages": []}
        backend = Backend(
            name="test",
            base_url="https://api.example.com",
            api_key="key",
            timeout=None,
            target_model=None,
            supports_reasoning=False,
        )
        original_body = b'{"model": "test", "messages": []}'
        result = build_backend_body(payload, backend, original_body)
        assert result == original_body

    def test_rewrites_model_name(self):
        """Test that model name is rewritten."""
        payload = {"model": "original", "messages": []}
        backend = Backend(
            name="test",
            base_url="https://api.example.com",
            api_key="key",
            timeout=None,
            target_model="rewritten",
            supports_reasoning=False,
        )
        original_body = b'{"model": "original", "messages": []}'
        result = build_backend_body(payload, backend, original_body)
        import json
        parsed = json.loads(result)
        assert parsed["model"] == "rewritten"

    def test_adds_thinking_for_reasoning_backends(self):
        """Test that thinking is added for reasoning backends."""
        payload = {"model": "test", "messages": []}
        backend = Backend(
            name="test",
            base_url="https://api.example.com",
            api_key="key",
            timeout=None,
            target_model=None,
            supports_reasoning=True,
        )
        original_body = b'{"model": "test", "messages": []}'
        result = build_backend_body(payload, backend, original_body)
        import json
        parsed = json.loads(result)
        assert parsed.get("thinking") == {"type": "enabled"}

    def test_does_not_add_thinking_when_disabled(self):
        """Test that thinking is not added when explicitly disabled."""
        payload = {"model": "test", "messages": [], "thinking": {"type": "disabled"}}
        backend = Backend(
            name="test",
            base_url="https://api.example.com",
            api_key="key",
            timeout=None,
            target_model=None,  # No target model change
            supports_reasoning=True,
        )
        # The original body doesn't have thinking
        original_body = b'{"model": "test", "messages": []}'
        result = build_backend_body(payload, backend, original_body)
        assert result == original_body


class TestFilterResponseHeaders:
    """Tests for filtering response headers."""

    def test_removes_hop_by_hop_headers(self):
        """Test that hop-by-hop headers are removed."""
        headers = {
            "Transfer-Encoding": "chunked",
            "Content-Length": "100",
            "Content-Type": "application/json",
        }
        result = filter_response_headers(headers)
        assert "Transfer-Encoding" not in result
        assert "Content-Length" not in result
        assert result.get("Content-Type") == "application/json"

    def test_preserves_custom_headers(self):
        """Test that custom headers are preserved."""
        headers = {
            "X-Request-Id": "12345",
            "X-Rate-Limit-Remaining": "100",
            "Content-Type": "application/json",
        }
        result = filter_response_headers(headers)
        assert result.get("X-Request-Id") == "12345"
        assert result.get("X-Rate-Limit-Remaining") == "100"


class TestNormalizeRequestModel:
    """Tests for normalizing request model names."""

    def test_returns_normal_model(self):
        """Test that normal model names are returned as-is."""
        assert normalize_request_model("gpt-4") == "gpt-4"

    def test_strips_openai_prefix(self):
        """Test that openai/ prefix is stripped."""
        assert normalize_request_model("openai/gpt-4") == "gpt-4"

    def test_case_insensitive_prefix(self):
        """Test that prefix matching is case-insensitive."""
        assert normalize_request_model("OPENAI/gpt-4") == "gpt-4"
        assert normalize_request_model("OpenAI/gpt-4") == "gpt-4"

    def test_strips_whitespace(self):
        """Test that whitespace is stripped."""
        assert normalize_request_model("  gpt-4  ") == "gpt-4"

    def test_returns_empty_for_invalid_input(self):
        """Test that invalid input returns empty string."""
        assert normalize_request_model("") == ""
        assert normalize_request_model(None) == ""


class TestExtractTargetModel:
    """Tests for extracting target model from parameters."""

    def test_returns_none_when_no_model(self):
        """Test that None is returned when no model specified."""
        result = extract_target_model({})
        assert result is None

    def test_returns_target_model_override(self):
        """Test that target_model override is returned."""
        params = {"model": "original", "target_model": "override"}
        result = extract_target_model(params)
        assert result == "override"

    def test_returns_forward_model_override(self):
        """Test that forward_model override is returned."""
        params = {"model": "original", "forward_model": "override"}
        result = extract_target_model(params)
        assert result == "override"

    def test_strips_api_type_prefix(self):
        """Test that API type prefix is stripped."""
        params = {"model": "openai/gpt-4"}
        result = extract_target_model(params, api_type="openai")
        assert result == "gpt-4"

    def test_returns_original_model_when_no_override(self):
        """Test that original model is returned when no override."""
        params = {"model": "gpt-4"}
        result = extract_target_model(params)
        assert result == "gpt-4"


class TestExtractApiType:
    """Tests for extracting API type from parameters."""

    def test_returns_openai_by_default(self):
        """Test that openai is returned by default."""
        assert extract_api_type({}) == "openai"

    def test_returns_custom_api_type(self):
        """Test that custom API type is returned."""
        assert extract_api_type({"api_type": "anthropic"}) == "anthropic"

    def test_normalizes_api_type(self):
        """Test that API type is normalized to lowercase."""
        assert extract_api_type({"api_type": "Azure"}) == "azure"

    def test_returns_openai_for_empty_string(self):
        """Test that empty string returns openai."""
        assert extract_api_type({"api_type": ""}) == "openai"
