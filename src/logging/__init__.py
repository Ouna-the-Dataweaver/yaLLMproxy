"""Logging module for the proxy."""

from .recorder import RequestLogRecorder, log_error_event
from .setup import logger, setup_logging

__all__ = [
    "logger",
    "setup_logging",
    "RequestLogRecorder",
    "log_error_event",
]

