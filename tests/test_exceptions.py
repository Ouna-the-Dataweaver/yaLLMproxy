"""Tests for the exceptions module."""

import sys
from pathlib import Path

import pytest

# Add src directory to path
sys.path.insert(0, str(Path(__file__).parent.parent / "src"))

from core.exceptions import (
    ProxyError,
    BackendRetryableError,
    ConfigurationError,
    ModelNotFoundError,
    InvalidRequestError,
)


class TestProxyError:
    """Tests for the base ProxyError exception."""

    def test_creates_error_with_message(self):
        """Test that error is created with message."""
        error = ProxyError("test error message")
        assert error.message == "test error message"
        assert str(error) == "test error message"


class TestBackendRetryableError:
    """Tests for BackendRetryableError exception."""

    def test_creates_error_with_message(self):
        """Test that error is created with message."""
        error = BackendRetryableError("retry needed")
        assert error.message == "retry needed"
        assert error.response is None

    def test_creates_error_with_response(self):
        """Test that error can include a response."""
        from fastapi import Response
        
        mock_response = Response(content="error", status_code=500)
        error = BackendRetryableError("retry needed", response=mock_response)
        assert error.message == "retry needed"
        assert error.response is mock_response


class TestConfigurationError:
    """Tests for ConfigurationError exception."""

    def test_creates_error_with_message(self):
        """Test that error is created with message."""
        error = ConfigurationError("invalid config")
        assert error.message == "invalid config"


class TestModelNotFoundError:
    """Tests for ModelNotFoundError exception."""

    def test_creates_error_with_message(self):
        """Test that error is created with message."""
        error = ModelNotFoundError("model not found: test")
        assert error.message == "model not found: test"


class TestInvalidRequestError:
    """Tests for InvalidRequestError exception."""

    def test_creates_error_with_default_code(self):
        """Test that default code is used."""
        error = InvalidRequestError("invalid request")
        assert error.message == "invalid request"
        assert error.code == "invalid_request"

    def test_creates_error_with_custom_code(self):
        """Test that custom code is used."""
        error = InvalidRequestError("missing parameter", code="missing_param")
        assert error.message == "missing parameter"
        assert error.code == "missing_param"

