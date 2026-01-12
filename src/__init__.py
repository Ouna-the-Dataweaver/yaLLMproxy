"""yaLLMproxy - Yet Another LLM Proxy.

Exports common entry points, with a best-effort eager import for compatibility.
Falls back to lazy imports when optional dependencies are missing.
"""

from __future__ import annotations

import importlib
from typing import Any

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

_LAZY_EXPORTS: dict[str, tuple[str, str]] = {
    "app": (".main", "app"),
    "config": (".main", "config"),
    "create_app": (".main", "create_app"),
    "router": (".main", "router"),
    "SERVER_HOST": (".main", "SERVER_HOST"),
    "SERVER_PORT": (".main", "SERVER_PORT"),
    "Backend": (".core", "Backend"),
    "BackendRetryableError": (".core", "BackendRetryableError"),
    "ProxyRouter": (".core", "ProxyRouter"),
    "load_config": (".config_loader", "load_config"),
    "RequestLogRecorder": (".logging", "RequestLogRecorder"),
    "logger": (".logging", "logger"),
    "setup_logging": (".logging", "setup_logging"),
}


def __getattr__(name: str) -> Any:
    target = _LAZY_EXPORTS.get(name)
    if target is None:
        raise AttributeError(f"module {__name__!r} has no attribute {name!r}")
    module_name, attr = target
    module = importlib.import_module(module_name, __name__)
    value = getattr(module, attr)
    globals()[name] = value
    return value


def __dir__() -> list[str]:
    return sorted(set(globals()).union(_LAZY_EXPORTS))


def _try_eager_import() -> None:
    """Populate common exports if dependencies are available."""
    try:
        from .main import app, create_app, config, router, SERVER_HOST, SERVER_PORT
        from .core import Backend, BackendRetryableError, ProxyRouter
        from .config_loader import load_config
        from .logging import RequestLogRecorder, logger, setup_logging
    except ImportError:
        return

    globals().update(
        {
            "app": app,
            "config": config,
            "create_app": create_app,
            "router": router,
            "SERVER_HOST": SERVER_HOST,
            "SERVER_PORT": SERVER_PORT,
            "Backend": Backend,
            "BackendRetryableError": BackendRetryableError,
            "ProxyRouter": ProxyRouter,
            "load_config": load_config,
            "RequestLogRecorder": RequestLogRecorder,
            "logger": logger,
            "setup_logging": setup_logging,
        }
    )


_try_eager_import()
