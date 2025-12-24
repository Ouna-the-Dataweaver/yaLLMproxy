"""Core exceptions for the proxy."""

from typing import Optional

from fastapi import Response


class ProxyError(Exception):
    """Base exception for proxy errors."""
    
    def __init__(self, message: str) -> None:
        super().__init__(message)
        self.message = message


class BackendRetryableError(ProxyError):
    """Signals that another backend attempt should be made."""
    
    def __init__(self, message: str, response: Optional[Response] = None) -> None:
        super().__init__(message)
        self.response = response


class ConfigurationError(ProxyError):
    """Raised when there's an issue with the configuration."""
    pass


class ModelNotFoundError(ProxyError):
    """Raised when a requested model is not found in the configuration."""
    pass


class InvalidRequestError(ProxyError):
    """Raised when an incoming request is invalid."""
    
    def __init__(self, message: str, code: str = "invalid_request") -> None:
        super().__init__(message)
        self.code = code

