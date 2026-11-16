# Images in Marshal — design

**Status:** approved for planning, 2026-11-17.
**Amended:** 2026-11-17 — vision resolves to *attempt* by default rather than
refuse, with a send-anyway override and a retry-without-images recovery (§7).
**Base:** `main` at `9206749b`. Every code anchor below is a symbol and a file,
not a line number, so it survives unrelated edits.
**Plan:** not yet written.

## 1. Problem

Marshal's agent cannot see. An image reaches the model only as text — a
base64 blob or a path — which is useless for the thing images are for: reading
a screenshot, checking a UI the agent just built, reading an error dialog, or
looking at a diagram. Plenty of the models Marshal already routes to (several
of them local) accept image content natively, and nothing in the prompt path
carries it.

The gap is in the *content model*, not in the providers: every message is a
string, so there is nowhere for pixels to live.

### What this is not about

The provider wire formats already support images in all four dialects Marshal
speaks (Anthropic content blocks, OpenAI `image_url` parts, Ollama-native
`message.images`, Responses `input_image`). No provider work is invented here,
only encoding.

## 2. Scope

### In v1

- Images as prompt content on every path that carries content: TUI paste, ACP
  prompts, native tool results, subagent results, MCP tool results, and
  web/fleet uploads. Several images per message.
- One tool, `image.view`, whose job is *viewing* an image that exists: a file
  path, or an image already in the store.
- A content-addressed store with normalization, so the same screenshot viewed
  twice costs one copy and one hash.
- A vision capability flag resolved per route, attempting by default, with
  explicit refusal only for a model that is known or proven unable to take
  images — and a one-keystroke "send anyway" when the refusal is wrong (§7).

### Non-goals

- **No terminal image rendering** (kitty/sixel/iTerm escapes). The TUI shows a
  chip; the model sees pixels. Terminal graphics is its own subsystem with its
  own portability problems.
- **No capture and no render.** `image.capture` (screenshot) and
  `image.render` (draw code/HTML/SVG to an image) are genuinely separate
  tools with separate dependencies; they plug into this plumbing later.
- **No image generation**, no video, no audio, no dedicated OCR tool.
- **No image-token pricing.** Cost estimates continue to ignore image tokens;
  they are counted for the context budget only.
- **No provider files API.** Everything is inline base64 in v1. An upload path
  is a provider-specific optimisation, not a requirement.
- **No interleaved ordering.** A message is text followed by its images. The
  model never emits images. This is the deliberate departure from a full
  "content parts" model (§12).

## 3. Data model

One ref type, defined once, in the dependency-free wire package.

```go
// internal/llm/schema/image.go
type ImageRef struct {
    Hash   string `json:"hash"`   // content hash; the store key
    MIME   string `json:"mime"`   // "image/png" | "image/jpeg"
    Width  int    `json:"width"`
    Height int    `json:"height"`
    Bytes  int    `json:"bytes"`  // stored size, not source size
    Label  string `json:"label,omitempty"` // "screenshot of /tmp/ui.png"
}
```

No bytes in a ref. Bytes are resolved from the store at encode time, so
transcript JSON, session rows, ACP frames, and the reusable prompt prefix all
stay small.

```go
// internal/llm/schema/chat.go
type ChatMessage struct {
    // ...existing fields unchanged...
    Images []ImageRef // appended after Content; never interleaved
}

type ChatRequest struct {
    // ...existing fields unchanged...
    ImageSource ImageSource
}

// ImageSource resolves a ref's bytes. Nil means "this request cannot carry
// images"; an encoder or estimator meeting a non-empty Images with a nil
// source fails with ErrVisionUnsupported rather than sending text only.
type ImageSource interface {
    Image(ctx context.Context, hash string) (data []byte, mime string, err error)
}
```

`internal/imagepayload.Store` implements `ImageSource`, so the runner needs no
adapter.

Other carriers:

- `internal/tools/registry.ToolResult.Images []schema.ImageRef` — a tool
  result's images. `Content` stays the human-readable text (spill, audit and
  text-only consumers read only that).
