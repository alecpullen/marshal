# P2 plan 8b: Browsers — implementation plan

Date: 2026-11-06. Status: ready for review.
Base: `main` at `6c6d16b2` (plans 7c and 7d merged). Every anchor below was checked there.
- **This plan runs in parallel with plan 8a** (decided with the user, 2026-11-06). The two touch different TUI files. In the files both edit, each inserts at its own anchor, named in its steps:
  - `acpclient/acpclient.go` and `acpclient/methods.go`: this plan inserts after `DecideTrust` and after `CommandAction`; 8a appends to the end of `Session` and of `Methods`;
  - `inproc/inproc.go` and `inproc/events.go`: this plan adds `Deps` and `Session` fields after `Tools` and `tools`, and the `newSession` key after `tools:`; 8a adds after `UserConfigPath`, `modelCacheDir` and `modelCacheDir:`;
  - `tui/model.go` and `tui/commands_dispatch.go`: different functions. The one likely conflict is `model.go`'s import block, where this plan drops `skills` and 8a drops the `sddplans` line next to it. Keep both removals;
  - `tui/boundary_allowlist_test.go` and `tui/moved_reads_test.go`: different keys and entries;
  - `docs/daemon/p2-client-seam.md`: both entries in its revision record are already written. Only the Implementation record can conflict, and the second plan to merge keeps both entries.

Approved design: [p2-client-seam.md](../p2-client-seam.md): C2, C5 (the skill gate in `Status`), C10 (skill-gate settings), C11 (project data), Migration order item 8b, its revision record entry "2026-11-06, against `6c6d16b2` (plans 7c and 7d merged)", and tests T1, T1b, T4 and T5. P3's R9 list ([p3-acp-transport.md](../p3-acp-transport.md)) records the ACP differences this plan finds.
Previous plans: [plan 7c, trust and tools](2026-11-05-p2-7c-trust-tools-plan.md) and [plan 7d, secrets](2026-11-05-p2-7d-secrets-plan.md).
Authoring workflow: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md).

**How this plan is written.** No prototype was built, and the plan embeds no implementation code. The Contracts section holds declarations and the exact strings the tests pin. Every other step states its decision in prose. Executing the plan also tests how well Marshal follows a plan. "As `X` does" means `X` exists and is the model to copy.

**Goal:** the session picker and the memory, plugins and skills panels reach the agent side only through the seam.
- `acpwire` gains the browser types, `CodeNoDatabase` and `SessionStatus.SkillGate`.
- `acpclient.Host` gains `ListSessions`, and `acpclient.Session` gains fourteen methods for memory, plugins, skills and the skill gate.
- The three panels stop importing `db`, `plugins`, `skills` and `app/session`, and stop calling `config`'s file functions. The plugins panel's clone, scan, hash and install move into the adapter.
- `WithSkillIndex` is deleted.

**Non-goals:**
- **No history, file index, doctor, plans or run outcome.** Plan 8a moves them.
- **No deletion of `WithMemoryStore`, `memoryDB` or `memoryProject`.** Plan 8a's file index and doctor still read them on its branch, so plan 9 deletes them.
- **No change to what users see.**
- **No ACP behaviour change.** No changelog entry.

**Assumptions:**
- Every Go test command below runs with `TMPDIR=/private/tmp/p2-8b-suite`. Create the directory first: `mkdir -p /private/tmp/p2-8b-suite`. Keep the path short: a long one breaks the unix-socket tests in `internal/acp` and `web/bridge`. Plan 8a uses `/private/tmp/p2-8a-suite`. Never share a directory between the two runs.
- **The base.** The full suite ran at `6c6d16b2` while this plan was written: 117 packages passed. `internal/acp`'s `TestStackPatchApplyBudget` and `TestStackRoundTripBudget` failed under the suite's load and pass alone with `-count=3`. `web/bridge`'s `TestRecipeRunMaxUSDSetsAPausingCapForTheRun` has hung about one run in three since `90488446`. The hang is in its fake agent (`fakeagent_test.go`), not in product code, so treat a hang there as known.
- **Two suites may run at once.** Load makes the flakes listed under Task 7 likelier. Rerun a failure alone before treating it as new.
- **The session picker today.** `openSessionPicker` (`model.go:6473`) reads `m.state.DB()`. With none it notes "Session list is not available (no database)."; otherwise it calls `database.ListSessions(ctx, st.WorkingDir, "", 100)` and notes "Could not list sessions: %v" on an error. Each item's detail is `UpdatedAt.Format("2006-01-02 15:04")` and the message count. The database returns `UpdatedAt` in UTC (`db/sessions.go:567`).
- **The memory browser today.**
  - `/memory` (`commands_dispatch.go:262`) notes "Memory browser not available (no database configured)." without `memoryDB`; Ctrl+K (`keypress.go:135`) does nothing then. Otherwise both open `memory.NewPanel(m.memoryDB, m.memoryProject)`.
  - The panel loads `GetMemories(projectID)`, the project's own rows in any scope, and deletes with `DeleteMemory` before it emits `DeletedMsg` (`memory/panel.go:121`).
  - Picking an entry emits `ShowMsg{ID}`, and the model notes `memory.RenderEntry(m.memoryDB, id)` (`model.go:1829`), which re-reads the row with `GetMemory`.
- **The plugins panel today** (`plugins/panel.go`).
  - It lists the global lockfile, and the project lockfile when trusted, on every render (`lockEntries`, `:216`, called from the field builder).
  - A scan (`runScan`, `:620`, and `scanPlugin`, `:650`) normalizes and validates the source, clones it into a fresh temp parent, removes `.git`, scans it and hashes it. A `scanLease` (`scan_lease.go`) owns the temp parent until the panel claims it, so an abandoned or undelivered scan still deletes its clone. Generations and the lease stop a late result from attaching to a new review.
  - Confirm (`runInstallFromScan`, `:775`) refuses when the source or scope changed since the scan, hands the clone to a worker, and installs with `plugins.InstallReviewed` against the reviewed hash. The worker deletes the parent on every outcome.
  - Remove (`runRemove`, `:349`) deletes the store directory and the lockfile entry.
- **The skills panel today** (`skills/panel.go`).
  - It lists both stores with `skills.ListScopes` on every render (`buildFields`, `:444`): the global store under `config.UserDir(home)/skills`, never `skills.ScopeDir`, and the project store under `<work dir>/.marshal/skills`.
  - "Load now" (`loadableSelection`, `:206`) re-reads the stores and compares the entry with the snapshot the frame was built from (same file, same content), then with the session's index, then refuses an active skill. It loads from a one-entry index with `skills.LoadSkillIntoSession`.
  - Install (`runInstall`, `:376`) calls `skills.Install` into the scope's store. Remove (`runRemove`, `:421`) calls `skills.RemoveScopedSkill` with the discovered entry.
  - Autoload (`setAutoload`, `:145`) writes `skills.autoload` with `config.SaveUserConfigValue` and assigns the state's config without its lock.
  - The skill gate's row and decision rows call `SkillGateEnabled`, `SkillGateSetEnabled`, `SkillGateDecisions` and `SkillGateClearDecision` on the state.
