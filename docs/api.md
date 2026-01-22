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

### Embeddings

```http
POST /v1/embeddings
```

OpenAI-compatible embeddings endpoint. Generate vector representations of text for use in search, clustering, recommendations, and other ML tasks.

**Request Body:**

```json
{
  "model": "text-embedding-model",
  "input": "Hello, world!",
  "encoding_format": "float",
  "dimensions": 1536
}
```

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `model` | string | Yes | Model name to use for embeddings |
| `input` | string or array | Yes | Text to embed (string or array of strings) |
| `encoding_format` | string | No | Output format: `float` (default) or `base64` |
| `dimensions` | integer | No | Output dimensions (model-dependent) |
| `user` | string | No | End-user identifier for tracking |

**Response:**

```json
{
  "object": "list",
  "data": [
    {
      "object": "embedding",
      "index": 0,
      "embedding": [0.0023064255, -0.009327292, ...]
    }
  ],
  "model": "text-embedding-model",
  "usage": {
    "prompt_tokens": 9,
    "total_tokens": 9
  }
}
```

**Notes:**
- The same model configuration works for both chat and embeddings - the proxy routes based on the endpoint path
- Configure embedding models the same way as chat models in `config.yaml`
- Embeddings requests are never streaming

### Anthropic Messages

```http
POST /v1/messages
```

Anthropic Messages API endpoint. Accepts Anthropic-format requests and translates them to OpenAI Chat Completions internally, enabling you to use Anthropic-compatible clients with OpenAI backends.

**Request Body:**

```json
{
  "model": "model-name",
  "max_tokens": 1024,
  "system": "You are a helpful assistant.",
  "messages": [
    {"role": "user", "content": "Hello, world!"}
  ],
  "temperature": 0.7,
  "stream": false
}
```

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `model` | string | Yes | Model name to use |
| `max_tokens` | integer | Yes | Maximum tokens to generate |
| `messages` | array | Yes | Array of messages (Anthropic format) |
| `system` | string/array | No | System prompt (string or content blocks) |
| `temperature` | number | No | Sampling temperature (0-2) |
| `top_p` | number | No | Nucleus sampling parameter |
| `stop_sequences` | array | No | Stop sequences (converted to `stop`) |
| `stream` | boolean | No | Enable streaming responses |
| `tools` | array | No | Tool definitions (Anthropic format) |
| `tool_choice` | string/object | No | Tool selection strategy |
| `metadata` | object | No | Request metadata (user_id mapped to `user`) |

**Message Content Formats:**

Messages can contain string content or content blocks:

```json
{
  "role": "user",
  "content": [
    {"type": "text", "text": "What's in this image?"},
    {"type": "image", "source": {"type": "base64", "media_type": "image/png", "data": "..."}}
  ]
}
```

**Supported Content Block Types:**
- `text` - Text content
- `image` - Base64 or URL images (converted to OpenAI `image_url`)
- `tool_use` - Tool/function calls (assistant role)
- `tool_result` - Tool results (converted to OpenAI `tool` messages)

**Response (non-streaming):**

```json
{
  "id": "msg_abc123",
  "type": "message",
  "role": "assistant",
  "content": [
    {"type": "text", "text": "Hello! How can I help you today?"}
  ],
  "model": "model-name",
  "stop_reason": "end_turn",
  "stop_sequence": null,
  "usage": {
    "input_tokens": 15,
    "output_tokens": 12
  }
}
```

**Response (streaming):**

Streaming responses use Server-Sent Events (SSE) with Anthropic event types:

```
event: message_start
data: {"type":"message_start","message":{...}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":12}}

event: message_stop
data: {"type":"message_stop"}
```

**Tool Use Example:**

Request with tools:
```json
{
  "model": "model-name",
  "max_tokens": 1024,
  "tools": [
    {
      "name": "get_weather",
      "description": "Get current weather for a location",
      "input_schema": {
        "type": "object",
        "properties": {
          "location": {"type": "string", "description": "City name"}
        },
        "required": ["location"]
      }
    }
  ],
  "messages": [{"role": "user", "content": "What's the weather in Tokyo?"}]
}
```

Response with tool use:
```json
{
  "id": "msg_abc123",
  "type": "message",
  "role": "assistant",
  "content": [
    {
      "type": "tool_use",
      "id": "toolu_abc123",
      "name": "get_weather",
      "input": {"location": "Tokyo"}
    }
  ],
  "stop_reason": "tool_use",
  "usage": {"input_tokens": 50, "output_tokens": 30}
}
```