- `internal/app/session.Message.Images []schema.ImageRef` — the transcript.
  Persisted to a new `images_json TEXT NOT NULL DEFAULT ''` column on the
  `messages` table (`internal/db/migrations.go`), written by `appendMessage`
  and parsed by `loadFromDB`. A parse failure is a logged error and empty
  images, never a failed load. The column's `''` default parses to no images,
  which is what every existing row holds.
- `internal/acpwire.PromptParams.Images []acpwire.PromptImage` — the prompt
  attachment. `acpwire` stays stdlib-only, so `PromptImage` is its own struct
  carrying exactly `Hash`, `MIME`, `Width`, `Height`, `Bytes` and `Label`;
  `internal/acp` converts at the seam, the same way it converts roles.
- `web/bridge/contract.PromptImage` — the same fields again for the fleet and
  web UI, regenerated into `web/ui/src/lib/contract.ts` with the existing
  `tsgen -update` test.

`internal/app/session` importing `schema` for `ImageRef` is deliberate and
does not contradict the `session.Role`-vs-`schema.Role` split: unlike `Role`,
`ImageRef` has no behavioural difference between the two layers to preserve,
and duplicating it would guarantee drift in a persisted format.

## 4. The store: `internal/imagepayload`

New leaf package. Imports `internal/llm/schema` and the standard library, plus
`golang.org/x/image/webp`.

```
<data dir>/images/<hash[:2]>/<hash>          # stored bytes, 0600
<data dir>/images/<hash[:2]>/<hash>.json     # sidecar: ref fields + last_used
```

`<data dir>` is `config.DataDir(home)` (`internal/app/config/locations.go`),
so `MARSHAL_DATA_DIR` and `XDG_DATA_HOME` relocate it with everything else.
Directories are 0700. Writes are tmp-file + rename, so a crash never leaves a
truncated entry that a hash would point at.

```go
type Config struct {
    MaxInputBytes  int64 // default 20 MiB, source file
    MaxStoredBytes int    // default 4 MiB, per stored image
    MaxEdge        int    // default 1568 px on the long edge
    JPEGQuality    int    // default 85
    MaxPerMessage  int    // default 10
    MaxRequestBytes int64 // default 12 MiB of base64 per request
    MaxStoreBytes  int64  // default 512 MiB on disk
    RetainDays     int    // default 14
}

type Options struct { // per-call overrides
    MaxEdge int
    Format  string // "" (default) | "png" | "jpeg"
    Quality int
    Label   string
}

func Open(root string, cfg Config) (*Store, error)
func (s *Store) Put(ctx context.Context, r io.Reader, o Options) (schema.ImageRef, error)
func (s *Store) Image(ctx context.Context, hash string) ([]byte, string, error)
func (s *Store) Stat(ctx context.Context, hash string) (schema.ImageRef, bool, error)
func (s *Store) Sweep(ctx context.Context, referenced func() (map[string]struct{}, error)) (Sweep, error)

type Sweep struct {
    Removed    int
    FreedBytes int64
}
```

`Image` touches the sidecar's `last_used`, which is what eviction orders by.

`MaxPerMessage` and `MaxRequestBytes` live in this `Config` for one source of
truth but are **not** enforced by the store: `MaxPerMessage` is enforced where
a message is built (§5.3), and `MaxRequestBytes` where a request is assembled,
with shedding delegated to prune (§8). The store enforces only the byte and
retention limits on its own files.

### Normalization

Unconditional, deterministic, and the reason a repeated view dedupes instead of
growing the store:

1. **Sniff** the real type from magic bytes; reject anything that is not PNG,
   JPEG, GIF (first frame) or WebP with `ErrUnsupportedFormat` naming what was
   found. The declared MIME is never trusted.
2. **Decode.** A decode failure is `ErrCorruptImage`.
3. **Downscale** to `MaxEdge` on the long edge, preserving aspect and never
   upscaling. Resampling is `x/image/draw`'s Catmull-Rom.
