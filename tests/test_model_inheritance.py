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
            "modules": {
                "upstream": {
                    "enabled": True,
                    "response": ["parse_unparsed"],
                    "parse_unparsed": {"parse_thinking": True},
                }
            },
        }
        derived = {
            "model_name": "derived",
            "extends": "base",
            "modules": {
                "upstream": {
                    "response": ["swap_reasoning_content"],  # Replace list
                    "swap_reasoning_content": {"mode": "reasoning_to_content"},
                }
            },
        }
        all_models = {"base": base, "derived": derived}
        result = _resolve_single_model_inheritance(derived, all_models, [])

        assert result["modules"]["upstream"]["enabled"] is True  # Inherited
        assert result["modules"]["upstream"]["response"] == ["swap_reasoning_content"]  # Replaced
        # Base parser config should still be present from deep merge
        assert result["modules"]["upstream"]["parse_unparsed"]["parse_thinking"] is True
        # Derived parser config should be added
        assert (
            result["modules"]["upstream"]["swap_reasoning_content"]["mode"]
            == "reasoning_to_content"
        )

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
        config_path = tmp_path / "config.yaml"

        _write_yaml(
            config_path,
            {
                "model_list": [
                    {
                        "model_name": "base",
                        "protected": True,
                        "model_params": {
                            "api_base": "http://base.local",
                            "api_key": "base-key",
                        },
                    },
                    {
                        "model_name": "derived",
                        "protected": False,
                        "extends": "base",
                        "model_params": {"api_key": "derived-key"},
                    },
                ]
            },
        )

        store = ConfigStore(config_path=str(config_path))
        runtime = store.get_runtime_config()
        models = runtime.get("model_list", [])

        assert len(models) == 2
        derived = next(m for m in models if m["model_name"] == "derived")
        assert derived["model_params"]["api_base"] == "http://base.local"
        assert derived["model_params"]["api_key"] == "derived-key"
        assert derived.get("editable") is True  # protected=False

    def test_list_models_resolves_inheritance(self, tmp_path: Path) -> None:
        """Test that list_models resolves inheritance."""
        config_path = tmp_path / "config.yaml"

        _write_yaml(
            config_path,
            {
                "model_list": [
                    {
                        "model_name": "base",
                        "protected": True,
                        "model_params": {"api_base": "http://base.local"},
                    },
                    {
                        "model_name": "derived",
                        "protected": False,
                        "extends": "base",
                        "model_params": {"extra": "value"},
                    },
                ]
            },
        )

        store = ConfigStore(config_path=str(config_path))
        protected_models, unprotected_models = store.list_models()

        assert len(protected_models) == 1
        assert len(unprotected_models) == 1

        derived = unprotected_models[0]
        assert derived["model_name"] == "derived"
        assert derived["model_params"]["api_base"] == "http://base.local"
        assert derived["model_params"]["extra"] == "value"

    def test_model_extends_base_model(self, tmp_path: Path) -> None:
        """Test that models can extend other models."""
        config_path = tmp_path / "config.yaml"

        _write_yaml(
            config_path,
            {
                "model_list": [
                    {
                        "model_name": "GLM-4.7",
                        "protected": True,
                        "model_params": {
                            "api_type": "openai",
                            "model": "GLM-4.7",
                            "api_base": "https://api.z.ai/api/coding/paas/v4",
                            "api_key": "test-key",
                        },
                        "modules": {
                            "upstream": {
                                "enabled": True,
                                "response": ["swap_reasoning_content"],
                            }
                        },
                    },
                    {
                        "model_name": "GLM-4.7:Cursor",
                        "protected": False,
                        "extends": "GLM-4.7",
                        "modules": {
                            "upstream": {
                                "response": ["parse_unparsed", "swap_reasoning_content"],
                            }
                        },
                    },
                ]
            },
        )

        store = ConfigStore(config_path=str(config_path))
        runtime = store.get_runtime_config()
        models = runtime.get("model_list", [])

        cursor_model = next(m for m in models if m["model_name"] == "GLM-4.7:Cursor")
        assert cursor_model["model_params"]["api_base"] == "https://api.z.ai/api/coding/paas/v4"
        assert cursor_model["modules"]["upstream"]["enabled"] is True
        assert cursor_model["modules"]["upstream"]["response"] == [
            "parse_unparsed",
            "swap_reasoning_content",
        ]

    def test_list_models_without_inheritance_resolution(self, tmp_path: Path) -> None:
        """Test that list_models can skip inheritance resolution."""
        config_path = tmp_path / "config.yaml"

        _write_yaml(
            config_path,
            {
                "model_list": [
                    {
                        "model_name": "base",
                        "protected": True,
                        "model_params": {"api_base": "http://base.local"},
                    },
                    {
                        "model_name": "derived",
                        "protected": False,
                        "extends": "base",
                    },
                ]
            },
        )

        store = ConfigStore(config_path=str(config_path))
        _, unprotected_models = store.list_models(resolve_inheritance=False)

        derived = unprotected_models[0]
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
        config_path = tmp_path / "config.yaml"

        _write_yaml(
            config_path,
            {
                "model_list": [
                    {
                        "model_name": "base-model",
                        "protected": True,
                        "model_params": {
                            "api_type": "openai",
                            "api_base": "http://base.local/v1",
                            "api_key": "base-key",
                            "request_timeout": 120,
                        },
                    },
                    {
                        "model_name": "derived-model",
                        "protected": False,
                        "extends": "base-model",
                        "model_params": {
                            "api_key": "derived-key",
                        },
                    },
                ]
            },
        )

        store = ConfigStore(config_path=str(config_path))
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

    def test_router_with_inherited_modules(self, tmp_path: Path) -> None:
        """Test that router correctly handles inherited module configuration."""
        config_path = tmp_path / "config.yaml"

        _write_yaml(
            config_path,
            {
                "model_list": [
                    {
                        "model_name": "base-model",
                        "protected": True,
                        "model_params": {
                            "api_base": "http://base.local/v1",
                            "api_key": "base-key",
                        },
                        "modules": {
                            "upstream": {
                                "enabled": True,
                                "response": ["swap_reasoning_content"],
                                "swap_reasoning_content": {
                                    "mode": "reasoning_to_content",
                                },
                            }
                        },
                    },
                    {
                        "model_name": "derived-model",
                        "protected": False,
                        "extends": "base-model",
                        "modules": {
                            "upstream": {
                                "response": ["parse_unparsed", "swap_reasoning_content"],
                            }
                        },
                    },
                ]
            },
        )

        store = ConfigStore(config_path=str(config_path))
        config = store.get_runtime_config()

        router = ProxyRouter(config)

        # Derived model should have parser overrides
        assert "derived-model" in router.response_module_overrides
        derived_parser = router.response_module_overrides["derived-model"]
        # The module pipeline should have 2 modules
        assert len(derived_parser.parsers) == 2

    def test_router_with_inherited_parameters(self, tmp_path: Path) -> None:
        """Test that router correctly handles inherited parameter overrides."""
        config_path = tmp_path / "config.yaml"

        _write_yaml(
            config_path,
            {
                "model_list": [
                    {
                        "model_name": "base-model",
                        "protected": True,
                        "model_params": {
                            "api_base": "http://base.local/v1",
                            "api_key": "base-key",
                        },
                        "parameters": {
                            "temperature": {"default": 1.0, "allow_override": False},
                            "top_p": {"default": 0.95, "allow_override": False},
                        },
                    },
                    {
                        "model_name": "derived-model",
                        "protected": False,
                        "extends": "base-model",
                        "parameters": {
                            "temperature": {"default": 0.7},  # Override temperature
                        },
                    },
                ]
            },
        )

        store = ConfigStore(config_path=str(config_path))
        config = store.get_runtime_config()

        router = ProxyRouter(config)

        derived_backend = router.backends["derived-model"]
        # Temperature should be overridden
        assert derived_backend.parameters["temperature"].default == 0.7
        # allow_override should be inherited
        assert derived_backend.parameters["temperature"].allow_override is False
        # top_p should be inherited
        assert derived_backend.parameters["top_p"].default == 0.95


