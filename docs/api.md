# API Reference

This document covers endpoints exposed by the Go server.

## Model List

```http
GET /v1/models
```

Returns configured client-facing models. Model entries include display metadata such as
`supports_reasoning`, `capabilities.reasoning`, and a `reasoning` tag when the
configured backend is marked reasoning-capable. These fields are informational;
they do not cause the proxy to add reasoning or thinking parameters to requests.

## Chat Completions

```http
POST /v1/chat/completions
POST /chat/completions
```

OpenAI-compatible chat completions. Supports streaming and non-streaming responses.

Required request fields:

- `model`
- `messages`

The proxy preserves unknown request fields unless model parameter defaults intentionally override them.

## Embeddings

```http
POST /v1/embeddings
POST /embeddings
```

OpenAI-compatible embeddings.

Required request fields:

- `model`
- `input`

`input` may be a string or an array of strings/token arrays.

## Rerank

```http
POST /v1/rerank
POST /rerank
```

Rerank-compatible request forwarding.

Required request fields:

- `model`
- `query`
- `documents`

`documents` must be a non-empty array of strings. `top_n`, when present, must be positive.

## Anthropic Messages

```http
POST /v1/messages
POST /messages
```

Enabled by `proxy_settings.enable_messages_endpoint`.

For OpenAI-compatible backends, the proxy translates Anthropic Messages requests to Chat Completions and translates responses back. For `api_type: anthropic` backends, requests pass through to `/v1/messages`.

When exact inference caching is enabled, identical non-stream translated Messages requests can read/write the router-owned cache. Streaming requests and native Anthropic pass-through responses are not cached.

Required request fields:

- `model`
- `messages`

The translator supports common text, image, tool use/result, thinking/redacted-thinking blocks, reasoning signatures, tool choice, metadata `user_id`, streaming, and usage mapping.

## Responses API

```http
POST /v1/responses
POST /responses
```

Enabled by `proxy_settings.enable_responses_endpoint`.

For OpenAI-compatible backends, the proxy translates Responses API requests to Chat Completions. For backends with `supports_responses_api: true`, requests pass through to `/v1/responses`.

When exact inference caching is enabled, identical non-stream translated Responses requests can read/write the router-owned cache. Streaming requests and native Responses provider responses are not cached.

Required request fields:

- `model`
- `input`

Supported features include text/tool output translation, reasoning output items and reasoning-summary stream events, streaming events, `store`, `previous_response_id`, SQLite-backed response state, and sticky follow-up routing.

## Passthrough

```http
ANY /p1/{model_name}/{path...}
```

Routes to models configured with `api_type: passthrough`. The proxy preserves the arbitrary method/path/query/body and injects upstream auth. When app keys are enabled it also enforces model access and required-header policy, strips the client credential before forwarding, and applies configured client/backend RPS, RPM, concurrency, queue, and timeout controls. Passthrough does not parse arbitrary response bodies for token-quota accounting.

## Health

```http
GET /v1/health
```

Returns backend health snapshots. When half-open recovery is configured, backend entries may include bounded recovery fields: `state` (`healthy`, `half_open`, or `unhealthy`), `recovery_successes`, and `recovery_success_threshold`. The endpoint does not expose tenant data, raw upstream errors, or raw upstream response bodies.

## Admin Config

```http
GET /admin/config
PUT /admin/config
POST /admin/config/validate
POST /admin/config/reload
```

- `GET /admin/config` returns masked config.
- `PUT /admin/config` validates, preserves missing sensitive fields, persists YAML when a config path is known, and rebuilds runtime router state.
- `POST /admin/config/validate` returns structured validation issues.
- `POST /admin/config/reload` reloads config from disk.

When `YALLMP_ADMIN_PASSWORD` is configured, all four config endpoints require `x-admin-password` (or the legacy `admin_password` query parameter). Leaving the password unset preserves the historical open behavior for these endpoints; do that only on a trusted loopback development listener.

## Admin Cache Stats

```http
GET /admin/cache
```

Returns a read-only exact inference cache snapshot. The request requires `x-admin-password` or `admin_password` matching `YALLMP_ADMIN_PASSWORD`.

Example response:

