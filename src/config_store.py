"""In-memory config store for unified model configs."""

from __future__ import annotations

import copy
import logging
from pathlib import Path
from threading import Lock
from typing import Any
from dataclasses import dataclass, field

import yaml

try:
    from .config_loader import (
        CONFIG_PATH,
        load_config,
        load_env_values,
        resolve_config_path,
        resolve_env_path,
        _substitute_env_vars,
    )
except ImportError:  # pragma: no cover - support direct module imports in tests
    from config_loader import (  # type: ignore
        CONFIG_PATH,
        load_config,
        load_env_values,
        resolve_config_path,
        resolve_env_path,
        _substitute_env_vars,
    )

logger = logging.getLogger("yallmp-proxy")

# Maximum depth for model inheritance chains to prevent infinite loops
MAX_INHERITANCE_DEPTH = 10
ADMIN_PASSWORD_ENV = "YALLMP_ADMIN_PASSWORD"


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
        resolved = copy.deepcopy(model_entry)
        resolved.pop("source", None)
        protected = _normalize_protected(model_entry.get("protected"), default=True)
        resolved["protected"] = protected
        resolved["editable"] = not protected
        return resolved

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

    # Protected/editable flags are not inherited; they are per-model.
    resolved.pop("source", None)
    protected = _normalize_protected(model_entry.get("protected"), default=True)
    resolved["protected"] = protected
    resolved["editable"] = not protected

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


def _parse_bool(value: Any) -> bool:
    if isinstance(value, str):
        return value.strip().lower() in {"1", "true", "yes", "on"}
    return bool(value)


def _normalize_protected(value: Any, default: bool = True) -> bool:
    if value is None:
        return default
    return _parse_bool(value)


def _normalize_fallbacks(value: Any) -> list[dict[str, list[str]]]:
    if not isinstance(value, list):
        return []
    normalized: list[dict[str, list[str]]] = []
    for entry in value:
        if isinstance(entry, dict):
            normalized.append(entry)
    return normalized


@dataclass
class DeleteResult:
    success: bool
    error: str | None = None
    dependents: list[str] = field(default_factory=list)
    deleted: list[str] = field(default_factory=list)


@dataclass
class ModelNode:
    """Node in the model inheritance tree."""

    name: str
    config: dict[str, Any]
    parent: str | None
    children: list[str] = field(default_factory=list)
    protected: bool = True
    editable: bool = False
    parent_missing: bool = False
    _cached_resolved: dict[str, Any] | None = None
    _cached_inherited: dict[str, Any] | None = None
    _cached_chain: list[str] | None = None


