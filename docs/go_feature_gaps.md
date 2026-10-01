# Go Feature Gaps

Last verified: 2026-07-09.

This document is the Python-to-Go migration inventory. Many original gaps are now marked implemented; sections with a remaining-gap paragraph are the active publish risks or deliberate follow-up work.

## GigaChat Custom CA Bundles

The Go GigaChat client supports cloud OAuth, local mTLS client certificates, system trust roots, and the `verify_ssl` switch. It does not accept a separate custom CA bundle path.

The newer `codex/gigachat-ca-cert-followup` branch adds `ca_cert` to the historical Python implementation only; that branch diverges from the Go refactor at the pre-pass baseline and is not evidence that the Go feature is complete.

Remaining gap: add a masked, validated Go `ca_cert` field and load it into `tls.Config.RootCAs`, with focused certificate-pool tests, before operators rely on private GigaChat certificate authorities.

## Admin Model Management APIs

Python exposed `GET /admin/models`, `GET /admin/models/tree`, `GET /admin/models/{name}/ancestry`, `GET /admin/models/{name}/dependents`, `DELETE /admin/models/{name}`, and `POST /admin/models/copy`.

What for: these endpoints let the admin UI and scripts inspect model inheritance, understand protected/editable state, copy a known-good model, and delete models safely.

Why it mattered: configs can contain inherited models and protected base models. Without tree/dependent visibility, editing raw YAML can accidentally break derived models or remove a base that other entries still need.

How Python worked: `CONFIG_STORE` kept the raw YAML, built a model tree, resolved inheritance for runtime, masked secrets for reads, required `YALLMP_ADMIN_PASSWORD` for protected-model mutations, supported cascade deletion, copied model entries while removing UI metadata, and unregistered deleted runtime backends.

Go status: implemented. Config loading resolves `extends`, `GET/PUT /admin/config`, `POST /admin/config/validate`, `POST /admin/config/reload`, and `POST /admin/models` exist. Go exposes model management APIs for listing protected/unprotected resolved models, raw inheritance tree inspection, ancestry, dependents, raw-entry copy, and delete with cascade checks. Responses mask sensitive values and preserve the React admin SPA behavior for browser HTML requests to `/admin/models`. Copy/delete mutate raw `model_list`, preserve inheritance/env placeholders/unknown keys, require the admin password for protected mutations, clean fallbacks on delete, validate candidate config, and then swap runtime routing atomically. The React Routing UI exposes copy/delete actions in the model tree, sends only `source`/`target` for copy, shows direct children/all descendants before delete, and uses explicit cascade confirmation.

Remaining follow-up: broader operational dashboards beyond the current cache/health/usage Observability tab and read-only Traces tab remain separate work.

Current workaround for unsupported model fields: use the dedicated backend endpoints directly, or edit raw config through `PUT /admin/config` or the React raw config editor.

## Provider/Node/Client Model Backend CRUD APIs

Python-era admin workflows could manage structured routing resources without editing the full YAML document.

What for: operators need to add or update shared providers, concrete upstream nodes, and client-facing node-backed models while preserving credentials and routing safety.

Why it mattered: provider and node entries carry upstream credentials and are referenced by client models. Deleting them blindly can break routing or expose secrets through masked-edit flows.

Go status: implemented for backend APIs. Go exposes `GET/POST/GET-by-name/PUT/DELETE` for `/admin/providers`, `/admin/nodes`, and `/admin/client-models`. Writes require the admin password, mutate cloned raw config, preserve unknown keys, unresolved environment placeholders, and omitted sensitive fields, validate the full candidate config, persist when loaded from disk, and apply runtime routing only after validation succeeds. Provider deletes reject referenced providers, node deletes reject referenced nodes, and client-model deletes clean `router_settings.fallbacks` entries without touching legacy `model_list` routes.

Go status: implemented for React UI phase 1 plus safe-field edits. The admin SPA exposes structured list, create, dependency-aware delete, and edit workflows for `/admin/providers`, `/admin/nodes`, and `/admin/client-models`. Edit support covers provider `api_base`, `api_type`, optional API key replacement, endpoints, and limits; node `provider`, `model`, optional `api_base`, endpoints, limits, modules, and `allowed_keys`; client model `nodes`, `node_weights`, `balancing`, limits, and `allowed_keys`; replace-only router fallback chains through `/admin/router-settings`; and legacy model dependency visibility plus copy/delete actions over the legacy inheritance APIs. Names remain read-only. Provider `modules` are not exposed because they do not have config/runtime semantics.

