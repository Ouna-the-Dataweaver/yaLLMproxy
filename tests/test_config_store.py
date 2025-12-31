"""Tests for the config store behavior."""

import sys
from pathlib import Path

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
