# Database Support

yaLLMproxy supports SQLite (default) and PostgreSQL databases for persistent logging and usage tracking.

## Overview

The database module provides:
- **Request Logging**: Store all request/response data with JSONB columns
- **Error Tracking**: Log errors with optional references to request logs
- **Usage Statistics**: Query historical usage data by model, time period, etc.
- **Interchangeable Backends**: Switch between SQLite and PostgreSQL without code changes

## Configuration

Add database settings to your `configs/config_default.yaml`:

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

### Environment Variables

For PostgreSQL, add these to your `.env_added` file:

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
| `path` | String | Request path |
| `method` | String | HTTP method |
| `query` | String | Query string |
| `headers` | JSONB | Request headers (sanitized) |
| `body` | JSONB | Request body |
| `route` | JSONB | Routing information |
| `backend_attempts` | JSONB | Backend attempts with responses |
| `stream_chunks` | JSONB | Stream chunk data |
| `errors` | JSONB | Error information |
| `usage_stats` | JSONB | Token usage statistics |
| `outcome` | String | success/error/cancelled (indexed) |
| `duration_ms` | Integer | Request duration |
| `created_at` | DateTime | Record creation time (indexed) |

### error_logs Table

Stores error events with optional references to request logs.

| Column | Type | Description |
|--------|------|-------------|
| `id` | UUID | Primary key |
| `timestamp` | DateTime | When error occurred (indexed) |
| `model_name` | String | Associated model (indexed) |
| `error_type` | String | Error type (indexed) |
| `error_message` | Text | Detailed error message |
| `backend_name` | String | Backend that produced error |
| `http_status` | Integer | HTTP status code |
| `request_path` | String | Request path |
| `request_log_id` | UUID | Foreign key to request_log |
| `extra_context` | JSONB | Additional context |
| `created_at` | DateTime | Record creation time (indexed) |

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

1. Update `configs/config_default.yaml`:
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

2. Set environment variables in `.env_added`:
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

## Architecture

```
src/database/
├── __init__.py           # Public API exports
├── base.py               # BaseDatabase abstract class
├── sqlite.py             # SQLite implementation
├── postgres.py           # PostgreSQL implementation
├── factory.py            # Database factory
├── logger.py             # DatabaseLogRecorder
├── repository.py         # UsageRepository
└── models/
    ├── __init__.py       # Model exports
    ├── base.py           # SQLAlchemy base
    ├── request_log.py    # RequestLog model
    └── error_log.py      # ErrorLog model
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
