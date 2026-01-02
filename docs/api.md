# API Reference

Complete documentation for all yaLLMproxy API endpoints.

## Core Endpoints

### Chat Completions

```http
POST /v1/chat/completions
```

OpenAI-compatible chat completions endpoint. Supports both streaming and non-streaming responses.

**Request Body:**

```json
{
  "model": "model-name",
  "messages": [
    {"role": "system", "content": "You are helpful."},
    {"role": "user", "content": "Hello, world!"}
  ],
  "temperature": 0.7,
  "max_tokens": 1000,
  "stream": false
}
```

**Response (non-streaming):**

```json
{
  "id": "chatcmpl-abc123",
  "object": "chat.completion",
  "created": 1677858242,
  "model": "model-name",
  "choices": [
    {
      "index": 0,
      "message": {
        "role": "assistant",
        "content": "Hello! How can I help you today?"
      },
      "finish_reason": "stop"
    }
  ],
  "usage": {
    "prompt_tokens": 15,
    "completion_tokens": 12,
    "total_tokens": 27
  }
}
```

### Models List

```http
GET /v1/models
```

Lists all currently registered models in OpenAI format.

**Response:**

```json
{
  "object": "list",
  "data": [
    {
      "id": "gpt-4",
      "object": "model",
      "created": 1677858242,
      "owned_by": "yallmp-proxy"
    }
  ]
}
```

### Responses Endpoint (Optional)

```http
POST /v1/responses
```

OpenAI Responses API endpoint. Only available if `enable_responses_endpoint` is set to `true` in `proxy_settings` in the configuration. This endpoint is disabled by default.

---

## Admin Endpoints

### Register Runtime Model

```http
POST /admin/models
```

Register or replace a backend at runtime without restarting the proxy.

**Request Body:**

```json
{
  "model_name": "my-model",
  "api_base": "https://api.example.com/v1",
  "api_key": "secret-key",
  "target_model": "gpt-4",
  "request_timeout": 60,
  "supports_reasoning": true,
  "fallbacks": ["backup-model"]
}
```

**Response:**

```json
{
  "success": true,
  "message": "Model 'my-model' registered successfully"
}
```

### Get Full Configuration

```http
GET /admin/config
```

Returns the full runtime configuration (models, settings).

**Response:**

```json
{
  "model_list": [...],
  "router_settings": {...},
  "proxy_settings": {...}
}
```

### Update Configuration

```http
PUT /admin/config
```

Update runtime configuration. Body format same as GET response.

### List Models

```http
GET /admin/models
```

List all registered models with their source (default/added) and editability.

**Response:**

```json
[
  {
    "model_name": "gpt-4",
    "source": "default",
    "editable": false
  }
]
```

### Delete Runtime Model

```http
DELETE /admin/models/{model_name}
```

Remove a model added at runtime. Only works for runtime-added models, not for models loaded from `config_default.yaml`. To remove config-loaded models, edit `config_default.yaml` and restart the proxy.

**Response:**

```json
{
  "success": true,
  "message": "Model 'my-model' deleted successfully"
}
```

### Serve Admin UI

```http
GET /admin/
GET /admin_2/
```

Serve the admin web UI. `/admin/` serves `admin_new.html`. `/admin_2/` expects
`static/admin/admin_2.html` and will return 404 if that file is not present.

---

## Usage Statistics

### Usage Page

```http
GET /usage
```

Return HTML page with usage statistics visualization.

### Usage API

```http
GET /api/usage
```

Return usage statistics as JSON.

**Response:**

```json
{
  "total_requests": 150,
  "total_tokens": 50000,
  "by_model": {
    "gpt-4": {"requests": 100, "tokens": 40000},
    "claude-3": {"requests": 50, "tokens": 10000}
  },
  "errors": 2
}
```

### Usage Page Data

```http
GET /api/usage/page
```

Return usage data formatted for the usage page UI.

---

## Error Responses

All endpoints return consistent error responses:

```json
{
  "error": {
    "message": "Error description",
    "type": "invalid_request_error",
    "code": "error_code"
  }
}
```

### Common Error Codes

| Code | Description |
|------|-------------|
| `invalid_json` | Request body is not valid JSON |
| `invalid_json_shape` | Request body is not an object |
| `missing_parameter` | Required parameter is missing |
| `missing_messages` | messages array is required for chat completions |
| `model_not_found` | Requested model is not configured |
