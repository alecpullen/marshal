# Streaming thinking blocks + live output — design

**Date:** 2026-07-04
**Status:** Approved, ready for implementation planning

## Problem

While the model generates a response, the TUI shows a blank/idle screen until the
full turn completes. Some models (notably local reasoning models served over an
OpenAI-compatible endpoint — DeepSeek-R1 distills via vLLM/Ollama, and similar)
emit a separate `reasoning_content` delta channel alongside the normal `content`
channel. Today `internal/llm/schema.ChatEvent` only carries a flat text delta, so
this reasoning content is invisible even when the model produces it, and
`agent.Runner.chatOnce` fully buffers all deltas before the TUI ever sees
anything, so nothing streams live regardless.

## Key constraint

The model's `content` channel in this agent is **not** plain chat text — it's a
structured action envelope (plan / tool-call / answer JSON) that `ParseAction`
(`internal/agent/action.go`) only parses into something displayable after the
full stream completes (`internal/agent/runner.go:121`). Live-streaming that
channel token-by-token would show raw, partially-formed JSON, which is not
useful. `reasoning_content`, by contrast, is free-form natural language the
model emits outside that envelope, so it streams cleanly.

**Scope decision:** only the reasoning/thinking channel streams live. The final
answer keeps today's behavior — it appears atomically once the full response
parses — but the screen is no longer blank while waiting, because the thinking
box fills it.

## Design

### 1. Provider/schema layer

`internal/llm/schema/event.go` gains a `DeltaKind` on `ChatEvent`:

```go
type DeltaKind int
const (
    DeltaAnswer DeltaKind = iota // zero value = today's behavior, unchanged
    DeltaThinking
)

type ChatEvent struct {
    Type         ChatEventType
    Kind         DeltaKind // only meaningful when Type == ChatEventDelta
    Delta        string
    FinishReason string
    Err          error
}
```

`internal/llm/provider/openai_compatible.go`'s `streamChatEvents` reads a new
`reasoning_content` field off the wire delta struct. When present and non-empty,
it emits `ChatEvent{Type: ChatEventDelta, Kind: DeltaThinking, Delta: ...}`
instead of/in addition to the answer delta for that chunk. Models/providers that
never populate `reasoning_content` are completely unaffected — `Kind` defaults
to `DeltaAnswer` everywhere else.

This keeps the event schema provider-agnostic: a future provider (e.g.
Anthropic extended thinking) maps its own wire format onto the same
`DeltaThinking` kind without touching the agent runtime or TUI.

### 2. Agent runtime — `internal/agent/runner.go`

`chatOnce` (line 247) currently accumulates only `ChatEventDelta` text into a
`strings.Builder` and returns it once the stream ends. It changes to route by
`Kind`:

```go
func (r *Runner) chatOnce(ctx context.Context, p provider.Provider, model string, messages []schema.ChatMessage) (string, error) {
    events, err := p.Chat(ctx, schema.ChatRequest{Model: model, Messages: messages, Stream: true})
    if err != nil {
        return "", err
    }

    r.State.BeginStreaming()
    defer r.State.EndStreaming()

    var sb strings.Builder
    for event := range events {
        switch event.Type {
        case schema.ChatEventDelta:
            if event.Kind == schema.DeltaThinking {
                r.State.AppendThinking(event.Delta)
            } else {
                sb.WriteString(event.Delta)
            }
        case schema.ChatEventError:
            return "", event.Err
        case schema.ChatEventDone:
            return sb.String(), nil
        }
    }
    return sb.String(), nil
}
```

Answer deltas still only accumulate in the local builder — `ParseAction` and the
rest of the ReAct loop are untouched. `BeginStreaming`/`EndStreaming` (via
`defer`, so they always run even on stream error) bracket the in-progress
message's lifecycle and record start time for the "thought for Ns" duration.

Because `chatOnce` can run multiple times per turn (a planning call, then one
call per ReAct iteration), each call gets its own begin/end cycle and its own
collapsed summary line in the transcript — one "⚙ thought for Ns" per model
call, which honestly reflects what happened rather than merging unrelated
reasoning spans together.

### 3. Session state — `internal/app/session/session.go`

New in-progress message, guarded by the existing `mu`:

```go
type InProgressMessage struct {
    Reasoning strings.Builder
    StartedAt time.Time
    Active    bool
}

func (s *State) BeginStreaming()               // resets and marks s.inProgress active
func (s *State) AppendThinking(delta string)    // appends under lock
func (s *State) EndStreaming()                  // marks inactive
func (s *State) InProgress() InProgressMessage  // locked copy, like Messages()
```

`Message` gains:

```go
Reasoning     string
ThinkDuration time.Duration
```

