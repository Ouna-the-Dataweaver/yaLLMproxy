# Plan: Add /v1/messages (Anthropic) Support

## Current Status (Updated 2026-01-22)

### ✅ COMPLETED
- **Route handler**: `src/api/routes/messages.py` - fully implemented
  - JSON parsing and validation
  - Model lookup and authentication
  - Anthropic-format error responses
  - Request logging and usage metrics
  - Streaming detection and response handling
  - **OpenAI translation path**: Translates requests for non-anthropic backends
- **Feature flag**: `proxy_settings.enable_messages_endpoint` in config (currently `true`)
- **Module structure**: `src/messages/__init__.py` exports translator functions and stream adapter
- **Anthropic pass-through**: Works for backends with `api_type: "anthropic"`
  - Forwards requests directly to Anthropic-compatible backends
  - Streaming works natively (Anthropic SSE format)
- **Anthropic types**: `src/types/chat.py` has type definitions for Anthropic responses
- **Backend configuration**: Supports `api_type: anthropic` and `anthropic_version` fields
- **Route registration**: Conditional registration in `src/main.py`
- **Translator**: `src/messages/translator.py` - fully implemented
  - `messages_to_chat_completions()` - translates Anthropic Messages to OpenAI Chat Completions
  - `chat_completion_to_messages()` - translates OpenAI responses back to Anthropic format
- **Stream adapter**: `src/messages/stream_adapter.py` - fully implemented
  - `ChatToMessagesStreamAdapter` - converts OpenAI SSE stream to Anthropic SSE format
  - Handles text blocks, tool_use blocks, and proper event sequencing
- **Tests**: Full test coverage
  - `tests/test_messages_translator.py` - 46 tests for translator functions
  - `tests/test_messages_stream_adapter.py` - 11 tests for stream adapter

### ⚠️ STUB/PLACEHOLDER
(None - all components implemented)

### ❌ NOT IMPLEMENTED
(None - all planned features implemented)

---

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

---

## Completed Work

### Phase 1: Request Translation (Anthropic -> OpenAI Chat) ✅
Implemented `messages_to_chat_completions()` in `src/messages/translator.py`:

- **Top-level system**: Converts string or content blocks to OpenAI system message
- **Messages list**: Converts all content block types (text, image, tool_use, tool_result)
- **Tools and tool_choice**: Maps Anthropic tools to OpenAI format, tool_choice mappings (auto, any->required, tool, none)
- **Other parameters**: max_tokens, stop_sequences->stop, temperature, top_p, stream, metadata

### Phase 2: Response Translation (OpenAI Chat -> Anthropic Messages) ✅
Implemented `chat_completion_to_messages()` in `src/messages/translator.py`:

- **Response envelope**: Builds complete Anthropic message with id, type, role, model, usage, stop_reason
- **Stop reason mapping**: stop->end_turn, length->max_tokens, tool_calls->tool_use, content_filter->refusal
- **Content blocks**: Converts text content and tool_calls to Anthropic format

### Phase 3: Streaming Translation (OpenAI Stream -> Anthropic SSE) ✅
Created `src/messages/stream_adapter.py`:

- **ChatToMessagesStreamAdapter**: Full streaming translation
- **Event flow**: message_start -> content_block_start -> content_block_delta -> content_block_stop -> message_delta -> message_stop
- **Tool use support**: Proper input_json_delta streaming with ID stability
- **Usage tracking**: Captures input/output tokens from stream

### Phase 4: Route Handler Integration ✅
Updated `src/api/routes/messages.py`:

- Replaced 501 error path with actual translation logic
- Non-streaming: Translates request, forwards to /v1/chat/completions, translates response
- Streaming: Wraps OpenAI stream with ChatToMessagesStreamAdapter
- Graceful error handling during translation

### Phase 5: Tests ✅
Created comprehensive test coverage:

- **`tests/test_messages_translator.py`** (46 tests):
  - Text-only, mixed text+image, tool_use/tool_result, assistant-prefill, system param
  - tool_choice mapping; tool_use id preservation
  - stop_reason mapping; usage conversion

- **`tests/test_messages_stream_adapter.py`** (11 tests):
  - Text streaming, tool_call streaming with input_json_delta
  - Multi-tool call ordering, finish_reason mapping
  - Empty stream handling, interleaved content

---

## Architecture (Fully Implemented)

```
src/
├── api/routes/messages.py     ✅ Route handler (complete with translation)
├── messages/
│   ├── __init__.py            ✅ Module exports (translator + stream_adapter)
│   ├── translator.py          ✅ Full implementation
│   └── stream_adapter.py      ✅ Full implementation
└── types/chat.py              ✅ Anthropic types defined

tests/
├── test_messages_translator.py    ✅ 46 tests
└── test_messages_stream_adapter.py ✅ 11 tests
```

## Implementation Sequence (Completed)

1. ~~Types + translator skeleton (non-streaming) with unit tests.~~ ✅ Done
2. ~~Route handler with simulation path (non-streaming) and tests.~~ ✅ Done
3. ~~Implement `messages_to_chat_completions()` with unit tests~~ ✅ Done
4. ~~Implement `chat_completion_to_messages()` with unit tests~~ ✅ Done
5. ~~Create streaming adapter (`stream_adapter.py`) + tests~~ ✅ Done
6. ~~Wire translation path into route handler~~ ✅ Done
7. ~~Unit tests for translation path~~ ✅ Done (57 total tests)

## Sources (for quick reference)
- Create a Message API reference: https://platform.claude.com/docs/en/api/python/messages/create
- Tool use implementation: https://docs.anthropic.com/en/docs/agents-and-tools/tool-use/implement-tool-use
- Streaming Messages: https://docs.anthropic.com/claude/reference/messages-streaming
- Handling stop reasons: https://docs.anthropic.com/en/api/handling-stop-reasons
- Messages API reference (headers/required): https://anthropic.mintlify.app/it/api/messages
- API release notes (tool_choice none, tools optional): https://docs.anthropic.com/en/release-notes/api