```json
{
  "enabled": true,
  "mode": "read_write",
  "hits": 12,
  "misses": 7,
  "writes": 7,
  "evictions": 1,
  "entries": 6,
  "total_entry_bytes": 42184,
  "max_entries": 1024,
  "max_entry_bytes": 1048576,
  "persistent_enabled": true,
  "persistent_mode": "read_write",
  "persisted_loaded": 4
}
```

The snapshot contains only counters, configured size bounds, and persistence status/counts. It does not include cache keys, prompts, request bodies, URLs, app-key scopes, headers, or cached response content. Exact-cache persistence is disabled by default; when enabled, it writes cached client-facing response bodies to the configured SQLite path, which operators must protect.

The React admin frontend exposes these counters in the Observability tab alongside backend health and usage summaries.

## Admin Model Catalog

```http
GET /admin/catalog
```

Returns the read-only model catalog. The request requires `x-admin-password` or `admin_password` matching `YALLMP_ADMIN_PASSWORD`.

The catalog is informational only. It lists configured model/provider/node availability and, when `proxy_settings.model_catalog.enabled` plus `probe_nodes` or `probe_providers` opt in probes, overlays provider `/v1/models` results. Probe failures are exposed only as bucketed `error_class` values such as `auth`, `rate_limited`, `timeout`, `network`, `status_error`, `invalid_response`, or `unsupported_api_type`.

The JSON response also includes bounded read-only `warnings` derived from the redacted snapshot, such as probe opt-in gaps, stale probe data, auth/rate-limit probe buckets, or models whose catalog nodes are all unavailable. These warnings are advisory and do not mutate config or routing. The React admin Catalog tab renders the same snapshot and warnings.

The response does not include credentials, raw upstream response bodies, raw error strings, prompts, request headers, client app keys, tenant-scoped request data, or routing mutations.

## Admin Routing CRUD

```http
GET /admin/providers
POST /admin/providers
GET /admin/providers/{name}
PUT /admin/providers/{name}
DELETE /admin/providers/{name}

GET /admin/nodes
POST /admin/nodes
GET /admin/nodes/{name}
PUT /admin/nodes/{name}
DELETE /admin/nodes/{name}

GET /admin/client-models
POST /admin/client-models
GET /admin/client-models/{name}
PUT /admin/client-models/{name}
DELETE /admin/client-models/{name}
```

These endpoints manage top-level `providers`, `nodes`, and client-facing `models` entries. Client models intentionally use `/admin/client-models` so they do not collide with the legacy `/admin/models` model-inheritance APIs.

Reads return raw-ish config entries with sensitive fields recursively masked. Writes require `x-admin-password` or `admin_password` matching `YALLMP_ADMIN_PASSWORD`.

`POST` creates a new entry. `PUT` updates the entry named in the path; payloads may omit the name, but a supplied `name` or `model_name` must match the path. Rename is rejected. Mutations preserve raw unknown keys, unresolved `${ENV}` placeholders, and existing sensitive values omitted from masked payloads. The server validates the full candidate config and rebuilds runtime routing only after validation and apply succeed.

Deletes are dependency-aware:

- Provider delete returns `409 provider_has_nodes` when nodes reference the provider.
- Node delete returns `409 node_has_client_models` when client models reference the node.
- Client-model delete validates the resulting config before applying it and removes deleted client-model names from `router_settings.fallbacks` sources and targets. Legacy `model_list` routes are not touched.

## Admin Router Settings

```http
GET /admin/router-settings
PUT /admin/router-settings
```

`GET /admin/router-settings` returns the current fallback chain payload:

```json
{
  "fallbacks": [
    {"client-model": ["legacy-backup", "node-backup"]}
  ]
}
```

`PUT /admin/router-settings` replaces `router_settings.fallbacks` with the supplied `fallbacks` array. The request requires `x-admin-password` or `admin_password` matching `YALLMP_ADMIN_PASSWORD`.

The fallback payload is strict: it must be a list of single-key objects, each source must be non-empty, each target list must contain non-empty strings, and source/target names must already exist as a legacy `model_list` model, a client model, or a node. Unknown names and malformed entries return `400` with code `invalid_fallbacks`. The mutation preserves unrelated `router_settings` keys and top-level unknown config keys, validates the full candidate config, and applies runtime routing only after validation succeeds.

## Debug Profiling

```http
GET /debug/pprof/
GET /debug/pprof/{profile}
GET /debug/pprof/cmdline
GET /debug/pprof/symbol
GET /debug/pprof/trace
```

