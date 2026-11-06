# P2 plan 8a: History, index facts and plan runs — implementation plan

Date: 2026-11-06. Status: ready for review.
Base: `main` at `6c6d16b2` (plans 7c and 7d merged). Every anchor below was checked there.
- **This plan runs in parallel with plan 8b** (decided with the user, 2026-11-06). The two touch different TUI files. In the files both edit, each inserts at its own anchor, named in its steps:
  - `acpclient/acpclient.go` and `acpclient/methods.go`: this plan appends to the end of `Session` and of `Methods`; 8b inserts after `DecideTrust` and after `CommandAction`;
  - `inproc/inproc.go` and `inproc/events.go`: this plan adds `Deps` fields after `UserConfigPath`, `Session` fields after `modelCacheDir`, and `newSession` keys after `modelCacheDir:`; 8b adds after `Tools`, `tools` and `tools:`;
  - `tui/model.go` and `tui/commands_dispatch.go`: different functions. The one likely conflict is `model.go`'s import block, where this plan drops `sddplans` and 8b drops the `skills` line next to it. Keep both removals;
  - `tui/boundary_allowlist_test.go` and `tui/moved_reads_test.go`: different keys and entries;
  - `docs/daemon/p2-client-seam.md`: both entries in its revision record are already written. Only the Implementation record can conflict, and the second plan to merge keeps both entries.

Approved design: [p2-client-seam.md](../p2-client-seam.md): C8 (the watch resume's prompt), C11 (project data), Migration order item 8a, its revision record entry "2026-11-06, against `6c6d16b2` (plans 7c and 7d merged)", and tests T1, T1b, T4 and T5. P3's R9 list ([p3-acp-transport.md](../p3-acp-transport.md)) records the ACP differences this plan finds.
Previous plans: [plan 7c, trust and tools](2026-11-05-p2-7c-trust-tools-plan.md) and [plan 7d, secrets](2026-11-05-p2-7d-secrets-plan.md).
Authoring workflow: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md).

**How this plan is written.** No prototype was built, and the plan embeds no implementation code. The Contracts section holds declarations and the exact strings the tests pin. Every other step states its decision in prose. Executing the plan also tests how well Marshal follows a plan. "As `X` does" means `X` exists and is the model to copy.

**Goal:** the TUI reads history, the file index, the doctor's checks and plan files, and runs the plan outcome's reads, only through the seam.
- `acpwire` gains the history, index and plan types, and `PromptParams.WatchResume`.
- `acpclient.Session` gains ten methods: `RecentPrompts`, `SavePrompt`, `Messages`, `FileIndex`, `Intelligence`, `Plans`, `DiscardCandidate`, `ScaffoldPlan`, `Diff` and `ReadFile`.
- The TUI stops importing `sddplans`, `sddauthor`, `watch`, `worktree`, `pubsub` and `tools/native`, and stops reading `state.Messages()`.
- Dead code found on the way goes: the todo row helpers, two unused state fields and two unused broker fields.

**Non-goals:**
- **No session picker, memory, plugins or skills.** Plan 8b moves them.
- **No deletion of `WithMemoryStore`, `memoryDB` or `memoryProject`.** Plan 8b's panels still read them on its branch, so plan 9 deletes them.
- **No change to what users see.**
- **No ACP behaviour change.** No changelog entry.