## OpenTelemetry Request Tracing

Python did not have a first-class OpenTelemetry pipeline, but the Go refactor tracks observability as a publish requirement for operating local model fleets.

What for: traces help debug admission, routing, retry/fallback, backend saturation, and upstream latency without opening request payloads or logs.

Why it matters: the proxy needs operational visibility while staying transparent and avoiding prompt, header, key, or tenant leaks in telemetry backends.

Go status: implemented as disabled-by-default request-span, metrics, and span-event log groundwork. `proxy_settings.otel` validates `enabled`, `insecure`, `service_name`, `exporter`, `endpoint`, exporter `headers`, and `logs.enabled`; enabled OTel requires an OTLP gRPC endpoint, and `logs.enabled: true` requires global OTel to be enabled. The runtime wires tracing and metrics through no-op providers by default, OTLP exporters when enabled, and an in-memory recorder for tests. The generic JSON proxy endpoints (`/v1/chat/completions`, `/v1/embeddings`, and `/v1/rerank`) plus translator-specific `/v1/messages` and `/v1/responses` emit request, policy, admission, routing, upstream-attempt, response, parser, and logging spans where those phases run. Direct `/p1` passthrough emits request, optional app-key policy/admission, upstream-attempt, and response spans while preserving arbitrary payload semantics. OTel metrics include request counts, request durations, GenAI token usage from reconciled usage, TTFT after the first successful client-visible streamed write, and one aggregate TPOT/ITL sample at stream EOF. When `proxy_settings.otel.logs.enabled` is true, the provider emits sanitized `proxy.log` span events through the existing trace exporter rather than adding a separate OTLP logs exporter. Span, metric, and log-event attributes are bounded and sanitized: phase, endpoint kind, configured model label, backend name, outcome, attempt/retry indexes, stream flag, HTTP status, error bucket, key-scope bucket, token type/counts, trace/span IDs, and GenAI-style `gen_ai.system`, `gen_ai.operation.name`, `gen_ai.request.model`, and response-phase `gen_ai.response.model` when it matches the sanitized configured model label. Persisted request metadata and `/admin/logs` responses carry proxy-owned `trace_id` and `span_id` when OTel is enabled, using only the span context returned by the proxy's own request span. Admin trace navigation is available through the admin-protected `GET /admin/logs?trace_id=...` exact filter, metadata-only `GET /admin/traces/{trace_id}` aggregation over linked request, attempt, and error records, and a read-only React Traces tab that renders only sanitized trace metadata. Prompts, request/response bodies, raw headers, query strings, bearer tokens, raw app-key IDs, raw group IDs, required-header values, provider API keys, raw backend URLs, and raw error strings are not recorded in telemetry or the trace dashboard.

Prometheus timing status: implemented as a separate disabled-by-default path under `proxy_settings.metrics.timings.enabled`. When the `/metrics` endpoint is enabled, Go can expose OTel-independent histograms for request duration, backend attempt duration, stream TTFT, and stream TPOT with bounded sanitized labels. These metrics do not require OTel and skip failed/no-write streams.

Remaining gap: richer React admin dashboard views remain beyond the current cache/health/usage panel and read-only trace metadata dashboard, especially queue/SLO/cost views and deeper metrics visualizations.

## Routing And Backend Steering

Python had simpler direct routing, while the Go refactor tracks routing intelligence as an operational requirement for local model fleets.

Go status: implemented for retry/fallback routing, backend health snapshots, active health probes, score-aware fallback ordering, opt-in half-open recovery, weighted round-robin, sticky conversation/follow-up routing, app-key/node `allowed_keys`, provider/node endpoint gates, provider/node admission limits, opt-in quota-aware backend selection, opt-in backend quota restore from log metadata, opt-in prefix-cache-affinity routing, and opt-in concurrency-aware least-in-flight node selection. The router exposes backend health, in-flight counts, quota metrics, and deterministic tests for combined quota/prefix/sticky/health behavior.

Remaining gap: shared multi-replica backend quota state, upstream 429 learning, adaptive latency/queue-aware routing beyond the current health/score/least-in-flight signals, and centralized flow-control queues are still future work.

