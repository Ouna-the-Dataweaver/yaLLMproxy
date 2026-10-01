# Go Release Readiness

Last audited: 2026-07-10.

The Go rewrite is suitable for a controlled team rollout after the deployment gates below are met. This pass reviewed the request/runtime core, config mutation lifecycle, translators and provider adapters, forwarders, smoke harness, logging/trace UI, packaging, and dependency posture. It fixed confirmed defects rather than treating existing test volume as proof of readiness.

## What This Audit Fixed

| Area | Corrected behavior |
| --- | --- |
| Admission | A cancellation/promotion race no longer strands a concurrency slot. |
| Runtime config | Mutations are serialized against in-flight requests; legacy model writes validate and apply transactionally; config files use crash-atomic POSIX replacement with a Windows compatibility fallback. |
| Admin boundary | Config and all admin/compatibility log APIs require the configured admin password; comparisons are constant-time. Legacy open config/log access remains only when no password is configured. |
| Client credentials | The proxy's configurable app-key header is consumed locally and removed before every upstream path, including Chat, Messages, Responses, and passthrough. |
| Passthrough | App-key/model/required-header policy, client and backend admission, upstream timeout, and credential stripping now apply without changing arbitrary payload forwarding. |
| Quotas | Restart restoration for rolling token-quota windows excludes expired usage. |
| HTTP/TCP forwarders | Requests/responses stream instead of being fully buffered; encoded responses retain their encoding header; SSE flushes; listener startup is race-safe; TCP half-close no longer truncates the returning response. |
| GigaChat | Concurrent cloud-token refreshes coalesce, waiters honor cancellation, recursive local schema references are bounded, and stream wrappers preserve bytes returned with a terminal source error. |
| Messages/Responses streams | Adapters no longer discard valid bytes when an upstream read returns both data and an error. |
| Log outcomes | Non-stream Chat, Messages, and Responses records derive canonical stop/tool outcomes consistently. |
| Admin UI | Logs now have real filters and pagination plus a structured request inspector for messages, tools, responses, usage, attempts, warnings, headers, raw JSON, and Trace-to-Request navigation. Passwords are session-only. |
| Distribution | The production React bundle is committed and embedded in the Go binary, so `/admin` no longer depends on the process working directory or a runtime Node install. |
| Toolchain | The minimum Go patch level is 1.26.5, which contains the standard-library TLS fix for [GO-2026-5856](https://pkg.go.dev/vuln/GO-2026-5856) missing from 1.26.4. |

## Verification From This Pass

- `task audit` passed on Go 1.26.5: locked frontend install/build and committed-bundle check, `go vet ./...`, `go test ./...`, `go test -race ./...`, and fake smoke.
- `task audit:vuln` reported no called-code vulnerabilities after the toolchain upgrade.
- Real smoke passed for the available `glm_local` configuration, including non-stream/stream traffic and temporary log/sidecar verification.
- Windows amd64 cross-compilation passed for the config package and main command.
- `npm audit` reported no frontend dependency vulnerabilities.
- `task test:postgres` was invoked but skipped because `YALLMP_TEST_POSTGRES_DSN` was not configured.

## Required Deployment Gates

1. Use Go 1.26.5 or newer in the 1.26 line.
2. Set a strong `YALLMP_ADMIN_PASSWORD` for every shared or non-loopback deployment. An unset value intentionally preserves legacy-open masked config and log APIs and is not a production-safe mode.
3. Run `task audit` from a clean checkout. It rebuilds and checks the embedded UI, runs vet, normal and race-enabled Go tests, and runs deterministic fake smoke.
4. Run `task audit:vuln` with network access and address any called-code finding before rollout.
5. Run `task smoke:real -- --config /path/to/team/config.yaml --model <model>` against every provider/model class the team will initially enable.
6. If PostgreSQL is used, run `YALLMP_TEST_POSTGRES_DSN=... task test:postgres` against a disposable database.
7. Back up the config and metadata database, restrict sidecar/log paths, and canary the Go proxy with a small caller group before replacing the Python service.

Config reloads wait for active requests using the old runtime to finish so that routers, databases, caches, and telemetry providers are not closed underneath streams. Plan reloads around unusually long-lived requests.

## Remaining Known Risks

- The Go GigaChat client still lacks a custom CA-bundle option; it supports system roots, mTLS client certificates, and the development-only `verify_ssl: false` escape hatch.
- Live PostgreSQL, GigaChat cloud/mTLS, and external OTLP collector integrations are environment-gated and are not covered by the default local audit.
- The frontend has build/type validation but no browser E2E suite. Unusual provider payloads remain accessible through raw JSON, but interactive regressions need manual or future Playwright coverage.
- Streamed database rows do not yet derive canonical terminal stop reasons when persistence receives usage but no terminal response body.
- The HTTP forwarder does not fully proxy trailers; per-connection TCP forwarding errors are intentionally silent to keep the byte path transparent.
- Full-request sidecar writes are best-effort. Failures do not break inference traffic, but there is no operator-visible failure counter yet.
- Several non-stream proxy/translation paths intentionally accept and buffer complete request or response bodies. There is no global byte limit; use a trusted ingress with appropriate body/concurrency limits when exposing the service broadly.
- Provider-specific Responses, Messages, parser, and GigaChat long-tail surfaces still require focused regression tests before the team depends on a new field or content type.

## Rollout Recommendation

Start with one Go canary using the same upstreams and a distinct log database/sidecar directory. Compare status, latency, token usage, stop reason, tool-call behavior, and streamed output against the Python service for representative requests. Expand only after the real smoke and team-specific parser/tool corpus are clean; keep rollback to the Python listener available during the first rollout window.
