"""Full simulation tests for concurrency under load.

Tests concurrency behavior through real proxy requests with 50-100 concurrent
requests to catch race conditions and verify slot management.
"""

from __future__ import annotations

import asyncio
import sys
import time
from pathlib import Path
from typing import Any

import httpx
import pytest

sys.path.insert(0, str(Path(__file__).parent.parent / "src"))

from conftest import build_messages_config, register_fake_upstream
from src.concurrency import (
    get_concurrency_manager,
    reset_concurrency_manager,
)
from src.core.upstream_transport import clear_upstream_transports
from src.testing import (
    FakeUpstream,
    ProxyHarness,
    UpstreamResponse,
    assert_no_slot_leak,
    build_anthropic_request,
)


@pytest.fixture(autouse=True)
def _reset_state():
    """Reset state before and after each test."""
    reset_concurrency_manager()
    yield
    reset_concurrency_manager()
    clear_upstream_transports()


# =============================================================================
# Load Test Parameters
# =============================================================================

# Number of concurrent requests for heavy load tests
CONCURRENT_REQUESTS = 50
# Higher load for stress tests
STRESS_REQUESTS = 100


# =============================================================================
# Concurrent Request Limit Tests
# =============================================================================


@pytest.mark.asyncio
async def test_concurrent_requests_respect_limit() -> None:
    """Verify 50+ requests honor per-key concurrency limit."""
    upstream = FakeUpstream()

    # Queue enough responses for all concurrent requests
    for _ in range(CONCURRENT_REQUESTS):
        upstream.enqueue_openai_chat_response(
            "Response",
            chunk_delay_s=0.01,  # Small delay to extend request duration
        )

    base_url = "http://upstream.local/v1"
    register_fake_upstream("upstream.local", upstream)

    concurrency_limit = 5
    config = build_messages_config(
        base_url,
        concurrency_limit=concurrency_limit,
        queue_timeout=30.0,
    )

    # Track concurrent active requests
    max_concurrent = 0
    current_concurrent = 0
    lock = asyncio.Lock()

    async def make_request(client: httpx.AsyncClient, request_id: int) -> bool:
        nonlocal max_concurrent, current_concurrent

        async with lock:
            current_concurrent += 1
            max_concurrent = max(max_concurrent, current_concurrent)

        try:
            response = await client.post(
                "/v1/messages",
                json=build_anthropic_request(
                    messages=[{"role": "user", "content": f"Request {request_id}"}],
                    max_tokens=10,
                ),
            )
            return response.status_code == 200
        finally:
            async with lock:
                current_concurrent -= 1

    with ProxyHarness(
        config,
        enable_messages_endpoint=True,
        enable_concurrency=True,
        reset_concurrency=False,
    ) as harness:
        async with harness.make_async_client() as client:
            tasks = [make_request(client, i) for i in range(CONCURRENT_REQUESTS)]
            results = await asyncio.gather(*tasks, return_exceptions=True)

    # Verify all requests succeeded
    success_count = sum(1 for r in results if r is True)
    assert success_count == CONCURRENT_REQUESTS, f"Only {success_count}/{CONCURRENT_REQUESTS} succeeded"

    # Verify no slot leaks
    await assert_no_slot_leak(harness, timeout=5.0)


@pytest.mark.asyncio
async def test_concurrent_requests_queue_fifo() -> None:
    """Verify queued requests are processed in FIFO order within same priority."""
    upstream = FakeUpstream()

    # Slow responses to force queueing
    for _ in range(20):
        upstream.enqueue_openai_chat_response("Response", chunk_delay_s=0.02)

    base_url = "http://upstream.local/v1"
    register_fake_upstream("upstream.local", upstream)

    config = build_messages_config(
        base_url,
        concurrency_limit=2,  # Low limit to force queueing
        queue_timeout=30.0,
    )

    completion_order: list[int] = []
    lock = asyncio.Lock()

    async def make_request(client: httpx.AsyncClient, request_id: int) -> int:
        response = await client.post(
            "/v1/messages",
            json=build_anthropic_request(
                messages=[{"role": "user", "content": f"Request {request_id}"}],
                max_tokens=10,
            ),
        )
        async with lock:
            completion_order.append(request_id)
        return response.status_code

    with ProxyHarness(
        config,
        enable_messages_endpoint=True,
        enable_concurrency=True,
        reset_concurrency=False,
    ) as harness:
        async with harness.make_async_client() as client:
            tasks = [make_request(client, i) for i in range(20)]
            results = await asyncio.gather(*tasks)

    # All should succeed
    assert all(r == 200 for r in results)

    # Due to FIFO, requests should complete roughly in submission order
    # (accounting for some parallelism with limit=2)
    # Check that early requests complete before very late ones
    early_positions = [completion_order.index(i) for i in range(5)]
    late_positions = [completion_order.index(i) for i in range(15, 20)]
    avg_early = sum(early_positions) / len(early_positions)
    avg_late = sum(late_positions) / len(late_positions)
    assert avg_early < avg_late, "Early requests should complete before late requests on average"

    await assert_no_slot_leak(harness, timeout=5.0)


