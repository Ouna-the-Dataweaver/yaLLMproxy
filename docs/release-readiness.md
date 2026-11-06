# Release Readiness

Use this checklist for each deployment. Local tests cover deterministic fake
upstreams; they do not establish compatibility with a team's live providers,
PostgreSQL deployment, or external telemetry collector.

## Deployment Checks

1. Use Go 1.26.10 or newer in the 1.26 line, as required by `go.mod`.
2. Set a strong `YALLMP_ADMIN_PASSWORD` for every shared or non-loopback deployment.
   An unset value preserves legacy-open masked config and log APIs.
3. Run `task audit` from a clean checkout. It checks the embedded frontend bundle,
   runs frontend tests, Go vet, normal and race-enabled Go tests, and fake smoke.
4. Run `task audit:vuln` with network access and address called-code findings.
   Run `npm audit` in `web/admin` for frontend dependencies.
5. Run `task smoke:real -- --config /path/to/config.yaml --model <model>` against
   every provider/model class being enabled. Validate representative tool calls,
   parser settings, cancellation, and streamed output with that provider.
6. If PostgreSQL is used, run `task test:postgres` with
   `YALLMP_TEST_POSTGRES_DSN` pointing at a disposable database.
7. Back up configuration and metadata, restrict access to logs and sidecars, and
   canary a small caller group before expanding traffic. Keep the prior binary
   and compatible config/database backups available for rollback.

See [testing](testing.md) for test commands and environment-gated integrations.

## Routing, Queues, and Reloads

Routing state, admission scopes, priority queues, and live quota accounting are
local to one proxy process. Multiple proxy replicas do not share a centralized
queue or quota budget. Limits must account for the number of proxy replicas.

Lower numeric priorities run first, with FIFO ties; strict priority can starve
lower-priority traffic. Queue timeouts apply per admission attempt, not to the
complete sequence of replicas and retries. Availability checks are advisory
snapshots, so concurrent arrivals can still queue. Concurrency-aware selection
compares active-attempt counts, not utilization normalized by capacity. Set
explicit weights for unequal nodes; GPU capacity is not inferred automatically.

Config reloads swap runtime generations without waiting for active streams.
Existing streams retain their original runtime. Old databases, caches, and
telemetry providers close after the last request using that generation finishes.
Live quota accounting is retained across reloads. Changes to process-level
console logging suppression require a restart.

## Known Limitations

- Live PostgreSQL, GigaChat cloud/mTLS, engine, and external OTLP integrations
  require configured environments and are not covered by the default local audit.
- The frontend has regression tests, typechecking, and build validation, but no
  browser E2E suite. Exercise critical admin workflows before deployment.
- Streamed database rows do not always derive canonical terminal stop reasons
  when persistence receives usage without a terminal response body.
- Full-request sidecar writes are best-effort, with no operator-visible failure
  counter. Protect and monitor storage separately. Sidecars use JSON and related
  artifacts; legacy text `.log` files are not generated.
- Several non-stream paths buffer complete request or response bodies. There is
  no global byte limit; configure appropriate ingress body and concurrency limits.
- Responses and Messages translation supports common text, reasoning, image,
  tool, and usage flows, but does not implement every provider protocol surface.
  Validate file/audio inputs, hosted tools, native content, and other uncommon
  fields before relying on them. Responses retrieve/delete is not implemented.
- Provider-specific parser behavior needs representative regression samples.
  The bounded Jinja renderer does not implement full macro/import/include support;
  template inspection is for parser configuration, not production prompt rewriting.
- Exact caching excludes streams and native provider passthrough. Semantic
  caching is limited to non-stream Chat Completions and an in-memory vector
  index; it does not support persistence or translated Messages/Responses.
- The HTTP forwarder does not fully proxy trailers. Per-connection TCP forwarding
  errors are intentionally silent.

Roll out with a distinct log database and sidecar directory for the canary.
Compare status, latency, usage, tool-call behavior, and streamed output against
representative traffic before expanding.