class TestDynamicInheritance:
    """Tests for dynamic inheritance behavior.

    These tests verify that changes to base models are reflected in derived models.
    Currently, inheritance is resolved statically at config load time.

    Note: These tests have mixed results because get_runtime_config() re-resolves
    inheritance on each call, so SOME dynamic behavior works, but the underlying
    issue is that 'extends' relationships are not preserved after resolution.
    """

    def test_updates_to_base_model_propagate_to_derived(self, tmp_path: Path) -> None:
        """Test that updating a base model updates derived models."""
        config_path = tmp_path / "config.yaml"

        # Initial config with base and derived model
        _write_yaml(
            config_path,
            {
                "model_list": [
                    {
                        "model_name": "base-model",
                        "protected": True,
                        "model_params": {
                            "api_base": "http://base.local/v1",
                            "api_key": "base-key",
                            "request_timeout": 120,
                        },
                    },
                    {
                        "model_name": "derived-model",
                        "protected": False,
                        "extends": "base-model",
                    },
                ]
            },
        )

        store = ConfigStore(config_path=str(config_path))

        # Initial load - derived model should inherit timeout from base
        initial_config = store.get_runtime_config()
        derived = next(m for m in initial_config["model_list"] if m["model_name"] == "derived-model")
        initial_timeout = derived["model_params"]["request_timeout"]
        assert initial_timeout == 120, "Derived model should inherit initial timeout from base"

        # Verify 'extends' is removed after resolution (this is the static behavior)
        assert "extends" not in derived, (
            "After resolution, 'extends' field should be removed. "
            "This means no true dynamic inheritance is possible."
        )

        # Update the base model to have a different timeout
        base_entry = {
            "model_name": "base-model",
            "protected": True,
            "model_params": {
                "api_base": "http://base.local/v1",
                "api_key": "base-key",
                "request_timeout": 300,  # Changed from 120 to 300
            },
        }
        store.upsert_model(base_entry, None)

        # Reload to pick up changes
        store.reload()

        # Get the updated config
        updated_config = store.get_runtime_config()

        # Find the derived model after base update
        derived_after = next(
            (m for m in updated_config["model_list"] if m["model_name"] == "derived-model"),
            None
        )
        assert derived_after is not None, "Derived model should still exist"
        updated_timeout = derived_after["model_params"]["request_timeout"]

        # This passes because get_runtime_config() re-resolves each time
        # But the underlying issue is that 'extends' is NOT preserved
        assert updated_timeout == 300, (
            f"Expected timeout=300, got {updated_timeout}"
        )

    def test_base_model_deletion_affects_derived(self, tmp_path: Path) -> None:
        """Test that deleting a base model affects derived models."""
        config_path = tmp_path / "config.yaml"

        _write_yaml(
            config_path,
            {
                "model_list": [
                    {
                        "model_name": "base-model",
                        "protected": True,
                        "model_params": {
                            "api_base": "http://base.local/v1",
                            "api_key": "base-key",
                        },
                    },
                    {
                        "model_name": "derived-model",
                        "protected": False,
                        "extends": "base-model",
                    },
                ]
            },
        )

        store = ConfigStore(config_path=str(config_path))

        # Verify initial state - derived model should exist with inherited values
        initial_config = store.get_runtime_config()
        derived = next(
            (m for m in initial_config["model_list"] if m["model_name"] == "derived-model"),
            None
        )
        assert derived is not None, "Derived model should exist initially"
        assert derived["model_params"]["api_base"] == "http://base.local/v1"

        # Delete the base model from config
        config = store.get_raw()
        config["model_list"] = [
            m for m in config.get("model_list", []) if m.get("model_name") != "base-model"
        ]
        store.save(config)

        # Reload to pick up changes
        store.reload()

        # Check if derived model still exists and what values it has
        updated_config = store.get_runtime_config()
        derived = next(
            (m for m in updated_config["model_list"] if m["model_name"] == "derived-model"),
            None
        )

        # Derived model remains unresolved when base is deleted
        assert derived is not None
        assert derived.get("extends") == "base-model"
