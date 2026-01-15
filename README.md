# y(et) a(nother) LLM proxy (yallmp)

Yet Another LLM Proxy - A lightweight, modular LLM proxy with OpenAI-compatible API, fallback routing, and response parsing.

## Features

- **Modular Architecture**: Clean separation of concepts with dedicated modules for routing, logging, API endpoints, and configuration
- **Backend Failover**: Automatically routes to fallback backends when primary backends fail
- **Request/Response Logging**: Detailed logs of all requests and responses for debugging
- **Database Support**: SQLite (default) or PostgreSQL for persistent logging with JSONB columns
- **OpenAI Compatibility**: Works with OpenAI-compatible clients and tools
- **Runtime Registration**: Register new backends without restarting the proxy
- **Environment Variable Support**: Configure via environment variables in YAML files
- **Streaming Support**: Transparent handling of streaming responses with SSE error detection
- **Model Inheritance**: Create derived models with configuration overrides (e.g., `GLM-4.7:Cursor` extends `GLM-4.7`)
- **Model Copying**: Duplicate existing models via API for easy configuration reuse
- **Template-Based Parsing**: Use Jinja2 templates for custom response parsing and extraction
- **Logs Viewer**: Built-in UI for browsing and analyzing request logs with filtering

## Installation

### Prerequisites

