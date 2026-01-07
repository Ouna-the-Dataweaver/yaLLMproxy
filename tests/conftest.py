"""Pytest configuration and fixtures for testing."""

import sys
from pathlib import Path

import pytest

# Add src directory to path for imports
sys.path.insert(0, str(Path(__file__).parent / "src"))


@pytest.fixture(autouse=True)
def disable_database_logging():
    """Disable database logging during all tests to prevent test data from being saved to the database.

    This fixture is automatically used for all tests due to autouse=True.
    It ensures that test requests don't pollute the production database with test model names.
    """
    from src.logging import set_db_logging_enabled

    # Disable database logging before the test
    set_db_logging_enabled(False)

    yield

    # Re-enable database logging after the test (in case subsequent tests need it)
    set_db_logging_enabled(True)