The stdlib Go pprof endpoints are disabled unless `proxy_settings.profiling.enabled` is true. When enabled, every profiling request requires `x-admin-password` or `admin_password` matching `YALLMP_ADMIN_PASSWORD`; missing or invalid admin credentials return `403`.

Profiles may expose in-memory runtime data and should only be enabled temporarily on trusted deployments.

## Admin Model Registration

```http
POST /admin/models
```

Registers or replaces a legacy `model_list` model and persists it when the app was loaded from a config file.

Minimal body:

```json
{
  "model_name": "local",
  "api_base": "http://localhost:8000/v1",
  "api_key": "",
  "model": "upstream-model",
  "api_type": "openai"
}
```

Protected existing models require `x-admin-password` or `admin_password` query parameter matching `YALLMP_ADMIN_PASSWORD`.

## Admin Model Reads

```http
GET /admin/models
GET /admin/models/tree
GET /admin/models/{name}/ancestry
GET /admin/models/{name}/dependents
```

Read-only model management endpoints do not mutate config files or runtime routing. Responses mask sensitive values recursively.

`GET /admin/models` returns JSON for API clients:

```json
{
  "protected": [],
  "unprotected": []
}
```

For browser navigations that clearly request HTML, such as `Accept: text/html`, `/admin/models` continues to serve the React admin SPA.

`GET /admin/models/tree` returns:

```json
{
  "roots": ["base"],
  "nodes": {
    "base": {
      "config": {},
      "parent": "",
      "children": ["derived"],
      "protected": true,
      "editable": false
    }
  }
}
```

Tree relationships come from raw `model_list[].extends`, not from flattened resolved model configs.

`GET /admin/models/{name}/ancestry` returns `model`, `chain` with `[name, parent, ...]`, and `inheritance_depth`. `GET /admin/models/{name}/dependents` returns `model`, `direct_children`, `all_descendants`, and `descendant_count`. Unknown models return `404` with JSON error code `model_not_found`.

Model mutation endpoints:

```http
POST /admin/models/copy?source={source}&target={target}
DELETE /admin/models/{name}?cascade=true
```

`POST /admin/models/copy` also accepts a JSON object body with `source` and `target`, but query parameters are supported for legacy admin UI compatibility. The server copies the raw `model_list` entry, preserves `extends`, unknown keys, modules, parameters, limits, and unresolved environment placeholders, strips UI/runtime metadata such as `editable`, `source`, and `_inherited_from`, sets `model_name` to the target, validates the candidate config, and swaps runtime routing only after validation succeeds. Unknown sources return `404` with code `model_not_found`; existing targets return `409` with code `duplicate_model`.

Protected source models require `x-admin-password` or `admin_password` matching `YALLMP_ADMIN_PASSWORD`. If the admin password is unset, protected copies return `403`.

Copy response:

```json
{
  "status": "ok",
  "model": "target-model",
  "config": {
    "model_name": "target-model",
    "extends": "base-model",
    "model_params": {"model": "upstream-name"}
  }
}
```

`config` is recursively masked and never returns secrets.

`DELETE /admin/models/{name}` supports slash-containing names using the same path parsing rules as the read APIs. Unknown targets return `404` with code `model_not_found`. If the target has dependent models and `cascade=true` is not present, the server returns `409` with code `model_has_dependents`, `direct_children`, `dependents`, and a cascade hint. With `cascade=true`, the target and all descendants are removed from raw `model_list`; `router_settings.fallbacks` entries are cleaned when deleted models appear as sources or fallback targets. The candidate config is validated and applied atomically before runtime state changes.

Delete requires the admin password when the target or any deleted descendant is protected.

## Admin App Keys

```http
GET /admin/keys
GET /admin/keys/{key_id}
POST /admin/keys
PUT /admin/keys/{key_id}
DELETE /admin/keys/{key_id}
POST /admin/keys/{key_id}/regenerate
POST /admin/keys/config
```

Every `/admin/keys*` request requires `x-admin-password` or `admin_password` matching `YALLMP_ADMIN_PASSWORD`. If the admin password is unset or wrong, the server returns `403` and does not return key metadata.

Secrets are never returned by list, get, update, or config responses. `POST /admin/keys` and `POST /admin/keys/{key_id}/regenerate` return the new secret exactly once in the top-level `secret` field; store it immediately.