## Exact Inference Cache

TensorZero-style exact inference caching is tracked as a cost and latency feature that must remain transparent to client-visible responses.

Go status: implemented for complete 200 JSON non-stream `/v1/chat/completions`, `/v1/embeddings`, and translated non-stream `/v1/messages` or `/v1/responses` requests. The cache is disabled by default, supports read/write, read-only, write-only, and off modes, scopes keys by selected backend plus app-key/group and required-header attribution, strips the truthy `x-yallmp-cache-bypass` request header upstream, and exposes only bounded counters through admin-protected `GET /admin/cache`. Optional SQLite persistence is also disabled by default; when enabled, it stores the same client-facing response bodies and filtered response headers on disk, rehydrates only entries still inside TTL/max-age/size/count limits, and keeps persistence failures non-fatal. Operators must protect the configured path. Translated Messages/Responses entries store the final client-facing translated JSON response, not the internal upstream Chat Completions response. Streams, errors, non-200 responses, native Anthropic Messages pass-through, and native Responses provider pass-through are not cached.

Remaining gap: streaming cache support and any native provider pass-through caching remain future work.

## Semantic Inference Cache

Bifrost-style semantic caching can reduce repeat chat costs, but it is intentionally less transparent than exact caching because a similar prompt can reuse a prior answer.

Go status: implemented as an opt-in layer for non-stream `/v1/chat/completions` only. It requires the global inference cache mode to be enabled plus explicit `proxy_settings.inference_cache.semantic.embedding_node` and `embedding_model` values. On lookup/store it embeds the canonical outbound request body through that node, keeps embeddings and complete 200 client-facing JSON responses in a bounded in-memory vector index, and filters candidates by endpoint, raw query, backend, client model, embedding model, app-key/group scope, required-header attribution, and non-message request controls before cosine ranking. `x-yallmp-cache-bypass` skips semantic reads and writes, and embedding errors fall through to exact cache/upstream without failing the request. `GET /admin/cache` exposes semantic counters and size totals only; it does not expose prompts, request bodies, cache keys, embeddings, vectors, scopes, backend URLs, headers, or response bodies.

Remaining gap: persistence/ANN indexing, translated Messages/Responses semantic caching, streaming semantic caching, per-model thresholds, and prompt-normalization heuristics are future work.

## Model Catalog And Provider Availability

Bifrost-style provider visibility is useful for operators managing multiple providers and node-backed client models.

Go status: implemented as a read-only catalog exposed through admin-protected `GET /admin/catalog`. It is seeded from configured legacy `model_list` entries plus provider/node/client-model resources. Optional `/v1/models` availability probes are disabled by default and require both `proxy_settings.model_catalog.enabled: true` and explicit `probe_nodes` or `probe_providers` opt-in. Probes use provider credentials, strip internal headers, bucket failures, honor TTL/backoff settings, and never change routing decisions. The endpoint includes bounded advisory warnings derived from the redacted snapshot, and the React admin Catalog tab displays configured, available, stale, and unavailable rows plus those warnings.

Remaining gap: React selector integration, richer provider metadata, and any future routing integration remain follow-up work.

## Admin App-Key CRUD APIs

Python exposed `GET /admin/keys`, `GET /admin/keys/{key_id}`, `POST /admin/keys`, `PUT /admin/keys/{key_id}`, `DELETE /admin/keys/{key_id}`, `POST /admin/keys/{key_id}/regenerate`, and `POST /admin/keys/config`.

What for: these endpoints let operators manage client-facing keys without manually editing YAML and without reusing secrets in logs or UI responses.

Why it mattered: app keys control access, per-key/group model permissions, concurrency, priority, queue timeout, RPS/RPM, and token quotas. Runtime key creation/regeneration is an operational workflow.

How Python worked: handlers required the admin password, generated key IDs/secrets, masked secrets after creation, preserved existing secrets on update, toggled key auth, and persisted changes to config.

Go status: implemented. The Go server exposes the app-key CRUD/admin endpoints, requires `YALLMP_ADMIN_PASSWORD` for every `/admin/keys*` request, masks existing secrets, returns create/regenerate secrets exactly once, validates changes, persists `app_keys`, and updates runtime policy immediately. Required-header policy is also supported at global, group, and key levels with optional model scoping and full-request sidecar attribution aliases. The React admin UI has an App Keys tab for list/create/edit/delete/regenerate/config workflows; secrets are only shown from create/regenerate responses in a one-time banner.

