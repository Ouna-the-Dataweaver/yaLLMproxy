#!/usr/bin/env python3
"""Replay a logged proxy request against the upstream endpoint.

Usage:
    python replay_request.py path/to/request.json [--base-url http://host:port]

The script loads the saved request JSON or log, restores headers/body, and
replays the request directly against the upstream server. If the saved
Authorization header is masked, it can look up the API key from
config_default.yaml/config_added.yaml (and their .env files).
"""

from __future__ import annotations

import argparse
import base64
import importlib.util
import json
import logging
import os
import shlex
import sys
from pathlib import Path
from typing import Any, Dict, Optional, Tuple

import httpx

PROJECT_ROOT = Path(__file__).resolve().parent.parent

REQUEST_BODY_START = "-- REQUEST BODY START --"
REQUEST_BODY_END = "-- REQUEST BODY END --"

logger = logging.getLogger("replay_request")


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


def _setup_logging(debug: bool) -> None:
    level = logging.DEBUG if debug else logging.WARNING
    logging.basicConfig(level=level, format="%(levelname)s: %(message)s")


def _redact_headers(headers: Dict[str, str]) -> Dict[str, str]:
    redacted: Dict[str, str] = {}
    for key, value in headers.items():
        if key.lower() == "authorization":
            if isinstance(value, str) and value.startswith("Bearer "):
                token = value[7:]
                redacted[key] = f"Bearer {token[:3]}****" if token else value
            else:
                redacted[key] = "****"
        else:
            redacted[key] = value
    return redacted


def parse_log(
    log_path: Path,
) -> Tuple[str, str, str, Dict[str, str], bytes, bool, Optional[str]]:
    method = ""
    path = ""
    query = ""
    headers: Dict[str, str] = {}
    body_lines: list[str] = []
    is_stream = False
    in_body = False

    with log_path.open("r", encoding="utf-8") as fh:
        for raw_line in fh:
            line = raw_line.rstrip("\n")
            if line.startswith("method=") and not method:
                method = line.split("=", 1)[1].strip() or "POST"
            elif line.startswith("path=") and not path:
                path = line.split("=", 1)[1].strip() or "/v1/chat/completions"
            elif line.startswith("query=") and not query:
                query = line.split("=", 1)[1].strip()
            elif line.startswith("headers=") and not headers:
                payload = line.split("=", 1)[1].strip()
                headers = json.loads(payload)
            elif line.startswith("is_stream="):
                is_stream = line.split("=", 1)[1].strip().lower() == "true"
            elif line.strip() == REQUEST_BODY_START:
                in_body = True
                body_lines.clear()
            elif line.strip() == REQUEST_BODY_END:
                in_body = False
            elif in_body:
                body_lines.append(raw_line)

    if not method or not path or not headers or not body_lines:
        raise ValueError(f"Failed to parse required fields from {log_path}")

    body_text = "".join(body_lines)
    body_bytes = body_text.encode("utf-8")
    return method, path, query, headers, body_bytes, is_stream, None


def _encode_body(body: Any, body_is_json: bool, body_base64: Optional[str]) -> bytes:
    if body_base64:
        return base64.b64decode(body_base64)
    if body_is_json:
        if body is None:
            return b""
        return json.dumps(body, ensure_ascii=False).encode("utf-8")
    if body is None:
        return b""
    if isinstance(body, (dict, list)):
        return json.dumps(body, ensure_ascii=False).encode("utf-8")
    return str(body).encode("utf-8")


def parse_request_json(
    request_path: Path,
) -> Tuple[str, str, str, Dict[str, str], bytes, bool, Optional[str]]:
    payload = json.loads(request_path.read_text(encoding="utf-8"))
    logger.debug("Loaded request JSON from %s", request_path)

    method = payload.get("method") or "POST"
    path = payload.get("path") or "/v1/chat/completions"
    query = payload.get("query") or ""
    headers = payload.get("headers") or {}
    is_stream = bool(payload.get("is_stream"))

    body_is_json = bool(payload.get("body_is_json"))
    body_base64 = payload.get("body_base64")
    body = payload.get("body")
    body_bytes = _encode_body(body, body_is_json, body_base64)

    model = payload.get("model")
    if not model and isinstance(body, dict):
        model = body.get("model")

    logger.debug(
        "Parsed request: method=%s path=%s query=%s model=%s stream=%s body_len=%s",
        method,
        path,
        query,
        model,
        is_stream,
        len(body_bytes),
    )
    return method, path, query, headers, body_bytes, is_stream, model


def parse_request(
    path: Path,
) -> Tuple[str, str, str, Dict[str, str], bytes, bool, Optional[str]]:
    if path.suffix.lower() == ".json":
        return parse_request_json(path)
    return parse_log(path)


