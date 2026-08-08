# Knowledge Agent

`internal/knowledge/` is Marshal's **session finalization** pass. When a session
ends, it reviews the transcript, tool activity, and touched files, asks an LLM
to distill them into durable project knowledge, and persists the result to
SQLite. It is the *write* side of the memory system; the read-back side lives
in `internal/agent/`, `internal/contextpack/`, and the TUI memory browser.

The package is deliberately small and self-contained: a single entry point, a
prompt builder, and a response parser.

## When it runs

`knowledge.EndSession` is invoked exactly once per session, from
`internal/app/app.go` during the app's three-phase teardown:

1. **Phase 1** — the TUI exits and the runtime quiesces.
2. **Phase 2** — `knowledge.EndSession` runs, while the DB and logger are still
   open.
3. **Phase 3** — the runtime closes.

It is skipped when the user resumes a different session (the current transcript
is already persisted; knowledge runs when that session is eventually exited)
and when trust was granted inline (no agent work ever ran).

## Entry point

```go
func EndSession(ctx context.Context, in EndSessionInput)
```

`EndSessionInput` carries everything the pass needs:

| Field | Purpose |
|-------|---------|
| `DB` | `*db.DB` handle for persistence. |
| `ProjectID` | Project the session belongs to. |
| `SessionID` | The session being finalized. |
| `State` | `*session.State` — source of messages and the audit log. |
| `RouteResolver` | Resolves the `"knowledge"` task class to a route + provider. |
| `WorkingDir` | Base directory for reading touched files. |
| `Now` | Clock injection (defaults to `time.Now`). |
| `Logger` | `*slog.Logger` for best-effort error logging. |

`EndSession` returns nothing. It is **best-effort**: every failure is logged
and swallowed, and a failed pass never affects process exit.

## Flow

```
EndSession
  ├─ no user messages? ────────────────► return (no-op)
  ├─ RouteResolver.Resolve("knowledge") ─► error? log + return
  ├─ read touched files from audit log
  ├─ BuildExtractionPrompt(...)          → one user-turn ChatMessage
  ├─ provider.ChatText (non-streaming)  → error? log + return
  ├─ ParseExtraction(raw)                → error? log + return (no retry)
  └─ persist, in order:
       ├─ db.EndSession(sessionID, now, sessionSummary)
       ├─ db.SaveMemory(...) per memory        (always tentative)
       └─ db.UpdateFileSummary(...) per file   (only if session-touched)
```

### No-op guard

If the session has no user messages, `EndSession` returns immediately — no LLM
call, no writes.

### Touched-files guard

File summaries are only persisted for paths that appear in the session's audit
log (`FilesChanged`). This prevents the model from summarizing files it merely
read rather than modified.

## Files

| File | Role |
|------|------|
| `knowledge.go` | Orchestration — `EndSession`, `EndSessionInput`, `RouteResolver`. |
| `prompts.go` | Prompt construction — `BuildExtractionPrompt` and the prompt template. |
| `protocol.go` | Response parsing — `Extraction`, `ParseExtraction`, JSON wire types. |
| `*_test.go` | Tests with fake provider / route-resolver doubles. |

## The extraction protocol

The model is instructed to return **exactly one JSON object**:

```json
{
  "session_summary": "short paragraph",
  "memories": [
    {"kind": "fact", "content": "..."},
    {"kind": "architecture", "content": "..."},
    {"kind": "decision", "content": "..."}
  ],
  "file_summaries": {"path/to/file.go": "one-line summary"}
}
```

`ParseExtraction` uses `jsonextract.Extract` (a balanced-brace scanner) so it
tolerates markdown fences and surrounding prose. It trims whitespace on all
fields, skips memories with blank content, and defaults a missing/blank `kind`
to `"fact"`. If no JSON object is found it returns `ErrNoExtractionFound`.

## Persistence

The pass writes to three DB surfaces:

| Surface | Call | Notes |
|---------|------|-------|
| Session | `db.EndSession` | Sets `ended_at` and the session summary. |
| Memories | `db.SaveMemory` | One row per memory, always `tentative` confidence. |
| Files | `db.UpdateFileSummary` | Only for session-touched paths. |

## Configuration

The knowledge model is selected through the routing profile's `RoleKnowledge`
entry (task class `"knowledge"`). If the active profile has no such entry, the
router falls back to the implementer role. The package has no configuration of
its own.

## Design notes

- **Import-cycle avoidance via structural interfaces.** `RouteResolver` and
  `MemoryNote` are declared locally in `internal/knowledge` (and `MemoryNote`
  again in `internal/contextpack`) rather than shared, so `internal/agent` and
  `internal/knowledge` never import each other. Go's structural typing lets a
  single concrete `*routedProviderResolver` satisfy both `knowledge.RouteResolver`
  and `agent.RouteResolver`.
- **One-shot, non-streaming LLM call.** Uses the shared `provider.ChatText`
  helper, which drains a `Chat` stream to a single string.
- **Best-effort lifecycle.** A failed knowledge pass never blocks shutdown.
- **Prompt template as a package-level const** with `fmt.Sprintf`-style
  substitution of three rendered sections (transcript, tool activity, touched
  files).

## Read-back (where memories are consumed)

The knowledge pass only writes. Memories are later read back into context by:

- `internal/agent/runner.go` — the `MemoryProvider` interface
  (`Memories(projectID)`).
- `internal/agent/route.go` — `mergeMemories` fetches memories and calls
  `contextpack.MergeMemories` during turn setup.
- `internal/contextpack/builder.go` — `MergeMemories` replaces the memory
  section of the context pack within the token budget.
- `internal/app/app.go` — `dbMemoryProvider` adapts `db.GetMemories` (filtering
  out stale memories) to `contextpack.MemoryNote`.
- `internal/app/tui/memory/` — the TUI memory browser for curation
  (stale/confirmed toggling).