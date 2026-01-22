# Testing Guide

This document describes the testing architecture for yaLLMproxy, with emphasis on the real-life simulation tests that validate the proxy against realistic client and upstream scenarios.

## Test Architecture Overview

yaLLMproxy employs a layered testing approach that combines traditional unit tests with comprehensive simulation tests. The simulation tests are particularly important because they test the proxy through its actual HTTP interfaces using fake client and upstream implementations, catching integration issues that unit tests might miss.

The testing infrastructure includes:

- **FakeUpstream**: A simulated upstream API server that can enqueue responses, simulate errors, and record received requests
- **ProxyHarness**: A test harness that starts a complete proxy instance with configurable endpoints and modules
- **Request builders**: Utilities to construct valid Anthropic-format requests
- **Assertion helpers**: Validators for response structure and slot management

## Running Tests

Run all tests using the Taskfile command:

```bash
task test
```

Run specific test files:

```bash
uv run pytest tests/test_simulated_messages.py -v
uv run pytest tests/test_simulated_stream_adapter.py -v
uv run pytest tests/test_simulated_translator.py -v
uv run pytest tests/test_simulated_concurrency.py -v
```

Run with coverage:

```bash
uv run pytest --cov=src --cov-report=html
```

## Simulation Test Suite

The simulation tests are located in `tests/test_simulated_*.py` and test the proxy against realistic scenarios that approximate production use cases.

### test_simulated_messages.py

This test module validates the complete `/v1/messages` endpoint flow, from receiving Anthropic-format requests through translation to OpenAI format, forwarding to the upstream, translating responses back, and returning Anthropic-format responses.

**Happy Path Tests**

| Test | Description |
|------|-------------|
| `test_messages_to_openai_translation_nonstream` | Verifies complete non-streaming flow with proper request/response translation |
| `test_messages_to_openai_translation_streaming` | Validates SSE adaptation for streaming responses |
| `test_messages_tool_use_roundtrip` | Ensures tool_use blocks translate correctly in both directions |
| `test_messages_tool_result_translation` | Confirms tool_result in requests becomes tool messages for upstream |

**Slot Release Verification Tests**

These tests are critical for concurrency safety. They verify that request slots are properly released when requests complete, error out, or are cancelled.

| Test | Description |
|------|-------------|
| `test_messages_backend_error_releases_slot` | Slot released when backend returns 500 error |
| `test_messages_invalid_model_releases_slot` | Slot released when model not found (400/404) |
| `test_messages_stream_slot_release_on_completion` | Slot released only after stream fully consumed |
| `test_messages_slot_release_on_all_error_paths` | Systematic test covering all error paths (invalid JSON, missing fields) |

**System Message Translation Tests**

| Test | Description |
|------|-------------|
| `test_messages_system_string_translation` | System prompt as string translates correctly |
| `test_messages_system_blocks_translation` | System prompt as content blocks translates correctly |

**Parameter Translation Tests**

| Test | Description |
|------|-------------|
| `test_messages_parameters_translation` | temperature, top_p, stop_sequences translate correctly |
| `test_messages_max_tokens_translation` | max_tokens sent upstream, length stop_reason maps to max_tokens |

**Content Filter Tests**

| Test | Description |
|------|-------------|
| `test_messages_content_filter_translation` | content_filter finish reason maps correctly |

### test_simulated_stream_adapter.py

This module tests the stream adapter under realistic conditions that are difficult to unit test in isolation, including network-level chunk fragmentation and timing variations.

**Chunk Fragmentation Tests**

Real HTTP responses often arrive split across chunk boundaries. These tests verify the adapter handles fragmentation correctly.

| Test | Description |
|------|-------------|
| `test_stream_adapter_fragmented_sse` | SSE events split across chunk boundaries handled correctly |
| `test_stream_adapter_multiple_events_per_chunk` | Multiple SSE events in single chunk processed correctly |

**Stream End Scenarios**

| Test | Description |
|------|-------------|
| `test_stream_adapter_no_done` | Stream ending without [DONE] sentinel handled correctly |
| `test_stream_with_delay_between_chunks` | Delays between chunks don't break event sequence |

**Tool Call Streaming Tests**

| Test | Description |
|------|-------------|
| `test_stream_adapter_interleaved_text_tools` | Text blocks followed by multiple tool_use blocks stream correctly |
| `test_stream_adapter_tool_arguments_streaming` | Tool argument JSON streamed across chunks reconstructed correctly |

**Stop Reason Mapping Tests**

| Test | Description |
|------|-------------|
| `test_stream_adapter_stop_reason_mapping` | Verifies stop/length/tool_calls map to end_turn/max_tokens/tool_use |

**Usage Token Streaming Tests**

| Test | Description |
|------|-------------|
| `test_stream_adapter_usage_tokens` | Usage tokens included in message_start and message_delta events |

**Empty Response Tests**

| Test | Description |
|------|-------------|
| `test_stream_adapter_empty_content` | Streaming response with empty content still produces valid event sequence |

### test_simulated_translator.py

This module tests translator edge cases through the full proxy stack, where interactions between components more closely resemble production behavior.

**Image Content Block Tests**

| Test | Description |
|------|-------------|
| `test_translator_image_base64_url` | Base64 image URL translates to OpenAI image_url format |
| `test_translator_image_url` | URL-based image reference passes through correctly |

**Multi-turn Conversation Tests**

| Test | Description |
|------|-------------|
| `test_translator_multi_turn_conversation` | Multiple conversation turns preserve correct role sequence |
| `test_translator_multi_turn_with_tool_use` | Tool use and results in multi-turn conversation translate correctly |