class ModelTree:
    """Maintains the model inheritance tree."""

    def __init__(self) -> None:
        self.nodes: dict[str, ModelNode] = {}
        self.roots: list[str] = []
        self._order: list[str] = []

    def build(self, models: list[dict[str, Any]]) -> None:
        self.nodes = {}
        self.roots = []
        self._order = []

        for entry in models:
            if not isinstance(entry, dict):
                continue
            name = entry.get("model_name")
            if not name:
                continue
            if name in self.nodes:
                logger.warning(
                    "Duplicate model_name '%s' found while building model tree; "
                    "later entry will override earlier definition.",
                    name,
                )
            parent = entry.get("extends")
            parent = str(parent).strip() if parent else None

            config = copy.deepcopy(entry)
            config.pop("extends", None)
            config.pop("editable", None)
            config.pop("source", None)
            config.pop("_inherited_from", None)

            protected = _normalize_protected(entry.get("protected"), default=True)

            node = ModelNode(
                name=name,
                config=config,
                parent=parent or None,
                protected=protected,
                editable=not protected,
            )
            self.nodes[name] = node
            if name not in self._order:
                self._order.append(name)

        # Build parent/child relationships
        for node in self.nodes.values():
            if node.parent and node.parent in self.nodes:
                self.nodes[node.parent].children.append(node.name)
            elif node.parent:
                node.parent_missing = True

        # Order children and roots based on original model list
        order_index = {name: idx for idx, name in enumerate(self._order)}
        for node in self.nodes.values():
            node.children.sort(key=lambda n: order_index.get(n, 0))

        self.roots = [
            node.name
            for node in self.nodes.values()
            if not node.parent or node.parent not in self.nodes
        ]
        self.roots.sort(key=lambda n: order_index.get(n, 0))

        self._validate_no_cycles()

    def _validate_no_cycles(self) -> None:
        for name in self.nodes:
            self._check_chain(name, [])

    def _check_chain(self, name: str, chain: list[str]) -> None:
        if name in chain:
            cycle = " -> ".join(chain + [name])
            raise ModelInheritanceError(
                f"Circular inheritance detected for model '{name}': {cycle}"
            )
        if len(chain) >= MAX_INHERITANCE_DEPTH:
            raise ModelInheritanceError(
                f"Maximum inheritance depth ({MAX_INHERITANCE_DEPTH}) exceeded for model '{name}'"
            )
        node = self.nodes.get(name)
        if not node or not node.parent or node.parent not in self.nodes:
            return
        self._check_chain(node.parent, chain + [name])

    def get_node(self, model_name: str) -> ModelNode | None:
        return self.nodes.get(model_name)

    def get_children(self, model_name: str) -> list[str]:
        node = self.nodes.get(model_name)
        if not node:
            return []
        return list(node.children)

    def get_descendants(self, model_name: str) -> list[str]:
        node = self.nodes.get(model_name)
        if not node:
            return []
        descendants: list[str] = []
        stack = list(reversed(node.children))
        while stack:
            child = stack.pop()
            descendants.append(child)
            child_node = self.nodes.get(child)
            if child_node:
                stack.extend(reversed(child_node.children))
        return descendants

    def get_ancestors(self, model_name: str) -> list[str]:
        node = self.nodes.get(model_name)
        if not node:
            return []
        ancestors: list[str] = []
        parent = node.parent
        depth = 0
        while parent:
            ancestors.append(parent)
            if parent in self.nodes:
                parent = self.nodes[parent].parent
            else:
                break
            depth += 1
            if depth >= MAX_INHERITANCE_DEPTH:
                raise ModelInheritanceError(
                    f"Maximum inheritance depth ({MAX_INHERITANCE_DEPTH}) exceeded for model '{model_name}'"
                )
        return ancestors

    def has_ancestor(self, model_name: str, ancestor_name: str) -> bool:
        return ancestor_name in self.get_ancestors(model_name)

    def get_inheritance_chain(self, model_name: str) -> list[str]:
        node = self.nodes.get(model_name)
        if not node:
            return []
        if node._cached_chain is not None:
            return list(node._cached_chain)
        chain = [model_name]
        parent = node.parent
        depth = 0
        while parent:
            chain.append(parent)
            if parent in self.nodes:
                parent = self.nodes[parent].parent
            else:
                break
            depth += 1
            if depth >= MAX_INHERITANCE_DEPTH:
                raise ModelInheritanceError(
                    f"Maximum inheritance depth ({MAX_INHERITANCE_DEPTH}) exceeded for model '{model_name}'"
                )
        node._cached_chain = chain
        return list(chain)

    def _resolve_chain(self, chain: list[str]) -> dict[str, Any]:
        merged: dict[str, Any] = {}
        for name in reversed(chain):
            node = self.nodes.get(name)
            if not node:
                continue
            merged = _deep_merge_dicts(merged, node.config)
        return merged

    def resolve_model(self, model_name: str) -> dict[str, Any] | None:
        node = self.nodes.get(model_name)
        if not node:
            return None
        if node._cached_resolved is not None:
            return copy.deepcopy(node._cached_resolved)

        chain = self.get_inheritance_chain(model_name)
        resolved = self._resolve_chain(chain)

        # Only remove 'extends' when the parent exists in the tree.
        if node.parent and node.parent in self.nodes:
            resolved.pop("extends", None)
        elif node.parent:
            # Preserve explicit extends for missing parents.
            resolved["extends"] = node.parent

        resolved["model_name"] = node.name
        resolved.pop("source", None)
        resolved.pop("editable", None)
        resolved.pop("_inherited_from", None)

        if node.parent:
            resolved["_inherited_from"] = node.parent

        resolved["protected"] = node.protected
        resolved["editable"] = not node.protected

        node._cached_resolved = resolved
        return copy.deepcopy(resolved)

    def inherited_fields(self, model_name: str) -> dict[str, Any]:
        node = self.nodes.get(model_name)
        if not node:
            return {}
        if node._cached_inherited is not None:
            return copy.deepcopy(node._cached_inherited)
        chain = self.get_inheritance_chain(model_name)
        inherited = self._resolve_chain(chain[1:]) if len(chain) > 1 else {}
        node._cached_inherited = inherited
        return copy.deepcopy(inherited)

    def resolve_models(self, order: list[str] | None = None) -> list[dict[str, Any]]:
        names = order or self._order
        resolved: list[dict[str, Any]] = []
        for name in names:
            model = self.resolve_model(name)
            if model is not None:
                resolved.append(model)
        return resolved

    def delete_model(self, model_name: str, cascade: bool = False) -> DeleteResult:
        node = self.nodes.get(model_name)
        if not node:
            return DeleteResult(success=False, error="Model not found")
        children = list(node.children)
        if children and not cascade:
            return DeleteResult(
                success=False,
                error="Cannot delete model with existing dependents",
                dependents=children,
            )
        to_delete = [model_name]
        if cascade:
            to_delete.extend(self.get_descendants(model_name))
        # Update tree in-memory
        for name in to_delete:
            current = self.nodes.pop(name, None)
            if current and current.parent and current.parent in self.nodes:
                parent_node = self.nodes[current.parent]
                if name in parent_node.children:
                    parent_node.children.remove(name)
        self.roots = [
            node.name
            for node in self.nodes.values()
            if not node.parent or node.parent not in self.nodes
        ]
        return DeleteResult(success=True, deleted=to_delete)


