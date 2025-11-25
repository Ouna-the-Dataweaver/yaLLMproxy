

# y(et) a(nother) LLM proxy (yallmp)

- FastAPI app that fronts multiple OpenAI-compatible backends with retries and per-model fallback order.
- Reads LiteLLM-style configuration from `YALLMP_CONFIG` (defaults to `litellm_config.yaml`).
- Streams responses transparently and rewrites payloads when necessary (`target_model`, reasoning block).
- Logs every request/response to `logs/requests/*.log` for later inspection. (TODO: make this optional)

## Installation

### Linux/macOS
```bash
# Install dependencies and create virtual environment
./install.sh

# Run the proxy
./run.sh
```

### Windows
```cmd
# Install dependencies and create virtual environment
install.bat

# Run the proxy
run.bat
```

Both platforms require `uv` package manager to be installed. Install it from https://github.com/astral-sh/uv if you don't have it already. 

## Configuration
- Place a LiteLLM config at `litellm_config.yaml` or point `YALLM_CONFIG` to another path.
- `.env` in the same directory is loaded automatically; `${VAR}` and `$VAR` inside the YAML are substituted from the environment.
- `model_list` entries define backends (`model_name`, `litellm_params.api_base`, `api_key`, `request_timeout`, `supports_reasoning`, optional `target_model`).
- `router_settings.num_retries` controls per-backend attempts; `router_settings.fallbacks` lists per-model failover order.

## Endpoints
- `POST /v1/chat/completions` — OpenAI-compatible chat completions with optional streaming.
- `POST /v1/responses` — OpenAI Responses API (only if enabled in config).
- `GET /v1/models` — Lists currently registered models.
- `POST /admin/models` — Register or replace a backend at runtime.

## Runtime model registration
Send a JSON body to `/admin/models`:

```bash
curl -X POST http://localhost:17771/admin/models \
  -H "Content-Type: application/json" \
  -d '{
    "model_name": "user123_model",
    "api_base": "https://api.example.com/v1",
    "api_key": "secret",
    "target_model": "gpt-4o",
    "request_timeout": 60,
    "supports_reasoning": true,
    "fallbacks": ["backup-model"]
  }'
```

Fields:
- `model_name` (required) registers the backend and becomes the OpenAI `model`.
- `api_base` and `api_key` point to the upstream.
- `target_model` rewrites outbound payloads when the upstream expects a different name.
- `supports_reasoning: true` injects `{"thinking":{"type":"enabled"}}` when absent. (TODO do a research on how this is actually handled in different providers, since some have reasoning_effort fiels, etc)
- `fallbacks` accepts a string or list and updates the router's failover map.
- `request_timeout` overrides the default 30s timeout for the backend.

## Request logging
- Logs live under `logs/requests/` with filenames like `YYYYMMDD_HHMMSS-<id>_<model>.log`.
- Each log captures request metadata, body, backend attempts, responses, stream chunks, errors, and final outcome.
- Extremely useful for debugging your agent applications, debugging LLM calls (reasoning/tool parsing), and looking into what applications like cursor/kilo code are actually sending

## Request replay
Useful when you're debugging your LLM provider or inference. For example it's easy to reproduce streaming bugs that way by overriding the steam flag in the script. 

The `replay_request.py` script replays logged requests against the same endpoint, extracting method, path, headers, body and streaming flag from log files.

### Basic usage

```bash
# Replay a logged request (derives URL from Host header in log)
python replay_request.py logs/requests/20231125_143052-abc123_gpt-4o.log

# Replay with explicit base URL
python replay_request.py logs/requests/20231125_143052-abc123_gpt-4o.log --base-url http://localhost:17771
```

### Advanced options

```bash
# Override the model name in the request body
python replay_request.py logs/requests/20231125_143052-abc123_gpt-4o.log --model gpt-3.5-turbo

# Force streaming mode on/off (useful for reproducing streaming bugs)
python replay_request.py logs/requests/20231125_143052-abc123_gpt-4o.log --stream-mode on
python replay_request.py logs/requests/20231125_143052-abc123_gpt-4o.log --stream-mode off

# Print equivalent curl command without sending request
python replay_request.py logs/requests/20231125_143052-abc123_gpt-4o.log --print-curl

# Dry run - show what would be sent without actually sending
python replay_request.py logs/requests/20231125_143052-abc123_gpt-4o.log --dry-run

# Custom timeout
python replay_request.py logs/requests/20231125_143052-abc123_gpt-4o.log --timeout 120
```

## On fly patching

TODO: patching requests on the fly: fix reasoning field names, tool calls
TODO: stateful proxy: enable interleaved thinking for any model (especially useful for MiniMax M2 / Kimi K2 thinking since they expect interleaved thinking mode but almost all agentic applications erase reasoning traces on followup requests)