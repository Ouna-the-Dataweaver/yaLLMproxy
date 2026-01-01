# Configuration Guide

Complete reference for yaLLMproxy configuration options.

## Configuration Files

yaLLMproxy uses two configuration files:

| File | Purpose |
|------|---------|
| `configs/config_default.yaml` | Base configuration with default models |
| `configs/config_added.yaml` | Runtime-added models (不会被版本控制覆盖) |

Environment variables are loaded from corresponding `.env` files:
- `configs/.env_default` - Environment variables for default config
- `configs/.env_added` - Environment variables for added config

## Full Configuration Reference

```yaml
model_list:
  - model_name: GLM-4.6-nano              # 唯一模型标识符
    model_params:
      api_type: openai                     # API类型: openai (目前仅支持)
      model: z-ai/glm-4.6:thinking         # 实际模型名称
      api_base: https://nano-gpt.com/api/v1thinking  # API基础URL
      api_key: ${NANOGPT_API_KEY}          # API密钥 (从.env读取)
      supports_reasoning: true             # 是否支持思考内容
      request_timeout: 540                 # 请求超时(秒)
      parameters:                          # 参数覆盖配置
        temperature:
          default: 1.0
          allow_override: false            # 是否允许请求覆盖
        top_p:
          default: 0.95
          allow_override: false
    parsers:                               # 响应解析器配置
      enabled: true
      response:
        - parse_unparsed
        - swap_reasoning_content
      parse_unparsed:
        parse_thinking: true
        parse_tool_calls: true
        think_tag: "think"
        tool_tag: "tool_call"
      swap_reasoning_content:
        mode: "reasoning_to_content"       # reasoning_to_content | content_to_reasoning | auto
        think_tag: "think"
        think_open:
          prefix: ""
          suffix: ""
        think_close:
          prefix: ""
          suffix: ""
        include_newline: true

router_settings:
  num_retries: 1                           # 每个后端重试次数
  fallbacks:                               # 备用模型配置
    - primary_model: [fallback1, fallback2]

proxy_settings:
  server:
    host: 127.0.0.1                        # 监听地址
    port: 7979                             # 监听端口
  enable_responses_endpoint: false         # 是否启用/v1/responses端点
  logging:
    log_parsed_response: true              # 记录解析后的响应
    log_parsed_stream: true                # 记录解析后的流式响应
  parsers:
    enabled: false                         # 全局解析器是否启用
    response:
      - parse_unparsed
      - swap_reasoning_content
    paths:
      - /chat/completions                  # 应用解析器的路径
    parse_unparsed:
      parse_thinking: true
      parse_tool_calls: true
      think_tag: "think"
      tool_tag: "tool_call"
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

forwarder_settings:
  listen:
    host: 0.0.0.0                          # 转发器监听地址
    port: 6969                             # 转发器监听端口
  target:
    host: 127.0.0.1                        # 代理主机
    port: 7979                             # 代理端口
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
| `model_params.model` | string | - | Actual model name to send to backend |
| `model_params.api_type` | string | "openai" | API type (only "openai" supported) |
| `model_params.target_model` | string | - | Override model name sent to backend |
| `model_params.request_timeout` | number | 60 | Request timeout in seconds |
| `model_params.supports_reasoning` | boolean | false | Whether model supports thinking content |
| `model_params.http2` | boolean | false | Whether to use HTTP/2 |
| `model_params.parameters` | object | - | Parameter override configuration |

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

## Response Parsers

### Available Parsers

1. **parse_unparsed** - Extract tool calls and thinking content from raw responses
2. **swap_reasoning_content** - Swap reasoning content with main content

### Parser Configuration

#### parse_unparsed

```yaml
parse_unparsed:
  parse_thinking: true         # Extract thinking tags
  parse_tool_calls: true       # Extract tool calls
  think_tag: "think"           # Tag name for thinking content
  tool_tag: "tool_call"        # Tag name for tool calls
```

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

### Per-Model Parser Overrides

Per-model parsers replace global `proxy_settings.parsers` config:

```yaml
model_list:
  - model_name: GLM-4.7
    model_params:
      api_base: https://api.example.com/v1
      api_key: ${GLM_API_KEY}
    parsers:
      enabled: true
      response:
        - swap_reasoning_content
      swap_reasoning_content:
        mode: reasoning_to_content
```

If `enabled` is omitted, per-model parsers default to enabled. Set `enabled: false` to explicitly disable.

## Environment Variables

### Configuration Override

| Environment Variable | Description |
|---------------------|-------------|
| `YALLMP_HOST` | Server bind address (overrides config) |
| `YALLMP_PORT` | Server bind port (overrides config) |
| `YALLMP_CONFIG_DEFAULT` | Path to default config file |
| `YALLMP_CONFIG_ADDED` | Path to added config file |

### Forwarder Override

| Environment Variable | Description |
|---------------------|-------------|
| `FORWARD_LISTEN_HOST` | Forwarder listen address |
| `FORWARD_LISTEN_PORT` | Forwarder listen port |
| `FORWARD_TARGET_HOST` | Forwarder target host |
| `FORWARD_TARGET_PORT` | Forwarder target port |

### In-Config Substitution

Use `${VAR_NAME}` or `$VAR_NAME` syntax in config to substitute environment variables:

```yaml
api_key: ${GLM_API_KEY}        # Braced format
api_key: $GLM_API_KEY          # Simple format
```

Variables are first read from `.env` files, then from actual environment.

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

## Template Inspection

To help align formatting with a Jinja chat template, inspect a template and print suggested `think_open`/`think_close` prefixes and suffixes:

```bash
uv run python scripts/inspect_template.py template_example.jinja
```

Copy the suggested values into your `swap_reasoning_content` config. If you set `think_close.suffix` to include a newline, consider setting `include_newline: false` to avoid double newlines.

## Configuration Validation

When yaLLMproxy starts, it validates the configuration and logs any issues. Common validation errors include:

- Missing required fields (`model_name`, `api_base`, `api_key`)
- Invalid URL in `api_base`
- Unset environment variables (logged as warnings)
- Duplicate model names

## Hot Reloading

Runtime model additions via `/admin/models` endpoints are persisted to `configs/config_added.yaml` and take effect immediately without restarting the proxy.

Default models cannot be modified at runtime, but can be overridden by adding a model with the same name to `config_added.yaml`.
