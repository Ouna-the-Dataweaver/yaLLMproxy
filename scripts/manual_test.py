#!/usr/bin/env python3
"""Manual test script for the yaLLM proxy.

Usage:
    python manual_test.py [--base-url http://host:port]

This script:
1. Lists available models from the proxy
2. Sends a simple "hello world" chat completion request
"""

from __future__ import annotations

import argparse
import json
import sys

import httpx


def list_models(base_url: str) -> bool:
    """List available models from the proxy."""
    url = f"{base_url.rstrip('/')}/v1/models"
    print("\n" + "=" * 60)
    print(f"GET {url}")
    print("=" * 60)

    try:
        with httpx.Client(timeout=30.0) as client:
            resp = client.get(url)
            print(f"Status: {resp.status_code}")
            if resp.status_code == 200:
                data = resp.json()
                models = data.get("data", [])
                print(f"Found {len(models)} model(s):")
                for model in models:
                    model_id = model.get("id", "unknown")
                    print(f"  - {model_id}")
                return True
            else:
                print(f"Error: {resp.text}")
                return False
    except httpx.RequestError as e:
        print(f"Request error: {e}")
        return False


def send_hello_world(base_url: str, model: str | None = None) -> bool:
    """Send a simple hello world chat completion request."""
    url = f"{base_url.rstrip('/')}/v1/chat/completions"

    # Use first available model if none specified, or default to a common one
    if not model:
        model = "MiniMax-M2"

    payload = {
        "model": model,
        "messages": [
            {"role": "user", "content": "Hello, world!"}
        ],
        "max_tokens": 50,
        "temperature": 0.7,
    }

    print("\n" + "=" * 60)
    print(f"POST {url}")
    print(f"Model: {model}")
    print("=" * 60)

    try:
        with httpx.Client(timeout=60.0) as client:
            resp = client.post(url, json=payload)
            print(f"Status: {resp.status_code}")

            if resp.status_code == 200:
                data = resp.json()
                choice = data.get("choices", [{}])[0]
                message = choice.get("message", {})
                content = message.get("content", "").strip()

                print("\nResponse:")
                print("-" * 40)
                print(content)
                print("-" * 40)

                # Print usage stats if available
                usage = data.get("usage")
                if usage:
                    print(f"\nUsage: {usage}")

                return True
            else:
                print(f"Error response:")
                try:
                    error_data = resp.json()
                    if "error" in error_data:
                        error = error_data["error"]
                        print(f"  Type: {error.get('type', 'unknown')}")
                        print(f"  Message: {error.get('message', 'no message')}")
                    else:
                        print(json.dumps(error_data, indent=2))
                except json.JSONDecodeError:
                    print(resp.text)
                return False
    except httpx.RequestError as e:
        print(f"Request error: {e}")
        return False


def main() -> None:
    parser = argparse.ArgumentParser(description="Manual test for yaLLM proxy")
    parser.add_argument(
        "--base-url",
        default="http://127.0.0.1:6666",
        help="Base URL of the proxy server (default: http://127.0.0.1:6666)",
    )
    parser.add_argument(
        "--model",
        help="Model name to use for chat completion (default: auto-detect or MiniMax-M2)",
    )
    parser.add_argument(
        "--skip-models",
        action="store_true",
        help="Skip the models list check",
    )
    parser.add_argument(
        "--skip-chat",
        action="store_true",
        help="Skip the chat completion test",
    )
    args = parser.parse_args()

    print("yaLLM Proxy Manual Test")
    print("=" * 60)
    print(f"Target: {args.base_url}")

    results = []

    if not args.skip_models:
        results.append(("List Models", list_models(args.base_url)))
    else:
        print("\n[SKIPPED] List Models")

    if not args.skip_chat:
        results.append(("Chat Completion", send_hello_world(args.base_url, args.model)))
    else:
        print("\n[SKIPPED] Chat Completion")

    # Summary
    print("\n" + "=" * 60)
    print("Summary")
    print("=" * 60)

    all_passed = True
    for name, passed in results:
        status = "PASS" if passed else "FAIL"
        print(f"  {name}: {status}")
        if not passed:
            all_passed = False

    if all_passed:
        print("\nAll tests passed!")
        sys.exit(0)
    else:
        print("\nSome tests failed!")
        sys.exit(1)


if __name__ == "__main__":
    main()