Remaining gap: backend API and frontend controls are present. Broader app-key policy tiers and runtime config transaction cleanup remain separate follow-ups.

## Template Management APIs

Python exposed `GET /admin/templates`, `POST /admin/templates`, and `GET /admin/templates/inspect`.

What for: templates help configure parser behavior for model-specific thinking/tool-call formats.

Why it mattered: local models may emit different tags or separators. Inspecting a Jinja template gave suggested parser/swap config without hand-deriving prefixes and delimiters.

How Python worked: the API listed files under `configs/jinja_templates`, accepted UTF-8 uploads with safe filenames, constrained paths to the template directory, and used the template analyzer to return detected think/tool config.

Go status: implemented. The Go server exposes `GET /admin/templates`, `POST /admin/templates`, and `GET /admin/templates/inspect` for safe files under `configs/jinja_templates`. Inspect uses a bounded Jinja-compatible inspection renderer/analyzer plus raw marker detection to derive `parse_tags` config, including `tool_parser: qwen_xml` for marker-based Qwen-style `<tool_call><function=...><parameter=...>` templates. Responses include detection evidence, warnings for unsupported or capped inspection behavior, and `analysis.engine: bounded_jinja_inspection`. An opt-in `render_preview=1` inspect parameter adds a byte-capped `bounded_jinja_runtime` preview over synthetic sample messages/tools without changing inspector behavior.

Remaining gap: Go includes a bounded runtime renderer for common HuggingFace chat-template expressions, control flow, loops, loop metadata, whitespace trimming, and common filters, but not full Jinja macro/import/include parity. The bounded inspector is still intended for admin/parser-config inference, not production prompt rewriting.

## PostgreSQL Log Database

Python supported SQLite and PostgreSQL through the SQLAlchemy database layer.

What for: PostgreSQL allowed central persistent request metadata and analytics across longer-running or multi-host deployments.

Why it mattered: SQLite is convenient locally, but a shared operational proxy may need a remote DB, pooling, backups, and concurrent readers.

How Python worked: `database.backend: postgres` selected PostgreSQL, used `connection.postgres` credentials, created SQLAlchemy sessions, and stored request/error/metadata rows.

Go status: implemented. `internal/logdb` supports SQLite plus PostgreSQL through `pgx/v5/stdlib`, accepts `postgres` and `postgresql` backends, initializes the request metadata, attempts, error, and response state tables, and keeps request-time persistence failures non-fatal.

Remaining note: full request/response sidecar files are still stored on the local filesystem path configured under `proxy_settings.logging.full_request_storage`; PostgreSQL stores only the sidecar metadata path and expiration.

## Legacy Static Admin Routes

Python served legacy static pages at `/admin/`, `/logs`, `/usage`, `/api/usage`, and related paths.

What for: these were quick operator pages for config, logs, and usage views before the React admin UI.

Why it mattered: old bookmarks and scripts may still hit those paths.

How Python worked: FastAPI mounted static assets from `static/admin`, served HTML files directly, and exposed `/api/usage` alongside `/v1/usage`-style runtime metrics.

Go status: mostly implemented. Go embeds the committed React production bundle and serves it under `/admin`, keeps static legacy `/logs` and `/usage` pages from `static/admin`, exposes `/v1/usage`, and exposes JSON log APIs under both `/admin/logs` and compatibility `/api/logs` paths. Configured admin passwords protect both log namespaces and config APIs. The React Logs view now has a structured request inspector, filters/pagination, readable prompt/tool/response sections, raw mode, and Trace-to-Request navigation; the Traces tab consumes sanitized `/admin/traces/{trace_id}` metadata. The old `/api/usage` alias is not preserved.

Current workaround: use `/admin` for the maintained UI, `/logs` or `/usage` only for legacy static pages, `/v1/usage` for usage JSON, and `/admin/logs` or `/api/logs` for log JSON.

## Parsed Response Sidecar Logs

Python had disk logging paths controlled by `proxy_settings.logging.log_to_disk`, `log_parsed_response`, and `log_parsed_stream`.

What for: these files helped compare raw upstream output with parsed/transformed output when debugging parser rules.

Why it mattered: parser bugs are easiest to diagnose when raw chunks, parsed chunks, and final response metadata are all available.