**Assumptions:**
- Every Go test command below runs with `TMPDIR=/private/tmp/p2-8a-suite`. Create the directory first: `mkdir -p /private/tmp/p2-8a-suite`. Keep the path short: a long one breaks the unix-socket tests in `internal/acp` and `web/bridge`. Plan 8b uses `/private/tmp/p2-8b-suite`. Never share a directory between the two runs.
- **The base.** The full suite ran at `6c6d16b2` while this plan was written: 117 packages passed. `internal/acp`'s `TestStackPatchApplyBudget` and `TestStackRoundTripBudget` failed under the suite's load and pass alone with `-count=3` (medians 1.0ms of 2ms and 34ms of 50ms). `web/bridge`'s `TestRecipeRunMaxUSDSetsAPausingCapForTheRun` has hung about one run in three since `90488446`. The hang is in its fake agent (`fakeagent_test.go`), not in product code, so treat a hang there as known.
- **Two suites may run at once.** Load makes the flakes listed under Task 7 likelier. Rerun a failure alone before treating it as new.
- **The reads this plan moves, today:**
  - **Input history.** `New` loads `database.RecentPrompts(m.memoryProject, promptHistoryLimit)` from `state.DB()` when history is enabled (`model.go:1242`). `recordPrompt` saves with `database.SavePrompt(m.memoryProject, value, time.Now())`, ignoring the error (`history.go:24`). Both need `WithMemoryStore`'s project ID.
  - **Messages.** Five scans of `m.state.Messages()`: `lastAssistantText` (`model.go:4055`, only for the watch resume's goal at `:4014`), `sessionHasUserTurn` (`:4810`), `computeSuggestion`'s last assistant reply (`:5056`), `rewindPickerItems`' user turns (`:7011`) and `startNewSession`'s cleared count (`commands_dispatch.go:178`). A sixth, `lastFinalAssistantPlan` (`sdd_command.go:85`), takes the state itself and serves `/sdd new --from-last-plan` (`model.go:4090`).
  - **The watch resume.** `startWatchResumeRun` builds the goal with `watch.ResumeGoal(name, m.lastAssistantText(), repeat)` and passes it to `startPrompt` (`model.go:4010`).
  - **The file index.** `Run` seeds it with `tui.WithFileIndex(loadFileIndexPaths(database, projectID))` (`app.go:2784`, `:3245`). Without a seed, the first `@` calls `populateFileIndexIfNeeded` (`model.go:3455`), which reads `m.memoryDB.GetFileIndex`.
  - **The doctor.** `/doctor` calls `doctorpanel.ComputeIntelligence(cfg, m.memoryDB, m.memoryProject)` and opens `doctorpanel.New(m.state, diags, checks)` (`commands_dispatch.go:308`). The panel never reads its `state` field.
  - **Plans.** `sddplans.Discover` runs in `openRunPreflight` (`model.go:3809`, the task count) and `openSDDPlanPicker` (`:6665`). `sddauthor.RemoveCandidate` runs on the review panel's `DiscardMsg` (`:2253`). `scaffoldSDDPlan` writes the starter plan with `os` calls (`sdd_scaffold.go:66`).
  - **The run outcome.** `/run` builds `runOutcomeDoc` from `Status().SDD` and passes `runOutcomeActions(st.SDD, worktree.CLIGitOps{}, st.WorkingDir, m.width)` (`commands_dispatch.go:430`). The diff action runs `git diff -U3 base..branch` in the working directory, and the ledger action reads the ledger with `os.ReadFile` (`runoutcome.go:195`, `:212`). Both actions run inside a `tea.Cmd` (`localDocActions`, `docpanels.go:21`).
- **The ledger** lives at `<working dir>/.marshal/pipeline/<slug>/progress.md` (`pipeline/paths.go:19`, `:59`); the pipeline's repo root is the state's `WorkingDir` (`app.go:1876`).
- **Dead code:**
  - `todoLine`, `todoProgress` and `todosAllDone` (`todos.go:31` to `:70`) are called only from `todos_test.go`;
  - `tasksPanelModel.state` (`todos.go:122`) and `doctorpanel.Panel.state` are set and never read;
  - the model's `workspaceBroker`, `workspaceEvents`, `subagentBroker` and `subagentEvents` fields (`model.go:343` to `:352`) are never read or set since plan 2b.
- **`Update` has a value receiver** (`model.go:1459`). A closure that runs later captures the session and the context values, never `m`. A synchronous call passes `m.ctx`, as `PlanPreflight`'s call does (`commands_dispatch.go:419`); a closure falls back to `context.Background()` when the captured context is nil, as `extractPostmortemCmd` does (`model.go:4829`).

**Decisions this plan makes beyond the spec** (all are recorded in the spec's revision record):

1. **One message query.** `Messages` selects by role, by finality and by the newest N, and its page counts every match before the cut. The six readers become six queries, and `/new`'s count reads a page's `Total`. It is a call, cheap enough to stay synchronous in `Update` (P3 R6).
2. **The watch resume's goal is written agent side.** `PromptParams.WatchResume` names the watch, and the session writes the goal from its own last assistant reply with `watch.ResumeGoal`, ignoring `Text`. The TUI's `lastAssistantText` goes.
3. **The file index keeps its startup seed.** `Run` reads it through the session it just built, and the first `@` still asks the session when no seed arrived.
4. **The doctor's checks move into the adapter unchanged**, strings included. The language-server probe runs on the agent's `PATH`.
5. **The starter plan moves to `sddplans`**, as `Scaffold`, beside `Discover`, with its parse and compile tests.
6. **The run outcome's reads take `Diff` and `ReadFile`.** `ReadFile` takes a path relative to the working directory, as `session/file` does, so the TUI makes the ledger path relative.
7. **The allowlist loses 12 entries across 7 files, and 6 keys go entirely:**
   - `commands_dispatch.go` (`worktree`), `runoutcome.go` (`worktree`), `sdd_command.go` (`app/session`) and `doctorpanel/doctorpanel.go` (`app/session`) lose their only entry;
   - `doctorpanel/intelligence.go` (`db`, `lsp`) goes with the file;
   - `todos.go` (`app/session`, `tools/native`) loses both;
   - `model.go` loses `pubsub`, `sddauthor`, `sddplans` and `watch`.

   T1b gains `Messages`. `DB` gets no entry: plan 8b moves its other reader, and plan 9 deletes T1b.
8. **The new ACP methods are named** `session/prompt_history`, `session/prompt_history_save`, `session/messages`, `session/file_index`, `session/intelligence`, `session/plans`, `session/sdd_discard` and `session/sdd_scaffold`. `Diff` and `ReadFile` name `session/diff` and `session/file`, which exist.

**Execution:**
- Branch first: `git switch -c p2-8a-history` from `main`.
- If `git status` shows `docs/daemon/p2-client-seam.md`, `docs/daemon/p3-acp-transport.md` or this plan uncommitted, commit them first as explicit paths, with the message `p2-8a-history: record the plan 8a and 8b revisions and plan 8a`. If plan 8b's run committed the specs already, commit only this plan. Plan 8b's plan file may also be uncommitted: leave it to plan 8b. The main checkout holds other untracked files (`scripts/lmstudio-bench/` and others). Leave them alone.
- Run the tasks in order. Run each task's Verify and commit it as `p2-8a-history: task N — <title>`.
- Stage explicit paths only. Never `git add -A` without a path.
- Run `gofmt -w` on every Go file a task touches before its Verify.

## Contracts

### Wire types

`internal/acpwire/history.go` (new):

```go
// MessageQuery selects the transcript messages Session.Messages returns (C11,
// plan 8a). The zero value matches every message.
type MessageQuery struct {
	// Role keeps only messages of one role, RoleUser or RoleAssistant. Empty
	// matches every role.
	Role string `json:"role,omitempty"`
	// Final keeps only final answers whose text is not blank.
	Final bool `json:"final,omitempty"`
	// Last keeps only the newest Last matches. Zero keeps them all.
	Last int `json:"last,omitempty"`
}

// MessagePage is what Session.Messages returns: the kept messages, oldest
// first, and how many messages matched before Last cut them.
type MessagePage struct {
	Messages []HistoryMessage `json:"messages,omitempty"`
	Total    int              `json:"total,omitempty"`
}

// HistoryMessage is one transcript message as a query returns it.
type HistoryMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	Final   bool   `json:"final,omitempty"`
}
```

`internal/acpwire/project.go` (new):

```go
// IntelligenceCheck is one line of the doctor's Intelligence section (C11,
// plan 8a). Status is "ok", "warn" or "off".
type IntelligenceCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// PlanCandidate is one plan an SDD run can execute, as plan discovery found
// it. Error says why the plan does not parse, LedgerError why its progress
// ledger could not be read, and Resumable that a previous run left it partly
// done.
type PlanCandidate struct {
	Path        string `json:"path"`
	Name        string `json:"name"`
	Slug        string `json:"slug,omitempty"`
	Tasks       int    `json:"tasks,omitempty"`
	Done        int    `json:"done,omitempty"`
	Error       string `json:"error,omitempty"`
	LedgerError string `json:"ledgerError,omitempty"`
	Resumable   bool   `json:"resumable,omitempty"`
}

// ScaffoldResult is where ScaffoldPlan's starter plan is. Existed says a file
// was already there, and nothing was written.
type ScaffoldResult struct {
	Path    string `json:"path"`
	Existed bool   `json:"existed,omitempty"`
}

// DiffQuery names the range Session.Diff renders, Base..Head.
type DiffQuery struct {
	Base string `json:"base"`
	Head string `json:"head"`
}

// FileContent is one file Session.ReadFile read, as session/file returns it.
// Content is empty for a binary file, and cut at 1 MiB when Truncated.
type FileContent struct {
	Path      string `json:"path"`
	Size      int64  `json:"size,omitempty"`
	Binary    bool   `json:"binary,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
	Content   string `json:"content,omitempty"`
}
```

`PromptParams` (`internal/acpwire/turns.go:4`) gains, after `Queued`:

```go
	// WatchResume, when set, names the watch whose resume this turn is. The
	// session writes the turn's text itself, from the watch and its own last
	// assistant reply, and ignores Text.
	WatchResume *WatchResume `json:"watchResume,omitempty"`
```

### Interface (`internal/acpclient`)

`Session` gains, at its end, after `SaveVerifyCommands`:

```go
	// RecentPrompts returns the project's input history, newest first, at
	// most limit entries. A session without a database or a project returns
	// none.
	//
	// ACP: session/prompt_history (new in P3).
	RecentPrompts(ctx context.Context, limit int) ([]string, error)

	// SavePrompt adds text to the project's input history. A session without
	// a database or a project saves nothing and reports no error.
	//
	// ACP: session/prompt_history_save (new in P3).
	SavePrompt(ctx context.Context, text string) error

	// Messages returns the session's transcript messages that query selects.
	// A closed session keeps answering from its final transcript.
	//
	// ACP: session/messages (new in P3).
	Messages(ctx context.Context, query acpwire.MessageQuery) (acpwire.MessagePage, error)

	// FileIndex returns the repository's indexed file paths, for the @
	// completion popup. A session without an index returns none.
	//
	// ACP: session/file_index (new in P3).
	FileIndex(ctx context.Context) ([]string, error)

	// Intelligence returns the doctor's intelligence checks: the index, the
	// embeddings, the language servers and the index watcher.
	//
	// ACP: session/intelligence (new in P3).
	Intelligence(ctx context.Context) ([]acpwire.IntelligenceCheck, error)

	// Plans lists the plans an SDD run can execute, from the configured plans
	// directory. A plan that does not parse is listed with its error.
	//
	// ACP: session/plans (new in P3).
	Plans(ctx context.Context) ([]acpwire.PlanCandidate, error)

	// DiscardCandidate removes one authored plan candidate. It refuses a path
	// that is not a regular file directly inside the plans directory.
	//
	// ACP: session/sdd_discard (new in P3).
	DiscardCandidate(ctx context.Context, path string) error

	// ScaffoldPlan writes the runnable starter plan into the plans directory.
	// It never overwrites a file that is already there.
	//
	// ACP: session/sdd_scaffold (new in P3).
	ScaffoldPlan(ctx context.Context) (acpwire.ScaffoldResult, error)

	// Diff returns the unified diff of query's range in the session's working
	// directory, with three lines of context. It never changes the
	// repository.
	//
	// ACP: session/diff (exists). P3 gives it an optional head.
	Diff(ctx context.Context, query acpwire.DiffQuery) (string, error)

	// ReadFile reads one regular file, named relative to the session's
	// working directory. It refuses a path that leaves the directory.
	//
	// ACP: session/file (exists).
	ReadFile(ctx context.Context, path string) (acpwire.FileContent, error)
```

`Methods` gains ten rows at its end, after the `Session`/`SaveVerifyCommands` row, after one blank line:

| Iface | Name | ACP | Exists |
|---|---|---|---|
| Session | RecentPrompts | session/prompt_history | false |
| Session | SavePrompt | session/prompt_history_save | false |
| Session | Messages | session/messages | false |
| Session | FileIndex | session/file_index | false |
| Session | Intelligence | session/intelligence | false |
| Session | Plans | session/plans | false |
| Session | DiscardCandidate | session/sdd_discard | false |
| Session | ScaffoldPlan | session/sdd_scaffold | false |
| Session | Diff | session/diff | true |
| Session | ReadFile | session/file | true |

### Adapter (`internal/acpclient/inproc`)

`Deps` gains, at its end, after `UserConfigPath`:

```go
	// FileIndex reads the repository's indexed file paths. Nil means the
	// session has no index: FileIndex returns none.
	FileIndex func() ([]string, error)
	// Git runs Diff. Nil means worktree.CLIGitOps{}.
	Git worktree.GitOps
```

`Session` gains, after the `modelCacheDir` field, as its own commented block:

```go
	// fileIndex is Deps.FileIndex, and git is Deps.Git or
	// worktree.CLIGitOps{}, never nil.
	fileIndex func() ([]string, error)
	git       worktree.GitOps
```

New files: `history.go` (`RecentPrompts`, `SavePrompt`, `Messages`, and the unexported `lastAssistantText`), `project.go` (`FileIndex`, `Intelligence`, `Plans`, `DiscardCandidate`, `ScaffoldPlan`, `Diff`, `ReadFile`) and `intelligence.go` (the checks), each with a `_test.go`.

- `func intelligenceChecks(cfg config.Config, database *db.DB, projectID int64) []acpwire.IntelligenceCheck` in `intelligence.go`: `doctorpanel.ComputeIntelligence`'s four checks, with their helpers and every string unchanged.
- `func (s *Session) lastAssistantText() string` in `history.go`: the content of the state's last `RoleAssistant` message, or "".

### `internal/sddplans`

New file `scaffold.go`:

```go
// StarterPlanName is the starter plan's file name. It is fixed, so the
// "already exists" guard means something.
const StarterPlanName = "starter-plan.md"

// StarterPlan is the runnable template Scaffold writes.
const StarterPlan = `...` // tui's sddStarterPlan, moved unchanged

// Scaffold writes StarterPlan to plansDir (relative to repoRoot), creating the
// directory. existed is true, and nothing is written, when the file is already
// there. Its errors read "create <dir>: <err>" and "write <path>: <err>".
func Scaffold(repoRoot, plansDir string) (path string, existed bool, err error)
```

### App (`internal/app`)

- `Run` (`app.go:2735`) sets `Deps.FileIndex` to a function returning `loadFileIndexPaths(database, projectID)`.
- The eager seed (`app.go:2784`) reads `seamSession.FileIndex(ctx)` instead of calling `loadFileIndexPaths` itself, and keeps its condition: pass `tui.WithFileIndex` only on no error and a non-empty list.

### TUI

- `doctorpanel.New(diags []config.Diagnostic, checks []acpwire.IntelligenceCheck) *Panel`. `doctorpanel.Check`, `ComputeIntelligence` and `intelligence.go` are deleted.
- `runOutcomeActions(ctx context.Context, sess acpclient.Session, p acpwire.SDDProgress, workingDir string, width int) map[string]func() acpwire.CommandResult`.
- `branchDiff(ctx context.Context, sess acpclient.Session, base, branch string, width int) string`.
- `readLedger(ctx context.Context, sess acpclient.Session, workingDir, ledgerPath string) string` replaces `readFileOrExplain`.
- **Deleted:** `lastAssistantText`, `lastFinalAssistantPlan`, `sddStarterPlan` and `sddStarterPlanName` (moved to `sddplans`), `readFileOrExplain`, `todoLine`, `todoProgress`, `todosAllDone`, `tasksPanelModel.state`, `doctorpanel.Panel.state`, and the four broker fields.

### Pinned strings

- `Diff` with an empty base or head: `inproc: a diff needs a base and a head`.
- `ReadFile` on something that is not a regular file: `inproc: <path> is not a regular file`.
- The ledger action, for a ledger path outside the working directory: `Could not read <ledger path>: it is outside the working directory`.
- The ledger action, for a binary file: `<ledger path> is not a text file.`
- Every other TUI note and doc text keeps today's wording: `Could not read %s: %v`, `%s is empty.`, `Could not read the diff for %s..%s: %v`, `No changes between %s and %s.`, the scaffold's three notes, and the doctor's check details.

## Task 1: The seam carries history

**Goal:** the history types, `PromptParams.WatchResume` and the three history methods exist in the seam and the adapter. The TUI uses none of them yet.

**Files:**
- `internal/acpwire/history.go` (new), `internal/acpwire/turns.go`;
- `internal/acpclient/acpclient.go`, `internal/acpclient/methods.go`;
- `internal/acpclient/inproc/history.go` (new), `internal/acpclient/inproc/history_test.go` (new), `internal/acpclient/inproc/turns.go`, `internal/acpclient/inproc/turns_test.go`.

**Steps:**

1. **Declarations.** Add the three history types, `PromptParams.WatchResume`, the `RecentPrompts`, `SavePrompt` and `Messages` methods with their doc comments, and their three `Methods` rows, at the anchors Contracts names.
2. **`RecentPrompts`** returns `nil, nil` when `s.state.DB()` is nil or `s.projectID` is 0. Otherwise it returns `database.RecentPrompts(s.projectID, limit)`.
3. **`SavePrompt`** returns nil, saving nothing, in the same two cases. Otherwise it returns `database.SavePrompt(s.projectID, text, s.state.Now())`.
4. **`Messages`** walks `s.state.Messages()`, oldest first.
   - A message matches when `query.Role` is empty or equals its role, and, with `query.Final` set, when it is final and its content is not blank after `strings.TrimSpace`.
   - `Total` is the number of matches. `Messages` holds the last `query.Last` matches, or all of them when `Last` is 0 or more than `Total`, oldest first, as `HistoryMessage`s with the role as a string.
   - It never fails, and a closed session keeps answering.
5. **`lastAssistantText`**, as Contracts says.
6. **`Prompt`** (`inproc/turns.go:41`). Before it starts the turn, it takes the turn's text from `params.Text`, or, when `params.WatchResume` is set, from `watch.ResumeGoal(name, s.lastAssistantText(), repeat)` with the watch's name and repeat flag. The runner's `Run` gets that text. Nothing else in `Prompt` changes, so a refused start still re-queues by `params.Queued` alone.
7. **Tests** (`history_test.go`). Build the database as `sheet_test.go:415` does, with a project from `GetOrCreateProject`.
   - `TestPromptHistoryRoundTrips`: two `SavePrompt` calls, then `RecentPrompts(ctx, 1)` returns the second text alone, and `RecentPrompts(ctx, 10)` returns both, newest first.
   - `TestPromptHistoryWithoutADatabase`: a session with no database, and one with a database but `ProjectID` 0, each return `nil, nil` from `RecentPrompts` and nil from `SavePrompt`.
   - `TestMessagesSelectsByRoleFinalityAndLast`: a state holding, in order, user "one", assistant "draft" (not final), assistant "answer" (final, through `AddMessageFinal`), user "two", and assistant "   " (final, blank). Then:
     - `{Role: user}` returns "one" and "two", with `Total` 2;
     - `{Role: assistant, Last: 1}` returns the blank final message, with `Total` 3;
     - `{Role: assistant, Final: true, Last: 1}` returns "answer", with `Final` true and `Total` 1;
     - `{Last: 1}` returns one message, and `Total` counts every message in the state.
8. **Test** (`turns_test.go`). `TestPromptWritesTheWatchResumeGoal`, with `newFakeRunner`: after an assistant reply "I fixed the build.", a `Prompt` with `WatchResume: &acpwire.WatchResume{Name: "ci", Repeat: true}` and `Text: "ignored"` hands the runner exactly `watch.ResumeGoal("ci", "I fixed the build.", true)`.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go vet ./internal/acpwire/ ./internal/acpclient/...
TMPDIR=/private/tmp/p2-8a-suite go test ./internal/acpwire/ ./internal/acpclient/... -count=1
TMPDIR=/private/tmp/p2-8a-suite go test ./internal/acp/ -run TestACPClientMethodTableMatchesTheServer -count=1
TMPDIR=/private/tmp/p2-8a-suite go test ./internal/app/tui/... -count=1
```

Expected: all pass. `TestWireFieldsFollowTheRules`, `TestMethodTableCoversTheInterfaces` and `TestMethodDocsNameTheirACPMethod` pass with the new declarations.

## Task 2: The seam carries index facts and plan runs

**Goal:** the project types and the other seven methods exist in the seam and the adapter, `sddplans.Scaffold` exists, and `Run` wires the file index. The TUI uses none of them yet.

**Files:**
- `internal/acpwire/project.go` (new);
- `internal/acpclient/acpclient.go`, `internal/acpclient/methods.go`;
- `internal/acpclient/inproc/inproc.go`, `internal/acpclient/inproc/events.go`;
- `internal/acpclient/inproc/project.go` (new), `internal/acpclient/inproc/project_test.go` (new), `internal/acpclient/inproc/intelligence.go` (new), `internal/acpclient/inproc/intelligence_test.go` (new);
- `internal/sddplans/scaffold.go` (new), `internal/sddplans/scaffold_test.go` (new);
- `internal/app/app.go`, `internal/app/app_test.go`.

**Steps:**

1. **Declarations.** Add the five project types, the seven methods with their doc comments, and their seven `Methods` rows, at the anchors Contracts names. Add the two `Deps` fields and the two `Session` fields. In `newSession` (`events.go:20`), default `git` to `worktree.CLIGitOps{}` when `d.Git` is nil, beside the `gitInfo` default, and set `fileIndex: d.FileIndex` and `git: git` directly after `modelCacheDir:`.
2. **`sddplans.Scaffold`.** Move `sddStarterPlan` (`tui/sdd_scaffold.go:12`) into `scaffold.go` as `StarterPlan`, byte for byte, and add `StarterPlanName`. `Scaffold` joins `repoRoot` and `plansDir`, creates the directory with mode 0o755, reports `existed` when `os.Stat` finds the file, and otherwise writes the template with mode 0o644. The TUI's copy stays until Task 5 deletes it.
3. **`intelligence.go`.** Copy `doctorpanel/intelligence.go`'s logic as `intelligenceChecks`, with its helpers renamed lowercase where they clash, returning `acpwire.IntelligenceCheck` values. Every name, status and detail string stays as it is. The TUI's copy stays until Task 4 deletes it.
4. **The methods** (`project.go`). Each reads the state's working directory and its live config, `s.state.ConfigSnapshot()`, at call time.
   - `FileIndex` returns `nil, nil` without `s.fileIndex`, and its result otherwise.
   - `Intelligence` returns `intelligenceChecks(s.state.ConfigSnapshot(), s.state.DB(), s.projectID)` and a nil error.
   - `Plans` maps `sddplans.Discover(workingDir, cfg.SDD.PlansDir)` to `PlanCandidate`s in order: the error strings from `Err` and `LedgerErr` when set, and `Resumable` from `Candidate.Resumable()`. It never fails.
   - `DiscardCandidate` returns `sddauthor.RemoveCandidate(path, workingDir, cfg.SDD.PlansDir)`.
   - `ScaffoldPlan` returns `sddplans.Scaffold(workingDir, cfg.SDD.PlansDir)` as a `ScaffoldResult`, with the error unchanged.
   - `Diff` refuses an empty base or head with the pinned error. Otherwise it returns `s.git.Diff(workingDir, base+".."+head, 3)`.
   - `ReadFile` resolves the path with `native.SafeResolve(workingDir, path)`, which refuses absolute and upward paths, and returns its error unchanged. It refuses anything but a regular file with the pinned error. It reads at most 1 MiB plus one byte, sets `Truncated` and cuts to 1 MiB when there was more, and sets `Binary`, leaving `Content` empty, when the first 8192 bytes hold a NUL, as `acp.FilesManager.File` does (`acp/files.go:99`). `Path` is the path as given, and `Size` the file's size.
5. **`Run`.** Set `Deps.FileIndex`, and read the eager seed through `seamSession.FileIndex(ctx)`, as Contracts says. `loadFileIndexPaths` stays, as the function behind `Deps.FileIndex`.
6. **Tests.**
   - `sddplans/scaffold_test.go`: move `TestScaffoldWritesAParseablePlan`, `TestScaffoldDoesNotClobberAnExistingFile` and `TestScaffoldPlanCompilesWithExecutableBlocks` from `tui/sdd_scaffold_test.go`, calling `Scaffold` over a temp repo root and `config.Default()`'s plans directory. The TUI's copies stay until Task 5.
   - `inproc/intelligence_test.go`: copy `doctorpanel/intelligence_test.go`'s six tests, calling `intelligenceChecks`. The TUI's copies stay until Task 4.
   - `inproc/project_test.go`:
     - `TestFileIndexReadsTheDeps`: without `Deps.FileIndex` it returns none; with one, its paths; with one that fails, its error.
     - `TestPlansListsTheDiscoveredPlans`: a working directory with one plan that parses (written as `writeTestPlan` in `tui/model_test.go` writes one) and one that does not lists both, the second with a non-empty `Error`.
     - `TestDiscardCandidateRefusesOutsideThePlansDirectory`: a file directly in the plans directory is removed; a file elsewhere in the working directory is refused and stays.
     - `TestScaffoldPlanReportsAnExistingFile`: the first call writes the file and reports `Existed` false; the second reports `Existed` true and leaves the file as the first wrote it.
     - `TestDiffReadsWithoutMutating`: moved from `tui/runoutcome_test.go`'s `TestBranchDiffReadsWithoutMutating`, over `Deps.Git: worktree.NewFakeGitOps()`. No mutating call is recorded, and an empty head gives the pinned error.
     - `TestReadFileStaysInTheWorkingDirectory`: a file in the working directory reads with its content and size; `../x` and an absolute path fail; a directory fails with the pinned error; a file with a NUL byte reads as `Binary` with empty `Content`.
   - `app_test.go`: `TestRunWiresTheFileIndex`, built like `TestRunWiresToolNames`. Inside the program runner, `model.(tui.Model).Session().FileIndex(ctx)` returns no error.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go vet ./internal/acpwire/ ./internal/acpclient/... ./internal/sddplans/ ./internal/app/
TMPDIR=/private/tmp/p2-8a-suite go test ./internal/acpwire/ ./internal/acpclient/... ./internal/sddplans/ -count=1
TMPDIR=/private/tmp/p2-8a-suite go test ./internal/acp/ -run TestACPClientMethodTableMatchesTheServer -count=1
TMPDIR=/private/tmp/p2-8a-suite go test ./internal/app/ -run 'TestRunWiresTheFileIndex|TestRunWiresToolNames' -count=1
TMPDIR=/private/tmp/p2-8a-suite go test ./internal/app/tui/... -count=1
```

Expected: all pass.

## Task 3: History and messages go through the seam

**Goal:** input history, every message scan and the watch resume's goal go through the session, and the TUI reads no `state.Messages()`.

**Files:**
- `internal/app/tui/history.go`, `internal/app/tui/history_test.go`;
- `internal/app/tui/model.go`, `internal/app/tui/commands_dispatch.go`, `internal/app/tui/sdd_command.go`;
- `internal/app/tui/model_test.go` and any other test that calls a deleted helper;
- `internal/app/tui/boundary_allowlist_test.go`, `internal/app/tui/moved_reads_test.go`.

**Steps:**

1. **Input history.**
   - `New` (`model.go:1242`) loads `m.sess.RecentPrompts(m.ctx, promptHistoryLimit)` when history is enabled, and keeps the list only on no error. The database and project checks go.
   - `recordPrompt` (`history.go:24`) calls `m.sess.SavePrompt(m.ctx, value)` and ignores the error, as today. The `time` import goes.
2. **Messages.** Each reader asks one query, and treats an error as an empty page:
   - `sessionHasUserTurn`: `{Role: RoleUser, Last: 1}`, true when `Total` is above 0;
   - `computeSuggestion`: `{Role: RoleAssistant, Last: 1}`, returning nil without a message, and reading the one message's content in place of `last.Content`;
   - `rewindPickerItems`: `{Role: RoleUser}`, building the same items from the page's messages;
   - `startNewSession` (`commands_dispatch.go:178`): `{Last: 1}`'s `Total`, read before `NewSession` as today;
   - `startSDDAuthoring`'s `--from-last-plan` (`model.go:4090`): `{Role: RoleAssistant, Final: true, Last: 1}`, with today's note when the page is empty.
3. **The watch resume.** `startWatchResumeRun` keeps its guards. It calls `m.startSessionRun("Cannot start work", …)` itself, as `startPrompt` does (`model.go:3980`), with a run that sends `PromptParams{WatchResume: &acpwire.WatchResume{Name: name, Repeat: repeat}}`. It keeps assigning `*m` from the result.
4. **Deletions.** Delete `lastAssistantText` (`model.go:4051`) and `lastFinalAssistantPlan` (`sdd_command.go:82`). `model.go`'s `watch` import goes, and `sdd_command.go`'s `app/session` import, which only that function used.
5. **Tests.**
   - `TestPromptRecordedToDBAndLoadedOnStartup` (`history_test.go:234`) builds each model with `testModelWith(inproc.Deps{State: …, ProjectID: projectID})` in place of `WithMemoryStore`. Its assertions stay.
   - A test that called `lastFinalAssistantPlan` or `lastAssistantText` drives the command or the watch resume instead. Find them with the compiler.
   - Add `TestWatchResumeSendsTheWatchNotTheText` in `model_test.go`: over a model with an assistant reply "I fixed the build." and a `fakeAgentRunner` installed with `withPromptRunner` (as `TestPostAcceptedPromptCannotReopenWork` does, `model_test.go:11049`), `m.startWatchResumeRun("ci", false)` returns a command, and running it hands the runner `watch.ResumeGoal("ci", "I fixed the build.", false)`. The test file may import `internal/watch`.
6. **Guards.**
   - `movedStateReads` gains `"Messages": "8a"`, as its own commented block directly after the `"DismissNotice": "6",` entry, before the plan 7b comment.
   - The allowlist loses `sdd_command.go`'s key, and `model.go` loses `marshal/internal/watch`.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
grep -rn 'state.Messages()\|watch\.ResumeGoal' internal/app/tui --include='*.go' | grep -v '_test.go'
go vet ./internal/app/...
TMPDIR=/private/tmp/p2-8a-suite go test ./internal/app/tui/... -count=1
TMPDIR=/private/tmp/p2-8a-suite go test ./internal/app/ -count=1
```

Expected:
- the grep prints nothing;
- the packages pass, `TestTUIStaysOnTheClientSeam` and `TestMovedStateReadsStayMoved` included.

Mutation check: make the adapter's `Messages` ignore `query.Final`, and `TestMessagesSelectsByRoleFinalityAndLast` fails. Restore it.

## Task 4: The file index and the doctor go through the seam

**Goal:** the `@` popup's lazy load and the doctor's checks go through the session, and the doctor panel holds no state.

**Files:**
- `internal/app/tui/model.go`, `internal/app/tui/commands_dispatch.go`;
- `internal/app/tui/doctorpanel/doctorpanel.go`, `internal/app/tui/doctorpanel/doctorpanel_test.go`;
- `internal/app/tui/doctorpanel/intelligence.go` and `intelligence_test.go` (deleted);
- `internal/app/tui/boundary_allowlist_test.go`, and any test that builds a doctor panel.

**Steps:**

1. **The file index.** `populateFileIndexIfNeeded` (`model.go:3455`) calls `m.sess.FileIndex(m.ctx)` in place of the `memoryDB` checks and read. On an error it marks the index loaded and keeps it empty, as today; otherwise it builds the items from the paths with `buildFileIndexItems`. `WithFileIndex` stays.
2. **The doctor.**
   - `doctorpanel.New` takes the diagnostics and the wire checks, and the panel's `state` field and `app/session` import go. `diagsToFields` and its check rows read `acpwire.IntelligenceCheck`'s three fields.
   - `/doctor` (`commands_dispatch.go:308`) asks `m.sess.Intelligence(m.ctx)`, uses no checks on an error, and opens `doctorpanel.New(diags, checks)`.
   - Delete `doctorpanel/intelligence.go` and its test file: Task 2 copied both into the adapter.
3. **Tests.** `doctorpanel_test.go` builds `acpwire.IntelligenceCheck` values where it built `Check`s. The model tests that drive `/doctor` (in `doctor_secret_test.go`, `status_test.go` and `model_test.go`) keep passing over the adapter with no database, where the index check reads "database unavailable".
4. **Allowlist.** The `doctorpanel/doctorpanel.go` and `doctorpanel/intelligence.go` keys go.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
grep -rn 'memoryDB.GetFileIndex\|ComputeIntelligence' internal/app/tui --include='*.go'
go vet ./internal/app/...
TMPDIR=/private/tmp/p2-8a-suite go test ./internal/app/tui/... -count=1
```

Expected:
- the grep prints nothing;
- the packages pass, `TestTUIStaysOnTheClientSeam` included.

## Task 5: Plans and the run outcome go through the seam

**Goal:** plan discovery, discarding a candidate, the starter plan and the run outcome's two reads go through the session.

**Files:**
- `internal/app/tui/model.go`, `internal/app/tui/commands_dispatch.go`;
- `internal/app/tui/sdd_scaffold.go`, `internal/app/tui/sdd_scaffold_test.go`;
- `internal/app/tui/runoutcome.go`, `internal/app/tui/runoutcome_test.go`;
- `internal/app/tui/boundary_allowlist_test.go`, and any test that reads a deleted constant.

**Steps:**

1. **Discovery.**
   - `openRunPreflight` (`model.go:3809`) finds the plan's task count in `m.sess.Plans(m.ctx)` by path, reading `Error`, `Tasks`, `LedgerError` and `Done` where it read the candidate's fields. An error leaves "unknown task count", as no match does today.
   - `openSDDPlanPicker` (`model.go:6663`) lists `m.sess.Plans(m.ctx)`, treating an error as no plans. It reads the wire fields: `Resumable` for `Resumable()`, a non-empty `Error` or `LedgerError` where it tested `Err` or `LedgerErr`, and `Error` as the unreadable plan's detail.
2. **Discard.** The review panel's `DiscardMsg` (`model.go:2253`) calls `m.sess.DiscardCandidate(m.ctx, path)` in place of `sddauthor.RemoveCandidate`, with the same error handling.
3. **The starter plan.** `scaffoldSDDPlan` calls `m.sess.ScaffoldPlan(m.ctx)`:
   - an error notes `"Could not " + err.Error()`, which reads as today's two failure notes;
   - `Existed` notes today's "already exists" sentence with the result's path;
   - otherwise it notes today's "Wrote a starter plan…" text with the result's path.

   Delete `sddStarterPlan` and `sddStarterPlanName`, and `sdd_scaffold.go`'s `os` and `path/filepath` imports.
4. **The run outcome.**
   - `/run` (`commands_dispatch.go:430`) passes `runOutcomeActions(m.ctx, m.sess, st.SDD, st.WorkingDir, m.width)`. The `worktree` import goes.
   - In `runOutcomeActions`, each closure falls back to `context.Background()` when its context is nil. The diff action calls `branchDiff`, and the ledger action calls `readLedger`.
   - `branchDiff` asks `sess.Diff(ctx, acpwire.DiffQuery{Base: base, Head: branch})` and keeps today's three outcomes and their texts.
   - `readLedger` makes the ledger path relative to the working directory with `filepath.Rel`. A path it cannot make relative, or one that starts with `..`, gives the pinned "outside the working directory" text. Otherwise it reads `sess.ReadFile(ctx, rel)`, and returns `Could not read <ledger path>: <err>` on an error, the pinned binary text for a binary file, `<ledger path> is empty.` for a zero size, and the content otherwise.
   - Delete `readFileOrExplain`, and update the comments that said the reads run locally until plan 8: they now go through the session.
5. **Tests.**
   - `sdd_scaffold_test.go` keeps `TestScaffoldTellsTheUserWhereItWrote` and renames `TestScaffoldDoesNotClobberAnExistingFile` to `TestScaffoldReportsAnExistingFile`, asserting the "already exists" note as well as the unchanged file. The two parse and compile tests go: Task 2 moved them to `sddplans`. Their `pipeline` import goes.
   - In `runoutcome_test.go`, `TestBranchDiffReadsWithoutMutating` goes (Task 2 moved it). `TestOutcomeActionsRunLocally` becomes `TestOutcomeActionsReadThroughTheSession`: the ledger sits under a temp working directory, the session is an adapter over a state in that directory, and the ledger action's text is the file's contents. Add a case whose ledger path is outside the working directory and reads as the pinned text.
6. **Allowlist.** The `commands_dispatch.go` and `runoutcome.go` keys go, and `model.go` loses `marshal/internal/sddauthor` and `marshal/internal/sddplans`.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
grep -rn 'sddplans\.\|sddauthor\.\|worktree\.\|os.ReadFile\|os.WriteFile\|os.MkdirAll' internal/app/tui --include='*.go' | grep -v '_test.go'
go vet ./internal/app/...
TMPDIR=/private/tmp/p2-8a-suite go test ./internal/app/tui/... ./internal/sddplans/ -count=1
```

Expected:
- the grep prints nothing;
- the packages pass, `TestTUIStaysOnTheClientSeam` included.

Mutation check: make `readLedger` skip the outside-the-directory check, and the new case in `TestOutcomeActionsReadThroughTheSession` fails. Restore it.

## Task 6: Dead code goes

**Goal:** the todo row helpers, the tasks panel's state and the model's broker fields are deleted.

**Files:**
- `internal/app/tui/todos.go`, `internal/app/tui/todos_test.go`, `internal/app/tui/todo_band_test.go`;
- `internal/app/tui/model.go`;
- `internal/app/tui/boundary_allowlist_test.go`.

**Steps:**

1. **`todos.go`.** Delete `todoLine`, `todoProgress`, `todosAllDone` and the comment above `todoLine`. Delete `tasksPanelModel.state` and its assignment in `newTasksPanel`. Drop the imports the compiler then reports unused; `app/session` and `tools/native` must be among them.
2. **Tests.** Delete `TestTodoLineFitsWidthWithWideRunes`, `TestTodoLineDoneIsMutedNotStruckThrough` and `TestTodoLineNoStrikethroughWhenPending` (`todos_test.go:34` to `:65`). In `todo_band_test.go:69`, the comment drops `todoProgress` and says the todo counters are the band's inputs.
3. **The brokers.** Delete `workspaceBroker`, `workspaceEvents`, `subagentBroker` and `subagentEvents` with their comments (`model.go:343` to `:352`). `model.go`'s `pubsub` import goes.
4. **Allowlist.** The `todos.go` key goes, and `model.go` loses `marshal/internal/pubsub`.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
grep -rn 'todoLine\|todoProgress\|todosAllDone\|workspaceBroker\|subagentBroker' internal/app/tui --include='*.go'
go vet ./internal/app/...
TMPDIR=/private/tmp/p2-8a-suite go test ./internal/app/tui/... -count=1
```

Expected:
- the grep prints nothing;
- the packages pass, `TestTUIStaysOnTheClientSeam` included. `model.go`'s allowlist entry now holds `app/session`, `db` and `skills` only (plan 8b removes `skills`).

## Task 7: Verification and docs

**Goal:** the full suite is green, and the spec records plan 8a.

**Files:** `docs/daemon/p2-client-seam.md`.

**Steps:**

1. **`p2-client-seam.md`.** Add a plan 8a entry to the Implementation record, after plan 7d's. If plan 8b's entry is already there, add 8a's before it, so the record stays in plan order. It lists:
   - the wire types and `PromptParams.WatchResume`;
   - the ten methods and their `Methods` rows: eight new ACP methods, and `session/diff` and `session/file`, which exist;
   - the two `Deps` fields, `sddplans.Scaffold` and the adapter's copy of the doctor's checks;
   - what the TUI now asks the session for, and what it deleted;
   - the allowlist's 12 entries across 7 files and 6 keys entirely, and T1b's `Messages`.
2. **Main may have moved.** If plan 8b merged while this branch ran, merge `main` into the branch before the full verification. Expect conflicts only in the files this plan's header names. Keep both sides: each plan's methods, rows, fields, allowlist changes and record entries.
3. **Full verification.**

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
gofmt -l ./internal ./cmd ./web
go vet ./...
TMPDIR=/private/tmp/p2-8a-suite CGO_ENABLED=1 go build ./cmd/marshal
TMPDIR=/private/tmp/p2-8a-suite go test ./... -timeout 20m 2>&1 | grep -E '^(--- FAIL|FAIL|ok)' | grep -v '^ok'
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
- Up recalls a prompt typed in an earlier session of the same project;
- `@` lists repository files;
- `/doctor` shows the Intelligence section;
- `/rewind` lists the session's turns, and `/new` reports how many messages it cleared;
- in a repository with no plans, `/sdd` offers "Write a starter plan", which writes `.marshal/plans/starter-plan.md` and says so; picking it again says it already exists;
- after a finished plan run, `/run` shows the outcome, and its "branch diff" and "ledger" rows open.

## Integration notes

- **Plan 8b** runs alongside. See the header for the shared files.
- **Plan 9** deletes `WithMemoryStore` with `memoryDB` and `memoryProject`, `WithLegacyState` and `WithLegacySession`, and T1b.
- **P3:**
  - the eight new methods need handlers;
  - `session/prompt` gains `watchResume`, and `session/diff` gains `head` (R9);
  - `ReadFile` already matches `session/file`: a path relative to the working directory.

## Self-review

- **Every task verifiable alone.** Tasks 1 and 2 add the seam, which nothing uses yet. Tasks 3 to 5 each move one group of readers, and Task 6 deletes code nothing reaches.
- **Cold executor.** Each step names the function, the query or call it makes after the change, and what stays. The two copies Task 2 makes are deleted by name in Tasks 4 and 5.
- **Contracts.** The wire types, the methods and their rows, the `Deps` and `Session` fields, `sddplans.Scaffold`, the TUI's changed signatures and every new string are declared there.
- **Anchors.** Every path, symbol and line was checked at `6c6d16b2`.
- **Spec.** C11, C2 and the Migration order are updated, with the revision record "2026-11-06, against `6c6d16b2` (plans 7c and 7d merged)". P3's R9 list carries the ACP differences.
- **No placeholders.** `StarterPlan`'s `...` in Contracts stands for the moved template, which step 2 of Task 2 names.
