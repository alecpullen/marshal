# P1 — Project host and headless indexing

Parent: [`design.md`](design.md) §4.2. This spec replaces §4.2's sketch
where they differ.
Depends on: nothing. Agent side only: `internal/app`, `internal/index`,
`internal/lsp` and the two `internal/acp` entry points. No change to the
bridge, the TUI's data flow, the database schema or the ACP protocol.
Status: spec agreed 2026-10-19 and revised while planning (H3, H6, Design,
T3, T4 and T8 describe the built prototype). Plan:
[`plans/2026-10-19-p1-project-host-plan.md`](plans/2026-10-19-p1-project-host-plan.md).

Decided:
- **Index scope:** one index per project root, as today. Isolated
  sessions keep searching the root's index. Worktree indexes are a later
  phase (F1).
- **OAuth refresh workers** stay in the TUI until the daemon runs them
  once (P4).
- **Snapshot service, skill index and MCP** stay per session.
- **Database connections:** sessions keep their own. The host opens,
  migrates and reconciles once, and keeps one more connection for index
  writes.

## Goal

Sessions in one process share their project's database setup, index
workers and language servers, and indexing runs on every production path,
so a headless agent keeps its project's embeddings current with no TUI
running. Two processes on one tree run one indexer between them.

## Where the code is today

- **Only the TUI indexes.** `Run` builds the index workers
  (`internal/app/app.go:2883`); `startRuntime`, which ACP uses, builds
  none.
- **Every runtime start ends every open generation in the project.**
  `startRuntime` calls `ReconcileOpenGenerations`
  (`internal/app/runtime.go:1641`), whose `UPDATE` has no session filter
  (`internal/db/generations.go:262`). A second session started in the
  same process ends the first session's live generation.
- **Every rebuild restarts the language servers.**
  `buildAgentRunnerWithLock` builds a new `lsp.Manager` each time it runs
  (`app.go:1059-1064`). A config reload (`app.go:3224-3231`) and
  `NewSession` (`runtime.go:2133-2140`) then stop the old manager and run
  the new one, even when the server set did not change.
- **The TUI's reference finder goes stale after a reload.** It is bound to
  the startup handle (`app.go:2765`), and a reload replaces
  `rt.LSPManager` with a new handle.
- **The index's LSP symbols come from the session's handle**
  (`app.go:2899`), which points at a worktree's servers after the session
  enters one, while the index scans the project root.
- **Nothing reads `Runtime.IndexBroker`.** The comment on
  `buildIndexWorkers` says completions trigger a context-pack re-seed;
  no subscriber exists.
- **Fleet agents are separate processes** (`web/bridge/fleet.go:382`),
  isolated in a worktree by default
  (`web/ui/src/lib/newagent/newAgent.ts:26`). The scanner skips
  `.marshal/` (`internal/repo/scanner.go:51`), so the root index never
  sees an isolated agent's edits.
- **The watcher is opt-in.** `config.WatchEnabled` is true only when
  `indexing.watch = true` (`internal/app/config/watch.go:6`). Without it,
  the index refreshes once per launch.

## In scope

1. A process-wide host registry, one `ProjectHost` per project root.
2. Database bootstrap once per host.
3. Index workers owned by the host, on every production path.
4. A language-server pool shared by sessions, keyed by active root and
   server set.
5. A cross-process index lease, so one process indexes a tree.
6. A 30-second linger, so a session replacement reuses a warm host.

## Out of scope (follow-ups)

- **F1.** Worktree indexes, so an isolated session searches its own
  branch. Copy-seeded versus overlay is decided there.
- **F2.** Routing a project's host-process agents into one shared
  `marshal acp` child, so fleet agents share language servers too. A
  bridge change, in P4.
- **F3.** Pinned projects, an idle timeout for language servers, and
  OAuth refresh in the daemon. P4.
- **F4.** Watching config files and re-checking trust when they change
  (`design.md` §6.2).
- **F5.** Changing the `indexing.watch` default. P1 keeps the watcher
  opt-in.
- **F6.** A new process still ends other processes' open generations at
  startup. The fix belongs with the attach-or-skip check
  (`design.md` §6.5).
- **F7.** Sharing the snapshot service or the skill index, and moving the
  startup snapshot sweep to the host.

## Behaviour

### H1. Registry and keys

- A `Hosts` registry holds one `ProjectHost` per project root. The root
  is what `resolveWorkingDir` returns: the git root, or the directory
  itself outside git. Sessions started from subdirectories of one repo,
  or with different `additionalDirectories`, share one host.
