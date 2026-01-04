# y(et) a(nother) LLM proxy (yallmp)

Yet Another LLM Proxy - A lightweight, modular LLM proxy with OpenAI-compatible API, fallback routing, and response parsing.

## Features

- **Modular Architecture**: Clean separation of concepts with dedicated modules for routing, logging, API endpoints, and configuration
- **Backend Failover**: Automatically routes to fallback backends when primary backends fail
- **Request/Response Logging**: Detailed logs of all requests and responses for debugging
- **OpenAI Compatibility**: Works with OpenAI-compatible clients and tools
- **Runtime Registration**: Register new backends without restarting the proxy
- **Environment Variable Support**: Configure via environment variables in YAML files
- **Streaming Support**: Transparent handling of streaming responses with SSE error detection
- **Model Inheritance**: Create derived models with configuration overrides (e.g., `GLM-4.7:Cursor` extends `GLM-4.7`)
- **Model Copying**: Duplicate existing models via API for easy configuration reuse

## Installation

### Prerequisites

- Python 3.10+
- [uv](https://github.com/astral-sh/uv) package manager
- [Taskfile](https://taskfile.dev/) for running tasks
### Install with uv

```bash
# Install dependencies
uv sync

# Configure your API keys in configs/.env_default
# Edit configs/config_default.yaml to add your models

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
      - parse_unparsed
      - swap_reasoning_content
    paths:                         # Apply to paths containing any of these strings
      - /chat/completions
    parse_unparsed:                # Parse <think> / <tool_call> tags into structured fields
      parse_thinking: true
      parse_tool_calls: true
      think_tag: think
      tool_tag: tool_call
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
```

Per-model parser overrides can be added to individual entries in `model_list`.
When present, they replace the global `proxy_settings.parsers` config for that model.
If `enabled` is omitted, per-model parsers default to enabled (set `enabled: false`
to explicitly disable parsing on that model).

To help align formatting with a Jinja chat template, you can inspect a template
and print suggested `think_open`/`think_close` prefixes and suffixes:

```bash
uv run python scripts/inspect_template.py template_example.jinja
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

## Forwarder

The TCP forwarder is optional but useful when you need a separate inbound port or a separate process for the inbound traffic (e.g. you have a VPN which you must use for API access, but it breaks inbound traffic(or WSL shenanigans), in that case you can whitelist the forwarder executable/process, or run forwader in windows, and keep proxy running under VPN/in WSL etc.):

It reads `forwarder_settings` from `configs/config_default.yaml`. You can override with
`FORWARD_LISTEN_HOST`, `FORWARD_LISTEN_PORT`, `FORWARD_TARGET_HOST`,
`FORWARD_TARGET_PORT` at runtime.

## Documentation
- [API Reference](docs/api.md) - Complete endpoint documentation
- [Configuration Guide](docs/configuration.md) - Config options, environment variables
- [Project Structure](docs/project-structure.md) - Directory layout and module descriptions
- [Known Issues](docs/known_issues.md) - Known bugs, limitations, and workarounds


```
yaLLMproxy/
├── src/                  # Source code
├── static/admin/         # Admin UI
├── configs/              # Configuration files
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
| `uv run pytest tests/` | Run tests directly |

## Runtime Management

### Hot Reload Configuration

You can reload the proxy configuration without restarting by calling the admin API:

```bash
curl -X POST http://localhost:7979/admin/config/reload
```

Or use the **Reload** button in the admin UI (`/admin/`).

This will:
1. Reload `config_default.yaml` and `config_added.yaml` from disk
2. Re-parse all models and backends
3. Update the router's runtime state atomically

Useful for applying config changes without interrupting active requests.

## License

MIT