4. **Re-encode.** PNG when the source has alpha (alpha cannot survive JPEG) or
   when the PNG result fits `MaxStoredBytes`. Otherwise JPEG, walking the
   quality ladder 85 → 75 → 60. If the highest quality that fits still
   exceeds `MaxStoredBytes`, halve the edge and repeat, at most twice, then
   fail with `ErrTooLarge`.
   *Screenshots are text.* The PNG-first rule is what keeps UI text legible;
   an alpha-free screenshot that fits 4 MiB stays PNG.
5. **Hash** the stored bytes (sha256, hex). The hash is the identity: same
   input + same options ⇒ same hash ⇒ `Put` is a no-op that just refreshes
   `last_used`.

Per-call `Format`/`Quality` still hash the *result*, so an explicit
`format: "jpeg"` view of a file already stored as PNG produces a second entry.
That is intended: different bytes are different images.

### Token estimate

```go
func EstimateTokens(width, height int) int { return 85 + (width*height+749)/750 }
```

Consumed by `internal/agent/compact.go`'s wire estimator, so overflow is
detected before encoding rather than by the provider returning a 400.

## 5. Entry paths

### 5.1 TUI paste (and `/image`)

`ctrl+v` reads the clipboard **on demand** — nothing polls, nothing watches:

- darwin: `pngpaste -`, falling back to an `osascript` run that writes the
  clipboard's PNG to a temp file.
- linux: `wl-paste --type image/png` when `WAYLAND_DISPLAY` is set, else
  `xclip -selection clipboard -t image/png -o`.

Implemented in `internal/app/clipboard` next to the existing text reader, with
the same injected `Run` seam the boundary test already allowlists, so it is
tested without touching a real clipboard. Failure modes are notices, not
silent no-ops: "clipboard has no image", "install pngpaste" /
"install wl-clipboard or xclip".

`/image <path>` attaches a file path through the same `images.put` RPC. It is
the only path that works headless (SSH, WSL without a clipboard bridge), it is
~30 lines, and it is the manual testing path for everything below.

Both paths insert a chip in the composer (dimensions, size, thumbnail-less
label). A chip whose dimensions exceed `MaxEdge` says "will downscale". A chip
pasted while the route is explicitly or provably text-only renders marked
("model can't see images") but is not refused — routing can change before send,
and the send is the authoritative point (§7).

On submit, the TUI sends `PromptParams{Text, Images: [...refs]}`; bytes never
cross on prompt, only once, on `images.put`.

### 5.2 ACP

```go
// internal/acpwire — new method
const MethodImagesPut = "session/images_put"

type ImagesPutParams struct {
    Data  string `json:"data"`            // base64
    Label string `json:"label,omitempty"` // origin, for the sidecar
}
// → ImagesPutResult{ Image PromptImage }
```

`PromptImage` carries the ref fields. `PromptParams.Images` is additive: an
older client sends none and behaves exactly as today. The session resolves
each ref through the store before starting the turn, so a ref naming a missing
hash fails the prompt with a clear error instead of failing mid-stream.

### 5.3 Native tool results

`internal/agent/prompts.go`'s tool-result message builder copies
`ToolResult.Images` onto the message and appends one line per image to the
text:

```
[image: 1440×900 png 214 KB 3f9a1c2e]
```

That line is what prune stubs, what the transcript renders, and what a
text-only or non-vision consumer still reads sensibly. The builder caps a
message at `MaxPerMessage` images; it does not shed — the request-level byte
budget and its shedding are prune's job (§8).

`file.read` on a supported image type does not return base64. It returns a
notice pointing at `image.view` — the tool that can actually show it.

### 5.4 Subagent results

`agent.run`'s result carries up to `MaxPerMessage` refs taken from the child's
last tool result that carried images, labelled `<agent>: <label>`. Nothing
else is hoisted: the child's transcript stays the child's.

### 5.5 MCP tool results

The MCP result → `registry.ToolResult` conversion maps `type:"image"` content
blocks (base64 + mimeType) through `Store.Put` with default options and the
server's name in the label, bounded to four per result. A block that fails to
store is dropped with a notice line; the rest of the result survives.

### 5.6 Web and fleet uploads