class ConfigStore:
    """Manage unified config in memory with persistence."""

    def __init__(
        self,
        config_path: str | None = None,
        env_path: str | None = None,
    ) -> None:
        self._lock = Lock()
        self.config_path = resolve_config_path(config_path or CONFIG_PATH)
        self.env_path = resolve_env_path(self.config_path, env_path)
        self._raw: dict[str, Any] = {}
        self._env: dict[str, str] = {}
        self._model_tree: ModelTree = ModelTree()
        self.reload()

    def reload(self) -> None:
        with self._lock:
            self._raw = load_config(
                str(self.config_path),
                env_path=str(self.env_path),
                substitute_env=False,
            )
            self._env = load_env_values(self.env_path)
            self._rebuild_model_tree_locked()

    def _rebuild_model_tree_locked(self) -> None:
        resolved = _substitute_env_vars(
            copy.deepcopy(self._raw),
            self._env,
            warn_on_missing=True,
        )
        model_list = _ensure_list(resolved.get("model_list"))
        tree = ModelTree()
        try:
            tree.build(model_list)
        except ModelInheritanceError as exc:
            logger.error("Model tree build failed: %s", exc)
            # Fallback: build a flat tree without inheritance
            fallback_models: list[dict[str, Any]] = []
            for model in model_list:
                if isinstance(model, dict):
                    entry = copy.deepcopy(model)
                    entry.pop("extends", None)
                    fallback_models.append(entry)
            tree = ModelTree()
            tree.build(fallback_models)
        self._model_tree = tree

    def get_raw(self) -> dict[str, Any]:
        with self._lock:
            return copy.deepcopy(self._raw)

    def get_resolved(self, warn_on_missing: bool = True) -> dict[str, Any]:
        with self._lock:
            return _substitute_env_vars(
                copy.deepcopy(self._raw),
                self._env,
                warn_on_missing,
            )

    def get_env_value(self, key: str) -> str | None:
        with self._lock:
            value = self._env.get(key)
        if value is not None and str(value).strip():
            return value
        from os import getenv

        env_value = getenv(key)
        if env_value is None:
            return None
        env_value = str(env_value)
        return env_value if env_value.strip() else None

    def get_admin_password(self) -> str | None:
        return self.get_env_value(ADMIN_PASSWORD_ENV)

    def get_runtime_config(self) -> dict[str, Any]:
        with self._lock:
            cfg = _substitute_env_vars(
                copy.deepcopy(self._raw),
                self._env,
                warn_on_missing=True,
            )
            cfg["model_list"] = self._model_tree.resolve_models()
            return cfg

    def list_models(
        self, resolve_inheritance: bool = True
    ) -> tuple[list[dict[str, Any]], list[dict[str, Any]]]:
        """List all models, split into protected/unprotected.

        Args:
            resolve_inheritance: If True, resolve model inheritance.

        Returns:
            Tuple of (protected_models, unprotected_models) lists.
        """
        with self._lock:
            cfg = _substitute_env_vars(
                copy.deepcopy(self._raw),
                self._env,
                warn_on_missing=False,
            )
            if resolve_inheritance:
                models = self._model_tree.resolve_models()
            else:
                models = _mark_models(_ensure_list(cfg.get("model_list")))

            protected_models = [m for m in models if m.get("protected")]
            unprotected_models = [m for m in models if not m.get("protected")]
            return protected_models, unprotected_models

    def save(self, new_config: dict[str, Any]) -> None:
        with self._lock:
            cfg = copy.deepcopy(new_config)
            self._write_config(self.config_path, cfg)
            self._raw = cfg
            self._rebuild_model_tree_locked()

    def upsert_model(
        self, model_entry: dict[str, Any], fallbacks: list[str] | None = None
    ) -> bool:
        with self._lock:
            cfg = copy.deepcopy(self._raw)
            if "extends" in model_entry and not model_entry.get("extends"):
                model_entry = copy.deepcopy(model_entry)
                model_entry.pop("extends", None)
            existing = None
            for model in _ensure_list(cfg.get("model_list")):
                if isinstance(model, dict) and model.get("model_name") == model_entry.get("model_name"):
                    existing = model
                    break
            if existing is not None and "extends" in existing and "extends" not in model_entry:
                model_entry = copy.deepcopy(model_entry)
                model_entry["extends"] = existing.get("extends")
            replaced = _upsert_model(cfg, model_entry)
            if fallbacks is not None:
                _set_fallbacks(cfg, model_entry.get("model_name"), fallbacks)
            self._write_config(self.config_path, cfg)
            self._raw = cfg
            self._rebuild_model_tree_locked()
            return replaced

    def delete_model(self, model_name: str) -> bool:
        result = self.delete_model_with_dependents(model_name, cascade=False)
        return result.success

    def delete_model_with_dependents(
        self, model_name: str, cascade: bool = False
    ) -> DeleteResult:
        with self._lock:
            node = self._model_tree.get_node(model_name)
            if not node:
                return DeleteResult(success=False, error="Model not found")
            children = self._model_tree.get_children(model_name)
            if children and not cascade:
                return DeleteResult(
                    success=False,
                    error="Cannot delete model with existing dependents",
                    dependents=children,
                )
            to_delete = [model_name]
            if cascade:
                to_delete.extend(self._model_tree.get_descendants(model_name))
            cfg = copy.deepcopy(self._raw)
            model_list = _ensure_list(cfg.get("model_list"))
            cfg["model_list"] = [
                m for m in model_list if m.get("model_name") not in to_delete
            ]
            for name in to_delete:
                _remove_fallbacks(cfg, name)
            self._write_config(self.config_path, cfg)
            self._raw = cfg
            self._rebuild_model_tree_locked()
            return DeleteResult(success=True, deleted=to_delete)

    def copy_model(self, source_name: str, new_name: str) -> dict[str, Any]:
        """Copy an existing model to a new model with a different name.

        Args:
            source_name: The name of the model to copy.
            new_name: The name for the new copied model.

        Returns:
            The newly created model entry.

        Raises:
            ValueError: If source model not found or new_name already exists.
        """
        with self._lock:
            source_model: dict[str, Any] | None = None
            model_list = _ensure_list(copy.deepcopy(self._raw.get("model_list")))
            for model in model_list:
                if isinstance(model, dict) and model.get("model_name") == source_name:
                    source_model = model
                    break

            if source_model is None:
                raise ValueError(f"Source model '{source_name}' not found")

            existing_names = {
                m.get("model_name")
                for m in model_list
                if isinstance(m, dict) and m.get("model_name")
            }
            if new_name in existing_names:
                raise ValueError(f"Model '{new_name}' already exists")

            new_model = copy.deepcopy(source_model)
            new_model["model_name"] = new_name

            # Remove metadata fields that shouldn't be copied
            new_model.pop("editable", None)
            new_model.pop("source", None)
            new_model.pop("_inherited_from", None)

            model_list.append(new_model)
            cfg = copy.deepcopy(self._raw)
            cfg["model_list"] = model_list
            self._write_config(self.config_path, cfg)
            self._raw = cfg
            self._rebuild_model_tree_locked()

            logger.info("Copied model '%s' to '%s'", source_name, new_name)
            return new_model

    def find_model(self, model_name: str) -> dict[str, Any] | None:
        """Find a model by name in config.

        Args:
            model_name: The name of the model to find.

        Returns:
            The model entry if found, None otherwise.
        """
        with self._lock:
            model_list = _ensure_list(self._raw.get("model_list"))
            for model in model_list:
                if isinstance(model, dict) and model.get("model_name") == model_name:
                    return copy.deepcopy(model)
            return None

    def get_model_tree(self) -> ModelTree:
        with self._lock:
            return self._model_tree

    # -------------------------------------------------------------------------
    # App Keys Management
    # -------------------------------------------------------------------------

    def get_app_keys_config(self) -> dict[str, Any]:
        """Get the full app_keys configuration section with env vars resolved.

        Returns:
            The app_keys config dict, or empty dict if not configured.
        """
        with self._lock:
            cfg = _substitute_env_vars(
                copy.deepcopy(self._raw),
                self._env,
                warn_on_missing=False,
            )
            return _ensure_dict(cfg.get("app_keys"))

    def list_app_keys(self, mask_secrets: bool = True) -> list[dict[str, Any]]:
        """List all configured app keys.

        Args:
            mask_secrets: If True, remove secret values from the response.

        Returns:
            List of app key entries.
        """
        with self._lock:
            cfg = _substitute_env_vars(
                copy.deepcopy(self._raw),
                self._env,
                warn_on_missing=False,
            )
            app_keys = _ensure_dict(cfg.get("app_keys"))
            keys = _ensure_list(app_keys.get("keys"))

            result: list[dict[str, Any]] = []
            for key_entry in keys:
                if not isinstance(key_entry, dict):
                    continue
                entry = copy.deepcopy(key_entry)
                if mask_secrets:
                    entry.pop("secret", None)
                result.append(entry)
            return result

    def get_app_key(self, key_id: str, mask_secret: bool = True) -> dict[str, Any] | None:
        """Get a specific app key by ID.

        Args:
            key_id: The key ID to find.
            mask_secret: If True, remove the secret from the response.

        Returns:
            The key entry if found, None otherwise.
        """
        with self._lock:
            cfg = _substitute_env_vars(
                copy.deepcopy(self._raw),
                self._env,
                warn_on_missing=False,
            )
            app_keys = _ensure_dict(cfg.get("app_keys"))
            keys = _ensure_list(app_keys.get("keys"))

            for key_entry in keys:
                if not isinstance(key_entry, dict):
                    continue
                if key_entry.get("key_id") == key_id:
                    entry = copy.deepcopy(key_entry)
                    if mask_secret:
                        entry.pop("secret", None)
                    return entry
            return None

    def upsert_app_key(self, key_entry: dict[str, Any]) -> bool:
        """Create or update an app key.

        Args:
            key_entry: The key configuration to upsert. Must have 'key_id' and 'secret'.

        Returns:
            True if an existing key was replaced, False if a new key was added.

        Raises:
            ValueError: If key_id is missing.
        """
        key_id = key_entry.get("key_id")
        if not key_id:
            raise ValueError("key_id is required")

        with self._lock:
            cfg = copy.deepcopy(self._raw)
            app_keys = _ensure_dict(cfg.get("app_keys"))
            keys = _ensure_list(app_keys.get("keys"))

            # Find existing key
            replaced = False
            for idx, existing in enumerate(keys):
                if isinstance(existing, dict) and existing.get("key_id") == key_id:
                    # Preserve secret if not provided in update
                    if "secret" not in key_entry and "secret" in existing:
                        key_entry = copy.deepcopy(key_entry)
                        key_entry["secret"] = existing["secret"]
                    keys[idx] = key_entry
                    replaced = True
                    break

            if not replaced:
                keys.append(key_entry)

            app_keys["keys"] = keys
            cfg["app_keys"] = app_keys
            self._write_config(self.config_path, cfg)
            self._raw = cfg
            logger.info("Upserted app key '%s' (replaced=%s)", key_id, replaced)
            return replaced

    def delete_app_key(self, key_id: str) -> bool:
        """Delete an app key by ID.

        Args:
            key_id: The key ID to delete.

        Returns:
            True if the key was found and deleted, False otherwise.
        """
        with self._lock:
            cfg = copy.deepcopy(self._raw)
            app_keys = _ensure_dict(cfg.get("app_keys"))
            keys = _ensure_list(app_keys.get("keys"))

            original_len = len(keys)
            keys = [
                k for k in keys
                if not (isinstance(k, dict) and k.get("key_id") == key_id)
            ]

            if len(keys) == original_len:
                return False

            app_keys["keys"] = keys
            cfg["app_keys"] = app_keys
            self._write_config(self.config_path, cfg)
            self._raw = cfg
            logger.info("Deleted app key '%s'", key_id)
            return True

    def set_app_keys_enabled(self, enabled: bool) -> None:
        """Enable or disable app key authentication.

        Args:
            enabled: True to enable, False to disable.
        """
        with self._lock:
            cfg = copy.deepcopy(self._raw)
            app_keys = _ensure_dict(cfg.get("app_keys"))
            app_keys["enabled"] = enabled
            cfg["app_keys"] = app_keys
            self._write_config(self.config_path, cfg)
            self._raw = cfg
            logger.info("App key authentication %s", "enabled" if enabled else "disabled")

    @staticmethod
    def _write_config(path: Path, data: dict[str, Any]) -> None:
        path.parent.mkdir(parents=True, exist_ok=True)
        with path.open("w", encoding="utf-8") as handle:
            yaml.safe_dump(data, handle, default_flow_style=False, sort_keys=False)


def _mark_models(models: list[Any]) -> list[dict[str, Any]]:
    marked: list[dict[str, Any]] = []
    for model in models:
        if not isinstance(model, dict):
            continue
        entry = copy.deepcopy(model)
        protected = _normalize_protected(entry.get("protected"), default=True)
        entry["protected"] = protected
        entry["editable"] = not protected
        entry.pop("source", None)
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
