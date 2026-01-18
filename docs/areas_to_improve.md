# Areas To Improve - yaLLMproxy

**Last updated:** 2026-01-17

## API coverage

1. **Add `/v1/messages` (Anthropic) support and translation.**
   - Implement route + translation layer to/from OpenAI chat or Responses.
   - Map Anthropic content blocks (`text`, `thinking`, `tool_use`, `tool_result`, `image`) to OpenAI/Responses equivalents.
   - Handle streaming event types (`message_start`, `content_block_delta`, `message_delta`) with proper SSE conversion.

2. **Expand Responses API translation coverage.**
   - Map `text`, `reasoning`, `truncation`, `service_tier`, `background`, and `metadata` fields to upstream providers when possible.
   - Add support for `parallel_tool_calls` and `max_tool_calls` in chat requests (provider-specific).
   - Support `response_format`/JSON-schema style output where supported.

## Streaming & translation fidelity

3. **Make the Responses stream adapter choice-aware.**
   - Maintain per-choice state (`accumulated_text`, tool calls, output indices) so `n > 1` can be supported.
   - Emit proper `response.failed`/`response.incomplete` events when upstream ends unexpectedly or with errors.

4. **Improve multimodal translation in `chat_completion_to_response`.**
   - Convert OpenAI content-part arrays into `output_text` and other content parts rather than storing raw lists.
   - Add support for audio and image outputs when present.

5. **Preserve more content types in history reconstruction.**
   - When rebuilding messages from stored response items, include `refusal`, `summary_text`, and `reasoning_text` items, or provide a configurable filtering policy.

## Storage & durability

6. **Implement ResponseStateStore database persistence.**
   - Wire `ResponseStateStore` to `src/database/models/response_state.py`.
   - Add migration + TTL cleanup (use `expires_at`) to avoid unbounded storage growth.
   - Allow configuring max in-memory entries and persistence behavior via `configs/config.yaml`.

7. **Support `store=true` for pass-through Responses backends.**
   - Intercept the upstream response (including streaming) to store it for `previous_response_id`.
   - Consider a config flag to disable interception for performance-sensitive deployments.

## Testing & validation

8. **Add focused tests for translation and streaming.**
   - Unit tests for Responses <-> Chat translation (tool calls, multimodal parts, refusals).
   - Streaming adapter tests for tool-call-only streams and multi-choice outputs.
   - Regression tests around `previous_response_id` replay.
