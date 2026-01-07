"""Database models for yaLLMproxy.

Provides SQLAlchemy models for request logs and error logs with JSONB support.
"""

from .base import Base
from .request_log import RequestLog
from .error_log import ErrorLog

__all__ = ["Base", "RequestLog", "ErrorLog"]