- Python 3.10+
- [uv](https://github.com/astral-sh/uv) package manager
- [task](https://taskfile.dev/) for running automation tasks

Install `task` on your platform:

**Linux/macOS:**
```bash
sh -c "$(curl -sSL https://taskfile.dev/install.sh)"
```

**Windows (winget):**
```bash
winget install go-task.go-task
```
OR
```bash
choco install go-task
```

**macOS (Homebrew):**
```bash
brew install go-task
```

### Install with uv

```bash
# Install dependencies
uv sync

# Configure your API keys in configs/.env
# Edit configs/config.yaml to add your models

# Run the proxy
uv run python -m src.main
# Or use: task run
```

### Alternative: Using scripts

```bash
# Linux/macOS
./install.sh
./run.sh

# Windows
install.bat
run.bat

# Run with autoreload (development)
task run:reload
```

### Quick Test

```bash
# List available models
curl http://localhost:7979/v1/models
```

### Configuration Structure

```yaml
model_list:
  - model_name: my-model           # Display name for the model
    protected: true                # Requires admin password to edit/delete
    model_params:
      api_base: https://api.example.com/v1  # Backend URL
      api_key: sk-xxx              # API key (use env vars for security)
      model: gpt-4o                # Upstream model name (optional)
      request_timeout: 30          # Timeout in seconds (optional)
      target_model: gpt-4          # Rewrite model name in requests (optional)
      supports_reasoning: false    # Enable reasoning block injection (optional)
      api_type: openai             # API type: openai, anthropic, etc. (optional)
    parameters:                    # Per-model parameter defaults (optional)
      temperature:
        default: 1.0               # Default value if not specified in request
        allow_override: false      # If true, use request value if provided (default: true)

proxy_settings:
  server:
    host: 127.0.0.1                # Bind address
    port: 7979                     # Port number
  enable_responses_endpoint: false # Enable /v1/responses endpoint
  logging:                         # Request logging options (optional)
    log_parsed_response: false     # Write parsed non-stream responses to *.parsed.log
    log_parsed_stream: false       # Write parsed stream chunks to *.parsed.log
  parsers:                         # Response parser pipeline (optional)
    enabled: false
    response:                      # Ordered response parsers
      - parse_tags
      - swap_reasoning_content
    paths:                         # Apply to paths containing any of these strings
      - /chat/completions
    parse_tags:                    # Parse <think> / <tool_call> tags into structured fields
      template_path: ""            # Optional: auto-detect config from Jinja template
      parse_thinking: true
      parse_tool_calls: true
      think_tag: think
      tool_arg_format: xml         # xml | json (for K2-style JSON arguments)
      tool_tag: tool_call          # For xml format
      tool_open: "<tool_call>"     # Custom open delimiter (auto-set for json format)
      tool_close: "</tool_call>"   # Custom close delimiter (auto-set for json format)
      tool_buffer_limit: 200       # Optional: max buffered chars before treating as literal
    swap_reasoning_content:        # Swap reasoning_content <-> content
      mode: reasoning_to_content   # reasoning_to_content | content_to_reasoning | auto
      think_tag: think
      think_open:
        prefix: ""                 # Prefix before <think>
        suffix: ""                 # Suffix after <think>
      think_close:
        prefix: ""                 # Prefix before </think>
        suffix: ""                 # Suffix after </think>
      include_newline: true        # Default true: add newline between </think> and content

forwarder_settings:
  listen:
    host: 0.0.0.0                  # External listen address
    port: 6969                     # External listen port
  target:
    host: 127.0.0.1                # Proxy host
    port: 7979                     # Proxy port

http_forwarder_settings:
  preserve_host: true              # Preserve Host header (recommended)
  listen:
    host: 0.0.0.0                  # External listen address
    port: 6969                     # External listen port
  target:
    scheme: http
    host: 127.0.0.1
    port: 7979
```

Protected models require `YALLMP_ADMIN_PASSWORD` in `configs/.env` for edits/deletes via admin APIs.

Per-model parser overrides can be added to individual entries in `model_list`.
When present, they replace the global `proxy_settings.parsers` config for that model.
If `enabled` is omitted, per-model parsers default to enabled (set `enabled: false`
to explicitly disable parsing on that model).

Parsing notes: `parse_tags` can auto-detect think/tool tags and argument format from
a Jinja template when `template_path` is provided. Use `tool_arg_format: json` for
K2-style models that output JSON arguments (e.g., `<|tool_call_begin|>func<|arg|>{"key":"val"}`).
Tool parsing buffers until a full block is parsed; if `tool_buffer_limit` is set and
exceeded, the tag is treated as literal text.

To help align formatting with a Jinja chat template, you can inspect a template
and print suggested `think_open`/`think_close` prefixes and suffixes:

```bash
uv run python scripts/inspect_template.py configs/jinja_templates/template_example.jinja
```

Copy the suggested values into your `swap_reasoning_content` config. If you set
`think_close.suffix` to include a newline, consider setting `include_newline: false`
to avoid double newlines.

```yaml
model_list:
  - model_name: GLM-4.7
    model_params:
      api_base: https://api.z.ai/api/coding/paas/v4
      api_key: ${GLM_API_KEY}
    parsers:
      enabled: true
      response:
        - swap_reasoning_content
      swap_reasoning_content:
        mode: reasoning_to_content
```

## Forwarders

### TCP forwarder

The TCP forwarder is optional but useful when you need a separate inbound port or a separate process for the inbound traffic (e.g. you have a VPN which you must use for API access, but it breaks inbound traffic (or WSL shenanigans); you can whitelist the forwarder executable/process, or run the forwarder in Windows, and keep the proxy running under VPN/in WSL).

It reads `forwarder_settings` from `configs/config.yaml`. You can override with
`FORWARD_LISTEN_HOST`, `FORWARD_LISTEN_PORT`, `FORWARD_TARGET_HOST`,
`FORWARD_TARGET_PORT` at runtime.

### HTTP forwarder (reverse proxy)

Use the HTTP forwarder when you want a protocol-aware reverse proxy (e.g., to avoid connection resets on large JSON responses). It preserves the `Host` header by default and streams SSE responses.

It reads `http_forwarder_settings` from `configs/config.yaml`. You can override with
`HTTP_FORWARD_LISTEN_HOST`, `HTTP_FORWARD_LISTEN_PORT`, `HTTP_FORWARD_TARGET_SCHEME`,
`HTTP_FORWARD_TARGET_HOST`, `HTTP_FORWARD_TARGET_PORT`, and `HTTP_FORWARD_PRESERVE_HOST`.

Note: both forwarders default to port `6969`. If you want to run both at once, change one of their ports.

## Documentation
- [API Reference](docs/api.md) - Complete endpoint documentation
- [Configuration Guide](docs/configuration.md) - Config options, environment variables
- [Project Structure](docs/project-structure.md) - Directory layout and module descriptions
- [Known Issues](docs/known_issues.md) - Known bugs, limitations, and workarounds


```
yaLLMproxy/
├── src/                  # Source code
│   ├── modules/           # Request/response pipeline modules
│   ├── parsers/           # Response module pipeline (legacy parsers alias)
│   ├── core/              # Core proxy functionality
│   ├── api/               # HTTP API layer
│   ├── database/           # Database support
│   ├── logging/           # Request/response logging
│   ├── middleware/        # Request/response middleware
│   ├── routing/           # Model routing utilities
│   └── types/             # Type definitions
├── static/admin/         # Admin UI (includes logs viewer)
├── configs/              # Configuration files
│   └── jinja_templates/  # Jinja2 templates for parsing
├── docs/                 # Detailed documentation
├── scripts/              # Utility scripts
└── tests/                # Test suite
```

## Commands

| Command | Description |
|---------|-------------|
| `task run` | Start the proxy |
| `task run:reload` | Start with autoreload (development) |
| `task test` | Run tests |
| `task forwarder` | Run TCP forwarder |
| `task forwarder:http` | Run HTTP forwarder |
| `uv run pytest tests/` | Run tests directly |

## Runtime Management

### Hot Reload Configuration

You can reload the proxy configuration without restarting by calling the admin API:

```bash
curl -X POST http://localhost:7979/admin/config/reload
```

Or use the **Reload** button in the admin UI (`/admin/`).

This will:
1. Reload `config.yaml` from disk
2. Re-parse all models and backends
3. Update the router's runtime state atomically
4. Apply model inheritance changes to derived models

Useful for applying config changes without interrupting active requests. Note: Changes to base models update derived models in runtime config, but require router reload to apply to active backends.

## Database Configuration

yaLLMproxy supports SQLite (default) and PostgreSQL databases for persistent logging.

### SQLite (Default)

SQLite is enabled by default with no additional configuration:

```yaml
database:
  backend: sqlite
  connection:
    sqlite:
      path: logs/yaLLM.db
```

### PostgreSQL

To use PostgreSQL, update your configuration:

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

Add credentials to your `.env` file:

```bash
DB_USER=your_postgres_user
DB_PASSWORD=your_postgres_password
```

### Database Tasks

```bash
task db:migrate    # Run database migrations
task db:rollback   # Rollback last migration
task db:current    # Show current revision
task db:history    # Show migration history
task clean         # Clear logs (preserves database)
```

### See Also

- [Database Documentation](docs/database.md) - Detailed database guide
- [Project Structure](docs/project-structure.md) - Module descriptions

## License

MIT
