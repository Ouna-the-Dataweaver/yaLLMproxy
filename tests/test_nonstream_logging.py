"""Test for non-streaming request logging issue.

Reproduces the bug where non-streaming OpenAI responses don't get their
content logged to the database (full_response is NULL).
"""

from __future__ import annotations

import sys
from pathlib import Path

import httpx
import pytest

sys.path.insert(0, str(Path(__file__).parent.parent / "src"))

from src.core.upstream_transport import clear_upstream_transports, register_upstream_transport
from src.testing import FakeUpstream, ProxyHarness, UpstreamResponse
from src.logging.recorder import RequestLogRecorder


def _build_config(base_url: str) -> dict:
    return {
        "model_list": [
            {
                "model_name": "glm_local",
                "model_params": {
                    "model": "openai/fake",
                    "api_base": base_url,
                    "api_key": "test-key",
                },
            }
        ],
    }


# The exact response from the debug log file
UPSTREAM_RESPONSE = {
    "id": "chatcmpl-bdd972c71873499796af80ee3cbe144c",
    "object": "chat.completion",
    "created": 1769684938,
    "model": "GLM_air_fp8",
    "choices": [
        {
            "index": 0,
            "message": {
                "role": "assistant",
                "content": "mindmap\n  Российские города\n    Москва\n      Столица России",
                "refusal": None,
                "annotations": None,
                "audio": None,
                "function_call": None,
                "tool_calls": [],
                "reasoning": "I need to transform the given text...",
                "reasoning_content": "I need to transform the given text...",
            },
            "logprobs": None,
            "finish_reason": "stop",
            "stop_reason": 151336,
            "token_ids": None,
        }
    ],
    "service_tier": None,
    "system_fingerprint": None,
    "usage": {
        "prompt_tokens": 354,
        "total_tokens": 957,
        "completion_tokens": 603,
        "prompt_tokens_details": None,
    },
    "prompt_logprobs": None,
    "prompt_token_ids": None,
    "kv_transfer_params": None,
}


@pytest.fixture(autouse=True)
def _clear_transport_registry():
    yield
    clear_upstream_transports()


@pytest.mark.asyncio
async def test_nonstream_response_content_accumulated() -> None:
    """Test that non-streaming OpenAI responses get their content accumulated for logging."""
    upstream = FakeUpstream()
    upstream.enqueue(UpstreamResponse(json_body=UPSTREAM_RESPONSE))

    base_url = "http://upstream.local/v1"
    register_upstream_transport("upstream.local", httpx.ASGITransport(app=upstream.app))

    # Track the request log to inspect accumulated content
    captured_log: RequestLogRecorder | None = None
    original_finalize = RequestLogRecorder.finalize

    def capturing_finalize(self, outcome: str) -> None:
        nonlocal captured_log
        captured_log = self
        # Don't call original finalize - we just want to inspect state
        # Mark as finalized to prevent double-finalize
        self._finalized = True

    # Monkey-patch finalize to capture the log before it's finalized
    RequestLogRecorder.finalize = capturing_finalize

    try:
        with ProxyHarness(_build_config(base_url)) as proxy:
            async with proxy.make_async_client() as client:
                response = await client.post(
                    "/v1/chat/completions",
                    json={
                        "model": "glm_local",
                        "messages": [{"role": "user", "content": "hello"}],
                        "stream": False,
                    },
                )

        assert response.status_code == 200
        payload = response.json()

        # Verify the response was returned correctly
        assert payload["choices"][0]["message"]["content"] == "mindmap\n  Российские города\n    Москва\n      Столица России"

        # THE KEY CHECK: Verify the content was accumulated for logging
        assert captured_log is not None, "Request log was not captured"

        # Check accumulated response parts
        accumulated = captured_log._accumulated_response_parts
        print(f"Accumulated response parts: {accumulated}")

        assert len(accumulated) > 0, (
            "LOGGING BUG: _accumulated_response_parts is empty! "
            "Non-streaming response content was not accumulated for database logging."
        )

        full_response = "".join(accumulated)
        assert "mindmap" in full_response, f"Expected 'mindmap' in accumulated response, got: {full_response}"
        assert "Российские города" in full_response, f"Expected Russian text in accumulated response"

        print(f"SUCCESS: Full response was accumulated: {full_response[:100]}...")

    finally:
        # Restore original finalize
        RequestLogRecorder.finalize = original_finalize


@pytest.mark.asyncio
async def test_streaming_response_content_accumulated() -> None:
    """Verify streaming responses DO accumulate content (for comparison)."""
    upstream = FakeUpstream()
    upstream.enqueue_openai_chat_response(
        "This is the streamed content",
        stream=True,
        finish_reason="stop",
    )

    base_url = "http://upstream.local/v1"
    register_upstream_transport("upstream.local", httpx.ASGITransport(app=upstream.app))

    captured_log: RequestLogRecorder | None = None
    original_finalize = RequestLogRecorder.finalize

    def capturing_finalize(self, outcome: str) -> None:
        nonlocal captured_log
        captured_log = self
        self._finalized = True

    RequestLogRecorder.finalize = capturing_finalize

    try:
        with ProxyHarness(_build_config(base_url)) as proxy:
            async with proxy.make_async_client() as client:
                async with client.stream(
                    "POST",
                    "/v1/chat/completions",
                    json={
                        "model": "glm_local",
                        "messages": [{"role": "user", "content": "hello"}],
                        "stream": True,
                    },
                ) as response:
                    # Consume the stream
                    async for _ in response.aiter_raw():
                        pass

        assert captured_log is not None, "Request log was not captured"

        accumulated = captured_log._accumulated_response_parts
        print(f"Streaming accumulated parts: {accumulated}")

        assert len(accumulated) > 0, "Streaming response should accumulate content"
        full_response = "".join(accumulated)
        assert "This is the streamed content" in full_response

        print(f"SUCCESS: Streaming response accumulated: {full_response}")

    finally:
        RequestLogRecorder.finalize = original_finalize


if __name__ == "__main__":
    import asyncio
    asyncio.run(test_nonstream_response_content_accumulated())
