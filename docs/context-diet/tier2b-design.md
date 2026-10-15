# Context diet: Tier 2b (LSP integration)

Status: design approved 2026-10-14; spec awaiting review. Builds on Tier 1
(`design.md`) and Tier 2a (`tier2a-design.md`); lands after 2a.

## Intent

The user wants language servers kept and made to work. They run on the host,
are deterministic, and cost the model nothing.

Success means:
- The model gets correct definition, reference and hover answers by symbol
  name.
- After every edit, the model learns about the errors that edit caused in any
  file.
- Context stays lean: one `symbols` tool replaces five surfaces.
- Results degrade cleanly when a language has no server.

## Why it fails today

Measured over 52,549 tool calls since 2026-08-01: `definition` and `references`
returned **no useful results in 145 calls**, and `hover` was called once.

The structure is mostly there already. Post-edit diagnostics already try the
LSP first, and `lsp.Handle` already restarts the server when a session enters
a worktree. The failures come from six bugs:

1. **Wrong name lookup.** Names are resolved through Marshal's own symbol index
   (`internal/tools/native/lsp_tools.go` `resolveSymbol`) instead of the server.
   86 calls returned "not found".
2. **Wrong position.** Queries are sent at column 0 of the symbol's line, which
   is the `func` keyword. 26 calls returned "none".
3. **No startup wait.** Servers start eagerly for every installed language,
   and the tools never wait for readiness. 29 calls returned "no lsp".
4. **Clean files look like a missing server.**
   `lsp.DiagnosticsAdapter.Diagnostics` returns `("", false)` when a file has
   no errors, so every clean edit falls through to `go vet`. It also opens and
   closes the document on every call and polls for only 250 ms.
5. **Wrong file read.** The same adapter reads the file relative to Marshal's
   working directory, not the server's root.
6. **Wrong checkout checked.** The `go vet {package}` fallback
   (`internal/diagnostics/checkers.go`) runs with no working directory set, so
   in a worktree session it checks the project checkout's copy. That explains
   the postmortems' "stale type errors that contradicted go test".

## Decisions

- **Post-edit diagnostics report errors the edit caused anywhere,** not just in
  the edited file. Pre-existing breakage elsewhere is not repeated.
- **With no ready server, post-edit diagnostics use the command checker,**
  fixed: working directory set to the active worktree, errors only, capped.
  With no server and no command, report nothing.
- **Approach: live document sync.** Servers stay running per session, the file
  tools notify them of every write, and Marshal keeps a diagnostics cache.
  - Rejected: fixing the open/close-per-call model in place. It can't see
    errors in other files.
  - Rejected: shelling out to `gopls` CLIs. That's Go-only and reloads
    packages on every call.

## Components

**1. `lsp.Manager`** (existing, changed)
- Starts servers only for languages present in the repo: installed or
  configured servers intersected with the languages found by a bounded walk
  of the root (first 5,000 files, skipping `repo.IsDefaultIgnoredDir`
  directories, classified with `repo.DetectLanguage`).
  The walk runs at manager start and on every worktree restart. It doesn't use
  the index, which may still be empty on a fresh project.
- Adds `WaitReady(ctx, lang, timeout) bool`, backed by a per-language ready
  signal.
- Supervises each server. On exit (client end-of-stream) the language is
  marked not ready and restarted once, immediately. A second failure disables
  the language for the session, and the reason is kept for result notes.
- The worktree restart through `lsp.Handle` is unchanged.

**2. `lsp.Workspace`** (new): document sync and the diagnostics cache. One per
manager, so it resets on a worktree restart.
- `DidWrite(path)`: open the document if needed, otherwise send a full-text
  change with the next version, then save. The text is read from disk at
  `root/path`.
- A cache of `publishDiagnostics` per file, keeping errors only (severity 1).
- `Snapshot()` returns the error set.
- `Settle(ctx)` waits for the first diagnostics published after the write and
  then 300 ms of quiet. If nothing is published within a 1.5 s grace period
  (servers may skip unchanged diagnostics), it returns. It always returns by
  3 s. All three durations are injectable for tests.