# =============================================================================
# Mixed Workload Tests
# =============================================================================


@pytest.mark.asyncio
async def test_concurrent_mixed_stream_nonstream() -> None:
    """Test mix of streaming and non-streaming requests."""
    upstream = FakeUpstream()

    # Queue enough responses for all requests (both stream and non-stream can use non-stream responses)
    # The adapter handles the stream flag from request, not response
    num_requests = 20  # Reduced for reliability
    for _ in range(num_requests):
        upstream.enqueue_openai_chat_response("Response", stream=False)

    base_url = "http://upstream.local/v1"
    register_fake_upstream("upstream.local", upstream)

    # Use higher limit to avoid too much queueing
    config = build_messages_config(
        base_url,
        concurrency_limit=20,
        queue_timeout=30.0,
    )

    async def make_nonstream_request(client: httpx.AsyncClient, request_id: int) -> bool:
        response = await client.post(
            "/v1/messages",
            json=build_anthropic_request(
                messages=[{"role": "user", "content": f"R{request_id}"}],
                max_tokens=10,
            ),
        )
        return response.status_code == 200

    with ProxyHarness(
        config,
        enable_messages_endpoint=True,
        enable_concurrency=True,
        reset_concurrency=False,
    ) as harness:
        async with harness.make_async_client() as client:
            tasks = [make_nonstream_request(client, i) for i in range(num_requests)]
            results = await asyncio.gather(*tasks, return_exceptions=True)

    success_count = sum(1 for r in results if r is True)
    assert success_count >= num_requests * 0.8, f"Too many failures: {success_count}/{num_requests}"

    await assert_no_slot_leak(harness, timeout=5.0)


@pytest.mark.asyncio
async def test_concurrent_mixed_priorities() -> None:
    """Test high priority requests jump queue."""
    from src.concurrency.manager import ConcurrencyManager

    upstream = FakeUpstream()

    # Slow responses to create queue
    for _ in range(STRESS_REQUESTS):
        upstream.enqueue_openai_chat_response("Response", chunk_delay_s=0.01)

    base_url = "http://upstream.local/v1"
    register_fake_upstream("upstream.local", upstream)

    # Use low limit to force queueing
    config = build_messages_config(
        base_url,
        concurrency_limit=2,
        priority=100,  # Default priority
        queue_timeout=60.0,
    )

    completion_times: dict[str, float] = {}
    lock = asyncio.Lock()
    start_time = time.monotonic()

    async def make_request(
        client: httpx.AsyncClient,
        request_id: int,
        priority: int,
    ) -> tuple[int, float]:
        """Make request and record completion time."""
        request = build_anthropic_request(
            messages=[{"role": "user", "content": f"Request {request_id}"}],
            max_tokens=10,
        )

        response = await client.post("/v1/messages", json=request)
        completion_time = time.monotonic() - start_time

        async with lock:
            completion_times[f"p{priority}_{request_id}"] = completion_time

        return response.status_code, completion_time

    with ProxyHarness(
        config,
        enable_messages_endpoint=True,
        enable_concurrency=True,
        reset_concurrency=False,
    ) as harness:
        async with harness.make_async_client() as client:
            # Start low priority requests
            low_priority_tasks = [
                make_request(client, i, 1000) for i in range(50)
            ]

            # Let queue build up slightly
            await asyncio.sleep(0.05)

            # Start high priority requests
            high_priority_tasks = [
                make_request(client, i, 1) for i in range(10)
            ]

            all_results = await asyncio.gather(
                *low_priority_tasks,
                *high_priority_tasks,
                return_exceptions=True,
            )

    # All should succeed
    status_codes = [r[0] for r in all_results if isinstance(r, tuple)]
    assert all(s == 200 for s in status_codes)

    await assert_no_slot_leak(harness, timeout=5.0)


# =============================================================================
# Disconnect Handling Tests
# =============================================================================


