# LSP Integration (Subsystem #4)

**Date:** 2026-07-25
**Status:** Design approved; ready for implementation plan
**Umbrella:** [Passive Knowledge Architecture](2026-07-24-passive-knowledge-architecture-design.md)
**Depends on:** [Semantic Index (#2)](2026-07-25-semantic-index-design.md) and
[Index Watcher (#3)](2026-07-25-index-watcher-design.md) — uses the `index.Run`
orchestrator and the `worker.Worker` lifecycle seam from #3, and the symbol
index from #2.
**Type:** Subsystem spec — real cross-language symbols, navigation, and
diagnostics via the Language Server Protocol, layered over the existing
tree-sitter symbol index.

## Scope

**In (full LSP):**
- An LSP **client** (JSON-RPC over stdio) for the subset Marshal needs.
- A **server manager**: a built-in default language→server map, PATH
  auto-detection, `[lsp]` config override/extend/disable, and per-language
  spawn / health / restart / shutdown. Implements `worker.Worker`.
- **Layered symbol persistence**: the index pass populates the `symbols` table
  from LSP for languages with a ready server (`source='lsp'`), falling back to
  tree-sitter (`source='treesitter'`) otherwise. Adds a `source` column.
- **Live tools**, symbol-name addressed: `definition`, `references`, `hover`.
- **Diagnostics**: LSP as an additional source behind the existing
  `internal/diagnostics.Checker`, consulted by `diagnostics.check` and the
  post-edit hook.

**Out (later / documented extensions):**
- Passive **injection of diagnostics into context packs** — rides the later
  retrieval/passive-context spec (avoids the in-flight `contextpack` churn).
- The **call/reference graph** (umbrella extension #5).
- Non-symbol LSP features: rename, formatting, completion, code actions.

## Guiding decisions (settled during brainstorming)

1. **Full scope** in one spec: client + manager + layered symbols + live tools +
   diagnostics.
2. **Built-in defaults auto-detected on PATH, config-overridable.** Zero-config
   when servers are installed; a `[lsp]` section overrides commands/args or
   disables a language.
3. **Symbol-name-addressed tools.** Tools take a symbol name (+ optional path);
   Marshal resolves it to a declaration position via the `symbols` table, then
   calls LSP. Ambiguous → return candidates; unindexed → friendly message.
4. **Best-effort in-pass symbol population, converge via re-index.** During
   `index.Run`, ready-server languages get LSP symbols; warming/absent servers
   fall back to tree-sitter or are skipped; the watcher / next manual index
   upgrades them once the server is ready.
5. **No new dependency.** The JSON-RPC framing and the LSP type subset are
   hand-rolled with `encoding/json` + `os/exec`.

## Packages & files

- `internal/lsp/client.go` — one connection to one server process:
  - Base-protocol framing (`Content-Length` headers) over the process's
    stdin/stdout; a read loop that correlates responses by request `id` and
    routes notifications.
  - Lifecycle: `initialize`/`initialized` handshake advertising the client
    capabilities Marshal uses; `shutdown`/`exit`.
  - Document sync: `didOpen`/`didClose` (and `didChange` when a watched file
    changes). Interactive tools keep a small open-document cache; the index pass
    opens-queries-closes per file.
  - Requests: `textDocument/documentSymbol`, `textDocument/definition`,
    `textDocument/references`, `textDocument/hover`.
  - Notifications: `textDocument/publishDiagnostics` collected per-URI.
- `internal/lsp/manager.go` — the server registry and lifecycle:
  - `DefaultServers` map (e.g. `go→gopls`, `typescript→typescript-language-server
    --stdio`, `python→pyright`/`pylsp`, `rust→rust-analyzer`).
  - Startup: merge defaults with `[lsp]` config; for each language, enable when
    its command resolves on `PATH` (or is explicitly configured) and not
    disabled; skip the rest silently.
  - Per-language process spawn, crash detection, bounded-backoff restart, and
    graceful shutdown on context cancel. Implements `worker.Worker` (`Run`
    supervises children until `ctx` is cancelled, then shuts them down).
  - `ServerFor(lang) (*client, ready bool)` — used by the index pass and tools;
    `ready` is false while a server is warming up or absent.
- `internal/lsp/symbols.go` — map LSP `DocumentSymbol`/`SymbolInformation` to
  `[]db.Symbol` with `Source="lsp"` (kind/name/receiver/signature/line range).
- Modify `internal/db/symbols.go` + `migrations.go`:
  - Add `source TEXT` to the `symbols` table via the existing `migrationColumns`
    slice (`ALTER TABLE symbols ADD COLUMN source TEXT`, guarded by
    `PRAGMA table_info`).
  - `db.Symbol` gains `Source string`; `SaveSymbols` writes it; `scanSymbol`,
    `GetSymbols`, `FindSymbols` read it (append to the column list).
- Modify `internal/index/run.go` (from #3): per file, choose the extractor —
  ready LSP server for the language → `documentSymbol` (`source='lsp'`); else Go
  tree-sitter (`source='treesitter'`); else no symbols. `SaveSymbols` still
  full-replaces per pass, so layering needs no per-file upsert and best-effort
  converges naturally.
- Modify `internal/diagnostics/` — add an LSP-backed source. Define a small
  interface the `Checker` consults: when a ready server exists for a file's
  language, open the file, collect `publishDiagnostics` for its URI (bounded
  wait), and return those; otherwise run the configured command checker. The
  `diagnostics.check` tool and the `file.go` post-edit hook keep their current
  shape and simply get richer output.
- `internal/tools/native/lsp_tools.go` — `definition`, `references`, `hover`
  (see below).
- Config: `LSPConfig` (`[lsp]`) in `internal/app/config`.
- `internal/app/app.go` — construct the manager, start it via the shared
  `startWorker` helper (#3), and hand it to the index `Deps`, the native tool
  set, and the diagnostics `Checker`. A functional option allows injecting a
  fake manager in tests.

## Live tools (symbol-name addressed)

All three share resolution: look up the symbol name via `db.FindSymbols`
(optionally filtered by a `path` arg).

- 0 matches → `ToolResult` "no indexed symbol named X" (not an error).
- >1 matches and no disambiguating `path` → return the candidate list
  (`path:line` each) and ask the model to pass `path`.
- 1 match → take its declaration position (file + `line_start`, resolving the
  identifier column) and call LSP.

Tools:

- `definition` — `{ "symbol": string (required), "path"?: string }` → the
  resolved declaration location(s) (LSP `textDocument/definition`, useful across
  packages / interfaces / embeddings; falls back to the symbols-table location
  when no server is ready).
- `references` — same schema → LSP `textDocument/references` at the declaration
  position; returns all usage sites as `path:line` with snippets.
- `hover` — same schema → LSP `textDocument/hover`; returns the type
  signature + documentation.

All are `RiskReadOnly`. When no ready server exists for the symbol's language,
each returns a friendly "no language server available for `<lang>`" result.

## Config

```toml
[lsp]
enabled = true                       # master switch; default true

[lsp.servers.go]
command = "gopls"                    # override the default command
args = []

[lsp.servers.typescript]
command = "typescript-language-server"
args = ["--stdio"]

[lsp.servers.python]
disabled = true                      # opt a default language out
```

`LSPConfig`:

```go
type LSPConfig struct {
    Enabled *bool                          `toml:"enabled"`  // nil => true
    Servers map[string]LSPServerConfig     `toml:"servers"`
}
type LSPServerConfig struct {
    Command  string   `toml:"command"`
    Args     []string `toml:"args"`
    Disabled bool     `toml:"disabled"`
}
```

Effective servers = built-in defaults overlaid with `Servers`; a language is
enabled when not `Disabled`, `enabled != false`, and its command resolves on
`PATH`.

## Local-first graceful degradation

With no servers on `PATH` and none configured, LSP is fully inert:

- symbol extraction falls back to tree-sitter (Go) exactly as today;
- `definition`/`references`/`hover` return "no language server available";
- diagnostics fall back to the configured command checker.

Nothing in the existing flows breaks when LSP is unavailable.

## Testing strategy

- **Client** (`client_test.go`) — an **in-process fake LSP server** over
  `io.Pipe`s speaking the base protocol: `initialize` handshake; a
  `documentSymbol` response parsed into `db.Symbol`s; `references` and `hover`
  request/response; `publishDiagnostics` notification collected per URI;
  request-`id` correlation across interleaved responses; clean `shutdown`.
- **Manager** (`manager_test.go`) — PATH detection with a stub executable on a
  temp `PATH`; config override + `disabled` merge; enable/skip decisions;
  crash-then-restart with a stub server that exits once; graceful shutdown on
  context cancel; `ServerFor` returns `ready=false` for absent/warming servers.
- **Symbol mapping** (`symbols_test.go`) — LSP `DocumentSymbol` trees → flat
  `[]db.Symbol` with `Source="lsp"`, correct kinds and line ranges.
- **`index.Run` layering** (`run_test.go`, extends #3) — fake manager reporting a
  ready server for a language ⇒ those files stored `source='lsp'`; other files
  `source='treesitter'`; server-not-ready ⇒ tree-sitter fallback.
- **db** (`symbols_test.go`) — `source` column round-trips through `SaveSymbols`
  / `GetSymbols` / `FindSymbols`; the `migrationColumns` ALTER adds it to a
  pre-existing DB (existing rows get NULL/empty source).
- **Live tools** (`lsp_tools_test.go`) — fake manager/client with canned
  responses: name found (1 match) → LSP called at the right position; ambiguous
  → candidate list; not found → friendly message; no-ready-server → "no language
  server" message; output formatting.
- **Diagnostics** (`checkers_test.go`) — with a ready LSP source the `Checker`
  returns LSP diagnostics; without one it falls back to configured commands;
  `diagnostics.check` output reflects whichever ran.

## Dependencies

No new dependency. JSON-RPC framing and the LSP type subset are hand-rolled with
the standard library (`encoding/json`, `bufio`, `os/exec`).

## Open questions handed to implementation

- The exact built-in default server list and their invocation flags — start with
  gopls / typescript-language-server / pyright / rust-analyzer; expand as needed.
- Bounded wait for `publishDiagnostics` after `didOpen` (servers publish
  asynchronously) — pick a small timeout; return whatever has arrived.
- Restart backoff bounds and a max-restart circuit-breaker per server.
- Identifier-column resolution for a symbol's declaration position (symbols store
  `line_start`; find the name's column on that line, or default to column 0 and
  let the server resolve).
- Whether interactive tools keep documents open (cache) or open-query-close each
  call — default to a small LRU open-set; revisit if servers complain.
