# models.dev preset enrichment: bulk presets at provider registration

Status: approved design and written specification; implemented (merged to `main` as `f612a64a`).
Date: 2026-10-24.
Workflow: [Marshal brainstorming](../../internal/skills/builtin/brainstorming.md), followed by Marshal's internal plan-writing skill after written-spec review.

## Context and classification

Registering a provider in Marshal currently produces one preset for one chosen
model. A provider that reports forty models leaves the user to hand-build the
other thirty-nine, guessing context windows, output limits, tool support and
prices. models.dev publishes exactly that metadata for most hosted providers,
so the gap is data Marshal can fetch once and apply in bulk.

The request was to streamline preset creation using models.dev. The user
sharpened it during interrogation: enrichment should happen **at provider
registration** — a successful `/connect` flow builds a preset for **every model
the provider reports**, not just the one the user picks.

This is a **bounded** change. It adds two application-layer components and one
user-global sidecar, and it edits the existing `/connect` flow. It does not
restructure provider discovery, the routing layer, or the preset type.

## Approved design

### 1. Registration flow

`/connect` becomes:

1. **Probe the provider** and retain its live reported model list.
2. **Ask for models.dev consent once** if no preference exists. The setting is
   `privacy.models_dev_enrichment`: omitted = ask, `true` = allow, `false` =
   deny, editable in Settings.
3. **Prepare every reported model**, asynchronously downloading or reading the
   models.dev cache when allowed.
4. **Choose the active model** from that list. Manual model entry remains
   available.
5. For the active model only, retain the existing optional Ollama `/api/show`
   capability probe, applying its result automatically without a confirmation
   screen.
6. **Review the normal summary and finish.** It shows the active model plus
   counts such as "42 presets prepared · 35 models.dev matches · 7 using
   fallbacks."

Completion saves all prepared presets and makes the selected preset active
through the existing single-model profile. Renaming a provider still works
because preset names are assigned only at completion.

Settings' per-role model assignment remains unchanged; it benefits from the
presets already created but does not trigger bulk creation itself.

### 2. Catalog boundary and matching

Add two application-layer components rather than extending provider discovery:

- `internal/llm/modelcatalog`: models.dev decoding, HTTP caching, explicit
  provider bindings, and exact model lookup.
- `internal/app/presetsetup`: metadata resolution, bulk preset application, and
  refresh provenance. The TUI invokes these services but does not own their
  policy.

The client downloads only the public `https://models.dev/api.json` document. It
sends **no API keys, provider URLs, model IDs, or project data**.

Provider bindings are explicitly reviewed mappings, for example:

- `gemini` → `google`
- `together` → `togetherai`
- `fireworks` → `fireworks-ai`
- `kimi` + the CN coding endpoint → `kimi-code-plan-cn`
- `opencode-go` → `opencode-go`
- `ollama-cloud` → `ollama-cloud`

A renamed provider retains its template binding. A custom or repointed endpoint
is not assumed to be the original hosted service.

Binding resolution is deterministic:

- A provider with no binding is **not enriched**. Every preset it creates uses
  provider metadata and Marshal defaults only, and no models.dev lookup is
  attempted for it.
- A binding that depends on the endpoint (the `kimi` CN coding case) resolves
  only when the configured base URL matches the reviewed value. A `kimi`
  provider pointed anywhere else has no binding.
- A provider whose binding cannot be resolved unambiguously is treated as
  unbound rather than guessed.

Model IDs must match **exactly and case-sensitively** within that provider.
Tagged models such as `deepseek-v4.1-flash:1017` work when models.dev carries
that exact tag. There is no fuzzy matching, tag stripping, or cross-provider use
of `canonical_model_id`.

### 3. Initial preset resolution

For each new preset, fields resolve independently:

**provider bulk metadata or active-model probe → models.dev → Marshal defaults**

models.dev supplies:

| models.dev field | Marshal preset field |
|---|---|
| `limit.context` | `context_window` |
| `limit.output` | `max_output_tokens` |
| `tool_call` | `tool_calling` (`true` → `native`, `false` → `none`) |
| `cost.input` | input pricing |
| `cost.output` | output pricing |
| `cost.reasoning` | reasoning pricing |
| `cost.cache_read` | cache-read pricing |
| `cost.cache_write` | cache-write pricing |

Missing context/output values use the existing local/remote defaults. Unknown
tool support stays unknown. Missing pricing remains unset so normal pricing
lookup applies.

Prices convert from models.dev's USD per million tokens to Marshal's integer
cents per million (`pricing.ModelPricing`) using nearest-cent rounding. Invalid
or negative values are ignored individually. Only base text-token rates are
imported; audio prices, tiered prices, and `context_over_200k` are ignored.

models.dev's reasoning, modality, attachment, and temperature flags do **not**
synthesize Marshal thinking controls.

### 4. Safe reconnect refreshes

A user-global sidecar, separate from the public catalog cache, stores per-field
automatic provenance:

```json
{
  "version": 1,
  "presets": {
    "openrouter/example/model": {
      "catalog_provider": "openrouter",
      "catalog_model": "example/model",
      "fields": {
        "context_window": {
          "source": "models.dev",
          "value": "262144"
        },
        "pricing.input": {
          "source": "models.dev",
          "value": "25"
        }
      }
    }
  }
}
```

Values use canonical strings so integer pricing and context sizes survive JSON
round trips exactly. State tracks automatic values from `provider`,
`models.dev`, `default`, or `unknown`.

On reconnect:

