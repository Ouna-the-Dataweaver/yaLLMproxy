# Project Structure

Complete documentation of the yaLLMproxy directory structure and module purposes.

## Directory Tree

```
yaLLMproxy/
├── docs/                          # Documentation files
│   ├── api.md                     # Complete API endpoint reference
│   ├── configuration.md           # Detailed configuration guide
│   └── project-structure.md       # This file
│
├── src/                           # Source code
│   ├── __init__.py                # Main package exports
│   ├── main.py                    # FastAPI application & lifecycle
│   ├── config_loader.py           # Configuration loading with env var substitution
│   ├── config_store.py            # Config persistence & management
│   ├── usage_metrics.py           # Usage tracking & metrics
│   │
│   ├── core/                      # Core proxy functionality
│   │   ├── __init__.py            # Core module exports
│   │   ├── backend.py             # Backend dataclass & routing utilities
│   │   ├── exceptions.py          # Custom exceptions (BackendRetryableError)
│   │   ├── registry.py            # Router registry (avoids circular imports)
│   │   ├── router.py              # ProxyRouter with fallback logic
│   │   └── sse.py                 # SSE stream error detection
│   │
│   ├── api/                       # HTTP API layer
│   │   ├── __init__.py            # API module exports
│   │   └── routes/
│   │       ├── __init__.py        # Routes exports
│   │       ├── admin.py           # POST /admin/models
│   │       ├── chat.py            # POST /v1/chat/completions
│   │       ├── config.py          # GET/PUT /admin/config
│   │       ├── models.py          # GET /v1/models
│   │       ├── queue.py           # Queue management endpoints
│   │       └── usage.py           # GET /usage, GET /api/usage
│   │
│   ├── logging/                   # Request/response logging
│   │   ├── __init__.py            # Logging module exports
│   │   ├── logger.py              # Logger configuration
│   │   ├── recorder.py            # RequestLogRecorder & error logging
│   │   └── setup.py               # Logging configuration
│   │
│   ├── middleware/                # Request/response middleware
│   │   ├── __init__.py
│   │   ├── parsers.py             # Response parsing middleware
│   │   └── stateful_api.py        # Stateful API utilities
│   │
│   ├── parsers/                   # Response parsing pipeline
│   │   ├── __init__.py
│   │   └── response_pipeline.py   # Response parsing pipeline
│   │
│   ├── routing/                   # Model routing utilities
│   │   ├── __init__.py
│   │   └── model_resolver.py      # Model resolution utilities
│   │
│   └── types/                     # Type definitions
│       ├── __init__.py
│       ├── chat.py                # Chat schema types
│       └── model.py               # Model schema types
│
│
├── database/                      # Database support (SQLite/PostgreSQL)
│   ├── __init__.py                # Database module exports
│   ├── base.py                    # Base database class & connection management
│   ├── sqlite.py                  # SQLite database implementation
│   ├── postgres.py                # PostgreSQL database implementation
│   ├── factory.py                 # Database factory for creating instances
│   ├── logger.py                  # Database logger for async logging
│   ├── repository.py              # Repository classes for queries
│   └── models/                    # SQLAlchemy models
│       ├── __init__.py            # Model exports
│       ├── base.py                # Base declarative model
│       ├── request_log.py         # Request logs table (JSONB)
│       └── error_log.py           # Error logs table (JSONB)
│
├── static/                        # Static files for admin UI
│   └── admin/
│       ├── admin.html             # Admin UI
│       ├── admin.css              # Admin UI styles
│       ├── admin.js               # Admin UI JavaScript
│       ├── theme.css              # Theme styles
│       ├── theme.js               # Theme JavaScript
│       ├── ui.css                 # UI component styles
│       ├── usage.html             # Usage statistics page
│       ├── usage.css              # Usage page styles
│       └── usage.js               # Usage page JavaScript
│
├── scripts/                       # Utility scripts
│   ├── inspect_template.py        # Inspect Jinja templates for parsing
│   ├── manual_test.py             # Manual testing utilities
│   ├── print_run_config.py        # Print resolved configuration
│   ├── replay_request.py          # Replay logged requests
│   └── tcp_forward.py             # TCP forwarding script
│
├── tests/                         # Test suite
│   ├── __init__.py
│   ├── __pycache__/
│   │   └── test_proxy_app.cpython-311-pytest-9.0.2.pyc
│   └── test_proxy_app.py          # Proxy application tests
│
├── configs/                       # Configuration files
│   ├── config_default.yaml        # Default configuration
│   ├── config_added.yaml          # Runtime-added models (git-ignored)
│   ├── .env_default               # Default environment variables
│   └── .env_added                 # Added environment variables (git-ignored)
│
├── logs/                          # Log files
│   ├── requests/                  # Request/response logs
│   │   ├── YYYYMMDD_HHMMSS-<id>_<model>.log
│   │   ├── YYYYMMDD_HHMMSS-<id>_<model>.json
│   │   └── YYYYMMDD_HHMMSS-<id>_<model>.parsed.log
│   └── errors/                    # Error logs (if any)
│
├── pyproject.toml                 # Project metadata & dependencies
├── Taskfile.yml                   # Task automation (run, test, etc.)
├── README.md                      # Quick start guide
├── AGENTS.md                      # Agent-specific rules
├── template_example.jinja         # Example Jinja template
├── LICENSE                        # MIT License
│
├── install.sh                     # Installation script (Unix)
├── install.bat                    # Installation script (Windows)
├── run.sh                         # Run script (Unix)
├── run.bat                        # Run script (Windows)
├── run_forwarder.sh               # Run forwarder script (Unix)
└── run_forwarder.bat              # Run forwarder script (Windows)
```

