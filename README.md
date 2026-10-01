# y(et) a(nother) LLM proxy (yaLLMproxy)

<div align="center">
  <img src="static/logo.png" alt="yaLLMproxy Logo" width="200">
</div>

yaLLMproxy is a Go LLM proxy for OpenAI-compatible traffic, local model fleets, and operator-facing observability. It focuses on transparent request forwarding while adding routing, fallback, parser, quota, and logging controls.

## Features

- OpenAI-compatible `/v1/chat/completions`, `/v1/embeddings`, `/v1/rerank`, and `/v1/models`.
- Optional Anthropic Messages API at `/v1/messages`, with OpenAI backend translation, thinking-block mapping, or native Anthropic pass-through.
- Optional OpenAI Responses API at `/v1/responses`, with Chat Completions translation, reasoning output/stream mapping, `store`, `previous_response_id`, sticky follow-up routing, and selected terminal-status mapping.
- Native GigaChat backend support with cloud OAuth, local mTLS, streaming conversion, and OpenAI tool-call translation.
- Legacy `model_list` config support plus provider/node/client-model config shapes.
- Backend fallback, retryable-status failover, health snapshots, active health probes, score-aware routing, default prompt-prefix affinity, opt-in least-in-flight routing, and admin-managed fallback chains.
- Read-only admin model catalog with disabled-by-default provider `/v1/models` availability probes.
- Round-robin, weighted round-robin, node access control, and sticky routing for conversation/follow-up identifiers.
- App-key policy with flat/grouped keys, model access, unauthenticated limits, per-key/group limits, token quotas with rolling or UTC calendar resets, opt-in backend quota-aware routing, and admin UI controls.
- Disabled-by-default exact and semantic inference cache layers with bounded counters and request-level bypass controls.
- Disabled-by-default Prometheus metrics and OpenTelemetry request tracing/metrics with bounded, secret-safe labels, including backend health/quota/in-flight gauges, optional Prometheus timing histograms, GenAI-style span, token-usage, stream TTFT/TPOT, parser, and logging phase attributes.
- Response parser modules for reasoning and tool-call normalization across non-stream and SSE responses, including marker-derived Qwen XML tool-call parsing, bounded stream parser buffers, and opt-in streamed tool-argument JSON repair.
- SQLite/PostgreSQL request metadata logging, full request/response sidecars including parsed response/stream artifacts, admin log detail APIs, token usage extraction, and cleanup helpers.
- Built-in Go TCP forwarder and HTTP reverse forwarder.
- React admin frontend source under `web/admin`, including Observability, Catalog, Routing, Logs, App Keys, and read-only Traces views.

Known Go-vs-Python gaps are tracked in [docs/go_feature_gaps.md](docs/go_feature_gaps.md).

## Documentation Map

- [docs/api.md](docs/api.md), [docs/configuration.md](docs/configuration.md), [docs/database.md](docs/database.md), [docs/testing.md](docs/testing.md), and [docs/project-structure.md](docs/project-structure.md) are the maintained user/operator docs.
- [docs/release-readiness.md](docs/release-readiness.md) records the 2026-07-10 audit, required deployment gates, and remaining risks for the Go rollout.
- [state.md](state.md) is the current Go refactor status snapshot, including post-pass branch notes.
- [feature_list_go.md](feature_list_go.md) is the feature backlog and batch status tracker; as of 2026-07-09 the last completed feature pass is Batch 28.
- [journal.md](journal.md) is the chronological work log. It is useful for resuming context but is not a concise product doc.
- [go_refactor.md](go_refactor.md) and [shared_core.md](shared_core.md) are historical/planning notes. Treat maintained docs and code as authoritative before following those plans.
- `.codex-upgrade/` is an ignored local archive of feature-batch planning and review artifacts; it is intentionally not tracked.

## Development Status

The Go-only `codex/go_refactor` line contains the completed Batch 28 feature pass and the later admin-password/UI follow-up. The newer `codex/gigachat-ca-cert-followup` branch is based on the old Python line, not the Go refactor; its custom GigaChat CA-bundle change has not been ported to Go. See [state.md](state.md) for the concise done/remaining snapshot and [journal.md](journal.md) for the chronological integration record.

