"""Logging module for the proxy."""

from .recorder import RequestLogRecorder, log_error_event
from .setup import logger, setup_logging

# Database logger integration (optional, may not be available if database module not initialized)
_db_logger_available = False
try:
    from ..database.logger import get_db_logger
    _db_logger_available = True
except ImportError:
    pass

__all__ = [
    "logger",
    "setup_logging",
    "RequestLogRecorder",
    "log_error_event",
]

if _db_logger_available:
    __all__.append("get_db_logger")

