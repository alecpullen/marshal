# P3 plan 7b: Notes, commands, browsers and project data — implementation plan

Date: 2026-11-09. Status: ready for review.
Base: `main` at `d103051b` (P3 plans 6b and 7a merged). Every anchor below was checked there. The full suite ran there (`TMPDIR=/private/tmp/p3-b67`, `-timeout 20m`): 118 packages ok, and `web/bridge` timed out in the known `TestRecipeRunMaxUSDSetsAPausingCapForTheRun` hang; rerun alone, `web/bridge` passed (156s), so 119 ok.
- **It runs alongside P3 plan 6c** ([pipelines, the end flow and sessions](2026-11-09-p3-6c-ending-plan.md)). Both plans add a manager to `internal/acp`, rows to `acpclient.Methods`, methods to the conformance harness's `seam`, and an entry to the spec's Implementation record. "Integration with plan 6c" (at the end) lists the conflicts this causes and how to resolve each one, and the one step that belongs to whichever plan merges second.
- **The spec edits are already made**, uncommitted in the main checkout: the header's `Status:`, "Where the code is today", "Methods P3 adds" (plans 6c and 7b), R2's errors, R3's `ActiveWork` and `Commands` rows, R6, R7, R9's list, R10, and Migration order items 6c and 7b. Task 7 adds only the Implementation record and the re-measured counts.
- Nothing a user sees changes. The TUI still runs on `inproc`, in process, until plan 8.

Approved design: [p3-acp-transport.md](../p3-acp-transport.md): R1 (the service behind the handlers; the interface checks arrive with the last plan), R2 (read your writes, cancellation), R3 (the mirror: the `Commands` row), R6 and R10 (`AddNote` stays synchronous), R9 (compatibility), "Decided with the user (2026-11-08)", Migration order item 7b, and tests V1, V3, V6 and V8.
Previous plans: [P3 plan 7a, settings](2026-11-08-p3-7a-settings-plan.md), [P3 plan 6b, turns and controls](2026-11-08-p3-6b-controls-plan.md).
Authoring workflow: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md).

**How this plan is written.** No prototype was built, and the plan embeds no implementation code. The Contracts section holds declarations only. Every other step states its decision in prose. Executing the plan also tests how well Marshal follows a plan. "As `X` does" means `X` exists and is the model to copy.

**Goal:** a marshal client adds notes, runs commands and their row actions, browses its memories, plugins and skills, and reads its project's history, plans, files and diffs over ACP on the session's service.
- Fourteen new methods: `session/note`, `session/command_action`, `session/prompt_history`, `session/prompt_history_save`, `session/messages`, `session/file_index`, `session/intelligence`, `session/plans`, `session/sdd_discard`, `session/sdd_scaffold`, `session/skills_install`, `session/skills_autoload`, `session/skill_gate_set` and `session/skill_gate_clear`.
- For a marshal client, fourteen existing methods are served by the session's service: `session/command`, `command_list`, `memory_list`, `memory_delete`, `plugins_list`, `plugins_install_scan`, `plugins_install_confirm`, `plugins_install_discard`, `plugins_remove`, `skills_list`, `skills_load`, `skills_remove`, `diff` and `file`. Every other client reaches today's handlers.
- The command list is fetched once per session and kept.
- `AddNote` stays synchronous: a note is in the transcript the client reads when the call returns.
- `rpc.Session` gains the group's twenty-eight methods, and the conformance harness gains their cases.

**Non-goals:**
- **No change for a client without the capability.** Its calls to the fourteen existing methods are today's, byte for byte (R9).
- **No pipelines, no end flow, no sessions.** They are plan 6c's.
- **No change to the service's logic.** The handlers only decode and encode.
- **No `commands_changed` update.** The runtime's command registry never changes after startup (decision 2), so there is nothing to push.
- **No change to `internal/app/tui`.** `Command` is already asynchronous (plan 6a), and `AddNote` stays synchronous (decision 3). The interface's methods keep their signatures; only their `ACP:` doc lines change.
- **No changelog entry.**