**Translation Notes:**
- `stop_sequences` → OpenAI `stop`
- `tool_choice: "any"` → OpenAI `"required"`
- `tool_choice: {"type": "tool", "name": "..."}` → OpenAI function selection
- `top_k` is logged but ignored (OpenAI doesn't support it)
- Stop reason mapping: `stop`→`end_turn`, `length`→`max_tokens`, `tool_calls`→`tool_use`

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

### Reload Configuration

```http
POST /admin/config/reload
```

Reload configuration from disk without restarting the proxy. This will:
1. Reload config files from disk
2. Re-parse all models and backends
3. Update the router's runtime state

**Response:**

```json
{
  "status": "ok",
  "message": "Configuration reloaded successfully",
  "models_count": 5
}
```

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

---

## Template Management

### List Templates

```http
GET /admin/templates
```

List available Jinja templates in `configs/jinja_templates`.

**Response:**

```json
{
  "templates": [
    {"name": "glm_4.5_air.jinja", "path": "configs/jinja_templates/glm_4.5_air.jinja"}
  ]
}
```

### Upload Template

```http
POST /admin/templates
```

Upload a new Jinja template file.

**Request Body (multipart/form-data):**

| Field | Type | Description |
|-------|------|-------------|
| `file` | file | The template file to upload |

**Response:**

```json
{
  "name": "my_template.jinja",
  "path": "configs/jinja_templates/my_template.jinja"
}
```

### Inspect Template

```http
GET /admin/templates/inspect?template_path={path}&think_tag={tag}
```

Inspect a template and return derived swap reasoning configuration.

**Query Parameters:**

| Parameter | Type | Description |
|-----------|------|-------------|
| `template_path` | string | Path to the template file (relative to repo or in templates directory) |
| `think_tag` | string | Optional explicit think tag to inspect |

**Response:**

```json
{
  "template_path": "configs/jinja_templates/my_template.jinja",
  "config": {
    "enabled": true,
    "think_tag": "think",
    "block_params": true,
    "block_output": false
  },
  "detected_think_tag": "think"
}
```

---

## App Key Management

### List App Keys

```http
GET /admin/keys
```

List all configured app keys with secrets masked.

**Response:**

```json
{
  "enabled": true,
  "header_name": "x-api-key",
  "allow_unauthenticated": false,
  "keys": [
    {
      "key_id": "key-abc123",
      "name": "My App Key",
      "description": "API key for my application",
      "enabled": true
    }
  ]
}
```

### Create App Key

```http
POST /admin/keys
```

Create a new app key. Requires admin password.

**Request Body:**

```json
{
  "key_id": "optional-key-id",
  "secret": "optional-secret",
  "name": "My App Key",
  "description": "Description of the key",
  "enabled": true
}
```

**Response:**

```json
{
  "status": "ok",
  "key_id": "key-abc123",
  "secret": "sk-live-xyz...",
  "message": "Store this secret securely - it will not be shown again"
}
```

### Get App Key

```http
GET /admin/keys/{key_id}
```

Get a specific app key by ID.

**Response:**

```json
{
  "key_id": "key-abc123",
  "name": "My App Key",
  "description": "Description",
  "enabled": true
}
```

### Update App Key

```http
PUT /admin/keys/{key_id}
```

Update an app key's metadata (not the secret). Requires admin password.

**Request Body:**

```json
{
  "name": "Updated Name",
  "description": "Updated description",
  "enabled": false
}
```

### Regenerate App Key Secret

```http
POST /admin/keys/{key_id}/regenerate
```

Regenerate the secret for an existing app key. Requires admin password.

**Response:**

```json
{
  "status": "ok",
  "key_id": "key-abc123",
  "secret": "sk-live-new...",
  "message": "Store this secret securely - it will not be shown again"
}
```

### Delete App Key

```http
DELETE /admin/keys/{key_id}
```

Delete an app key. Requires admin password.

**Response:**

```json
{
  "status": "ok",
  "key_id": "key-abc123",
  "message": "App key 'key-abc123' deleted"
}
```

### Configure App Key Authentication

```http
POST /admin/keys/config
```

Enable or disable app key authentication. Requires admin password.

**Request Body:**

```json
{
  "enabled": true,
  "header_name": "x-api-key",
  "allow_unauthenticated": false
}
```

**Response:**

```json
{
  "status": "ok",
  "enabled": true,
  "header_name": "x-api-key",
  "allow_unauthenticated": false
}
```

---

## Serve Admin UI

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
| `missing_parameter` | Required parameter is missing (model, messages, input) |
| `invalid_parameter` | Parameter has invalid type or value |
| `model_not_found` | Requested model is not configured |
