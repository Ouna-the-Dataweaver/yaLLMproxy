# Go Refactor State

Latest working-tree verification: 2026-09-16 (simple routing rework). `task test`,
`task test:race`, static analysis, `task frontend:build`, `task smoke:fake`, and the
headless Chrome provider/node workflow passed.

Previous release-readiness audit: 2026-07-10, based on `codex/go_refactor` at `50ad609`.

Batch 28 was the last completed feature pass and was integrated on 2026-06-29. The latest integrated Go follow-up hardened admin-password handling and the React admin workflow on 2026-07-07. The newer `codex/gigachat-ca-cert-followup` branch is based on the historical Python line; its custom GigaChat `ca_cert` support is not part of this Go branch.

## Simple routing rework (2026-09-16 working tree)

New setups can use `configs/config_simple_example.yaml` and `docs/simple-routing.md`.
Providers select models and derive runtime nodes; unwired nodes form public model
pools automatically. Existing `model_list` and explicitly wired pools remain
supported. Provider discovery and selection are available in Admin → Models & Nodes.
The overview shows the effective pools; explicit wiring and model fallbacks are
under expandable advanced sections.

Routing now retains eligible siblings for failover before different-model fallbacks.
Conversation affinity precedes prompt-prefix affinity, which is enabled when its
setting is omitted; explicit `false` remains respected. Successful failover updates
conversation affinity. Compatible native Messages/Responses replicas retain their
native endpoint. Provider timeout and adapter override inheritance were corrected.
Selected-provider aliases share RPS/RPM/concurrency allowance per upstream model;
existing token quotas remain per backend.

Validation covers expansion/persistence, provider selection lifecycle, model-name
collisions, alias rate limits, cache-aware failover, and native/stream behavior.
The embedded frontend has been rebuilt and the provider discovery → bulk selection
→ direct-node addition → inference workflow was exercised in headless Chrome with
fake upstreams. No real-provider configuration was migrated by this pass.

## Implemented