- Missing presets are created normally.
- For an automatically managed field, if the current preset value still equals
  the last-applied value, fresh metadata may update it using the normal
  precedence.
- If the current value differs, Marshal treats it as a user edit, preserves it,
  and removes that field from automatic management.
- Other managed fields still refresh independently, so editing context does not
  freeze output limits or pricing.
- If fresh metadata omits a previously supplied field, the existing value is
  retained rather than erased.
- Existing presets without provenance—including all pre-feature presets—are
  left untouched.
- Presets no longer reported by the provider are not deleted.
- Missing or corrupt provenance disables updates to existing presets; it never
  authorizes overwriting them.

This compare-before-refresh rule also detects edits made directly in
`config.toml`, without requiring every settings surface to update the sidecar.

### 5. Cache, failure, and persistence behavior

**File locations.** Both files live under the user data directory
(`config.DataDir`, i.e. `$MARSHAL_DATA_DIR` / `$XDG_DATA_HOME/marshal` /
`~/.local/share/marshal`), alongside the existing `models/discovered.json`:

- `<dataDir>/models-dev/catalog.json` — the public catalog cache.
- `<dataDir>/models-dev/preset-provenance.json` — the per-field provenance
  sidecar described in §4.

They are separate files so that clearing the catalog cache never discards
provenance, and a corrupt provenance file never invalidates a good cache.

**Catalog cache.** `catalog.json` stores the decoded document plus the HTTP
`ETag` and fetch timestamp. A cache younger than 24 hours is used without a
request; an older one triggers a conditional request and is replaced on `200`
or re-stamped on `304`.

**Empty provider lists.** A provider that reports zero models creates no
presets and no provenance; the flow falls through to manual model entry.

- Use a bounded asynchronous request with a response-size limit.
- On refresh failure, use a valid stale cache; without one, continue with
  provider/default fallbacks.
- Denied consent means no models.dev request or enrichment.
- Offline or unmatched results never block provider registration.
- Catalog or provenance data is never placed in prompts and never marks
  `ModelInfo.ProviderReported`.
- Provider and preset config is saved before provenance. If provenance then
  fails to save, the presets remain saved with a warning; subsequent refreshes
  fail safe rather than risking user edits.
- Cancelling before completion applies no provider/preset changes. An explicit
  consent answer may still be remembered.

### 6. Verification

Tests will cover:

- Exact provider/model matching, tagged IDs, renamed providers, and ambiguous
  endpoint rejection.
- Source precedence, fallback behavior, false tool support, free pricing, price
  conversion, and malformed fields.
- Fresh/stale/304 cache behavior, offline fallback, corrupt data, and no request
  without consent.
- Bulk creation of every reported model and no catalog-only additions.
- Catalog refresh versus independently edited context, output, tool, and pricing
  fields.
- Pre-feature presets remaining untouched and omitted metadata never erasing
  values.
- `/connect` consent, cancellation, rename, active-model selection, and removal
  of limit confirmation.
- Config/provenance save failures and runtime reload behavior.

## Decisions and trade-offs

**Connect-scoped enrichment over a runtime metadata layer.** models.dev is a
setup-time service. It never re-enters provider discovery, never feeds the
runtime limits path, and never clamps a live request. The cost is that a preset
created before this feature stays stale until the user reconnects; the benefit
is that no third-party figure can silently shrink a working context window.

**Bulk creation over a searchable catalog.** The user asked for presets for
every reported model, not a browsable models.dev index. This keeps the flow
inside `/connect` and avoids a second model-picking surface.

**Field-level provenance over whole-preset ownership.** A preset is not "managed
by models.dev" or "managed by the user"; each field is. This is what lets a
reconnect refresh pricing while preserving a hand-tuned context window, and it
is why the sidecar stores canonical strings per field rather than a snapshot of
the whole preset.

**Explicit provider bindings over name inference.** Marshal provider names and
models.dev provider IDs diverge (`gemini`/`google`, `together`/`togetherai`).
A reviewed table is auditable; guessing from substrings would silently attach
the wrong catalog to a repointed endpoint.

**Exact model matching over fuzzy matching.** A near-miss would attach another
model's limits and prices to a preset, which is worse than falling back to
defaults. Tagged variants therefore only enrich when models.dev carries the
exact tag.

**Remove limit confirmation from `/connect`.** With bulk creation, a per-model
confirmation screen would either block the flow or be skipped for all but one
model. The active model's limits are resolved automatically and remain editable
in Settings afterwards.

## Assumptions

- models.dev's public `api.json` remains the only endpoint used, and its
  `limit`/`cost`/`tool_call` field names are stable enough to decode
  defensively (unknown fields ignored, malformed entries skipped).
- The existing `[privacy]` config section is the right home for
  `models_dev_enrichment`; it is user-global in practice because provider and
  preset config already is.
- `pricing.ModelPricing` (US cents per million tokens) is the correct target
  for models.dev's USD-per-million rates, so conversion is a factor of 100 with
  nearest-cent rounding.
- The existing single-model profile mechanism is sufficient to make the chosen
  preset active; no new "active preset" concept is introduced.
- The Ollama `/api/show` probe stays scoped to the active model. Bulk probing
  every reported model is out of scope.

## Non-goals

No searchable models.dev catalog, model recommendations, background
synchronization, automatic role assignment, bulk `/api/show` probing, preset
deletion, tiered/audio pricing, or runtime third-party limits table. models.dev
never directly clamps runtime limits.

## Open questions

None. All interrogation points were resolved before approval.