- **Panels rebuild their rows on every render.** `listpanel.FieldList.Rows` calls `Refresh`, which calls the builder (`listpanel/fieldlist.go:128`).
- **`Update` has a value receiver** (`model.go:1459`). A closure that runs later captures the session and the context values, never `m`.

**Decisions this plan makes beyond the spec** (all are recorded in the spec's revision record):

1. **Memory, plugins and skills are `Session` methods.** Their ACP methods are session-scoped, and each reads the session's project, home directory or trust.
2. **`CodeNoDatabase`** marks a session without a database, so the session picker and `/memory` keep their notes, and Ctrl+K keeps doing nothing.
3. **Memory keeps the project's own rows** (`GetMemories`). `session/memory_list` lists what the session sees; P3 gives it an optional param (R9). A picked entry renders from the listing the panel holds, so `ShowMsg` carries the entry.
4. **The plugin scan stays agent side behind a token.** The adapter keeps the clone and its reviewed hash, installs through `plugins.InstallReviewed`, and deletes the clone on every outcome. A closed session discards the scans it still holds. The panel's `scanLease` owns a token instead of a directory, with the same guarantees: a token nobody claims is discarded exactly once.
5. **Skills are keyed by scope and source.** The adapter keeps its last listing per session. `LoadSkill` and `RemoveSkill` find the entry there and check the store against it, as the panel checked its frame's snapshot. Why a load is unavailable is worked out when the list is read.
6. **A skill install stays one step**, on a new `session/skills_install`.
7. **`SetSkillAutoload` writes the user config and installs the change in the live config**, under the state's own locking.
8. **The skill gate is read from `Status().SkillGate`**, and changed with `SetSkillGate` and `ClearSkillGateDecision`. Both fire `Changed`.
9. **Panels cache their listings.** Each reads its listing when it opens and after each action that changes it. A listing error shows an empty list, as today's ignored read errors did.
10. **The panels take narrow interfaces**, `plugins.Service` and `skills.Service`, which `acpclient.Session` satisfies, so their tests can fake the session. The memory panel takes its entries and a delete function.
11. **The allowlist loses 9 entries across 5 files, and 4 keys go entirely:** `memory/entries.go` (`db`), `memory/panel.go` (`db`), `plugins/panel.go` (`plugins`) and `skills/panel.go` (its five entries), and `model.go` loses `marshal/internal/skills`. T1b gains `SkillGateEnabled`, `SkillGateSetEnabled`, `SkillGateDecisions`, `SkillGateClearDecision` and `HasActiveSkill`, and `deferredStateReads` loses the skills panel's `Config`.
12. **The new ACP methods are named** `session/skills_install`, `session/skills_autoload`, `session/skill_gate_set` and `session/skill_gate_clear`. The other eleven methods name ACP methods that exist.

**Execution:**
- Branch first: `git switch -c p2-8b-browsers` from `main`.
- If `git status` shows `docs/daemon/p2-client-seam.md`, `docs/daemon/p3-acp-transport.md` or this plan uncommitted, commit them first as explicit paths, with the message `p2-8b-browsers: record the plan 8a and 8b revisions and plan 8b`. If plan 8a's run committed the specs already, commit only this plan. Plan 8a's plan file may also be uncommitted: leave it to plan 8a. The main checkout holds other untracked files (`scripts/lmstudio-bench/` and others). Leave them alone.
- Run the tasks in order. Run each task's Verify and commit it as `p2-8b-browsers: task N — <title>`.
- Stage explicit paths only. Never `git add -A` without a path.
- Run `gofmt -w` on every Go file a task touches before its Verify.

## Contracts

### Wire types

`internal/acpwire/browsers.go` (new):

```go
// SessionEntry is one stored session the resume picker lists (C11, plan 8b).
// UpdatedAt is Unix milliseconds.
type SessionEntry struct {
	SessionID    string `json:"sessionId"`
	Title        string `json:"title,omitempty"`
	UpdatedAt    int64  `json:"updatedAt,omitempty"`
	MessageCount int    `json:"messageCount,omitempty"`
}

// MemoryEntry is one of the project's memories. CreatedAt is Unix
// milliseconds.
type MemoryEntry struct {
	ID         int64  `json:"id"`
	Kind       string `json:"kind"`
	Content    string `json:"content"`
	Confidence string `json:"confidence,omitempty"`
	Scope      string `json:"scope,omitempty"`
	CreatedAt  int64  `json:"createdAt,omitempty"`
}

// PluginEntry is one installed plugin, from the global or the project
// lockfile. Scope is "global" or "project".
type PluginEntry struct {
	Name   string `json:"name"`
	Source string `json:"source"`
	Ref    string `json:"ref,omitempty"`
	Commit string `json:"commit,omitempty"`
	Scope  string `json:"scope"`
}

// PluginScan is a plugin cloned and scanned for review, not yet installed.
// Token names the staged clone that InstallPlugin and DiscardPluginScan take.
// Source is the normalized clone URL the scan fetched.
type PluginScan struct {
	Token    string         `json:"token"`
	Name     string         `json:"name"`
	Source   string         `json:"source"`
	Commit   string         `json:"commit,omitempty"`
	Contents PluginContents `json:"contents"`
}

// PluginContents is everything a scanned plugin would add.
type PluginContents struct {
	Skills      []PluginItem               `json:"skills,omitempty"`
	Commands    []PluginItem               `json:"commands,omitempty"`
	Hooks       []PluginHook               `json:"hooks,omitempty"`
	MCPServers  map[string]PluginMCPServer `json:"mcpServers,omitempty"`
	MCPPolicies map[string]string          `json:"mcpPolicies,omitempty"`
}

// PluginItem is one passive entry: a skill or a slash command.
type PluginItem struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// PluginHook is a scanned hook, as config.HookConfig holds it.
type PluginHook struct {
	Event     string `json:"event"`
	Matcher   string `json:"matcher,omitempty"`
	Command   string `json:"command"`
	TimeoutMs int    `json:"timeoutMs,omitempty"`
}

// PluginMCPServer is a scanned MCP server, as config.MCPServerConfig holds it.
type PluginMCPServer struct {
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Trust   string            `json:"trust,omitempty"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Type    string            `json:"type,omitempty"`
	Auth    string            `json:"auth,omitempty"`
}