- `Rescan()` stats the files under the root (skipping
  `repo.IsDefaultIgnoredDir` directories), limited to languages with a running
  server. For files changed since the last scan, it sends
  `workspace/didChangeWatchedFiles` and re-sends open documents. It runs after
  every `shell.run`.
- `NewErrors(before, after)` compares by (file, message) as a multiset, so a
  line shift in the edited file doesn't produce false "new" errors. Results are
  rendered with their current lines.

**3. `lsp.SymbolQuery`** (new, replaces `QueryAdapter` and the `LSPQuerier`
interface)
- **Name resolution:** the server's `workspace/symbol`, keeping exact name
  matches. `Type.Method` matches on the container name. An optional `path`
  filter narrows the matches.
  - One match: query at the identifier's position.
  - Several matches: return the candidate list with `path:line`.
  - None: fall back to the index, then report "no symbol named X" with up to 5
    near matches.
- **Direct addressing:** with `path` + `line` given, find the name's column on
  that line and query there.
- **Operations:**
  - `definition`: `path:line` plus up to 5 lines of the declaration.
  - `references`: `path:line` entries, capped at 50 with the total count.
  - `hover`: signature and documentation, capped at 1,500 characters.
  - `find`: fuzzy `workspace/symbol`, 20 lines of `path:line kind name`.
- **Fallback:** with no server, or a server not ready within 10 s, answer from
  Marshal's symbol index (`db.FindSymbols`) with a one-line note. The index can
  answer `find` and `definition`; it can't answer `references` or `hover`.

**4. `symbols` tool** (`internal/tools/native`)
- Arguments: `{name, op: find|definition|references|hover, path?, line?}`.
  `op` defaults to `definition`.
- It replaces `symbols.find`, `definition`, `references`, `hover` and
  `repo.search kind=`.

**5. `diagnostics.Checker`** (changed)
- **Ready server for the file's language:** the edit path takes `before`,
  writes, calls `DidWrite` and `Settle`, then reports the new errors.
  - Up to 10 lines of `path:line: message`, then `+N more across M files`.
  - When nothing is new: `diagnostics: no new errors`.
  - When the settle cap is hit, `(diagnostics still settling)` is appended.
- **Otherwise:** the command checker. Its runner takes a working directory,
  which is set to the active root. Output keeps the existing 15 s timeout and
  30-line cap.
- Edits never wait for server startup.

**Removed:**
- tools: `definition`, `references`, `hover`, `symbols.find`,
  `diagnostics.check`, and the `kind` parameter of `repo.search`
- `lsp.QueryAdapter`, `native.LSPQuerier`, and `lsp.DiagnosticsAdapter` with
  its open/close and 250 ms poll

## Data flow

**Edit** (`file.edit`, `file.write`, and the internal `file.write_patch`, all
through `applyFilePatches` / the `file.write` handler):
1. Check readiness without waiting. If ready: `before := Snapshot()`.
2. Write with the existing guards.
3. `DidWrite`, then `Settle`.
4. `NewErrors(before, after)`, rendered into the tool result.

**Shell:** after any foreground `shell.run` completes, and when a background
job exits, call `Workspace.Rescan()`.

**Subagents:** `agent.run` children get the parent's `Workspace` through
`native.Options`, so their edits keep the server in sync. Writes are already
serialized by the shared write lock, so before/after pairs never interleave.

## Failure handling

A language-server problem never fails a tool call.