Create body fields:

```json
{
  "key_id": "client-a",
  "secret": "optional-existing-secret",
  "group_id": "team-a",
  "name": "Client A",
  "description": "Production integration",
  "enabled": true,
  "models": ["premium-model"],
  "required_headers": [
    {"name": "X-Workload-ID", "log_as": "workload", "models": ["premium-model"]}
  ],
  "limits": {"rps": 5},
  "concurrency_limit": 2,
  "priority": 50,
  "queue_timeout": 15
}
```

`key_id` and `secret` are generated when omitted. Duplicate key IDs are rejected. `PUT /admin/keys/{key_id}` updates metadata and limits while preserving the existing secret. `DELETE` removes the key from persisted config and runtime policy. `regenerate` replaces the secret and invalidates the old one immediately.

`POST /admin/keys/config` updates app-key settings, defaults, unauthenticated limits, and groups:

```json
{
  "enabled": true,
  "header_name": "x-api-key",
  "allow_unauthenticated": false,
  "required_headers": [
    {"name": "X-Tenant-ID", "log_as": "tenant"}
  ],
  "defaults": {"rps": 10},
  "unauthenticated": {"rpm": 30},
  "groups": [
    {
      "group_id": "team-a",
      "name": "Team A",
      "enabled": true,
      "required_headers": [{"name": "X-Team-ID", "log_as": "team"}]
    }
  ]
}
```

`required_headers` entries require `name` and may include `log_as` and `models`. Missing headers return `400` with code `missing_required_header` before admission, quota, or upstream routing. `log_as` values are only written to full-request sidecar logs for accepted requests when full-request storage is enabled; Prometheus metrics do not include raw header values.

All mutations run the same config validation and apply path as `/admin/config`; unknown groups, invalid limits, invalid required-header rules, and router build failures return structured validation errors without mutating runtime state.

## Admin Templates

```http
GET /admin/templates
POST /admin/templates
GET /admin/templates/inspect?template_path={path}
GET /admin/templates/inspect?template_path={path}&render_preview=1
```

Templates are constrained to `configs/jinja_templates`.

- `GET /admin/templates` lists `.jinja`, `.j2`, and `.jinja2` files.
- `POST /admin/templates` accepts multipart `file`, JSON `{ "name": "...", "content": "..." }`, or raw body with a `name` query parameter. Content must be UTF-8. When `YALLMP_ADMIN_PASSWORD` is configured, pass it as `x-admin-password` or `admin_password`.
- `GET /admin/templates/inspect` reads a template by `template_path`, `path`, or `name` and returns the parse-tags config detected by Go's bounded Jinja inspection analyzer and raw marker detector. It includes evidence, warnings, and analysis metadata. The analyzer is for parser-config inference and is not a full production Jinja renderer.
- Add `render_preview=1` to include a strictly additive `render_preview` object. The preview uses `engine: bounded_jinja_runtime`, a synthetic sample chat/tool context, and a byte cap; it never renders live request or response bodies. Unknown runtime filters, statements, or functions are reported in the preview `error` field without changing the inspect `analysis.engine`, which remains `bounded_jinja_inspection`.

Example inspect response:

```json
{
  "template_path": "configs/jinja_templates/template_example.jinja",
  "detected": true,
  "config": {
    "think_tag": "think",
    "tool_tag": "tool_call",
    "tool_arg_format": "xml"
  },
  "evidence": [
    {
      "kind": "tool_parser",
      "value": "qwen_xml",
      "source": "raw_template"
    }
  ],
  "warnings": [],
  "analysis": {
    "engine": "bounded_jinja_inspection",
    "full_jinja": false
  },
  "render_preview": {
    "engine": "bounded_jinja_runtime",
    "full_jinja": false,
    "truncated": false,
    "rendered": "<|im_start|>system\n..."
  }
}
```

## Admin Logs

```http
GET /admin/logs
GET /admin/logs/{id}
GET /admin/logs/stop-reasons
GET /admin/logs/tool-call-rate
GET /admin/logs/models
GET /admin/traces/{trace_id}
```

`GET /admin/logs` uses persistent request metadata when configured and falls back to in-memory request logs otherwise.

Compatibility aliases are also available at `/api/logs`, `/api/logs/{id}`, `/api/logs/stop-reasons`, `/api/logs/tool-call-rate`, and `/api/logs/models`.

