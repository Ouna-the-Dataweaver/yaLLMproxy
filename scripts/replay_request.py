#!/usr/bin/env python3
"""Replay a logged proxy request against the same endpoint.

Usage:
    python replay_request.py path/to/log.log [--base-url http://host:port]

The script extracts the original method, path, headers, body and (if present)
the streaming flag from the log, then replays the request directly against the
server. Overrides for base URL, model name and streaming mode let you quickly
try different combinations without editing the original log.
"""

from __future__ import annotations

import argparse
import json
import shlex
import sys
from pathlib import Path
from typing import Dict, Optional, Tuple

import httpx


REQUEST_BODY_START = "-- REQUEST BODY START --"
REQUEST_BODY_END = "-- REQUEST BODY END --"


def parse_log(log_path: Path) -> Tuple[str, str, str, Dict[str, str], bytes, bool]:
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
    return method, path, query, headers, body_bytes, is_stream


def build_url(base_url: Optional[str], headers: Dict[str, str], path: str, query: str) -> str:
    if base_url:
        base = base_url.rstrip("/")
    else:
        host = headers.get("host") or headers.get("Host")
        if not host:
            raise ValueError("No host header in log; please pass --base-url")
        scheme = "https" if host.endswith(":443") else "http"
        base = f"{scheme}://{host}"
    if not path.startswith("/"):
        path = "/" + path
    url = base + path
    if query:
        sep = "?" if "?" not in path else "&"
        url = f"{url}{sep}{query}" if not url.endswith("?") else f"{url}{query}"
    return url


def filter_headers(headers: Dict[str, str]) -> Dict[str, str]:
    skip = {"content-length", "host"}
    cleaned: Dict[str, str] = {}
    for key, value in headers.items():
        if key.lower() in skip:
            continue
        cleaned[key] = value
    return cleaned


def build_curl_command(url: str, method: str, headers: Dict[str, str], body: bytes) -> str:
    parts = ["curl", "-X", shlex.quote(method), shlex.quote(url)]
    for key, value in headers.items():
        header = f"{key}: {value}"
        parts.extend(["-H", shlex.quote(header)])
    parts.extend(["--data", shlex.quote(body.decode("utf-8"))])
    return " ".join(parts)


def override_request_body(
    body: bytes,
    model_name: Optional[str],
    stream_override: Optional[bool],
) -> bytes:
    """Return a possibly modified JSON request body with overrides applied."""
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
    return json.dumps(payload, ensure_ascii=False).encode("utf-8")


def main() -> None:
    parser = argparse.ArgumentParser(description="Replay a logged request")
    parser.add_argument("log_path", type=Path, help="Path to request log file")
    parser.add_argument(
        "--base-url",
        help="Override the target base URL (default: derive from Host header)",
    )
    parser.add_argument(
        "--timeout",
        type=float,
        default=60.0,
        help="Request timeout in seconds (default: 60)",
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
        help="Print the equivalent curl command before executing",
    )
    parser.add_argument(
        "--dry-run",
        action="store_true",
        help="Only print the request info without sending it",
    )
    args = parser.parse_args()

    method, path, query, headers, body, is_stream = parse_log(args.log_path)
    stream_override = None
    if args.stream_mode == "on":
        stream_override = True
        is_stream = True
    elif args.stream_mode == "off":
        stream_override = False
        is_stream = False

    body = override_request_body(body, args.model, stream_override)
    url = build_url(args.base_url, headers, path, query)
    replay_headers = filter_headers(headers)

    if args.print_curl:
        curl_cmd = build_curl_command(url, method, replay_headers, body)
        print("Equivalent curl command:\n", curl_cmd, sep="")

    if args.dry_run:
        print("Dry run; request not sent.")
        return

    print(f"Sending {method} request to {url} (stream={is_stream})")
    client = httpx.Client(timeout=args.timeout)
    try:
        if is_stream:
            with client.stream(method, url, headers=replay_headers, content=body) as resp:
                print(f"Status: {resp.status_code}")
                print("-- STREAM BEGIN --")
                for chunk in resp.iter_text():
                    if chunk:
                        sys.stdout.write(chunk)
                        sys.stdout.flush()
                print("\n-- STREAM END --")
        else:
            resp = client.request(method, url, headers=replay_headers, content=body)
            print(f"Status: {resp.status_code}")
            print(resp.text)
    finally:
        client.close()


if __name__ == "__main__":
    main()
