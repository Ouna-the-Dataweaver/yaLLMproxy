# Models, nodes, and providers

The runtime model is **public model name → available nodes**. Start with
[`configs/config_simple_example.yaml`](../configs/config_simple_example.yaml).

## Add a node

```yaml
nodes:
  - name: gpu-a
    api_base: http://gpu-a:8000/v1
    model: qwen
    upstream_model: Qwen/Qwen3-32B
  - name: gpu-b
    api_base: http://gpu-b:8000/v1
    model: qwen
    upstream_model: Qwen/Qwen3-32B
```

Call the proxy with `model: qwen`. Both nodes belong to that pool automatically.
`upstream_model` is optional and defaults to `model`; it is sent verbatim, including
slashes. Use the same public name only for deployments you consider interchangeable.
Nodes can specify their own `api_key` or inherit settings with `provider: name`.

## Add many models from a provider

```yaml
providers:
  hosted:
    api_base: https://inference.example/v1
    api_key: ${HOSTED_KEY}
    limits:
      rpm: 60
    models:
      - name: qwen
        upstream_model: vendor/qwen
      - llama
```

Put `HOSTED_KEY` in the adjacent `.env` file. This selects two models and derives
two nodes with the shared connection settings. Combined with the node example,
`qwen` has three locations and `llama` has one. The older provider list syntax
(`- name: hosted`) remains supported.

In **Admin → Models & Nodes**, enter the provider URL and credentials, fetch its
model list, select models, then save. You can also enter names manually, one per
line, with `public-name = upstream-name` for aliases. Fetching is read-only; only
saving changes the exposed models. OpenAI-compatible `/models` discovery is
supported; other API types use manual selection.

Generated nodes are runtime data, not additional saved config. Changing the
provider key updates all its generated nodes. Deselecting a model removes that
provider's node; direct nodes stay. A provider still referenced by manually
configured nodes cannot be deleted until those references are removed.

Provider model selections inherit the provider limits independently per upstream
model. Public aliases of the same upstream model share the provider's RPS, RPM,
and concurrency allowance. Direct nodes have their own limits. Token-quota
accounting retains its existing per-backend semantics. Providers without a model
selection retain the older per-node limit inheritance behavior.

## Default routing

1. Match the requested public model and enforce endpoint and node access rules.
2. Prefer healthy nodes and available capacity when alternatives exist.
3. Prefer the eligible node used by the conversation.
4. Otherwise prefer the node associated with the longest matching prompt prefix.
5. Otherwise balance using round-robin, or weights when configured.
6. On retryable failure, try remaining eligible nodes in the pool before explicit
   fallback models. The successful node becomes the conversation's preferred node.

Prefix affinity is enabled when `router_settings.prefix_cache_affinity.enabled`
is omitted. Existing explicit `false` settings stay off. It tracks recent successful
requests, not actual upstream cache contents: upstream eviction can still cause a
cache miss. The existing bounded, in-memory tracker hashes the first one, two, and
three messages. Reloading clears its observations.

Rate availability is checked before selection and admission is enforced per
attempt. When all candidates are unavailable or unhealthy, the existing last-resort
attempt behavior remains. Prefix affinity cannot override hard access restrictions.
Optional health-score ordering and quota-aware scheduling remain available for
advanced configurations; health-score ordering can also reorder explicit fallbacks.

Retryable failures include connection failures and HTTP 408, 409, 429, 500, 502,
503, and 504. Other statuses are returned directly. Once a successful stream has
started, the proxy does not replay it to another node. For compatibility,
`num_retries` still means **total attempts per backend**, with a minimum of one.
Omit it for one attempt at each node before moving on.

## Existing configurations and migration

- `model_list` continues to work. No automatic rewrite of your file occurs.
- Nodes explicitly referenced by top-level `models` entries retain the old public
  names, access controls, and wiring. Their upstream names are not auto-exposed.
- Unreferenced nodes with a `model` form automatic pools. Nodes without a model
  remain infrastructure-only; passthrough nodes retain their existing behavior.
- Automatic pool names must not collide with explicit or legacy model names.
  Remove the old definition as part of the same config save when migrating a name.
- For a legacy node whose upstream name differs from its public name, set
  `model` to the desired public name and `upstream_model` to the exact upstream ID,
  then remove its explicit `models` entry to opt into automatic grouping.
- Automatic pools reject mixed native Anthropic and Chat Completions backends.
  Use separate public names for incompatible APIs. Homogeneous native Messages
  and Responses pools retain their native endpoint during failover.
- Explicit pool editing, different-model fallbacks, and legacy inheritance are
  available under expandable advanced sections in the admin page.

The model overview lists the actual runtime pools and node health. It does not
advance balancing or make requests. `GET /admin/routing-pools` exposes the same
credential-free snapshot; `POST /admin/providers/discover` fetches a provider model
list. Provider CRUD saves model selections transactionally with the existing
full-config validation and atomic persistence.

Start with one model, add its replicas, and use its public name from your clients.
No additional model-pool resource is required.