**Assumptions:**
- Every Go test command below runs with `TMPDIR=/private/tmp/p3-7b-suite`. Create the directory first: `mkdir -p /private/tmp/p3-7b-suite`. Keep the path short: a long one breaks the unix-socket tests in `internal/acp` and `web/bridge`. Plan 6c uses `/private/tmp/p3-6c-suite`. Never share a directory between runs.
- **Two suites may run at once.** Load makes the flakes listed under Task 7 likelier. Rerun a failure alone before treating it as new.
- **The service** (`internal/acp/service`): `AddNote` (`notes.go:26`); `Commands` (`commands.go:26`), `Command` (`:44`) and `CommandAction` (`:63`), which mints row-action IDs it keeps for the sixteen newest results; `RecentPrompts` (`history.go:13`), `SavePrompt` (`:23`) and `Messages` (`:34`); `FileIndex` (`project.go:23`), `Intelligence` (`:32`), `Plans` (`:38`), `DiscardCandidate` (`:64`), `ScaffoldPlan` (`:71`), `Diff` (`:82`) and `ReadFile` (`:91`); `Memories` (`browsers.go:43`) and `DeleteMemory` (`:68`); `Plugins` (`plugins.go:42`), `ScanPlugin` (`:72`, which clones with its context), `InstallPlugin` (`:198`, which installs with its context), `DiscardPluginScan` (`:224`) and `RemovePlugin` (`:237`); `Skills` (`skills.go:104`), `LoadSkill` (`:181`), `InstallSkill` (`:206`, which installs with its context), `RemoveSkill` (`:232`), `SetSkillAutoload` (`:261`), `SetSkillGate` (`:291`) and `ClearSkillGateDecision` (`:302`). Many of their failures are plain errors whose sentence the TUI shows (the plugins and skills panels, the command popup).
- **ACP's handlers for the existing methods differ from the service** and stay as they are for other clients (R9): `CommandManager.Command` (`commands.go:107`) refuses during a turn, runs no TUI-only command, mints no action IDs and returns no agent goal; `CommandList` (`:162`) rewrites a headless-capable command's kind and refuses a session with no registry; `MemoryManager.MemoryList` lists the session's scoped rows where the TUI lists the project's own; the plugin handlers return content counts, check no reviewed hash and roll nothing back; the skill handlers find the global store with `skills.ScopeDir`, key a skill by name and load without checking the store; `WorktreeManager.Diff` (`worktree.go:206`'s `diffParams`) takes a base and a path but no head. The web UI uses most of these (`web/ui/src`).
- **The command registry is fixed.** `StartRuntime` builds `CommandRegistry` once (`runtime.go:1890`) and nothing registers a command after it; `ServiceDeps` hands the same registry to every session (`servicedeps.go:67`), a replacement's included.
- **A note changes the transcript.** `State.AddMessage` publishes `EventMessageAdded` (`session/messages.go:414`) to the state's event broker; the turn manager's idle stack loop marks the stack dirty on each event and, on its fence, flushes a dirty stack before the response (`stack.go:473` to `:555`). A runtime attaches its broker to its state; the conformance harness's `serveTranscript` (`conformance_test.go:280`) builds a broker for the idle loop but does not attach it to the served state, so on the pipe arm today a note never marks the stack dirty.
- **Errors.** The server writes any handler error that is neither an `*acpwire.Error` nor a JSON-RPC error as `internal error` (F-SEC-37). Plan 7a's `settingsFailureOf` (`settings.go:862`) wraps a handler so a plain error travels as an uncoded `*acpwire.Error` carrying its sentence; until plan 6c's client fix lands, such a sentence reaches the client prefixed "server error: ", so a case compares it with `strings.Contains`, as plan 7a's do. Plan 6c also makes this the rule for every marshal client's error; this plan does not depend on it.
- **Handlers and managers.** `registerHandlers` (`host.go:359`) registers `session/command` and `command_list` at `host.go:518` and `:519`, `memory_list` at `:531`, `memory_delete` at `:535`, `diff` at `:566`, `file` at `:592`, `skills_list` at `:603`, `skills_remove` at `:607`, `skills_load` at `:608`, and the five plugin methods at `:620` to `:624`; the managers they belong to (`cmds`, `mem`, `wtMgr`, `filesMgr`, `skillsMgr`, `pluginsMgr`) are all built by `:624`. `marshalOr` is `host.go:292`. `RequestManager.service` (`requests.go:228`) is the model for resolving a session's service. `cancellable` is `server.go:443`.
- **The client.** `rpc/turns.go` is the model for plain calls; `Host.ProviderTemplates` (`rpc/settings.go:27`) for a read fetched once and kept.
- **The interface** (`internal/acpclient/acpclient.go`): `AddNote` (`:588`), `Commands` (`:611`) to `ClearSkillGateDecision` (`:712`), and `RecentPrompts` (`:790`) to `ReadFile` (`:845`).
- **Test models.** `conformance_settings_test.go` moved a group's service tests with their fixtures; `TestStackBusyFlipBudget` (`conformance_transcript_test.go:65`) holds a 10ms budget for a call that pays one flush of the 2,000-call session (`largeStackSession`, `stack_wire_test.go:413`); `TestControlCallBudget` (`conformance_controls_test.go:1253`) a 1ms one.

**Decisions this plan makes beyond the spec** (R3, R6, R9 and R10 record them):

1. **A marshal client's group runs on the service.** The fourteen existing methods go through `marshalOr`, as plan 6b routed the turn's (decided with the user, 2026-11-08): each differs from the service in what the TUI sees (Assumptions). The fourteen new methods are registered for every client.
2. **The command list is fetched once and kept.** `Commands()` sends `session/command_list` on its first read and keeps the answer, as `ProviderTemplates` does: the registry does not change while the runtime runs, and a replacement shares it. `commands_changed` is dropped, as plan 7a dropped `session/tools`.
3. **`AddNote` stays synchronous.** It returns once the barrier has flushed the note's stack patch, so a read after it shows the note, as in process. Making it asynchronous would let a note land after a later call's effect: the agent runs each request on its own goroutine, so only a returned call is ordered. Its cost is one flush, which in process the TUI already paid on its next refresh; it is measured against the 10ms R10 sets for such a call.
4. **Every handler keeps its sentence.** Each new method is registered through `settingsFailureOf`, and each `marshalOr` method's marshal branch is wrapped in it, so a plain error reaches the client as its sentence; other clients' handlers are not wrapped. This holds whether or not plan 6c's error rule has merged, and is harmless once it has.
5. **The installs are cancellable.** `session/plugins_install_scan`, `plugins_install_confirm` and `skills_install` take their context from `cancellable`, so a cancelled clone stops (R2).
6. **The harness's pipe arm flushes the transcript as a runtime does.** `serveTranscript` attaches its event broker to the served state, so a call that changes the transcript marks the stack dirty and the barrier flushes it. Without it, the pipe arm could not show a note right after `AddNote`, or measure what that costs.
7. **ACP's `session/diff` gains no `head`.** A marshal client's diff is the service's, which takes `base` and `head`; R9's earlier "it gains an optional head" is replaced.

**Execution:**
- In the main checkout, if `git status` shows `docs/daemon/p3-acp-transport.md`, this plan or plan 6c's plan uncommitted, commit them to `main` first as explicit paths, with the message `docs/daemon/plans: record P3 plans 6c and 7b`. Plan 6c gives the same instruction, so whichever plan starts first commits all three. The main checkout holds other untracked files (`scripts/lmstudio-bench/`, `docs/fleet-cli/` and others). Leave them alone.
- Then branch: `git switch -c p3-7b-browsers` from `main`, or create a worktree on that branch.
- Run the tasks in order. Run each task's Verify and commit it as `p3-7b-browsers: task N — <title>`.
- Stage explicit paths only. Never `git add -A` without a path.
- Run `gofmt -w` on every Go file a task touches before its Verify.

## Contracts

### Wire types (`internal/acpwire/browsercalls.go`, new)

```go
// The notes, command, browser and project methods (P3 plan 7b).
const (
	MethodNote                  = "session/note"
	MethodCommand               = "session/command"
	MethodCommandList           = "session/command_list"
	MethodCommandAction         = "session/command_action"
	MethodPromptHistory         = "session/prompt_history"
	MethodPromptHistorySave     = "session/prompt_history_save"
	MethodMessages              = "session/messages"
	MethodFileIndex             = "session/file_index"
	MethodIntelligence          = "session/intelligence"
	MethodPlans                 = "session/plans"
	MethodSDDDiscard            = "session/sdd_discard"
	MethodSDDScaffold           = "session/sdd_scaffold"
	MethodDiff                  = "session/diff"
	MethodFile                  = "session/file"
	MethodMemoryList            = "session/memory_list"
	MethodMemoryDelete          = "session/memory_delete"
	MethodPluginsList           = "session/plugins_list"
	MethodPluginsInstallScan    = "session/plugins_install_scan"
	MethodPluginsInstallConfirm = "session/plugins_install_confirm"
	MethodPluginsInstallDiscard = "session/plugins_install_discard"
	MethodPluginsRemove         = "session/plugins_remove"
	MethodSkillsList            = "session/skills_list"
	MethodSkillsLoad            = "session/skills_load"
	MethodSkillsInstall         = "session/skills_install"
	MethodSkillsRemove          = "session/skills_remove"
	MethodSkillsAutoload        = "session/skills_autoload"
	MethodSkillGateSet          = "session/skill_gate_set"
	MethodSkillGateClear        = "session/skill_gate_clear"
)

// NoteParams is session/note's params.
type NoteParams struct {
	SessionID string `json:"sessionId"`
	Note      Note   `json:"note"`
}

// CommandCallParams is session/command's params from a marshal client; ACP's
// carry the same keys.
type CommandCallParams struct {
	SessionID string   `json:"sessionId"`
	Name      string   `json:"name"`
	Args      []string `json:"args,omitempty"`
}

// CommandActionParams is session/command_action's params: a row action's ID.
type CommandActionParams struct {
	SessionID string `json:"sessionId"`
	ID        string `json:"id"`
}

// PromptHistoryParams is session/prompt_history's params.
type PromptHistoryParams struct {
	SessionID string `json:"sessionId"`
	Limit     int    `json:"limit,omitempty"`
}

// PromptHistoryResult is session/prompt_history's result, newest first.
type PromptHistoryResult struct {
	Prompts []string `json:"prompts,omitempty"`
}

// PromptSaveParams is session/prompt_history_save's params.
type PromptSaveParams struct {
	SessionID string `json:"sessionId"`
	Text      string `json:"text"`
}

// MessagesParams is session/messages' params.
type MessagesParams struct {
	SessionID string       `json:"sessionId"`
	Query     MessageQuery `json:"query"`
}

// FileIndexResult is session/file_index's result.
type FileIndexResult struct {
	Paths []string `json:"paths,omitempty"`
}

// IntelligenceResult is session/intelligence's result.
type IntelligenceResult struct {
	Checks []IntelligenceCheck `json:"checks,omitempty"`
}

// PlansResult is session/plans' result.
type PlansResult struct {
	Plans []PlanCandidate `json:"plans,omitempty"`
}

// PathParams is the params of a call that names one path: session/sdd_discard
// and a marshal client's session/file.
type PathParams struct {
	SessionID string `json:"sessionId"`
	Path      string `json:"path"`
}

// DiffCallParams is a marshal client's session/diff params: the range
// Base..Head.
type DiffCallParams struct {
	SessionID string `json:"sessionId"`
	Base      string `json:"base"`
	Head      string `json:"head"`
}

// DiffResult is a marshal client's session/diff result.
type DiffResult struct {
	Diff string `json:"diff"`
}

// MemoriesResult is a marshal client's session/memory_list result.
type MemoriesResult struct {
	Memories []MemoryEntry `json:"memories,omitempty"`
}

// MemoryDeleteParams is session/memory_delete's params; ACP's carry the same
// keys.
type MemoryDeleteParams struct {
	SessionID string `json:"sessionId"`
	ID        int64  `json:"id"`
}

// PluginsResult is a marshal client's session/plugins_list result.
type PluginsResult struct {
	Plugins []PluginEntry `json:"plugins,omitempty"`
}

// PluginScanParams is a marshal client's session/plugins_install_scan params.
type PluginScanParams struct {
	SessionID string `json:"sessionId"`
	Source    string `json:"source"`
}

// PluginInstallParams is a marshal client's session/plugins_install_confirm
// params; ACP's carry the same keys.
type PluginInstallParams struct {
	SessionID string `json:"sessionId"`
	ScanToken string `json:"scanToken"`
	Scope     string `json:"scope"`
}

// PluginDiscardParams is a marshal client's session/plugins_install_discard
// params; ACP's carry the same keys.
type PluginDiscardParams struct {
	SessionID string `json:"sessionId"`
	ScanToken string `json:"scanToken"`
}

// PluginRemoveParams is a marshal client's session/plugins_remove params;
// ACP's carry the same keys.
type PluginRemoveParams struct {
	SessionID string `json:"sessionId"`
	Name      string `json:"name"`
	Scope     string `json:"scope"`
}

// SkillsResult is a marshal client's session/skills_list result.
type SkillsResult struct {
	Skills []SkillEntry `json:"skills,omitempty"`
}

// SkillParams names one listed skill by its store and source: a marshal
// client's session/skills_load and session/skills_remove params.
type SkillParams struct {
	SessionID string `json:"sessionId"`
	Scope     string `json:"scope"`
	Source    string `json:"source"`
}

// SkillNameResult is session/skills_load's and session/skills_install's
// result for a marshal client: the skill's name.
type SkillNameResult struct {
	Name string `json:"name"`
}

// SkillInstallParams is session/skills_install's params.
type SkillInstallParams struct {
	SessionID string `json:"sessionId"`
	Source    string `json:"source"`
	Scope     string `json:"scope"`
}

// SkillAutoloadParams is session/skills_autoload's params.
type SkillAutoloadParams struct {
	SessionID string `json:"sessionId"`
	Name      string `json:"name"`
	On        bool   `json:"on"`
}

// SkillGateParams is session/skill_gate_set's params.
type SkillGateParams struct {
	SessionID string `json:"sessionId"`
	On        bool   `json:"on"`
}

// SkillGateClearParams is session/skill_gate_clear's params.
type SkillGateClearParams struct {
	SessionID string `json:"sessionId"`
	Skill     string `json:"skill"`
}
```

Results: `session/note`, `prompt_history_save`, `sdd_discard`, `memory_delete`, `plugins_install_confirm`, `plugins_install_discard`, `plugins_remove`, `skills_remove`, `skills_autoload`, `skill_gate_set` and `skill_gate_clear` answer `{}`; `session/command` and `command_action` a `CommandResult`; `session/command_list` a `CommandListResult`; `session/messages` a `MessagePage`; `session/sdd_scaffold` a `ScaffoldResult`; `session/file` a `FileContent`; `session/plugins_install_scan` a `PluginScan`; the lists their result types above. `session/command_list`, `file_index`, `intelligence`, `plans`, `sdd_scaffold`, `memory_list`, `plugins_list` and `skills_list` take plan 6b's `ControlParams`.

### The browser handlers (`internal/acp/browsers.go`, new)

```go
// BrowserManagerConfig wires one connection's browser handlers.
type BrowserManagerConfig struct {
	// Lookup resolves a session's service.
	Lookup func(sessionID string) (*service.Session, bool)
}

// BrowserManager serves a session's notes, commands, browsers and project
// reads over its service for one connection. It runs no forwarders.
type BrowserManager struct{ /* unexported */ }

func NewBrowserManager(cfg BrowserManagerConfig) *BrowserManager

func (m *BrowserManager) Note(ctx context.Context, params json.RawMessage) (any, error)
func (m *BrowserManager) Command(ctx context.Context, params json.RawMessage) (any, error)
func (m *BrowserManager) CommandList(ctx context.Context, params json.RawMessage) (any, error)
func (m *BrowserManager) CommandAction(ctx context.Context, params json.RawMessage) (any, error)

func (m *BrowserManager) PromptHistory(ctx context.Context, params json.RawMessage) (any, error)
func (m *BrowserManager) PromptHistorySave(ctx context.Context, params json.RawMessage) (any, error)
func (m *BrowserManager) Messages(ctx context.Context, params json.RawMessage) (any, error)
func (m *BrowserManager) FileIndex(ctx context.Context, params json.RawMessage) (any, error)
func (m *BrowserManager) Intelligence(ctx context.Context, params json.RawMessage) (any, error)
func (m *BrowserManager) Plans(ctx context.Context, params json.RawMessage) (any, error)
func (m *BrowserManager) SDDDiscard(ctx context.Context, params json.RawMessage) (any, error)
func (m *BrowserManager) SDDScaffold(ctx context.Context, params json.RawMessage) (any, error)
func (m *BrowserManager) Diff(ctx context.Context, params json.RawMessage) (any, error)
func (m *BrowserManager) File(ctx context.Context, params json.RawMessage) (any, error)

func (m *BrowserManager) MemoryList(ctx context.Context, params json.RawMessage) (any, error)
func (m *BrowserManager) MemoryDelete(ctx context.Context, params json.RawMessage) (any, error)
func (m *BrowserManager) PluginsList(ctx context.Context, params json.RawMessage) (any, error)
func (m *BrowserManager) PluginsInstallScan(ctx context.Context, params json.RawMessage) (any, error)
func (m *BrowserManager) PluginsInstallConfirm(ctx context.Context, params json.RawMessage) (any, error)
func (m *BrowserManager) PluginsInstallDiscard(ctx context.Context, params json.RawMessage) (any, error)
func (m *BrowserManager) PluginsRemove(ctx context.Context, params json.RawMessage) (any, error)
func (m *BrowserManager) SkillsList(ctx context.Context, params json.RawMessage) (any, error)
func (m *BrowserManager) SkillsLoad(ctx context.Context, params json.RawMessage) (any, error)
func (m *BrowserManager) SkillsInstall(ctx context.Context, params json.RawMessage) (any, error)
func (m *BrowserManager) SkillsRemove(ctx context.Context, params json.RawMessage) (any, error)
func (m *BrowserManager) SkillsAutoload(ctx context.Context, params json.RawMessage) (any, error)
func (m *BrowserManager) SkillGateSet(ctx context.Context, params json.RawMessage) (any, error)
func (m *BrowserManager) SkillGateClear(ctx context.Context, params json.RawMessage) (any, error)

// registerBrowsers registers the fourteen new methods on srv, each through
// settingsFailureOf, and the fourteen existing ones through marshalOr, whose
// marshal branch is wrapped in settingsFailureOf: a client without the
// capability reaches others[method], unchanged.
func registerBrowsers(srv *Server, browsers *BrowserManager, others map[string]Handler)
```

### The client (`internal/acpclient/rpc/browsers.go`, new)

```go
// commandsCallTimeout bounds the first read's session/command_list.
const commandsCallTimeout = 5 * time.Second

func (s *Session) AddNote(ctx context.Context, note acpwire.Note) error
func (s *Session) Commands() []acpwire.CommandInfo
func (s *Session) Command(ctx context.Context, name string, args []string) (acpwire.CommandResult, error)
func (s *Session) CommandAction(ctx context.Context, id string) (acpwire.CommandResult, error)

func (s *Session) RecentPrompts(ctx context.Context, limit int) ([]string, error)
func (s *Session) SavePrompt(ctx context.Context, text string) error
func (s *Session) Messages(ctx context.Context, query acpwire.MessageQuery) (acpwire.MessagePage, error)
func (s *Session) FileIndex(ctx context.Context) ([]string, error)
func (s *Session) Intelligence(ctx context.Context) ([]acpwire.IntelligenceCheck, error)
func (s *Session) Plans(ctx context.Context) ([]acpwire.PlanCandidate, error)
func (s *Session) DiscardCandidate(ctx context.Context, path string) error
func (s *Session) ScaffoldPlan(ctx context.Context) (acpwire.ScaffoldResult, error)
func (s *Session) Diff(ctx context.Context, query acpwire.DiffQuery) (string, error)
func (s *Session) ReadFile(ctx context.Context, path string) (acpwire.FileContent, error)

func (s *Session) Memories(ctx context.Context) ([]acpwire.MemoryEntry, error)
func (s *Session) DeleteMemory(ctx context.Context, id int64) error
func (s *Session) Plugins(ctx context.Context) ([]acpwire.PluginEntry, error)
func (s *Session) ScanPlugin(ctx context.Context, source string) (acpwire.PluginScan, error)
func (s *Session) InstallPlugin(ctx context.Context, token, scope string) error
func (s *Session) DiscardPluginScan(ctx context.Context, token string) error
func (s *Session) RemovePlugin(ctx context.Context, name, scope string) error
func (s *Session) Skills(ctx context.Context) ([]acpwire.SkillEntry, error)
func (s *Session) LoadSkill(ctx context.Context, scope, source string) (string, error)
func (s *Session) InstallSkill(ctx context.Context, source, scope string) (string, error)
func (s *Session) RemoveSkill(ctx context.Context, scope, source string) error
func (s *Session) SetSkillAutoload(ctx context.Context, name string, on bool) error
func (s *Session) SetSkillGate(ctx context.Context, on bool) error
func (s *Session) ClearSkillGateDecision(ctx context.Context, skill string) error
```

### When both plans 6c and 7b have merged (`internal/acpclient/rpc/sessions.go`)

Whichever of the two plans merges second adds exactly these, in plan 6c's `sessions.go`, over plan 6c's unexported `replaceSession` (R1). Plan 6c's plan gives the same declarations.

```go
// NewSession creates a session in the agent that replaces the current one,
// attaches it, and makes it current. A failure leaves the current session as
// it was.
func (h *Host) NewSession(ctx context.Context, name string) (acpclient.Session, error)

var (
	_ acpclient.Host    = (*Host)(nil)
	_ acpclient.Session = (*Session)(nil)
)
```

`NewSession` returns a nil interface, not a nil `*Session`, on failure.

### The harness (`internal/acp/conformance_test.go`, `conformance_browsers_test.go` new)

```go
// seam gains the twenty-eight methods above, with the interface's signatures.

// serveBrowsers registers the browser handlers on the pipe arm's server over
// the served service.
func serveBrowsers(tb testing.TB, srv *Server, served *service.Session, sessionID string)

func TestBrowserConformance(t *testing.T)
func TestNoteCallBudget(t *testing.T)
func TestBrowserCallBudget(t *testing.T)
```

## Task 1: The wire types

**Goal:** `acpwire` carries the group's params and results.

**Files:** `internal/acpwire/browsercalls.go` (new), `internal/acpwire/browsercalls_test.go` (new).

**Steps:**

1. Add the constants and types in Contracts.
2. Tests in `browsercalls_test.go`:
   - `TestNoteParamsCarryTheNote`: `NoteParams{SessionID: "s1", Note: Note{Text: "hi", Role: RoleUser}}` round-trips and encodes `"note":{"text":"hi","role":"user"}`.
   - `TestTheExistingMethodsKeepACPsKeys`: `CommandCallParams`, `MemoryDeleteParams`, `PluginInstallParams`, `PluginDiscardParams` and `PluginRemoveParams` encode exactly the keys ACP's params use (`name`/`args`, `id`, `scanToken`/`scope`, `scanToken`, `name`/`scope`), besides `sessionId`.
   - `TestTheBrowserResultsRoundTrip`: `PromptHistoryResult`, `FileIndexResult`, `IntelligenceResult`, `PlansResult`, `DiffResult`, `MemoriesResult`, `PluginsResult`, `SkillsResult` and `SkillNameResult` each round-trip with one element, and an empty `PromptHistoryResult` encodes `{}`.

   `TestWireFieldsFollowTheRules` must keep passing.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
mkdir -p /private/tmp/p3-7b-suite
go build ./...
TMPDIR=/private/tmp/p3-7b-suite go test ./internal/acpwire/ -count=1
```

Expected: the package passes, its rules test included.

## Task 2: The notes and the commands on the service

**Goal:** `session/note` and `session/command_action` call the session's service, and a marshal client's `session/command` and `session/command_list` reach it too.

**Files:** `internal/acp/browsers.go` (new), `internal/acp/browsers_test.go` (new), `internal/acp/host.go`, `internal/acpclient/methods.go`, `internal/acpclient/acpclient.go`.

**Steps:**

1. **The manager.** `BrowserManager` holds its `Lookup`. Its `service(sessionID)` refuses an empty ID with `invalidParamsError("sessionId is required")` and an unknown one with `serverErrorf("unknown session: %s", id)`, as `RequestManager.service` does. It has no forwarders, no `Close` and no fence: nothing it serves is a mirror the agent pushes.
2. **The handlers**, each decoding its params type (a decode error is `invalidParamsError`), resolving the service, and returning the service's answer or its error unchanged:
   - `Note`: decode `NoteParams`; call `AddNote(ctx, p.Note)`; return `struct{}{}`.
   - `Command`: decode `CommandCallParams`; return `Command(ctx, Name, Args)`.
   - `CommandList`: decode `ControlParams`; return `acpwire.CommandListResult{Commands: …}` from `Commands()`.
   - `CommandAction`: decode `CommandActionParams`; return `CommandAction(ctx, ID)`.
3. **`registerBrowsers`** as in Contracts. This task registers `session/note` and `session/command_action` with `srv.Handle(method, settingsFailureOf(handler))`, and `session/command` and `session/command_list` with `srv.Handle(method, marshalOr(srv.marshalClient, settingsFailureOf(handler), others[method]))` (decision 4). Tasks 3 and 4 add the rest.
4. **Host wiring.** In `registerHandlers`, right after the five plugin lines (`host.go:620` to `:624`), build `browsers := NewBrowserManager(BrowserManagerConfig{Lookup: manager.Service})` and call `registerBrowsers(srv, browsers, map[string]Handler{acpwire.MethodCommand: cmds.Command, acpwire.MethodCommandList: cmds.CommandList})`. Delete the two lines registering `session/command` and `session/command_list` (`host.go:518`, `:519`). Tasks 3 and 4 add their methods to this map. The barrier is unchanged: a note's patch is the idle stack loop's, which the barrier already fences (`turns`).
5. **The method table.** In `methods.go`, mark `AddNote` and `CommandAction` existing. Rewrite their `ACP:` paragraphs in `acpclient.go` as "ACP: session/note (exists). Synchronous: the call returns once the note's stack patch is applied (R6)." and "ACP: session/command_action (exists)." Add to `Commands`' paragraph: "Read once, on the first read, and kept: the registry does not change while the runtime runs. A marshal client's call is served by the session's service." Add to `Command`'s: "A marshal client's call is served by the session's service."
6. **Tests** in `browsers_test.go`, serving a `BrowserManager` on a pipe to a raw `rpc.Conn` that declares the capability (or not), as `controls_test.go`'s `startControlAgent` serves the control manager; the service's `Deps.Commands` is a `commands.Registry` with test commands.
   - `TestANoteReachesTheTranscript`: `session/note` with a user note appends a user message with its text to the state; a note with the role `assistant` answers an error whose message contains the service's sentence ("a note's role must be system or user").
   - `TestAMarshalCommandRunsOnTheService`: a command whose handler returns a doc with one row action answers that doc with an action ID, and `session/command_action` with the ID runs the action; an `others` stub for `session/command` is never called.
   - `TestAStaleActionKeepsItsSentence`: `session/command_action` with an unknown ID answers an error whose message contains "that action is no longer available".
   - `TestTheCommandListIsTheServices`: `session/command_list` answers the registry's commands in name order, kinds unrewritten; a session with no registry answers an empty list, not an error.
   - `TestACommandWithoutTheCapabilityReachesItsOtherHandler` (V6): without the capability, `session/command` and `session/command_list` reach their `others` stubs.
   - `TestTheNoteAndCommandCallsRefuseBadParams`: a missing `sessionId` is invalid params for each of the four methods; an unknown session is the unknown-session error.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go build ./...
go vet ./internal/acp/... ./internal/acpclient/...
TMPDIR=/private/tmp/p3-7b-suite go test ./internal/acp/ -run 'Note|Command|ACPClientMethodTable|ACPWireCommand' -count=1 -v 2>&1 | tail -40
TMPDIR=/private/tmp/p3-7b-suite go test ./internal/acp/... ./internal/acpclient/... -count=1
```

Expected: the new tests pass; every existing command test passes unchanged (`commands_test.go`, `TestACPWireCommandDispatchListAndRun`), none of them declaring the capability; the method-table tests pass.

Mutation check: register `session/command` without `marshalOr`, straight to `browsers.Command`. `TestACommandWithoutTheCapabilityReachesItsOtherHandler` fails. Restore it.

## Task 3: The history and the project reads on the service

**Goal:** the prompt history, the messages, the file index, the doctor's checks and the plan calls reach the session's service, and a marshal client's `session/diff` and `session/file` reach it too.

**Files:** `internal/acp/browsers.go`, `internal/acp/browsers_test.go`, `internal/acp/host.go`, `internal/acpclient/methods.go`, `internal/acpclient/acpclient.go`.

**Steps:**

1. **The handlers**, as Task 2's:
   - `PromptHistory`: decode `PromptHistoryParams`; return `PromptHistoryResult{Prompts}` from `RecentPrompts(ctx, Limit)`.
   - `PromptHistorySave`: decode `PromptSaveParams`; call `SavePrompt(ctx, Text)`; return `struct{}{}`.
   - `Messages`: decode `MessagesParams`; return `Messages(ctx, p.Query)`.
   - `FileIndex`, `Intelligence`, `Plans`: decode `ControlParams`; return `FileIndexResult`, `IntelligenceResult` and `PlansResult` from the service's answers.
   - `SDDDiscard`: decode `PathParams`; call `DiscardCandidate(ctx, Path)`; return `struct{}{}`.
   - `SDDScaffold`: decode `ControlParams`; return `ScaffoldPlan(ctx)`.
   - `Diff`: decode `DiffCallParams`; return `DiffResult{Diff}` from `Diff(ctx, acpwire.DiffQuery{Base, Head})`.
   - `File`: decode `PathParams`; return `ReadFile(ctx, Path)`.
2. **Registration.** `registerBrowsers` adds the eight new methods (`prompt_history`, `prompt_history_save`, `messages`, `file_index`, `intelligence`, `plans`, `sdd_discard`, `sdd_scaffold`) through `settingsFailureOf`, and `session/diff` and `session/file` through `marshalOr` as Task 2's. In `registerHandlers`, delete the `session/diff` and `session/file` lines (`host.go:566`, `:592`) and add `wtMgr.Diff` and `filesMgr.File` to the `others` map under their `acpwire` names.
3. **The method table.** Mark `RecentPrompts`, `SavePrompt`, `Messages`, `FileIndex`, `Intelligence`, `Plans`, `DiscardCandidate` and `ScaffoldPlan` existing, and rewrite their `ACP:` paragraphs as "ACP: session/prompt_history (exists)." and so on. Rewrite `Diff`'s: "ACP: session/diff (exists). A marshal client's call is served by the session's service, which takes the range's head; ACP's handler, which takes none, serves every other client." Add to `ReadFile`'s: "A marshal client's call is served by the session's service."
4. **Tests** in `browsers_test.go`, over Task 2's agent:
   - `TestThePromptHistoryRoundTrips`: over a state with a migrated database and a project (copy the service's `openHistoryTestDB`, and set `Deps.ProjectID` to its project), `session/prompt_history_save` of "first" then "second", then `session/prompt_history` with limit 10, answers both, newest first.
   - `TestMessagesSelectsOverTheWire`: `session/messages` with `{"role":"user","last":1}` answers the newest user message and the total.
   - `TestTheProjectReadsReachTheService`: `session/file_index` answers `Deps.FileIndex`'s paths; `session/plans` lists a plan written into the plans directory; `session/sdd_scaffold` answers the starter plan's path, and a second call answers `existed`; `session/sdd_discard` of a path outside the plans directory answers an error whose message contains the service's refusal.
   - `TestAMarshalDiffTakesTheHead`: in a git repository with two commits, `session/diff` with `base` the first and `head` the second answers the unified diff between them; an `others` stub for `session/diff` is never called.
   - `TestAMarshalReadFileStaysInTheDirectory`: `session/file` of a file in the working directory answers its content; `../outside` answers the service's refusal.
   - `TestADiffWithoutTheCapabilityReachesItsOtherHandler` (V6): without the capability, `session/diff` and `session/file` reach their `others` stubs.
   - `TestTheProjectCallsRefuseBadParams`.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go vet ./internal/acp/ ./internal/acpclient/...
TMPDIR=/private/tmp/p3-7b-suite go test ./internal/acp/ -run 'Prompt|Messages|Project|Diff|File|Plans|Scaffold|ACPClientMethodTable|Worktree' -count=1 -v 2>&1 | tail -40
TMPDIR=/private/tmp/p3-7b-suite go test ./internal/acp/... ./internal/acpclient/... -count=1
```

Expected: the new tests pass; every existing diff and file test passes unchanged (`worktree_test.go`, `files_test.go`).

Mutation check: make `Diff` drop `Head`. `TestAMarshalDiffTakesTheHead` fails. Restore it.

## Task 4: The browsers on the service

**Goal:** a marshal client's memory, plugin and skill calls reach the session's service, and the four new skill methods do too.

**Files:** `internal/acp/browsers.go`, `internal/acp/browsers_test.go`, `internal/acp/host.go`, `internal/acpclient/methods.go`, `internal/acpclient/acpclient.go`.

**Steps:**

1. **The handlers**, as Task 2's:
   - `MemoryList`: decode `ControlParams`; return `MemoriesResult{Memories}` from `Memories(ctx)`. `MemoryDelete`: decode `MemoryDeleteParams`; call `DeleteMemory(ctx, ID)`.
   - `PluginsList`: return `PluginsResult{Plugins}` from `Plugins(ctx)`. `PluginsInstallScan`: decode `PluginScanParams`; return `ScanPlugin(runCtx, Source)`. `PluginsInstallConfirm`: decode `PluginInstallParams`; call `InstallPlugin(runCtx, ScanToken, Scope)`. `PluginsInstallDiscard`: decode `PluginDiscardParams`; call `DiscardPluginScan(ctx, ScanToken)`. `PluginsRemove`: decode `PluginRemoveParams`; call `RemovePlugin(ctx, Name, Scope)`.
   - `SkillsList`: return `SkillsResult{Skills}` from `Skills(ctx)`. `SkillsLoad`: decode `SkillParams`; return `SkillNameResult{Name}` from `LoadSkill(ctx, Scope, Source)`. `SkillsInstall`: decode `SkillInstallParams`; return `SkillNameResult{Name}` from `InstallSkill(runCtx, Source, Scope)`. `SkillsRemove`: decode `SkillParams`; call `RemoveSkill(ctx, Scope, Source)`. `SkillsAutoload`: decode `SkillAutoloadParams`; call `SetSkillAutoload(ctx, Name, On)`. `SkillGateSet`: decode `SkillGateParams`; call `SetSkillGate(ctx, On)`. `SkillGateClear`: decode `SkillGateClearParams`; call `ClearSkillGateDecision(ctx, Skill)`.

   The calls that answer nothing return `struct{}{}`. `runCtx` is the context `cancellable(ctx)` returns, its cancel called when the handler returns (decision 5).
2. **Registration.** `registerBrowsers` adds `skills_install`, `skills_autoload`, `skill_gate_set` and `skill_gate_clear` through `settingsFailureOf`, and the ten existing methods (`memory_list`, `memory_delete`, the five plugin methods, `skills_list`, `skills_load`, `skills_remove`) through `marshalOr` as Task 2's. In `registerHandlers`, delete their ten lines (`host.go:531`, `:535`, `:603`, `:607`, `:608`, `:620` to `:624`) and add `mem.MemoryList`, `mem.MemoryDelete`, `skillsMgr.SkillsList`, `skillsMgr.SkillsLoad`, `skillsMgr.SkillsRemove` and the five `pluginsMgr` handlers to the `others` map under their `acpwire` names. Leave every other memory, skill and plugin method (`memory_suggestions`, `skills_install_preview`, …) where it is. The `registerBrowsers` call now sits where the five plugin lines were.
3. **The method table.** Mark `InstallSkill`, `SetSkillAutoload`, `SetSkillGate` and `ClearSkillGateDecision` existing, and rewrite their `ACP:` paragraphs as "ACP: session/skills_install (exists)." and so on. Add to the paragraphs of `Memories`, `DeleteMemory`, the five plugin methods, `Skills`, `LoadSkill` and `RemoveSkill`: "A marshal client's call is served by the session's service."
4. **Tests** in `browsers_test.go`, over Task 2's agent:
   - `TestAMarshalMemoryListIsTheProjectsOwn`: over a migrated database with a project memory and a global one (`Deps.ProjectID` set to the project), `session/memory_list` answers only the project's rows, oldest first, with `createdAt` in Unix milliseconds; `session/memory_delete` removes one; without a database both answer `CodeNoDatabase`.
   - `TestAMarshalPluginScanCarriesTheContents`: over a local plugin repository (copy the service's `pluginRepo` and `initPluginRepo`), `session/plugins_install_scan` answers a `PluginScan` naming the command and a token; `session/plugins_install_confirm` with the token installs it, and `session/plugins_list` lists it; `session/plugins_remove` removes it.
   - `TestADiscardedScanCannotBeInstalled`: after `session/plugins_install_discard`, confirming the token answers an error.
   - `TestAMarshalSkillIsKeyedByItsSource`: with one skill in the user store under the home directory `Deps.HomeDir` names, `session/skills_list` lists it with its scope and source; `session/skills_load` by that scope and source answers its name and loads it into the state; `session/skills_remove` by the same key deletes its source.
   - `TestTheSkillSettingsReachTheService`: `session/skills_autoload` writes the name into the user config `Deps.UserConfigPath` names; `session/skill_gate_set` on shows in the state's gate; `session/skill_gate_clear` clears a recorded decision.
   - No test cancels a clone mid-flight: `ScanPlugin` builds its installer with `plugins.NewInstaller()` (`plugins.go:91`), which offers no seam to hold a clone open, and a local clone finishes before a cancel can land. The service's `TestPluginScanWithAnEndedContextStagesNothing` keeps pinning what an ended context does; the `cancellable` wrapping is checked by the branch review.
   - `TestABrowserWithoutTheCapabilityReachesItsOtherHandler` (V6): one subtest per routed method; without the capability, each reaches its `others` stub.
   - `TestTheBrowserCallsRefuseBadParams`.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go vet ./internal/acp/ ./internal/acpclient/...
TMPDIR=/private/tmp/p3-7b-suite go test ./internal/acp/ -run 'Memory|Plugin|Skill|Browser|Scan|ACPClientMethodTable|ACPWire' -count=1 -v 2>&1 | tail -40
TMPDIR=/private/tmp/p3-7b-suite go test ./internal/acp/... ./internal/acpclient/... -count=1
TMPDIR=/private/tmp/p3-7b-suite go test ./internal/acp/ -run 'MarshalPluginScan|DiscardedScan|MarshalSkill' -race -count=3
```

Expected: the new tests pass, three times under `-race`; every existing memory, plugin and skill test passes unchanged (`memory_test.go`, `plugins_test.go`, `skills_test.go`, `TestACPWireMemoryListAndDelete`, `TestACPWireSkillsInstallPreviewConfirmAndList`).

Mutation check: route `session/skills_load` to `skillsMgr.SkillsLoad` for every client. `TestAMarshalSkillIsKeyedByItsSource` fails. Restore it.

## Task 5: The client's notes, commands, browsers and project reads

**Goal:** `rpc.Session` sends the twenty-eight calls and keeps the command list.

**Files:** `internal/acpclient/rpc/browsers.go` (new), `internal/acpclient/rpc/session.go`, `internal/acpclient/rpc/browsers_test.go` (new).

**Steps:**

1. **The calls** in `browsers.go`. Each sends its method with its params type and the session's ID, and returns the result or the error, as `rpc/turns.go`'s do. A list returns the result's slice; `Diff` the result's text; `LoadSkill` and `InstallSkill` the result's name.
2. **The command list.** The session gains `commands []acpwire.CommandInfo` and `commandsSet bool`, guarded by its mutex. `Commands()` returns the kept list without I/O when set; otherwise it sends `session/command_list` under a `commandsCallTimeout` context, keeps the answer (an empty one included) and returns it, as `Host.ProviderTemplates` does; a failed read returns nil and asks again next time. A concurrent first read's answer stands.
3. **`AddNote` is synchronous** (decision 3): it sends `session/note` and returns when the call does, so the barrier's stack patch is applied before it returns.
4. **Tests**, over the scripted agent of `session_test.go`:
   - `TestAddNoteSendsTheNote`: the frame carries the note's text, role and content type.
   - `TestCommandsAreFetchedOnce`: the first `Commands()` sends one `session/command_list`; the second sends nothing and answers the same list; after a failed first read, the next read asks again.
   - `TestTheCommandCallsSendTheirMethods`: `Command` sends `name` and `args`; `CommandAction` sends the ID; both decode the result, a doc with a row action included.
   - `TestTheProjectCallsSendTheirMethods`: `RecentPrompts`, `SavePrompt`, `Messages`, `FileIndex`, `Intelligence`, `Plans`, `DiscardCandidate`, `ScaffoldPlan`, `Diff` (with `base` and `head`) and `ReadFile`.
   - `TestTheBrowserCallsSendTheirMethods`: the memory, plugin and skill calls, `scanToken` among the keys.
   - `TestABrowserCallsErrorComesBack`: a coded error comes back with `acpclient.IsCode` true.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go vet ./internal/acpclient/...
TMPDIR=/private/tmp/p3-7b-suite go test ./internal/acpclient/... -count=1
TMPDIR=/private/tmp/p3-7b-suite go test ./internal/acpclient/rpc/ -race -count=3
```

Expected: every `acpclient` package passes, its import test included, three times under `-race`; plans 3b's to 7a's client tests pass unchanged.

Mutation check: make `Commands()` skip the kept list. `TestCommandsAreFetchedOnce` fails. Restore it.

## Task 6: The conformance harness and the budgets

**Goal:** the group's cases run against the service and against the client over a pipe, and agree (V1); a note and a plain call stay inside R10's budgets.

**Files:** `internal/acp/conformance_test.go`, `internal/acp/conformance_browsers_test.go` (new), `internal/acp/service/notes_test.go`, `internal/acp/service/commands_test.go`, `internal/acp/service/history_test.go`, `internal/acp/service/project_test.go`, `internal/acp/service/browsers_test.go`, `internal/acp/service/plugins_test.go`, `internal/acp/service/skills_test.go`.

**Steps:**

1. **The seam.** Add the twenty-eight methods in Contracts to `seam`.
2. **The pipe arm.** In `openPipeArmsTB`, after `serveControls`, call `serveBrowsers(tb, srv, served, sessionID)`. It builds a `BrowserManager` whose `Lookup` returns the served service for the served ID, and calls `registerBrowsers` with a nil `others`. It adds nothing to the barrier.
3. **The transcript broker** (decision 6). In `serveTranscript`, attach the broker it builds to the served state with `d.State.SetEventBroker(broker)`, before the turn manager is built, with a comment: a runtime attaches its own broker to its state, and without it a call that changes the transcript marks nothing dirty on the pipe arm. If an existing pipe-arm case fails once the broker is attached, that is a difference between the arms the attachment exposed: fix it by step 5's rule, or stop and report it.
4. **The table.** `TestBrowserConformance` runs every case once per arm, as `TestControlConformance` does. Move these tests from the service's tests into it, deleting them there, and copy the helpers they use (`browserDB`, the skill-store and plugin-repository fixtures, the test commands) into the new file:
   - notes: `TestAddNoteAppendsASystemMessage`, `TestAddNoteRecordsTheUsersText`, `TestAddNoteRefusesOtherRoles`;
   - commands: `TestCommandsListsTheRegistry`, `TestCommandRunsTheHandlerOverTheSession`, `TestCommandMintsActionIDs`, `TestAnActionsResultIsMintedToo`, `TestCommandEdgeCases`;
   - history: `TestPromptHistoryRoundTrips`, `TestPromptHistoryWithoutADatabase`, `TestMessagesSelectsByRoleFinalityAndLast`;
   - project: `TestFileIndexReadsTheDeps`, `TestPlansListsTheDiscoveredPlans`, `TestDiscardCandidateRefusesOutsideThePlansDirectory`, `TestScaffoldPlanReportsAnExistingFile`, `TestDiffReadsWithoutMutating`, `TestReadFileStaysInTheWorkingDirectory`, `TestReadFileTruncatesAndSniffsOnlyTheHead`;
   - browsers: `TestMemoriesListsTheProjectsOwnRows`, `TestPluginProjectScopeNeedsTrust`, `TestSkillsListsBothStoresWithLoadReasons`, `TestLoadSkillChecksTheStoreAgainstTheListing`, `TestRemoveSkillUsesTheListedSource`, `TestInstallSkillRefusals`, `TestSetSkillAutoloadWritesTheUserConfig`, `TestSkillGateIsInTheStatus`. Where one of these reads or changes the session's state through `sess.state`, the case uses the state it built.

   These stay in the service's package: the tests that drive the in-process host's `swap` (`TestAClosedSessionTakesNoNotes`); the tests that read the session's staged scans or its constants (`TestOldActionsExpire`, `TestPluginScanReviewsAndInstalls`, `TestPluginInstallRejectsAChangedClone`, `TestPluginScanWithAnEndedContextStagesNothing`, `TestDiscardPluginScanDeletesTheClone`, `TestRemovePluginDropsTheLockEntry`); `TestClosingDiscardsPluginScans`, which needs plan 6c's `Close`; the helper-level tests (`TestRunErrorCarriesTheNotice`, `TestWireConstantsMatchTheSession`, the `TestIntelligenceChecks…` tests); and `TestBrowsersWithoutADatabase`, which plan 6c's `ListSessions` half shares. Add these new cases instead:
   - `aNoteIsInTheStackAtOnce` (V3): read `Stack(0, false)` once, `AddNote` a system note, and the very next `Stack(0, false)` holds a node carrying the note's text, with no wait.
   - `memoriesWithoutADatabaseAreCoded`: `Memories` and `DeleteMemory` fail with `CodeNoDatabase`.
   - `intelligenceAnswersFourChecks`: `Intelligence` on a state with a migrated database answers the four checks, named Index, Embeddings, LSP and Watcher.
   - `aScannedPluginInstallsAndLists`: `ScanPlugin` of a local repository, `InstallPlugin` with its token into the global scope, and `Plugins` lists it; `RemovePlugin` removes it.

   Each case builds its `Deps` and opens the arm before acting. Where a service test waits on one `Changed` signal and then reads, the case waits until the read shows the change, with a 5s deadline. Where a case compares a plain error's text, it compares with `strings.Contains`, which holds before and after plan 6c's error rule.
5. **A difference is a bug, never the case's,** as in plans 5, 6b and 7a: fix the wire type, the handler or the client so both arms agree (a nil slice against an empty one is the client's to match). If the fix would change what the TUI sees today, stop and report it instead.
6. **The budgets (R10).**
   - `TestNoteCallBudget`: the pipe arm over `largeStackSession`. Read `Stack(0, false)` once, which starts the idle loop. Time 30 `AddNote` calls of a one-line system note; each returns after the barrier flushed the note's patch and the client applied it. After the last, assert the last node `Stack(0, false)` answers carries the last note's text, so the timing measured the flush. The median must be under 10ms, R10's bound for a call that pays one flush (plan 4c measured that flush at 4.3ms). Also time, on a second `largeStackSession` through the service arm, 30 rounds of `AddNote` and `Stack(0, false)`: what the TUI pays in process after a note. Log both medians with `t.Logf` for the Implementation record.
   - `TestBrowserCallBudget`: the pipe arm over `largeStackSession`. Time 30 `SetSkillGate` calls alternating on and off; each includes its barrier and the `session_status` update that carries the gate. The median must be under 1ms. Log it.

   Skip both under `-short` and with the race detector, as `TestControlCallBudget` does; call `withoutMemoCheck(t)` first, as `TestStackBusyFlipBudget` does.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
TMPDIR=/private/tmp/p3-7b-suite go test ./internal/acp/ -run 'TestBrowserConformance|TestNoteCallBudget|TestBrowserCallBudget|TestControlConformance|TestSettingsConformance|TestRequestConformance|TestStatusConformance|TestTranscriptConformance|TestStackBusyFlipBudget' -count=1 -v 2>&1 | tail -80
TMPDIR=/private/tmp/p3-7b-suite go test ./internal/acp/... -count=1
TMPDIR=/private/tmp/p3-7b-suite go test ./internal/acp/ -run 'TestBrowserConformance' -race -count=3
```

Expected: every case passes on both arms, three times under `-race`; both budget tests pass and log their medians; the earlier plans' cases still pass with the broker attached and the browser manager in the pipe arm; the service package passes without the moved tests.

Mutation checks:
- Drop the `SetEventBroker` line from `serveTranscript`. `aNoteIsInTheStackAtOnce` fails on the pipe arm. Restore it.
- Make the client's `LoadSkill` send `name` instead of `source`. The moved `TestLoadSkillChecksTheStoreAgainstTheListing` case fails on the pipe arm. Restore it.

## Task 7: The docs and full verification

**Goal:** the spec records what plan 7b built and the counts it moved.

**Files:** `docs/daemon/p3-acp-transport.md`, `AGENTS.md`, and, only if plan 6c merged first, `internal/acpclient/rpc/sessions.go` and `internal/acpclient/rpc/sessions_test.go`.

**Steps:**

1. **The counts.** Re-measure the spec's "Where the code is today" counts as plan 7a did. Expect, if plan 6c has not merged: 111 rows, 101 of them an existing ACP method (fourteen more: `AddNote`, `CommandAction`, `RecentPrompts`, `SavePrompt`, `Messages`, `FileIndex`, `Intelligence`, `Plans`, `DiscardCandidate`, `ScaffoldPlan`, `InstallSkill`, `SetSkillAutoload`, `SetSkillGate` and `ClearSkillGateDecision`), 8 new in P3 carried by 8 distinct new methods, and 2 carrying none; and 131 registered methods (117 plus the fourteen), plus the `session/cancel` notification. If plan 6c merged first, measure the merged tree and record what it shows.
2. **The Implementation record.** Add a "Plan 7b — notes, commands, browsers and project data" entry after plan 6c's (after plan 7a's, if plan 6c has not merged). It names: the browser manager and its fourteen new methods; the fourteen existing methods through `marshalOr`, each wrapped so it keeps its sentence; the cancellable installs; the command list fetched once, and why `commands_changed` was dropped; `AddNote` staying synchronous, with both budget medians and the in-process cost beside them; the transcript broker attached in the harness; `rpc.Session`'s twenty-eight methods; the harness's cases; and `TestBrowserCallBudget`'s median.
3. **`AGENTS.md`.** The map's `internal/acp/` line gains "browsers" in its list, last.
4. **Main may have moved.** If plan 6c merged while this branch ran, merge `main` into the branch before the full verification, resolve the conflicts as "Integration with plan 6c" says, and then add the declarations under "When both plans 6c and 7b have merged" to `rpc/sessions.go`, with one test in `sessions_test.go`: `TestNewSessionIsTheReplacement`, which runs `NewSession` against the scripted agent and gets the attached session back as an `acpclient.Session`, and on an agent error gets a nil interface. If plan 6c has not merged, leave them to it.
5. **Full verification:**

```bash
cd /Users/alecpullen/projects/marshal
mkdir -p /private/tmp/p3-7b-suite
gofmt -l internal cmd
go vet ./...
TMPDIR=/private/tmp/p3-7b-suite go test ./... -timeout 20m 2>&1 | tail -150
```

Expected:
- `gofmt -l` prints nothing, and `go vet` is clean;
- every package passes. Known load flakes, each to be rerun alone before treating it as new:
  - `internal/acp`: `TestStackPatchApplyBudget`, `TestStackRoundTripBudget`, `TestResponseBarrierBudget`, `TestNoticeCallBudget`, `TestAnswerCallBudget`, `TestStackBusyFlipBudget`, `TestControlCallBudget`, `TestSettingsCallBudget`, and now `TestNoteCallBudget` and `TestBrowserCallBudget`;
  - `internal/app`: `TestRuntimeAccessSerializesWithReloadAndQuiesce`, `TestRuntimeCloseRunsNoGitGC`;
  - `internal/snapshot`: `TestRunManagedCancellationKillsTree`;
  - `web/bridge`: `TestRecipeRunMaxUSDSetsAPausingCapForTheRun`, which hangs about one run in three in its fake agent. A hang there is known.

No manual run: nothing a user sees changes.

## Integration with plan 6c

Plan 6c adds the end manager, the replacement and the marshal error rule. Whichever branch merges second merges `main` first and resolves these, keeping both sides:
- **`host.go`'s `registerHandlers`:** this plan deletes the fourteen `Handle` lines of the commands, memories, diff, file, skills and plugins, and registers them through `registerBrowsers` where the five plugin lines were; plan 6c moves three pipeline lines into the controls' `others` map, builds the end manager after `settings`, replaces the `session/close` and `session/list` lines with `registerEnds`, appends `ends` to the barrier and closes it in the cleanup. The hunks are different lines; keep both. This plan adds no fence, so the barrier's order is plan 6c's.
- **`conformance_test.go`:** both add methods to `seam`; plan 6c also adds `ListSessions` to `hostSeam`, and a `serveEnds` line and barrier entry to the pipe arm; this plan adds a `serveBrowsers` line and the `SetEventBroker` line in `serveTranscript`. Keep every line.
- **`rpc/session.go`:** this plan adds the command list's fields to `Session`; plan 6c adds an `end_work` case to `routeNotify` and a `host` field. Keep both.
- **`acpclient/methods.go` and `acpclient.go`:** different rows and different methods' docs; no line is shared.
- **`AGENTS.md`:** both add a word to the `internal/acp/` line; keep both, "end flow" before "browsers".
- **Errors.** Once plan 6c's rule has merged, this plan's `settingsFailureOf` wrappers are redundant but harmless, and the `strings.Contains` comparisons still hold; leave them.
- **The interface checks** belong to the second merge: it adds the declarations under "When both plans 6c and 7b have merged" (Task 7, step 4).
- **The spec:** both add an Implementation record entry and re-measure the counts. Record the merged tree's counts: after both, 111 rows, 109 of them existing, none new, 2 carrying none, and 139 registered methods.

## Self-review

- **Spec coverage.** The 2026-11-08 decision, extended to this group: Tasks 2 to 4 (`marshalOr`), proved by the `…WithoutTheCapabilityReachesItsOtherHandler` tests and the conformance table. R3's `Commands` row (fetched once and kept): Task 5. R6 and R10 (`AddNote` synchronous, one flush): decision 3, `aNoteIsInTheStackAtOnce` and `TestNoteCallBudget`. R2's cancellation: the `cancellable` installs (decision 5), checked by review; no clone can be held open in a test. R2 and V3: `aNoteIsInTheStackAtOnce`, and every moved case that reads after a call on the pipe arm. R9 and V6: the `…WithoutTheCapability…` tests, and the unchanged ACP tests for the routed methods. R10: `TestNoteCallBudget`, `TestBrowserCallBudget`. V1: Task 6. V8: Task 7.
- **What could go wrong.**
  - **A note that costs more than a frame on a long session.** One flush of the 2,000-call session is 4.3ms after plan 4c, which the TUI already pays in process on its next refresh; the budget stops the plan if the wire makes it worse than 10ms. If it fails, report the two medians; do not make the call asynchronous without the user (decision 3's ordering argument).
  - **The broker attachment changes an earlier case.** It is a harness change shared by every pipe-arm case; a case that fails is handled by Task 6, step 3.
  - **A command that needs the TUI.** The service runs a command's handler over the session, as in process; a TUI-only entry has no handler and answers an empty result, as in process (`Command`'s doc).
  - **Plan 6c.** The shared files and their resolutions are listed above.