`AddMessage` (line 112) reads whatever accumulated in `s.inProgress` since the
last `BeginStreaming`, attaches it (`Reasoning`, `ThinkDuration = time.Since(inProgress.StartedAt)`)
to the new `Message`, and clears `s.inProgress`. No call site of `AddMessage`
needs to change signature — it picks up ambient state.

A single `m.thinkingExpanded bool` toggle lives on the TUI model (not session
state) — see section 5.

### 4. Persistence — `internal/db/`

Additive migration on the `messages` table:

```sql
ALTER TABLE messages ADD COLUMN reasoning TEXT;
ALTER TABLE messages ADD COLUMN think_duration_ms INTEGER;
```

`db.Message` gains matching fields. `SaveMessage` (called from
`session.AddMessage`) passes them through; whatever reconstructs `session.Message`
on session resume reads them back, so expand/collapse continues to work after
reopening a past session. Existing rows get `NULL` in both columns, which the
TUI treats as "no reasoning captured" (no collapsed line shown for that
message). Persistence remains best-effort and logged, never blocking the
in-memory transcript — same pattern as the existing content save.

### 5. TUI rendering — `internal/app/tui/model.go`

**While streaming** (`InProgress().Active`): a boxed panel below the transcript,
reusing the approval-banner template (`lipgloss.RoundedBorder()`,
`truncateRunes` per line, dynamic footer — see `e977fbe`) but styled with
`dimColor` instead of `accentColor`, italicized. Title `"thinking"`. Content is
`InProgress().Reasoning`, tail-truncated to fit available height (show the most
recent lines — the newest text is what's relevant while still generating).

**On finalize**: the box collapses into one line rendered just above that
message in the transcript: `⚙ thought for 4s` (computed from
`Message.ThinkDuration`).

**Global toggle**: `Ctrl+H` (unused — confirmed against existing bindings
Ctrl+P/X/T/R, Tab/Shift+Tab, Enter, Esc) flips `m.thinkingExpanded`. When true,
`refreshViewport` (line 453) renders every message's full `Reasoning` inline
(boxed, dim) instead of the collapsed line; when false, every message shows only
the collapsed line. This is a global all-or-nothing toggle, not per-message —
see "Deferred" below.

**Dirty-check extension** (line 455-458): today gates re-render on
`lastMessageCount` being unchanged plus `!m.busy`. Add: while `m.busy &&
InProgress().Active`, also compare `len(InProgress().Reasoning)` against a
stored `m.lastStreamLen`, forcing re-render on growth. This is the actual fix
for "blank screen," since message count doesn't change mid-stream — only
in-progress reasoning length does.

### 6. Error handling

- **Model never emits `reasoning_content`**: `Kind` stays `DeltaAnswer`
  throughout, `Reasoning` stays empty, no thinking box ever renders — fully
  backward compatible.
- **Stream error mid-thinking**: `chatOnce` returns the error; `EndStreaming`
  still runs via `defer`, so the box always closes (collapsing with whatever
  partial reasoning was captured) instead of getting stuck open.
- **DB write failure for reasoning**: best-effort/log-and-continue, same as
  existing content persistence.

### 7. Testing

- Provider: table-driven test that `streamChatEvents` maps `reasoning_content`
  chunks to `Kind: DeltaThinking` and plain `content` chunks to the
  `DeltaAnswer` zero value.
- Runner: `chatOnce` routes thinking deltas to `State.AppendThinking` and answer
  deltas to the returned string, using a fake event channel.
- Session: `BeginStreaming`/`AppendThinking`/`EndStreaming`/`AddMessage`
  lifecycle test — reasoning captured between Begin/End attaches to the next
  `AddMessage` and clears after. Concurrency test alongside the existing
  render-during-mutation race test.
- DB: migration test (new nullable columns, old rows unaffected) + round-trip
  save/load for `Reasoning`/`ThinkDurationMs`.
- TUI: `refreshViewport` collapsed-line format and `Ctrl+H` toggle; dirty-check
  test asserting re-render triggers on reasoning-length growth alone.

## Deferred (explicitly out of scope for this spec)

- **Per-message expand/collapse.** There's no per-message selection/cursor
  concept in the transcript today (only global hotkeys and free scroll). Real
  per-message toggle requires building that as its own TUI primitive
  (up/down to focus a message, enter to toggle it) — a standalone piece of
  infrastructure independent of this feature's core need. This spec ships the
  global `Ctrl+H` toggle-all instead; a future design can introduce message
  navigation and route the toggle through it, deprecating the global key.
- **Live-streaming the final answer text.** Blocked on the action-JSON
  contract described above. Would require either a new "answer" action type
  whose content streams as plain trailing text after the JSON envelope closes,
  or incremental JSON parsing to detect the content field early — a change to
  the agent's prompt format and `ParseAction`, not just this feature's layers.
- **Non-OpenAI-compatible providers.** No second provider exists yet; the
  `DeltaKind` schema is designed to accommodate one later without rework, but
  no other provider is being built as part of this spec.
