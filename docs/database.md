# Database Support

The Go branch supports SQLite and PostgreSQL for persistent request metadata, error logs, response state, token usage, and admin log browsing.

## Configuration

```yaml
database:
  backend: sqlite
  connection:
    sqlite:
      path: logs/yaLLM.db

proxy_settings:
  logging:
    full_request_storage:
      enabled: true
      path: logs/requests
      retention_hours: 48
      cleanup_on_startup: true
```

If `database.backend` is omitted, SQLite is used when a database block is present. Relative SQLite and sidecar paths are resolved from the project root.

PostgreSQL uses the `pgx/v5` database/sql driver. Both `postgres` and `postgresql` are accepted backend names:

```yaml
database:
  backend: postgres
  connection:
    postgres:
      dsn: ${YALLMP_POSTGRES_DSN}
```

The PostgreSQL DSN can also be expressed as fields:

```yaml
database:
  backend: postgresql
  connection:
    postgres:
      host: localhost
      port: 5432
      database: yallmp_logs
      user: yallmp
      password: ${YALLMP_POSTGRES_PASSWORD}
      sslmode: disable
```

`database.connection.postgres.dbname` is accepted as an alias for `database`. Password values are redacted from database open/init error text before those errors are returned by startup or config reload.

Optional pool fields are mapped to conservative `database/sql` settings when present: `pool_size`, `max_overflow`, `max_open_conns`, `max_idle_conns`, `conn_max_lifetime_seconds`, and `conn_max_idle_time_seconds`.

Startup and config reload fail when an explicit database backend is unsupported, malformed, unreachable, or cannot initialize its schema. Existing runtime state is kept when reload fails. Request-time log persistence errors are still non-fatal to proxied requests.

## Tables

### request_metadata

Stores request listing and analytics data.

| Column | Description |
| --- | --- |
| `id` | Request log UUID |
| `request_time` | Request start time |
| `model_name` | Client-facing model name |
| `is_stream` | Whether the request streamed |
| `path`, `method`, `query`, `request_path` | HTTP routing fields |
| `outcome` | `success` or `error` |
| `duration_ms` | Request duration |
| `stop_reason` | OpenAI/translated stop reason when available |
| `is_tool_call` | Whether the response finished with tool calls |
| `app_key_id` | Authenticated app key ID when available |
| `backend_name` | Selected backend/node name |
| `backend_status` | Upstream/proxy status code |
| `prompt_tokens`, `completion_tokens`, `total_tokens` | Token counts extracted from upstream usage |
| `cached_tokens`, `reasoning_tokens` | Detail token counts when present |
| `tokens_per_second`, `weighted_tokens` | Throughput metrics when derivable |
| `full_request_path` | Full request sidecar JSON path |
| `full_request_expires_at` | Sidecar expiration time |

### request_attempts

Stores metadata-only upstream attempt trails for shared proxy routes.

| Column | Description |
| --- | --- |
| `request_id` | Linked `request_metadata.id` |
| `attempt_index` | 1-based chronological attempt number |
| `backend_name` | Backend/node attempted |
| `retry_index` | 0-based retry number within that backend |
| `status` | Upstream HTTP status, or `0` when no response was received |
| `retryable` | Whether the attempt was retryable by proxy policy |
| `outcome` | `retry`, `final`, `blocked`, or `error` |
| `duration_ms` | Attempt duration in milliseconds |
| `error` | Sanitized metadata-only error text |
| `created_at` | Insert time |

Attempt rows never store request bodies, response bodies, headers, `Authorization`, provider API keys, or raw client secrets. For streams, `duration_ms` is measured to upstream response headers; stream body forwarding remains handled by the existing response body wrapper.

### error_logs

Stores error records with optional links to request metadata.

| Column | Description |
| --- | --- |
| `id` | Error UUID |
| `timestamp` | Error time |
| `model_name` | Associated model |
| `error_type` | Request, backend, auth, or rate-limit category |
| `error_message` | Human-readable error |
| `backend_name` | Backend involved, when known |
| `http_status` | HTTP status |
| `request_path` | Path including query |
| `request_log_id` | Linked request metadata ID |
| `extra_context` | JSON context |

### response_states

Stores Responses API state for `store` and `previous_response_id`.

| Column | Description |
| --- | --- |
| `id` | Response ID |
| `previous_response_id` | Previous response in the chain |
| `model` | Model name |
| `status` | Response status |
| `input_data` | Original Responses API input |
| `output_data` | Output items |
| `full_response` | Complete response object |
| `usage` | Token usage |
| `response_metadata` | User metadata |
| `expires_at` | Optional expiration |

## Full Request Sidecars

When `proxy_settings.logging.full_request_storage.enabled` is true, the proxy stores JSON sidecars containing:

- request headers,
- response headers,
- base64-encoded request body,
- base64-encoded response body for non-stream requests,
- `parsed_response.raw_upstream_response_body_base64` and `parsed_response.transformed_client_response_body_base64` for non-stream JSON responses changed by response modules when `log_to_disk` and `log_parsed_response` are enabled,
- status, model, path, and error text.

Sidecars are linked from `request_metadata.full_request_path` and surfaced by `GET /admin/logs/{id}`. Attempt trails are surfaced from `request_attempts` as `attempt_trail` and `attempts`.

## Admin Log APIs

`GET /admin/logs` supports:

- `limit`
- `offset`
- `model` or `model_name`
- `outcome`
- `stop_reason`
- `is_tool_call`
- `start_time` or `start_date`
- `end_time` or `end_date`
- `search` across retained sidecar bodies

`GET /admin/logs/{id}` returns a decoded sidecar detail when available, plus `attempt_trail` and `attempts` when upstream attempts were recorded.

Analytics endpoints:

- `GET /admin/logs/stop-reasons`
- `GET /admin/logs/tool-call-rate`
- `GET /admin/logs/models`

## Cleanup

Preview cleanup:

```bash
task db-clean-dry-run -- --all --keep-days=7
```

Delete matching request/error rows:

```bash
task db-clean -- --all --keep-days=7
```

Clear log files while preserving `logs/yaLLM.db`:

```bash
task clean
```