def build_url(base_url: Optional[str], headers: Dict[str, str], path: str, query: str) -> str:
    if base_url:
        base = base_url.rstrip("/")
    else:
        host = headers.get("host") or headers.get("Host")
        if not host or host == "proxy_host":
            raise ValueError("No usable host in request; please pass --base-url or use config.")
        scheme = "https" if host.endswith(":443") else "http"
        base = f"{scheme}://{host}"

    if not path.startswith("/"):
        path = "/" + path
    if path.startswith("/v1"):
        path = path[3:]
        if not path:
            path = "/"

    url = base + path
    if query:
        sep = "?" if "?" not in path else "&"
        url = f"{url}{sep}{query}" if not url.endswith("?") else f"{url}{query}"
    logger.debug("Built URL: %s", url)
    return url


def _lookup_model_entry(config: dict, model_name: Optional[str]) -> Optional[dict]:
    if not model_name:
        return None
    for entry in config.get("model_list", []) or []:
        if entry.get("model_name") == model_name:
            return entry
    return None


def resolve_config(config_path: Optional[str]) -> Optional[dict]:
    if load_config is None:
        print("Warning: config loader not available; skipping config lookup.")
        return None
    try:
        config = load_config(config_path)
        logger.debug("Loaded config from %s", config_path or "default path")

        added_path = os.getenv("YALLMP_CONFIG_ADDED")
        if added_path:
            try:
                added_cfg = load_config(added_path)
                config.setdefault("model_list", []).extend(
                    added_cfg.get("model_list", []) or []
                )
            except Exception as exc:
                logger.debug("Failed to load added config: %s", exc)
        else:
            default_added = PROJECT_ROOT / "configs" / "config_added.yaml"
            if default_added.exists():
                try:
                    added_cfg = load_config(str(default_added))
                    config.setdefault("model_list", []).extend(
                        added_cfg.get("model_list", []) or []
                    )
                except Exception as exc:
                    logger.debug("Failed to load added config: %s", exc)
        return config
    except Exception as exc:
        print(f"Warning: failed to load config: {exc}")
        return None


def resolve_api_key(config: Optional[dict], model_name: Optional[str]) -> Optional[str]:
    if not config or not model_name:
        return None
    entry = _lookup_model_entry(config, model_name)
    if not entry:
        return None
    api_key = (entry.get("model_params") or {}).get("api_key")
    if not api_key or not isinstance(api_key, str):
        return None
    if "****" in api_key or api_key.strip().startswith("$"):
        return None
    logger.debug("Resolved API key for model %s: %s", model_name, "found" if api_key else "missing")
    return api_key


def resolve_api_base(config: Optional[dict], model_name: Optional[str]) -> Optional[str]:
    if not config or not model_name:
        return None
    entry = _lookup_model_entry(config, model_name)
    if not entry:
        return None
    api_base = (entry.get("model_params") or {}).get("api_base")
    if not api_base or not isinstance(api_base, str):
        return None
    logger.debug("Resolved api_base for model %s: %s", model_name, api_base or "missing")
    return api_base


def resolve_http2(config: Optional[dict], model_name: Optional[str]) -> Optional[bool]:
    if not config or not model_name:
        return None
    entry = _lookup_model_entry(config, model_name)
    if not entry:
        return None
    http2 = (entry.get("model_params") or {}).get("http2")
    if http2 is None:
        return None
    resolved = bool(http2)
    logger.debug("Resolved http2 for model %s: %s", model_name, resolved)
    return resolved


HOP_BY_HOP_HEADERS = {
    "connection",
    "keep-alive",
    "proxy-authenticate",
    "proxy-authorization",
    "te",
    "trailers",
    "transfer-encoding",
    "upgrade",
}


def filter_headers(
    headers: Dict[str, str], api_key: Optional[str], is_stream: bool
) -> Dict[str, str]:
    cleaned: Dict[str, str] = {}
    normalized_keys: set[str] = set()
    raw_auth: Optional[str] = None
    for key, value in headers.items():
        key_lower = key.lower()
        if key_lower == "authorization":
            raw_auth = value
            continue
        if is_stream and key_lower == "accept":
            continue
        if key_lower in HOP_BY_HOP_HEADERS or key_lower in {
            "host",
            "content-length",
        }:
            continue
        if key_lower in normalized_keys:
            continue
        cleaned[key] = value
        normalized_keys.add(key_lower)

    if "content-type" not in normalized_keys:
        cleaned["Content-Type"] = headers.get("content-type", "application/json")
        normalized_keys.add("content-type")
    if is_stream:
        cleaned["Accept"] = "text/event-stream"
        normalized_keys.add("accept")

    if api_key:
        cleaned["Authorization"] = f"Bearer {api_key}"
    elif raw_auth and "****" not in raw_auth:
        cleaned["Authorization"] = raw_auth
    elif raw_auth:
        print("Warning: Authorization header is masked; set api_key in config.")

    logger.debug("Replay headers: %s", _redact_headers(cleaned))
    return cleaned


