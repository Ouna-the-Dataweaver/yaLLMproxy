# Areas To Improve - yaLLMproxy

**Last updated:** 2026-01-23

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

## Observability & debugging

9. **Add request correlation/tracing IDs.**
   - Generate a unique request ID (UUID4) at the start of each request in middleware.
   - Accept incoming `X-Request-ID` header from clients; if present, use it (allows end-to-end tracing across services).
   - Pass the ID to backends via `X-Request-ID` header (configurable header name).
   - Include the ID in all log entries (`RequestLogRecorder`, file logs, database logs).
   - Return the ID in response headers so clients can reference it in support requests.
   - Implementation locations:
     - Add middleware in `src/middleware/` to extract/generate ID and attach to request state.
     - Update `RequestLogRecorder.log_request()` and `log_response()` to include `request_id` field.
     - Update `src/database/models/request_log.py` to add `request_id` column (indexed).
     - Update `src/logging/setup.py` to include request ID in log format.

10. **Track retry and fallback metrics.**
    - Record per-backend statistics in a sliding time window (e.g., last 5 minutes):
      - Total requests attempted
      - Successful responses (2xx)
      - Retryable failures (429, 5xx, timeouts)
      - Non-retryable failures (4xx client errors)
      - Retry attempts triggered
      - Fallback triggers (when primary failed and secondary was used)
    - Expose via `/admin/metrics` endpoint:
      ```json
      {
        "backends": {
          "openai-gpt4": {
            "requests": 1250,
            "success_rate": 0.94,
            "retry_rate": 0.08,
            "avg_latency_ms": 1823,
            "p99_latency_ms": 4521
          }
        },
        "fallbacks": {
          "gpt-4 -> gpt-4-backup": { "triggers": 23, "success_rate": 0.87 }
        }
      }
      ```
    - Implementation:
      - Add `src/metrics/backend_stats.py` with `BackendStatsCollector` class.
      - Use a ring buffer or time-bucketed counters for efficient sliding window.
      - Hook into `ProxyRouter._try_backend()` to record outcomes.
      - Add cleanup task to prune old buckets periodically.

11. **Expose concurrency queue wait time metrics.**
    - Track queue wait times per API key and per model:
      - Count of requests that waited in queue
      - p50, p90, p95, p99 wait times
      - Max wait time observed
      - Queue timeout count (requests that exceeded `queue_timeout`)
    - Add to existing `/usage` or new `/admin/concurrency-stats` endpoint:
      ```json
      {
        "by_key": {
          "key-abc123": {
            "queued_requests": 145,
            "wait_time_p50_ms": 120,
            "wait_time_p99_ms": 2450,
            "timeouts": 3
          }
        },
        "by_model": {
          "gpt-4": { "queued_requests": 89, "wait_time_p50_ms": 95 }
        }
      }
      ```
    - Implementation:
      - Update `ConcurrencyManager.acquire_slot()` to record wait start/end times.
      - Add `src/metrics/concurrency_stats.py` with percentile calculation (use t-digest or simple sorted list for small windows).
      - Expose via route in `src/api/routes/usage.py` or new admin route.

## Intelligent routing

12. **Implement backend health scoring.**
    - Maintain a health score (0.0–1.0) per backend based on recent success/failure rates.
    - Score calculation (configurable weights):
      ```
      health = (success_weight * success_rate)
             - (error_weight * error_rate)
             - (timeout_weight * timeout_rate)
             - (latency_weight * normalized_latency)
      ```
    - Decay old observations using exponential moving average or time-bucketed windows.
    - Use health scores to influence routing decisions:
      - **Option A (soft):** Order fallback chain by health score instead of static config order.
      - **Option B (hard):** Skip backends below a health threshold (e.g., < 0.3) unless no alternatives.
      - **Option C (weighted):** Probabilistic routing weighted by health scores (load balancing with health awareness).
    - Circuit breaker integration:
      - If a backend's health drops below threshold, mark it as "unhealthy" for a cooldown period.
      - Periodically send probe requests to check recovery.
      - Gradually restore traffic after successful probes.
    - Implementation:
      - Add `src/core/health.py` with `BackendHealthTracker` class.
      - Integrate with `ProxyRouter._build_route()` to sort/filter backends.
      - Add config options in `router_settings`:
        ```yaml
        router_settings:
          health_scoring:
            enabled: true
            window_seconds: 300
            unhealthy_threshold: 0.3
            cooldown_seconds: 60
            probe_interval_seconds: 15
        ```