**Content Block Simplification Tests**

OpenAI expects single text content as a string, while Anthropic uses arrays. These tests verify correct simplification behavior.

| Test | Description |
|------|-------------|
| `test_translator_single_text_simplifies` | Single text content block becomes string for upstream |
| `test_translator_multiple_text_blocks_preserved` | Multiple text blocks stay as array |

**Tool Choice Translation Tests**

| Test | Description |
|------|-------------|
| `test_translator_tool_choice_auto` | tool_choice auto translates correctly |
| `test_translator_tool_choice_any_to_required` | Anthropic "any" maps to OpenAI "required" |
| `test_translator_tool_choice_specific_tool` | Tool choice with specific tool name formats correctly |

**Error Response Translation Tests**

| Test | Description |
|------|-------------|
| `test_translator_openai_error_to_anthropic` | OpenAI error format translated to appropriate response |
| `test_translator_rate_limit_error` | 429 rate limit errors handled correctly |

**Edge Case Tests**

| Test | Description |
|------|-------------|
| `test_translator_empty_assistant_content` | Assistant message with empty content handled gracefully |
| `test_translator_tool_result_with_array_content` | tool_result with array content blocks processed correctly |
| `test_translator_tool_result_error_flag` | tool_result is_error flag handled correctly |

**Metadata Translation Tests**

| Test | Description |
|------|-------------|
| `test_translator_metadata_user_id` | metadata.user_id maps to user parameter for upstream |

### test_simulated_concurrency.py

This module tests concurrency behavior under realistic load conditions with 50-100 concurrent requests to catch race conditions and verify slot management. These tests are essential for ensuring the proxy handles production traffic patterns correctly.

**Load Test Parameters**

- `CONCURRENT_REQUESTS`: 50 concurrent requests for standard tests
- `STRESS_REQUESTS`: 100 requests for stress tests

**Concurrent Request Limit Tests**

| Test | Description |
|------|-------------|
| `test_concurrent_requests_respect_limit` | 50+ requests honor per-key concurrency limit without exceeding it |
| `test_concurrent_requests_queue_fifo` | Queued requests processed in FIFO order within same priority |

**Mixed Workload Tests**

| Test | Description |
|------|-------------|
| `test_concurrent_mixed_stream_nonstream` | Mix of streaming and non-streaming requests handled correctly |
| `test_concurrent_mixed_priorities` | High priority requests jump ahead of queued low priority requests |

**Disconnect Handling Tests**

These tests verify that slots are released when clients disconnect at various points in the request lifecycle.

| Test | Description |
|------|-------------|
| `test_concurrent_client_disconnect_while_queued` | Slot released when client disconnects while waiting in queue |
| `test_concurrent_client_disconnect_during_stream` | Slot released when client disconnects during active streaming |

**Metrics Accuracy Tests**

| Test | Description |
|------|-------------|
| `test_concurrent_metrics_accuracy` | Metrics accurately reflect zero active requests after completion |

**Slot Leak Prevention Tests**

Slot leaks are a critical bug class where a request completes but its slot is never released, eventually causing the proxy to stop accepting requests. These tests systematically verify no leaks occur.

| Test | Description |
|------|-------------|
| `test_concurrent_no_slot_leak_on_timeout` | No leaks when some requests encounter timeouts |
| `test_concurrent_no_slot_leak_on_backend_errors` | No leaks when backend returns 500 errors |
| `test_concurrent_no_slot_leak_mixed_scenarios` | Comprehensive test across 100 requests with mixed success/error/streaming scenarios |

## Test Infrastructure

### FakeUpstream

The `FakeUpstream` class simulates an upstream API server for testing. It supports:

- Enqueueing responses (success or error)
- Configuring streaming responses with custom chunk delays
- Recording received requests for verification
- Simulating chunk fragmentation and timing variations

### ProxyHarness

The `ProxyHarness` class creates a complete test proxy instance with:

- Configurable endpoints (messages, chat, embeddings)
- Optional module enablement (concurrency, rate limiting)
- Async client for making test requests
- Concurrency state inspection

### Assertion Helpers

Key assertions used across tests:

- `assert_anthropic_message_valid(payload)`: Validates response structure matches Anthropic API format
- `assert_no_slot_leak(harness, timeout)`: Waits until all concurrency slots are released
- `build_anthropic_request(...)`: Creates valid Anthropic-format request payloads

## Writing New Simulation Tests

When adding new simulation tests, follow these patterns:

1. **Use the harness pattern**: Wrap tests in a `with ProxyHarness(...)` context manager
2. **Clean up state**: Use autouse fixtures to reset concurrency state and transports
3. **Verify upstream received**: Check what the fake upstream recorded, not just the response
4. **Test slot release**: For any test involving the messages endpoint, consider adding slot leak verification

Example test structure:

```python
@pytest.mark.asyncio
async def test_new_scenario() -> None:
    upstream = FakeUpstream()
    upstream.enqueue_openai_chat_response("Expected response")

    base_url = "http://upstream.local/v1"
    register_fake_upstream("upstream.local", upstream)

    config = build_messages_config(base_url)
    with ProxyHarness(config, enable_messages_endpoint=True) as harness:
        async with harness.make_async_client() as client:
            response = await client.post(
                "/v1/messages",
                json=build_anthropic_request(
                    messages=[{"role": "user", "content": "Test"}],
                    max_tokens=100,
                ),
            )

    assert response.status_code == 200
    # Additional assertions...
```
