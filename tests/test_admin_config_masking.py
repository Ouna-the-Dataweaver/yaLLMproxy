"""Tests for admin config masking helper."""

import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).parent.parent / "src"))

from src.api.routes import config as config_routes


def test_mask_sensitive_data_masks_long_api_key():
    data = {"api_key": "abcd1234efgh5678"}
    masked = config_routes._mask_sensitive_data(data)
    assert "api_key" not in masked


def test_mask_sensitive_data_does_not_mask_short_key():
    data = {"API_KEY": "shortkey"}
    masked = config_routes._mask_sensitive_data(data)
    assert "API_KEY" not in masked


def test_mask_sensitive_data_recurses_and_preserves_non_strings():
    data = {
        "nested": [{"api_key": "abcd1234efgh5678"}, {"api_key": None}],
        "api_key": 1234,
    }
    masked = config_routes._mask_sensitive_data(data)
    assert "api_key" not in masked["nested"][0]
    assert "api_key" not in masked["nested"][1]
    assert "api_key" not in masked
