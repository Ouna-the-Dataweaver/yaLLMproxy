# Configuration Guide

Complete reference for yaLLMproxy configuration options.

## Table of Contents

- [Configuration Files](#configuration-files)
- [Full Configuration Reference](#full-configuration-reference)
- [Model Configuration](#model-configuration)
  - [Required Fields](#required-fields)
  - [Optional Fields](#optional-fields)
  - [Parameter Overrides](#parameter-overrides)
  - [Model Inheritance](#model-inheritance)
- [Response Modules](#response-modules)
  - [Available Modules](#available-modules)
  - [Per-Model Module Overrides](#per-model-module-overrides)
  - [Template-Based Parsing](#template-based-parsing)
- [Environment Variables](#environment-variables)
- [App Key Authentication](#app-key-authentication)
- [Router Settings](#router-settings)
- [Logging Configuration](#logging-configuration)
- [Template-Derived Parser Config](#template-derived-parser-config)
- [Configuration Validation](#configuration-validation)
- [Hot Reloading](#hot-reloading)

## Configuration Files

yaLLMproxy uses a single configuration file:

| File | Purpose |
|------|---------|
| `configs/config.yaml` | Unified configuration and model list |

Environment variables are loaded from:
- `configs/.env` - Environment variables referenced in config

## Full Configuration Reference

```yaml
model_list:
  - model_name: GLM-4.6-nano              # Unique model identifier
    protected: true
    model_params:
      api_type: openai                     # API type: openai (default), anthropic, gigachat, or passthrough
      model: z-ai/glm-4.6:thinking         # Actual model name
      api_base: https://nano-gpt.com/api/v1thinking  # API base URL
      api_key: ${NANOGPT_API_KEY}          # API key (from .env)
      supports_reasoning: true             # Display metadata only; does not force thinking
      request_timeout: 540                 # Request timeout (seconds)
      parameters:                          # Parameter override config
        temperature:
          default: 1.0
          allow_override: false            # Allow request override
        top_p:
          default: 0.95
          allow_override: false
    modules:                               # Response module config
      upstream:
        enabled: true
        response:
          - parse_tags
          - swap_reasoning_content
          - stream_json_repair
        parse_tags:
          parse_thinking: true
          parse_tool_calls: true
          think_tag: "think"
          tool_arg_format: "xml"           # xml | json (for K2-style)
          tool_tag: "tool_call"            # For xml format
        swap_reasoning_content:
          mode: "reasoning_to_content"     # reasoning_to_content | content_to_reasoning | auto
          think_tag: "think"
          think_open:
            prefix: ""
            suffix: ""
          think_close:
            prefix: ""
            suffix: ""
          include_newline: true
        stream_json_repair:
          enabled: false                  # Explicit opt-in; repairs only streamed tool/function argument JSON fragments
          max_stream_buffer_bytes: 1048576 # Shared cap for pending repair buffers
          max_stream_buffer_age_seconds: 0 # Optional age cap for pending repair buffers; 0 disables
          on_failure: passthrough         # passthrough | empty_object

router_settings:
  num_retries: 1                           # Retry attempts per backend
  fallbacks:                               # Fallback model config
    - primary_model: [fallback1, fallback2]
  health_checks:
    enabled: false                         # Periodically probe upstreams
    path: /models
    interval_seconds: 15
    timeout_seconds: 2
    failure_threshold: 2
    unhealthy_cooldown_seconds: 30
  health_scoring:
    enabled: false                         # Sort/filter fallback route by success ratio
    unhealthy_threshold: 0.3               # Skip lower scores when alternatives exist
    failure_threshold: 2
    cooldown_seconds: 30
  quota_aware_selection:
    enabled: false                         # Opt-in: prefer backends with token-quota capacity
    persist_backend_quota: false           # Requires enabled: true; restore backend::<name> quota state from log DB
  prefix_cache_affinity:
    enabled: true                          # Default: prefer nodes that recently served the same shallow message prefix
    max_entries: 4096                      # Bounded in-memory (node, prefix-hash) binding count
  concurrency_aware_selection:
    enabled: false                         # Opt-in: fresh requests prefer the least in-flight eligible node

proxy_settings:
  server:
    host: 127.0.0.1                        # Listen address
    port: 7979                             # Listen port
  enable_responses_endpoint: false         # Enable /v1/responses endpoint
  inference_cache:
    mode: off                              # off, read_write, read_only, write_only
    ttl_seconds: 300                       # Hard lifetime for in-memory entries
    max_age_seconds: 300                   # Maximum response age accepted on lookup
    max_entries: 1024                      # Bounded in-memory entry count
    max_entry_bytes: 1048576               # Skip responses larger than this
    semantic:
      enabled: false                       # Opt-in semantic recall for non-stream chat completions
      embedding_node: ""                   # Required when enabled; must name a configured node
      embedding_model: ""                  # Required embedding model sent to the embedding node
      similarity_threshold: 0.95           # Cosine threshold; omitted/0 uses 0.95
      top_k: 5                             # Bounded candidate count for vector search
      max_entries: 256                     # Bounded in-memory semantic entry count
      max_entry_bytes: 1048576             # Skip semantic entries larger than this
    persistence:
      enabled: false                       # Opt-in SQLite persistence for exact cache entries
      path: logs/inference_cache.db        # Operators must protect this file path
      flush_mode: writethrough             # writethrough/read_write, read_only, write_only, off
  model_catalog:
    enabled: false                         # Enable read-only catalog and optional probes
    probe_interval_seconds: 300            # Probe cadence for opted-in nodes/providers
    probe_timeout_seconds: 5               # Per-node /v1/models timeout
    failure_backoff_seconds: 60            # Retry delay after failed probes
    cache_ttl_seconds: 1800                # Mark old probe data stale after this age
    probe_providers: []                    # Provider names explicitly opted into probes
    probe_nodes: []                        # Node names explicitly opted into probes
  profiling:
    enabled: false                         # Enable guarded /debug/pprof endpoints
  metrics:
    enabled: false                         # Enable guarded /metrics snapshot endpoint
    timings:
      enabled: false                       # Opt-in Prometheus request/backend/stream timing histograms
  otel:
    enabled: false                         # Enable OpenTelemetry request spans
    service_name: yallmp-proxy             # Optional service.name resource attribute
    exporter: otlp_grpc                    # Supported exporter: otlp_grpc
    endpoint: ""                           # host:port or absolute http/https OTLP endpoint
    insecure: false                        # Use insecure gRPC transport for host:port endpoints
    headers:                               # Optional OTLP exporter headers; values can use env refs
      x-otlp-token: ${OTEL_TOKEN}
    logs:
      enabled: false                       # Emit sanitized proxy.log span events; requires otel.enabled
  logging:
    log_parsed_response: true              # Store raw/transformed non-stream JSON response fields when response modules transform output
    log_parsed_stream: false               # Store capped raw/transformed SSE sidecars when response modules transform streams
  modules:
    upstream:
      enabled: false                       # Enable upstream response modules
      response:
        - parse_tags
        - swap_reasoning_content
        - stream_json_repair
      paths:
        - /chat/completions                # Paths to apply upstream modules
      parse_tags:
        parse_thinking: true
        parse_tool_calls: true
        think_tag: "think"
        tool_arg_format: "xml"             # xml | json (for K2-style)
        tool_tag: "tool_call"              # For xml format
      swap_reasoning_content:
        mode: "reasoning_to_content"
        think_tag: "think"
        think_open:
          prefix: ""
          suffix: ""
        think_close:
          prefix: ""
          suffix: ""
        include_newline: true
      stream_json_repair:
        enabled: false                     # Explicit opt-in; only streamed tool/function arguments are considered
        max_stream_buffer_bytes: 1048576   # Shared cap for pending repair buffers
        max_stream_buffer_age_seconds: 0   # Optional age cap for pending repair buffers; 0 disables
        on_failure: passthrough            # passthrough | empty_object
    downstream:
      enabled: false                       # Enable downstream request modules
      request: []

forwarder_settings:
  enabled: false                           # Set true to run TCP forwarder in-process
  bufsize: 65536
  listen:
    host: 0.0.0.0                          # Forwarder listen address
    port: 6969                             # Forwarder listen port
  target:
    host: 127.0.0.1                        # Proxy host
    port: 7979                             # Proxy port
  accept_timeout_seconds: 1
  dial_timeout_seconds: 10

http_forwarder_settings:
  preserve_host: true
  debug: false                       # Enable debug logging
  timeout_seconds: 300               # Request timeout (optional, no timeout if not set)
  listen:
    host: 0.0.0.0
    port: 6969
  target:
    scheme: http
    host: 127.0.0.1
    port: 7979
  ssl:                               # SSL/TLS configuration (optional)
    enabled: false
    cert_file: certs/localhost+2.pem
    key_file: certs/localhost+2-key.pem
```

When inference caching is enabled, a client can send `x-yallmp-cache-bypass: true` for a single request to skip both cache lookup and cache write. The proxy strips internal `x-yallmp-*` headers before forwarding upstream.

The exact cache covers complete 200 JSON responses for non-stream `/v1/chat/completions`, `/v1/embeddings`, and translated non-stream `/v1/messages` or `/v1/responses` requests. It does not cache streaming responses, errors, non-200 responses, native Anthropic Messages pass-through, or native Responses provider pass-through. Cache keys include the selected backend, app-key/group scope, required-header attribution, raw query, endpoint, and canonical outbound request body; translated Messages/Responses entries store the final client-facing translated JSON response.

Persistent exact-cache storage is disabled by default. When `proxy_settings.inference_cache.persistence.enabled` is true, the cache writes the same client-facing response bodies and filtered response headers stored in memory to a self-contained SQLite database at `persistence.path`. Protect that path with the same care as response logs. `flush_mode` controls disk interaction: `read_only` rehydrates existing rows but does not write new ones, `write_only` writes new rows but does not rehydrate reads, `read_write` or `writethrough` does both, and `off` disables the store. Persisted entries are rehydrated only if they still satisfy the configured TTL, max-age, max-entry, and max-entry-byte limits.

Semantic inference caching is also disabled by default and requires `inference_cache.mode` to be `read_write`, `read_only`, or `write_only` plus explicit `semantic.embedding_node` and `semantic.embedding_model` values. The embedding node receives the canonical outbound chat request body for non-stream `/v1/chat/completions` requests, so use a trusted local or approved embedding backend for sensitive prompts. Semantic recall is in-memory only, stores embeddings plus the complete 200 client-facing JSON response, and filters candidates by endpoint, raw query, selected backend, client model, embedding model, app-key/group scope, required-header attribution, and non-message request controls before cosine ranking. The same truthy `x-yallmp-cache-bypass` header skips semantic reads and writes. Streams, errors, non-200 responses, embeddings, translated Messages/Responses, and native pass-through requests are not semantically cached.

Operators can inspect bounded exact-cache and semantic-cache counters and size totals with `GET /admin/cache`. This endpoint is read-only, requires the admin password, and does not expose cache keys, prompts, request bodies, embedding vectors, URLs, app-key scopes, headers, or cached response content.

The model catalog is disabled by default. When `proxy_settings.model_catalog.enabled` is true, `GET /admin/catalog` returns a read-only view of configured model/provider/node availability. No provider probes run unless `probe_nodes` or `probe_providers` explicitly opts in a node or provider. Opted-in OpenAI-compatible nodes are probed with provider credentials against `/v1/models`; client keys and internal `x-yallmp-*` headers are not reused. Probe results only enrich the catalog, use bucketed error classes, and never change routing decisions. The response includes bounded advisory `warnings` derived from the redacted snapshot, and the React admin Catalog tab displays them read-only.

## Model Configuration

### Required Fields

| Field | Type | Description |
|-------|------|-------------|
| `model_name` | string | Unique identifier for the model |
| `model_params.api_base` | string | Base URL for the API endpoint |
| `model_params.api_key` | string | API authentication key |

### Optional Fields

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `protected` | boolean | true | Require admin password before replacing this model through `POST /admin/models` |
| `model_params.model` | string | - | Actual model name to send to backend |
| `model_params.api_type` | string | "openai" | API type: "openai", "anthropic", "gigachat", or "passthrough" |
| `model_params.target_model` | string | - | Override model name sent to backend |
| `model_params.request_timeout` | number | 60 | Request timeout in seconds |
| `model_params.supports_reasoning` | boolean | false | Display-only reasoning capability metadata exposed by `/v1/models`; it does not alter upstream requests |
| `model_params.http2` | boolean | false | Whether to use HTTP/2 |
| `model_params.parameters` | object | - | Parameter override configuration |

### GigaChat Backends

Set `model_params.api_type: gigachat` to use native GigaChat request, auth, response, streaming, and tool-call translation while exposing the normal OpenAI-compatible chat endpoint to clients.

```yaml
model_list:
  - model_name: GigaChat-Max
    model_params:
      api_type: gigachat
      api_base: https://gigachat.devices.sberbank.ru/api/v1
      api_key: ${GIGACHAT_API_KEY}
      scope: ${GIGACHAT_SCOPE}
      auth_url: https://ngw.devices.sberbank.ru:9443/api/v2/oauth
      model: GigaChat-Max
      request_timeout: 120
      emulate_tool_calls: false
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `model_params.mode` | string | auto | `cloud` for OAuth, `local` for mTLS, or omitted to infer from credentials |
| `model_params.api_key` | string | - | Base64 `client_id:client_secret` credential for cloud OAuth |
| `model_params.scope` | string | `GIGACHAT_API_CORP` | OAuth scope for cloud mode |
| `model_params.auth_url` | string | GigaChat OAuth URL | OAuth endpoint for cloud mode |
| `model_params.client_cert` | string | - | Client certificate path for local mTLS mode |
| `model_params.client_key` | string | - | Client private key path for local mTLS mode |
| `model_params.verify_ssl` | boolean | true | Verify upstream TLS certificates |
| `model_params.emulate_tool_calls` | boolean | false | Ask GigaChat to return JSON tool-call decisions instead of using native function calls |

The Go client uses the host system trust store when `verify_ssl` is true. It does not currently load a custom `ca_cert` bundle; that option exists only on the separate Python follow-up branch. Install the CA into the host trust store or port the option before relying on a private CA. Disabling `verify_ssl` is available for controlled development environments but removes server-certificate verification.

The admin model form also accepts `client_id` and `client_secret` for GigaChat cloud mode and persists the generated `api_key`.

## Provider/Node Models

For the simpler workflow with automatic pools and provider model selections, start
with [Models, nodes, and providers](simple-routing.md). The explicit wiring below
is still supported for advanced and existing configurations.

For node-backed models, define shared provider settings, individual nodes, and client-facing models:

```yaml
providers:
  - name: local-openai
    api_base: http://node-a.local:8000/v1
    api_key: ${LOCAL_API_KEY}
    endpoints: [chat, embeddings]
    limits:
      rps: 5
      concurrency: 4

nodes:
  - name: node-a
    provider: local-openai
    model: openai/upstream-a
  - name: node-b
    provider: local-openai
    api_base: http://node-b.local:8000/v1
    model: openai/upstream-b
    endpoints: [embeddings]
    allowed_keys: [app-prod-001]
    limits:
      concurrency: 2

models:
  - name: client-model
    nodes: [node-a, node-b]
    balancing: weighted_round_robin
    node_weights:
      node-a: 2
      node-b: 1
```

Node settings inherit from their provider. For `limits`, provider values are applied first and non-zero node values override them. For `endpoints`, node values replace provider values when set; empty endpoint lists mean all endpoints are allowed. `nodes[].allowed_keys` narrows which app-key IDs may use that node inside a client model. Backend RPS/RPM/concurrency limits are enforced for each upstream attempt, and endpoint-limited or key-limited backends are skipped before an upstream request is made.

GigaChat settings (`api_type`, `api_key`, `scope`, `auth_url`, `client_cert`, `client_key`, `verify_ssl`, `emulate_tool_calls`, and `request_timeout`) can be placed on a provider and overridden per node.

Providers, nodes, top-level client `models` entries, and router fallback chains can also be managed through the backend admin APIs: `/admin/providers`, `/admin/nodes`, `/admin/client-models`, and `/admin/router-settings`. Mutating these resources requires `YALLMP_ADMIN_PASSWORD`, preserves raw unknown keys and environment placeholders, validates the full candidate config, and applies runtime routing only after validation succeeds. The React Routing UI exposes safe edits for provider/node endpoints and limits, node modules and `allowed_keys`, client-model `node_weights`, `balancing`, limits, and `allowed_keys`, replace-only router fallback chains, plus legacy model dependency, copy, and delete actions; use raw config editing for unsupported fields.

### Parameter Overrides

The `parameters` section allows you to enforce or default certain LLM parameters:

```yaml
parameters:
  temperature:
    default: 1.0              # Default value if not provided in request
    allow_override: false     # If false, always use default (ignore request)
  top_p:
    default: 0.95
    allow_override: true      # If true, use request value if provided
```

To explicitly force an upstream reasoning/thinking request field, configure it as
a parameter default. `supports_reasoning` remains only a client-facing display
capability:

```yaml
parameters:
  thinking.type:
    default: enabled
    allow_override: true
```

## Model Inheritance

Models can inherit configuration from other models using the `extends` field. This allows you to create derived models with configuration overrides without duplicating the full configuration.

### Basic Inheritance

```yaml
model_list:
  # Base model with full configuration
  - model_name: GLM-4.7
    protected: true
    model_params:
      api_base: https://api.example.com/v1
      api_key: ${GLM_API_KEY}
      parameters:
        temperature:
          default: 1.0
          allow_override: false
    modules:
      upstream:
        enabled: true
        response:
          - swap_reasoning_content

  # Derived model inherits from GLM-4.7, adds custom modules
  - model_name: GLM-4.7:Cursor
    protected: false
    extends: GLM-4.7
    modules:
      upstream:
        enabled: true
        response:
          - parse_tags
          - swap_reasoning_content
```

The derived model `GLM-4.7:Cursor` inherits:
- All `model_params` from `GLM-4.7` (api_base, api_key, parameters, etc.)
- Then overrides/extends with its own configuration (parsers)

### How Inheritance Works

1. The base model is fully resolved first (including its own inheritance chain)
2. Derived model settings are deep-merged on top
3. Nested configurations (like `parameters`, `parsers`) are merged recursively
4. Lists are replaced (not merged) - e.g., `response` parser list
5. The `extends` field is removed from the resolved model

### Chained Inheritance

Inheritance chains are supported:

```yaml
model_list:
  - model_name: base-model
    model_params:
      api_base: https://base.local/v1
      api_key: ${BASE_KEY}

  - model_name: middle-model
    extends: base-model
    model_params:
      api_key: ${MIDDLE_KEY}
      request_timeout: 120

  - model_name: derived-model
    extends: middle-model
    model_params:
      parameters:
        temperature:
          default: 0.7
```

The final `derived-model` will have:
- `api_base` from `base-model` (inherited through chain)
- `api_key` from `middle-model`
- `request_timeout` from `middle-model`
- `temperature` parameter set to 0.7

### Inheritance Within One Config

Models in the same `config.yaml` can inherit from each other:

```yaml
# configs/config.yaml
model_list:
  - model_name: GLM-4.7
    protected: true
    model_params:
      api_base: https://api.z.ai/api/coding/paas/v4
      api_key: ${GLM_API_KEY}
    modules:
      upstream:
        enabled: true
        response:
          - swap_reasoning_content

  - model_name: GLM-4.7:Custom
    protected: false
    extends: GLM-4.7
    modules:
      upstream:
        response:
          - parse_tags
          - swap_reasoning_content
```

### Error Handling

- **Circular references**: Detected and raises error (e.g., A extends B, B extends A)
- **Missing base model**: Raises error if the referenced model doesn't exist
- **Maximum depth**: Inheritance chains are limited to 10 levels

## Response Modules

### Available Modules

The proxy uses a modular pipeline system for processing responses. Modules can be applied globally or per-model.

1. **parse_tags** - Extract tool calls and thinking content from raw responses
2. **swap_reasoning_content** - Swap reasoning content with main content
3. **stream_json_repair** - Opt-in repair for incomplete streamed tool/function argument JSON fragments

### Module Configuration

#### parse_tags

Parses `<think>` and `<tool_call>` tags (or custom delimiters) from response content into structured fields (`reasoning_content`, `tool_calls`).

```yaml
parse_tags:
  # Optional: auto-detect config from Jinja template
  template_path: configs/jinja_templates/template_example.jinja

  # Parsing options
  parse_thinking: true           # Extract thinking tags
  parse_tool_calls: true         # Extract tool calls
  think_tag: "think"             # Tag name for thinking content

  # Tool argument format
  tool_arg_format: "xml"         # xml | json

  # For xml format (default):
  tool_tag: "tool_call"          # Tag name: <tool_call>...</tool_call>

  # For json format (K2-style models):
  tool_open: "<|tool_call_begin|>"       # Custom open delimiter
  tool_close: "<|tool_call_end|>"        # Custom close delimiter
  tool_arg_separator: "<|tool_call_argument_begin|>"  # Separates name from JSON args
  drop_tags:                             # Tags to strip (e.g., section markers)
    - "<|tool_calls_section_begin|>"
    - "<|tool_calls_section_end|>"

  tool_buffer_limit: 200          # Optional: max buffered tool chars before treating as literal
  max_stream_buffer_bytes: 1048576 # Optional: max stream parser buffer before literal flush
  max_stream_buffer_age_seconds: 0 # Optional: max stream parser buffer age before literal flush; 0 disables
```

**Tool Argument Formats:**

- **xml** (default): Tool calls use XML-style argument encoding
  ```xml
  <tool_call>function_name<arg_key>param</arg_key><arg_value>value</arg_value></tool_call>
  ```

- **json**: Tool calls use JSON arguments (K2-style models like Kimi)
  ```
  <|tool_call_begin|>function_name<|tool_call_argument_begin|>{"param": "value"}<|tool_call_end|>
  ```

**Template Auto-Detection:**

When `template_path` is provided, the module automatically detects:
- Think tag name from template expressions using `reasoning_content`
- Tool argument format (xml vs json/K2) from template markers
- Custom delimiters for K2-style tool calls

**Notes:**
- Uses incremental streaming parser for both stream and non-stream responses
- Tool calls can appear inside thinking blocks; tool parsing has higher priority
- Tool calls are emitted only after a full, parseable block is confirmed
- If `tool_buffer_limit` is exceeded without closing, the tag is treated as literal text
- If `max_stream_buffer_bytes` is exceeded while waiting for a tag boundary, buffered text is flushed as literal content or reasoning
- If `max_stream_buffer_age_seconds` is positive and a pending stream buffer ages past it, the buffered text is flushed as literal content or reasoning on the next stream event

#### swap_reasoning_content

```yaml
swap_reasoning_content:
  mode: "reasoning_to_content"  # reasoning_to_content | content_to_reasoning | auto
  think_tag: "think"
  think_open:
    prefix: ""                  # Prefix before <think>
    suffix: ""                  # Suffix after <think>
  think_close:
    prefix: ""                  # Prefix before </think>
    suffix: ""                  # Suffix after </think>
  include_newline: true         # Add newline between </think> and content
```

**Mode Options:**
- `reasoning_to_content`: Move thinking content to message content
- `content_to_reasoning`: Move content to thinking
- `auto`: Auto-detect based on tags

#### stream_json_repair

Repairs incomplete JSON only in streamed `delta.tool_calls[].function.arguments` fragments. It is disabled unless the response module is listed and `stream_json_repair.enabled: true` is set. Normal content streams, non-stream responses, arbitrary text, and already valid tool-argument JSON pass through unchanged.

```yaml
modules:
  upstream:
    enabled: true
    response:
      - stream_json_repair
    stream_json_repair:
      enabled: true
      max_stream_buffer_bytes: 1048576
      max_stream_buffer_age_seconds: 0
      on_failure: passthrough # passthrough | empty_object
```

Repair is conservative and bounded. The module buffers only structured-looking object/array argument fragments, emits valid JSON unchanged when the accumulated argument becomes valid, and otherwise tries simple repairs at tool-call finish or `[DONE]`: trimming trailing commas, closing unterminated strings, and balancing missing `}` / `]` delimiters. If repair is uncertain, the default `passthrough` policy emits the original accumulated argument text. `empty_object` emits `{}` instead.

`max_stream_buffer_bytes` caps all pending repair buffers for the stream. If the cap would be exceeded, buffered text is released unchanged and that tool call bypasses further repair until its finish event.

`max_stream_buffer_age_seconds` is disabled by default. When positive, pending streamed argument buffers older than the configured age are released unchanged on the next stream event, then that tool call bypasses further repair until its finish event.

### Per-Model Module Overrides

Per-model modules replace global `proxy_settings.modules` config:

```yaml
model_list:
  - model_name: GLM-4.7
    model_params:
      api_base: https://api.example.com/v1
      api_key: ${GLM_API_KEY}
    modules:
      upstream:
        enabled: true
        response:
          - swap_reasoning_content
        swap_reasoning_content:
          mode: reasoning_to_content
```

If `enabled` is omitted, per-model modules default to enabled. Set `enabled: false` to explicitly disable.

## Environment Variables

### Configuration Override

| Environment Variable | Description |
|---------------------|-------------|
| `YALLMP_HOST` | Server bind address (overrides config) |
| `YALLMP_PORT` | Server bind port (overrides config) |
| `YALLMP_CONFIG` | Path to config file |
| `YALLMP_ADMIN_PASSWORD` | Admin password for config, logs/traces, routing, keys, cache/catalog, quota, and profiling APIs; strongly recommended outside loopback development |

When an admin password is configured, the server also protects masked config reads/writes and every `/admin/logs`, `/admin/traces`, and compatibility `/api/logs` route. Without one, config and log APIs retain legacy open access while stricter admin surfaces remain unavailable. Treat an unset password as loopback-only development mode because decoded log sidecars may contain prompts and responses.

`proxy_settings.profiling.enabled`, `proxy_settings.metrics.enabled`, `proxy_settings.metrics.timings.enabled`, `proxy_settings.otel.enabled`, and `proxy_settings.otel.logs.enabled` use strict bool parsing. Boolean values and common bool-like strings such as `true`, `false`, `yes`, `no`, `on`, and `off` validate; other types are rejected by config validation and do not enable the guarded endpoint. `proxy_settings.otel.insecure` uses the same strict bool parsing. `otel.logs.enabled: true` is invalid unless `otel.enabled: true`.

`proxy_settings.metrics.enabled` exposes the guarded Prometheus text snapshot at `/metrics`. `proxy_settings.metrics.timings.enabled` is a separate disabled-by-default opt-in that adds in-memory Prometheus histograms for `yallmp_request_duration_seconds`, `yallmp_backend_request_duration_seconds`, `yallmp_stream_ttft_seconds`, and `yallmp_stream_tpot_seconds` when the `/metrics` endpoint is enabled. These histograms are independent from OTel exporters and use only bounded labels such as endpoint kind, sanitized configured model/backend label, stream flag, outcome, status class, and error bucket. They do not include prompts, raw headers, raw URLs, query strings, API keys, app-key IDs, group IDs, backend URLs, or raw error strings.

`proxy_settings.otel` is disabled by default. When enabled, the only supported exporter is `otlp_grpc` and `endpoint` is required. Bare endpoints must be `host:port`; absolute `http` and `https` OTLP URLs are also accepted. Header names must be valid HTTP field names, and header values must be strings so `${...}` env references can be used without exposing secrets in the repository. The generic JSON proxy endpoints (`/v1/chat/completions`, `/v1/embeddings`, and `/v1/rerank`) plus translator-specific `/v1/messages` and `/v1/responses` emit request, policy, admission, routing, upstream-attempt, response, parser, and logging spans where those phases run. Direct `/p1` passthrough emits request, optional app-key policy/admission, upstream-attempt, and response spans while keeping arbitrary payload forwarding. The same OTel provider also emits request count, request duration, reconciled GenAI token usage, stream TTFT (`gen_ai.client.operation.time_to_first_token`), and stream aggregate TPOT/ITL (`gen_ai.client.operation.time_per_output_token`) metrics. TTFT is observed after the first successful client-visible stream write, and TPOT is recorded once at stream EOF as an average between observed chunks. `proxy_settings.otel.logs.enabled` remains off unless explicitly enabled and emits sanitized `proxy.log` span events through the existing trace exporter rather than a separate OTLP logs exporter. Span, metric, and log-event attributes use bounded operational values for phase, endpoint kind, sanitized configured model name, backend name, outcome, attempt/retry indexes, stream flag, HTTP status, error bucket, key-scope bucket, token type/count fields, trace/span IDs when available, and GenAI-style `gen_ai.system`, `gen_ai.operation.name`, and `gen_ai.request.model` attributes. Response-phase spans also set `gen_ai.response.model` when it is the same sanitized configured model label. They do not record prompts, bodies, raw headers, query strings, bearer tokens, raw app-key IDs, raw group IDs, raw provider keys, raw backend URLs, required-header values, or raw error strings.

### Forwarder Override

| Environment Variable | Description |
|---------------------|-------------|
| `FORWARD_LISTEN_HOST` | Forwarder listen address |
| `FORWARD_LISTEN_PORT` | Forwarder listen port |
| `FORWARD_TARGET_HOST` | Forwarder target host |
| `FORWARD_TARGET_PORT` | Forwarder target port |

### HTTP Forwarder Override

| Environment Variable | Description |
|---------------------|-------------|
| `HTTP_FORWARD_LISTEN_HOST` | HTTP forwarder listen address |
| `HTTP_FORWARD_LISTEN_PORT` | HTTP forwarder listen port |
| `HTTP_FORWARD_TARGET_SCHEME` | HTTP forwarder target scheme |
| `HTTP_FORWARD_TARGET_HOST` | HTTP forwarder target host |
| `HTTP_FORWARD_TARGET_PORT` | HTTP forwarder target port |
| `HTTP_FORWARD_PRESERVE_HOST` | Preserve Host header (true/false) |
| `HTTP_FORWARD_DEBUG` | Enable debug logging (true/false) |
| `HTTP_FORWARD_TIMEOUT` | Request timeout in seconds |
| `HTTP_FORWARD_SSL_ENABLED` | Enable SSL/TLS (true/false) |
| `HTTP_FORWARD_SSL_CERT` | Path to SSL certificate file |
| `HTTP_FORWARD_SSL_KEY` | Path to SSL private key file |

### In-Config Substitution

Use `${VAR_NAME}` or `$VAR_NAME` syntax in config to substitute environment variables:

```yaml
api_key: ${GLM_API_KEY}        # Braced format
api_key: $GLM_API_KEY          # Simple format
```

Variables are first read from `.env`, then from the actual environment.

## Router Settings

### Retry Configuration

```yaml
router_settings:
  num_retries: 1               # Number of retry attempts per backend
```

### Fallback Models

```yaml
router_settings:
  fallbacks:
    - gpt-4: [gpt-4-turbo, claude-3-opus]  # If gpt-4 fails, try fallbacks in order
    - claude-3: [claude-3-sonnet]
```

### Health Checks And Scoring

```yaml
router_settings:
  health_checks:
    enabled: true
    path: /models
    interval_seconds: 15
    timeout_seconds: 2
    failure_threshold: 2
    unhealthy_cooldown_seconds: 30
    recovery_success_threshold: 0
  health_scoring:
    enabled: true
    unhealthy_threshold: 0.3
    recovery_success_threshold: 0
  quota_aware_selection:
    enabled: false
    persist_backend_quota: false
```

`recovery_success_threshold` is optional and defaults to `0`, preserving the legacy cooldown behavior where a backend becomes healthy as soon as its cooldown expires. Set it to a positive number to enable half-open recovery: after cooldown expiry the backend is used only as a last resort or by active probes until it records that many consecutive successful trials. Any half-open failure starts a fresh cooldown.

### Quota-Aware Backend Selection

```yaml
router_settings:
  quota_aware_selection:
    enabled: true
    persist_backend_quota: false
```

Quota-aware selection is disabled by default. When enabled, the router peeks at configured provider/node `limits.token_quota` windows before selecting a backend and skips exhausted candidates when another healthy candidate has estimated token capacity. The peek does not reserve or mutate quota state. If every candidate is exhausted, routing falls back to the full candidate route so existing retry and upstream failure behavior remains possible.

Backend quota selection uses the same `limits.token_quota`, `token_quota_window_seconds`, and UTC calendar `token_quota_reset_mode` fields as request quotas, but accounting is per backend under `backend::<node-name>` and is updated from successful upstream usage only. Per-backend quota usage remains in-memory by default and is not restored on restart/reload unless both `enabled: true` and `persist_backend_quota: true` are set.

When quota-aware selection, `persist_backend_quota`, and the metadata log database are enabled, startup and config reload restore configured backend quota scopes from stored successful request metadata. Restore is filtered to the active rolling or UTC calendar window for each backend limit, ignores removed/unconfigured backends, and treats log database query failures as non-fatal. `GET /admin/backend-quotas` provides an admin-password-protected, read-only snapshot with backend name, used tokens, configured limit, remaining tokens, and reset time; it does not expose app-key/group IDs, request bodies, prompts, headers, backend URLs, or secrets.

### Prefix-Cache-Affinity Node Scheduling

```yaml
router_settings:
  prefix_cache_affinity:
    enabled: true
    max_entries: 4096
```

Prefix-cache affinity is enabled by default (explicit `enabled: false` opts out) and never mutates requests, headers, or upstream bodies. When enabled, the router hashes shallow message prefixes (message 0, messages 0-1, and messages 0-2 of the routed payload's `messages`) and records a bounded in-memory binding between each served prefix hash and the node that served it after a successful `200` upstream response.

During node-pool selection, among the nodes that already passed endpoint support, node/app-key `allowed_keys`, health and half-open state, backend-availability, and quota-aware predicates, the router prefers the node bound to the longest matching prefix. Sticky routing from `conversation_id`, `thread_id`, or `previous_response_id` always takes precedence over prefix affinity. When affinity is disabled, no prefix matches, or every matching node is otherwise ineligible, routing is byte-for-byte identical to the existing weighted/round-robin behavior.

`max_entries` bounds the total number of `(node, prefix-hash)` bindings held in memory (default `4096`); the oldest binding is evicted when the bound is exceeded. Bindings are in-memory only and are not restored on restart/reload.

### Concurrency-Aware Node Selection

```yaml
router_settings:
  concurrency_aware_selection:
    enabled: true
```

Concurrency-aware selection is disabled by default. When enabled, fresh node-pool requests prefer the eligible node with the fewest live upstream attempts. The in-flight count is per configured backend/node, starts after an upstream attempt is admitted, and is released when the attempt completes, including non-stream responses, upstream errors, retries and fallbacks, stream EOF/Close, and cancelled stream closes.

Sticky routing from `conversation_id`, `thread_id`, `previous_response_id`, and stored response metadata takes precedence. Prefix-cache affinity also takes precedence when it finds an eligible node. Concurrency-aware selection only considers nodes that already passed endpoint support, app-key/node `allowed_keys`, health and half-open state, backend-availability checks, quota-aware predicates, and existing fallback route structure. Ties use the existing weighted or round-robin order.

## Logging Configuration

```yaml
proxy_settings:
  logging:
    log_parsed_response: true  # Store raw/transformed non-stream JSON response fields when a response transform occurs
    log_parsed_stream: false   # Store capped raw/transformed SSE sidecars when a stream transform occurs
    log_to_disk: true          # Enables parsed sidecar fields with full_request_storage
    full_request_storage:
      enabled: true
      path: logs/requests
      retention_hours: 48
      cleanup_on_startup: true
      cleanup_interval_hours: 24
```

Parsed response sidecar fields are written only for non-stream JSON responses where a response module changes the upstream body. They require all three gates: `full_request_storage.enabled`, `log_to_disk`, and `log_parsed_response`.

Parsed stream sidecars are written for streaming/SSE responses when stream response modules are active. They require all three gates: `full_request_storage.enabled`, `log_to_disk`, and `log_parsed_stream`. The full-request record stores `parsed_stream` metadata plus capped raw-upstream and transformed-client `.sse` sidecar paths, byte counts, content type, and truncation flags. `/admin/logs/{id}` reads sidecar text with a bounded limit, and retention cleanup removes generated stream sidecar files.

## App Key Authentication

yaLLMproxy supports optional API key authentication for proxy access. This feature allows you to control access to the proxy and implement rate limiting per key.

```yaml
# App Key Authentication (optional)
app_keys:
  enabled: true                    # Set to true to enable app key authentication
  header_name: x-api-key           # Header name for API key (also accepts Authorization: Bearer)
  allow_unauthenticated: false     # Allow requests without a key when enabled
  required_headers:                # Optional headers required for every allowed request
    - name: X-Tenant-ID
      log_as: tenant               # Optional alias stored in full-request sidecar logs
      models: ["premium-model"]    # Optional; empty applies to all requested models
  defaults:                        # Base limits for authenticated keys
    concurrency_limit: 0           # Alias for limits.concurrency; 0 means unlimited
    priority: 100                  # Lower number is higher priority
    queue_timeout: 30              # Alias for queue_timeout_seconds
  unauthenticated:                 # Limits used when allow_unauthenticated is true
    concurrency_limit: 5
    priority: 1000
    queue_timeout: 30
  groups:
    - group_id: "0123456789abcdef0123456789abcdef"
      name: "Production team"
      enabled: true
      models: ["premium-model"]
      required_headers:
        - name: X-Team-ID
          log_as: team
      limits:
        rps: 5
        rpm: 120
        token_quota: 100000
        token_quota_reset_mode: daily
  keys:
    - key_id: "app-dev-001"
      secret: "${APP_KEY_DEV}"     # Use env var for secrets
      name: "Development"
      description: "Development team access"
      enabled: true
    - key_id: "abcdef0123456789abcdef0123456789"
      group_id: "0123456789abcdef0123456789abcdef"
      secret: "${APP_KEY_PROD}"    # Example value: yallmp-{group_uuid}-{key_uuid}
      name: "Production app"
      enabled: true
      models: ["premium-model"]
      required_headers:
        - name: X-Workload-ID
          log_as: workload
      limits:
        concurrency: 2             # Non-zero key limits override group limits
      concurrency_limit: 2         # Legacy top-level alias for limits.concurrency
      priority: 50                 # Legacy top-level alias for limits.priority
      queue_timeout: 15            # Legacy top-level alias for limits.queue_timeout_seconds
```

### Configuration Options

|| Option | Type | Default | Description |
||--------|------|---------|-------------|
|| `enabled` | boolean | false | Enable/disable app key authentication |
|| `header_name` | string | "x-api-key" | Header name for API key lookup |
|| `allow_unauthenticated` | boolean | false | Allow requests without a key when authentication is enabled |
|| `required_headers` | list | [] | Required request headers applied globally before group/key rules |
|| `defaults` | object | `{}` | Base limits applied to authenticated keys before group/key overrides |
|| `unauthenticated` | object | `{}` | Limits applied to requests without a key when unauthenticated requests are allowed |
|| `groups` | list | [] | Optional groups for shared model access and limit scopes |
|| `keys` | list | [] | List of API keys |

### Group Configuration

|| Option | Type | Description |
||--------|------|-------------|
|| `group_id` | string | Unique group identifier, matching the middle segment of `yallmp-{group_uuid}-{key_uuid}` keys |
|| `name` | string | Human-readable group name |
|| `enabled` | boolean | Enable/disable every key in this group |
|| `models` | list | Models allowed for this group; empty allows all models |
|| `limits` | object | Shared request/concurrency/token limits for the group |
|| `required_headers` | list | Additional required request headers for keys in this group |

### Key Configuration

|| Option | Type | Description |
||--------|------|-------------|
|| `key_id` | string | Unique identifier for the key (used in access control) |
|| `group_id` | string | Optional group identifier; inferred from `yallmp-{group_uuid}-{key_uuid}` when omitted |
|| `secret` | string | The API key secret (supports env var substitution) |
|| `name` | string | Human-readable name for the key |
|| `description` | string | Description of the key's purpose |
|| `enabled` | boolean | Enable/disable this key |
|| `models` | list | Models allowed for this key; empty allows all models |
|| `limits` | object | Per-key limit overrides |
|| `required_headers` | list | Additional required request headers for this key |
|| `concurrency_limit` | integer | Legacy top-level alias for `limits.concurrency` |
|| `priority` | integer | Legacy top-level alias for `limits.priority` |
|| `queue_timeout` | number | Legacy top-level alias for `limits.queue_timeout_seconds` |

When a key belongs to a group, the effective model policy is the intersection of the group and key model lists. App-key defaults are applied first, group limits are applied second, and non-zero key limits override them. Admission and token quota accounting use the group scope, so different keys in the same group share request-rate, concurrency, and token-quota budgets.

### Limit Configuration

`limits.token_quota` caps the accumulated token usage for the key or group/model scope. By default, quota accounting is cumulative unless `token_quota_window_seconds` is set, which keeps the existing rolling-window behavior by resetting each active scope after that many seconds from its first reservation in the current window.

Use `limits.token_quota_reset_mode` for calendar-aligned token quota resets. Accepted values are empty/`rolling`, `daily`, `weekly`, `monthly`, and `yearly`. Calendar modes use UTC boundaries: daily resets at the next 00:00 UTC, weekly at the next Monday 00:00 UTC, monthly on the first day of the next month at 00:00 UTC, and yearly on January 1 at 00:00 UTC. Calendar modes are mutually exclusive with `token_quota_window_seconds`; validation rejects configs that set both.

### Required Header Policy

`required_headers` can be configured globally under `app_keys`, on a group, and on an individual key. The effective policy is conservative: global, group, and key rules are combined, and every matching rule must be present on the request. A rule with `models` applies only when the requested model matches one of those names; `*` and `all` match every model.

Each rule supports:

|| Field | Type | Description |
||-------|------|-------------|
|| `name` | string | HTTP request header name to require. `Authorization` and the configured app-key header are not valid required headers. |
|| `log_as` | string | Optional attribution alias. When full-request storage is enabled and the request is accepted, the header value is written to the sidecar `required_headers` map under this alias. |
|| `models` | list | Optional model names for this rule. Empty applies the rule to all models. |

Missing required headers return `400` with code `missing_required_header` before admission, quota reservation, concurrency acquisition, or upstream routing. Rejected requests are not written to full-request sidecar storage. The Prometheus counter `yallmp_required_header_missing_total` is label-free; raw header values are never used as metric labels.

Use `log_as` only for non-secret attribution headers such as tenant, project, user, or workload identifiers. The normal full-request sidecar still stores request headers with the existing name-based redaction rules.

### Admin App-Key API

The Go server exposes admin CRUD endpoints for app keys:

```http
GET /admin/keys
GET /admin/keys/{key_id}
POST /admin/keys
PUT /admin/keys/{key_id}
DELETE /admin/keys/{key_id}
POST /admin/keys/{key_id}/regenerate
POST /admin/keys/config
```

All `/admin/keys*` endpoints require `YALLMP_ADMIN_PASSWORD` through `x-admin-password` or the `admin_password` query parameter. If the password is unset or incorrect, the server returns `403`.

List, get, update, and config responses never include existing `secret` values. Create and regenerate responses return the new secret once in a top-level `secret` field and masked key metadata with `has_secret`.

`POST /admin/keys/config` updates `enabled`, `header_name`, `allow_unauthenticated`, `required_headers`, `defaults`, `unauthenticated`, and `groups`. Key CRUD endpoints update the `keys` list, including per-key `required_headers`, and preserve existing secrets on metadata updates. Every mutation validates the full config, persists `app_keys` to `configs/config.yaml` when the proxy was loaded from disk, and updates runtime policy immediately.

The React admin UI exposes the same workflows from the App Keys tab. Create and regenerate responses show the new secret once in a dismissible banner; existing secrets are never loaded into edit forms.

### Per-Model Access Control

You can restrict which API keys have access to specific models:

```yaml
model_list:
  - model_name: premium-model
    model_params:
      api_base: https://api.example.com/v1
      api_key: ${PREMIUM_API_KEY}
    access_control:
      allowed_keys:                # List of key_ids that can access this model
        - app-prod-001
        - app-premium-001
```

For node-backed client models, use the top-level `allowed_keys` field:

```yaml
models:
  - name: premium-model
    nodes: [premium-node-a, premium-node-b]
    allowed_keys:
      - app-prod-001
      - app-premium-001
```

**Special Values for `allowed_keys`:**

- `"all"` - All API keys can access this model (default)
- `"none"` - No API keys can access this model (requires unauthenticated access)

## Template-Derived Parser Config

Go parser modules can derive common think/tool markers directly from a configured `template_path`:

```yaml
modules:
  upstream:
    enabled: true
    response:
      - parse_tags
    parse_tags:
      template_path: configs/jinja_templates/template_example.jinja
```

Use `GET /admin/templates` to list templates, `POST /admin/templates` to upload UTF-8 templates, and `GET /admin/templates/inspect?template_path=...` to inspect Go's derived parse-tags config. The inspect endpoint runs bounded Jinja-compatible inspection over representative chat/tool contexts, combines rendered and raw marker hints, and returns `evidence`, `warnings`, and `analysis.engine: bounded_jinja_inspection`. Add `render_preview=1` to include an optional, byte-capped `render_preview` from the additive bounded runtime renderer using synthetic sample messages/tools only. The runtime preview supports common HuggingFace chat-template expressions, `if`/`elif`/`else`, `for`, `range`, loop metadata, whitespace trimming, and common filters, but it is still bounded and is not full Jinja macro/import/include parity.

## Configuration Validation

Use `POST /admin/config/validate` to validate a config before applying it. `PUT /admin/config` runs the same validation before persisting and rebuilding runtime state. Common validation errors include:

- Missing required fields (`model_name`, `api_base`)
- Invalid URL in `api_base`
- Duplicate model names

## Database

Persistent log storage is configured with the top-level `database` block. SQLite remains the default when the block is present and `database.backend` is omitted:

```yaml
database:
  backend: sqlite
  connection:
    sqlite:
      path: logs/yaLLM.db
```

PostgreSQL is available with either `postgres` or `postgresql`:

```yaml
database:
  backend: postgres
  connection:
    postgres:
      dsn: ${YALLMP_POSTGRES_DSN}
```

Field-based PostgreSQL config also works with `host`, `port`, `database` or `dbname`, `user`, `password`, and `sslmode`. Database open/init failures fail startup and config reload; reload keeps the previous runtime state when the new database config cannot be applied.

## Hot Reloading

Runtime model additions via `/admin/models` are persisted to `configs/config.yaml` and take effect immediately without restarting the proxy.

Protected models require `YALLMP_ADMIN_PASSWORD` before replacement through `POST /admin/models`. Legacy model copy/delete and provider/node/client-model backend CRUD workflows are also available through admin APIs and the React Routing UI, then persist to `configs/config.yaml` when the proxy was loaded from disk.

Runtime app-key changes via `/admin/keys*` are also persisted to `configs/config.yaml` and take effect immediately without restarting the proxy.