## Module Descriptions

### Core Modules

| Module | Purpose |
|--------|---------|
| `main.py` | FastAPI application entry point, lifespan management, route registration |
| `config_loader.py` | YAML config loading with environment variable substitution |
| `config_store.py` | In-memory config store with persistence for runtime model additions |
| `usage_metrics.py` | Request counting and token usage tracking |

### Core Submodules

| Module | Purpose |
|--------|---------|
| `backend.py` | Backend dataclass, URL building, header/body transformation |
| `router.py` | ProxyRouter with fallback logic, retry handling, streaming support |
| `registry.py` | Global router registry (breaks circular imports) |
| `exceptions.py` | Custom exceptions (BackendRetryableError) |
| `sse.py` | Server-Sent Events stream error detection |

### API Routes

| Module | Endpoints | Purpose |
|--------|-----------|---------|
| `chat.py` | POST /v1/chat/completions, POST /v1/responses | Chat completion handler |
| `models.py` | GET /v1/models | List available models |
| `admin.py` | POST /admin/models | Register runtime models |
| `config.py` | GET/PUT /admin/config, GET/DELETE /admin/models/* | Config management |
| `usage.py` | GET /usage, GET /api/usage | Usage statistics |
| `queue.py` | (reserved for future use) | Queue management endpoint (not yet implemented) |

### Logging

| Module | Purpose |
|--------|---------|
| `setup.py` | Configure logging handlers and formatters |
| `recorder.py` | RequestLogRecorder for detailed request/response logging |
| `logger.py` | Logger utilities |

### Database

| Module | Purpose |
|--------|---------|
| `base.py` | BaseDatabase abstract class, connection management |
| `sqlite.py` | SQLite implementation with file-based storage |
| `postgres.py` | PostgreSQL implementation with connection pooling |
| `factory.py` | Database factory for creating interchangeable instances |
| `logger.py` | DatabaseLogRecorder for async request/error logging |
| `repository.py` | UsageRepository for querying usage statistics |

### Database Models

| Module | Purpose |
|--------|---------|
| `request_log.py` | RequestLog model with JSONB columns |
| `error_log.py` | ErrorLog model with JSONB columns |

### Middleware & Parsers

| Module | Purpose |
|--------|---------|
| `parsers.py` | Response parsing middleware |
| `stateful_api.py` | Stateful API utilities |
| `response_pipeline.py` | Response parsing pipeline |

## Configuration Flow

```
config_default.yaml ──┐
                      ├──> config_loader.py ──> config_store.py ──> ProxyRouter
config_added.yaml ────┘
```

## Request Flow

```
Client Request ──> chat.py ──> ProxyRouter ──> Backend (HTTP request)
                      │
                      └──> RequestLogRecorder (logging)
```

## Data Flow

```
Incoming Request
    │
    ▼
┌─────────────┐
│   chat.py   │  Validate request, parse JSON
└─────────────┘
    │
    ▼
┌─────────────┐
│   Router    │  Route to appropriate backend
└─────────────┘
    │
    ▼
┌─────────────┐
│   Backend   │  Transform headers/body, build URL
└─────────────┘
    │
    ▼
┌─────────────┐
│    HTTPX    │  Forward to LLM provider
└─────────────┘
    │
    ▼
┌─────────────┐
│  Parsers    │  Parse/transform response
└─────────────┘
    │
    ▼
    │
◄─┴─► Stream/Send response to client
```

## Key Classes

| Class | Location | Purpose |
|-------|----------|---------|
| `ProxyRouter` | `core/router.py` | Main routing logic, fallback handling |
| `Backend` | `core/backend.py` | Backend configuration dataclass |
| `ConfigStore` | `config_store.py` | Config management with persistence |
| `RequestLogRecorder` | `logging/recorder.py` | Detailed request/response logging |
| `ResponsePipeline` | `parsers/response_pipeline.py` | Response parsing chain |
| `SQLiteDatabase` | `database/sqlite.py` | SQLite database implementation |
| `PostgreSQLDatabase` | `database/postgres.py` | PostgreSQL database implementation |
| `DatabaseLogRecorder` | `database/logger.py` | Async database logging |
| `UsageRepository` | `database/repository.py` | Usage statistics queries |
