# Database Support

yaLLMproxy supports SQLite (default) and PostgreSQL databases for persistent logging and usage tracking.
Database request logging is enabled by default when the database module is configured and reachable.

## Overview

The database module provides:
- **Request Logging**: Store all request/response data with JSON columns
- **Response State Storage**: Persist responses for the Responses API with conversation chaining
- **Error Tracking**: Log errors with optional references to request logs
- **Usage Statistics**: Query historical usage data by model, time period, etc.
- **Interchangeable Backends**: Switch between SQLite and PostgreSQL without code changes

## Configuration

Add database settings to your `configs/config.yaml`:

```yaml
database:
  # Database backend: sqlite or postgres
  backend: sqlite

  # Connection settings
  connection:
    # SQLite-specific
    sqlite:
      path: logs/yaLLM.db

    # PostgreSQL-specific
    postgres:
      host: localhost
      port: 5432
      database: yallm_proxy
      user: ${DB_USER}
      password: ${DB_PASSWORD}

  # Connection pool settings
  pool_size: 5
  max_overflow: 10
```

**Pool settings notes:**
- `pool_size`: Base number of open connections in the pool (applies to PostgreSQL).
- `max_overflow`: Extra connections allowed above `pool_size` during bursts (PostgreSQL).
- For file-based SQLite, these values are ignored (SQLite uses a NullPool by default).

### Environment Variables

For PostgreSQL, add these to your `.env` file:

```bash
DB_USER=your_postgres_user
DB_PASSWORD=your_postgres_password
```

## Database Schema

### request_logs Table

Stores all chat completion requests and responses.

| Column | Type | Description |
|--------|------|-------------|
| `id` | UUID | Primary key |
| `request_time` | DateTime | When request was received (indexed) |
| `model_name` | String | Model name used (indexed) |
| `is_stream` | Boolean | Whether streaming was used |
| `path` | String | Request path (e.g., /v1/chat/completions) |
| `method` | String | HTTP method (e.g., POST) |
| `query` | String | Query string if present |
| `headers` | JSON | Request headers (sanitized) |
| `body` | JSON | Request body as JSON |
| `route` | JSON | Routing information (list of backends tried) |
| `backend_attempts` | JSON | Backend attempts with responses |
| `stream_chunks` | JSON | Stream chunks data (if logged) |
| `errors` | JSON | Error information if any errors occurred |
| `usage_stats` | JSON | Usage statistics from the response |
| `outcome` | String | Request outcome: success, error, cancelled (indexed) |
| `duration_ms` | Integer | Request duration in milliseconds |
| `request_path` | String | Full request path including query |
| `stop_reason` | String | Finish reason from the response: stop, tool_calls, length, content_filter, etc. (indexed) |
| `full_response` | Text | Concatenated complete response text (especially for streaming) |
| `is_tool_call` | Boolean | Whether this request resulted in tool/function calls |
| `conversation_turn` | Integer | Turn number in agentic conversation sequence |
| `modules_log` | JSON | Debug logs from response modules (reasoning detection, tool calls, swaps, etc.) |
| `created_at` | DateTime | Record creation time (indexed) |

### error_logs Table

Stores error events with optional references to request logs.

| Column | Type | Description |
|--------|------|-------------|
| `id` | UUID | Primary key |
| `timestamp` | DateTime | When error occurred (indexed) |
| `model_name` | String | Associated model (indexed) |
| `error_type` | String | Error type (e.g., sse_stream_error, http_error, timeout) (indexed) |
| `error_message` | Text | Detailed error message |
| `backend_name` | String | Backend that produced error |
| `http_status` | Integer | HTTP status code if applicable |
| `request_path` | String | Request path where error occurred |
| `request_log_id` | UUID | Foreign key to request_logs.id (SET NULL on delete) |
| `extra_context` | JSON | Additional error context as JSON |
| `created_at` | DateTime | Record creation time (indexed) |

### response_states Table

Stores response objects for the Responses API with conversation chaining support.

| Column | Type | Description |
|--------|------|-------------|
| `id` | String(64) | Primary key - response ID (e.g., resp_abc123...) |
| `previous_response_id` | String(64) | ID of the previous response in the conversation chain (indexed) |
| `model` | String(255) | The model name used for this response (indexed) |
| `status` | String(32) | Response status (completed, failed, etc.) |
| `input_data` | JSON | Original input from the request (string or items array) |
| `output_data` | JSON | Response output items array |
| `full_response` | JSON | Complete response object for retrieval |
| `usage` | JSON | Token usage statistics |
| `response_metadata` | JSON | User-provided metadata |
| `expires_at` | DateTime | Expiration time for automatic cleanup (indexed) |
| `created_at` | DateTime | Record creation time |

