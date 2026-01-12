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
  "extends": "base-model",
  "fallbacks": ["backup-model"],
  "protected": false
}
```

**Response:**

```json
{
  "status": "ok",
  "model": "my-model",
  "replaced": false,
  "fallbacks": ["backup-model"],
  "protected": false
}
```

**Protected models:** If the model is protected (or you are modifying a protected model), include the admin password via `x-admin-password` header or `admin_password` in the request body/query.

### Get Full Configuration

```http
GET /admin/config
```

Returns the full runtime configuration (models, settings). API keys are removed from the response.

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

Protected models require the admin password via `x-admin-password` header (or `admin_password` in the request body/query).

### List Models

```http
GET /admin/models
```

List all registered models, grouped by protection status. API keys are removed from the response.

**Response:**

```json
{
  "protected": [
    {"model_name": "gpt-4", "protected": true, "editable": false}
  ],
  "unprotected": [
    {"model_name": "gpt-4-mini", "protected": false, "editable": true}
  ]
}
```

### Model Tree

```http
GET /admin/models/tree
```

Returns the full inheritance tree (parents/children). API keys are removed from the response.

**Response:**

```json
{
  "roots": ["base-model"],
  "nodes": {
    "base-model": {
      "config": {"model_name": "base-model", "model_params": {...}},
      "parent": null,
      "children": ["derived-model"],
      "protected": true,
      "editable": false
    }
  }
}
```

### Model Ancestry

```http
GET /admin/models/{model_name}/ancestry
```

Returns the inheritance chain for a model.

**Response:**

```json
{
  "model": "derived-model",
  "chain": ["derived-model", "base-model"],
  "inheritance_depth": 2
}
```

### Model Dependents

```http
GET /admin/models/{model_name}/dependents
```

Returns direct children and all descendants of a model.

**Response:**

```json
{
  "model": "base-model",
  "direct_children": ["derived-model"],
  "all_descendants": ["derived-model"],
  "descendant_count": 1
}
```

### Delete Runtime Model

```http
DELETE /admin/models/{model_name}
```

Remove a model from config/runtime. Protected models require the admin password.

Use `?cascade=true` to delete dependents.

**Response:**

```json
{
  "status": "ok",
  "message": "Deleted 2 model(s)",
  "deleted": ["base-model", "derived-model"]
}
```

If dependents exist and `cascade` is not set:

```json
{
  "error": "Cannot delete model with existing dependents",
  "dependents": ["derived-model"],
  "hint": "Set cascade=true to delete dependents, or update them to use a different parent"
}
```

### Copy Model

```http
POST /admin/models/copy?source={source}&target={target}
```

Copy an existing model to a new model name. Protected source models require the admin password.

### Serve Admin UI

```http
GET /admin/
```

Serve the admin web UI. `/admin/` serves `admin.html`.

---

## Logs Viewer

### Logs Page

```http
GET /logs
```

Serve the logs viewer HTML page for browsing and analyzing request logs.

### Get Logs

```http
GET /api/logs
```

Get paginated request logs with optional filters.

**Query Parameters:**

| Parameter | Type | Description |
|-----------|--------|-------------|
| `model` | string | Filter by model name (partial match) |
| `outcome` | string | Filter by outcome: success, error, cancelled |
| `stop_reason` | string | Filter by stop reason: stop, tool_calls, length, content_filter |
| `is_tool_call` | boolean | Filter by whether tool calls were made |
| `start_date` | datetime | Start of time range (ISO 8601) |
| `end_date` | datetime | End of time range (ISO 8601) |
| `search` | string | Full-text search in request/response body |
| `limit` | integer | Maximum number of logs to return (1-200, default: 50) |
| `offset` | integer | Number of logs to skip (default: 0) |

**Response:**

```json
{
  "logs": [...],
  "total": 150,
  "limit": 50,
  "offset": 0
}
```

### Get Log by ID

```http
GET /api/logs/{log_id}
```

Get a single request log by ID with full details.

**Query Parameters:**

| Parameter | Type | Description |
|-----------|--------|-------------|
| `body_max_chars` | integer | Maximum size of request body to include (0 disables truncation, default: 10000) |

### Get Stop Reasons Analytics

```http
GET /api/stop-reasons
```

Get statistics on stop reasons from logged requests.

**Response:**

```json
{
  "stop_reasons": [
    {"stop_reason": "stop", "count": 100, "percentage": 66.7},
    {"stop_reason": "tool_calls", "count": 30, "percentage": 20.0},
    {"stop_reason": "length", "count": 15, "percentage": 10.0},
    {"stop_reason": "content_filter", "count": 5, "percentage": 3.3}
  ],
  "time_range": {
    "start": "2026-01-11T12:00:00Z",
    "end": "2026-01-12T12:00:00Z"
  }
}
```

### Get Tool Call Rate

```http
GET /api/tool-call-rate
```

Get percentage of requests that resulted in tool calls.

**Response:**

```json
{
  "total_requests": 150,
  "tool_call_requests": 30,
  "tool_call_rate": 20.0
}
```

### Get Models with Stop Reasons

```http
GET /api/models
```

Get request counts per model with stop reason breakdown.

**Response:**

```json
{
  "models": [
    {
      "model_name": "gpt-4",
      "total_requests": 100,
      "stop_reasons": [
        {"stop_reason": "stop", "count": 70},
        {"stop_reason": "tool_calls", "count": 20},
        {"stop_reason": "length", "count": 10}
      ]
    }
  ]
}
```

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