How Python worked: `RequestLogRecorder` wrote request/response logs and optional parsed sidecars. Non-stream and stream paths could record transformed content separately from the raw request record.

Go status: Go stores request headers/body and final response bodies in full-request JSON sidecars, plus structured SQLite metadata and usage. For non-stream JSON responses transformed by response modules, Go also stores raw upstream and transformed client response bodies under the sidecar `parsed_response` field when `full_request_storage.enabled`, `log_to_disk`, and `log_parsed_response` are all strictly true. For streaming/SSE responses, Go stores capped raw-upstream and transformed-client `.sse` sidecar files under `parsed_stream` when `full_request_storage.enabled`, `log_to_disk`, and `log_parsed_stream` are all strictly true. Admin log detail reads these files with a bounded limit, and retention cleanup removes generated stream sidecars. Legacy separate `.log` files are not recreated.

Remaining gap: legacy `.log` files are not recreated; the Go implementation uses full-request JSON plus sidecar metadata/files.

Current workaround: use `/admin/logs/{id}` full request detail and SQLite metadata for parser debugging.

## Responses API Edge Parity

Python had broader OpenAI Responses translator coverage.

What for: it supported more content/usage details when translating between Responses API and Chat Completions.

Why it mattered: clients that depend on token-detail fields, refusal/reasoning details, or exact error object shape may see differences.

How Python worked: the translator mapped token details such as cached/reasoning token details, preserved more non-text history content, and built richer error responses.

Go status: common text, tool calls, streaming events, `store`, `previous_response_id`, SQLite-backed response state, sticky follow-up routing, and detailed usage mapping are implemented. For translated non-stream and stream responses, cached input token details and reasoning output token details are mapped from Chat Completions usage, and OpenAI-compatible `reasoning_content` maps to Responses reasoning output items and reasoning-summary stream events. Translated non-stream terminal mapping now handles `finish_reason: length` as `status: incomplete` with `incomplete_details`, maps `content_filter` to `failed`, and gives `length` precedence when both signals are present. Batch 26 Slice C added Responses `tool_choice` normalization for required/any/specific-tool forms, `service_tier` and `prompt_cache_key` pass-through, safe OpenAI-style error `param`, image content mapping, URL/file citation and file-path annotation regression coverage, Anthropic Messages `image_url` response mapping, and cached-input usage fields for Messages non-stream/stream translation. The regression corpus now also covers refusal/annotation content, empty assistant content, streamed incomplete details, malformed or unknown SSE payloads, Anthropic content block start/stop ordering, interleaved text/tool/text streams, and Messages stop-reason/cache-usage deltas. Known remaining risk is broader provider-specific Responses and Anthropic protocol parity, especially file/audio inputs, hosted tools, retrieve/delete, native provider content, and other rare surfaces.

Current workaround: use Chat Completions for unsupported edge cases or add focused Go translator tests before relying on those fields.

## Parser Edge Parity

Python had a larger parser test corpus for template round-trips and provider-specific chunk boundaries.

What for: parser modules normalize reasoning content and recover tool calls from models that emit informal XML/K2/Qwen-style tags.

Why it mattered: small parser differences can silently change `finish_reason`, drop content, or emit malformed tool-call arguments.

How Python worked: tests covered non-stream/stream parity, fragmented SSE events, start-in-think behavior, custom tags, drop tags, duplicate Qwen function wrappers, tool-buffer fallback, and template-rendered round trips.

Go status: Go implements the main parser modules and many corresponding tests, including invalid literals, fragmented reasoning/tool calls, arbitrary fragmentation, K2/Qwen markers, template-derived `qwen_xml` detection, Qwen start-in-think/no-arg/duplicate repair, trailing text behavior, custom/drop tags, buffer fallback, stream buffer byte caps and max-age caps that flush malformed buffers to literal content/reasoning, explicit reset/close lifecycle hooks for stream parser state, opt-in streamed tool-argument JSON repair with byte/age-bounded raw release, template-derived round trips, bounded Jinja inspector repeat/shuffle corpus coverage, and reasoning/content swap. The full Python parser corpus has not been ported one-for-one, so provider-specific edge parity remains a risk.

Current workaround: keep parser-heavy models behind explicit config, run `task smoke:fake`, and add Go regression tests when a model-specific parser issue is found.
