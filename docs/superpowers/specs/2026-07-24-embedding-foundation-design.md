# Embedding Foundation (Subsystem #1)

**Date:** 2026-07-24
**Status:** Design approved; ready for implementation plan
**Umbrella:** [Passive Knowledge Architecture](2026-07-24-passive-knowledge-architecture-design.md)
**Type:** Subsystem spec — the dependency-free foundation the semantic index
(spec #2) builds on. Delivers an `Embedder` abstraction, two local-first
backends, and config/resolution. Nothing consumes embeddings yet.

## Scope

**In:** the `Embedder` interface, an Ollama-native and an OpenAI-compatible
backend, a factory, the `embedding` routing role + a dedicated resolver,
formalizing `"ollama"` as a recognized `ProviderConfig.Type` value (embedding
backend selection only), and tests.

**Out (later specs):** chunking, DB tables (`chunks`/`embeddings`), the index
engine, context-pack wiring, any TUI/settings surface, and the managed local
inference **service** lifecycle. The Embedder is endpoint-driven so a future
llama.cpp/Ollama service manager just supplies a base URL.

## Motivation

Marshal has no embedding capability today. The semantic code index needs one,
and it must be local-first (nomic-embed-text via Ollama) with room to grow into
a managed local inference service. Embedding is a distinct capability — it
returns vectors, not chat events, and not every provider offers it — so it gets
its own interface rather than extending `Provider`. This spec builds only that
foundation, independently testable and useful, so the index spec can depend on a
stable seam.

## Package & interface

New package `internal/llm/embedding/`:

```go
// Embedder is implemented by every embedding backend Marshal can talk to.
type Embedder interface {
    // Embed returns one vector per input text, in input order. An empty
    // input slice returns an empty result and no error.
    Embed(ctx context.Context, texts []string) ([][]float32, error)
    // Model returns the embedding model name. Spec #2 stores this alongside
    // each vector so a model change marks vectors stale.
    Model() string
    // Dims returns the embedding dimension. Discovered on the first
    // successful Embed and cached; 0 before the first embed.
    Dims() int
}
```

Files:

- `embedding.go` — the `Embedder` interface, shared types, sentinel errors, and
  a package-level `Probe(ctx, Embedder) (dims int, err error)` helper (embeds a
  short fixed string, returns the discovered dimension). `Probe` exists for a
  future "test embedding connection" affordance and for tests; **no UI wiring in
  this spec.**
- `ollama.go` — Ollama-native backend (`POST {base_url}/api/embed`).
- `openai.go` — OpenAI-compatible backend (`POST {base_url}/v1/embeddings`).
- `factory.go` — constructs the correct backend from a resolved provider entry +
  model name.

### Backend wire formats

- **Ollama-native** — request `{"model": <model>, "input": [<texts...>]}` to
  `/api/embed`; response `{"embeddings": [[...], ...]}`. (The native endpoint
  accepts an array input and returns one embedding per input.)
- **OpenAI-compatible** — request `{"model": <model>, "input": [<texts...>]}` to
  `/v1/embeddings`; response `{"data": [{"embedding": [...], "index": n}, ...]}`.
  Results are re-sorted by `index` before returning, so order matches input
  regardless of server ordering. This path also works against Ollama's own
  `/v1/embeddings` endpoint, LM Studio, a llama.cpp server, and hosted
  OpenAI-style APIs.

## Resolution & config

Reuses the existing provider/preset/profile machinery — **no new config
structs.** Verified: the profile `Roles map[AgentRole]string` is copied
wholesale through `merge.go` and round-tripped by `save.go`, and nothing filters
role keys against `AllRoles`, so an `embedding` key survives load→merge→save
cleanly.

- Add `RoleEmbedding AgentRole = "embedding"` in `internal/llm/routing/types.go`.
  It is **deliberately excluded from `AllRoles`**, so chat onboarding/settings
  that enumerate `AllRoles` do not list embedding as a chat role.
- Add `StaticRouter.ResolveEmbedding() (Route, error)`:
  - Reads the active profile's `Roles[RoleEmbedding]` → preset → provider+model.
  - **No implementer fallback** (unlike `ResolveRole`): a chat model cannot
    produce vectors, so silently falling back would be a latent bug. Returns a
    distinct `ErrEmbeddingNotConfigured` when the role is unset, letting spec #2
    gracefully disable semantic indexing rather than error.
  - Honors `RemoteAllowed` — a remote embedding provider under
    `remote_providers_allowed = false` returns `ErrRemoteProviderBlocked`, the
    same as chat resolution.

Example configuration:

```toml
[providers.ollama]
type = "ollama"                       # selects the native /api/embed backend
base_url = "http://localhost:11434"

[models.presets.nomic]
provider = "ollama"
model = "nomic-embed-text"

[agent_profiles.local.roles]
embedding = "nomic"
```

### Backend selection

Driven by the provider entry's `Type`:

| `type` | Backend |
|---|---|
| `"ollama"` | Ollama-native `/api/embed` |
| `"openai_compatible"` or `""` | OpenAI-compatible `/v1/embeddings` |

**Formalizing `"ollama"` as a recognized `Type`.** This spec makes `"ollama"` a
first-class value of `ProviderConfig.Type`, not an ad-hoc string the embedding
factory happens to match:

- Update the `Type` field doc comment in `internal/app/config/types.go` (line
  294) to document `"ollama"` as a recognized value used for embedding backend
  selection, alongside `"openai_compatible"`.
- There is **no central config-load validation** of `Type` today — it is only
  resolved at construction time — so no validator needs changing. Both factories
  switch on `Type` at construction.
- The **embedding factory** switches on it as above (native vs OpenAI-compatible).
- The **chat factory** (`provider.NewFromConfig`) is intentionally **not**
  extended to build a chat backend for `"ollama"`: a provider entry typed
  `"ollama"` is meant for embedding. If used as a chat provider it continues to
  return the existing `unsupported type` error (a native Ollama chat provider is
  a documented future extension, out of scope here). A test pins this intended
  rejection so the boundary is explicit.

Ollama users who prefer the OpenAI-compatible path can instead leave `type`
unset and point `base_url` at Ollama's `/v1` endpoint — that routes through the
OpenAI-compatible embedding backend and also yields a working chat provider.

API-key resolution reuses the existing `resolveAPIKey` behavior (literal
`api_key` wins over `api_key_env`; absent auth is normal for local endpoints).

## Behavior & robustness

- **Batching** — `Embed` splits `texts` into bounded batches (default ~64
  inputs per request) and concatenates results in order. One request when the
  input fits.
- **Retry** — bounded retries with backoff on transient network/5xx errors;
  respects `ctx` cancellation and deadlines. Non-transient errors (4xx, decode
  failures) return immediately.
- **Dimension consistency** — `Dims()` is set from the first successful embed
  and cached; any later vector whose length differs is an error (guards against
  a silently swapped model).
- **Empty input** — returns `([][]float32{}, nil)` without a network call.

## Testing

- **Backend unit tests** (`httptest` fakes for both wire formats): request body
  shape, batching/splitting, order preservation (including out-of-order
  `index` for the OpenAI path), dimension discovery, retry-then-succeed, and
  non-transient error passthrough.
- **Router tests**: `ResolveEmbedding` resolves provider+model from a profile
  role; returns `ErrEmbeddingNotConfigured` when unset (proving no implementer
  fallback); returns `ErrRemoteProviderBlocked` for a remote provider under
  local-only.
- **Config round-trip test**: a profile with `roles.embedding = "nomic"`
  survives load→merge→save with the key intact.
- **Factory test**: `type="ollama"` builds the native backend; `""` /
  `"openai_compatible"` builds the OpenAI-compatible backend.
- **Chat-factory boundary test**: `provider.NewFromConfig` with `type="ollama"`
  returns the existing `unsupported type` error — pinning that `"ollama"` is an
  embedding-only type for now.
- **Live contract tests**: gated/skipped when no local endpoint is reachable,
  mirroring the existing provider `integration_test.go` gating.

## Non-goals / deferred

- Chunking strategy, `chunks`/`embeddings` tables, and the incremental index
  engine (spec #2).
- Context-pack wiring and retrieval fusion (spec #2 / retrieval).
- Any TUI/settings surface for configuring or testing the embedding provider
  (the `Probe` helper is the seam; wiring is later).
- Managed local inference service lifecycle (documented umbrella extension).

## Open questions handed to implementation

- Default batch size and retry/backoff constants — start with ~64 and a small
  bounded backoff; tune if a real backend argues otherwise.
- Whether `Dims()` should also be overridable via preset config for backends
  that don't return a stable dimension on probe — default to discovery-only
  unless a backend needs it.