`POST /api/projects/images` on the bridge (bearer-token protected like the rest
of `/api/`), body `{data, label}` → relayed to the project child's
`session/images_put` over its viewer socket → `{image: PromptImage}`. The web
UI and the fleet screen then send `PromptImage` refs on the prompt.

`web/` remains standard library only: the route lives on the agent side of
ACP, `contract.PromptImage` is the shared shape, and `contract.ts` is
regenerated. No image bytes go into a contract type beyond this request body.

## 6. Encoders

One shared helper, `schema.ImageRef` → dialect block, called from each
provider's request builder. Placement differs because the wire formats differ;
nothing else does.

| Provider | Placement |
|---|---|
| Anthropic | `tool_result.content` gains `{"type":"image","source":{"type":"base64",…}}` blocks after the text block. User messages become text + image blocks. |
| OpenAI-compatible | `role:"tool"` carries text only, so the images ride a synthetic `role:"user"` message immediately after the tool result. User messages become a parts array with `image_url` `data:` URLs. |
| Ollama-native | `message.images` = base64 strings, allowed on any role, so images attach to the tool message itself. |
| Codex / Responses | `input_image` with a `data:` URL where accepted; an endpoint that rejects it is handled by the retry in §7.3 |

Two invariants:

- Encoders never rewrite a message ahead of the transcript. Images are encoded
  where they already are, so `TestPromptPrefixStableAcrossSteps`
  (`internal/agent`) holds with a new image-bearing case.
- Every encoder asks `ImageSource` for bytes and fails with
  `ErrVisionUnsupported` when the source is nil and `Images` is non-empty. No
  encoder silently sends text only.

## 7. Capability and refusal

### 7.1 Resolution

`routing.Route.Vision` answers one question: **may this route be sent images.**
It is not a claim about what the model understands.

- `routing.ModelPreset.Vision string` — `""` (unset, equivalent to `"auto"`),
  `"auto"`, `"on"`, `"off"`; TOML key `vision`. Presets are user-global, like
  every other preset field.
- `provider.ModelCapabilities.Vision *bool` — reported by
  `CapabilityProber` (`internal/llm/provider/capabilities.go`). Nil means not
  reported.
- Learned rejections (§7.3), consulted before any guesswork.

Resolved in the route resolver, first match wins:

1. preset `"on"` → vision. The user's word is final, and a rejection then
   surfaces as itself with no silent retry (below).
2. preset `"off"` → no vision.
3. a learned rejection for this provider+model → no vision.
4. probe `false` → no vision; probe `true` → vision.
5. a name heuristic for families known to be text-only → no vision.
6. **otherwise vision** — an unrecognised model is attempted.

The default is attempt, not refuse. A wrong "on" is a provider error on a turn
the user is watching, and §7.3 turns the loud case into a working turn; a wrong
"off" blocks the feature at first use, which is the moment a detour costs the
most. What remains is an endpoint that *accepts* an image part and discards it,
on a model no rule knows. That is accepted: the person pasting their own
screenshot sees a wrong description immediately and can say so, and the model's
description is checkable in a way a base64 blob never was.

### 7.2 Registry gating

`internal/app` wraps the runner's registry with
`agent.DenylistView(reg, []string{"image.view"})`
(`internal/agent/registry_scope.go`) when the resolved route has
`Vision == false`. The model is never offered a tool it cannot use, and the
tool costs no prompt tokens on text-only routes.

### 7.3 Learned rejection: retry without images

An endpoint that cannot take images usually says so loudly, and that is cheap to
learn from.

**Trigger.** A request that carried images fails with a
`*provider.ProviderError` (`internal/llm/provider/errors.go`) whose
`StatusCode` is 400, 415 or 422 *and* whose `Body` matches a narrow
vision-specific matcher (case-insensitive: `image`, `vision`, `multimodal`,
`image_url`, `image content`, `does not support images`). Deliberately narrow: a
400 caused by request size or an unknown parameter must never be read as "this
model is blind".

**Action, at most once per turn**, and only when `Vision` came from rules 3–6 —
never when the user forced `vision = "on"`. The same request is re-sent with
every `Message.Images` empty and `[images omitted: model rejected image input]`
appended to the last message that carried them, and the run records a notice.

