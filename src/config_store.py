"""In-memory config store for default and added model configs."""

from __future__ import annotations

import copy
import logging
from pathlib import Path
from threading import Lock
from typing import Any

import yaml

try:
    from .config_loader import (
        CONFIG_ADDED_PATH,
        CONFIG_DEFAULT_PATH,
        load_config,
        load_env_values,
        resolve_config_path,
        resolve_env_path,
        _substitute_env_vars,
    )
except ImportError:  # pragma: no cover - support direct module imports in tests
    from config_loader import (  # type: ignore
        CONFIG_ADDED_PATH,
        CONFIG_DEFAULT_PATH,
        load_config,
        load_env_values,
        resolve_config_path,
        resolve_env_path,
        _substitute_env_vars,
    )

logger = logging.getLogger("yallmp-proxy")


def _ensure_dict(value: Any) -> dict[str, Any]:
    if isinstance(value, dict):
        return value
    return {}


def _ensure_list(value: Any) -> list[Any]:
    if isinstance(value, list):
        return value
    return []


def _normalize_fallbacks(value: Any) -> list[dict[str, list[str]]]:
    if not isinstance(value, list):
        return []
    normalized: list[dict[str, list[str]]] = []
    for entry in value:
        if isinstance(entry, dict):
            normalized.append(entry)
    return normalized


class ConfigStore:
    """Manage config_default and config_added in memory with persistence."""

    def __init__(
        self,
        default_path: str | None = None,
        added_path: str | None = None,
        default_env_path: str | None = None,
        added_env_path: str | None = None,
    ) -> None:
        self._lock = Lock()
        self.default_path = resolve_config_path(default_path or CONFIG_DEFAULT_PATH)
        self.added_path = resolve_config_path(added_path or CONFIG_ADDED_PATH)
        self.default_env_path = resolve_env_path(self.default_path, default_env_path)
        self.added_env_path = resolve_env_path(self.added_path, added_env_path)
        self._default_raw: dict[str, Any] = {}
        self._added_raw: dict[str, Any] = {}
        self._default_env: dict[str, str] = {}
        self._added_env: dict[str, str] = {}
        self.reload()

    def reload(self) -> None:
        with self._lock:
            if not self.added_path.exists():
                logger.warning(
                    "config_added file not found at %s; creating an empty one",
                    self.added_path,
                )
                self._write_config(
                    self.added_path,
                    {"model_list": [], "router_settings": {"fallbacks": []}},
                )
            self._default_raw = load_config(
                str(self.default_path),
                env_path=str(self.default_env_path),
                substitute_env=False,
            )
            self._added_raw = load_config(
                str(self.added_path),
                env_path=str(self.added_env_path),
                substitute_env=False,
            )
            self._default_env = load_env_values(self.default_env_path)
            self._added_env = load_env_values(self.added_env_path)

    def get_default_raw(self) -> dict[str, Any]:
        with self._lock:
            return copy.deepcopy(self._default_raw)

    def get_added_raw(self) -> dict[str, Any]:
        with self._lock:
            return copy.deepcopy(self._added_raw)

    def get_default_resolved(self) -> dict[str, Any]:
        with self._lock:
            return _substitute_env_vars(copy.deepcopy(self._default_raw), self._default_env)

    def get_added_resolved(self) -> dict[str, Any]:
        with self._lock:
            return _substitute_env_vars(copy.deepcopy(self._added_raw), self._added_env)

    def get_runtime_config(self) -> dict[str, Any]:
        default_cfg = self.get_default_resolved()
        added_cfg = self.get_added_resolved()
        return _merge_configs(default_cfg, added_cfg)

    def list_models(self) -> tuple[list[dict[str, Any]], list[dict[str, Any]]]:
        default_cfg = self.get_default_resolved()
        added_cfg = self.get_added_resolved()
        default_models = _mark_models(
            _ensure_list(default_cfg.get("model_list")), "default", editable=False
        )
        added_models = _mark_models(
            _ensure_list(added_cfg.get("model_list")), "added", editable=True
        )
        return default_models, added_models

    def save_default(self, new_config: dict[str, Any]) -> None:
        with self._lock:
            cfg = copy.deepcopy(new_config)
            self._write_config(self.default_path, cfg)
            self._default_raw = cfg

    def save_added(self, new_config: dict[str, Any]) -> None:
        with self._lock:
            cfg = copy.deepcopy(new_config)
            self._write_config(self.added_path, cfg)
            self._added_raw = cfg

    def upsert_default_model(
        self, model_entry: dict[str, Any], fallbacks: list[str] | None = None
    ) -> bool:
        with self._lock:
            cfg = copy.deepcopy(self._default_raw)
            replaced = _upsert_model(cfg, model_entry)
            if fallbacks is not None:
                _set_fallbacks(cfg, model_entry.get("model_name"), fallbacks)
            self._write_config(self.default_path, cfg)
            self._default_raw = cfg
            return replaced

    def upsert_added_model(
        self, model_entry: dict[str, Any], fallbacks: list[str] | None = None
    ) -> bool:
        with self._lock:
            cfg = copy.deepcopy(self._added_raw)
            replaced = _upsert_model(cfg, model_entry)
            if fallbacks is not None:
                _set_fallbacks(cfg, model_entry.get("model_name"), fallbacks)
            self._write_config(self.added_path, cfg)
            self._added_raw = cfg
            return replaced

    def delete_added_model(self, model_name: str) -> bool:
        with self._lock:
            cfg = copy.deepcopy(self._added_raw)
            model_list = _ensure_list(cfg.get("model_list"))
            filtered = [m for m in model_list if m.get("model_name") != model_name]
            if len(filtered) == len(model_list):
                return False
            cfg["model_list"] = filtered
            _remove_fallbacks(cfg, model_name)
            self._write_config(self.added_path, cfg)
            self._added_raw = cfg
            return True

    @staticmethod
    def _write_config(path: Path, data: dict[str, Any]) -> None:
        path.parent.mkdir(parents=True, exist_ok=True)
        with path.open("w", encoding="utf-8") as handle:
            yaml.safe_dump(data, handle, default_flow_style=False, sort_keys=False)


