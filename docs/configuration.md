# Configuration Guide

Complete reference for yaLLMproxy configuration options.

## Table of Contents

- [Configuration Files](#configuration-files)
- [Full Configuration Reference](#full-configuration-reference)
- [Model Configuration](#model-configuration)
  - [Required Fields](#required-fields)
  - [Optional Fields](#optional-fields)
  - [Parameter Overrides](#parameter-overrides)
  - [Model Inheritance](#model-inheritance)
  - [Model Copying](#model-copying)
- [Response Modules](#response-modules)
  - [Available Modules](#available-modules)
  - [Per-Model Module Overrides](#per-model-module-overrides)
  - [Template-Based Parsing](#template-based-parsing)
- [Environment Variables](#environment-variables)
- [App Key Authentication](#app-key-authentication)
- [Router Settings](#router-settings)
- [Logging Configuration](#logging-configuration)
- [Template Inspection](#template-inspection)
- [Configuration Validation](#configuration-validation)
- [Hot Reloading](#hot-reloading)

## Configuration Files

yaLLMproxy uses a single configuration file:

| File | Purpose |
|------|---------|
| `configs/config.yaml` | Unified configuration and model list |

Environment variables are loaded from:
- `configs/.env` - Environment variables referenced in config

## Full Configuration Reference

```yaml
model_list:
  - model_name: GLM-4.6-nano              # Unique model identifier
    protected: true
    model_params:
      api_type: openai                     # API type: openai (currently only)
      model: z-ai/glm-4.6:thinking         # Actual model name
      api_base: https://nano-gpt.com/api/v1thinking  # API base URL
      api_key: ${NANOGPT_API_KEY}          # API key (from .env)
      supports_reasoning: true             # Supports reasoning content
      request_timeout: 540                 # Request timeout (seconds)
      thinking:
        type: enabled                      # Thinking mode: enabled, auto, disabled
      parameters:                          # Parameter override config
        temperature:
          default: 1.0
          allow_override: false            # Allow request override
        top_p:
          default: 0.95
          allow_override: false
    modules:                               # Response module config
      upstream:
        enabled: true
        response:
          - parse_tags
          - swap_reasoning_content
        parse_tags:
          parse_thinking: true
          parse_tool_calls: true
          think_tag: "think"
          tool_arg_format: "xml"           # xml | json (for K2-style)
          tool_tag: "tool_call"            # For xml format
        swap_reasoning_content:
          mode: "reasoning_to_content"     # reasoning_to_content | content_to_reasoning | auto
          think_tag: "think"
          think_open:
            prefix: ""
            suffix: ""
          think_close:
            prefix: ""
            suffix: ""
          include_newline: true

router_settings:
  num_retries: 1                           # Retry attempts per backend
  fallbacks:                               # Fallback model config
    - primary_model: [fallback1, fallback2]

proxy_settings:
  server:
    host: 127.0.0.1                        # Listen address
    port: 7979                             # Listen port
  enable_responses_endpoint: false         # Enable /v1/responses endpoint
  logging:
    log_parsed_response: true              # Log parsed response
    log_parsed_stream: true                # Log parsed stream response
  modules:
    upstream:
      enabled: false                       # Enable upstream response modules
      response:
        - parse_tags
        - swap_reasoning_content
      paths:
        - /chat/completions                # Paths to apply upstream modules
      parse_tags:
        parse_thinking: true
        parse_tool_calls: true
        think_tag: "think"
        tool_arg_format: "xml"             # xml | json (for K2-style)
        tool_tag: "tool_call"              # For xml format
      swap_reasoning_content:
        mode: "reasoning_to_content"
        think_tag: "think"
        think_open:
          prefix: ""
          suffix: ""
        think_close:
          prefix: ""
          suffix: ""
        include_newline: true
    downstream:
      enabled: false                       # Enable downstream request modules
      request: []

forwarder_settings:
  listen:
    host: 0.0.0.0                          # Forwarder listen address
    port: 6969                             # Forwarder listen port
  target:
    host: 127.0.0.1                        # Proxy host
    port: 7979                             # Proxy port

http_forwarder_settings:
  preserve_host: true
  listen:
    host: 0.0.0.0
    port: 6969
  target:
    scheme: http
    host: 127.0.0.1
    port: 7979
```

## Model Configuration

### Required Fields

| Field | Type | Description |
|-------|------|-------------|
| `model_name` | string | Unique identifier for the model |
| `model_params.api_base` | string | Base URL for the API endpoint |
| `model_params.api_key` | string | API authentication key |

### Optional Fields

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `protected` | boolean | true | Require admin password to edit/delete this model |
| `model_params.model` | string | - | Actual model name to send to backend |
| `model_params.api_type` | string | "openai" | API type (only "openai" supported) |
| `model_params.target_model` | string | - | Override model name sent to backend |
| `model_params.request_timeout` | number | 60 | Request timeout in seconds |
| `model_params.supports_reasoning` | boolean | false | Whether model supports reasoning content (legacy, prefer `thinking.type`) |
| `model_params.http2` | boolean | false | Whether to use HTTP/2 |
| `model_params.parameters` | object | - | Parameter override configuration |
| `model_params.thinking` | object | - | Thinking content configuration block |
| `model_params.thinking.type` | string | "auto" | Thinking mode: enabled, auto, disabled |

### Parameter Overrides

The `parameters` section allows you to enforce or default certain LLM parameters:

```yaml
parameters:
  temperature:
    default: 1.0              # Default value if not provided in request
    allow_override: false     # If false, always use default (ignore request)
  top_p:
    default: 0.95
    allow_override: true      # If true, use request value if provided
```

## Model Inheritance

Models can inherit configuration from other models using the `extends` field. This allows you to create derived models with configuration overrides without duplicating the full configuration.

### Basic Inheritance

```yaml
model_list:
  # Base model with full configuration
  - model_name: GLM-4.7
    protected: true
    model_params:
      api_base: https://api.example.com/v1
      api_key: ${GLM_API_KEY}
      parameters:
        temperature:
          default: 1.0
          allow_override: false
    modules:
      upstream:
        enabled: true
        response:
          - swap_reasoning_content

  # Derived model inherits from GLM-4.7, adds custom modules
  - model_name: GLM-4.7:Cursor
    protected: false
    extends: GLM-4.7
    modules:
      upstream:
        enabled: true
        response:
          - parse_tags
          - swap_reasoning_content
```

The derived model `GLM-4.7:Cursor` inherits:
- All `model_params` from `GLM-4.7` (api_base, api_key, parameters, etc.)
- Then overrides/extends with its own configuration (parsers)

### How Inheritance Works

1. The base model is fully resolved first (including its own inheritance chain)
2. Derived model settings are deep-merged on top
3. Nested configurations (like `parameters`, `parsers`) are merged recursively
4. Lists are replaced (not merged) - e.g., `response` parser list
5. The `extends` field is removed from the resolved model

### Chained Inheritance

Inheritance chains are supported:

```yaml
model_list:
  - model_name: base-model
    model_params:
      api_base: https://base.local/v1
      api_key: ${BASE_KEY}

  - model_name: middle-model
    extends: base-model
    model_params:
      api_key: ${MIDDLE_KEY}
      request_timeout: 120

  - model_name: derived-model
    extends: middle-model
    model_params:
      parameters:
        temperature:
          default: 0.7
```

The final `derived-model` will have:
- `api_base` from `base-model` (inherited through chain)
- `api_key` from `middle-model`
- `request_timeout` from `middle-model`
- `temperature` parameter set to 0.7

### Inheritance Within One Config

Models in the same `config.yaml` can inherit from each other:

```yaml
# configs/config.yaml
model_list:
  - model_name: GLM-4.7
    protected: true
    model_params:
      api_base: https://api.z.ai/api/coding/paas/v4
      api_key: ${GLM_API_KEY}
    modules:
      upstream:
        enabled: true
        response:
          - swap_reasoning_content

  - model_name: GLM-4.7:Custom
    protected: false
    extends: GLM-4.7
    modules:
      upstream:
        response:
          - parse_tags
          - swap_reasoning_content
```

### Error Handling

- **Circular references**: Detected and raises error (e.g., A extends B, B extends A)
- **Missing base model**: Raises error if the referenced model doesn't exist
- **Maximum depth**: Inheritance chains are limited to 10 levels

## Model Copying

You can duplicate existing models via the Admin API to create new models with modified configuration.

### Copy Model API

```http
POST /admin/models/copy?source={source_model}&target={new_model}
```

**Example using curl:**

```bash
curl -X POST "http://localhost:7979/admin/models/copy?source=GLM-4.7&target=GLM-4.7-Copy"
```

**Response:**

```json
{
  "status": "ok",
  "message": "Model 'GLM-4.7' copied to 'GLM-4.7-Copy'",
  "model": {
    "model_name": "GLM-4.7-Copy",
    "model_params": {
      "api_base": "https://api.z.ai/api/coding/paas/v4"
    },
    "protected": false,
    "editable": true
  }
}
```

### Copy Behavior

- The copied model is saved to `config.yaml`
- Source model can be any existing model in `config.yaml`
- Protected source models require an admin password to copy
- All settings are copied except metadata fields (`editable`, `_inherited_from`)
- The new model name must not already exist
- API keys are removed from admin API responses

### Use Cases

1. **Quick duplication**: Create a new model based on an existing one
2. **Configuration experiments**: Copy a model, modify settings, test without affecting original
3. **Per-environment models**: Copy production model to staging with different parameters

## Response Modules

### Available Modules

The proxy uses a modular pipeline system for processing responses. Modules can be applied globally or per-model.

1. **parse_tags** - Extract tool calls and thinking content from raw responses
2. **swap_reasoning_content** - Swap reasoning content with main content

### Module Configuration

#### parse_tags

Parses `<think>` and `<tool_call>` tags (or custom delimiters) from response content into structured fields (`reasoning_content`, `tool_calls`).

```yaml
parse_tags:
  # Optional: auto-detect config from Jinja template
  template_path: configs/jinja_templates/template_example.jinja

  # Parsing options
  parse_thinking: true           # Extract thinking tags
  parse_tool_calls: true         # Extract tool calls
  think_tag: "think"             # Tag name for thinking content

  # Tool argument format
  tool_arg_format: "xml"         # xml | json

  # For xml format (default):
  tool_tag: "tool_call"          # Tag name: <tool_call>...</tool_call>

  # For json format (K2-style models):
  tool_open: "<|tool_call_begin|>"       # Custom open delimiter
  tool_close: "<|tool_call_end|>"        # Custom close delimiter
  tool_arg_separator: "<|tool_call_argument_begin|>"  # Separates name from JSON args
  drop_tags:                             # Tags to strip (e.g., section markers)
    - "<|tool_calls_section_begin|>"
    - "<|tool_calls_section_end|>"

  tool_buffer_limit: 200         # Optional: max buffered chars before treating as literal
```

**Tool Argument Formats:**

- **xml** (default): Tool calls use XML-style argument encoding
  ```xml
  <tool_call>function_name<arg_key>param</arg_key><arg_value>value</arg_value></tool_call>
  ```

- **json**: Tool calls use JSON arguments (K2-style models like Kimi)
  ```
  <|tool_call_begin|>function_name<|tool_call_argument_begin|>{"param": "value"}<|tool_call_end|>
  ```

**Template Auto-Detection:**

When `template_path` is provided, the module automatically detects:
- Think tag name from template expressions using `reasoning_content`
- Tool argument format (xml vs json/K2) from template markers
- Custom delimiters for K2-style tool calls

**Notes:**
- Uses incremental streaming parser for both stream and non-stream responses
- Tool calls can appear inside thinking blocks; tool parsing has higher priority
- Tool calls are emitted only after a full, parseable block is confirmed
- If `tool_buffer_limit` is exceeded without closing, the tag is treated as literal text

#### swap_reasoning_content

```yaml
swap_reasoning_content:
  mode: "reasoning_to_content"  # reasoning_to_content | content_to_reasoning | auto
  think_tag: "think"
  think_open:
    prefix: ""                  # Prefix before <think>
    suffix: ""                  # Suffix after <think>
  think_close:
    prefix: ""                  # Prefix before </think>
    suffix: ""                  # Suffix after </think>
  include_newline: true         # Add newline between </think> and content
```

**Mode Options:**
- `reasoning_to_content`: Move thinking content to message content
- `content_to_reasoning`: Move content to thinking
- `auto`: Auto-detect based on tags

### Per-Model Module Overrides

Per-model modules replace global `proxy_settings.modules` config:

```yaml
model_list:
  - model_name: GLM-4.7
    model_params:
      api_base: https://api.example.com/v1
      api_key: ${GLM_API_KEY}
    modules:
      upstream:
        enabled: true
        response:
          - swap_reasoning_content
        swap_reasoning_content:
          mode: reasoning_to_content
```

If `enabled` is omitted, per-model modules default to enabled. Set `enabled: false` to explicitly disable.

## Environment Variables

### Configuration Override

| Environment Variable | Description |
|---------------------|-------------|
| `YALLMP_HOST` | Server bind address (overrides config) |
| `YALLMP_PORT` | Server bind port (overrides config) |
| `YALLMP_CONFIG` | Path to config file |
| `YALLMP_ADMIN_PASSWORD` | Admin password for protected model changes |

### Forwarder Override

| Environment Variable | Description |
|---------------------|-------------|
| `FORWARD_LISTEN_HOST` | Forwarder listen address |
| `FORWARD_LISTEN_PORT` | Forwarder listen port |
| `FORWARD_TARGET_HOST` | Forwarder target host |
| `FORWARD_TARGET_PORT` | Forwarder target port |

### HTTP Forwarder Override

| Environment Variable | Description |
|---------------------|-------------|
| `HTTP_FORWARD_LISTEN_HOST` | HTTP forwarder listen address |
| `HTTP_FORWARD_LISTEN_PORT` | HTTP forwarder listen port |
| `HTTP_FORWARD_TARGET_SCHEME` | HTTP forwarder target scheme |
| `HTTP_FORWARD_TARGET_HOST` | HTTP forwarder target host |
| `HTTP_FORWARD_TARGET_PORT` | HTTP forwarder target port |
| `HTTP_FORWARD_PRESERVE_HOST` | Preserve Host header (true/false) |

### In-Config Substitution

Use `${VAR_NAME}` or `$VAR_NAME` syntax in config to substitute environment variables:

```yaml
api_key: ${GLM_API_KEY}        # Braced format
api_key: $GLM_API_KEY          # Simple format
```

Variables are first read from `.env`, then from the actual environment.

## Router Settings

### Retry Configuration

```yaml
router_settings:
  num_retries: 1               # Number of retry attempts per backend
```

### Fallback Models

```yaml
router_settings:
  fallbacks:
    - gpt-4: [gpt-4-turbo, claude-3-opus]  # If gpt-4 fails, try fallbacks in order
    - claude-3: [claude-3-sonnet]
```

## Logging Configuration

```yaml
proxy_settings:
  logging:
    log_parsed_response: true  # Log parsed response bodies
    log_parsed_stream: true    # Log parsed stream chunks
```

## App Key Authentication

yaLLMproxy supports optional API key authentication for proxy access. This feature allows you to control access to the proxy and implement rate limiting per key.

```yaml
# App Key Authentication (optional)
app_keys:
  enabled: true                    # Set to true to enable app key authentication
  header_name: x-api-key           # Header name for API key (also accepts Authorization: Bearer)
  allow_unauthenticated: false     # Allow requests without a key when enabled
  keys:
    - key_id: "app-dev-001"
      secret: "${APP_KEY_DEV}"     # Use env var for secrets
      name: "Development"
      description: "Development team access"
      enabled: true
```

### Configuration Options

|| Option | Type | Default | Description |
||--------|------|---------|-------------|
|| `enabled` | boolean | false | Enable/disable app key authentication |
|| `header_name` | string | "x-api-key" | Header name for API key lookup |
|| `allow_unauthenticated` | boolean | false | Allow requests without a key when authentication is enabled |
|| `keys` | list | [] | List of API keys |

### Key Configuration

|| Option | Type | Description |
||--------|------|-------------|
|| `key_id` | string | Unique identifier for the key (used in access control) |
|| `secret` | string | The API key secret (supports env var substitution) |
|| `name` | string | Human-readable name for the key |
|| `description` | string | Description of the key's purpose |
|| `enabled` | boolean | Enable/disable this key |

### Per-Model Access Control

You can restrict which API keys have access to specific models:

```yaml
model_list:
  - model_name: premium-model
    model_params:
      api_base: https://api.example.com/v1
      api_key: ${PREMIUM_API_KEY}
    access_control:
      allowed_keys:                # List of key_ids that can access this model
        - app-prod-001
        - app-premium-001
```

**Special Values for `allowed_keys`:**

- `"all"` - All API keys can access this model (default)
- `"none"` - No API keys can access this model (requires unauthenticated access)

## Template Inspection

To help align formatting with a Jinja chat template, inspect a template and print suggested `think_open`/`think_close` prefixes and suffixes:

```bash
uv run python scripts/inspect_template.py configs/jinja_templates/template_example.jinja
```

Copy the suggested values into your `swap_reasoning_content` config. If you set `think_close.suffix` to include a newline, consider setting `include_newline: false` to avoid double newlines.

## Configuration Validation

When yaLLMproxy starts, it validates the configuration and logs any issues. Common validation errors include:

- Missing required fields (`model_name`, `api_base`, `api_key`)
- Invalid URL in `api_base`
- Unset environment variables (logged as warnings)
- Duplicate model names

## Hot Reloading

Runtime model additions via `/admin/models` are persisted to `configs/config.yaml` and take effect immediately without restarting the proxy.

Protected models require `YALLMP_ADMIN_PASSWORD` to edit/delete. Unprotected models can be edited without a password.
