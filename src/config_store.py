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

# Maximum depth for model inheritance chains to prevent infinite loops
MAX_INHERITANCE_DEPTH = 10


class ModelInheritanceError(Exception):
    """Raised when there's an error resolving model inheritance."""

    pass


def _deep_merge_dicts(base: dict[str, Any], override: dict[str, Any]) -> dict[str, Any]:
    """Deep merge two dictionaries, with override values taking precedence.

    For nested dicts, recursively merge. For other types, override replaces base.
    Lists are replaced, not merged.

    Args:
        base: The base dictionary to merge into.
        override: The dictionary with values that override base.

    Returns:
        A new dictionary with merged values.
    """
    result = copy.deepcopy(base)
    for key, value in override.items():
        if (
            key in result
            and isinstance(result[key], dict)
            and isinstance(value, dict)
        ):
            result[key] = _deep_merge_dicts(result[key], value)
        else:
            result[key] = copy.deepcopy(value)
    return result


def _resolve_single_model_inheritance(
    model_entry: dict[str, Any],
    all_models: dict[str, dict[str, Any]],
    resolution_chain: list[str],
) -> dict[str, Any]:
    """Recursively resolve inheritance for a single model.

    Args:
        model_entry: The model entry to resolve.
        all_models: Lookup dict of all models by name.
        resolution_chain: List of model names in current resolution chain (for cycle detection).

    Returns:
        The fully resolved model entry with all inherited values merged.

    Raises:
        ModelInheritanceError: If circular reference detected or base model not found.
    """
    model_name = model_entry.get("model_name", "<unknown>")
    extends = model_entry.get("extends")

    # No inheritance - return as-is
    if not extends:
        return copy.deepcopy(model_entry)

    # Check for circular reference
    if extends in resolution_chain:
        cycle = " -> ".join(resolution_chain + [extends])
        raise ModelInheritanceError(
            f"Circular inheritance detected for model '{model_name}': {cycle}"
        )

    # Check depth limit
    if len(resolution_chain) >= MAX_INHERITANCE_DEPTH:
        raise ModelInheritanceError(
            f"Maximum inheritance depth ({MAX_INHERITANCE_DEPTH}) exceeded for model '{model_name}'"
        )

    # Find base model
    base_model = all_models.get(extends)
    if base_model is None:
        raise ModelInheritanceError(
            f"Model '{model_name}' extends '{extends}', but base model not found"
        )

    # Recursively resolve base model's inheritance first
    resolved_base = _resolve_single_model_inheritance(
        base_model, all_models, resolution_chain + [model_name]
    )

    # Deep merge: base model values + current model overrides
    # Remove 'extends' from result since it's now resolved
    resolved = _deep_merge_dicts(resolved_base, model_entry)
    resolved.pop("extends", None)

    # Preserve the derived model's name, not the base model's
    resolved["model_name"] = model_name

    # Track inheritance for debugging/logging
    if "extends" in model_entry:
        resolved["_inherited_from"] = extends

    logger.debug(
        "Resolved model '%s' inheritance from '%s'", model_name, extends
    )

    return resolved