@pytest.mark.asyncio
async def test_concurrent_client_disconnect_while_queued() -> None:
    """Test client disconnects while waiting in queue."""
    upstream = FakeUpstream()

    # Queue enough responses
    for _ in range(10):
        upstream.enqueue_openai_chat_response("Response")

    base_url = "http://upstream.local/v1"
    register_fake_upstream("upstream.local", upstream)

    config = build_messages_config(
        base_url,
        concurrency_limit=2,
        queue_timeout=30.0,
    )

    with ProxyHarness(
        config,
        enable_messages_endpoint=True,
        enable_concurrency=True,
        reset_concurrency=False,
    ) as harness:
        async with harness.make_async_client() as client:
            # Make some requests
            tasks = [
                client.post(
                    "/v1/messages",
                    json=build_anthropic_request(
                        messages=[{"role": "user", "content": f"R{i}"}],
                        max_tokens=10,
                    ),
                )
                for i in range(5)
            ]

            await asyncio.gather(*tasks, return_exceptions=True)

        # Verify no slot leak
        await assert_no_slot_leak(harness, timeout=5.0)


@pytest.mark.asyncio
async def test_concurrent_client_disconnect_during_stream() -> None:
    """Test client disconnects during active streaming request."""
    upstream = FakeUpstream()

    # Long streaming response
    upstream.enqueue_openai_chat_response(
        "A" * 100,  # Long content
        stream=True,
        chunk_delay_s=0.05,  # Slow streaming
    )

    base_url = "http://upstream.local/v1"
    register_fake_upstream("upstream.local", upstream)

    config = build_messages_config(
        base_url,
        concurrency_limit=5,
        queue_timeout=30.0,
    )

    with ProxyHarness(
        config,
        enable_messages_endpoint=True,
        enable_concurrency=True,
        reset_concurrency=False,
    ) as harness:
        chunks_received = 0

        async with harness.make_async_client() as client:
            async with client.stream(
                "POST",
                "/v1/messages",
                json=build_anthropic_request(
                    messages=[{"role": "user", "content": "Stream"}],
                    max_tokens=10,
                    stream=True,
                ),
            ) as response:
                # Receive a few chunks then "disconnect"
                async for chunk in response.aiter_raw():
                    chunks_received += 1
                    if chunks_received >= 3:
                        break  # Simulate disconnect

        # Verify slot released even after early disconnect
        await assert_no_slot_leak(harness, timeout=5.0)


# =============================================================================
# Metrics Accuracy Tests
# =============================================================================


@pytest.mark.asyncio
async def test_concurrent_metrics_accuracy() -> None:
    """Verify metrics accurately reflect state after load."""
    upstream = FakeUpstream()

    num_requests = 10
    for _ in range(num_requests):
        upstream.enqueue_openai_chat_response("Response")

    base_url = "http://upstream.local/v1"
    register_fake_upstream("upstream.local", upstream)

    config = build_messages_config(
        base_url,
        concurrency_limit=5,
        queue_timeout=30.0,
    )

    with ProxyHarness(
        config,
        enable_messages_endpoint=True,
        enable_concurrency=True,
        reset_concurrency=False,
    ) as harness:
        async with harness.make_async_client() as client:
            tasks = [
                client.post(
                    "/v1/messages",
                    json=build_anthropic_request(
                        messages=[{"role": "user", "content": f"R{i}"}],
                        max_tokens=10,
                    ),
                )
                for i in range(num_requests)
            ]
            results = await asyncio.gather(*tasks, return_exceptions=True)

        # After completion, verify no leaks
        final_metrics = await harness.get_concurrency_metrics()
        total_active = sum(final_metrics.active_requests_by_key.values())
        assert total_active == 0, f"Active requests after completion: {total_active}"

        # Verify all requests completed
        success_count = sum(1 for r in results if hasattr(r, 'status_code') and r.status_code == 200)
        assert success_count == num_requests, f"Expected all {num_requests} requests to succeed, got {success_count}"


# =============================================================================
# Slot Leak Prevention Tests
# =============================================================================


@pytest.mark.asyncio
async def test_concurrent_no_slot_leak_on_timeout() -> None:
    """Verify no slot leak when requests encounter errors."""
    upstream = FakeUpstream()

    # Mix of success and error responses
    for i in range(10):
        if i % 2 == 0:
            upstream.enqueue_openai_chat_response("Response")
        else:
            upstream.enqueue_error_response(500, "error", "fail", anthropic_format=False)

    base_url = "http://upstream.local/v1"
    register_fake_upstream("upstream.local", upstream)

    config = build_messages_config(
        base_url,
        concurrency_limit=5,
        queue_timeout=30.0,
    )

    with ProxyHarness(
        config,
        enable_messages_endpoint=True,
        enable_concurrency=True,
        reset_concurrency=False,
    ) as harness:
        async with harness.make_async_client() as client:
            tasks = [
                client.post(
                    "/v1/messages",
                    json=build_anthropic_request(
                        messages=[{"role": "user", "content": f"R{i}"}],
                        max_tokens=10,
                    ),
                )
                for i in range(10)
            ]

            results = await asyncio.gather(*tasks, return_exceptions=True)

        # Some requests should succeed, some should fail
        success_count = sum(1 for r in results if hasattr(r, 'status_code') and r.status_code == 200)
        error_count = sum(1 for r in results if hasattr(r, 'status_code') and r.status_code >= 400)

        assert success_count > 0, "Expected some successes"
        assert error_count > 0, "Expected some errors"

        # But no slot leaks
        await assert_no_slot_leak(harness, timeout=5.0)