13. **Add conditional/content-based routing.**
    - Route requests to specific backends based on request properties:
      - **Context length:** Route long-context requests (> N tokens) to backends with larger context windows.
      - **Tool usage:** Route requests with `tools` array to backends known to handle tools well.
      - **Model features:** Route `response_format: json_schema` to backends that support structured output.
      - **Content type:** Route multimodal requests (images, audio) to capable backends.
      - **Priority/tier:** Route high-priority keys to dedicated backends.
    - Configuration via routing rules:
      ```yaml
      router_settings:
        routing_rules:
          - name: "long-context-to-128k"
            condition:
              type: "context_length"
              min_tokens: 32000
            route_to: "gpt-4-128k"

          - name: "tools-to-specialized"
            condition:
              type: "has_tools"
            route_to: "gpt-4-tools"

          - name: "images-to-vision"
            condition:
              type: "has_image_content"
            route_to: "gpt-4-vision"
      ```
    - Implementation:
      - Add `src/core/routing_rules.py` with `RoutingRule` and `RuleEvaluator` classes.
      - Token counting: Use tiktoken or simple heuristic (chars/4) for fast estimation.
      - Evaluate rules in `ProxyRouter.route_request()` before `_build_route()`.
      - Rules can override or prepend to the normal fallback chain.

## Stateful context management

14. **Implement reasoning preservation for stateful chat.**
    - Problem: Some clients strip reasoning/thinking content from responses before sending follow-up requests, but some LLMs (e.g., Claude with extended thinking) require reasoning to remain in context for coherent multi-turn conversations.
    - Solution: Proxy-side conversation state that can restore reasoning into requests.
    - Design options:

      **Option A: Transparent restoration (recommended)**
      - Store conversation state keyed by a session ID (from header like `X-Session-ID` or derived from API key + conversation hash).
      - On each response, extract and store reasoning content (from `reasoning_content`, thinking blocks, or parsed tags).
      - On subsequent requests, detect if reasoning is missing from the message history and inject it back.
      - Configurable per-model behavior:
        ```yaml
        model_params:
          reasoning_preservation:
            enabled: true
            storage: "memory"  # or "database"
            ttl_seconds: 3600
            inject_mode: "restore_missing"  # or "always_include"
        ```

      **Option B: Explicit state management**
      - Client sends `X-Conversation-ID` header.
      - Proxy maintains full conversation history server-side.
      - Client can send minimal requests; proxy reconstructs full context.
      - More storage-intensive but gives full control.

    - Key implementation challenges:
      - **Message matching:** Identify which stored reasoning corresponds to which assistant message (use content hash or index).
      - **Content format:** Handle different reasoning formats (OpenAI `reasoning_content`, Anthropic thinking blocks, custom tags).
      - **Storage limits:** Cap stored conversations and implement LRU eviction.
      - **Streaming:** Capture reasoning from streaming responses (already done in parsers).

    - Implementation locations:
      - Add `src/state/conversation_store.py` with `ConversationStateStore` class.
      - Add `src/state/reasoning_injector.py` to handle restoration logic.
      - Hook into `chat.py` request handling to check/restore reasoning.
      - Add database model if persistence needed: `src/database/models/conversation_state.py`.

    - Testing considerations:
      - Test with clients that strip reasoning (verify restoration works).
      - Test with clients that preserve reasoning (verify no duplication).
      - Test TTL expiration and LRU eviction.
      - Test across different reasoning formats (OpenAI, Anthropic, parsed tags).

15. **Add conversation replay/debugging endpoint.**
    - Expose `/admin/conversations/{session_id}` to view stored conversation state.
    - Show original messages, stored reasoning, and what would be injected.
    - Useful for debugging why a conversation went wrong.
    - Allow manual clearing of conversation state.


## Other

## Frontend enhancements:

16. **App Keys Management Page (HIGH PRIORITY)**
   - You have complete backend API endpoints for app key management (/admin/keys/*) but no dedicated UI. A page would include:
     - List all app keys with metadata (name, enabled status, creation date, usage)
     - Create new keys with custom settings (rate limits, concurrency limits, priority)
     - Edit key properties
     - Delete/revoke keys with confirmation
     - Regenerate key secrets
     - View key usage statistics