- Go test suite and Go module.
- Config loading with environment substitution and legacy `model_list` inheritance.
- Compatibility with existing `model_list` configs plus first-pass Provider/Node/Model config shapes.
- OpenAI-compatible chat, embeddings, rerank, and model-list endpoints.
- First-pass Anthropic Messages endpoint support, including non-stream and streaming OpenAI-compatible translation plus direct `api_type: anthropic` backend pass-through, enabled by `proxy_settings.enable_messages_endpoint`.
- Anthropic Messages translator parity for missing tool schemas, empty system prompts, unknown text-like content block fallback, multimodal image blocks in both translation directions, thinking/redacted-thinking blocks, reasoning signatures, required/any/specific-tool `tool_choice` normalization, cache-control block tolerance, cached-input usage fields, and forward-compatible string `tool_choice` pass-through.
- First-pass Responses API endpoint support, including non-stream and streaming OpenAI-compatible translation, direct `supports_responses_api` pass-through, and in-memory plus SQLite-backed durable `store` / `previous_response_id` replay, enabled by `proxy_settings.enable_responses_endpoint`.
- Responses API follow-up requests bind generated response IDs to the selected backend node, so `previous_response_id` follow-ups prefer the same node for cache locality even when the request is translated to Chat Completions upstream.
- Responses API translated non-stream and stream usage details map Chat Completions cached input token details and reasoning output token details into Responses usage.
- Responses API translated non-stream and stream outputs map OpenAI-compatible `reasoning_content` into Responses reasoning output items and reasoning-summary stream events while preserving visible `output_text`.
- Responses API translated non-stream terminal statuses map Chat Completions `finish_reason: "length"` to `status: "incomplete"` with `incomplete_details`, map `content_filter` to `failed`, and prefer `length` when both signals are present.
- Responses API translated requests pass through `service_tier` and `prompt_cache_key`, normalize `tool_choice` required/any/specific-tool forms for Chat Completions upstreams, preserve image input content through the existing `image_url` strategy, and return safe OpenAI-style error objects with `type`, `code`, `param`, and `message`.
- Responses/Messages translator regression tests cover refusal and URL/file citation plus file-path annotation content, multimodal image content, empty content, streamed incomplete details, malformed or unknown SSE payloads, Anthropic content block start/stop ordering, interleaved text/tool/text streams, `tool_choice` enforcement forms, and `message_delta` stop-reason/cache-usage mapping.
- Admin model registration with full candidate validation, transactional runtime replacement, crash-safe YAML persistence when loaded from disk, fallback updates, and protected-model password checks.
- Transparent backend forwarding with header filtering, upstream API key injection, model rewrite, forced/default parameters, retryable-status fallback, and streaming passthrough.
- Passthrough routing via `/p1/{model_name}/*`, preserving arbitrary payloads while enforcing enabled app-key/model/required-header policy, client/backend admission, upstream timeouts, and client-credential stripping.
- Response module pipeline for non-stream and SSE event transforms:
  - MiniMax-style `reasoning_details` normalization
  - `<think>` extraction
  - XML-ish tool-call extraction
  - stateful streamed `<think>` and XML-ish `<tool_call>` parsing across fragmented SSE events
  - template-derived custom think tags, XML tool tags, and K2 JSON-style tool call markers
  - template-derived `qwen_xml` tool-parser detection from marker-based Qwen-style templates
  - bounded Jinja-compatible template inspection for admin/template-derived parser config, including rendered/raw evidence and warnings
  - additive bounded runtime Jinja-compatible rendering for common HuggingFace chat-template preview constructs, exposed only as an opt-in admin inspect preview over synthetic sample messages/tools
  - Qwen XML tool-call repair, tool-buffer fallback, and incremental streamed `function.arguments` deltas
  - stream parser buffer caps via `max_stream_buffer_bytes`, flushing malformed over-limit buffers to literal content/reasoning
  - stream parser max-age bounds via `max_stream_buffer_age_seconds`, flushing over-age parser buffers as literal content/reasoning and over-age JSON repair buffers as unchanged arguments
  - explicit reset/close lifecycle hooks for per-stream parser state
  - opt-in streamed tool/function argument JSON repair with bounded buffering and passthrough-on-uncertainty fallback
  - non-stream parsed tool calls set `finish_reason: "tool_calls"` for OpenAI-compatible parity
  - Qwen JSON-in-`<tool_call>` tool-call blocks parse in non-stream and fragmented stream responses
  - reasoning/content swap
