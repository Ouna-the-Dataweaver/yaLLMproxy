# Go Release Readiness

Last audited: 2026-11-06. The July audit below is retained as historical evidence;
its live-provider and dependency results must not be treated as current checks.

The Go rewrite is suitable for a controlled team rollout after the deployment gates below are met. This pass reviewed the request/runtime core, config mutation lifecycle, translators and provider adapters, forwarders, smoke harness, logging/trace UI, packaging, and dependency posture. It fixed confirmed defects rather than treating existing test volume as proof of readiness.

## November Review

The review covered the working tree as well as cancellation, admission, multi-node
routing, stream framing and translation, request logs, quota restoration, and
the admin usage display. Confirmed defects were fixed with regression coverage:

- Canceled requests stop retries and fallback attempts without penalizing node
  health. Queued cancellation bursts retain all admission capacity.
- Fractional request rates can admit one request and refill at the configured
  rate instead of permanently rejecting traffic.
- SSE framing handles fragmented events and CR/LF boundaries. Translated streams
  drain buffered output, emit failure events on transport/protocol errors, and
  finish on `[DONE]` without waiting for the upstream connection to close.
- Engine adapters copy nested parameter defaults before rewriting them. Node
  fallback and explicit rank routing preserve the original request for each
  attempt; engine profiles isolate cache entries.
- Stream read, write, and protocol failures are logged as errors even when the
  already-sent HTTP status is 200. Canceled requests persist logs with their trace
  context under a separate bounded deadline, and queue rejections are logged.
- Early stream closure finalizes failed Responses state before storage callbacks.
  Observed usage on interrupted streams remains chargeable after quota reload.
- HTTP errors returned before Messages/Responses streaming begins stay valid
  API-specific JSON, with upstream status and retry headers preserved. Logs retain
  the original streaming request flag and admission slots are released.
- Cached-token reporting distinguishes absent usage from explicit zero and
  preserves native Anthropic cache-read counts.

The fetched main revision `e728ab2` contained newer Python-side changes that
would otherwise have been lost during promotion. Their Go equivalents now
cover custom GigaChat CA trust for OAuth/inference and mTLS, nullable and scalar
tool schemas, legacy function calls and history, unsupported schema formats,
stream startup retry, JSON HTTP error responses, and trailing stream usage.
Retries are limited to failures before converted stream output is available.
The default-on master logging switch now overrides request logging sinks while
leaving Responses application state available; GigaChat diagnostics honor that
switch and redact credentials and request text.

Main revision `e728ab2` was merged into the Go branch after these ports. Config
conflicts retain the reviewed Go examples, including the CA option. Obsolete
Python source and tests remain removed, and main is an ancestor of the reviewed
branch. This review did not push, merge into main, or deploy a service.

The initial Go vulnerability scan reported 25 called-code advisories. The minimum
Go version is now 1.26.10; affected networking, text, gRPC, and OpenTelemetry
dependencies were upgraded and module checksums verified. `task audit:vuln`
now reports zero called-code findings. It still notes one advisory in imported
packages and two in required modules whose vulnerable symbols are not called.
The frontend's four high-severity build dependency advisories were resolved with
compatible upgrades; `npm audit` now reports zero advisories.

Final local verification passed on Go 1.26.10:

- `task audit`: locked frontend install, four frontend regressions, typecheck and
  production build, committed-bundle consistency, Go vet, `task test`,
  `task test:race`, and deterministic fake-upstream smoke.
- `task audit:vuln`: zero called-code findings after reconciling main.
- `go mod verify` and `git diff --check`.
- The fetched main commit is an ancestor of the reviewed branch; its merge
  conflicts have been resolved and obsolete Python files were not restored.

No remaining code or merge blocker was identified in the reviewed paths. This
supports promotion to main; production rollout still requires the deployment
gates below. No persistent test service was left running.

Live provider, PostgreSQL, and browser E2E checks were not run in this review.
`YALLMP_TEST_ENGINE_URLS` and `YALLMP_TEST_POSTGRES_DSN` were not configured.
The earlier audit's integration results are not assumed to cover these changes.

## July Audit Fixes (Historical)

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

## July Verification (Historical)

- `task audit` passed on Go 1.26.5: locked frontend install/build and committed-bundle check, `go vet ./...`, `go test ./...`, `go test -race ./...`, and fake smoke.
- `task audit:vuln` reported no called-code vulnerabilities after the toolchain upgrade.
- Real smoke passed for the available `glm_local` configuration, including non-stream/stream traffic and temporary log/sidecar verification.
- Windows amd64 cross-compilation passed for the config package and main command.
- `npm audit` reported no frontend dependency vulnerabilities.
- `task test:postgres` was invoked but skipped because `YALLMP_TEST_POSTGRES_DSN` was not configured.

## Required Deployment Gates

1. Use Go 1.26.10 or newer in the 1.26 line.
2. Set a strong `YALLMP_ADMIN_PASSWORD` for every shared or non-loopback deployment. An unset value intentionally preserves legacy-open masked config and log APIs and is not a production-safe mode.
3. Run `task audit` from a clean checkout. It rebuilds and checks the embedded UI, runs vet, normal and race-enabled Go tests, and runs deterministic fake smoke.
4. Run `task audit:vuln` with network access and address any called-code finding before rollout.
5. Run `task smoke:real -- --config /path/to/team/config.yaml --model <model>` against every provider/model class the team will initially enable.
6. If PostgreSQL is used, run `YALLMP_TEST_POSTGRES_DSN=... task test:postgres` against a disposable database.
7. Back up the config and metadata database, restrict sidecar/log paths, and canary the Go proxy with a small caller group before replacing the Python service.

Config reloads swap runtime generations without waiting for active streams. Old
databases, caches, and telemetry providers close after the last request holding
that generation finishes. Existing streams retain their original runtime.

## Remaining Known Risks

- Live PostgreSQL, GigaChat cloud/mTLS, and external OTLP collector integrations are environment-gated and are not covered by the default local audit.
- The frontend has build/type validation but no browser E2E suite. Unusual provider payloads remain accessible through raw JSON, but interactive regressions need manual or future Playwright coverage.
- Streamed database rows do not yet derive canonical terminal stop reasons when persistence receives usage but no terminal response body.
- The HTTP forwarder does not fully proxy trailers; per-connection TCP forwarding errors are intentionally silent to keep the byte path transparent.
- Full-request sidecar writes are best-effort. Failures do not break inference traffic, but there is no operator-visible failure counter yet.
- Several non-stream proxy/translation paths intentionally accept and buffer complete request or response bodies. There is no global byte limit; use a trusted ingress with appropriate body/concurrency limits when exposing the service broadly.
- Provider-specific Responses, Messages, parser, and GigaChat long-tail surfaces still require focused regression tests before the team depends on a new field or content type.

## Rollout Recommendation

Start with one Go canary using the same upstreams and a distinct log database/sidecar directory. Compare status, latency, token usage, stop reason, tool-call behavior, and streamed output against the Python service for representative requests. Expand only after the real smoke and team-specific parser/tool corpus are clean; keep rollback to the Python listener available during the first rollout window.
