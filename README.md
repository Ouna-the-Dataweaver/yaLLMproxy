# y(et) a(nother) LLM proxy (yallmp)

A lightweight, modular LLM proxy that routes requests to multiple backends with automatic failover, comprehensive logging, and OpenAI-compatible endpoints.

## Features

- **Modular Architecture**: Clean separation of concepts with dedicated modules for routing, logging, API endpoints, and configuration
- **Backend Failover**: Automatically routes to fallback backends when primary backends fail
- **Request/Response Logging**: Detailed logs of all requests and responses for debugging
- **OpenAI Compatibility**: Works with OpenAI-compatible clients and tools
- **Runtime Registration**: Register new backends without restarting the proxy
- **Environment Variable Support**: Configure via environment variables in YAML files
- **Streaming Support**: Transparent handling of streaming responses with SSE error detection

## Installation

### Prerequisites

- Python 3.10+
- [uv](https://github.com/astral-sh/uv) package manager

### Install with uv

```bash
# Clone the repository
cd yaLLMproxy

# Create virtual environment and install dependencies
uv venv
uv sync --extra dev

# Run the proxy
uv run uvicorn src.main:app --host 0.0.0.0 --port 8000
```

### Alternative: Using scripts

```bash
# Linux/macOS
./install.sh
./run.sh

# Windows
install.bat
run.bat
```

## Configuration

### Config File

Default configuration lives at `configs/config.yaml`. You can override it by setting `YALLMP_CONFIG` to another path:

```bash
export YALLMP_CONFIG=/path/to/your/config.yaml
```

### Environment Variables

Environment variables can be substituted in the YAML configuration using `${VAR}` or `$VAR` syntax. These will be loaded from the `.env` file in the same directory as the config file (e.g., `configs/.env` when using `configs/config.yaml`). If an environment variable is not found, a **warning is logged** and the literal placeholder remains in the config (requests will likely fail).

**Priority:** `.env` file values take precedence over shell environment variables. If you set `MY_VAR=abc` in your shell and also have `MY_VAR=xyz` in your `.env` file, the value `xyz` from the `.env` file will be used.

```yaml
model_list:
  - model_name: gpt-4
    litellm_params:
      api_base: https://api.openai.com/v1
      api_key: ${OPENAI_API_KEY}  # Loaded from .env or shell env
```

### Configuration Structure

```yaml
model_list:
  - model_name: my-model           # Display name for the model
    litellm_params:
      api_base: https://api.example.com/v1  # Backend URL
      api_key: sk-xxx              # API key (use env vars for security)
      model: gpt-4o                # Upstream model name (optional)
      request_timeout: 30          # Timeout in seconds (optional)
      target_model: gpt-4          # Rewrite model name in requests (optional)
      supports_reasoning: false    # Enable reasoning block injection (optional)
      api_type: openai             # API type: openai, anthropic, etc. (optional)

router_settings:
  num_retries: 1                   # Number of retry attempts per backend
  fallbacks:
    - my-model: [fallback-model]   # Fallback order for each model

proxy_settings:
  server:
    host: 127.0.0.1                # Bind address
    port: 7978                     # Port number
  enable_responses_endpoint: false # Enable /v1/responses endpoint

forwarder_settings:
  listen:
    host: 0.0.0.0                  # External listen address
    port: 7979                     # External listen port
  target:
    host: 127.0.0.1                # Proxy host
    port: 7978                     # Proxy port
```

## Forwarder

The TCP forwarder is optional but useful when you need a separate inbound port or a separate process for the inbound traffic (e.g. you have a VPN which you must use for API access, but it breaks inbound traffic(or WSL shenanigans), in that case you can whitelist the forwarder executable/process, or run forwader in windows, and keep proxy running under VPN/in WSL etc.):

It reads `forwarder_settings` from `configs/config.yaml`. You can override with
`FORWARD_LISTEN_HOST`, `FORWARD_LISTEN_PORT`, `FORWARD_TARGET_HOST`,
`FORWARD_TARGET_PORT` at runtime.

## Project Structure

```
yaLLMproxy/
├── src/
│   ├── __init__.py              # Main package exports
│   ├── main.py                  # FastAPI application & lifecycle
│   ├── config_loader.py         # Configuration loading with env var substitution
│   ├── core/
│   │   ├── __init__.py          # Core module exports
│   │   ├── backend.py           # Backend dataclass & routing utilities
│   │   ├── exceptions.py        # Custom exceptions
│   │   ├── registry.py          # Router registry (breaks circular imports)
│   │   ├── router.py            # ProxyRouter with fallback logic
│   │   └── sse.py               # SSE stream error detection
│   ├── api/
│   │   ├── __init__.py          # API module exports
│   │   └── routes/
│   │       ├── __init__.py      # Routes exports
│   │       ├── admin.py         # POST /admin/models
│   │       ├── chat.py          # POST /v1/chat/completions
│   │       └── models.py        # GET /v1/models
│   ├── logging/
│   │   ├── __init__.py          # Logging module exports
│   │   ├── recorder.py          # RequestLogRecorder & error logging
│   │   └── setup.py             # Logging configuration
│   ├── middleware/              # Request/response middleware
│   ├── routing/                 # Model routing utilities
│   └── types/                   # Type definitions (chat, model schemas)
├── tests/                       # Test suite
├── config.yaml                  # Configuration file
├── pyproject.toml               # Project metadata & dependencies
└── README.md                    # This file
```

## API Endpoints

### Chat Completions

```http
POST /v1/chat/completions
```

OpenAI-compatible chat completions endpoint. Supports both streaming and non-streaming responses.

### Models List

```http
GET /v1/models
```

Lists all currently registered models in OpenAI format.

### Runtime Model Registration

```http
POST /admin/models
```

Register or replace a backend at runtime without restarting the proxy:

```bash
curl -X POST http://localhost:8000/admin/models \
  -H "Content-Type: application/json" \
  -d '{
    "model_name": "my-model",
    "api_base": "https://api.example.com/v1",
    "api_key": "secret-key",
    "target_model": "gpt-4",
    "request_timeout": 60,
    "supports_reasoning": true,
    "fallbacks": ["backup-model"]
  }'
```

### Responses Endpoint (Optional)

```http
POST /v1/responses
```

OpenAI Responses API endpoint. Only available if `enable_responses_endpoint` is set to `true` in the configuration.

## Request Logging

All requests and responses are logged to `logs/requests/` with detailed information including:

- Request metadata (method, path, headers, body)
- Backend routing information
- Backend attempts and responses
- Stream chunks (for streaming responses)
- Errors and final outcomes

Logs are stored as text files with names like `YYYYMMDD_HHMMSS-<id>_<model>.log`.
A JSON sidecar (`.json`) with the original request is written alongside each log.
Errors are additionally logged in `logs/errors/`.

### Sensitive Data Masking

Authorization and proxy-related headers are masked in logs (Bearer tokens show only the first 3 characters). Request bodies are logged verbatim, so avoid placing secrets there or disable logging if needed.

## Request Replay

The `replay_request.py` script in the scripts directory allows you to replay logged requests for debugging:

```bash
# Replay a logged request (use the .json sidecar)
python scripts/replay_request.py logs/requests/20231125_143052-abc123_gpt-4o.json

# With explicit base URL
python scripts/replay_request.py logs/requests/20231125_143052-abc123_gpt-4o.json --base-url http://localhost:8000

# Override model name
python scripts/replay_request.py logs/requests/20231125_143052-abc123_gpt-4o.json --model gpt-3.5-turbo

# Force streaming mode
python scripts/replay_request.py logs/requests/20231125_143052-abc123_gpt-4o.json --stream-mode on

# Print curl command without sending
python scripts/replay_request.py logs/requests/20231125_143052-abc123_gpt-4o.json --print-curl
```

## Running Tests

```bash
# Run all tests
uv sync --extra dev
uv run pytest tests/ -v

# Run specific test file
uv run pytest tests/test_backend.py -v

# Run with coverage
uv run pytest tests/ --cov=src --cov-report=html
```

## Development

### Code Structure

The project follows a modular architecture:

- **Core**: Backend routing, error handling, and SSE processing
- **API**: HTTP endpoint handlers
- **Logging**: Request/response recording and error logging
- **Config**: Configuration loading with environment variable support

### Adding New Features

1. Create new modules in appropriate directories
2. Update `__init__.py` files to export new functionality
3. Add tests for new functionality
4. Update documentation

## License

MIT