// SkillEntry is one skill in the global or the project store. Scope and
// Source identify it: Source is the store child discovery found, never the
// frontmatter name. LoadBlocked says why Load now cannot run for it, "" when
// it can.
type SkillEntry struct {
	Scope       string `json:"scope"`
	Source      string `json:"source"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	LoadBlocked string `json:"loadBlocked,omitempty"`
}

// SkillGateState is the session's skill load gate: whether it is on, and its
// sticky decisions, sorted by skill.
type SkillGateState struct {
	Enabled   bool                `json:"enabled,omitempty"`
	Decisions []SkillGateDecision `json:"decisions,omitempty"`
}

// SkillGateDecision is one sticky decision. Count is the deny counter, 0 for
// an allowed skill.
type SkillGateDecision struct {
	Skill   string `json:"skill"`
	Allowed bool   `json:"allowed,omitempty"`
	Count   int    `json:"count,omitempty"`
}
```

`internal/acpwire/errors.go` gains, after `CodeNoRunner`:

```go
	// CodeNoDatabase means the session has no project database.
	CodeNoDatabase ErrorCode = "no_database"
```

`SessionStatus` (`internal/acpwire/status.go:10`) gains, after `Notice`:

```go
	// SkillGate is the session's skill load gate (C11, plan 8b).
	SkillGate SkillGateState `json:"skillGate"`
```

### Interface (`internal/acpclient`)

`Host` gains, directly after `DecideTrust`:

```go
	// ListSessions lists the stored sessions whose working directory is cwd,
	// most recent first, at most limit. It fails with CodeNoDatabase when
	// the agent has no database.
	//
	// ACP: session/list (exists).
	ListSessions(ctx context.Context, cwd string, limit int) ([]acpwire.SessionEntry, error)
```

`Session` gains, directly after `CommandAction` and before `PrepareMCPLogin`, after one blank line:

```go
	// Memories lists the project's own memories, oldest first. It fails with
	// CodeNoDatabase when the session has no database.
	//
	// ACP: session/memory_list (exists).
	Memories(ctx context.Context) ([]acpwire.MemoryEntry, error)

	// DeleteMemory deletes one memory. It fails with CodeNoDatabase when the
	// session has no database.
	//
	// ACP: session/memory_delete (exists).
	DeleteMemory(ctx context.Context, id int64) error

	// Plugins lists the installed plugins: the global lockfile's, then the
	// project lockfile's when the session is trusted.
	//
	// ACP: session/plugins_list (exists).
	Plugins(ctx context.Context) ([]acpwire.PluginEntry, error)

	// ScanPlugin clones source into a staging directory and scans it, without
	// installing anything. The result's token names the staged clone until
	// InstallPlugin or DiscardPluginScan takes it, or the session closes.
	//
	// ACP: session/plugins_install_scan (exists).
	ScanPlugin(ctx context.Context, source string) (acpwire.PluginScan, error)

	// InstallPlugin installs the staged clone token names into scope's store,
	// if its content still hashes as it did when scanned. The staged clone is
	// deleted whatever the outcome.
	//
	// ACP: session/plugins_install_confirm (exists).
	InstallPlugin(ctx context.Context, token, scope string) error

	// DiscardPluginScan deletes the staged clone token names.
	//
	// ACP: session/plugins_install_discard (exists).
	DiscardPluginScan(ctx context.Context, token string) error

	// RemovePlugin deletes an installed plugin and its lockfile entry.
	//
	// ACP: session/plugins_remove (exists).
	RemovePlugin(ctx context.Context, name, scope string) error

	// Skills lists both skill stores, and remembers the listing for LoadSkill
	// and RemoveSkill.
	//
	// ACP: session/skills_list (exists).
	Skills(ctx context.Context) ([]acpwire.SkillEntry, error)

	// LoadSkill injects the listed skill into the session, if the store and
	// the session's index still hold the content the listing saw. It returns
	// the skill's name.
	//
	// ACP: session/skills_load (exists).
	LoadSkill(ctx context.Context, scope, source string) (string, error)

	// InstallSkill installs a skill from source into scope's store, and
	// returns its name.
	//
	// ACP: session/skills_install (new in P3).
	InstallSkill(ctx context.Context, source, scope string) (string, error)

	// RemoveSkill deletes the listed skill's source from its store.
	//
	// ACP: session/skills_remove (exists).
	RemoveSkill(ctx context.Context, scope, source string) error

	// SetSkillAutoload adds name to skills.autoload in the user config, or
	// removes it, and installs the change in the session's live config.
	//
	// ACP: session/skills_autoload (new in P3).
	SetSkillAutoload(ctx context.Context, name string, on bool) error

	// SetSkillGate turns the session's skill load gate on or off. Turning it
	// on clears its decisions.
	//
	// ACP: session/skill_gate_set (new in P3).
	SetSkillGate(ctx context.Context, on bool) error

	// ClearSkillGateDecision clears the gate's sticky decision for skill, so
	// its next load asks again.
	//
	// ACP: session/skill_gate_clear (new in P3).
	ClearSkillGateDecision(ctx context.Context, skill string) error
```

`Methods` gains the `ListSessions` row directly after the `Host`/`DecideTrust` row, and the fourteen `Session` rows directly after the `Session`/`CommandAction` row, before the blank line:

| Iface | Name | ACP | Exists |
|---|---|---|---|
| Host | ListSessions | session/list | true |
| Session | Memories | session/memory_list | true |
| Session | DeleteMemory | session/memory_delete | true |
| Session | Plugins | session/plugins_list | true |
| Session | ScanPlugin | session/plugins_install_scan | true |
| Session | InstallPlugin | session/plugins_install_confirm | true |
| Session | DiscardPluginScan | session/plugins_install_discard | true |
| Session | RemovePlugin | session/plugins_remove | true |
| Session | Skills | session/skills_list | true |
| Session | LoadSkill | session/skills_load | true |
| Session | InstallSkill | session/skills_install | false |
| Session | RemoveSkill | session/skills_remove | true |
| Session | SetSkillAutoload | session/skills_autoload | false |
| Session | SetSkillGate | session/skill_gate_set | false |
| Session | ClearSkillGateDecision | session/skill_gate_clear | false |

### Adapter (`internal/acpclient/inproc`)

`Deps` gains, directly after `Tools`:

```go
	// SkillIndex reads the runtime's skill index, the one LoadSkill checks
	// against. Nil, or a nil index, means the session has none.
	SkillIndex func() *skills.Index
```

`Session` gains, directly after the `tools` field, as its own commented block:

```go
	// skillIndex is Deps.SkillIndex. skillListing is the last Skills listing,
	// by scope and source; pluginScans holds the staged plugin scans by token,
	// and pluginSeq numbers them. All three of the last are guarded by mu.
	skillIndex   func() *skills.Index
	skillListing map[string]skills.ScopedSkill
	pluginScans  map[string]*pluginScan
	pluginSeq    uint64
```

`newSession` sets `skillIndex: d.SkillIndex` directly after `tools:`. The two maps are created lazily, under `mu`, by the first write.

New files: `browsers.go` (`ListSessions` on the host, `Memories`, `DeleteMemory`), `plugins.go` and `skills.go`, each with a `_test.go`.

- `type pluginScan struct { parent, cloneDir, name, source, commit, hash string }` in `plugins.go`: one staged scan.
- `func (s *Session) discardPluginScans()` in `plugins.go`: takes every staged scan under `mu`, then deletes their parents after releasing it.
- `var errNoDatabase = &acpwire.Error{Code: acpwire.CodeNoDatabase, Message: "inproc: the session has no database"}` in `browsers.go`.

### App (`internal/app`)

`Run` (`app.go:2735`) sets `Deps.SkillIndex` to a function that returns `rt.SkillIndex` under `rt.mu`, as the `WithLegacySession` closure reads `rt.State` (`app.go:2820`), and stops passing `tui.WithSkillIndex` (`:2801`).

### TUI

- `memory.NewPanel(entries []acpwire.MemoryEntry, loadErr error, del func(id int64) error) *BrowserPanel`. A nil `del` reports `no database` on a delete, as a nil database did.
- `memory.RenderEntry(e acpwire.MemoryEntry) string`, and `memory.ShowMsg{Entry acpwire.MemoryEntry}`.
- `(*Model).openMemory(fromKey bool) bool`: opens the memory browser, and reports whether it did.
- In `internal/app/tui/plugins`:

  ```go
  // Service is what the panel asks the session for. acpclient.Session
  // satisfies it.
  type Service interface {
  	Plugins(ctx context.Context) ([]acpwire.PluginEntry, error)
  	ScanPlugin(ctx context.Context, source string) (acpwire.PluginScan, error)
  	InstallPlugin(ctx context.Context, token, scope string) error
  	DiscardPluginScan(ctx context.Context, token string) error
  	RemovePlugin(ctx context.Context, name, scope string) error
  }

  func NewPanel(svc Service, projectTrusted bool, note func(string)) *Panel
  ```

  `newScanLease(cancel context.CancelFunc, discard func(token string)) *scanLease`, with `Publish(token string) bool`, `Claim() (string, bool)` and `Close()`.
- In `internal/app/tui/skills`:

  ```go
  // Service is what the panel asks the session for. acpclient.Session
  // satisfies it.
  type Service interface {
  	Config() config.Config
  	Status() acpwire.SessionStatus
  	Skills(ctx context.Context) ([]acpwire.SkillEntry, error)
  	LoadSkill(ctx context.Context, scope, source string) (string, error)
  	InstallSkill(ctx context.Context, source, scope string) (string, error)
  	RemoveSkill(ctx context.Context, scope, source string) error
  	SetSkillAutoload(ctx context.Context, name string, on bool) error
  	SetSkillGate(ctx context.Context, on bool) error
  	ClearSkillGateDecision(ctx context.Context, skill string) error
  }

  func NewPanel(svc Service, projectTrusted bool) *Panel
  ```

- **Deleted:** `WithSkillIndex` and the model's `skillIndex` field; in the plugins panel, `newPanelWithScanDeps`, `scanDeps`, `defaultScanDeps`, `scopedEntry`, `lockEntries`, `storePaths`, the four store and lock path helpers, the `homeDir`, `workDir`, `scanParent` and `scannedHash` fields; in the skills panel, the `homeDir`, `workDir`, `state` and `skillIndex` fields, `homeRoot`, `globalSkillsDir`, `projectSkillsDir`, `activeIndex` and `loadableSelection`.

### Pinned strings

- `errNoDatabase`'s message: `inproc: the session has no database`.
- `InstallPlugin` and `RemovePlugin` with project scope on an untrusted session: `project scope requires a trusted session`.
- An unknown plugin token: `inproc: no plugin scan "<token>"`.
- A plugin scope other than `global` or `project`: `invalid scope "<scope>": want "global" or "project"`.
- `LoadSkill` or `RemoveSkill` for an entry the last listing lacks: `<source> is no longer in the <scope> store — reopen the panel`.
- Every other message keeps today's wording, moved agent side where its check moved: the panel's scan validation errors, the four load-unavailable reasons and the three disk-change reasons of `loadableSelection`, the skill install's three refusals (`enter a source first`, `project scope needs a trusted folder`, `unknown skill scope %q`), `cannot remove skill: unknown scope %q`, and the panels' status and error lines.

## Task 1: The seam carries the session list and memory

**Goal:** the session and memory types, `CodeNoDatabase`, `Host.ListSessions`, `Memories` and `DeleteMemory` exist in the seam and the adapter. The TUI uses none of them yet.

**Files:**
- `internal/acpwire/browsers.go` (new; this task adds `SessionEntry` and `MemoryEntry`, Tasks 2 and 3 the rest), `internal/acpwire/errors.go`;
- `internal/acpclient/acpclient.go`, `internal/acpclient/methods.go`;
- `internal/acpclient/inproc/browsers.go` (new), `internal/acpclient/inproc/browsers_test.go` (new).

**Steps:**

1. **Declarations.** Add the two types, the code, the three methods with their doc comments, and their three `Methods` rows, at the anchors Contracts names.
2. **`ListSessions`** (on the host) reads the current state's database. Without one it returns `errNoDatabase`. Otherwise it maps `database.ListSessions(ctx, cwd, "", limit)` to `SessionEntry`s in order, with `UpdatedAt` through `acpwire.Millis`.
3. **`Memories`** returns `errNoDatabase` without a database. Otherwise it maps `database.GetMemories(s.projectID)` to `MemoryEntry`s in order, with `CreatedAt` through `acpwire.Millis`.
4. **`DeleteMemory`** returns `errNoDatabase` without a database, and `database.DeleteMemory(id)` otherwise.
5. **Tests** (`browsers_test.go`). Build the database as `sheet_test.go:415` does.
   - `TestListSessionsReadsTheDatabase`: two sessions created for one working directory, one with a title, list with their IDs, title and message counts; a session of another directory is not listed.
   - `TestMemoriesListsTheProjectsOwnRows`: a memory saved for the session's project lists with its kind, content, confidence and creation time; a memory saved for another project does not. `DeleteMemory` removes the first, and the next listing is empty.
   - `TestBrowsersWithoutADatabase`: `ListSessions`, `Memories` and `DeleteMemory` each fail with `CodeNoDatabase` (`acpclient.IsCode`).

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go vet ./internal/acpwire/ ./internal/acpclient/...
TMPDIR=/private/tmp/p2-8b-suite go test ./internal/acpwire/ ./internal/acpclient/... -count=1
TMPDIR=/private/tmp/p2-8b-suite go test ./internal/acp/ -run TestACPClientMethodTableMatchesTheServer -count=1
TMPDIR=/private/tmp/p2-8b-suite go test ./internal/app/tui/... -count=1
```

Expected: all pass. `TestWireFieldsFollowTheRules`, `TestMethodTableCoversTheInterfaces` and `TestMethodDocsNameTheirACPMethod` pass with the new declarations.

## Task 2: The seam carries plugins

**Goal:** the plugin types and the five plugin methods exist in the seam and the adapter, which clones, scans, hashes, installs and removes. The TUI uses none of them yet.

**Files:**
- `internal/acpwire/browsers.go`;
- `internal/acpclient/acpclient.go`, `internal/acpclient/methods.go`;
- `internal/acpclient/inproc/inproc.go`, `internal/acpclient/inproc/events.go`, `internal/acpclient/inproc/ending.go`;
- `internal/acpclient/inproc/plugins.go` (new), `internal/acpclient/inproc/plugins_test.go` (new).

**Steps:**

1. **Declarations.** Add the six plugin types, the five methods with their doc comments, and their five `Methods` rows. Add the `Session` field block Contracts gives (all four fields: Task 3 uses the first two), and `pluginScan`.
2. **Paths.** The global store and lockfile are `plugins.GlobalStoreDir(home)` and `plugins.GlobalLockPath(home)`, where `home` is the session's home directory, or `os.UserHomeDir()` when it is empty. The project ones are `plugins.ProjectStoreDir` and `plugins.ProjectLockPath` of the state's working directory.
3. **`Plugins`** lists the global lockfile's entries, then, when `s.state.Trusted()`, the project lockfile's, as the panel's `lockEntries` does, skipping a lockfile that fails to read. It never fails.
4. **`ScanPlugin`** refuses on a closed session with `errSessionClosed`. It then runs the panel's `scanPlugin` steps (`plugins/panel.go:650`) in the same order with the same errors: normalize, validate the name, validate the clone source against the working directory, make the installer, make a temp parent with the pattern `marshal-plugin-tui-*`, clone into `<parent>/clone` with `ctx`, remove `.git`, scan, hash. Any failure deletes the parent. Then, under `mu`:
   - if `ctx` has ended or the session has closed, it deletes the parent and returns `ctx.Err()` or `errSessionClosed`;
   - otherwise it stages the scan under the token `scan-<n>`, with `n` from `pluginSeq`, and returns the token, name, normalized source, commit and contents. Skills map to their name and description, commands to theirs, hooks and MCP servers field by field, and policies as they are.
5. **`InstallPlugin`** refuses a scope other than `global` or `project` with the pinned error, and project scope on an untrusted session with the pinned error, before it takes the scan. It then takes the staged scan out of the map, or fails with the unknown-token error. It installs with `plugins.InstallReviewed` (name, source, commit, the staged hash, the clone, and the scope's store and lockfile), and deletes the parent whatever the outcome.
6. **`DiscardPluginScan`** takes the staged scan, or fails with the unknown-token error, and deletes its parent.
7. **`RemovePlugin`** refuses a name `plugins.ValidName` rejects with `invalid plugin name "<name>"`, then a bad scope and an untrusted project scope as `InstallPlugin` does. It then does what the panel's `runRemove` does (`plugins/panel.go:349`): delete the store directory, read the lockfile, remove the entry and write it.
8. **Closing.** `close` (`events.go:208`) and `Close` (`ending.go:103`) call `discardPluginScans` once the session is marked closed.
9. **Tests** (`plugins_test.go`). Copy `initPluginRepo` from `acp/plugins_test.go:102` with the test constants it needs. Each test uses a temp home and a temp working directory.
   - `TestPluginScanReviewsAndInstalls`: a repository with one command scans with that command in its contents, a token, and the repository's commit; installing it globally lists it through `Plugins` with scope `global`, and its staged parent no longer exists.
   - `TestPluginInstallRejectsAChangedClone`: after a scan, a file in the staged clone (read from `s.pluginScans` in this internal test) is rewritten; the install fails, nothing is listed, and the staged parent is gone. This replaces the panel's `TestConfirmRejectsChangedClone`.
   - `TestPluginScanWithAnEndedContextStagesNothing`: a scan with a cancelled context fails, and no scan is staged.
   - `TestDiscardPluginScanDeletesTheClone`: the parent is gone after a discard, and a second discard fails with the unknown-token error.
   - `TestClosingDiscardsPluginScans`: a staged scan's parent is gone after `Close`.
   - `TestPluginProjectScopeNeedsTrust`: on an untrusted session, `InstallPlugin` and `RemovePlugin` with project scope fail with the pinned error; `Plugins` lists no project entry even when the project lockfile holds one.
   - `TestRemovePluginDropsTheLockEntry`: after an install, `RemovePlugin` deletes the store directory, and `Plugins` lists nothing.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go vet ./internal/acpwire/ ./internal/acpclient/...
TMPDIR=/private/tmp/p2-8b-suite go test ./internal/acpwire/ ./internal/acpclient/... -count=1
TMPDIR=/private/tmp/p2-8b-suite go test ./internal/acp/ -run TestACPClientMethodTableMatchesTheServer -count=1
```

Expected: all pass. Mutation check: make `InstallPlugin` skip `InstallReviewed`'s hash by passing the hash of the clone at install time, and `TestPluginInstallRejectsAChangedClone` fails. Restore it.

## Task 3: The seam carries skills and the skill gate

**Goal:** the skill types, `SessionStatus.SkillGate` and the six skill methods exist in the seam and the adapter, and `Run` wires the skill index. The TUI uses none of them yet.

**Files:**
- `internal/acpwire/browsers.go`, `internal/acpwire/status.go`;
- `internal/acpclient/acpclient.go`, `internal/acpclient/methods.go`;
- `internal/acpclient/inproc/inproc.go`, `internal/acpclient/inproc/events.go`;
- `internal/acpclient/inproc/skills.go` (new), `internal/acpclient/inproc/skills_test.go` (new);
- `internal/app/app.go`, `internal/app/app_test.go`.

**Steps:**

1. **Declarations.** Add the three skill types, the `SkillGate` field, the six methods with their doc comments, their six `Methods` rows, the `SkillIndex` field in `Deps`, and the `skillIndex:` key in `newSession`.
2. **`Status`** (`inproc.go:320`) fills `SkillGate` from `st.SkillGateEnabled()` and `st.SkillGateDecisions()`, mapped in order, with `Decisions` left nil when there are none.
3. **The stores.** The global store is `filepath.Join(config.UserDir(home), "skills")`, with `home` as in Task 2, and the project store `filepath.Join(workingDir, ".marshal", "skills")`, exactly as the panel's `globalSkillsDir` and `projectSkillsDir` resolve them. Never `skills.ScopeDir`.
4. **`Skills`** calls `skills.ListScopes` on both stores and returns its error unchanged. Otherwise it replaces `skillListing` with the result, keyed by scope and source, and returns one `SkillEntry` per entry, in order. `LoadBlocked` is the first of `loadableSelection`'s index and session reasons that applies, with today's text: no index, a name the index lacks, a different indexed value, or an active skill (`HasActiveSkill`).
5. **`LoadSkill`** finds the entry in `skillListing`, or fails with the pinned "no longer in the store" error. It then runs the rest of `loadableSelection` (`skills/panel.go:206`) against that entry, in order and with today's text: re-read the stores, find the same scope and source, check `os.SameFile` and the content, then the index and the active skill. It loads from a one-entry index holding the checked value with `skills.LoadSkillIntoSession`, and returns the name.
6. **`InstallSkill`** refuses a blank source, a project scope on an untrusted session and an unknown scope with the panel's three messages, then returns `skills.Install(ctx, source, store, "")`.
7. **`RemoveSkill`** finds the entry in `skillListing` as `LoadSkill` does, refuses an unknown scope with the panel's message, and returns `skills.RemoveScopedSkill(store, entry)`.
8. **`SetSkillAutoload`** builds the new list from the live config's `Skills.Autoload` as `setAutoload` does (`skills/panel.go:145`), writes it with `config.SaveUserConfigValue` at `skills.autoload` in the user config file (`userConfigPath`, `config.go:26`), and refreshes the layers with `refreshLayers`, as `SaveUserValue` does. It then installs a copy of the live config holding the new list with `s.state.RebaseInteractionConfig`. A write error is returned unchanged, and nothing is installed.
9. **`SetSkillGate`** calls `s.state.SkillGateSetEnabled(on)`, and `ClearSkillGateDecision` calls `s.state.SkillGateClearDecision(skill)`. Each then calls `s.signal()`.
10. **`Run`** sets `Deps.SkillIndex` and stops passing `WithSkillIndex`, as Contracts says. Leave `WithSkillIndex` itself for Task 6.
11. **Tests** (`skills_test.go`). Write skills into the two stores with plain `.md` files, as `skills/panel_test.go`'s `writeSkillEntry` does.
    - `TestSkillsListsBothStoresWithLoadReasons`: with an index holding the global skill, the global entry has no `LoadBlocked`, and a project skill the index lacks reads "the current session has no <name> — restart Marshal to pick it up"; with no index, every entry reads the no-index reason.
    - `TestLoadSkillChecksTheStoreAgainstTheListing`: after a listing, rewriting the skill's file makes `LoadSkill` fail with the "changed on disk" reason; after a fresh listing it loads, and `HasActiveSkill` is true; a second `LoadSkill` fails with the "already active" reason.
    - `TestRemoveSkillUsesTheListedSource`: two entries sharing a frontmatter name in one store; removing one by its source leaves the other's file.
    - `TestInstallSkillRefusals`: a blank source, a project scope on an untrusted session and an unknown scope fail with the three messages.
    - `TestSetSkillAutoloadWritesTheUserConfig`: turning a name on writes it to the user config file and `Config().Skills.Autoload` holds it; turning it off removes it from both.
    - `TestSkillGateIsInTheStatus`: turning the gate off reads as `Enabled` false; turning it on and recording a deny with `SkillGateRecordDeny` lists that decision with `Count` 1; clearing it leaves none. Each call fires `Changed`.
    - `app_test.go`: `TestRunWiresTheSkillIndex`, built like `TestRunWiresToolNames`, with a skill written under `config.UserDir(home)/skills` before `Run`. Inside the program runner, the session's `Skills` lists it with an empty `LoadBlocked`.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go vet ./internal/acpwire/ ./internal/acpclient/... ./internal/app/
TMPDIR=/private/tmp/p2-8b-suite go test ./internal/acpwire/ ./internal/acpclient/... -count=1
TMPDIR=/private/tmp/p2-8b-suite go test ./internal/acp/ -run TestACPClientMethodTableMatchesTheServer -count=1
TMPDIR=/private/tmp/p2-8b-suite go test ./internal/app/ -run 'TestRunWiresTheSkillIndex|TestRunWiresToolNames' -count=1
TMPDIR=/private/tmp/p2-8b-suite go test ./internal/app/tui/... -count=1
```

Expected: all pass. Mutation check: drop `Deps.SkillIndex` from `Run`, and `TestRunWiresTheSkillIndex` fails. Restore it.

## Task 4: The session picker and the memory browser go through the seam

**Goal:** the resume picker lists through `Host.ListSessions`, and the memory browser lists, shows and deletes through the session.

**Files:**
- `internal/app/tui/model.go`, `internal/app/tui/commands_dispatch.go`, `internal/app/tui/keypress.go`;
- `internal/app/tui/memory/panel.go`, `internal/app/tui/memory/entries.go`, `internal/app/tui/memory/messages.go`, and their tests;
- `internal/app/tui/boundary_allowlist_test.go`, and any model test that opens either.

**Steps:**

1. **The picker.** `openSessionPicker` (`model.go:6473`) calls `m.host.ListSessions(m.ctx, st.WorkingDir, 100)`. `CodeNoDatabase` gives today's "not available" note, and any other error today's "Could not list sessions" note. The detail formats `acpwire.Time(s.UpdatedAt).UTC()` with today's layout, so it shows the same UTC time as before. Everything else stays.
2. **The memory panel.**
   - `NewPanel` takes the entries, the load error and the delete function, as Contracts says, and holds `[]acpwire.MemoryEntry`. The panel's filter, cursor, view and armed delete are unchanged; the view's age reads `acpwire.Time(mem.CreatedAt)`.
   - `deleteSelected` calls `del(mem.ID)` where it called the database.
   - `ShowMsg` carries the picked entry.
   - `RenderEntry` formats an entry, with `formatMeta` reading its kind, confidence and creation time. The `db` imports go.
3. **The model.**
   - `openMemory(fromKey bool)` asks `m.sess.Memories(m.ctx)`. On `CodeNoDatabase` it notes today's "not available" sentence unless `fromKey`, and reports false. Otherwise it opens the panel with the entries, the error, and a delete function capturing the session and the context (falling back to `context.Background()` when nil) that calls `DeleteMemory`.
   - `/memory` (`commands_dispatch.go:262`) calls `openMemory(false)` and refreshes the viewport. Ctrl+K (`keypress.go:135`) keeps its readline check, calls `openMemory(true)`, refreshes the viewport only when it opened, and reports the key handled either way.
   - `memory.ShowMsg` (`model.go:1829`) notes `memory.RenderEntry(msg.Entry)`.
   - The three `memoryDB` checks in these paths go. `WithMemoryStore` and its fields stay for plan 9.
4. **Tests.**
   - `memory/panel_test.go` builds panels from entry slices and a recording delete function; `newTestDB` and the `db` import go. `TestRenderEntryFormatsMemory` formats an entry. `TestMemoryPanelLoadErrorShownInFooter` passes a load error.
   - A model test that opened the browser through `WithMemoryStore` gives the adapter `Deps.ProjectID` over a state with a database instead.
   - Add `TestMemoryWithoutADatabase`: over a model whose state has no database, `/memory` notes the "not available" sentence and opens nothing, and Ctrl+K opens nothing and notes nothing.
5. **Allowlist.** The `memory/entries.go` and `memory/panel.go` keys go.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
grep -rn 'memory.NewPanel(m.memoryDB\|database.ListSessions\|state.DB()' internal/app/tui/model.go internal/app/tui/commands_dispatch.go internal/app/tui/keypress.go internal/app/tui/memory
go vet ./internal/app/...
TMPDIR=/private/tmp/p2-8b-suite go test ./internal/app/tui/... -count=1
```

Expected:
- the grep prints nothing;
- the packages pass, `TestTUIStaysOnTheClientSeam` included.

## Task 5: The plugins panel goes through the seam

**Goal:** the plugins panel lists, scans, installs, discards and removes through its `Service`, and holds tokens instead of clones.

**Files:**
- `internal/app/tui/plugins/panel.go`, `internal/app/tui/plugins/scan_lease.go`, and their tests;
- `internal/app/tui/commands_dispatch.go`;
- `internal/app/tui/boundary_allowlist_test.go`.

**Steps:**

1. **The lease.** `scanLease` owns a token instead of a directory, with the same states and rules:
   - `Publish(token)` discards the token, outside the mutex, and reports false when the lease is closed or claimed. Otherwise it keeps the token, discarding one it replaces.
   - `Claim` hands the token over once.
   - `Close` cancels the scan and discards an unclaimed token.

   The `os` import goes.
2. **Construction.** `NewPanel(svc, projectTrusted, note)` replaces both constructors. It reads the listing once with `svc.Plugins(context.Background())`, keeping an empty list on an error, into a new `entries []acpwire.PluginEntry` field. A small `reload` method does that read; the install and remove results call it on success, before the list refreshes. The field builder reads `p.entries` where it called `lockEntries`.
3. **Entries.** `acpwire.PluginEntry` replaces `scopedEntry`: `removalIdentity`, `detailFrame` and the rows read its fields.
4. **Scan.**
   - `runScan` keeps its checks and its generation, creates the lease with a discard function that calls `svc.DiscardPluginScan(context.Background(), token)`, ignoring the error, and returns a command that calls `scanPlugin(ctx, svc, lease, gen, raw, scope)`.
   - `scanPlugin` calls `svc.ScanPlugin(ctx, raw)`. On an error it closes the lease and returns the failed result. Otherwise, when `lease.Publish(scan.Token)` reports false, it returns the result with `context.Canceled`; else the result carries the raw source, the scope and the scan.
   - The result handler keeps every check. A claim stores the token in a new `scanToken` field in place of `scanParent`, and the review fields from the scan; `scannedHash` goes, since the adapter keeps the hash.
   - `invalidateReview` discards `p.scanToken` through the discard function, where it deleted `scanParent`.
5. **Install.** `runInstallFromScan` keeps its checks, testing `scanToken` where it tested `scanParent`. It clears `scanToken` and `scanLease` before returning a command that calls `svc.InstallPlugin(context.Background(), token, scope)`. The comments say the install consumes the token whatever the outcome.
6. **Remove.** `runRemove` returns a command that calls `svc.RemovePlugin(context.Background(), e.Name, e.Scope)`.
7. **Review rows.** `hookReviewLines` takes an `acpwire.PluginHook` and `mcpReviewLines` an `acpwire.PluginMCPServer`, reading the same fields. The `plugins`, `os` and `path/filepath` imports go, and `config` too if nothing else uses it.
8. **The model.** `/plugins` (`commands_dispatch.go:277`) opens `plugins.NewPanel(sess, m.sess.Status().Trusted, note)` with the session it already captures.
9. **Tests.**
   - Add a `fakeService` in `panel_test.go`: `Plugins` returns a settable list; `ScanPlugin` runs a per-test function (where tests injected delays and failures through `scanDeps`); `InstallPlugin`, `DiscardPluginScan` and `RemovePlugin` record their tokens and names and return settable errors. Every `newPanelWithScanDeps(…)` and `NewPanel(home, work, …)` call builds the panel over a fake.
   - Tests that asserted a temp clone was deleted assert on the fake instead: `TestAbandonedScanRemovesTempClone` (renamed `TestAbandonedScanDiscardsItsToken`), `TestAbandonedScanCannotAttachToReopenedFrame`, `TestPanelReplacementCleansUndeliveredScan` and `TestInstallOwnsCloneAfterConfirm` (renamed `TestInstallConsumesTheToken`): each token the fake staged is either installed once or discarded once, never both.
   - `TestConfirmRejectsChangedClone` goes: Task 2's `TestPluginInstallRejectsAChangedClone` replaces it.
   - The review tests feed `acpwire.PluginScan` contents where they built `plugins.Contents`.
   - `scan_lease_test.go`: `TestScanLeaseClosePublishClaimRaces` races publish, claim and close over tokens with a recording discard, and asserts each published token is claimed or discarded exactly once.
10. **Allowlist.** The `plugins/panel.go` key goes.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
grep -rn 'marshal/internal/plugins\|os.RemoveAll\|os.MkdirTemp' internal/app/tui --include='*.go' | grep -v '_test.go'
go vet ./internal/app/...
TMPDIR=/private/tmp/p2-8b-suite go test ./internal/app/tui/... -count=1 -race -run 'Plugin|Scan|Install|Remove|Review' 
TMPDIR=/private/tmp/p2-8b-suite go test ./internal/app/tui/... -count=1
```

Expected:
- the grep prints nothing;
- the packages pass, under `-race` for the plugins tests, `TestTUIStaysOnTheClientSeam` included.

Mutation check: make `scanLease.Close` skip the discard, and `TestAbandonedScanDiscardsItsToken` fails. Restore it.

## Task 6: The skills panel goes through the seam

**Goal:** the skills panel lists, loads, installs, removes, sets autoload and drives the skill gate through its `Service`, and the TUI holds no skill index.

**Files:**
- `internal/app/tui/skills/panel.go`, `internal/app/tui/skills/panel_test.go`;
- `internal/app/tui/model.go`, `internal/app/tui/commands_dispatch.go`;
- `internal/app/tui/boundary_allowlist_test.go`, `internal/app/tui/moved_reads_test.go`, and any test that passes `WithSkillIndex`.

**Steps:**

1. **Construction.** `NewPanel(svc, projectTrusted)` keeps `svc` and reads the listing with `svc.Skills(context.Background())` into a new `entries []acpwire.SkillEntry` field, keeping an empty list on an error. A `reload` method does that read; the load, install and remove results call it on success, before the list refreshes.
2. **Rows.**
   - `buildFields` reads `p.entries`. The gate row always renders: `svc.Status().SkillGate.Enabled` for on and off, and `svc.Config().Skills.LoadGateThresholdTokens` for the threshold, with today's three descriptions. Its action calls `svc.SetSkillGate(ctx, !enabled)`, puts an error in `ErrMsg`, and otherwise sets today's status from the new value.
   - The decision rows read `svc.Status().SkillGate.Decisions`, with today's titles. Their action calls `svc.ClearSkillGateDecision` with today's status, or the error in `ErrMsg`.
   - `isAutoloaded` reads `svc.Config().Skills.Autoload`. `setAutoload` calls `svc.SetSkillAutoload`, shows `save autoload: <err>` in `ErrMsg` on an error, and sets today's two status lines otherwise.
3. **The detail frame** takes an `acpwire.SkillEntry`. Its rows read the entry's name, description and scope. The load row reads `LoadBlocked`: unavailable with `"unavailable — " + reason` and today's label when set, today's available description and label when not. Its action returns a command calling `svc.LoadSkill(context.Background(), e.Scope, e.Source)`. Remove returns one calling `svc.RemoveSkill` with the same identity. `removalIdentity` and `skillRowID` read the entry's scope and source.
4. **Install.** `runInstall` keeps its three checks and messages, and returns a command calling `svc.InstallSkill(context.Background(), source, scope)`.
5. **Deletions.** Delete the fields and functions Contracts lists for the skills panel. The `skills`, `app/session`, `os` and `path/filepath` imports go, and with them the uses of `config.UserDir`, `config.UserConfigPath` and `config.SaveUserConfigValue`.
6. **The model.** `/skills` (`commands_dispatch.go:272`) opens `skills.NewPanel(m.sess, m.sess.Status().Trusted)`. Delete `WithSkillIndex` and the `skillIndex` field (`model.go:234`, `:652`); `model.go`'s `skills` import goes.
7. **Tests.**
   - Add a `testPanel(t, home, work string, trusted bool, idx *skills.Index) (p *Panel, state *session.State, index **skills.Index)` helper in `panel_test.go`. It builds the state over `work` with `SetTrusted(trusted)`, an adapter with `HomeDir: home` and a `SkillIndex` that returns `*index`, which starts as `idx`, and the panel over the adapter's session. Every `NewPanel(home, work, trusted, state, idx)` call uses it.
   - Helpers that returned a `skills.ScopedSkill` from the panel (`discovered` and its callers) return the `acpwire.SkillEntry` from `p.entries`, and assertions on `.Skill.Name` read `.Name`.
   - A test that assigned `p.skillIndex` (the load tests' `newLoadPanel` and its callers, from `skills/panel_test.go:1195`) assigns `*index` instead, then calls `p.reload()` so the listing's load reasons follow.
   - Tests that edited the state's config or gate directly keep doing so through the returned state, and read the panel's rows after the panel's next refresh.
   - Tests that passed a nil state for a "stateless" panel use the helper with no index.
8. **Guards.**
   - `movedStateReads` gains `SkillGateEnabled`, `SkillGateSetEnabled`, `SkillGateDecisions`, `SkillGateClearDecision` and `HasActiveSkill` at plan `"8b"`, as their own commented block directly after `"Config": "7b",`. The comment above `"Config"` drops the skills-panel holdout sentence.
   - `deferredStateReads` loses its `skills/panel.go` entry, leaving the map empty.
   - The allowlist loses the `skills/panel.go` key, and `model.go` loses `marshal/internal/skills`.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
grep -rn 'WithSkillIndex\|skillIndex\|marshal/internal/skills"' internal/app/tui internal/app/app.go --include='*.go' | grep -v '_test.go'
go vet ./internal/app/...
TMPDIR=/private/tmp/p2-8b-suite go test ./internal/app/tui/... -count=1
TMPDIR=/private/tmp/p2-8b-suite go test ./internal/app/ -count=1
```

Expected:
- the grep prints nothing;
- the packages pass, `TestTUIStaysOnTheClientSeam` and `TestMovedStateReadsStayMoved` included; the latter's deferred-entry check now walks an empty map.

Mutation check: make the adapter's `LoadSkill` skip the active-skill check, and the "already-active name keeps its body" case of `TestLoadNowUsesMatchingSessionContent` fails. Restore it.

## Task 7: Verification and docs

**Goal:** the full suite is green, and the spec records plan 8b.

**Files:** `docs/daemon/p2-client-seam.md`.

**Steps:**

1. **`p2-client-seam.md`.** Add a plan 8b entry to the Implementation record, after plan 8a's if it is there, otherwise after plan 7d's. It lists:
   - the wire types, `CodeNoDatabase` and `SessionStatus.SkillGate`;
   - the fifteen methods and their `Methods` rows: eleven on existing ACP methods, four new;
   - `Deps.SkillIndex`, and what the adapter now keeps per session (the skill listing and the staged plugin scans);
   - what each panel now asks for, and what was deleted;
   - the allowlist's 9 entries across 5 files and 4 keys entirely, T1b's five names, and the empty `deferredStateReads`.
2. **Main may have moved.** If plan 8a merged while this branch ran, merge `main` into the branch before the full verification. Expect conflicts only in the files this plan's header names. Keep both sides: each plan's methods, rows, fields, allowlist changes and record entries.
3. **Full verification.**

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
gofmt -l ./internal ./cmd ./web
go vet ./...
TMPDIR=/private/tmp/p2-8b-suite CGO_ENABLED=1 go build ./cmd/marshal
TMPDIR=/private/tmp/p2-8b-suite go test ./... -timeout 20m 2>&1 | grep -E '^(--- FAIL|FAIL|ok)' | grep -v '^ok'
git diff --stat main..HEAD -- internal/app/tui/testdata internal/app/tui/livebox/testdata
```

Expected:
- `gofmt -l` prints nothing, and `go vet` is clean;
- the binary builds;
- the test line prints nothing, or only the known failures (Assumptions). Treat any other failure as new unless it also fails at `6c6d16b2`, or it is one of the load flakes below and passes on its own with `-count=3`;
- the `git diff` prints nothing.

**Baseline.** Earlier full runs saw these fail under load and pass alone:
- `TestStackPatchApplyBudget` and `TestStackRoundTripBudget` (`internal/acp`, at `6c6d16b2`);
- `TestRuntimeAccessSerializesWithReloadAndQuiesce` and `TestRuntimeCloseRunsNoGitGC` (`internal/app`);
- `TestRunManagedCancellationKillsTree` and `TestIntegrationOwnershipReleaseDrainsWritersBeforeUnlock` (`internal/snapshot`);
- `TestACPChildApprovalUsesOwningSessionRegistry` and `TestACPSendNowCancelsPromptRPCWithoutCancelingTurn` (`internal/acp`).

Finally, check by eye with `./marshal`:
- `/resume` lists earlier sessions with their times and message counts;
- Ctrl+K opens the memory browser; Enter shows a memory, and Ctrl+D twice deletes one;
- `/plugins`: install a small plugin from a local git repository (scan, read an executable row, confirm), then remove it;
- `/skills`: install a skill, toggle its autoload (it appears under `[skills] autoload` in the user config), "Load now" an indexed skill, turn the skill load gate off and on, and remove the skill.

## Integration notes

- **Plan 8a** runs alongside. See the header for the shared files.
- **Plan 9** deletes `WithMemoryStore` with `memoryDB` and `memoryProject`, `WithLegacyState` and `WithLegacySession`, and T1b.
- **P3:**
  - the four new methods need handlers;
  - ACP's memory, plugin and skill handlers differ from the adapter's in the ways R9 now lists, and move onto the adapter's logic;
  - `session/list` results need converting: RFC 3339 times, and the message count in `_meta`.

## Self-review

- **Every task verifiable alone.** Tasks 1 to 3 add the seam by group, which nothing uses yet. Tasks 4 to 6 each move one surface, and Task 3's app test proves the skill index's wiring end to end.
- **Cold executor.** Each step names the function, the call it makes after the change, and what stays. The plugin scan's cleanup rules are stated for the lease, the adapter and the panel separately.
- **Contracts.** The wire types, the code, the status field, the methods and their rows, the `Deps` and `Session` fields, the panels' interfaces and constructors, and every new string are declared there.
- **Anchors.** Every path, symbol and line was checked at `6c6d16b2`.
- **Spec.** C2, C10, C11 and the Migration order are updated, with the revision record "2026-11-06, against `6c6d16b2` (plans 7c and 7d merged)". P3's R9 list carries the ACP differences.
- **No placeholders.**