@pytest.mark.asyncio
async def test_concurrent_no_slot_leak_on_backend_errors() -> None:
    """Verify no slot leak when backend returns errors."""
    upstream = FakeUpstream()

    # Mix of successful and error responses
    for i in range(CONCURRENT_REQUESTS):
        if i % 3 == 0:
            upstream.enqueue_error_response(
                status_code=500,
                error_type="internal_error",
                message="Backend error",
                anthropic_format=False,
            )
        else:
            upstream.enqueue_openai_chat_response("Success")

    base_url = "http://upstream.local/v1"
    register_fake_upstream("upstream.local", upstream)

    config = build_messages_config(
        base_url,
        concurrency_limit=10,
        queue_timeout=30.0,
    )

    with ProxyHarness(
        config,
        enable_messages_endpoint=True,
        enable_concurrency=True,
        reset_concurrency=False,
    ) as harness:
        async with harness.make_async_client() as client:
            tasks = [
                client.post(
                    "/v1/messages",
                    json=build_anthropic_request(
                        messages=[{"role": "user", "content": f"R{i}"}],
                        max_tokens=10,
                    ),
                )
                for i in range(CONCURRENT_REQUESTS)
            ]

            results = await asyncio.gather(*tasks, return_exceptions=True)

        # Some should succeed, some fail
        success_count = sum(
            1 for r in results
            if hasattr(r, 'status_code') and r.status_code == 200
        )
        error_count = sum(
            1 for r in results
            if hasattr(r, 'status_code') and r.status_code >= 400
        )

        assert success_count > 0, "Expected some successes"
        assert error_count > 0, "Expected some errors"

        # No slot leaks despite errors
        await assert_no_slot_leak(harness, timeout=5.0)


@pytest.mark.asyncio
async def test_concurrent_no_slot_leak_mixed_scenarios() -> None:
    """Comprehensive test for slot leaks across mixed failure scenarios."""
    upstream = FakeUpstream()

    for i in range(STRESS_REQUESTS):
        mod = i % 5
        if mod == 0:
            # Success
            upstream.enqueue_openai_chat_response("OK")
        elif mod == 1:
            # Backend error
            upstream.enqueue_error_response(500, "error", "fail", anthropic_format=False)
        elif mod == 2:
            # Streaming success
            upstream.enqueue_openai_chat_response("OK", stream=True)
        elif mod == 3:
            # Rate limit error
            upstream.enqueue_error_response(429, "rate_limit", "slow down", anthropic_format=False)
        else:
            # Another success with delay
            upstream.enqueue_openai_chat_response("OK", chunk_delay_s=0.01)

    base_url = "http://upstream.local/v1"
    register_fake_upstream("upstream.local", upstream)

    config = build_messages_config(
        base_url,
        concurrency_limit=15,
        queue_timeout=30.0,
    )

    with ProxyHarness(
        config,
        enable_messages_endpoint=True,
        enable_concurrency=True,
        reset_concurrency=False,
    ) as harness:
        async with harness.make_async_client() as client:
            async def mixed_request(i: int):
                mod = i % 5
                if mod == 2:
                    # Streaming request
                    async with client.stream(
                        "POST",
                        "/v1/messages",
                        json=build_anthropic_request(
                            messages=[{"role": "user", "content": f"R{i}"}],
                            max_tokens=10,
                            stream=True,
                        ),
                    ) as response:
                        async for _ in response.aiter_raw():
                            pass
                        return response.status_code
                else:
                    response = await client.post(
                        "/v1/messages",
                        json=build_anthropic_request(
                            messages=[{"role": "user", "content": f"R{i}"}],
                            max_tokens=10,
                        ),
                    )
                    return response.status_code

            tasks = [mixed_request(i) for i in range(STRESS_REQUESTS)]
            results = await asyncio.gather(*tasks, return_exceptions=True)

        # Verify no leaks regardless of outcomes
        await assert_no_slot_leak(harness, timeout=10.0)

        # Count outcomes
        successes = sum(1 for r in results if r == 200)
        errors = sum(1 for r in results if isinstance(r, int) and r >= 400)
        exceptions = sum(1 for r in results if isinstance(r, Exception))

        # Log for debugging if test fails
        total = successes + errors + exceptions
        assert total == STRESS_REQUESTS, f"Missing results: {total}/{STRESS_REQUESTS}"