- `acp.Run`, `acp.ListenAndServe` and `app.Run` each create one registry
  per process with indexing on, pass it to every runtime with
  `app.WithHosts`, and close it after every session has closed.
- A runtime started without `WithHosts` gets a private registry with
  indexing off, closed by `Runtime.Close`. Tests and any other caller of
  `StartRuntime` behave as they do today.

### H2. Database bootstrap

- The first session on a root opens the host. Opening runs
  `dbmigrate.AdoptStrayProjectDB`, `db.Open(db.Path(root))`, `Migrate`
  and `ReconcileOpenGenerations`, in that order. Later sessions on the
  same host skip all four.
- A failure fails the session start with today's error text (`open
  database: …`, `migrate database: …`, `reconcile open generations: …`).
  A failed host is not kept; the next start tries again.
- Each session then opens its own connection with `db.Open` and uses it
  for everything it does today. The host keeps its connection for index
  passes and closes it when the host closes. Connections allow one query
  at a time (`internal/db/dbpool.go:15`), so a shared one would queue
  every session's reads behind an index pass.
- Creating `.marshal/`, the git-ignore entries and the session row stay
  per session.

### H3. Index workers

- When indexing is on, the first session to attach starts the host's
  **index supervisor** (H4) with its config and project ID. The host
  cannot start it earlier, because the session resolves the project row.
- The host keeps a **current config**: the first session's, replaced
  whenever a session attaches or a session's config reload succeeds. Each
  pass reads the current config for ignore patterns, size limits and the
  embedding route, as passes read `state.Config` today.
- When a config change alters whether the watcher runs, or its debounce,
  the host stops the supervisor (waiting up to 5s) and starts a new one
  with the new config. Other config changes take effect at the next pass.
- A pass gets LSP symbols from the pooled manager running for the host
  root (H5), if there is one, and runs without LSP symbols otherwise.
- Pass progress goes to every attached session's activity, as
  `buildIndexWorkers` sets it on the one session today.
- `ProjectHost.indexPass` replaces `buildIndexWorkers`'s pass.
  `buildIndexWorkers`, `namedWorker` and `index_worker_test.go` are
  removed; the host tests cover what that test file did.
- Completed passes are published on the host's own `index.Report` broker.
  `Runtime.IndexBroker` is removed.
- The test seam `app.WithWorker` moves with the workers: in `Run`,
  injected workers replace the host's index supervisor, start when the
  first session attaches, and stop when the registry closes.

### H4. Index lease

- The lease is a non-blocking exclusive `flock` on
  `<root>/.marshal/index.lock`, held for as long as the holder indexes.
  The file's contents are not used.
- The supervisor:
  1. Tries the lease.
  2. If another process holds it: with the watcher off, the supervisor
     stops (someone is scanning now). With the watcher on, it waits 30s
     and tries again, until the host closes.
  3. Once it holds the lease, it runs the startup scan. If the scan
     fails, it logs the error, releases the lease and stops for the rest
     of this supervisor's life, so a process that cannot index (an
     unreachable embedder, say) does not keep the lease from one that
     can.
  4. After a successful scan: with the watcher on, it runs the watcher
     until the host closes, then releases. With the watcher off, it
     releases at once.
- A watcher pass that fails is logged and the lease is kept, as a
  transient failure.
- If the lock file cannot be opened or locked for any reason other than
  another holder, the supervisor logs it and indexes without the lease.
  Duplicate indexing costs work but breaks nothing. Platforms without an
  `flock` binding (none of the release targets) behave the same way.
- Known limit: `flock` does not cross the Docker Desktop VM boundary on
  macOS, so a containerized agent and a host process can both index one
  tree there.

### H5. Language-server pool

- The host owns a pool of `lsp.Manager`s keyed by **active root** and a
  **server-set fingerprint**: each language, command and arguments of
  `lsp.ServersForRoot(servers, root)`, sorted.
- Each runtime keeps **one `lsp.Handle` for its whole life**. Binding
  points that handle at a pooled manager with a new
  `Handle.Set(m *Manager) (old *Manager)`. Tools, the diagnostics
  checker, the symbol query and the TUI's reference finder keep the same
  handle throughout, so the stale reference finder is fixed.
- A runtime rebinds when:
  - it starts (root: the session's active root, which a resumed session
    may have in a worktree);
  - a `WorkspaceEvent` changes the active root;
  - a config reload succeeds (the server set may differ);
  - `NewSession` installs a replacement session (root: its active root);
  - it closes (it binds to nothing).
- Binding acquires the new manager, sets the handle, then releases the
  old one. When the key is unchanged (a reload that kept the servers),
  the runtime keeps the manager it has. When LSP is disabled or no server
  is detected, the handle is set to nil and any previous manager is
  released.
- The pool runs each manager's `Run` loop under the host's context.
  Releasing the last reference stops the manager after the 30s linger
  (H6). Acquiring during the linger cancels the stop.
- `Handle.Restart` loses its only app caller.

### H6. Lifetimes

- A host stays open while at least one session is attached, plus 30s
  after the last one detaches. ACP `session/load` and `session/resume`,
  and TUI resume, close the old runtime before starting the new one, so
  within the linger they reuse the open database, the language servers
  and the index without a rescan.
- `Hosts.Close` closes every host at once, with no linger.
- Closing a host: stop the index supervisor and the pooled managers,
  wait up to 5s for them, close the index broker, then close the host's
  database connection.
- Opening a root whose previous host is still closing waits for that
  close to finish, so its lease and language servers are released first.
- `Runtime.Close` releases its language-server binding and detaches from
  its host last, after its own database connection is closed. A
  `WorkspaceEvent` bind still in flight when `Close` runs does nothing.
- The TUI closes its registry when `Run` returns, so quitting waits up to
  5s for index work to stop. Index passes are cancelled, so this is
  normally immediate.
  `NewSession` detaches the replaced session's state and attaches the
  new one.

### H7. Logging

- The host opens its own append handle on `logPath(root)` and closes it
  when the host closes. It must not log through a session's logger,
  whose file closes with that session.

## Design

| Area | Change |
|---|---|
| `internal/app/hosts.go` (new) | `Hosts`, `ProjectHost`, open/attach/detach/linger/close, H2 bootstrap, current config, progress fan-out, `indexPass`, and `WithHosts`. |
| `internal/app/host_index.go` (new) | The H4 supervisor: a value with injected lease, scan and watcher functions, so it is tested without a host. |
| `internal/app/lsp_pool.go` (new) | The H5 pool and the server-set fingerprint. |
| `internal/index/lease.go`, `lease_unix.go`, `lease_other.go` (new) | `TryLease(root string) (release func(), held bool, err error)`. The unix file uses `unix.Flock` with `LOCK_EX\|LOCK_NB`, like `internal/snapshot/lock_unix.go`. |
| `internal/lsp/handle.go` | Add `Set`. |
| `internal/app/runtime.go` | `startRuntime` gets its database setup and LSP binding from the host. `Runtime` gains `Host *ProjectHost` and `bindLSP`/`unbindLSP`, and drops `IndexBroker`, `lspCancel` and `runLSPManager`. `NewSession`, the `WorkspaceEvent` goroutine and `Close` rebind or release as H5 and H6 say. |
| `internal/app/app.go` | `buildAgentRunnerWithLock` receives the runtime's handle instead of creating and returning one; the test-only `buildAgentRunner` keeps its signature and passes an empty handle. `reloadAgentRuntime` rebinds and updates the host's config. `Run` creates a registry, stops building index workers, and keeps OAuth workers per iteration. |
| `internal/acp/run.go`, `internal/acp/listen.go` | Create a registry per process, pass `app.WithHosts`, close it after the agent host closes. |
| Exported API | `app.NewHosts() *Hosts`, `(*Hosts).Close(ctx context.Context) error`, `app.WithHosts(*Hosts) Option`, `Runtime.Host`, `(*ProjectHost).Root() string`. Everything else stays unexported. |

Constants: host and manager linger 30s; lease retry 30s; host close wait
5s.

## Tests

- **T1. Registry.** Two runtimes on one root (one started from a
  subdirectory) get the same `Runtime.Host`; different roots get
  different hosts. After both close, the host stays open through the
  linger and closes after it; a start inside the linger reuses it.
  `Hosts.Close` closes an attached host at once.
- **T2. Reconcile once.** With a shared registry, an open generation
  inserted for session 1 is still open after session 2 starts on the same
  root. A private registry per runtime still reconciles as today.
- **T3. Pool.** Two runtimes on one root with an LSP server configured
  (the `lsp-stub.sh` fixture the app tests already use) see the same
  `LSPManager.Get()`. A runtime whose active root moves to a worktree
  gets a different manager, and returns to the shared one when it moves
  back. A reload with an unchanged server set keeps the manager pointer,
  on a shared registry and on a private one (which has no linger, so this
  also checks that a rebind acquires before it releases); a changed one
  gets a new manager. The last release stops the manager after the
  linger.
- **T4. Stable handle.** `Runtime.LSPManager` is the same pointer before
  and after a reload and after `NewSession`. A bind after `Close` takes no
  lease.
- **T5. Lease.** A second `TryLease` on the same root fails while the
  first is held and succeeds after release. A root that does not exist
  reports an error rather than `held = false`, and the supervisor then
  indexes without the lease.
- **T6. Supervisor**, with fake passes. Watcher off: a held lease skips;
  a free one scans once and releases. Watcher on: a held lease retries
  and takes over after release, starting with a scan. A failed startup
  scan releases the lease and stops. A config change that turns the
  watcher on restarts the supervisor.
- **T7. Headless refresh (done-when).** `StartRuntime` with
  `WithHosts(NewHosts())`, `indexing.watch = true`, a short debounce and
  an `openai_compatible` embedding provider served by `httptest`. Writing
  a new file in the tree produces chunks for that file within the test
  deadline.
- **T8. ACP sharing.** Through the ACP `SessionManager`, starting real
  runtimes with a shared registry, two `session/new` calls on one cwd
  share one `Runtime.Host` and one `LSPManager.Get()`. The production
  entry points (`acp.Run`, `acp.ListenAndServe`) are checked with a built
  binary instead (plan Task 9).
- **T9. TUI seam.** `Run` with `WithWorker` still starts the injected
  worker and stops it on shutdown (the existing test, unchanged).

## Done when

- Two ACP sessions on one repo in one process share one project host, one
  set of language servers and one index watcher (T8, T1, T3).
- With `indexing.watch = true`, an edit in a headless session's tree
  refreshes its embeddings with no TUI running (T7).
- Two processes on one tree run one indexer between them (T5, T6).
- `go test ./...` and `go vet ./...` pass, apart from the known local
  baseline failures.

## Implementation record

Executed 2026-10-19 on branch `p1-project-host` from base `42f04028`, one
commit per plan task. Tasks 1–8 are the code:

```
840c0e16 p1-project-host: task 1 — Index lease
e3f41f5f p1-project-host: task 2 — Handle.Set
6573624c p1-project-host: task 3 — Language-server pool
a67b5363 p1-project-host: task 4 — Index supervisor
bb9ba6ec p1-project-host: task 5 — Project hosts
611bc983 p1-project-host: task 6 — Runtimes on the project host
83ec7778 p1-project-host: task 7 — TUI on Hosts; index work leaves the runtime
b41c8d4f p1-project-host: task 8 — ACP entry points
```

Task 9 is the commit that adds this record and updates the status lines in
this spec, `design.md` and the plan. Every task's Verify commands passed
before its commit, and the plan's code blocks were applied verbatim.

**Repository checks (plan Task 9, step 1).** `gofmt -l .` printed nothing,
`CGO_ENABLED=1 go build ./cmd/marshal` succeeded, and `go vet ./...` was
clean. `go test -race` passed on `internal/app`, `internal/acp`,
`internal/lsp` and `internal/index`. Two full `go test ./...` runs failed
only in the known local baseline packages, and not consistently:
`internal/tools/desktop/browser` (`TestStandaloneBackendNavigateAndRead`)
and `internal/worktree` (`TestCLIGitOpsCommitAllWithoutARepoIdentity`)
failed in both; `internal/snapshot`
(`TestRunManagedCancellationKillsTree`) failed in the first run and passed
in the second, matching the plan's note that it is flaky;
`internal/app/tui` (`TestStatusLineUntrustedIsWarningColored`), which the
plan also allows, passed in both. No failure appeared in a package this
work touched.

**Smoke check (plan Task 9, step 2).** A built `marshal acp` with an
isolated `HOME`/`XDG_CONFIG_HOME`/`XDG_DATA_HOME`, two `session/new` calls
on one fresh git project, then the project database read back:

```
session/new results: True True
index.lock exists: True
files rows: 1
acp exit code: 0
log lines mentioning index: []
```

Both sessions started on one project, the startup scan wrote the file
index (`files rows: 1`), and the process exited cleanly when stdin closed.
The smoke config sets no `[indexing]` section and no embedding route, so
the watcher is off: the supervisor takes the lease, scans, and releases it
before the script's 3s sleep ends. `index.lock exists` therefore shows the
lock file, not a held lease, and the pass is hashing-only. The embedding
path is covered by `TestHeadlessRuntimeRefreshesEmbeddingsAfterAnEdit`
(plan Task 7), not by this script.

**Deviations.** None from the plan's code or file lists. The work ran in a
git worktree rather than the main checkout, so the spec and plan were
copied in before Task 1 and committed with the work in Task 9.