- Admission manager with concurrency limits, queue timeout, per-key-or-group/model scopes, and priority ordering.
- RPS/RPM token-bucket request limits with model defaults and app-key overrides.
- Provider/node backend attempt limits: provider limits are inherited by nodes, node limits override provider limits, and backend RPS/RPM/concurrency are enforced per upstream attempt across Chat Completions, Messages, and Responses.
- Provider/node endpoint routing: provider endpoints are inherited by nodes, node endpoints override provider endpoints, invalid endpoint labels are rejected during config validation, empty endpoint lists allow all endpoints, and unsupported endpoint routes return 400 without calling upstream.
- Token quota limits via `limits.token_quota`, with optional rolling `limits.token_quota_window_seconds` windows or UTC calendar-aligned `limits.token_quota_reset_mode` resets (`daily`, `weekly`, `monthly`, `yearly`), request-side token reservations, non-stream and SSE usage reconciliation, rollback on failed upstream/queue attempts, read-only quota availability probes, `/v1/usage` quota metrics, and SQLite log-backed request quota usage restoration on startup/reload.
- Disabled-by-default quota-aware backend selection via `router_settings.quota_aware_selection.enabled`, using read-only backend token-quota peeks to skip exhausted fallback or weighted node-pool candidates when an alternative has estimated capacity. Successful upstream calls record actual usage to in-memory `backend::<name>` quota scopes; all-exhausted routes fall back to the full candidate set.
- Default-enabled prefix-cache-affinity node scheduling via `router_settings.prefix_cache_affinity.enabled`. The router hashes shallow message prefixes (message 0, messages 0-1, messages 0-2) and records bounded in-memory `(node, prefix-hash)` bindings after successful `200` upstream responses. During node-pool selection, among nodes already eligible by endpoint support, node/app-key `allowed_keys`, health and half-open state, backend-availability, and quota-aware predicates, it prefers the node bound to the longest matching prefix. Sticky routing from conversation/thread/response IDs always takes precedence; when disabled, no prefix matches, or every matching node is ineligible, routing is unchanged. Bindings are bounded by `prefix_cache_affinity.max_entries` (default `4096`, FIFO eviction) and are in-memory only.
- Disabled-by-default concurrency-aware node selection via `router_settings.concurrency_aware_selection.enabled`. The router tracks live upstream attempts per configured backend/node and, for fresh requests only, prefers the eligible node with the fewest in-flight attempts. Sticky routing and prefix-cache affinity keep precedence; endpoint support, app-key/node `allowed_keys`, health and half-open state, backend availability, quota-aware predicates, and fallback structure remain eligibility gates. Ties use existing weighted or round-robin order. `/v1/health` exposes `inflight_requests`, and `/metrics` exposes `yallmp_backend_inflight_requests` with bounded backend labels.
- Policy manager for flat and grouped app keys, per-key/group model access, model-side `allowed_keys` enforcement, node-side `allowed_keys` routing, app-key defaults, unauthenticated request limits, legacy limit aliases, merged group/key limits, and shared group admission/quota scopes.
- Backend health snapshots, retryable-failure tracking, unhealthy cooldown, opt-in half-open recovery with bounded consecutive-success trials, health-aware node-pool routing, optional active backend probes via `router_settings.health_checks`, and score-aware fallback ordering via `router_settings.health_scoring`.
- Read-only model catalog via admin-protected `GET /admin/catalog`, seeded from configured legacy models and provider/node/client-model resources, with disabled-by-default node/provider opt-in `/v1/models` probes, TTL/stale markers, failure backoff, bucketed probe error classes, and bounded advisory warnings derived from the redacted snapshot. Catalog probe results and warnings are informational only and do not mutate routing.
- Deterministic weighted round-robin load balancing from `nodes[].weight` or per-model `node_weights`, with sticky follow-up routing from conversation/thread IDs and stored Responses API response IDs.
- Strict admin router fallback replacement via `GET/PUT /admin/router-settings`, preserving raw router settings, rejecting malformed or unknown fallback sources/targets before apply, and rebuilding runtime routing atomically.
- Disabled-by-default OpenTelemetry request-span, metric, and span-event log groundwork through strict `proxy_settings.otel` config, strict `proxy_settings.otel.logs.enabled` validation, OTLP gRPC trace/metric exporter support, in-memory test provider, generic JSON proxy plus Messages/Responses request/policy/admission/routing/upstream-attempt/response/parser/logging spans where those phases run, direct passthrough request/optional-policy/admission/upstream-attempt/response spans, request count/duration metrics, reconciled GenAI token usage metrics, stream TTFT and aggregate TPOT/ITL metrics, optional sanitized `proxy.log` span events, bounded shutdown, sanitized low-cardinality attributes, bounded GenAI-style system/operation/model attributes, and proxy-owned request-span `trace_id`/`span_id` correlation/navigation in persisted admin logs.
- Disabled-by-default exact inference cache for identical non-stream chat/embeddings and translated non-stream Messages/Responses requests, including TTL/max-age/entry-size bounds, read/write modes, optional disabled-by-default SQLite persistence for client-facing response bytes, a truthy `x-yallmp-cache-bypass` request header that skips cache read/write without leaking upstream, atomic hit/miss/write/eviction counters, and an admin-protected read-only `GET /admin/cache` snapshot. Translated endpoint cache entries store complete 200 client-facing JSON responses and exclude streams, errors, native Anthropic Messages pass-through, and native Responses provider pass-through.
- Opt-in semantic inference cache for non-stream chat completions, configured under `proxy_settings.inference_cache.semantic` and disabled by default. It requires the global inference cache mode plus explicit embedding node/model config, embeds canonical outbound request bodies through that node, stores embeddings plus complete 200 client-facing JSON responses in a bounded in-memory index, filters candidates by backend/client model/app-key/group/required-header scope and non-message request controls before cosine ranking, honors the same bypass header and read/write modes, and exposes counters only in `/admin/cache`.
- Raw admin config workflows via masked `GET /admin/config`, safe `PUT /admin/config` with missing-secret preservation, crash-safe YAML persistence, runtime-serialized router/database/cache/telemetry replacement, and `POST /admin/config/reload`; configured admin passwords protect all config routes.
- Vite + React admin frontend source under `web/admin`, with a structured request inspector for prompts/messages/tools/responses/usage/attempts/warnings/headers, full log filters and pagination, raw/copy/download modes, Trace-to-Request navigation, session-only admin password storage, health/quota/config/model/key workflows, an Observability tab for exact/semantic cache counters/backend health/usage summaries, a read-only Traces tab over sanitized `/admin/traces/{trace_id}` metadata, a read-only Catalog tab for `/admin/catalog` rows and warnings, and Routing UI list/create/delete plus safe provider/node/client-model edits, including endpoints, limits, node modules, allowed-key scopes, client-model `node_weights`, `balancing`, replace-only fallback chains, legacy model copy/delete actions, and legacy model dependency views.
- Deterministic fake-local provider conformance coverage in server tests for chat, Messages, and Responses pass-through and translated paths, including reasoning/thinking translation paths, plus endpoint-denied behavior; fake smoke covers daily quota reset visibility, direct passthrough fidelity, in-memory OTel span/metric emission including parser/logging phases and streaming TTFT/TPOT, embeddings/rerank model rewrite, translated Messages/Responses streaming tool and terminal edge behavior, stream JSON repair, and Qwen-style template inspect reports `tool_parser: qwen_xml`.
- Committed admin production bundle embedded in the Go binary and served from `/admin` without a source-tree/runtime-Node dependency or JSON admin API shadowing.
- Field-level admin config validation via `POST /admin/config/validate`, PUT preflight rejection with structured issue paths, and frontend validation displays for raw config plus model registration fields.
- In-memory request logs, usage/admission metrics endpoint, admin-protected backend quota snapshot, bounded Prometheus backend quota and backend in-flight metrics, and disabled-by-default OTel-independent Prometheus request/backend/stream timing histograms.
- SQLite/PostgreSQL-backed persistent request metadata logging using the top-level `database` config, including non-stream and SSE token usage, canonical non-stream Chat/Messages/Responses stop/tool outcomes, timing, app-key id, proxy-owned OTel trace/span IDs when enabled, full-request sidecar links, filtered/paginated `/admin/logs`, decoded sidecar search, `/admin/logs/{id}` detail lookup, admin-protected metadata-only `/admin/traces/{trace_id}` aggregation, configured-password protection for `/admin/logs` plus `/api/logs`, linked error logs, stop-reason/tool-call/model aggregate endpoints, and request/error metadata cleanup helpers.
- Proxy logs record selected backend names for Chat Completions, Embeddings, Rerank, Messages, and Responses flows, including SQLite metadata.
- `/v1/health` endpoint.
- Legacy static admin pages at `/logs` and `/usage`, plus configured-password-protected compatibility `/api/logs` JSON aliases for log list/detail/aggregate endpoints.
- On-disk full request/response payload sidecars with retention cleanup and `payload_path` links from logs, including non-stream `parsed_response` raw/transformed fields and strict-gated parsed-stream raw/transformed `.sse` sidecars.
- Go fake and real smoke harness at `cmd/yallmp-smoke`, exposed as `task smoke:fake` and `task smoke:real`, checking client/upstream proxying, request body capture, backend logging, full request sidecars, parsed response/stream sidecars, token usage logging, daily quota reset visibility, direct passthrough fidelity, in-memory OTel span/metric emission including parser/logging phases and stream TTFT/TPOT plus admin-log trace correlation, exact-cache hits/bypass behavior for chat, embeddings, translated non-stream Messages/Responses, and opt-in persistence, embeddings/rerank model rewrite, opt-in streamed tool-argument JSON repair, translated `/v1/responses` non-stream/stream usage details plus streamed tool/terminal edge behavior, and translated Responses/Messages reasoning/thinking/tool-stream behavior.
- `stream_options.*` model defaults are only applied to streaming upstream requests, avoiding non-stream OpenAI-compatible validation failures.
- Built-in Go HTTP reverse forwarder:
  - optional in-process listener via `http_forwarder_settings.enabled`
  - dedicated `cmd/yallmp-forwarder`
  - `task forwarder:http`
  - host preservation, hop-by-hop header filtering, streaming request/response forwarding, encoded-response fidelity, and per-chunk SSE flushing
