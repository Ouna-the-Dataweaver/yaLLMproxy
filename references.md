# Local References

Cloned under `/home/theio/repos/references`.

## OpenAI API Docs

- Chat Completions API: `https://platform.openai.com/docs/api-reference/chat/create`
- Legacy Completions API: `https://platform.openai.com/docs/api-reference/completions/create`
- Responses API: `https://platform.openai.com/docs/api-reference/responses`

OpenAI's Chat Completions API creates model responses from conversation-style message lists, while the older Completions API covers prompt-in/text-out completion calls. The Responses API is OpenAI's newer unified response endpoint for multimodal, tool-using, and agent-style workflows.

## Bifrost AI Gateway

- Path: `/home/theio/repos/references/bifrost`
- Source: `https://github.com/maximhq/bifrost.git`
- Checkout: `b1e203b4b`

Bifrost is a high-performance AI gateway that exposes one OpenAI-compatible API for many LLM providers, with failover, load balancing, caching, monitoring, and governance features. The core is Go (`go.mod` modules under `core`, `transports`, `framework`, and `cli`), with a React 19 + TypeScript + Vite web UI under `ui`.

## TensorZero

- Path: `/home/theio/repos/references/tensorzero`
- Source: `https://github.com/tensorzero/tensorzero.git`
- Checkout: `62eb8f63e`

TensorZero is an LLMOps platform combining an LLM gateway, observability, evaluation, optimization, and experimentation. The gateway and main workspace are Rust (`crates/Cargo.toml`, edition 2024, Axum/Tokio/Reqwest), with TypeScript/Node tooling, Python bindings/scripts, and a web UI/docs toolchain managed with pnpm.

## Envoy AI Gateway

- Path: `/home/theio/repos/references/ai-gateway`
- Source: `https://github.com/envoyproxy/ai-gateway.git`
- Checkout: `f2d59a6a`

Envoy AI Gateway uses Envoy Gateway to route application traffic to generative AI providers and self-hosted model serving clusters, including auth, routing, rate limiting, and provider integrations. It is primarily Go (`go.mod`) on Kubernetes/Envoy Gateway APIs, with dependencies on Envoy control-plane, OpenTelemetry, provider SDKs, controller-runtime, and a Docusaurus/React documentation site under `site`.

## Gateway API Inference Extension

- Path: `/home/theio/repos/references/gateway-api-inference-extension`
- Source: `https://github.com/kubernetes-sigs/gateway-api-inference-extension.git`
- Checkout: `0bd1effa`

Gateway API Inference Extension turns an ext-proc capable Gateway API implementation into an inference gateway for self-hosted generative models on Kubernetes, with routing/scheduling APIs such as InferencePool and lightweight endpoint picking. It is primarily Go (`go.mod`) using Kubernetes APIs, controller-runtime, Envoy external processing/control-plane libraries, OpenTelemetry, Prometheus, gRPC, CRDs, and conformance-test tooling.
