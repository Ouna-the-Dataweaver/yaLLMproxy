"""Tests for the model inheritance tree."""

import sys
from pathlib import Path

import yaml

# Add src directory to path
sys.path.insert(0, str(Path(__file__).parent.parent / "src"))

from config_store import ConfigStore, ModelTree


def _write_yaml(path: Path, data: dict) -> None:
    path.write_text(yaml.safe_dump(data), encoding="utf-8")


class TestModelTree:
    def test_build_and_queries(self) -> None:
        models = [
            {
                "model_name": "base",
                "protected": True,
                "model_params": {"api_base": "http://base.local"},
            },
            {
                "model_name": "derived-1",
                "protected": False,
                "extends": "base",
                "model_params": {"api_key": "key-1"},
            },
            {
                "model_name": "derived-2",
                "protected": False,
                "extends": "base",
                "model_params": {"api_key": "key-2"},
            },
            {
                "model_name": "deep-derived",
                "protected": False,
                "extends": "derived-1",
                "model_params": {"request_timeout": 120},
            },
        ]

        tree = ModelTree()
        tree.build(models)

        assert tree.roots == ["base"]
        assert tree.get_children("base") == ["derived-1", "derived-2"]
        assert tree.get_descendants("base") == ["derived-1", "deep-derived", "derived-2"]
        assert tree.get_ancestors("deep-derived") == ["derived-1", "base"]
        assert tree.get_inheritance_chain("deep-derived") == [
            "deep-derived",
            "derived-1",
            "base",
        ]

        resolved = tree.resolve_model("deep-derived")
        assert resolved is not None
        assert resolved["model_name"] == "deep-derived"
        assert resolved["model_params"]["api_base"] == "http://base.local"
        assert resolved["model_params"]["api_key"] == "key-1"
        assert resolved["model_params"]["request_timeout"] == 120
        assert resolved.get("_inherited_from") == "derived-1"
        assert resolved.get("editable") is True

    def test_missing_parent_is_graceful(self) -> None:
        models = [
            {
                "model_name": "orphan",
                "protected": False,
                "extends": "missing-base",
                "model_params": {"api_base": "http://orphan.local"},
            }
        ]
        tree = ModelTree()
        tree.build(models)
        resolved = tree.resolve_model("orphan")
        assert resolved is not None
        assert resolved.get("extends") == "missing-base"
        assert resolved.get("_inherited_from") == "missing-base"
        assert resolved["model_params"]["api_base"] == "http://orphan.local"


class TestConfigStoreCascadeDelete:
    def test_delete_requires_cascade_when_dependents_exist(self, tmp_path: Path) -> None:
        config_path = tmp_path / "config.yaml"
        _write_yaml(
            config_path,
            {
                "model_list": [
                    {"model_name": "base", "model_params": {"api_base": "http://base"}},
                    {"model_name": "child", "extends": "base", "model_params": {"api_key": "k"}},
                    {"model_name": "grand", "extends": "child", "model_params": {"api_key": "g"}},
                ]
            },
        )
        store = ConfigStore(config_path=str(config_path))

        result = store.delete_model_with_dependents("base", cascade=False)
        assert result.success is False
        assert result.dependents == ["child"]

        result = store.delete_model_with_dependents("base", cascade=True)
        assert result.success is True
        assert set(result.deleted) == {"base", "child", "grand"}

        data = yaml.safe_load(config_path.read_text(encoding="utf-8"))
        assert data["model_list"] == []
