"""yaLLMproxy - Yet Another LLM Proxy

A lightweight, modular LLM proxy that routes requests to multiple backends
with fallback support and comprehensive logging.

This module provides:
- ProxyRouter: Routes requests to backends with automatic failover
- Backend management: Register and manage backends at runtime
- Comprehensive request/response logging
- OpenAI-compatible API endpoints

Example:
    >>> from src.main import app
    >>> import uvicorn
    >>> uvicorn.run(app, host="0.0.0.0", port=8000)
"""

from .main import app, create_app, config, router, SERVER_HOST, SERVER_PORT
from .core import Backend, BackendRetryableError, ProxyRouter
from .config_loader import load_config
from .logging import RequestLogRecorder, logger, setup_logging

__all__ = [
    "app",
    "Backend",
    "BackendRetryableError",
    "config",
    "create_app",
    "load_config",
    "logger",
    "ProxyRouter",
    "RequestLogRecorder",
    "router",
    "SERVER_HOST",
    "SERVER_PORT",
    "setup_logging",
]