- Built-in Go TCP forwarder:
  - optional in-process listener via `forwarder_settings.enabled`
  - dedicated `cmd/yallmp-tcp-forwarder`
  - `task forwarder`
  - legacy `FORWARD_*` environment overrides, `TCP_NODELAY`, and response-preserving bidirectional write-side half-close
- Go entrypoint at `cmd/yallmp`.
- Batch 27 Slice C React trace dashboard: read-only Traces tab with trace lookup/deep links, sanitized per-trace summary, timeline, upstream attempts, linked categorical errors, token counts, outcomes, retry metadata, backend names, endpoint kinds, and timing fields from existing admin APIs only.
- Batch 28 Slice C routing/GigaChat hardening: deterministic router tests now cover combined quota-aware selection, prefix-cache affinity, sticky precedence, health/half-open filtering, weighted round-robin fallback/no-op behavior, concurrent prefix recording/selection with FIFO eviction, and retryable 429 health/fallback side effects without recording failed prefix affinity. Prefix affinity recording is mutex-protected across tracker checks and mutation. GigaChat upstream errors now preserve JSON error bodies for non-stream calls and return SSE error events for stream calls, with success-path shape tests pinned.

## Not Yet Implemented

- Complete parser parity with all Python parser/template tests, especially remaining provider-specific parser edge cases beyond the added invalid literal, fragmentation, Qwen repair, custom/drop tag, buffer fallback, trailing text, and template-derived round-trip coverage.
- Full runtime Jinja template renderer parity beyond the bounded admin/template inspection analyzer.
- Remaining Anthropic Messages edge-case parity beyond the implemented text/tool/image/thinking/cache-usage stream adapter paths and added long-tail translator corpus.
- Remaining Responses API edge-case parity beyond the implemented text/tool/image/reasoning/usage, service-tier/cache-key, safe error shape, detailed usage mapping paths, and added long-tail translator corpus.
- Advanced app-key policy tiers beyond the current flat/grouped/default/unauthenticated model and merged limit fields.
- Legacy `/api/usage` compatibility alias. The maintained usage endpoint is `/v1/usage`; legacy `/logs`, `/usage`, and `/api/logs` paths are present.
- Richer quota policy, including more advanced quota tiers beyond flat/grouped scopes and fixed rolling or UTC calendar reset modes, shared multi-replica backend quota state, and upstream 429 learning.
- Richer circuit-breaker policy beyond current opt-in half-open failback, such as adaptive latency/queue-aware recovery.
- Broader operational dashboards beyond the current cache/health/usage observability panel, read-only trace dashboard, and metadata trace-navigation APIs, including richer metrics, queues, quota burn-down, and per-model cost.
- Model catalog UI selectors and any routing integration remain future work; the current catalog tab and warnings are read-only and informational.
- Custom GigaChat CA-bundle loading is not implemented in Go. The Go client supports system trust roots, `verify_ssl`, and local mTLS client cert/key files; the Python-only follow-up branch adds a separate `ca_cert` path that still needs an intentional Go port if required.

See `docs/go_feature_gaps.md` for the feature-gap details, including purpose and Python behavior.
