"""Tests for the config store behavior."""

import sys
from pathlib import Path

import pytest
import yaml

# Add src directory to path
sys.path.insert(0, str(Path(__file__).parent.parent / "src"))

from config_store import ConfigStore


def _write_yaml(path: Path, data: dict) -> None:
    path.write_text(yaml.safe_dump(data), encoding="utf-8")


def test_runtime_config_merges_and_marks_editable(tmp_path: Path) -> None:
    default_path = tmp_path / "config_default.yaml"
    added_path = tmp_path / "config_added.yaml"
    _write_yaml(
        default_path,
        {
            "model_list": [
                {
                    "model_name": "alpha",
                    "model_params": {"api_base": "http://alpha", "api_key": "a"},
                }
            ]
        },
    )
    _write_yaml(
        added_path,
        {
            "model_list": [
                {
                    "model_name": "beta",
                    "model_params": {"api_base": "http://beta", "api_key": "b"},
                }
            ]
        },
    )
    store = ConfigStore(default_path=str(default_path), added_path=str(added_path))
    runtime = store.get_runtime_config()
    models = runtime.get("model_list", [])
    assert len(models) == 2
    editable_by_name = {m["model_name"]: m.get("editable") for m in models}
    assert editable_by_name["alpha"] is False
    assert editable_by_name["beta"] is True


def test_upsert_added_model_persists(tmp_path: Path) -> None:
    default_path = tmp_path / "config_default.yaml"
    added_path = tmp_path / "config_added.yaml"
    _write_yaml(default_path, {"model_list": []})
    _write_yaml(added_path, {"model_list": []})
    store = ConfigStore(default_path=str(default_path), added_path=str(added_path))

    model_entry = {
        "model_name": "gamma",
        "model_params": {"api_base": "http://gamma", "api_key": "g"},
    }
    replaced = store.upsert_added_model(model_entry, None)
    assert replaced is False

    data = yaml.safe_load(added_path.read_text(encoding="utf-8"))
    assert data["model_list"][0]["model_name"] == "gamma"

    replaced = store.upsert_added_model(model_entry, None)
    assert replaced is True


