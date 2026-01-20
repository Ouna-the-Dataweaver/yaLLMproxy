# Project Structure

Complete documentation of the yaLLMproxy directory structure and module purposes.

## Directory Tree

```
yaLLMproxy/
├── docs/                          # Documentation files
│   ├── api.md                     # Complete API endpoint reference
│   ├── code_review.md             # Code review guidelines
│   ├── configuration.md           # Detailed configuration guide
│   ├── database.md                # Database documentation
│   ├── known_issues.md           # Known issues and workarounds
│   ├── model_tree.md             # Model inheritance tree documentation
│   └── project-structure.md       # This file
│
├── configs/                       # Configuration files
│   ├── config.yaml                # Main configuration file
│   └── jinja_templates/           # Jinja chat templates
│       ├── glm_4.5_air.jinja
│       ├── glm47.jinja
│       ├── k2thinking.jinja
│       └── template_example.jinja # Example Jinja template
│
├── external/                      # External projects and dependencies
│   ├── features.md               # External feature documentation
│   ├── kilocode/                 # Kilocode project (external reference)
│   ├── new-api/                  # New API project (external reference)
│   └── request_parsing.md        # Request parsing documentation
│
├── models/                        # Additional model files
│
├── src/                           # Source code
│   ├── __init__.py                # Main package exports
│   ├── main.py                    # FastAPI application & lifecycle
│   ├── http_forwarder.py         # HTTP reverse-proxy forwarder
│   ├── config_loader.py           # Configuration loading with env var substitution
│   ├── config_store.py            # Config persistence & management
│   ├── usage_metrics.py           # Usage tracking & metrics
│   │
│   ├── auth/                      # Authentication module
│   │   ├── __init__.py            # Auth module exports
│   │   └── app_key.py             # App key authentication
│   │
│   ├── core/                      # Core proxy functionality
│   │   ├── __init__.py            # Core module exports
│   │   ├── backend.py             # Backend dataclass & routing utilities
│   │   ├── exceptions.py          # Custom exceptions (BackendRetryableError)
│   │   ├── registry.py            # Router registry (avoids circular imports)
│   │   ├── router.py              # ProxyRouter with fallback logic
│   │   ├── sse.py                 # SSE stream error detection
│   │   └── upstream_transport.py # Upstream HTTP transport layer
│   │
│   ├── api/                       # HTTP API layer
│   │   ├── __init__.py            # API module exports
│   │   └── routes/
│   │       ├── __init__.py        # Routes exports
│   │       ├── admin.py           # POST /admin/models
│   │       ├── chat.py            # POST /v1/chat/completions
│   │       ├── config.py          # GET/PUT /admin/config
│   │       ├── embeddings.py      # POST /v1/embeddings
│   │       ├── keys.py            # API key management
│   │       ├── logs.py            # GET /admin/logs
│   │       ├── models.py          # GET /v1/models
│   │       ├── queue.py           # Queue management endpoints
│   │       ├── responses.py      # Response state management
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
│   ├── modules/                   # Request/response pipeline modules
│   │   ├── __init__.py
│   │   ├── request_pipeline.py    # Request pipeline modules
│   │   └── response_pipeline.py   # Response pipeline modules
│   │
│   ├── parsers/                   # Response module pipeline (legacy parsers alias)
│   │   ├── __init__.py
│   │   ├── response_pipeline.py   # Response parsing pipeline
│   │   └── template_analyzer.py   # Jinja template analysis
│   │
│   ├── responses/                 # Response handling utilities
│   │   ├── __init__.py
│   │   ├── state_store.py         # Response state storage
│   │   ├── stream_adapter.py      # Stream adapter utilities
│   │   └── translator.py          # Response translation
│   │
│   ├── routing/                   # Model routing utilities
│   │   ├── __init__.py
│   │   └── model_resolver.py      # Model resolution utilities
│   │
│   ├── database/                  # Database abstraction layer
│   │   ├── __init__.py
│   │   ├── base.py                # BaseDatabase abstract class
│   │   ├── factory.py             # Database factory
│   │   ├── logger.py              # DatabaseLogRecorder
│   │   ├── logs_repository.py     # LogsRepository
│   │   ├── repository.py          # UsageRepository
│   │   ├── sqlite.py              # SQLite implementation
│   │   ├── postgres.py            # PostgreSQL implementation
│   │   └── models/                # SQLAlchemy models
│   │       ├── __init__.py
│   │       ├── base.py            # Base declarative model
│   │       ├── request_log.py     # RequestLog model
│   │       ├── error_log.py       # ErrorLog model
│   │       └── response_state.py  # ResponseState model
│   │
│   ├── testing/                   # Testing utilities
│   │   ├── __init__.py
│   │   ├── fake_upstream.py       # Mock upstream server for testing
│   │   ├── proxy_harness.py       # Test harness for proxy testing
│   │   └── template_unparse.py    # Template unparser for testing
│   │
│   └── types/                     # Type definitions
│       ├── __init__.py
│       ├── chat.py                # Chat schema types
│       ├── model.py               # Model schema types
│       └── responses.py           # Response schema types
│
│
├── static/                        # Static files for admin UI
│   └── admin/
│       ├── admin.html             # Admin UI
│       ├── admin.css              # Admin UI styles
│       ├── admin.js               # Admin UI JavaScript
│       ├── logs.html              # Logs viewer page
│       ├── logs.js                # Logs viewer JavaScript
│       ├── theme.css              # Theme styles
│       ├── theme.js               # Theme JavaScript
│       ├── ui.css                 # UI component styles
│       ├── usage.html             # Usage statistics page
│       ├── usage.css              # Usage page styles
│       └── usage.js               # Usage page JavaScript
│
├── scripts/                       # Utility scripts
│   ├── db_clean.py                # Clean database logs
│   ├── inspect_template.py        # Inspect Jinja templates for parsing
│   ├── manual_test.py             # Manual testing utilities
│   ├── plan/                      # Planning and documentation
│   ├── print_run_config.py        # Print resolved configuration
│   ├── replay_request.py          # Replay logged requests
│   ├── simulate_proxy.py          # Simulate proxy behavior
│   └── tcp_forward.py             # TCP forwarding script
│
├── tests/                         # Test suite (27 test files)
│   ├── __init__.py
│   ├── __pycache__/
│   ├── chats/                     # Test chat data
│   ├── conftest.py                # Pytest configuration
│   ├── test_admin_config_masking.py
│   ├── test_backend.py
│   ├── test_config_loader.py
│   ├── test_config_reload.py
│   ├── test_config_store.py
│   ├── test_database.py
│   ├── test_database_logger.py
│   ├── test_embeddings.py
│   ├── test_exceptions.py
│   ├── test_http_forwarder.py
│   ├── test_log_routing.py
│   ├── test_logs_repository.py
│   ├── test_masking.py
│   ├── test_model_inheritance.py
│   ├── test_model_tree.py
│   ├── test_multiple_tool_calls.py
│   ├── test_parse_unparsed.py
│   ├── test_parser_template.py
│   ├── test_print_run_config.py
│   ├── test_proxy_app.py
│   ├── test_router_streaming.py
│   ├── test_simulated_upstream.py
│   ├── test_sse.py
│   ├── test_stream_parity.py
│   ├── test_tcp_forwarder.py
│   └── test_template_parse.py
│
├── logs/                          # Log files
│   ├── errors/                    # Error logs
│   ├── requests/                  # Request/response logs
│   │   ├── YYYYMMDD_HHMMSS-<id>_<model>.log
│   │   ├── YYYYMMDD_HHMMSS-<id>_<model>.json
│   │   └── YYYYMMDD_HHMMSS-<id>_<model>.parsed.log
│   ├── tests/                     # Test logs
│   └── yaLLM.db                   # SQLite database (if using SQLite)
│
├── __init__.py                    # Package initialization
├── AGENTS.md                      # Agent-specific rules
├── extract_model_output.py        # Model output extraction utility
├── install.bat                    # Installation script (Windows)
├── install.sh                     # Installation script (Unix)
├── LICENSE                        # MIT License
├── pyproject.toml                 # Project metadata & dependencies
├── README.md                      # Quick start guide
├── run.bat                        # Run script (Windows)
├── run.sh                         # Run script (Unix)
├── run_forwarder.bat              # Run TCP forwarder script (Windows)
├── run_forwarder.sh               # Run TCP forwarder script (Unix)
├── run_http_forwarder.bat         # Run HTTP forwarder script (Windows)
├── run_http_forwarder.sh          # Run HTTP forwarder script (Unix)
├── Taskfile.yml                   # Task automation (run, test, etc.)
└── uv.lock                        # UV lock file for dependency management
```