**Learning.** If the retry *succeeds*, persist the rejection and drop the
images-notice path from the successful turn's report. If the retry also fails,
persist nothing and surface the original error — the request had a different
problem, and we learned nothing about images.

**Where the fact lives.** A new
`internal/llm/provider/modelcache/capability.go`:
`CapabilityOverrides map[string]bool` keyed `provider + "/" + model`, in
`model_capabilities.json` beside the model cache (0600, tmp+rename). It is
deliberately *not* stored in `modelcache.Entry`: that entry is keyed by
`HashProvider` and expires by TTL (`Cache.Lookup`), so the next model-list
refresh would erase a fact we observed. Overrides also feed
`provider.ModelCapabilities.Vision` for every consumer of the prober's answer,
and expire after 30 days or when the provider's config hash changes.

### 7.4 Send-anyway

A refusal is a request for one keystroke, not a dead end. When a prompt is
refused (§7.5) the composer keeps the text and chips and offers "send anyway";
the retry sets a session-scoped, in-memory `forceVision[provider/model]` that
wins rule 3 for the rest of the session. It is never written to config — the
user is answering a question about *this* image on *this* model, not editing
their setup mid-turn.

### 7.5 Refusal matrix

| Boundary | Vision off | Rationale |
|---|---|---|
| Prompt carries images | Refuse before sending. `ErrVisionUnsupported` carrying model, preset and *why* vision is off. TUI: the turn fails, the composer keeps the text and chips, and "send anyway" (§7.4) resends with the override. ACP: the string code `acpwire.CodeVisionUnsupported` (`"vision_unsupported"`, declared in `internal/acpwire/errors.go` like every other code there) with the message naming the cause — `model <M> has vision off; set [models.presets.<M>] vision = "on" or switch model`, or for a learned rejection, `model <M> rejected image input on 2026-11-02`. | The user can act on it, in one keystroke or one line of config. Silently dropping reproduces the base64 blindness this design exists to end. |
| `ctrl+v` while the route is text-only | Allowed; chip marked. | Routing can change before send. |
| `image.view` | Not registered; the handler still returns an error result if reached through a stale registry. | Never offer it; never panic on it. |
| Tool-result images (MCP, subagent) | Dropped, text kept, `[image omitted: model has vision off]`. | The work already happened; aborting discards it. |
| Loud rejection on an image turn | Retry once without images (§7.3); if that succeeds, persist the rejection and continue. | A dead turn is a worse outcome than a text-only turn, and the retry is what makes "attempt by default" safe. |
| Replayed image whose bytes were GC'd | Dropped, text kept, `[image omitted: not in store]`. | A swept store must never brick a resume. |
| Store failure | Prompt: fails before the turn starts. Tool: error result. | Typed errors naming the limit that was hit. |

All refusals are typed errors with `errors.Is` support, not formatted strings
in encoders.

## 8. Retention, budget, cache stability

- **Retention.** Images live on their message and are replayed like text.
  Nothing except the existing pruning/rollover machinery removes them.
- **Accounting.** The wire estimator adds `EstimateTokens(w,h)` per image and
  checks `MaxRequestBytes` against the base64 size before encoding, so
  overflow detection stays honest and a provider 400 does not become the
  detection mechanism.
- **Shedding is part of prune, under the existing overflow gate.**
  `pruneStaleToolOutputs` (`internal/agent/prune.go`) drops a stubbed result's
  `Images` along with its text. If the request is still over, images are
  dropped oldest-first — tool results before user messages — leaving
  `[image omitted to fit context]`. This is the one sanctioned prefix rewrite,
  and it fires only where prune already fires.
- **The one other rewrite is the §7.3 retry**, which happens only after a
  request has already failed and never on a path that succeeded. It cannot
  cost a cache hit that was going to be used: the request it replaces returned
  an error.
- **Never on the happy path.** A test asserts zero shedding below the cap, and
  the image-bearing `TestPromptPrefixStableAcrossSteps` case pins byte
  stability across steps.
