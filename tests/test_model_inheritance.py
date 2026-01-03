"""Tests for model inheritance resolution in config_store."""

import sys
from pathlib import Path

import pytest
import yaml

# Add src directory to path
sys.path.insert(0, str(Path(__file__).parent.parent / "src"))

from config_store import (
    ConfigStore,
    ModelInheritanceError,
    _deep_merge_dicts,
    _resolve_single_model_inheritance,
    _resolve_all_model_inheritance,
)
from src.core.router import ProxyRouter


def _write_yaml(path: Path, data: dict) -> None:
    path.write_text(yaml.safe_dump(data), encoding="utf-8")


class TestDeepMergeDicts:
    """Tests for the _deep_merge_dicts helper function."""

    def test_simple_merge(self):
        """Test merging two flat dictionaries."""
        base = {"a": 1, "b": 2}
        override = {"b": 3, "c": 4}
        result = _deep_merge_dicts(base, override)
        assert result == {"a": 1, "b": 3, "c": 4}

    def test_nested_merge(self):
        """Test deep merging of nested dictionaries."""
        base = {"outer": {"a": 1, "b": 2}}
        override = {"outer": {"b": 3, "c": 4}}
        result = _deep_merge_dicts(base, override)
        assert result == {"outer": {"a": 1, "b": 3, "c": 4}}

    def test_list_replace_not_merge(self):
        """Test that lists are replaced, not merged."""
        base = {"items": [1, 2, 3]}
        override = {"items": [4, 5]}
        result = _deep_merge_dicts(base, override)
        assert result == {"items": [4, 5]}

    def test_does_not_modify_inputs(self):
        """Test that input dictionaries are not modified."""
        base = {"a": {"nested": 1}}
        override = {"a": {"other": 2}}
        _deep_merge_dicts(base, override)
        assert base == {"a": {"nested": 1}}
        assert override == {"a": {"other": 2}}