## Module Descriptions

### Core Modules

| Module | Purpose |
|--------|---------|
| `main.py` | FastAPI application entry point, lifespan management, route registration |
| `config_loader.py` | YAML config loading with environment variable substitution |
| `config_store.py` | In-memory config store with persistence for runtime model additions |
| `usage_metrics.py` | Request counting and token usage tracking |
| `http_forwarder.py` | HTTP reverse-proxy forwarder for forwarding requests |

### Authentication

| Module | Purpose |
|--------|---------|
| `app_key.py` | App key authentication for API access control |

### Core Submodules

| Module | Purpose |
|--------|---------|
| `backend.py` | Backend dataclass, URL building, header/body transformation |
| `router.py` | ProxyRouter with fallback logic, retry handling, streaming support |
| `registry.py` | Global router registry (breaks circular imports) |
| `exceptions.py` | Custom exceptions (BackendRetryableError) |
| `sse.py` | Server-Sent Events stream error detection |
| `upstream_transport.py` | Upstream HTTP transport layer for connecting to backends |

### API Routes

| Module | Endpoints | Purpose |
|--------|-----------|---------|
| `chat.py` | POST /v1/chat/completions, POST /v1/responses | Chat completion handler |
| `models.py` | GET /v1/models | List available models |
| `admin.py` | POST /admin/models | Register runtime models |
| `config.py` | GET/PUT /admin/config, GET/DELETE /admin/models/* | Config management |
| `usage.py` | GET /usage, GET /api/usage | Usage statistics |
| `queue.py` | (reserved for future use) | Queue management endpoint (not yet implemented) |
| `embeddings.py` | POST /v1/embeddings | Embeddings generation endpoint |
| `keys.py` | API key management endpoints | Manage API keys |
| `logs.py` | GET /admin/logs | Admin logs endpoint |
| `responses.py` | Response state management endpoints | Manage response states |

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
| `logs_repository.py` | Request log repository for querying request logs |
| `repository.py` | UsageRepository for querying usage statistics |

### Database Models

| Module | Purpose |
|--------|---------|
| `base.py` | Base declarative model for SQLAlchemy |
| `request_log.py` | RequestLog model with JSONB columns |
| `error_log.py` | ErrorLog model with JSONB columns |
| `response_state.py` | Response state model for tracking response states |

### Middleware & Parsers

| Module | Purpose |
|--------|---------|
| `parsers.py` | Response parsing middleware |
| `stateful_api.py` | Stateful API utilities |
| `response_pipeline.py` | Response parsing pipeline |

### Response Handling

| Module | Purpose |
|--------|---------|
| `state_store.py` | Response state storage for tracking response progress |
| `stream_adapter.py` | Stream adapter utilities for handling streaming responses |
| `translator.py` | Response translation between different formats |

### Routing

| Module | Purpose |
|--------|---------|
| `model_resolver.py` | Model resolution utilities for routing to correct backend |

### Testing

| Module | Purpose |
|--------|---------|
| `fake_upstream.py` | Mock upstream server for testing proxy behavior |
| `proxy_harness.py` | Test harness for proxy testing utilities |
| `template_unparse.py` | Template unparser for testing template processing |

## Configuration Flow

```
config.yaml ──┐
                      ├──> config_loader.py ──> config_store.py ──> ProxyRouter
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
| `LogsRepository` | `database/logs_repository.py` | Request log queries |
| `UpstreamTransport` | `core/upstream_transport.py` | Upstream HTTP transport layer |
| `StateStore` | `responses/state_store.py` | Response state storage |
| `ModelResolver` | `routing/model_resolver.py` | Model resolution and routing |