- **Rollover and compaction** drop refs together with the region they
  summarise. No special case.
- **Store GC** is independent of the transcript: on `Open` and after each
  `Put`, if the store exceeds `MaxStoreBytes`, entries are evicted by sidecar
  `last_used`, oldest first, skipping any hash still referenced by a session
  updated within `RetainDays`. Referenced hashes come from one query:
  `SELECT images_json FROM messages JOIN agent_sessions …` over sessions newer
  than the cutoff. A swept hash surfaces as the "not in store" row above.

## 9. The tool: `image.view`

Registered in `native.RegisterAll` (`internal/tools/native/native.go`) only
when `Options.Images` is set, following the existing `symbols`/`transcript_read`
pattern of "only when its dependency is wired". It is registered with
`registry.RiskReadOnly`, so reading an image never prompts for approval.

```json
{
  "name": "image.view",
  "parameters": {
    "type": "object",
    "properties": {
      "path":    {"type": "string", "description": "Workspace-relative or absolute path to a PNG, JPEG, GIF or WebP file."},
      "ref":     {"type": "string", "description": "A hash from an earlier image result, to view it again without re-reading the file."},
      "max_edge": {"type": "integer", "description": "Longest edge in pixels (default: images.max_edge)."},
      "format":  {"type": "string", "enum": ["png", "jpeg"], "description": "Re-encode as this format instead of the default choice."},
      "quality": {"type": "integer", "description": "JPEG quality 1-100 (default: images.jpeg_quality)."},
      "label":   {"type": "string", "description": "What this image is, for the transcript line."}
    },
    "oneOf": [{"required": ["path"]}, {"required": ["ref"]}]
  }
}
```

Path handling follows `file.read` exactly: the same policy engine, the same
read-only rules, the same refusal outside the workspace. `path` and `ref`
together is a validation error.

Result text:

```
image.view: /tmp/ui.png → 1440×900 png (214 KB) hash 3f9a1c2e
[downscaled from 3000×1875]
```

plus `ToolResult.Images = [ref]`. A notice is set for a downscale or a
format change, reusing the existing `ToolNotice` shape so the model can tell
it did not get the original pixels.

`image.capture` and `image.render` are separate tools later. They share
`Store.Put` and the same `ToolResult.Images` channel, which is the whole point
of putting normalization in a package rather than in the tool.

## 10. Config

```toml
[images]
max_input_bytes   = 20971520   # 20 MiB, source file
max_stored_bytes  = 4194304    # 4 MiB, per stored image
max_edge          = 1568
jpeg_quality      = 85
max_per_message   = 10
max_request_bytes = 12582912   # 12 MiB of base64 per request
max_store_bytes   = 536870912  # 512 MiB on disk
retain_days       = 14
```

Defaults in `config.Default()`, merging as usual (project config may set
`[images]`; it is not a provider or preset table). Zero or negative values are
replaced by the default with a diagnostic rather than disabling a limit.

## 11. Testing

**`internal/imagepayload`** — determinism (same input + options ⇒ same hash);
downscale on the long edge, never upscaling; PNG kept for alpha and for
screenshots that fit; JPEG ladder when they do not; every cap produces its
typed error; files 0600 under 0700 dirs; a crashed write leaves no entry;
`Sweep` keeps referenced hashes and drops unreferenced older ones;
`EstimateTokens` values pinned.

**Encoders** — one golden per dialect: Anthropic tool-result blocks, the
OpenAI synthetic user message, Ollama `message.images`, Codex `input_image`.
Nil `ImageSource` with non-empty `Images` fails with `ErrVisionUnsupported`.
`TestPromptPrefixStableAcrossSteps` gains a turn with images in the replayed
history and asserts an unchanged byte prefix up to `CacheAnchor` across steps.

**Retention** — prune stubs a superseded result and takes its images, keeping
the newest; no shedding below the cap; a GC'd hash degrades to the notice and
the turn still succeeds.

**Capability** — the resolution table over preset on/off/auto × probe
nil/true/false × learned-override absent/false; an unrecognised model resolves
to attempt; `DenylistView` removes `image.view` for a text-only route; the
refusal text and ACP code are pinned, including the two message variants
(explicit off vs learned rejection).

