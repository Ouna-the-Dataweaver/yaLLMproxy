"""Logging module for the proxy."""

from .recorder import (
    RequestLogRecorder,
    log_error_event,
    set_db_logging_enabled,
    is_db_logging_enabled,
    DbLogTarget,
    resolve_db_log_target,
)
from .setup import (
    logger,
    setup_logging,
    reconfigure_logging,
    setup_forwarder_logging,
    CONSOLE_LOG_PATH,
    TCP_FORWARDER_LOG_PATH,
    HTTP_FORWARDER_LOG_PATH,
    LOG_FORMAT,
    LOG_DATE_FORMAT,
)

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
    "reconfigure_logging",
    "setup_forwarder_logging",
    "CONSOLE_LOG_PATH",
    "TCP_FORWARDER_LOG_PATH",
    "HTTP_FORWARDER_LOG_PATH",
    "LOG_FORMAT",
    "LOG_DATE_FORMAT",
    "RequestLogRecorder",
    "log_error_event",
    "set_db_logging_enabled",
    "is_db_logging_enabled",
    "DbLogTarget",
    "resolve_db_log_target",
]

if _db_logger_available:
    __all__.append("get_db_logger")