| Situation | Behaviour |
|---|---|
| No server for the language | `symbols` answers from the index; diagnostics use the command checker or report nothing. |
| Server starting | `symbols` waits up to 10 s, then answers from the index with `(language server still starting; answered from index)`. An edit uses the command checker. |
| Server crash | Restarted once, immediately. After a second failure the language is disabled for the session, and results note `<server> unavailable: <reason>; using <fallback>`. |
| A request hangs | 10 s timeout per request, treated as "not ready" for that call. |
| Settle cap reached | Report the new errors seen so far, plus `(diagnostics still settling)`. |
| Diagnostic flood | Show 10, then `+N more across M files`. |
| File outside the server root (system access) | Skip the LSP and use the command checker. |
| cgo errors in the command checker | Verify that `go vet ./internal/repo` inside a worktree produces no tree-sitter errors once the working directory is fixed. If it still does, pass the build environment the shell uses. |

## Testing

- **Scripted fake server** built from the base-protocol `fakeServer` in
  `internal/lsp/client_test.go`. It answers `initialize`, `workspace/symbol`,
  `definition`, `references` and `hover`, records document and watched-file
  notifications, and lets a test push diagnostics. It drives these tests:
  - `Workspace`: comparison including line shifts, settle window and cap, the
    flood cap, re-sending documents changed on disk, and `Rescan` detecting a
    shell edit.
  - `SymbolQuery`: the identifier's column arrives in the position parameter,
    `Type.Method` matching, the ambiguous-candidates and near-matches messages,
    `path`+`line` addressing, and the index fallback note.
  - `Manager`: `WaitReady`, the repo-language filter, and restart-once followed
    by disable-after-two.
- **Checker:** the ready path reports only new errors. The fallback runs with
  its working directory set to the active root (asserted through the runner
  seam), errors only, capped.
- **Tool level:**
  - `symbols` through the native registry.
  - A `file.edit` result carries the diagnostics line.
  - The registry no longer has the removed tools, and `repo.search` has no
    `kind`.
- **Live, gated on `gopls` on PATH and not `-short`,** using a temp module:
  - a signature change that breaks a caller in another file is reported
  - a method resolves by name
  - references are counted
  - worktree diagnostics reflect the worktree's copy
- One optional `typescript-language-server` live test, gated the same way.
- **Prompt budget:** `TestDefaultPromptBudget`'s tool-schema ceiling drops
  from 16,000 to 15,000 bytes.
- **After landing:** re-run the tool-call analysis over a week of real
  sessions and record `symbols` success rates per operation in this document.

## Known limitations

- typescript-language-server publishes diagnostics only for open documents,
  so for TypeScript "errors anywhere" covers files the session has written or
  queried.
- A server reports "ready" once its handshake completes, but gopls keeps
  loading packages for some seconds after that on a large repo. Symbol queries
  and cross-file diagnostics in that window can come back incomplete, without
  any error.
- The index fallback can't answer `references` or `hover`. Those report that
  no language server is available.
- Servers for Svelte, Python and Rust aren't installed on this machine. They
  work through `[lsp.servers]` config once installed, but have no live tests
  here.

## Out of scope

Installing language servers, LSP code actions or rename, and diagnostics
beyond errors (warnings and hints).

## Measured result

Landed 2026-10-16. `TestDefaultPromptBudget` on a default native-tools
request, with `[lsp] enabled = false`:

| Metric | Value | Ceiling |
| --- | --- | --- |
| System prompt | 7,606 bytes | 9,000 |
| Tool schemas | 13,599 bytes | 15,000 |
| Model-facing tools | 23 | — |

The tool-schema ceiling dropped from 16,000 to 15,000 bytes as planned; the
measured figure sits 1,401 bytes under it. The five retired surfaces
(`symbols.find`, `definition`, `references`, `hover`, `repo.search kind=`)
were replaced by one `symbols` tool, and `diagnostics.check` was removed.

Live verification on this machine (gopls and typescript-language-server
installed): all five `TestLive*` tests pass, and all five skip under `-short`
or when the binaries are absent.

## Post-landing check

After a week of real use, run the tool-call analysis from the bloat review
(per-op `symbols` result summaries from `tool_calls`) and record the success
rate per op here.
