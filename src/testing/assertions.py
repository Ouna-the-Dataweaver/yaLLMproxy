"""Assertion utilities for simulation tests.

These utilities provide validation and comparison functions for API
responses, making tests more readable and catching structural issues early.
"""

from __future__ import annotations

import asyncio
from typing import TYPE_CHECKING, Any

if TYPE_CHECKING:
    from .proxy_harness import ProxyHarness


def assert_anthropic_message_valid(response: dict[str, Any]) -> None:
    """Validate that a response has valid Anthropic message structure.

    Args:
        response: Response dict to validate

    Raises:
        AssertionError: If structure is invalid
    """
    assert "type" in response, "Missing 'type' field"
    assert response["type"] == "message", f"Expected type='message', got '{response['type']}'"

    assert "id" in response, "Missing 'id' field"
    assert response["id"].startswith("msg_"), f"ID should start with 'msg_': {response['id']}"

    assert "role" in response, "Missing 'role' field"
    assert response["role"] == "assistant", f"Expected role='assistant', got '{response['role']}'"

    assert "content" in response, "Missing 'content' field"
    assert isinstance(response["content"], list), "Content should be a list"

    for i, block in enumerate(response["content"]):
        assert "type" in block, f"Content block {i} missing 'type'"
        block_type = block["type"]
        if block_type == "text":
            assert "text" in block, f"Text block {i} missing 'text'"
        elif block_type == "tool_use":
            assert "id" in block, f"Tool use block {i} missing 'id'"
            assert "name" in block, f"Tool use block {i} missing 'name'"
            assert "input" in block, f"Tool use block {i} missing 'input'"

    assert "stop_reason" in response, "Missing 'stop_reason' field"
    assert "usage" in response, "Missing 'usage' field"

    usage = response["usage"]
    assert "input_tokens" in usage, "Usage missing 'input_tokens'"
    assert "output_tokens" in usage, "Usage missing 'output_tokens'"


def assert_openai_chat_valid(response: dict[str, Any]) -> None:
    """Validate that a response has valid OpenAI chat completion structure.

    Args:
        response: Response dict to validate

    Raises:
        AssertionError: If structure is invalid
    """
    assert "id" in response, "Missing 'id' field"
    assert "object" in response, "Missing 'object' field"
    assert response["object"] == "chat.completion", f"Expected object='chat.completion', got '{response['object']}'"

    assert "choices" in response, "Missing 'choices' field"
    assert isinstance(response["choices"], list), "Choices should be a list"
    assert len(response["choices"]) > 0, "Choices list is empty"

    for i, choice in enumerate(response["choices"]):
        assert "index" in choice, f"Choice {i} missing 'index'"
        assert "message" in choice, f"Choice {i} missing 'message'"
        assert "finish_reason" in choice, f"Choice {i} missing 'finish_reason'"

        message = choice["message"]
        assert "role" in message, f"Choice {i} message missing 'role'"
        assert message["role"] == "assistant", f"Choice {i} expected role='assistant'"

        if "tool_calls" in message:
            for j, tc in enumerate(message["tool_calls"]):
                assert "id" in tc, f"Choice {i} tool_call {j} missing 'id'"
                assert "type" in tc, f"Choice {i} tool_call {j} missing 'type'"
                assert "function" in tc, f"Choice {i} tool_call {j} missing 'function'"
                assert "name" in tc["function"], f"Choice {i} tool_call {j} function missing 'name'"
                assert "arguments" in tc["function"], f"Choice {i} tool_call {j} function missing 'arguments'"


def assert_sse_event_sequence_valid(
    events: list[dict[str, Any]],
    expected_types: list[str] | None = None,
) -> None:
    """Validate SSE event sequence structure.

    Args:
        events: List of parsed SSE event dicts
        expected_types: Optional list of expected event types in order

    Raises:
        AssertionError: If structure is invalid
    """
    assert len(events) > 0, "Events list is empty"

    for i, event in enumerate(events):
        assert isinstance(event, dict), f"Event {i} should be a dict"
        assert "type" in event, f"Event {i} missing 'type' field"

    if expected_types:
        actual_types = [e["type"] for e in events]
        assert actual_types == expected_types, (
            f"Event type sequence mismatch.\n"
            f"Expected: {expected_types}\n"
            f"Actual: {actual_types}"
        )


def assert_anthropic_sse_valid(events: list[dict[str, Any]]) -> None:
    """Validate Anthropic SSE event sequence.

    Args:
        events: List of parsed SSE event dicts

    Raises:
        AssertionError: If structure is invalid
    """
    assert len(events) > 0, "Events list is empty"

    # Must start with message_start
    assert events[0]["type"] == "message_start", "First event should be message_start"
    assert "message" in events[0], "message_start missing 'message'"

    # Must end with message_stop
    assert events[-1]["type"] == "message_stop", "Last event should be message_stop"

    # Check for message_delta before message_stop
    has_message_delta = any(e["type"] == "message_delta" for e in events)
    assert has_message_delta, "Missing message_delta event"

    # Validate content block sequence
    open_blocks: set[int] = set()
    for event in events:
        event_type = event.get("type")

        if event_type == "content_block_start":
            index = event.get("index")
            assert index is not None, "content_block_start missing 'index'"
            assert index not in open_blocks, f"content_block {index} started twice"
            open_blocks.add(index)

        elif event_type == "content_block_delta":
            index = event.get("index")
            assert index is not None, "content_block_delta missing 'index'"
            assert index in open_blocks, f"content_block_delta for closed block {index}"

        elif event_type == "content_block_stop":
            index = event.get("index")
            assert index is not None, "content_block_stop missing 'index'"
            assert index in open_blocks, f"content_block_stop for never-opened block {index}"
            open_blocks.discard(index)


