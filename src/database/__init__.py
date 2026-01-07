"""Database support module for yaLLMproxy.

Provides interchangeable SQLite and PostgreSQL database support with JSONB columns.
"""

from .factory import get_database
from .base import DatabaseBase

__all__ = ["get_database", "DatabaseBase"]
