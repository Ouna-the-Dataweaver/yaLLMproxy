# Shared Core Plan: Internal Primitives

## Goal
Define a minimal, loss-minimizing internal representation (IR) that lets us:
- Support multiple provider schemas (OpenAI Chat/Responses, Anthropic Messages, Gemini, xAI).
- Preserve provider-specific fields via passthrough.
- Keep streaming semantics correct without forcing everything into one universal schema.

This is not a universal any2any adapter. It is a small shared core used by per-API translators.

## Research snapshot (Jan 18, 2026)
Gemini (native):
- REST uses `generateContent` / `streamGenerateContent`, request body contains `contents[]` of `Content` turns and `parts[]` inside each turn. Roles are `user` and `model`. `parts` can include text and `inline_data` (binary + MIME type). Authentication uses `x-goog-api-key`. Streaming returns a stream of `GenerateContentResponse` objects for the same request body.
- The API is stateless; thought signatures are used to preserve reasoning context across turns (required for function calling signatures on Gemini 3).
- System instruction is provided via a top-level config (`system_instruction` / `systemInstruction` in SDKs).
- Tooling is provided via `tools` with function declarations; function calls and function responses are represented in parts, and tool calling modes include AUTO/ANY/NONE.
- Gemini function calling may carry a `thought_signature` that must be preserved when returning function responses in the next turn.

Gemini (OpenAI-compatible via Vertex AI):
- Vertex AI exposes OpenAI-compatible Chat Completions endpoints so Gemini can be used via the OpenAI client + OpenAI-style requests.

xAI (Grok):
- xAI provides OpenAI- and Anthropic-compatible base URLs and recommends using the OpenAI SDK (base_url `https://api.x.ai/v1`) or Anthropic SDK (base_url `https://api.x.ai`). This suggests Grok can be used with either Chat Completions or Anthropic Messages shapes for many users.
- xAI exposes Chat Completions (legacy) and Chat Responses (preferred) with optional statefulness (response IDs, server-side history, 30-day retention). Tool calling includes tool calls in assistant responses and tool results with `tool_call_id`, plus `tool_choice` modes like `auto`, `required`, and `none`.

## Design principles
1) Transparency first: do not drop unknown fields; carry provider-specific extras.
2) Minimal core: only normalize what is demonstrably common across APIs.
3) Stable IDs: preserve tool call IDs and message IDs where possible.
4) Streaming fidelity: streaming adapters remain provider-specific, but share a core delta model.
5) No forced role normalization: map roles in adapters, not in the core.

## Proposed primitives

### 1) RequestEnvelope
Common request shape for internal flow only.
- `model: str`
- `system: SystemInstruction | None` (separate from turns)
- `turns: list[Turn]`
- `tools: list[Tool] | None`
- `tool_choice: ToolChoice | None`
- `sampling: SamplingParams`
- `limits: Limits`
- `response: ResponsePrefs` (e.g., response_format/json, modalities)
- `stream: bool`
- `state: StateHints` (previous_response_id, store, conversation_id)
- `metadata: dict`
- `extra: dict` (provider passthrough)

### 2) Turn
Represents a single conversational turn (not a full request).
- `role: Role` (enum: system, developer, user, assistant, model, tool)
- `content: list[ContentPart] | None`
- `tool_calls: list[ToolCall] | None`
- `tool_results: list[ToolResult] | None`
- `name: str | None`
- `extra: dict`

Notes:
- Gemini uses `role: "model"` for assistant. Keep the role distinct.
- Anthropic uses `user`/`assistant` and `system` as top-level. Adapters map.

### 3) ContentPart
Common representation of multi-modal content.
- `TextPart(text: str)`
- `ImagePart(mime: str, data: bytes | None, uri: str | None)`
- `AudioPart(mime: str, data: bytes | None, uri: str | None)`
- `FilePart(mime: str, data: bytes | None, uri: str | None, display_name: str | None)`
- `RefusalPart(text: str | None)`
- `ReasoningPart(text: str | None, encrypted: bool = False)`
- `CustomPart(type: str, payload: dict)` for provider-specific content parts
- Optional `meta: dict` on all parts (for provider-specific signatures like Gemini `thought_signature`)

### 4) Tool schema
Represents function/tool declarations across APIs.
- `Tool(name, description, parameters_json_schema)`
- `ToolChoice(mode: auto | any | validated | required | none | tool, tool_name: str | None)`