## Prerequisites

- Go 1.26.5 or newer in the 1.26 line, as required by `go.mod`. The repo also supports the bundled `.tools/go/bin/go` fallback and Go's automatic patch-toolchain selection used by `Taskfile.yml`.
- [go-task](https://taskfile.dev/) for common commands.
- Optional: Node.js/npm for building the React admin frontend.
- Optional: mkcert for local forwarder certificates.

## Simple model routing

For new setups, use [the simple routing guide](docs/simple-routing.md) and
[configs/config_simple_example.yaml](configs/config_simple_example.yaml). Add nodes
with the same public model name to form a pool automatically, or select several
models on a provider to share its credentials. The admin **Models & Nodes** page
supports provider discovery, selection, and a view of the resulting pools.
Conversation and prompt-cache affinity precede balancing; retryable failures try
other eligible nodes in the pool. Existing configurations remain supported.

## Quick Start

```bash
task install
cp -n configs/config_simple_example.yaml configs/config.yaml
# Edit the endpoint URLs and model IDs for your setup.
# Add secrets to configs/.env if your config references ${...} values.
task run
```

The default server address comes from `proxy_settings.server` in `configs/config.yaml`. `YALLMP_HOST`, `YALLMP_PORT`, and `YALLMP_CONFIG` can override runtime settings.

```bash
curl http://localhost:7979/v1/models

curl http://localhost:7979/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"qwen","messages":[{"role":"user","content":"Hello"}]}'
```

## Commands

| Command | Description |
| --- | --- |
| `task run` | Run the Go proxy server |
| `task run:reload` | Run the Go proxy server; autoreload is not implemented yet |
| `task test` | Run all Go tests |
| `task test:race` | Run all Go tests with the race detector |
| `task smoke:fake` | Run fake client/upstream smoke tests and verify logging |
| `task smoke:real` | Run real local-model smoke test; defaults to `glm_local` and external config path |
| `task audit` | Run vet, normal/race tests, fake smoke, and a locked frontend build |
| `task audit:vuln` | Scan called Go code against the Go vulnerability database |
| `task forwarder` | Run the Go TCP forwarder |
| `task forwarder:http` | Run the Go HTTP reverse forwarder |
| `task frontend:install` | Install React admin frontend dependencies |
| `task frontend:dev` | Run the Vite admin frontend dev server |
| `task frontend:build` | Build the React admin frontend into `web/admin/dist` |
| `task db-clean-dry-run` | Preview request/error log cleanup |
| `task db-clean` | Clean request/error logs |

Real smoke defaults:

```bash
task smoke:real -- --config /home/theio/repos/nidai_db/infra/yaLLMproxy/configs/config.yaml --model glm_local
```

The smoke command runs the Go app in-process through HTTP and uses a temporary SQLite DB/request directory, so it does not leave a persistent server running.

## Configuration

Config lives in `configs/config.yaml`; env placeholders are loaded from the matching `.env` file next to the config. The proxy accepts the legacy `model_list` format:

```yaml
model_list:
  - model_name: glm_local
    protected: false
    model_params:
      api_base: http://localhost:8000/v1
      model: local-upstream-model
      api_type: openai
      request_timeout: 120
      supports_reasoning: true   # Display metadata only; does not force thinking
      parameters:
        stream_options.include_usage:
          default: true
          allow_override: false

proxy_settings:
  server:
    host: 0.0.0.0
    port: 7979
  enable_messages_endpoint: true
  enable_responses_endpoint: false
  otel:
    enabled: false
    endpoint: ""
  inference_cache:
    mode: off
    ttl_seconds: 300
    max_age_seconds: 300
    max_entries: 1024
    max_entry_bytes: 1048576
  model_catalog:
    enabled: false
    probe_nodes: []
    probe_providers: []
  logging:
    full_request_storage:
      enabled: true
      path: logs/requests
      retention_hours: 48

database:
  backend: sqlite
  connection:
    sqlite:
      path: logs/yaLLM.db
```

PostgreSQL log storage is also supported with `database.backend: postgres` or `postgresql`; see [docs/database.md](docs/database.md).

Provider/node/client-model config is also supported for multi-node routing, weighted balancing, model groups, and GigaChat provider settings. See [docs/configuration.md](docs/configuration.md).

When exact inference caching is enabled, callers can send `x-yallmp-cache-bypass: true` to skip cache read/write for a single request; the internal header is removed before forwarding upstream.

`GET /admin/catalog` returns a read-only model/provider/node catalog with bounded advisory warnings. Optional provider `/v1/models` probes are disabled unless `proxy_settings.model_catalog.enabled` is true and nodes or providers are explicitly listed in `probe_nodes` or `probe_providers`; probe results and warnings never change routing. The React admin Catalog tab renders this snapshot.

## Admin And Logs

Set `YALLMP_ADMIN_PASSWORD` for every shared or non-loopback deployment. When it is configured, config and log APIs—including compatibility `/api/logs` aliases—require `x-admin-password`; the React UI keeps the credential in session storage only. Leaving it unset preserves legacy open access to masked config and log APIs for local compatibility.

- `GET /admin/config` returns masked config.
- `PUT /admin/config` validates, preserves missing secrets, persists YAML, and rebuilds runtime state.
- `POST /admin/config/validate` returns structured validation issues.
- `POST /admin/config/reload` reloads config from disk.
- `POST /admin/models` registers a model and persists it when a config path is known.
- `GET/POST /admin/templates` and `GET /admin/templates/inspect` list, upload, and inspect Jinja templates under `configs/jinja_templates`, including bounded parser-config inference evidence and warnings.
- `GET /admin/logs` lists persisted request metadata when database logging is enabled.
- `GET /admin/logs/{id}` returns decoded full request/response detail when sidecar storage is enabled.
- `GET /admin/traces/{trace_id}` returns sanitized metadata-only trace navigation.
- `GET /admin/backend-quotas` returns admin-protected configured backend quota snapshots.
- `GET /admin/logs/stop-reasons`, `/tool-call-rate`, and `/models` expose analytics.
- `GET /v1/usage` exposes admission/quota metrics.
- `GET /v1/health` exposes backend health snapshots.
- `GET /metrics` exposes disabled-by-default Prometheus metrics when enabled.

The committed production bundle is embedded into the Go binary and served from `/admin`; `task frontend:build` refreshes it after frontend source changes.

Compatibility log aliases are available under `/api/logs`. Legacy static `/logs` and `/usage` pages are still served from `static/admin`, but the maintained UI is `/admin` and usage JSON is `/v1/usage`.

## Forwarders

The TCP forwarder is available with `task forwarder`; the HTTP reverse forwarder is available with `task forwarder:http`. Both can also be enabled in-process from config.

Common HTTP forwarder settings:

```yaml
http_forwarder_settings:
  enabled: false
  preserve_host: true
  listen:
    host: 0.0.0.0
    port: 6969
  target:
    scheme: http
    host: 127.0.0.1
    port: 7979
```

## Testing

```bash
task test
task test:postgres
task smoke:fake
task smoke:real
```

`task test:postgres` skips unless `YALLMP_TEST_POSTGRES_DSN` points at a disposable PostgreSQL database.

`task smoke:fake` verifies request forwarding, model rewrite for chat/embeddings/rerank, stream usage propagation, passthrough fidelity, daily quota reset visibility from `/v1/usage`, in-memory OTel span/metric emission including parser/logging phases and stream TTFT/TPOT, opt-in streamed tool-argument JSON repair, translated Responses usage/reasoning/tool/terminal stream behavior, translated Messages thinking/tool stream behavior, Qwen template parser inspection, token logging, backend-name logging, exact-cache bypass behavior, and full request sidecars including parsed response/stream artifacts against an emulated upstream.

`task smoke:real` loads the external config path, defaults to `glm_local`, sends non-stream and stream requests through the Go proxy, and verifies logs using a temporary smoke database.

## License

MIT
