# Plan: Add /v1/messages (Anthropic) Support

## Goals
- Add a /v1/messages endpoint that accepts Anthropic Messages requests and returns Anthropic Messages responses.
- Pass through to true Anthropic backends when available; otherwise translate to OpenAI-compatible chat and back.
- Preserve tool use semantics, especially tool_use IDs and tool_result ordering.
- Support streaming with correct Anthropic SSE event flow and delta types.
- Keep proxy transparency: avoid dropping unknown fields and avoid rewriting content unless required.

## Spec notes to anchor implementation
- Messages requests use a top-level system parameter (no system role in messages), allow content as string or array of blocks, and support text/image/document blocks. max_tokens defines the maximum output tokens and has a documented range (1..8192). The API combines consecutive same-role turns and allows assistant-prefill to continue a response. Source: Create a Message reference (Anthropic). 
- Tool use behavior: tools are declared at top level; tool_choice supports auto/any/tool/none. tool_use blocks include id/name/input; tool_result blocks must immediately follow the tool_use response and appear first in the user content array. tool_result content can be a string or nested text/image blocks and may include is_error. Source: Implement tool use (Anthropic).
- Streaming flow: message_start -> content_block_start -> content_block_delta -> content_block_stop -> message_delta -> message_stop; tool_use arguments stream via input_json_delta partial_json. Source: Messages streaming (Anthropic).
- stop_reason values include end_turn, max_tokens, stop_sequence, tool_use, pause_turn, and refusal. Source: Handling stop reasons (Anthropic).
- Headers: x-api-key and anthropic-version are required; anthropic-beta is optional. Source: Messages API reference (Anthropic).
- Release notes indicate tool_choice="none" and that tools may not be required for tool_use/tool_result blocks; use permissive validation to avoid rejecting requests. Source: API release notes (Anthropic).

## Architecture fit in this repo
- Create a new route handler: src/api/routes/messages.py
  - Mirrors responses endpoint flow: validate request, select backend, decide pass-through vs simulation.
  - Reuse RequestLogRecorder and usage metrics similar to chat/responses.
- Add a translation module: src/messages/translator.py
  - Anthropic Messages -> OpenAI Chat request
  - OpenAI Chat response -> Anthropic Messages response
  - Shared helper for content block conversion (text/image/tool_use/tool_result/thinking).
- Add a streaming adapter: src/messages/stream_adapter.py
  - ChatCompletions stream -> Anthropic Messages SSE
  - Maintain per-block state and stable tool_use IDs.
- Wire new endpoint in src/main.py and feature flag (proxy_settings.enable_messages_endpoint) in configs/config.yaml.

## Backend selection and header handling
- Decide pass-through when backend.api_type == "anthropic" (or new supports_messages_api flag).
- Extend header handling in src/core/backend.build_outbound_headers:
  - For anthropic backends, send x-api-key instead of Authorization.
  - Require anthropic-version; allow config default (e.g., backend.anthropic_version) and pass through client header if provided.
  - Preserve anthropic-beta header from client if present.
- Keep model rewrite behavior for anthropic: still allow target_model mapping via config.

## Request translation (Anthropic -> OpenAI chat)
- Top-level system:
  - If system is string or content blocks, convert to a single OpenAI system message.
  - If system contains non-text blocks, either move those to first user message or raise a validation error (configurable).
- Messages list:
  - Roles limited to user/assistant; convert to OpenAI messages with same roles.
  - content string -> OpenAI string.
  - content blocks:
    - text -> content parts (type: text) or string if only text.
    - image -> content part (type: image_url). Map base64 to data URL; map url source to direct URL.
    - tool_use (assistant) -> assistant tool_calls. Preserve tool_use.id as tool_call.id.
    - tool_result (user) -> tool messages with tool_call_id. Enforce tool_result blocks first; split trailing text into a separate user message.
    - thinking / redacted_thinking -> map to assistant content with an explicit tag OR drop with a warning (default to drop to avoid leaking chain-of-thought; make configurable).
