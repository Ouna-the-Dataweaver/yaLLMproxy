# Testing Guide

This branch uses Go tests and Go smoke harnesses.

## Unit And Integration Tests

Run the full Go suite:

```bash
task test
```

This executes:

```bash
go test ./...
```

The Go tests cover:

- Config loading, env substitution, inheritance, validation, masking, app-key config, structured routing admin update preservation, and strict router fallback replacement.
- Router behavior, fallback, retries, health scoring, opt-in half-open circuit recovery, endpoint selection, node access control, sticky routing, weighted round robin, opt-in prefix-cache affinity, opt-in concurrency-aware least-in-flight selection, combined quota/prefix/sticky/health gate precedence, prefix FIFO/race hardening, retryable 429 health/fallback side effects, and exact-cache read/write/bypass behavior.
- Backend request body rewriting, upstream headers, target model rewriting, reasoning injection, and stream-only parameter handling.
- Admission/concurrency, upstream-attempt in-flight counter release paths, priority queues, RPS/RPM limits, token quotas, rolling and UTC calendar-aligned quota resets with fake-clock coverage, and quota restoration from log database rows.
- Prometheus `/metrics` default-off behavior, backend health/quota/admission/in-flight snapshots, opt-in OTel-independent request/backend/stream timing histograms, low-cardinality sanitized timing labels, OTel coexistence without duplicate OTel records, and stream timing samples only from successful client-visible writes.
- OpenTelemetry default-off behavior, strict config/runtime validation, in-memory span and metric recording, bounded attribute safety, GenAI-style span and token metrics, stream TTFT/TPOT metrics, generic JSON proxy, Messages, Responses, and passthrough phase spans, parser/logging phase spans, policy/admission rejection span boundaries, configured-model labeling for rejected unknown models, reconciled usage metric emission, no duplicate stream token metric records, byte-preserving stream timing observation, and admin-log trace/span correlation.
- Chat, embeddings, rerank, Messages, Responses, passthrough, admin config, usage, health, and log APIs, including Responses non-stream terminal finish-reason mapping and reasoning/thinking translation paths.
- GigaChat cloud/local config, chat/stream response conversion, tool-call emulation, and stream-vs-non-stream upstream error normalization.
- Responses and Messages translator regression corpus coverage for refusal/annotation content, empty content, incomplete/failed status details, Anthropic content block ordering, stop-reason/usage deltas, text/tool interleaving, and malformed or unknown SSE events.
- Response parser modules for reasoning/tool-call extraction, Qwen/K2 markers, Qwen XML and bounded Jinja template inspection, malformed literals, fragmented SSE events, arbitrary fragmentation, custom/drop tags, buffer fallback, stream buffer byte/age caps, parser stream reset/close lifecycle hooks, opt-in streamed tool-argument JSON repair, template-derived round trips, and reasoning/content swap.
- Deterministic fake-local provider conformance for chat, Messages, and Responses pass-through and translated paths, plus endpoint-denied behavior.
- SQLite and PostgreSQL log database configuration, request metadata, idempotent trace/span column migration, error logs, analytics, full request sidecars, parsed response/stream sidecars, response state storage, and cleanup.
- HTTP and TCP forwarders.

PostgreSQL live integration is optional and skipped by default. To run it, point `YALLMP_TEST_POSTGRES_DSN` at a disposable PostgreSQL database:

```bash
YALLMP_TEST_POSTGRES_DSN='postgres://yallmp:REDACTED@localhost:5432/yallmp_logs?sslmode=disable' task test:postgres
```

The default `task test` command does not require a live PostgreSQL service.

## Fake Smoke

Run:

```bash
task smoke:fake
```

The fake smoke command starts:

- an emulated upstream API server,
- the Go proxy handler,
- a real HTTP client against the proxy.

It verifies:

- `/v1/models` lists the configured model,
- chat, streaming chat, embeddings, and rerank requests pass through the proxy,
- request parameters are forwarded to the upstream instead of silently dropped,
- model rewriting, including embeddings/rerank model rewrite, and forced stream usage settings work,
- fallback routing retries a retryable primary failure on a secondary backend,
- stream usage tokens are observed,
- `/v1/usage` exposes the next controlled UTC calendar reset timestamp for a daily app-key token quota,
- direct passthrough preserves path normalization, query string, request body, selected headers, upstream auth injection, and response headers,
- exact cache hits avoid upstream and the `x-yallmp-cache-bypass` header forces an upstream call without leaking upstream,
- translated `/v1/responses` non-stream and stream usage/reasoning details are preserved, including streamed tool-call deltas and streamed terminal `length`/`content_filter` mappings,
- translated `/v1/messages` thinking stream behavior is preserved, including streamed tool calls and `tool_use` stop mapping,
- in-memory OpenTelemetry smoke spans and metrics are emitted for chat, translated Messages/Responses streaming, parser/logging phases, streaming TTFT/TPOT, and passthrough without leaking selected smoke secrets, and at least one admin log row correlates to a recorded proxy-owned request span,
- opt-in streamed tool/function argument JSON repair produces valid arguments for a malformed fake upstream stream,
- template inspect reports `tool_parser: qwen_xml` for marker-based Qwen-style templates,
- `/admin/logs` records token usage, backend names, and OTel trace/span IDs when enabled,
- `/admin/logs/{id}` exposes full request/response sidecar data, optional OTel trace/span IDs, and parsed response/stream artifacts when enabled.

No persistent server is left running.

## Real Smoke

Run:

```bash
task smoke:real
```

Defaults:

- config: `/home/theio/repos/nidai_db/infra/yaLLMproxy/configs/config.yaml`
- model: `glm_local`

Override them with:

```bash
task smoke:real -- --config /path/to/config.yaml --model glm_local
```

The real smoke loads the external config and its adjacent `.env`, then overrides only the logging database and full-request directory to temporary paths. It sends non-stream and stream chat requests through the Go proxy to the selected real upstream model, then checks admin logs and full request detail.

No persistent server is left running.

## Frontend

The React admin frontend is separate from the Go test suite:

```bash
task frontend:install
task frontend:build
```

Build output in `web/admin/dist` is served by the Go app at `/admin`.

The frontend build typechecks the admin Observability tab, including the exact-cache, backend-health, usage/quota, and optional trace ID rendering paths.