async def assert_slot_released(
    harness: "ProxyHarness",
    key: str,
    timeout: float = 1.0,
) -> None:
    """Assert that a concurrency slot is released within timeout.

    Args:
        harness: ProxyHarness instance
        key: Key identifier to check
        timeout: Maximum time to wait in seconds

    Raises:
        AssertionError: If slot not released within timeout
    """
    start = asyncio.get_event_loop().time()
    deadline = start + timeout

    while asyncio.get_event_loop().time() < deadline:
        metrics = await harness.get_concurrency_metrics()
        active = metrics.active_requests_by_key.get(key, 0)
        if active == 0:
            return
        await asyncio.sleep(0.05)

    metrics = await harness.get_concurrency_metrics()
    active = metrics.active_requests_by_key.get(key, 0)
    raise AssertionError(f"Slot not released within {timeout}s. Active count for '{key}': {active}")


async def assert_no_slot_leak(
    harness: "ProxyHarness",
    timeout: float = 2.0,
) -> None:
    """Assert that no concurrency slots are leaked (all released).

    Args:
        harness: ProxyHarness instance
        timeout: Maximum time to wait

    Raises:
        AssertionError: If any slots still held
    """
    start = asyncio.get_event_loop().time()
    deadline = start + timeout

    while asyncio.get_event_loop().time() < deadline:
        metrics = await harness.get_concurrency_metrics()
        total_active = sum(metrics.active_requests_by_key.values())
        if total_active == 0:
            return
        await asyncio.sleep(0.05)

    metrics = await harness.get_concurrency_metrics()
    raise AssertionError(
        f"Slots leaked! Active requests by key: {metrics.active_requests_by_key}"
    )


def assert_messages_equal(
    actual: dict[str, Any],
    expected: dict[str, Any],
    *,
    ignore_ids: bool = True,
    ignore_usage: bool = False,
) -> None:
    """Assert two Anthropic messages are semantically equal.

    Args:
        actual: Actual message dict
        expected: Expected message dict
        ignore_ids: Ignore ID field differences
        ignore_usage: Ignore usage field differences

    Raises:
        AssertionError: If messages differ
    """
    def normalize(msg: dict[str, Any]) -> dict[str, Any]:
        result = dict(msg)
        if ignore_ids:
            result.pop("id", None)
            if "content" in result:
                result["content"] = [
                    {k: v for k, v in block.items() if k != "id"}
                    for block in result["content"]
                ]
        if ignore_usage:
            result.pop("usage", None)
        return result

    actual_norm = normalize(actual)
    expected_norm = normalize(expected)

    # Compare content blocks carefully
    if "content" in actual_norm and "content" in expected_norm:
        assert len(actual_norm["content"]) == len(expected_norm["content"]), (
            f"Content block count mismatch: {len(actual_norm['content'])} vs {len(expected_norm['content'])}"
        )

        for i, (a_block, e_block) in enumerate(
            zip(actual_norm["content"], expected_norm["content"])
        ):
            assert a_block.get("type") == e_block.get("type"), (
                f"Block {i} type mismatch: {a_block.get('type')} vs {e_block.get('type')}"
            )

            if a_block.get("type") == "text":
                assert a_block.get("text") == e_block.get("text"), (
                    f"Block {i} text mismatch:\n"
                    f"Actual: {a_block.get('text')}\n"
                    f"Expected: {e_block.get('text')}"
                )
            elif a_block.get("type") == "tool_use":
                assert a_block.get("name") == e_block.get("name"), (
                    f"Block {i} tool name mismatch"
                )
                assert a_block.get("input") == e_block.get("input"), (
                    f"Block {i} tool input mismatch"
                )

    # Compare other fields
    for key in ["role", "stop_reason", "stop_sequence", "model"]:
        if key in expected_norm:
            assert actual_norm.get(key) == expected_norm.get(key), (
                f"Field '{key}' mismatch: {actual_norm.get(key)} vs {expected_norm.get(key)}"
            )


def assert_response_content_equals(
    response: dict[str, Any],
    expected_text: str,
    *,
    response_format: str = "anthropic",
) -> None:
    """Assert that response content matches expected text.

    Args:
        response: API response dict
        expected_text: Expected text content
        response_format: "anthropic" or "openai"

    Raises:
        AssertionError: If content doesn't match
    """
    if response_format == "anthropic":
        content = response.get("content", [])
        text_parts = [
            block.get("text", "")
            for block in content
            if block.get("type") == "text"
        ]
        actual = "".join(text_parts)
    else:
        choices = response.get("choices", [])
        if choices:
            actual = choices[0].get("message", {}).get("content", "")
        else:
            actual = ""

    assert actual == expected_text, (
        f"Content mismatch.\nExpected: {expected_text}\nActual: {actual}"
    )