- Tools and tool_choice:
  - tools[]: map to OpenAI tools/functions (name, description, input_schema->parameters).
  - tool_choice: map {auto, any, tool, none} to OpenAI equivalents (auto/required/function/none).
- Other parameters:
  - max_tokens -> max_tokens (chat completions)
  - stop_sequences -> stop
  - temperature/top_p/top_k, metadata -> pass through when supported.

## Response translation (OpenAI chat -> Anthropic Messages)
- Response envelope:
  - id/type/role/model/usage/stop_reason/stop_sequence.
  - stop_reason mapping: finish_reason stop->end_turn, length->max_tokens, tool_calls->tool_use; default to end_turn if unknown.
- Content blocks:
  - assistant content string/parts -> text blocks.
  - tool_calls -> tool_use blocks (id/name/input). Generate id if missing; ensure stable mapping for streaming.
  - If refusal/content_filter occurs, map to stop_reason=refusal and provide a refusal text block if available.

## Streaming translation (OpenAI stream -> Anthropic SSE)
- Emit message_start with empty content array and id/model.
- For each OpenAI delta:
  - text delta -> content_block_start (text) + content_block_delta (text_delta) + content_block_stop (on block end).
  - tool_call delta -> content_block_start (tool_use) + content_block_delta (input_json_delta).
  - Maintain index for content blocks in the final content array; track per-index accumulation.
- On finish_reason:
  - Emit message_delta with stop_reason and final usage if present.
  - Emit message_stop.
- Ensure tool_use ID stability:
  - Use tool_call.id if present; else generate deterministic call_id once per tool_call index.
  - For streaming arguments, emit partial_json in arrival order.
- Preserve ping events in pass-through mode; ignore/skip if not generated.

## Data model updates
- Extend src/types/chat.py Anthropic types:
  - Add tool_use_id to tool_result blocks, allow content to be string or array of blocks, add thinking/redacted_thinking types.
  - Expand AnthropicStreamEvent to include ping/error events.

## Validation rules (avoid breaking requests)
- Enforce only what spec requires:
  - model, messages present; max_tokens if required by backend/spec.
  - message role is user/assistant.
  - tool_result ordering (must be first and immediately after tool_use) only in strict mode; otherwise warn and attempt best-effort reorder.
- Allow unknown fields and pass through to backends where possible.

## Tests (incremental, as you build)
- Unit: translator
  - text-only, mixed text+image, tool_use/tool_result, assistant-prefill, system param.
  - tool_choice mapping; missing tools handling.
  - tool_use id preservation across translation.
- Unit: stream adapter
  - text streaming, tool_call streaming with input_json_delta, multi-tool call ordering, finish_reason mapping.
- API route tests:
  - pass-through vs simulation branch, header mapping, error handling.
- Run with task test after each chunk.

## Rollout and flags
- Add proxy_settings.enable_messages_endpoint (default false).
- Add backend.api_type: anthropic for pass-through.
- Add optional backend.anthropic_version config default; pass-through header if client overrides.

## Implementation sequence (suggested)
1) Types + translator skeleton (non-streaming) with unit tests.
2) Route handler with simulation path (non-streaming) and tests.
3) Streaming adapter + tests.
4) Header handling for anthropic backend + config changes + tests.
5) Optional strict validation toggles + docs update.

## Sources (for quick reference)
- Create a Message API reference: https://platform.claude.com/docs/en/api/python/messages/create
- Tool use implementation: https://docs.anthropic.com/en/docs/agents-and-tools/tool-use/implement-tool-use
- Streaming Messages: https://docs.anthropic.com/claude/reference/messages-streaming
- Handling stop reasons: https://docs.anthropic.com/en/api/handling-stop-reasons
- Messages API reference (headers/required): https://anthropic.mintlify.app/it/api/messages
- API release notes (tool_choice none, tools optional): https://docs.anthropic.com/en/release-notes/api
