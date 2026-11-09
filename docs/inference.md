# Decision models, embeddings, and speech recognition

These endpoints use model aliases, app-key policy, node access rules, rate and
concurrency admission, routing, and metadata logging. Declare new endpoint kinds
in provider/node `endpoints`: a legacy chat node does not implicitly gain decision
or audio support. `api_type: systemone` defaults to the native System One endpoint.
`/v1/models` and admin views expose configured endpoint capabilities; these describe
configured routes, not proof that an upstream has loaded a particular model.

Start with [the inference example](../configs/config_inference_example.yaml).
Keys live in `configs/.env` with normal environment substitution. The Go proxy
does not load speech models: run an ASR service with a supported HTTP/WebSocket
contract. No HTTP reverse forwarder is needed.

## Native decision APIs

`POST /v1/systemone` (also `/systemone`) accepts TypeSafe's native JSON contract:

```sh
curl http://127.0.0.1:7979/v1/systemone \
  -H 'Content-Type: application/json' \
  -d '{"model":"jev","state":{"ticket":"Refund the duplicate charge"},"questions":{"billing":{"type":"noul","instructions":"Is this about billing?"}}}'
```

Use `api_type: systemone` and `api_base: https://api.typesafe.ai/v1`. Named `noul`,
`choice`, `score`, and `box` questions, JSON/base64 image assets, provider extensions,
probabilities, confidence, and upstream errors retain their native representation.
Multipart image uploads are not implemented. The provider validates detailed
criteria and limits. Streaming is rejected.

`POST /v1/decisions` (also `/decisions`) accepts the distinct OpenAI contract:

```json
{"model":"decision-alias","input":"Refund the duplicate charge","questions":[{"type":"predicate","name":"billing","instructions":"Is this about billing?"}]}
```

Configure an OpenAI-compatible provider with `endpoints: [decisions]`. Input,
ordered questions/answers, typed values, confidence, and per-question refusals are
preserved. No conversion to System One or Chat Completions takes place. State and
questions both contribute to token reservations; input/output usage reconciles
them. Exact caching is available when enabled; semantic chat caching is excluded.

## Embeddings

`POST /v1/embeddings` (also `/embeddings`) accepts a string, strings, integer token
IDs, or batches of integer token IDs. Inputs must be non-empty and homogeneous;
token IDs must be nonnegative integers. `dimensions` must be a positive integer;
`encoding_format` must be `float` or `base64`. Model-specific feature support and
limits remain upstream-owned. Streaming is rejected. Provider extensions, vector
encoding, batch indices, model identity, and usage are preserved.

```sh
curl http://127.0.0.1:7979/v1/embeddings \
  -H 'Content-Type: application/json' \
  -d '{"model":"embed","input":[[101,102],[103]],"encoding_format":"base64"}'
```

Pools and fallbacks are anchored to the first embedding-capable node in configured
primary/fallback order before balancing or health selection, including when the
primary is chat-only. By default, compatible nodes need
the same API type and effective upstream model. `embedding_space` on a provider
or node declares equivalent replicas with different model IDs: identical weights,
tokenizer, preprocessing, and vector semantics. Equal dimensions alone do not
establish compatibility. Effective inference defaults, including dimensions and
input type, must also agree. Incompatible nodes are excluded.

GigaChat uses native `/embeddings` with its existing OAuth or mTLS setup. Per-item
usage is aggregated into top-level usage. Base64 output uses little-endian float32.
GigaChat token-ID inputs and custom dimensions are rejected, not silently decoded
or truncated. The semantic cache's embedding client uses the same native dispatch.

Known chat-only defaults and chat response parsers do not apply to embeddings,
decisions, or transcription. Other JSON provider extensions remain available.
Multipart transcription rewrites the model field only.

## File transcription

`POST /v1/audio/transcriptions` (also `/audio/transcriptions`) takes multipart
data with exactly one `model` field and one non-empty `file` part:

```sh
curl http://127.0.0.1:7979/v1/audio/transcriptions \
  -F model=parakeet -F file=@recording.wav -F response_format=json
```