def _resolve_all_model_inheritance(
    model_list: list[dict[str, Any]],
) -> list[dict[str, Any]]:
    """Resolve inheritance for all models in a list.

    Args:
        model_list: List of model entries, some may have 'extends' field.

    Returns:
        List of resolved model entries with inheritance applied.

    Raises:
        ModelInheritanceError: If any inheritance resolution fails.
    """
    if not model_list:
        return []

    # Build lookup dict by model_name
    all_models: dict[str, dict[str, Any]] = {}
    for model in model_list:
        if isinstance(model, dict):
            name = model.get("model_name")
            if name:
                all_models[name] = model

    # Resolve each model
    resolved_list: list[dict[str, Any]] = []
    for model in model_list:
        if not isinstance(model, dict):
            continue
        try:
            resolved = _resolve_single_model_inheritance(model, all_models, [])
            resolved_list.append(resolved)
        except ModelInheritanceError as e:
            logger.error("Failed to resolve model inheritance: %s", e)
            raise

    return resolved_list


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

    def get_default_resolved(self, warn_on_missing: bool = True) -> dict[str, Any]:
        with self._lock:
            return _substitute_env_vars(
                copy.deepcopy(self._default_raw),
                self._default_env,
                warn_on_missing,
            )

    def get_added_resolved(self, warn_on_missing: bool = True) -> dict[str, Any]:
        with self._lock:
            return _substitute_env_vars(
                copy.deepcopy(self._added_raw),
                self._added_env,
                warn_on_missing,
            )

    def get_runtime_config(self) -> dict[str, Any]:
        default_cfg = self.get_default_resolved()
        added_cfg = self.get_added_resolved()
        return _merge_configs(default_cfg, added_cfg)

    def list_models(
        self, resolve_inheritance: bool = True
    ) -> tuple[list[dict[str, Any]], list[dict[str, Any]]]:
        """List all models from default and added configs.

        Args:
            resolve_inheritance: If True, resolve model inheritance.

        Returns:
            Tuple of (default_models, added_models) lists.
        """
        default_cfg = self.get_default_resolved(warn_on_missing=False)
        added_cfg = self.get_added_resolved(warn_on_missing=False)
        default_models = _mark_models(
            _ensure_list(default_cfg.get("model_list")), "default", editable=False
        )
        added_models = _mark_models(
            _ensure_list(added_cfg.get("model_list")), "added", editable=True
        )

        if resolve_inheritance:
            # Resolve inheritance across all models together
            # (added models can extend default models)
            combined = default_models + added_models
            try:
                resolved = _resolve_all_model_inheritance(combined)
                # Split back into default and added based on source marker
                default_models = [m for m in resolved if m.get("source") == "default"]
                added_models = [m for m in resolved if m.get("source") == "added"]
            except ModelInheritanceError as e:
                logger.error("Model inheritance resolution failed in list_models: %s", e)
                # Fall back to unresolved models

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

    def copy_model(self, source_name: str, new_name: str) -> dict[str, Any]:
        """Copy an existing model to a new model with a different name.

        The copied model is always saved to the added config (config_added.yaml).
        Source model can come from either default or added config.

        Args:
            source_name: The name of the model to copy.
            new_name: The name for the new copied model.

        Returns:
            The newly created model entry.

        Raises:
            ValueError: If source model not found or new_name already exists.
        """
        with self._lock:
            # Find source model in default or added config
            source_model: dict[str, Any] | None = None

            # Check added config first (user's models take precedence)
            added_models = _ensure_list(self._added_raw.get("model_list"))
            for model in added_models:
                if isinstance(model, dict) and model.get("model_name") == source_name:
                    source_model = model
                    break

            # If not found in added, check default config
            if source_model is None:
                default_models = _ensure_list(self._default_raw.get("model_list"))
                for model in default_models:
                    if isinstance(model, dict) and model.get("model_name") == source_name:
                        source_model = model
                        break

            if source_model is None:
                raise ValueError(f"Source model '{source_name}' not found")

            # Check if new_name already exists in either config
            all_names: set[str] = set()
            for model in added_models:
                if isinstance(model, dict) and model.get("model_name"):
                    all_names.add(model["model_name"])
            default_models = _ensure_list(self._default_raw.get("model_list"))
            for model in default_models:
                if isinstance(model, dict) and model.get("model_name"):
                    all_names.add(model["model_name"])

            if new_name in all_names:
                raise ValueError(f"Model '{new_name}' already exists")

            # Create deep copy with new name
            new_model = copy.deepcopy(source_model)
            new_model["model_name"] = new_name

            # Remove metadata fields that shouldn't be copied
            new_model.pop("editable", None)
            new_model.pop("source", None)
            new_model.pop("_inherited_from", None)

            # Save to added config
            cfg = copy.deepcopy(self._added_raw)
            model_list = _ensure_list(cfg.get("model_list"))
            model_list.append(new_model)
            cfg["model_list"] = model_list
            self._write_config(self.added_path, cfg)
            self._added_raw = cfg

            logger.info("Copied model '%s' to '%s'", source_name, new_name)
            return new_model

    def find_model(self, model_name: str) -> dict[str, Any] | None:
        """Find a model by name in either default or added config.

        Args:
            model_name: The name of the model to find.

        Returns:
            The model entry if found, None otherwise.
        """
        with self._lock:
            # Check added config first
            added_models = _ensure_list(self._added_raw.get("model_list"))
            for model in added_models:
                if isinstance(model, dict) and model.get("model_name") == model_name:
                    return copy.deepcopy(model)

            # Check default config
            default_models = _ensure_list(self._default_raw.get("model_list"))
            for model in default_models:
                if isinstance(model, dict) and model.get("model_name") == model_name:
                    return copy.deepcopy(model)

            return None

    @staticmethod
    def _write_config(path: Path, data: dict[str, Any]) -> None:
        path.parent.mkdir(parents=True, exist_ok=True)
        with path.open("w", encoding="utf-8") as handle:
            yaml.safe_dump(data, handle, default_flow_style=False, sort_keys=False)


def _merge_configs(
    default_cfg: dict[str, Any],
    added_cfg: dict[str, Any],
    resolve_inheritance: bool = True,
) -> dict[str, Any]:
    """Merge default and added configs into a single runtime config.

    Args:
        default_cfg: The default configuration.
        added_cfg: The added configuration to merge.
        resolve_inheritance: If True, resolve model inheritance after merging.

    Returns:
        Merged configuration dictionary.
    """
    merged = copy.deepcopy(default_cfg)
    default_models = _ensure_list(default_cfg.get("model_list"))
    added_models = _ensure_list(added_cfg.get("model_list"))

    # Mark models with their source before merging
    marked_default = _mark_models(default_models, "default", editable=False)
    marked_added = _mark_models(added_models, "added", editable=True)
    combined_models = marked_default + marked_added

    # Resolve inheritance across all models (default + added)
    if resolve_inheritance:
        try:
            combined_models = _resolve_all_model_inheritance(combined_models)
        except ModelInheritanceError as e:
            logger.error("Model inheritance resolution failed: %s", e)
            # Fall back to unresolved models on error
            combined_models = marked_default + marked_added

    merged["model_list"] = combined_models

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