def _merge_configs(default_cfg: dict[str, Any], added_cfg: dict[str, Any]) -> dict[str, Any]:
    merged = copy.deepcopy(default_cfg)
    default_models = _ensure_list(default_cfg.get("model_list"))
    added_models = _ensure_list(added_cfg.get("model_list"))
    merged["model_list"] = (
        _mark_models(default_models, "default", editable=False)
        + _mark_models(added_models, "added", editable=True)
    )

    merged_router = _ensure_dict(merged.get("router_settings"))
    merged["router_settings"] = merged_router
    merged_fallbacks = _normalize_fallbacks(merged_router.get("fallbacks"))
    added_router = _ensure_dict(added_cfg.get("router_settings"))
    added_fallbacks = _normalize_fallbacks(added_router.get("fallbacks"))
    if added_fallbacks:
        merged_fallbacks.extend(copy.deepcopy(added_fallbacks))
    if merged_fallbacks:
        merged_router["fallbacks"] = merged_fallbacks

    return merged


def _mark_models(
    models: list[Any], source: str, editable: bool
) -> list[dict[str, Any]]:
    marked: list[dict[str, Any]] = []
    for model in models:
        if not isinstance(model, dict):
            continue
        entry = copy.deepcopy(model)
        entry["editable"] = editable
        entry["source"] = source
        marked.append(entry)
    return marked


def _upsert_model(cfg: dict[str, Any], model_entry: dict[str, Any]) -> bool:
    model_list = _ensure_list(cfg.get("model_list"))
    cfg["model_list"] = model_list
    model_name = model_entry.get("model_name")
    for idx, entry in enumerate(model_list):
        if entry.get("model_name") == model_name:
            model_list[idx] = model_entry
            return True
    model_list.append(model_entry)
    return False


def _set_fallbacks(cfg: dict[str, Any], model_name: str | None, fallbacks: list[str]) -> None:
    if not model_name:
        return
    router = _ensure_dict(cfg.get("router_settings"))
    fallbacks_list = _normalize_fallbacks(router.get("fallbacks"))
    fallbacks_list = [
        entry for entry in fallbacks_list if model_name not in entry
    ]
    if fallbacks:
        fallbacks_list.append({model_name: fallbacks})
    if fallbacks_list:
        router["fallbacks"] = fallbacks_list
    elif "fallbacks" in router:
        del router["fallbacks"]
    cfg["router_settings"] = router


def _remove_fallbacks(cfg: dict[str, Any], model_name: str) -> None:
    router = _ensure_dict(cfg.get("router_settings"))
    fallbacks_list = _normalize_fallbacks(router.get("fallbacks"))
    filtered = [entry for entry in fallbacks_list if model_name not in entry]
    if filtered:
        router["fallbacks"] = filtered
    else:
        router.pop("fallbacks", None)
    cfg["router_settings"] = router


CONFIG_STORE = ConfigStore()