class TestResolveModelInheritance:
    """Tests for model inheritance resolution."""

    def test_no_inheritance(self):
        """Test that models without 'extends' are returned as-is."""
        model = {
            "model_name": "test-model",
            "model_params": {"api_base": "http://test.local"},
        }
        all_models = {"test-model": model}
        result = _resolve_single_model_inheritance(model, all_models, [])
        assert result["model_name"] == "test-model"
        assert result["model_params"]["api_base"] == "http://test.local"
        assert "extends" not in result

    def test_simple_inheritance(self):
        """Test that a model with 'extends' inherits from base model."""
        base = {
            "model_name": "base-model",
            "model_params": {
                "api_base": "http://base.local",
                "api_key": "base-key",
                "request_timeout": 60,
            },
        }
        derived = {
            "model_name": "derived-model",
            "extends": "base-model",
            "model_params": {
                "api_key": "derived-key",  # Override
            },
        }
        all_models = {"base-model": base, "derived-model": derived}
        result = _resolve_single_model_inheritance(derived, all_models, [])

        assert result["model_name"] == "derived-model"
        assert result["model_params"]["api_base"] == "http://base.local"  # Inherited
        assert result["model_params"]["api_key"] == "derived-key"  # Overridden
        assert result["model_params"]["request_timeout"] == 60  # Inherited
        assert "extends" not in result
        assert result.get("_inherited_from") == "base-model"

    def test_chained_inheritance(self):
        """Test that inheritance chains resolve correctly (A extends B extends C)."""
        grandparent = {
            "model_name": "grandparent",
            "model_params": {"api_base": "http://gp.local", "timeout": 100},
        }
        parent = {
            "model_name": "parent",
            "extends": "grandparent",
            "model_params": {"api_key": "parent-key"},
        }
        child = {
            "model_name": "child",
            "extends": "parent",
            "model_params": {"timeout": 200},  # Override grandparent's timeout
        }
        all_models = {
            "grandparent": grandparent,
            "parent": parent,
            "child": child,
        }
        result = _resolve_single_model_inheritance(child, all_models, [])

        assert result["model_name"] == "child"
        assert result["model_params"]["api_base"] == "http://gp.local"  # From grandparent
        assert result["model_params"]["api_key"] == "parent-key"  # From parent
        assert result["model_params"]["timeout"] == 200  # Overridden by child

    def test_circular_reference_detection(self):
        """Test that circular references are detected and raise error."""
        model_a = {"model_name": "A", "extends": "B"}
        model_b = {"model_name": "B", "extends": "A"}
        all_models = {"A": model_a, "B": model_b}

        with pytest.raises(ModelInheritanceError, match="Circular inheritance"):
            _resolve_single_model_inheritance(model_a, all_models, [])

    def test_self_reference_detection(self):
        """Test that self-reference is detected."""
        model = {"model_name": "self-ref", "extends": "self-ref"}
        all_models = {"self-ref": model}

        with pytest.raises(ModelInheritanceError, match="Circular inheritance"):
            _resolve_single_model_inheritance(model, all_models, [])

    def test_missing_base_model(self):
        """Test appropriate error when base model not found."""
        derived = {
            "model_name": "derived",
            "extends": "nonexistent",
        }
        all_models = {"derived": derived}

        with pytest.raises(ModelInheritanceError, match="base model not found"):
            _resolve_single_model_inheritance(derived, all_models, [])

    def test_parser_config_inheritance(self):
        """Test that parser configuration is properly inherited and can be overridden."""
        base = {
            "model_name": "base",
            "model_params": {"api_base": "http://base.local"},
            "parsers": {
                "enabled": True,
                "response": ["parse_unparsed"],
                "parse_unparsed": {"parse_thinking": True},
            },
        }
        derived = {
            "model_name": "derived",
            "extends": "base",
            "parsers": {
                "response": ["swap_reasoning_content"],  # Replace list
                "swap_reasoning_content": {"mode": "reasoning_to_content"},
            },
        }
        all_models = {"base": base, "derived": derived}
        result = _resolve_single_model_inheritance(derived, all_models, [])

        assert result["parsers"]["enabled"] is True  # Inherited
        assert result["parsers"]["response"] == ["swap_reasoning_content"]  # Replaced
        # Base parser config should still be present from deep merge
        assert result["parsers"]["parse_unparsed"]["parse_thinking"] is True
        # Derived parser config should be added
        assert result["parsers"]["swap_reasoning_content"]["mode"] == "reasoning_to_content"

    def test_parameter_overrides_inheritance(self):
        """Test that parameter overrides from base are inherited and can be overridden."""
        base = {
            "model_name": "base",
            "model_params": {
                "api_base": "http://base.local",
                "parameters": {
                    "temperature": {"default": 1.0, "allow_override": False},
                    "top_p": {"default": 0.95, "allow_override": False},
                },
            },
        }
        derived = {
            "model_name": "derived",
            "extends": "base",
            "model_params": {
                "parameters": {
                    "temperature": {"default": 0.7},  # Partially override
                },
            },
        }
        all_models = {"base": base, "derived": derived}
        result = _resolve_single_model_inheritance(derived, all_models, [])

        params = result["model_params"]["parameters"]
        # Temperature should have merged values
        assert params["temperature"]["default"] == 0.7  # Overridden
        assert params["temperature"]["allow_override"] is False  # Inherited
        # Top_p should be fully inherited
        assert params["top_p"]["default"] == 0.95
        assert params["top_p"]["allow_override"] is False


class TestResolveAllModelInheritance:
    """Tests for resolving inheritance across all models."""

    def test_resolves_all_models(self):
        """Test that inheritance is resolved for all models in the list."""
        models = [
            {"model_name": "base", "model_params": {"api_base": "http://base.local"}},
            {
                "model_name": "derived",
                "extends": "base",
                "model_params": {"extra": "value"},
            },
        ]
        result = _resolve_all_model_inheritance(models)

        assert len(result) == 2
        derived = next(m for m in result if m["model_name"] == "derived")
        assert derived["model_params"]["api_base"] == "http://base.local"
        assert derived["model_params"]["extra"] == "value"

    def test_empty_list(self):
        """Test that empty list returns empty list."""
        result = _resolve_all_model_inheritance([])
        assert result == []

    def test_skips_non_dict_entries(self):
        """Test that non-dict entries are skipped."""
        models = [
            {"model_name": "valid", "model_params": {"api_base": "http://test.local"}},
            "invalid",
            None,
        ]
        result = _resolve_all_model_inheritance(models)
        assert len(result) == 1


