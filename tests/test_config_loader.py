"""Tests for the config loader module."""

import os
import sys
import tempfile
from pathlib import Path

import pytest
import yaml

# Add src directory to path
sys.path.insert(0, str(Path(__file__).parent.parent / "src"))

from config_loader import load_config, _substitute_env_vars


def _write_temp_yaml(data) -> str:
    fd, path = tempfile.mkstemp(suffix=".yaml")
    with os.fdopen(fd, "w", encoding="utf-8") as handle:
        if isinstance(data, str):
            handle.write(data)
        else:
            yaml.safe_dump(data, handle)
    return path


class TestLoadConfig:
    """Tests for loading configuration from YAML files."""

    def test_loads_simple_config(self):
        """Test loading a simple configuration."""
        config_data = {
            "model_list": [
                {
                    "model_name": "test-model",
                    "model_params": {
                        "api_base": "http://test.local/v1",
                        "api_key": "test-key",
                    },
                }
            ]
        }
        
        path = _write_temp_yaml(config_data)
        try:
            result = load_config(path)
            assert result["model_list"][0]["model_name"] == "test-model"
        finally:
            os.unlink(path)

    def test_raises_error_for_missing_config(self):
        """Test that error is raised for missing config file."""
        with pytest.raises(RuntimeError, match="Config file not found"):
            load_config("/nonexistent/path/config.yaml")

    def test_substitutes_environment_variables(self):
        """Test that environment variables are substituted."""
        os.environ["TEST_API_KEY"] = "my-secret-key"
        
        config_data = {
            "model_list": [
                {
                    "model_name": "test-model",
                    "model_params": {
                        "api_base": "http://test.local/v1",
                        "api_key": "${TEST_API_KEY}",
                    },
                }
            ]
        }
        
        path = _write_temp_yaml(config_data)
        try:
            result = load_config(path)
            assert result["model_list"][0]["model_params"]["api_key"] == "my-secret-key"
        finally:
            os.unlink(path)
            del os.environ["TEST_API_KEY"]

    def test_env_file_overrides_os_env(self, tmp_path):
        """Test that config-specific env files override os.environ values."""
        os.environ["TEST_API_KEY"] = "from-os"
        config_path = tmp_path / "config.yaml"
        env_path = tmp_path / ".env"
        env_path.write_text("TEST_API_KEY=from-env\n", encoding="utf-8")
        config_data = {
            "model_list": [
                {
                    "model_name": "test-model",
                    "model_params": {
                        "api_base": "http://test.local/v1",
                        "api_key": "${TEST_API_KEY}",
                    },
                }
            ]
        }
        config_path.write_text(yaml.safe_dump(config_data), encoding="utf-8")
        try:
            result = load_config(str(config_path))
            assert result["model_list"][0]["model_params"]["api_key"] == "from-env"
        finally:
            del os.environ["TEST_API_KEY"]

    def test_substitutes_simple_env_var_syntax(self):
        """Test that $VAR syntax is also substituted."""
        os.environ["SIMPLE_VAR"] = "simple-value"
        
        config_data = {
            "model_list": [
                {
                    "model_name": "test-model",
                    "model_params": {
                        "api_base": "http://test.local/v1",
                        "api_key": "$SIMPLE_VAR",
                    },
                }
            ]
        }
        
        path = _write_temp_yaml(config_data)
        try:
            result = load_config(path)
            assert result["model_list"][0]["model_params"]["api_key"] == "simple-value"
        finally:
            os.unlink(path)
            del os.environ["SIMPLE_VAR"]

    def test_preserves_undefined_env_vars(self):
        """Test that undefined environment variables are preserved."""
        config_data = {
            "model_list": [
                {
                    "model_name": "test-model",
                    "model_params": {
                        "api_base": "http://test.local/v1",
                        "api_key": "${UNDEFINED_VAR}",
                    },
                }
            ]
        }
        
        path = _write_temp_yaml(config_data)
        try:
            result = load_config(path)
            assert result["model_list"][0]["model_params"]["api_key"] == "${UNDEFINED_VAR}"
        finally:
            os.unlink(path)

    def test_loads_empty_config(self):
        """Test loading an empty configuration."""
        path = _write_temp_yaml("")
        try:
            result = load_config(path)
            assert result == {}
        finally:
            os.unlink(path)

    def test_substitutes_in_nested_config(self):
        """Test that environment variables are substituted in nested values."""
        os.environ["NESTED_KEY"] = "nested-value"
        
        config_data = {
            "general_settings": {
                "server": {
                    "host": "0.0.0.0",
                    "port": "${NESTED_KEY}",
                }
            }
        }
        
        path = _write_temp_yaml(config_data)
        try:
            result = load_config(path)
            assert result["general_settings"]["server"]["port"] == "nested-value"
        finally:
            os.unlink(path)
            del os.environ["NESTED_KEY"]

    def test_substitutes_in_list_values(self):
        """Test that environment variables are substituted in list values."""
        os.environ["LIST_VAR"] = "list-value"
        
        config_data = {
            "router_settings": {
                "fallbacks": [
                    {"model1": ["${LIST_VAR}"]}
                ]
            }
        }
        
        path = _write_temp_yaml(config_data)
        try:
            result = load_config(path)
            assert result["router_settings"]["fallbacks"][0]["model1"][0] == "list-value"
        finally:
            os.unlink(path)
            del os.environ["LIST_VAR"]


class TestSubstituteEnvVars:
    """Tests for environment variable substitution function."""

    def test_substitutes_in_string(self):
        """Test substitution in a string."""
        os.environ["TEST_VAR"] = "test-value"
        result = _substitute_env_vars("prefix-${TEST_VAR}-suffix")
        assert result == "prefix-test-value-suffix"
        del os.environ["TEST_VAR"]

    def test_prefers_env_values_mapping(self):
        """Test substitution uses provided env mapping first."""
        result = _substitute_env_vars("${MAPPED_VAR}", {"MAPPED_VAR": "mapped"})
        assert result == "mapped"

    def test_substitutes_multiple_vars(self):
        """Test substitution of multiple variables."""
        os.environ["VAR1"] = "value1"
        os.environ["VAR2"] = "value2"
        result = _substitute_env_vars("${VAR1} and ${VAR2}")
        assert result == "value1 and value2"
        del os.environ["VAR1"]
        del os.environ["VAR2"]

    def test_handles_dict(self):
        """Test substitution in dictionary."""
        os.environ["KEY"] = "value"
        result = _substitute_env_vars({"key": "${KEY}"})
        assert result["key"] == "value"
        del os.environ["KEY"]

    def test_handles_list(self):
        """Test substitution in list."""
        os.environ["ITEM"] = "item-value"
        result = _substitute_env_vars(["${ITEM}"])
        assert result[0] == "item-value"
        del os.environ["ITEM"]

    def test_passes_through_non_string_values(self):
        """Test that non-string values are passed through unchanged."""
        result = _substitute_env_vars(123)
        assert result == 123
        
        result = _substitute_env_vars(None)
        assert result is None
        
        result = _substitute_env_vars([1, 2, 3])
        assert result == [1, 2, 3]