Audio bytes, filenames, part headers, repeated fields, query parameters, and
provider options are preserved. The model alias is rewritten per attempt.
Uploads are bounded and buffered for retry before the response starts; the proxy
creates no temporary audio files. `stream=true` streams the upstream SSE response
with usage extraction. The upstream determines supported codecs, timestamps,
languages, and output formats. Live audio uses a WebSocket session instead.

## Live transcription

- `GET /v1/realtime?model=parakeet&intent=transcription`: native OpenAI-style
  transcription protocol, configured with `endpoints: [realtime]`.
- `GET /ws/voice?model=parakeet-voice`: native T3-style protocol, configured with
  `endpoints: [voice]`.

The required `model` query selects a proxy alias. Realtime consumes it locally:
the upstream URL carries `intent=transcription` without `model`, which would select
a conversation session. The upstream model is applied to modern `session.update`
or legacy `transcription_session.update` configuration. A supplied model must match
the public alias or selected upstream model. Model switches and conversation
sessions are rejected. Clients send normal session configuration and audio events;
the proxy does not convert sample rates or codecs.

T3-style sessions preserve JSON `start`/`finish`, binary Float32 PCM audio, and
`ready`/`update`/`finished` events with committed and tentative text. Use its required
16 kHz mono audio. That protocol uses the serving runtime's selected model:
configure one node/runtime per intended model. The proxy does not turn a batch
model into a native streaming model or translate between live protocols.

Native clients use the normal app-key header. Browsers can offer WebSocket
subprotocols `["realtime", "yallmp-key.<app-key>"]`; the credential is consumed
locally and never forwarded or echoed. `openai-insecure-api-key.<app-key>` is also
accepted. Required headers still apply. Same-origin browser requests are allowed
by default; `proxy_settings.asr.allowed_origins` adds origin host patterns. Upstream
authentication uses its configured API key. Redirects are refused.

Both concurrency slots remain occupied until both directions stop. Eligible
siblings/fallbacks can handle failed handshakes, with health/attempt accounting.
After upgrade the session is pinned: no replay or mid-session failover. Message
size and session duration are bounded. Close and cancellation release both
connections and slots. Live audio/transcripts are not stored in full-request
sidecars.

Token usage reconciles token quotas. Duration usage (`usage.type: duration`,
`usage.seconds`) persists as `audio_seconds` and `usage_stats` and emits the OTel
counter `yallmp.proxy.audio.duration` in seconds. Live per-result usage accumulates.
Audio seconds are not converted to tokens, and token quotas do not impose a
duration budget. Unmetered upstreams still have rate, concurrency, message-size,
and session-duration limits.

## Limits and verification

```yaml
proxy_settings:
  asr:
    max_upload_bytes: 33554432     # Entire multipart body; default 32 MiB
    max_message_bytes: 1048576     # Each WebSocket direction; default 1 MiB
    session_timeout_seconds: 600  # Includes session setup; default 10 minutes
    allowed_origins: []           # Same-origin only
```

Provider/node `realtime_path` overrides the absolute WebSocket path on the same
upstream host, without a query or fragment. Defaults are the versioned API's
`/realtime` or the host's `/ws/voice`.

Run `task test:inference` for focused contracts, `task test` for the full suite,
and `task test:race` for lifecycle/concurrency checks. Tests use fake upstreams;
recognition quality, GPU behavior, and microphone latency require validation
against the selected runtime/model.

## Protocol references

- [TypeSafe System One](https://docs.typesafe.ai/api/system-one)
- [OpenAI Decisions](https://developers.openai.com/api/reference/resources/decisions/methods/create)
- [Embedding request/response contract](https://developers.openai.com/api/reference/resources/embeddings/methods/create)
- [Realtime transcription](https://developers.openai.com/api/docs/guides/realtime-transcription)
- [T3 voice protocol](https://github.com/pingdotgg/t3code/blob/ade473018d32003658b7d38693e083ee2e314bd8/packages/contracts/src/speech.ts#L256)
