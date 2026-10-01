# Project Structure

## Top Level

```text
yaLLMproxy/
├── cmd/
│   ├── yallmp/                # Main proxy server
│   ├── yallmp-db-clean/       # Log database cleanup CLI
│   ├── yallmp-forwarder/      # HTTP reverse forwarder
│   ├── yallmp-smoke/          # Fake/real smoke harness
│   └── yallmp-tcp-forwarder/  # TCP forwarder
├── internal/
│   ├── admission/             # Concurrency, priority, RPS/RPM admission
│   ├── catalog/               # Read-only model catalog snapshots, probes, and warnings
│   ├── config/                # YAML/env config loading, validation, masking, inheritance
│   ├── forwarder/             # HTTP and TCP forwarder implementations
│   ├── gigachat/              # Native GigaChat client, auth, and response conversion
│   ├── logdb/                 # SQLite/PostgreSQL metadata, analytics, response state, cleanup
│   ├── logstore/              # Full request/response sidecar files
│   ├── messages/              # Anthropic Messages translation and streaming
│   ├── modules/               # Response parser modules
│   ├── policy/                # App-key auth and model access policy
│   ├── proxy/                 # Backend model, routing, forwarding, SSE helpers
│   ├── quota/                 # Token quota accounting
│   ├── responses/             # Responses API translation/state/streaming
│   └── server/                # HTTP app and route handlers
├── web/admin/                 # React source plus committed/embedded production bundle
├── static/                    # Logo and legacy static assets
├── configs/                   # Config examples and Jinja templates
├── docs/                      # Go branch documentation
├── certs/                     # Generated local certificates
├── go.mod
├── go.sum
├── Taskfile.yml
└── README.md
```

## Commands

| Path | Purpose |
| --- | --- |
| `cmd/yallmp` | Loads config, starts the Go proxy, optional health checks, optional in-process forwarders |
| `cmd/yallmp-db-clean` | Deletes or previews old request/error metadata |
| `cmd/yallmp-forwarder` | Starts the HTTP reverse forwarder as a standalone process |
| `cmd/yallmp-tcp-forwarder` | Starts the TCP byte forwarder as a standalone process |
| `cmd/yallmp-smoke` | Runs fake or real smoke tests without leaving a persistent server |

## Core Packages

| Package | Purpose |
| --- | --- |
| `internal/server` | HTTP route dispatch, admin APIs, request validation, logging integration, usage/health |
| `internal/proxy` | Backend definitions, outbound header/body building, route selection, fallback, forwarding |
| `internal/config` | Config structs, env substitution, YAML persistence, inheritance, validation |
| `internal/catalog` | Read-only catalog snapshots, provider `/v1/models` probes, and advisory warnings |
| `internal/gigachat` | Native GigaChat cloud/local auth, chat conversion, streaming, and error normalization |
| `internal/policy` | App-key authentication, group/key model access, merged per-key limits |
| `internal/admission` | Concurrency queue, priorities, RPS/RPM token buckets, metrics |
| `internal/quota` | Token quota reservations, commits, rollback, windowing, metrics |
| `internal/observability` | OpenTelemetry provider setup, no-op/in-memory tracing, and sanitized span helpers |
| `internal/modules` | Response parsing and rewriting for reasoning/tool-call normalization |
| `internal/messages` | Anthropic Messages request/response translators and stream adapter |
| `internal/responses` | Responses API translators, stream adapter, and response state store |
| `internal/logdb` | SQLite/PostgreSQL schema, request metadata, error logs, analytics, response states |
| `internal/logstore` | Full request sidecar JSON storage and retention cleanup |
| `internal/forwarder` | HTTP reverse proxy and TCP forwarding logic |

## Request Flow

```text
client
  -> internal/server route handler
  -> internal/policy auth and access checks
  -> internal/admission concurrency/RPS/RPM checks
  -> internal/quota token reservation
  -> internal/proxy route and backend forwarding
  -> internal/modules response transforms when configured
  -> internal/logdb and internal/logstore persistence
  -> client response
```

## Tests

Go tests live next to the package they exercise as `*_test.go`.

Run all tests:

```bash
task test
```

Smoke harness:

```bash
task smoke:fake
task smoke:real
```