**Indexes:**
- `ix_response_states_model_created`: Composite index on model and created_at
- `ix_response_states_previous`: Index on previous_response_id for conversation chain queries

## Database Tasks

### Running Migrations

```bash
# Apply all pending migrations
task db:migrate

# Check current revision
task db:current

# Show migration history
task db:history
```

### Creating Migrations

When you change models, create a new migration:

```bash
uv run alembic revision --autogenerate -m "your migration description"
```

### Rolling Back

```bash
# Rollback last migration
task db:rollback
```

## Switching Backends

### SQLite to PostgreSQL

1. Update `configs/config.yaml`:
   ```yaml
   database:
     backend: postgres
     connection:
       postgres:
         host: localhost
         port: 5432
         database: yallm_proxy
         user: ${DB_USER}
         password: ${DB_PASSWORD}
   ```

2. Set environment variables in `.env`:
   ```bash
   DB_USER=your_user
   DB_PASSWORD=your_password
   ```

3. Run migrations to create tables in PostgreSQL:
   ```bash
   task db:migrate
   ```

### PostgreSQL to SQLite

1. Update configuration to use `sqlite` backend
2. The application will create a new SQLite database file
3. Data is not automatically migrated - export from PostgreSQL if needed

## Usage API

The `/api/usage` endpoint now includes historical data from the database:

```json
{
  "generated_at": "2026-01-07T12:00:00Z",
  "realtime": {
    "received": 100,
    "served": 95,
    "ongoing": 5
  },
  "historical": {
    "enabled": true,
    "status": "available",
    "provider": "database",
    "total_stats": {
      "total_requests": 500,
      "successful_requests": 480,
      "failed_requests": 20
    },
    "requests_by_model": [
      {"model_name": "gpt-4", "count": 200},
      {"model_name": "claude-3", "count": 150}
    ],
    "error_rates": [...],
    "avg_response_times": [...],
    "usage_trends": [...]
  }
}
```

## Logs API

The `/api/logs` endpoint provides access to stored request logs with filtering and pagination:

```json
{
  "logs": [...],
  "total": 500,
  "limit": 100,
  "offset": 0,
  "has_more": true
}
```

**Filter parameters:**
- `model_name`: Filter by model name (partial match)
- `outcome`: Filter by outcome (success, error, cancelled)
- `stop_reason`: Filter by stop reason
- `is_tool_call`: Filter by whether tool calls were made
- `start_time`: Filter logs from this time
- `end_time`: Filter logs until this time
- `search`: Full-text search in request/response body

### Single Log Retrieval

Get detailed log information at `/api/logs/{log_id}`:

```json
{
  "id": "uuid",
  "request_time": "2026-01-07T12:00:00Z",
  "model_name": "gpt-4",
  "is_stream": true,
  "path": "/v1/chat/completions",
  "method": "POST",
  "body": {...},
  "route": [...],
  "backend_attempts": [...],
  "stream_chunks": [...],
  "errors": [...],
  "usage_stats": {...},
  "outcome": "success",
  "duration_ms": 1500,
  "stop_reason": "stop",
  "full_response": "The complete response text...",
  "is_tool_call": false,
  "conversation_turn": 1,
  "modules_log": {...},
  "error_logs": [...]
}
```

**Note:** Large fields like `stream_chunks` are limited to the first 50 chunks to prevent timeouts. Very large `body` fields are truncated to a configurable character limit.

## Architecture

```
src/database/
├── __init__.py           # Public API exports
├── base.py               # BaseDatabase abstract class
├── factory.py            # Database factory with multi-DB support
├── logger.py             # DatabaseLogRecorder for async logging
├── repository.py         # UsageRepository for statistics queries
├── logs_repository.py    # LogsRepository for log queries and filtering
├── sqlite.py             # SQLite implementation
├── postgres.py           # PostgreSQL implementation
└── models/
    ├── __init__.py       # Model exports (RequestLog, ErrorLog, ResponseState)
    ├── base.py           # SQLAlchemy base and mixins
    ├── request_log.py    # RequestLog model
    ├── error_log.py      # ErrorLog model
    └── response_state.py # ResponseState model for Responses API
```

## File Logging vs Database Logging

Both logging methods are active by default:

- **File Logging**: Writes `.log` and `.json` files to `logs/requests/`
- **Database Logging**: Stores data in SQLite/PostgreSQL

If the database is unavailable, file logging continues to work as a fallback.

## Clearing Logs

Use the `task clean` command to clear log files while preserving the database:

```bash
task clean
```

This deletes all files in `logs/` except `logs/yaLLM.db`.