**Learned rejection** — a 400 whose body mentions images triggers exactly one
retry without images, and persistence only when that retry succeeds; a 400 for
request size or an unknown parameter neither retries nor persists; a 415 and a
422 are covered; an explicit `vision = "on"` surfaces the error without a
retry; `CapabilityOverrides` survives a model-cache refresh and expiry; the
send-anyway override resends the same prompt successfully and does not touch
config.

**Paths** — `ctrl+v` through the injected clipboard `Run` (no real clipboard,
and the "no image" and "tool missing" notices); `/image` and paste round-trip
through `acptest` as `session/images_put` → `PromptParams.Images` → replayed
`session.Message.Images`; `POST /api/projects/images` against an `httptest`
bridge with the bearer token enforced; `TestTypeScriptContractIsCurrent`
after the `contract.ts` regen; an MCP fake server emitting an image block; a
subagent whose last tool result carried images, and the hoist bound.

**Boundaries** — `TestTUIStaysOnTheClientSeam` (the clipboard package is
already allowlisted) and `TestWebIsStdlibOnly` must stay green without an
allowlist change.

## 12. Decisions and trade-offs

1. **Side-channel `Images` list, not interleaved content parts.** A content-parts
   rewrite would touch ~146 `.Content` reads across 34 non-test files and 114
   message literals in order to add a feature to a text-shaped agent, and the
   failure mode is silent budget mis-accounting. "Text, then N images" covers
   every path in scope; a genuine need for interleaving later is a mechanical
   transform of one struct.
2. **Refs in the transcript, bytes in the store.** Persisting base64 in SQLite
   would bloat every session read, every ACP frame and the prompt prefix;
   hashing gives dedupe and makes the cache key stable for free.
3. **Normalize by default.** A 20 MB screenshot sent verbatim is rejected or
   silently truncated by providers, and every byte is cached-prefix cost.
   The per-call override exists because "show me the original pixels" is a
   real request when the agent is checking a rendering artefact.
4. **PNG-first re-encode** refines the approved "alpha ⇒ PNG, else JPEG": UI
   screenshots without alpha are exactly the case this feature exists for, and
   JPEG ringing on 11px text is the wrong default. JPEG is the fallback, not
   the baseline.
5. **Vision attempts by default; refusal is for what we know, not what we
   guess.** A wrong "on" used to mean an opaque provider error mid-turn, but a
   loud rejection is now recovered automatically (§7.3) and learned, and a
   wrong "off" is a first-use refusal with a one-keystroke override (§7.4).
   Since attempt is recoverable and refusal is a wall, attempt is the better
   default. The residual risk — an endpoint that silently discards image parts
   — is accepted because the model's description is checkable by the user.
6. **Shedding only under the overflow gate.** Dropping images on the happy
   path would rewrite the prompt prefix and break prompt caching for a feature
   whose whole value is repeated viewing.
7. **`/image <path>` ships in v1** despite the clipboard-only decision: it is
   the only attachment path on a machine without clipboard tooling, and it is
   the manual test harness for every other path.
8. **`golang.org/x/image` is the one new dependency**, for WebP decode and
   Catmull-Rom resampling. Rejecting WebP would refuse a common screenshot
   format; writing a resampler is not worth owning.

## 13. Assumptions

- Provider dialects accept inline base64 (`data:` URLs) for every model Marshal
  routes to. Where an endpoint rejects them loudly, the retry in §7.3 recovers
  the turn and the rejection is remembered; where it rejects them silently, the
  user sees a wrong description and sets `vision = "off"`.
- 1568 px on the long edge and the 85 + ⌈w·h/750⌉ estimate are close enough for
  every provider in scope. The estimate is a budget figure, not a bill.
- Session rows are the only persisted transcript (`internal/app/session`), so
  one column covers replay.
- The fleet screen and web studio show an image's *label* in v1; a rendered
  thumbnail in the browser is a UI follow-up on the same contract type.

## 14. Open questions

None.