When `YALLMP_ADMIN_PASSWORD` is configured, every listed `/admin/logs`, `/admin/traces`, and `/api/logs` request requires it. Leaving the password unset preserves open legacy log access except for endpoints such as trace aggregation that always require an admin password. Because log detail can include prompts and response bodies, configure the password for every shared or non-loopback deployment.

When OTel tracing is enabled, request log rows may include `trace_id` and `span_id` from the proxy-owned `proxy.request` span. These fields are empty or omitted when OTel is disabled/noop.

Supported SQLite query parameters:

- `limit`
- `offset`
- `model` or `model_name`
- `outcome`
- `stop_reason`
- `is_tool_call`
- `start_time` or `start_date`
- `end_time` or `end_date`
- `search`
- `trace_id` exact-match filter; this trace-navigation filter is honored on `/admin/logs`

`GET /admin/logs/{id}` returns decoded full request/response sidecar data when available. The metadata portion can include the same optional `trace_id` and `span_id` fields as the list response. Shared proxy routes also include metadata-only upstream attempts as `attempt_trail` and `attempts`; attempt entries include attempt index, backend name, retry index, status, retryable flag, outcome, duration in milliseconds, and sanitized error text when present.

`GET /admin/traces/{trace_id}` requires the admin password and returns metadata-only trace navigation over persisted request logs sharing that proxy-owned trace ID. The response includes paginated request metadata, flattened attempts, linked categorical error metadata, summary models/backends/outcomes, and pagination fields. It does not decode full-request sidecars or return request/response bodies, raw stored error messages, or query strings. Unknown valid trace IDs return `404`.

## Runtime Metrics

```http
GET /v1/usage
GET /v1/health
GET /admin/backend-quotas
GET /metrics
```

- `/v1/usage` returns admission and token quota metrics. Quota `reset_at_by_scope` values reflect the configured reset policy: rolling windows use the next rolling reset, while calendar modes use the next UTC daily/weekly/monthly/yearly boundary.
- `/v1/health` returns backend health snapshots, including `inflight_requests`, a read-only count of live upstream attempts for each configured backend.
- `/admin/backend-quotas` requires the admin password and returns a read-only snapshot of configured backend quota scopes with backend name, used tokens, configured limit, remaining tokens, and reset time. It does not include app-key/group IDs, request payloads, prompts, headers, backend URLs, or secrets.
- `/metrics` returns a Prometheus text-format snapshot when `proxy_settings.metrics.enabled` is true. It is disabled by default and returns the normal not-found response when disabled. `proxy_settings.metrics.timings.enabled` is a separate disabled-by-default opt-in for request/backend/stream timing histograms on the same enabled endpoint.

The Prometheus snapshot uses only in-memory admission, quota, backend-health, backend in-flight, and opt-in timing state. It does not read logs, request payloads, prompts, request paths, query strings, secrets, raw app-key IDs, raw group IDs, or backend error strings. Admission and quota metrics are aggregated without scope labels. Backend metrics, including `yallmp_backend_inflight_requests`, use only a bounded `backend` label, and backend quota samples are emitted only for configured backends with positive token quotas. Timing histograms are independent from OTel and include only bounded labels such as endpoint kind, sanitized configured model/backend label, stream flag, outcome, status class, and error bucket.

OpenTelemetry tracing, metrics, and span-event logs are configured through `proxy_settings.otel` and are disabled by default. When enabled, the generic JSON proxy endpoints (`/v1/chat/completions`, `/v1/embeddings`, and `/v1/rerank`) plus `/v1/messages` and `/v1/responses` emit request, policy, admission, routing, upstream-attempt, response, parser, and logging spans through OTLP gRPC where those phases run. Direct `/p1` passthrough emits request, optional policy/admission, upstream-attempt, and response spans while preserving arbitrary payload forwarding. OTel metrics include `yallmp.proxy.requests`, `yallmp.proxy.request.duration`, `gen_ai.client.token.usage`, `gen_ai.client.operation.time_to_first_token`, and `gen_ai.client.operation.time_per_output_token`; token usage is emitted only after usage reconciliation, TTFT is emitted after the first successful client-visible stream write, and TPOT is emitted once at stream EOF as an aggregate average between streamed chunks. `proxy_settings.otel.logs.enabled` is a second disabled-by-default gate that requires `proxy_settings.otel.enabled: true`; when enabled it emits one sanitized `proxy.log` span event at span end instead of adding a separate OTLP logs exporter. Attributes are intentionally bounded: phase, endpoint kind (`chat`, `embeddings`, `rerank`, `messages`, `responses`, `passthrough`, or `other`), sanitized configured model label, backend name, outcome, attempt/retry indexes, stream flag, HTTP status, error bucket, key-scope bucket, token type/count fields when present, trace/span IDs, and GenAI-style `gen_ai.system`, `gen_ai.operation.name`, and `gen_ai.request.model` attributes. Response-phase spans also set `gen_ai.response.model` when it is the same sanitized configured model label. Telemetry does not include prompts, request/response bodies, raw headers, query strings, bearer tokens, raw app-key IDs, raw group IDs, required-header values, provider API keys, raw backend URLs, or raw error strings.