# =============================================================================
# Responses API Assertions
# =============================================================================


def assert_responses_api_valid(response: dict[str, Any]) -> None:
    """Validate that a response has valid Responses API structure.

    Args:
        response: Response dict to validate

    Raises:
        AssertionError: If structure is invalid
    """
    assert "id" in response, "Missing 'id' field"
    assert response["id"].startswith("resp_"), f"ID should start with 'resp_': {response['id']}"

    assert "object" in response, "Missing 'object' field"
    assert response["object"] == "response", f"Expected object='response', got '{response['object']}'"

    assert "status" in response, "Missing 'status' field"
    valid_statuses = {"in_progress", "completed", "incomplete", "failed"}
    assert response["status"] in valid_statuses, (
        f"Invalid status '{response['status']}', expected one of {valid_statuses}"
    )

    assert "output" in response, "Missing 'output' field"
    assert isinstance(response["output"], list), "Output should be a list"

    # Validate each output item
    for i, item in enumerate(response["output"]):
        assert "type" in item, f"Output item {i} missing 'type'"
        item_type = item["type"]
        if item_type == "message":
            assert "role" in item, f"Message item {i} missing 'role'"
            assert "content" in item, f"Message item {i} missing 'content'"
            assert item["role"] == "assistant", f"Message item {i} should have role='assistant'"
        elif item_type == "function_call":
            assert "call_id" in item, f"Function call item {i} missing 'call_id'"
            assert "name" in item, f"Function call item {i} missing 'name'"
            assert "arguments" in item, f"Function call item {i} missing 'arguments'"

    # Validate usage if present, non-empty, and status is completed
    if response["status"] == "completed" and response.get("usage"):
        usage = response["usage"]
        if usage:  # Only validate if usage is non-empty dict
            # Accept either OpenAI format (prompt_tokens) or Responses format (input_tokens)
            has_input = "input_tokens" in usage or "prompt_tokens" in usage
            has_output = "output_tokens" in usage or "completion_tokens" in usage
            if has_input or has_output:  # Only check if some tokens are present
                assert has_input, f"Usage missing input tokens: {usage}"
                assert has_output, f"Usage missing output tokens: {usage}"


def assert_responses_sse_valid(events: list[dict[str, Any]]) -> None:
    """Validate Responses API SSE event sequence.

    Args:
        events: List of parsed SSE event dicts

    Raises:
        AssertionError: If structure is invalid
    """
    assert len(events) > 0, "Events list is empty"

    # Check required initial events
    assert events[0]["type"] == "response.created", (
        f"First event should be response.created, got '{events[0].get('type')}'"
    )

    # Must have in_progress after created
    event_types = [e.get("type") for e in events]
    assert "response.in_progress" in event_types, "Missing response.in_progress event"

    # Must end with a terminal event
    terminal_types = {"response.completed", "response.failed", "response.incomplete"}
    assert events[-1]["type"] in terminal_types, (
        f"Last event should be terminal (completed/failed/incomplete), got '{events[-1].get('type')}'"
    )

    # Check sequence numbers are monotonically increasing
    sequence_numbers = [e.get("sequence_number") for e in events if "sequence_number" in e]
    if sequence_numbers:
        for i in range(1, len(sequence_numbers)):
            assert sequence_numbers[i] > sequence_numbers[i - 1], (
                f"Sequence numbers not monotonic at index {i}: "
                f"{sequence_numbers[i - 1]} -> {sequence_numbers[i]}"
            )


def assert_responses_output_text_equals(
    response: dict[str, Any],
    expected_text: str,
) -> None:
    """Assert that response output_text matches expected.

    Args:
        response: Responses API response dict
        expected_text: Expected output text

    Raises:
        AssertionError: If text doesn't match
    """
    output_text = response.get("output_text", "")
    assert output_text == expected_text, (
        f"Output text mismatch.\nExpected: {expected_text}\nActual: {output_text}"
    )


def assert_responses_has_tool_calls(
    response: dict[str, Any],
    expected_count: int | None = None,
    expected_names: list[str] | None = None,
) -> None:
    """Assert that response has expected tool calls.

    Args:
        response: Responses API response dict
        expected_count: Expected number of function_call items
        expected_names: Expected function names

    Raises:
        AssertionError: If tool calls don't match expectations
    """
    output = response.get("output", [])
    function_calls = [item for item in output if item.get("type") == "function_call"]

    if expected_count is not None:
        assert len(function_calls) == expected_count, (
            f"Expected {expected_count} tool calls, got {len(function_calls)}"
        )

    if expected_names is not None:
        actual_names = [fc.get("name") for fc in function_calls]
        assert actual_names == expected_names, (
            f"Tool call names mismatch.\nExpected: {expected_names}\nActual: {actual_names}"
        )