class TestCopyModel:
    """Tests for the copy_model functionality."""

    def test_copy_model_creates_new_entry(self, tmp_path: Path) -> None:
        """Test that copy_model creates a new model entry."""
        default_path = tmp_path / "config_default.yaml"
        added_path = tmp_path / "config_added.yaml"
        _write_yaml(
            default_path,
            {
                "model_list": [
                    {
                        "model_name": "source-model",
                        "model_params": {
                            "api_base": "http://source.local",
                            "api_key": "source-key",
                        },
                    }
                ]
            },
        )
        _write_yaml(added_path, {"model_list": []})

        store = ConfigStore(default_path=str(default_path), added_path=str(added_path))
        new_model = store.copy_model("source-model", "copied-model")

        assert new_model["model_name"] == "copied-model"
        assert new_model["model_params"]["api_base"] == "http://source.local"
        assert new_model["model_params"]["api_key"] == "source-key"

        # Verify it was persisted
        data = yaml.safe_load(added_path.read_text(encoding="utf-8"))
        assert len(data["model_list"]) == 1
        assert data["model_list"][0]["model_name"] == "copied-model"

    def test_copy_model_from_added_config(self, tmp_path: Path) -> None:
        """Test copying a model from the added config."""
        default_path = tmp_path / "config_default.yaml"
        added_path = tmp_path / "config_added.yaml"
        _write_yaml(default_path, {"model_list": []})
        _write_yaml(
            added_path,
            {
                "model_list": [
                    {
                        "model_name": "added-model",
                        "model_params": {"api_base": "http://added.local"},
                    }
                ]
            },
        )

        store = ConfigStore(default_path=str(default_path), added_path=str(added_path))
        new_model = store.copy_model("added-model", "copied-model")

        assert new_model["model_name"] == "copied-model"
        assert new_model["model_params"]["api_base"] == "http://added.local"

    def test_copy_model_source_not_found_raises(self, tmp_path: Path) -> None:
        """Test appropriate error when source model not found."""
        default_path = tmp_path / "config_default.yaml"
        added_path = tmp_path / "config_added.yaml"
        _write_yaml(default_path, {"model_list": []})
        _write_yaml(added_path, {"model_list": []})

        store = ConfigStore(default_path=str(default_path), added_path=str(added_path))

        with pytest.raises(ValueError, match="not found"):
            store.copy_model("nonexistent", "new-model")

    def test_copy_model_target_exists_raises(self, tmp_path: Path) -> None:
        """Test error when target model name already exists."""
        default_path = tmp_path / "config_default.yaml"
        added_path = tmp_path / "config_added.yaml"
        _write_yaml(
            default_path,
            {
                "model_list": [
                    {
                        "model_name": "source-model",
                        "model_params": {"api_base": "http://source.local"},
                    },
                    {
                        "model_name": "existing-model",
                        "model_params": {"api_base": "http://existing.local"},
                    },
                ]
            },
        )
        _write_yaml(added_path, {"model_list": []})

        store = ConfigStore(default_path=str(default_path), added_path=str(added_path))

        with pytest.raises(ValueError, match="already exists"):
            store.copy_model("source-model", "existing-model")

    def test_copy_model_preserves_all_settings(self, tmp_path: Path) -> None:
        """Test that all settings are preserved during copy."""
        default_path = tmp_path / "config_default.yaml"
        added_path = tmp_path / "config_added.yaml"
        _write_yaml(
            default_path,
            {
                "model_list": [
                    {
                        "model_name": "full-model",
                        "model_params": {
                            "api_type": "openai",
                            "api_base": "http://full.local",
                            "api_key": "full-key",
                            "request_timeout": 120,
                            "parameters": {
                                "temperature": {"default": 0.7, "allow_override": False},
                            },
                        },
                        "parsers": {
                            "enabled": True,
                            "response": ["swap_reasoning_content"],
                        },
                    }
                ]
            },
        )
        _write_yaml(added_path, {"model_list": []})

        store = ConfigStore(default_path=str(default_path), added_path=str(added_path))
        new_model = store.copy_model("full-model", "copied-full")

        assert new_model["model_name"] == "copied-full"
        assert new_model["model_params"]["api_type"] == "openai"
        assert new_model["model_params"]["api_base"] == "http://full.local"
        assert new_model["model_params"]["request_timeout"] == 120
        assert new_model["model_params"]["parameters"]["temperature"]["default"] == 0.7
        assert new_model["parsers"]["enabled"] is True
        assert new_model["parsers"]["response"] == ["swap_reasoning_content"]

    def test_copy_model_removes_metadata_fields(self, tmp_path: Path) -> None:
        """Test that metadata fields are not copied."""
        default_path = tmp_path / "config_default.yaml"
        added_path = tmp_path / "config_added.yaml"
        _write_yaml(
            default_path,
            {
                "model_list": [
                    {
                        "model_name": "source",
                        "model_params": {"api_base": "http://source.local"},
                        "editable": False,
                        "source": "default",
                        "_inherited_from": "some-base",
                    }
                ]
            },
        )
        _write_yaml(added_path, {"model_list": []})

        store = ConfigStore(default_path=str(default_path), added_path=str(added_path))
        new_model = store.copy_model("source", "copied")

        assert "editable" not in new_model
        assert "source" not in new_model
        assert "_inherited_from" not in new_model

    def test_find_model_returns_model(self, tmp_path: Path) -> None:
        """Test that find_model returns the model entry."""
        default_path = tmp_path / "config_default.yaml"
        added_path = tmp_path / "config_added.yaml"
        _write_yaml(
            default_path,
            {
                "model_list": [
                    {
                        "model_name": "default-model",
                        "model_params": {"api_base": "http://default.local"},
                    }
                ]
            },
        )
        _write_yaml(
            added_path,
            {
                "model_list": [
                    {
                        "model_name": "added-model",
                        "model_params": {"api_base": "http://added.local"},
                    }
                ]
            },
        )

        store = ConfigStore(default_path=str(default_path), added_path=str(added_path))

        # Find from default
        model = store.find_model("default-model")
        assert model is not None
        assert model["model_name"] == "default-model"

        # Find from added
        model = store.find_model("added-model")
        assert model is not None
        assert model["model_name"] == "added-model"

        # Not found
        model = store.find_model("nonexistent")
        assert model is None