class TestConfigStoreInheritance:
    """Integration tests for ConfigStore with inheritance."""

    def test_runtime_config_resolves_inheritance(self, tmp_path: Path) -> None:
        """Test that get_runtime_config resolves inheritance."""
        default_path = tmp_path / "config_default.yaml"
        added_path = tmp_path / "config_added.yaml"

        _write_yaml(
            default_path,
            {
                "model_list": [
                    {
                        "model_name": "base",
                        "model_params": {
                            "api_base": "http://base.local",
                            "api_key": "base-key",
                        },
                    }
                ]
            },
        )
        _write_yaml(
            added_path,
            {
                "model_list": [
                    {
                        "model_name": "derived",
                        "extends": "base",
                        "model_params": {"api_key": "derived-key"},
                    }
                ]
            },
        )

        store = ConfigStore(default_path=str(default_path), added_path=str(added_path))
        runtime = store.get_runtime_config()
        models = runtime.get("model_list", [])

        assert len(models) == 2
        derived = next(m for m in models if m["model_name"] == "derived")
        assert derived["model_params"]["api_base"] == "http://base.local"
        assert derived["model_params"]["api_key"] == "derived-key"
        assert derived.get("editable") is True  # From added config

    def test_list_models_resolves_inheritance(self, tmp_path: Path) -> None:
        """Test that list_models resolves inheritance."""
        default_path = tmp_path / "config_default.yaml"
        added_path = tmp_path / "config_added.yaml"

        _write_yaml(
            default_path,
            {
                "model_list": [
                    {
                        "model_name": "base",
                        "model_params": {"api_base": "http://base.local"},
                    }
                ]
            },
        )
        _write_yaml(
            added_path,
            {
                "model_list": [
                    {
                        "model_name": "derived",
                        "extends": "base",
                        "model_params": {"extra": "value"},
                    }
                ]
            },
        )

        store = ConfigStore(default_path=str(default_path), added_path=str(added_path))
        default_models, added_models = store.list_models()

        assert len(default_models) == 1
        assert len(added_models) == 1

        derived = added_models[0]
        assert derived["model_name"] == "derived"
        assert derived["model_params"]["api_base"] == "http://base.local"
        assert derived["model_params"]["extra"] == "value"

    def test_added_model_extends_default_model(self, tmp_path: Path) -> None:
        """Test that added models can extend default models."""
        default_path = tmp_path / "config_default.yaml"
        added_path = tmp_path / "config_added.yaml"

        _write_yaml(
            default_path,
            {
                "model_list": [
                    {
                        "model_name": "GLM-4.7",
                        "model_params": {
                            "api_type": "openai",
                            "model": "GLM-4.7",
                            "api_base": "https://api.z.ai/api/coding/paas/v4",
                            "api_key": "test-key",
                        },
                        "parsers": {
                            "enabled": True,
                            "response": ["swap_reasoning_content"],
                        },
                    }
                ]
            },
        )
        _write_yaml(
            added_path,
            {
                "model_list": [
                    {
                        "model_name": "GLM-4.7:Cursor",
                        "extends": "GLM-4.7",
                        "parsers": {
                            "response": ["parse_unparsed", "swap_reasoning_content"],
                        },
                    }
                ]
            },
        )

        store = ConfigStore(default_path=str(default_path), added_path=str(added_path))
        runtime = store.get_runtime_config()
        models = runtime.get("model_list", [])

        cursor_model = next(m for m in models if m["model_name"] == "GLM-4.7:Cursor")
        assert cursor_model["model_params"]["api_base"] == "https://api.z.ai/api/coding/paas/v4"
        assert cursor_model["parsers"]["enabled"] is True
        assert cursor_model["parsers"]["response"] == [
            "parse_unparsed",
            "swap_reasoning_content",
        ]

    def test_list_models_without_inheritance_resolution(self, tmp_path: Path) -> None:
        """Test that list_models can skip inheritance resolution."""
        default_path = tmp_path / "config_default.yaml"
        added_path = tmp_path / "config_added.yaml"

        _write_yaml(
            default_path,
            {
                "model_list": [
                    {
                        "model_name": "base",
                        "model_params": {"api_base": "http://base.local"},
                    }
                ]
            },
        )
        _write_yaml(
            added_path,
            {
                "model_list": [
                    {
                        "model_name": "derived",
                        "extends": "base",
                    }
                ]
            },
        )

        store = ConfigStore(default_path=str(default_path), added_path=str(added_path))
        _, added_models = store.list_models(resolve_inheritance=False)

        derived = added_models[0]
        # 'extends' should still be present since inheritance wasn't resolved
        assert derived.get("extends") == "base"
        # model_params should NOT have the inherited api_base
        assert "model_params" not in derived or "api_base" not in derived.get(
            "model_params", {}
        )


