

# cLLMp proxy

- FastAPI app that fronts multiple OpenAI-compatible backends with retries and per-model fallback order.
- Reads LiteLLM-style configuration from `CLLMP_CONFIG` (defaults to `cLLMp/litellm_config.yaml`).
- Streams responses transparently and rewrites payloads when necessary (`target_model`, reasoning block).
- Logs every request/response to `cLLMp/logs/requests/*.log` for later inspection.

## Configuration
- Place a LiteLLM config at `cLLMp/litellm_config.yaml` or point `CLLMP_CONFIG` to another path.
- `.env` in the same directory is loaded automatically; `${VAR}` and `$VAR` inside the YAML are substituted from the environment.
- `model_list` entries define backends (`model_name`, `litellm_params.api_base`, `api_key`, `request_timeout`, `supports_reasoning`, optional `target_model`).
- `router_settings.num_retries` controls per-backend attempts; `router_settings.fallbacks` lists per-model failover order.
- `general_settings.enable_responses_endpoint` toggles the `/v1/responses` route; host/port fields are only logged (set uvicorn host/port via CLI).

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
- `supports_reasoning: true` injects `{"thinking":{"type":"enabled"}}` when absent.
- `fallbacks` accepts a string or list and updates the router’s failover map.
- `request_timeout` overrides the default 30s timeout for the backend.

## Request logging
- Logs live under `cLLMp/logs/requests/` with filenames like `YYYYMMDD_HHMMSS-<id>_<model>.log`.
- Each log captures request metadata, body, backend attempts, responses, stream chunks, errors, and final outcome.
