"""Request module pipeline for downstream -> upstream transforms."""

from __future__ import annotations

import logging
from dataclasses import dataclass, field
from typing import Any, Iterable, Mapping, Optional

from ..parsers.response_pipeline import ModuleContext

logger = logging.getLogger("yallmp-proxy")


RequestModuleContext = ModuleContext


def _parse_bool(value: Any) -> bool:
    if isinstance(value, str):
        return value.strip().lower() in {"1", "true", "yes", "on"}
    return bool(value)


def _ensure_list(value: Any) -> list[str]:
    if value is None:
        return []
    if isinstance(value, (list, tuple)):
        return [str(item) for item in value if item is not None]
    return [str(value)]


class RequestModule:
    name = "base"

    def apply_request(
        self, payload: Mapping[str, Any], ctx: RequestModuleContext
    ) -> Mapping[str, Any] | None:
        return payload


@dataclass
class RequestModulePipeline:
    modules: list[RequestModule] = field(default_factory=list)
    paths: list[str] = field(default_factory=list)

    def applies(self, ctx: RequestModuleContext) -> bool:
        if not self.modules:
            return False
        if not self.paths:
            return True
        return any(path in ctx.path for path in self.paths)

    def transform_request_payload(
        self, payload: Mapping[str, Any], ctx: RequestModuleContext
    ) -> Optional[dict[str, Any]]:
        if not self.applies(ctx):
            return None
        if not isinstance(payload, Mapping):
            return None
        updated: Mapping[str, Any] = dict(payload)
        for module in self.modules:
            result = module.apply_request(updated, ctx)
            if result is None:
                continue
            if not isinstance(result, Mapping):
                logger.warning(
                    "Request module '%s' returned non-mapping payload; skipping",
                    module.name,
                )
                continue
            updated = dict(result)
        return dict(updated)


def _select_downstream_modules_config(root_cfg: Mapping[str, Any]) -> dict[str, Any]:
    modules_cfg = root_cfg.get("modules")
    if not isinstance(modules_cfg, Mapping):
        if "upstream" in root_cfg or "downstream" in root_cfg:
            downstream_cfg = root_cfg.get("downstream")
            if not isinstance(downstream_cfg, Mapping):
                downstream_cfg = {}
            if "enabled" not in downstream_cfg and "enabled" in root_cfg:
                downstream_cfg = dict(downstream_cfg)
                downstream_cfg["enabled"] = root_cfg.get("enabled")
            return dict(downstream_cfg)
        return {}
    if "upstream" in modules_cfg or "downstream" in modules_cfg:
        downstream_cfg = modules_cfg.get("downstream")
        if not isinstance(downstream_cfg, Mapping):
            downstream_cfg = {}
        if "enabled" not in downstream_cfg and "enabled" in modules_cfg:
            downstream_cfg = dict(downstream_cfg)
            downstream_cfg["enabled"] = modules_cfg.get("enabled")
        return dict(downstream_cfg)
    return {}


def build_request_module_pipeline(
    config: Mapping[str, Any],
    *,
    enabled_default: bool = False,
    default_paths: Optional[Iterable[str]] = None,
) -> RequestModulePipeline:
    if "proxy_settings" in config:
        root_cfg = config.get("proxy_settings") or {}
        enabled_default = False
    else:
        root_cfg = config or {}

    modules_cfg = _select_downstream_modules_config(root_cfg)
    if not modules_cfg:
        return RequestModulePipeline()

    enabled_raw = modules_cfg.get("enabled")
    enabled = enabled_default if enabled_raw is None else _parse_bool(enabled_raw)
    if not enabled:
        return RequestModulePipeline()

    module_names = _ensure_list(modules_cfg.get("request"))
    if not module_names:
        return RequestModulePipeline()

    available: dict[str, type[RequestModule]] = {}
    parsed_modules: list[RequestModule] = []
    for name in module_names:
        module_cls = available.get(name)
        if not module_cls:
            logger.warning("Unknown request module '%s' configured; skipping", name)
            continue
        module_config = modules_cfg.get(name) or {}
        parsed_modules.append(module_cls(module_config))

    paths = _ensure_list(modules_cfg.get("paths"))
    if not paths:
        if default_paths:
            paths = list(default_paths)
        else:
            paths = ["/chat/completions"]
    return RequestModulePipeline(parsed_modules, paths)


def build_request_module_overrides(
    config: Mapping[str, Any],
) -> dict[str, RequestModulePipeline]:
    overrides: dict[str, RequestModulePipeline] = {}
    model_list = config.get("model_list") or []
    for entry in model_list:
        if not isinstance(entry, Mapping):
            continue
        name = entry.get("model_name")
        if not name:
            continue
        modules_cfg = entry.get("modules")
        if modules_cfg is None:
            model_params = entry.get("model_params") or {}
            if isinstance(model_params, Mapping):
                modules_cfg = model_params.get("modules")
        if modules_cfg is None:
            continue
        overrides[str(name)] = build_request_module_pipeline(
            modules_cfg,
            enabled_default=True,
            default_paths=["/chat/completions"],
        )
    return overrides


__all__ = [
    "RequestModuleContext",
    "RequestModule",
    "RequestModulePipeline",
    "build_request_module_pipeline",
    "build_request_module_overrides",
]