def override_request_body(
    body: bytes,
    model_name: Optional[str],
    stream_override: Optional[bool],
) -> bytes:
    if not (model_name or stream_override is not None):
        return body
    try:
        payload = json.loads(body)
    except json.JSONDecodeError:
        return body

    changed = False
    if model_name:
        payload["model"] = model_name
        changed = True
    if stream_override is not None:
        payload["stream"] = stream_override
        changed = True
    if not changed:
        return body
    updated = json.dumps(payload, ensure_ascii=False).encode("utf-8")
    logger.debug(
        "Overrode request body (model=%s stream_override=%s) new_len=%s",
        model_name,
        stream_override,
        len(updated),
    )
    return updated


def build_curl_command(
    url: str,
    method: str,
    headers: Dict[str, str],
    body: bytes,
    is_stream: bool,
) -> str:
    parts = ["curl", "-X", shlex.quote(method), shlex.quote(url)]
    if is_stream:
        parts.append("-N")
    for key, value in headers.items():
        header = f"{key}: {value}"
        parts.extend(["-H", shlex.quote(header)])
    if body:
        parts.extend(["--data", shlex.quote(body.decode("utf-8", errors="replace"))])
    return " ".join(parts)


def main() -> None:
    parser = argparse.ArgumentParser(description="Replay a logged request")
    parser.add_argument("request_path", type=Path, help="Path to request JSON or log file")
    parser.add_argument(
        "--base-url",
        help="Override the target base URL (default: derive from config or Host header)",
    )
    parser.add_argument(
        "--config",
        help="Path to config_default.yaml (default: configs/config_default.yaml or YALLMP_CONFIG_DEFAULT)",
    )
    parser.add_argument(
        "--timeout",
        type=float,
        default=60.0,
        help="Request timeout in seconds (default: 60)",
    )
    parser.add_argument(
        "--http2",
        action="store_true",
        help="Enable HTTP/2 for the outbound request",
    )
    parser.add_argument(
        "--model",
        help="Override the model name inside the JSON request body",
    )
    parser.add_argument(
        "--stream-mode",
        choices=["auto", "on", "off"],
        default="auto",
        help="Force streaming on/off (default: auto uses value from log)",
    )
    parser.add_argument(
        "--print-curl",
        action="store_true",
        help="Print and save the equivalent curl command",
    )
    parser.add_argument(
        "--dry-run",
        action="store_true",
        help="Only print the request info without sending it",
    )
    parser.add_argument(
        "--debug",
        action="store_true",
        help="Enable verbose debug logging",
    )
    args = parser.parse_args()

    _setup_logging(args.debug)
    logger.debug("Replay starting with args: %s", vars(args))

    method, path, query, headers, body, is_stream, request_model = parse_request(
        args.request_path
    )

    stream_override = None
    if args.stream_mode == "on":
        stream_override = True
        is_stream = True
    elif args.stream_mode == "off":
        stream_override = False
        is_stream = False

    body = override_request_body(body, args.model, stream_override)

    config = resolve_config(args.config)
    model_name = args.model or request_model
    api_key = resolve_api_key(config, model_name)
    api_base = resolve_api_base(config, model_name)
    http2_default = resolve_http2(config, model_name)

    base_url = args.base_url
    if not base_url:
        host = headers.get("host") or headers.get("Host")
        if not host or host == "proxy_host":
            base_url = api_base
    logger.debug("Base URL resolved as: %s", base_url or "missing")

    url = build_url(base_url, headers, path, query)
    replay_headers = filter_headers(headers, api_key, is_stream)

    if args.print_curl:
        curl_cmd = build_curl_command(url, method, replay_headers, body, is_stream)
        output_path = args.request_path.with_suffix(".curl.txt")
        output_path.write_text(curl_cmd + "\n", encoding="utf-8")
        print("Equivalent curl command:\n", curl_cmd, sep="")
        print(f"Curl command saved to {output_path}")

    if args.dry_run:
        print("Dry run; request not sent.")
        return

    print(f"Sending {method} request to {url} (stream={is_stream})")
    http2_enabled = args.http2 or bool(http2_default)
    logger.debug("HTTP/2 enabled: %s", http2_enabled)

    with httpx.Client(timeout=args.timeout, http2=http2_enabled) as client:
        if is_stream:
            with client.stream(method, url, headers=replay_headers, content=body) as resp:
                print(f"Status: {resp.status_code}")
                logger.debug("Response headers: %s", dict(resp.headers))
                print("-- STREAM BEGIN --")
                for chunk in resp.iter_text():
                    if chunk:
                        sys.stdout.write(chunk)
                        sys.stdout.flush()
                print("\n-- STREAM END --")
        else:
            resp = client.request(method, url, headers=replay_headers, content=body)
            print(f"Status: {resp.status_code}")
            logger.debug("Response headers: %s", dict(resp.headers))
            print(resp.text)


if __name__ == "__main__":
    main()