### 5) Tool call and result
- `ToolCall(id, name, arguments_json: str | dict, status)`
- `ToolResult(tool_call_id, content: list[ContentPart] | str, is_error: bool | None)`

### 6) SamplingParams
- `temperature`, `top_p`, `top_k`, `seed`
- `presence_penalty`, `frequency_penalty`

### 7) Limits
- `max_output_tokens` (primary)
- `max_input_tokens` (optional)
- `stop_sequences`

### 8) ResponsePrefs
Optional response preferences.
- `response_format` (json, text)
- `modalities` (text, image, audio)
- `include` (e.g., reasoning)

### 9) Usage
Normalized usage object for logging.
- `input_tokens`, `output_tokens`, `total_tokens`
- `cached_tokens`, `reasoning_tokens`
- `extra` for provider-specific usage fields

### 10) ResponseEnvelope
Internal response shape from adapters to routes.
- `id`, `model`, `created_at`
- `choices: list[Choice]`
- `usage: Usage | None`
- `error: Error | None`
- `extra: dict`

`Choice`:
- `index`
- `finish_reason`
- `message: Turn` (assistant/model)

### 11) StreamDelta
Shared delta model for streaming adapters.
- `event: start | content_delta | tool_delta | usage | error | stop`
- `turn_index`, `part_index`, `tool_index`
- `text_delta`, `json_delta`
- `finish_reason`
- `extra`

## Mapping notes (common intersections)
- System instruction:
  - OpenAI: system/developer role messages.
  - Anthropic: top-level `system`.
  - Gemini: `system_instruction` in config.
  - xAI: supports `system`/`developer` in Chat Completions.
- Roles:
  - Gemini uses `model` instead of `assistant`. Keep distinct.
  - Tool results can be represented as `role=tool` or `tool_result` blocks depending on provider.
- Tools:
  - OpenAI and xAI use `tool_calls` with `function` and `tool_call_id`.
  - Anthropic uses `tool_use` and `tool_result` blocks.
  - Gemini uses `functionCall` and `functionResponse` parts, and may include a `thought_signature` that must be returned.
- Images/binary:
  - OpenAI uses image_url (data URLs or remote URL).
  - Gemini uses `inline_data` with `mime_type` and bytes, or `fileData` refs.
  - Anthropic uses content blocks with `image` source (base64 or URL).

## Capability flags (per-provider)
Keep this per-backend in config to avoid hardcoding:
- supports_system_field
- supports_developer_role
- supports_tool_choice_modes (auto/any/none/tool)
- supports_tool_choice_validated
- supports_json_mode
- supports_streaming
- supports_stateful_responses (response_id / previous_response_id)
- supports_reasoning_content (and whether it is allowed to pass through)

## Implementation plan (repo-level)
1) Add `src/shared_core/primitives.py` for dataclasses/types.
2) Add `src/shared_core/codec/` with helper converters:
   - `content.py` for ContentPart conversion helpers
   - `tooling.py` for tool schema conversion
   - `usage.py` for usage normalization
3) Refactor existing translators to use the shared content/tool helpers.
4) Implement new adapters as API-specific edges:
   - Anthropic Messages translator + streaming adapter
   - Gemini native translator (generateContent)
   - xAI Responses translator (stateful)
5) Add tests per adapter + shared-core mapping tests.
6) Keep passthrough fields in `extra` and preserve ordering.

## Open questions
- Do we treat `model` as a role or normalize to `assistant` internally?
- Should `ReasoningPart` be kept by default or opt-in only?
- How strict should we be about tool_result ordering vs best-effort repair?
- Should binary data be stored as bytes in the IR or as data URLs to avoid memory overhead?

## References
- Gemini API reference (generateContent / streamGenerateContent, Content/Part roles, inline_data): https://ai.google.dev/api
- Gemini system instructions: https://ai.google.dev/gemini-api/docs/system-instructions
- Gemini function calling + modes: https://ai.google.dev/gemini-api/docs/function-calling
- Gemini thought signatures for function calls: https://ai.google.dev/gemini-api/docs/thought-signatures
- Vertex AI OpenAI compatibility (Gemini via OpenAI Chat Completions): https://cloud.google.com/vertex-ai/generative-ai/docs/start/openai
- xAI migration guide (OpenAI/Anthropic compatibility and base_url): https://docs.x.ai/docs/guides/migration
- xAI Responses API (stateful response IDs, retention): https://docs.x.ai/docs/guides/responses-api
- xAI function calling (tool_call_id + tool_choice modes): https://docs.x.ai/docs/guides/function-calling
