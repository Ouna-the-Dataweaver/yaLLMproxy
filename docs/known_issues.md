# Known Issues

This document tracks known issues, limitations, and workarounds for yaLLMproxy.

## Major Issues

### 1. Middleware stubs are exported but empty

**Location:** `src/middleware/`

**Severity:** Major (Feature gap)

**Description:** `src/middleware/stateful_api.py` and `src/middleware/parsers.py` only contain docstrings, but `src/middleware/__init__.py` exports `StatefulAPI`, `parse_response`, `reparse_bad_chunk`, and `reparse_thinking`. Importing `src.middleware` will fail until implementations exist.

**Impact:** Features that rely on these helpers will not function as documented.

---

### 2. Model inheritance is static, not dynamic

**Location:** `src/config_store.py`

**Severity:** Major (Feature gap)

**Description:** Model inheritance (`extends` field) is resolved statically at config load time. Changes to base models do not automatically propagate to derived models.

**Example:**
```yaml
# Initial config
- model_name: base-model
  model_params:
    api_base: https://api.example.com/v1
    timeout: 120

- model_name: derived-model
  extends: base-model
```

When `base-model` is updated to `timeout: 300`, `derived-model` still has `timeout: 120`.

**Impact:**
- Derived models don't reflect updates to base models
- Modifying a base model requires also updating all derived models manually
- Runtime configuration changes via API don't trigger inheritance re-resolution

**Workaround:** When modifying a base model, also update all derived models that extend it.

**Tests:** See `tests/test_model_inheritance.py::TestDynamicInheritance` for expected behavior tests that currently fail.

---

### 3. SSE decoder can grow its buffer without bounds

**Location:** `src/parsers/response_pipeline.py` (SSEDecoder)

**Severity:** Major (Memory risk)

**Description:** The SSE decoder appends incoming text until it sees a `\n\n` delimiter. If an upstream stream never sends a delimiter, the buffer can grow indefinitely.

**Recommendation:** Add a maximum buffer size with truncation or hard failure behavior.

---

## Minor Issues / Limitations

### 4. Admin config masking does not hide short API keys

**Location:** `src/api/routes/config.py`

**Severity:** Minor (Display)

**Description:** `_mask_sensitive_data` only masks `api_key` values longer than 8 characters. Shorter values are displayed in full in the admin config view.

---

### 5. SSE error detection only scans the first 4096 bytes

**Location:** `src/core/sse.py`

**Severity:** Minor (Detection limit)

**Description:** `STREAM_ERROR_CHECK_BUFFER_SIZE` is 4096 bytes, so error events that appear after the first 4 KB of a stream may not be detected.

---

### 6. Config merge does not deduplicate models or fallbacks

**Location:** `src/config_store.py`

**Severity:** Minor (Configuration edge case)

**Description:** When a model appears in both default and added configs, the merged runtime config keeps both entries. The router uses the last occurrence, but duplicates remain in the runtime config and fallback lists are not deduplicated.

---

## Configuration Notes

### Environment Variable Precedence

The proxy supports multiple ways to configure settings:

1. **Environment Variables (Highest Priority):**
   - `YALLMP_HOST` - Override server host
   - `YALLMP_PORT` - Override server port
   - `YALLMP_CONFIG_DEFAULT` - Override default config path
   - `YALLMP_CONFIG_ADDED` - Override added config path

2. **Environment Variables in .env Files:**
   - `.env_default` - For config_default.yaml
   - `.env_added` - For config_added.yaml

3. **Configuration Files:**
   - `configs/config_default.yaml` - Base configuration
   - `configs/config_added.yaml` - Runtime-added models

### Timeout Configuration

Default request timeout is set to 540 seconds (9 minutes) in config_default.yaml. This is quite long and may need adjustment based on your use case.

### Parser Configuration

Response parsers are disabled globally by default (`proxy_settings.parsers.enabled: false`) but can be enabled per-model. If enabling parsers globally, ensure `proxy_settings.parsers.paths` includes the endpoints you want to parse.

### Legacy Configuration Support

- `general_settings.enable_responses_endpoint` is still read for backwards compatibility but is not documented; prefer `proxy_settings.enable_responses_endpoint`.

---

## Testing Notes

### Current Test Coverage

- Unit tests exist for core functionality
- Tests can be run with `task test`
- No integration tests for end-to-end scenarios

### Known Test Limitations

- Limited streaming scenarios covered
- No tests for HTTP/2 fallback behavior
- No load testing or concurrency tests
- Limited malformed SSE coverage (decoder-only)

---

## Windows-Specific Issues

### Path Handling

- Windows paths use backslashes, which may need escaping in YAML
- The forwarder and proxy use different virtual environments (.venv_fwd vs .venv)

### Encoding

- The proxy attempts to configure stdout to UTF-8 on Windows, but some terminals may not support this
- Errors in reconfiguration are silently caught

---

## Future Work

### Planned Improvements

1. Complete middleware module implementations
2. Add comprehensive integration tests
3. Implement rate limiting
4. Add metrics/monitoring dashboard
5. Support for more API types beyond OpenAI
6. Configuration validation schema
7. ~~Hot-reload of configuration without restart~~ ✅ DONE - `POST /admin/config/reload`
8. **Dynamic model inheritance** - Changes to base models should propagate to derived models # TODO
9. **Cascading deletes** - Deleting a base model should handle or warn about derived models # TODO

### Known Limitations

- No persistent storage for usage statistics (in-memory only)
- No request authentication or rate limiting
- No support for WebSocket connections (not a priority - almost never used anywhere)
- Limited error recovery in streaming responses

---

## Contributing Fixes

When fixing issues listed above:

1. Update this document to mark the issue as resolved
2. Add tests to prevent regression
3. Update documentation if behavior changes
4. Consider backwards compatibility for existing deployments

---

Last Updated: 2026-01-03
