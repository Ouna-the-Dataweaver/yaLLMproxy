"""Tests for admin config masking helper."""

import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).parent.parent / "src"))

from src.api.routes import config as config_routes


def test_mask_sensitive_data_masks_long_api_key():
    data = {"api_key": "abcd1234efgh5678"}
    masked = config_routes._mask_sensitive_data(data)
    assert masked["api_key"] == "abcd****5678"


def test_mask_sensitive_data_does_not_mask_short_key():
    data = {"API_KEY": "shortkey"}
    masked = config_routes._mask_sensitive_data(data)
    assert masked["API_KEY"] == "shortkey"


def test_mask_sensitive_data_recurses_and_preserves_non_strings():
    data = {
        "nested": [{"api_key": "abcd1234efgh5678"}, {"api_key": None}],
        "api_key": 1234,
    }
    masked = config_routes._mask_sensitive_data(data)
    assert masked["nested"][0]["api_key"] == "abcd****5678"
    assert masked["nested"][1]["api_key"] is None
    assert masked["api_key"] == 1234