class TestRouterWithInheritedModels:
    """Tests for ProxyRouter handling inherited models."""

    def test_router_parses_inherited_model(self, tmp_path: Path) -> None:
        """Test that router correctly handles inherited models from config."""
        default_path = tmp_path / "config_default.yaml"
        added_path = tmp_path / "config_added.yaml"

        _write_yaml(
            default_path,
            {
                "model_list": [
                    {
                        "model_name": "base-model",
                        "model_params": {
                            "api_type": "openai",
                            "api_base": "http://base.local/v1",
                            "api_key": "base-key",
                            "request_timeout": 120,
                        },
                    }
                ]
            },
        )
        _write_yaml(
            added_path,
            {
                "model_list": [
                    {
                        "model_name": "derived-model",
                        "extends": "base-model",
                        "model_params": {
                            "api_key": "derived-key",
                        },
                    }
                ]
            },
        )

        store = ConfigStore(default_path=str(default_path), added_path=str(added_path))
        config = store.get_runtime_config()

        router = ProxyRouter(config)

        # Both base and derived models should be registered
        assert "base-model" in router.backends
        assert "derived-model" in router.backends

        # Derived model should have inherited base_url and overridden api_key
        derived_backend = router.backends["derived-model"]
        assert derived_backend.base_url == "http://base.local/v1"
        assert derived_backend.api_key == "derived-key"
        assert derived_backend.timeout == 120

    def test_router_with_inherited_parsers(self, tmp_path: Path) -> None:
        """Test that router correctly handles inherited parser configuration."""
        default_path = tmp_path / "config_default.yaml"
        added_path = tmp_path / "config_added.yaml"

        _write_yaml(
            default_path,
            {
                "model_list": [
                    {
                        "model_name": "base-model",
                        "model_params": {
                            "api_base": "http://base.local/v1",
                            "api_key": "base-key",
                        },
                        "parsers": {
                            "enabled": True,
                            "response": ["swap_reasoning_content"],
                            "swap_reasoning_content": {
                                "mode": "reasoning_to_content",
                            },
                        },
                    }
                ]
            },
        )
        _write_yaml(
            added_path,
            {
                "model_list": [
                    {
                        "model_name": "derived-model",
                        "extends": "base-model",
                        "parsers": {
                            "response": ["parse_unparsed", "swap_reasoning_content"],
                        },
                    }
                ]
            },
        )

        store = ConfigStore(default_path=str(default_path), added_path=str(added_path))
        config = store.get_runtime_config()

        router = ProxyRouter(config)

        # Derived model should have parser overrides
        assert "derived-model" in router.response_parser_overrides
        derived_parser = router.response_parser_overrides["derived-model"]
        # The parser should have 2 parsers in the pipeline
        assert len(derived_parser.parsers) == 2

    def test_router_with_inherited_parameters(self, tmp_path: Path) -> None:
        """Test that router correctly handles inherited parameter overrides."""
        default_path = tmp_path / "config_default.yaml"
        added_path = tmp_path / "config_added.yaml"

        _write_yaml(
            default_path,
            {
                "model_list": [
                    {
                        "model_name": "base-model",
                        "model_params": {
                            "api_base": "http://base.local/v1",
                            "api_key": "base-key",
                        },
                        "parameters": {
                            "temperature": {"default": 1.0, "allow_override": False},
                            "top_p": {"default": 0.95, "allow_override": False},
                        },
                    }
                ]
            },
        )
        _write_yaml(
            added_path,
            {
                "model_list": [
                    {
                        "model_name": "derived-model",
                        "extends": "base-model",
                        "parameters": {
                            "temperature": {"default": 0.7},  # Override temperature
                        },
                    }
                ]
            },
        )

        store = ConfigStore(default_path=str(default_path), added_path=str(added_path))
        config = store.get_runtime_config()

        router = ProxyRouter(config)

        derived_backend = router.backends["derived-model"]
        # Temperature should be overridden
        assert derived_backend.parameters["temperature"].default == 0.7
        # allow_override should be inherited
        assert derived_backend.parameters["temperature"].allow_override is False
        # top_p should be inherited
        assert derived_backend.parameters["top_p"].default == 0.95
