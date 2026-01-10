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

**Severity:** Resolved (2026-01-09)

**Resolution:** Implemented an in-memory model tree that preserves `extends` in raw config and resolves inheritance dynamically for runtime config. Admin endpoints expose the inheritance graph, and the admin UI shows the model tree.

**Operational note:** To apply inheritance changes to active backends, call `POST /admin/config/reload`.

---

### 3. SSE decoder can grow its buffer without bounds

**Location:** `src/parsers/response_pipeline.py` (SSEDecoder)

**Severity:** Major (Memory risk)

**Description:** The SSE decoder appends incoming text until it sees a `\n\n` delimiter. If an upstream stream never sends a delimiter, the buffer can grow indefinitely.

**Recommendation:** Add a maximum buffer size with truncation or hard failure behavior.

---

## Minor Issues / Limitations

### 4. SSE error detection only scans the first 4096 bytes

**Location:** `src/core/sse.py`

**Severity:** Minor (Detection limit)

**Description:** `STREAM_ERROR_CHECK_BUFFER_SIZE` is 4096 bytes, so error events that appear after the first 4 KB of a stream may not be detected.

---

## Configuration Notes

### Environment Variable Precedence

The proxy supports multiple ways to configure settings:

1. **Environment Variables (Highest Priority):**
   - `YALLMP_HOST` - Override server host
   - `YALLMP_PORT` - Override server port
   - `YALLMP_CONFIG` - Override config path
   - `YALLMP_ADMIN_PASSWORD` - Admin password for protected model changes

2. **Environment Variables in .env Files:**
   - `.env` - For config.yaml

3. **Configuration Files:**
   - `configs/config.yaml` - Unified configuration

### Timeout Configuration

Default request timeout is set to 540 seconds (9 minutes) in config.yaml. This is quite long and may need adjustment based on your use case.

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
8. ~~Model Tree Structure~~ - Implemented. See [model_tree.md](model_tree.md) for details.
9. ~~Dynamic model inheritance~~ - Implemented via model tree resolution.
10. ~~Cascading deletes~~ - Implemented via `/admin/models/{name}?cascade=true`.

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

Last Updated: 2026-01-09