Metric families currently exposed:

- `yallmp_admission_*` gauges/counters for active requests, global queue depth, total requests, queued totals, rate-limited totals, observed max queue depth, observed concurrency limit, and average queue wait.
- `yallmp_backend_admission_*` gauges/counters for backend-scoped admission state when backend limits create `backend:<name>` scopes.
- `yallmp_quota_tokens_used`, `yallmp_quota_scope_count`, and `yallmp_quota_next_reset_timestamp_seconds`.
- `yallmp_backend_quota_tokens_used`, `yallmp_backend_quota_tokens_limit`, `yallmp_backend_quota_tokens_remaining`, and `yallmp_backend_quota_next_reset_timestamp_seconds` for configured backend quota scopes.
- `yallmp_backend_healthy`, `yallmp_backend_score`, success/failure counters, consecutive failures, last status, and backend health timestamps.
- `yallmp_request_duration_seconds`, `yallmp_backend_request_duration_seconds`, `yallmp_stream_ttft_seconds`, and `yallmp_stream_tpot_seconds` histograms when `proxy_settings.metrics.timings.enabled` is true.

## Admin Frontend

```http
GET /admin
GET /admin/
GET /admin/assets/{asset}
GET /logs
GET /usage
```

Serves the React admin frontend embedded in the Go binary. Refresh the committed production bundle with:

```bash
task frontend:build
```

The admin frontend includes a Logs workspace with complete filters/pagination and a structured inspector for request messages, tools, responses, usage, upstream attempts, warnings/errors, headers, raw JSON, copy/download, and adjacent-request navigation. Its read-only Traces tab consumes admin-password-gated `GET /admin/traces/{trace_id}` responses and can open a trace event's full request inspector. The Observability tab summarizes `GET /admin/cache`, `GET /v1/health`, and `GET /v1/usage`; `/admin/cache` uses the admin-password request behavior while health and usage consume public runtime metrics endpoints.

The legacy static `/logs` and `/usage` pages are still served from `static/admin` when those files are present. They are compatibility pages; the maintained admin UI is `/admin`, and usage JSON is exposed at `/v1/usage` rather than the old `/api/usage` alias.

## Error Format

OpenAI-compatible endpoints return:

```json
{
  "error": {
    "message": "message",
    "type": "invalid_request_error",
    "code": "code"
  }
}
```

Messages and Responses endpoints return their API-specific error shapes where applicable.


## Simple routing administration

- `GET /admin/routing-pools` returns effective public model pools, node names,
  upstream model names, provider attribution, health, and whether prompt-prefix
  affinity is enabled. It does not change routing state or include credentials.
- `POST /admin/providers/discover` accepts `api_base`, optional `api_key` and
  `api_type` (OpenAI-compatible only), and optional `name` to reuse a saved
  provider's credentials. It returns `{ "models": ["model-id", ...] }` without
  saving anything. Requires the configured admin password. Redirects are not
  followed; discovery has a ten-second timeout and a two-MiB response limit.
- Existing provider POST/PUT APIs accept `models`: an array of strings or
  `{ "name": "public-name", "upstream_model": "upstream-id" }` objects.
  The selection derives runtime nodes and public pools atomically. Replacing
  the selection removes deselected generated nodes.
- Node POST/PUT accepts optional `upstream_model`. Nodes no longer require a
  provider if they specify their own `api_base`. Unwired nodes with a model name
  automatically form pools.

See [the simple routing guide](simple-routing.md) for examples and migration.
