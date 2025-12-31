#!/usr/bin/env python3
"""Print run-time config values as KEY=VALUE pairs for shell scripts."""

from __future__ import annotations

import argparse
import sys
from pathlib import Path
from typing import Any

import importlib.util
import logging

PROJECT_ROOT = Path(__file__).resolve().parent.parent

logging.basicConfig(level=logging.WARNING)


def _load_config_loader():
    config_module = PROJECT_ROOT / "src" / "config_loader.py"
    if not config_module.exists():
        return None
    spec = importlib.util.spec_from_file_location("config_loader", config_module)
    if spec is None or spec.loader is None:
        return None
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return getattr(module, "load_config", None)


load_config = _load_config_loader()


def _get(cfg: dict, *keys: str) -> Any:
    cur: Any = cfg
    for key in keys:
        if not isinstance(cur, dict):
            return None
        cur = cur.get(key)
    return cur


def _to_int(value: Any) -> int | None:
    if value is None:
        return None
    try:
        return int(value)
    except (TypeError, ValueError):
        return None


def _to_str(value: Any) -> str | None:
    if value is None:
        return None
    return str(value)


def _default_target_host(proxy_host: str | None) -> str:
    if proxy_host and proxy_host not in {"0.0.0.0", "::"}:
        return proxy_host
    return "127.0.0.1"


def main() -> int:
    parser = argparse.ArgumentParser(description="Print config values for run scripts")
    parser.add_argument(
        "--config",
        help="Path to config_default.yaml (default: YALLMP_CONFIG_DEFAULT or configs/config_default.yaml)",
    )
    args = parser.parse_args()

    cfg: dict = {}
    if load_config is not None:
        try:
            cfg = load_config(args.config)
        except Exception as exc:
            print(f"[WARN] Failed to load config: {exc}", file=sys.stderr)

    proxy_host = _to_str(_get(cfg, "proxy_settings", "server", "host")) or "127.0.0.1"
    proxy_port = _to_int(_get(cfg, "proxy_settings", "server", "port")) or 7978

    fwd_listen_host = _to_str(_get(cfg, "forwarder_settings", "listen", "host")) or "0.0.0.0"
    fwd_listen_port = _to_int(_get(cfg, "forwarder_settings", "listen", "port")) or 7979
    fwd_target_host = _to_str(_get(cfg, "forwarder_settings", "target", "host"))
    if not fwd_target_host:
        fwd_target_host = _default_target_host(proxy_host)
    fwd_target_port = _to_int(_get(cfg, "forwarder_settings", "target", "port")) or proxy_port

    lines = [
        f"CFG_PROXY_HOST={proxy_host}",
        f"CFG_PROXY_PORT={proxy_port}",
        f"CFG_FORWARD_LISTEN_HOST={fwd_listen_host}",
        f"CFG_FORWARD_LISTEN_PORT={fwd_listen_port}",
        f"CFG_FORWARD_TARGET_HOST={fwd_target_host}",
        f"CFG_FORWARD_TARGET_PORT={fwd_target_port}",
    ]
    sys.stdout.write("\n".join(lines) + "\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
