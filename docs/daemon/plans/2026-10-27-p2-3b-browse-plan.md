# P2 plan 3b: the browse actions — implementation plan

Date: 2026-10-27. Status: ready for review.
Base: `main` once plan 3a (branch `p2-3a-projection`) has merged.
- Written while plan 3a ran. Anchors were checked at `75ee412d`, and against plan 3a's tasks wherever 3a changes the same code. Line numbers are there to find the code fast; the function names are the anchors. Plan 3a's Task 4 shortens `refreshViewport` by about eight lines, so its line numbers below are approximate.
- The JSON literals in Contracts were produced by encoding the declared types, not written by hand.

Approved design: [p2-client-seam.md](../p2-client-seam.md): C2 (reads and calls), C3 (wire rules), C4 (the adapter), C6 (transcript: `StackNode` and `Callers`), Migration order item 3b and its revision record entry "against plan 3a", and tests T1, T1b, T2, T4 and T5.
Previous plan: [plan 3a, the projection](2026-10-27-p2-3a-projection-plan.md).
Authoring workflow: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md).

**How this plan is written.** No prototype was built, and the plan embeds no implementation code. The Contracts section holds declarations and the exact strings the tests pin. Every other step states its decision in prose. Executing the plan also tests how well Marshal follows a plan. "As `X` does" means `X` exists and is the model to copy.

**Goal:** the inspector, copy, open and the selection's full-subject copy read a node's wire form and its uncapped detail through the session, and callers lookups run agent side. Nothing a user sees changes, apart from the two edge cases in decision 6.
- `acpwire` holds `NodeDetail` and its parts, `StackNodeResult`, and the two decode types the TUI reads hooks and sandbox data with.
- `viewmodel` builds a node's detail (`StackNodeOf`) for both `acp`'s `session/stack_node` and the adapter. `NodeDetail` gains `step` and `completion`, `CallDetail` gains `summary`, and `WireTool` gains `callers`.
- `Session` gains `StackNode` and `Callers`. The adapter holds the reference finder.
- The TUI keeps a wire index of the transcript it renders (`m.wire`), projected from its own tree.
- `browse_inspect.go` and `browse_actions.go` stop reading audit events and session steps. `callers.go` stops reading the transcript and the finder.
- T1b gains `Transcript`, and T1's allowlist loses five entries.

**Non-goals:**
- **The renderer is unchanged.** Plan 3c ports the row renderers to the wire index.
- **`browseTree` stays for two copy paths.** A receipt copies `renderReceipt`'s text, and a run event copies `renderTranscriptItem`'s expanded text. Both go through renderers that plan 3c ports, and 3c deletes `browseTree` and `indexTree` with them.
- **The drill-in is unchanged.** `subagentAtCursor` and `boxAgentAtCursor` still look the subagent view up in the state (plan 3d). Only the ID they look up comes from the wire.
- **No detail for expanded rows.** Plan 3c adds a run event's whole body, a completion's own truncation flag, a message's whole reasoning and a running call's whole output.
- **No ACP behaviour change.** `session/stack_node` answers the same JSON, plus the new optional fields.
- **No web change.** The web UI ignores the new fields (spec F4).
- **No changelog entry.**

**Assumptions:**
- Every Go test command below runs with `TMPDIR=/private/tmp/p2-3b-suite`. Create the directory first: `mkdir -p /private/tmp/p2-3b-suite`. Keep the path short: a long one breaks the unix-socket tests in `internal/acp` and `web/bridge`.
- **Pre-existing failures** are plan 3a's baseline, copied into Task 9. They were taken at `75ee412d`. A failure outside that list is checked in a detached worktree at this plan's base before it is blamed on a task. A flaky test that fails in a full run counts as pre-existing only when it passes on its own with `-count=3`.
- **T6's goldens never change.** No task runs `TestTranscriptGoldens` with `-update`. Every task's Verify runs the `tui` package, which runs the goldens.

**Decisions this plan makes beyond the spec** (decisions 1 to 5 are also recorded in the spec's revision record):

1. **The TUI keeps a wire index of what it renders.**
   - `refreshViewport` projects the tree it built, with `viewmodel.Project(turns, rctx.workdir)`, and keeps the nodes by ID in `m.wire`. The band node is not in it.
   - It projects after the content-signature check, so a frame whose content did not change costs nothing.
   - Browse actions read node fields from `m.wire`, which is what is on screen. They ask `StackNode` only for what the wire caps or leaves out.
   - Plan 3c's renderers read the same index, and plan 3d replaces its source with `Session.Stack`.
2. **`NodeDetail` moves to `acpwire` with its JSON unchanged.**
   - The moved types are `NodeDetail`, `CallDetail`, `ExecutionDetail`, `SymbolDetail`, `NoticeDetail`, `ThoughtDetail`, `TodoDetail` and `RelationDetail`.
   - `CallDetail.Hooks` becomes a `json.RawMessage` holding `json.Marshal(ev.Hooks)`. `registry.HookMetadata` names a field `failed_open`, which C3's camelCase rule refuses inside `acpwire`, and the raw bytes are what ACP sends today. It is left empty when the call has no hooks, as `omitempty` left the slice before.
   - `CallDetail.Sandbox` is raw JSON already.
   - Two decode types give the TUI the parts it reads: `HookDetail` and `SandboxDetail` (Contracts). The sandbox JSON spells its keys Go's way (`"Enabled"`, `"KilledReason"`). `encoding/json` matches keys case-insensitively, so `SandboxDetail` decodes them.
3. **`StackNodeResult` lists `Detail` before `Node`.** `encoding/json` sorts a map's keys, so the map ACP returns today (`stacknode.go:328`) encodes `detail` first. This field order keeps the bytes.
4. **What the detail gains.**
   - **`NodeDetail.step`** (`StepDetail`) is a step record, with its owner, model, provider, "why" line and stored todo:
     - for a call row, the record of its first call's step (`State.Step(ev.StepID)`). It is absent when the call has no step or the state no longer has the record;
     - for a beat, its first response's record (`si.Step`). That record's ID is 0 for a loose beat;
     - `why` is today's `whyText` (`browse_inspect.go:113`): `StripEmphasis` of the first sentence of `State.StepNarration(id)`, and "" for ID 0;
     - `todoId` is the record's stored binding (`Step.TodoID`), which the inspector's "task" field shows today. It is not the beat's effective todo, which `WireStep.todoId` carries.
   - **`NodeDetail.completion`** (`CompletionDetail`) is a completion's retained output and its report, uncapped. The wire keeps only the output's last and the report's first `WireTextCap` bytes.
   - **`CallDetail.summary`** is the call's `ResultSummary`, uncut. Copy takes a call's diff, else its output, else its summary, as `toolCopyText` (`browse_actions.go:81`) does. `CallDetail.output` holds the content or the summary only for a tool that is not an edit, so for an edit with an empty result only `summary` has it.
5. **`WireTool.callers`** is true for a one-call row whose call changed a resolved symbol, in a subject-first tool other than `file.read`. Those are the rows `callerQueryCmds` asks about today (`callers.go:59-69`).
   - `viewmodel.CallersSymbol` is that test, and the adapter's `Callers` uses it to pick the symbol.
   - The TUI asks once per flagged row, and only while the view is not drilled in. Today's scan reads the session's own transcript, and `m.wire` holds the child's while drilled in. A row that settles during a drill-in is asked on the first tick after the view returns.
6. **Two differences a user could see,** both edge cases. Everything else matches field for field, and the tests name each field.
   - The inspector's subject, a beat's "tools" section and a beat's copied rows use the wire's `target`. That is `ScrubText` of today's raw `ToolTarget`, so a command with a line break shows on one line, and a control character is dropped. The web UI shows the same.
   - The inspector's times are built from Unix milliseconds in local time. A record loaded from the database with a UTC timestamp printed in UTC before.
7. **The adapter's `StackNode` builds the tree with `busy` false.** `busy` decides only which beat is live and whether the turn's receipt exists. Neither a node's ID nor its detail depends on it. ACP keeps the `busy` its turn manager knows.
8. **Subagents.** `StackNode` with a subagent ID reads that child's transcript, found with `childState` (`inproc/sheet.go:195`) as `Sheet` finds it. Unlike `Sheet`, an ID that names no child is an error. A node ID like `step:3` exists in every session, so falling back to the session's tree would answer for the wrong node.
9. **Errors read as "nothing here".** When `StackNode` fails, each action falls back to the message today's missing node gives:
   - the inspector: "Nothing to inspect here";
   - copy: "Nothing to copy";
   - open: "No file to open here".

   A failed `Callers` call is cached like an empty answer: no callers line, and no second ask.
10. **The reference finder moves into the adapter.**
    - `inproc.Deps.ReferenceFinder` is an interface the adapter declares, with today's one method. `lsp.SymbolQuery` satisfies it.
    - `app.go` sets it when the runtime has an LSP manager, as it passes `tui.WithReferenceFinder` today (2861). Task 4 adds it, and Task 7 deletes the TUI's option once the TUI asks the session, so lookups keep working at every commit.
    - The TUI keeps `lspCtx` (`model.go:353-360`) as the context of its `Callers` calls, so shutdown still cancels a lookup in flight.
11. **The guard and the allowlist.**
    - T1b gains `Transcript`: `callers.go:58` was the TUI's last `m.state.Transcript()` read.
    - T1 loses five entries: `session` and `registry` from `browse_inspect.go` (Task 5), `registry` from `browse_actions.go` (Task 6), and `session` and `registry` from `callers.go` (Task 7). No key is dropped.

**Execution:**
- Branch first: `git switch -c p2-3b-browse` from `main`, after plan 3a has merged.
- If `git status` shows `docs/daemon/p2-client-seam.md` or this plan uncommitted, commit them first as explicit paths, with the message `p2-3b-browse: record the plan 3b revisions and plan 3b`.
- Run the tasks in order. Run each task's Verify and commit it as `p2-3b-browse: task N — <title>`.
- Stage explicit paths only. Never `git add -A` without a path.
- Run `gofmt -w` on every Go file a task touches before its Verify.

## Contracts

**`internal/acpwire/stacknode.go` (new), after Task 1.**
- Moved verbatim, doc comments included, from `acp/stacknode.go`: `NodeDetail` (22), `CallDetail` (31), `ExecutionDetail` (64), `SymbolDetail` (149), `NoticeDetail` (156), `ThoughtDetail` (162), `TodoDetail` (168) and `RelationDetail` (177).
- `CallDetail.Hooks` changes type only. Its comment says the hooks travel as the JSON `registry.HookMetadata` encodes to, whose `failed_open` name the wire rules would refuse in a typed field, and that `HookDetail` decodes what the TUI reads.
- New:

```go
// StackNodeResult is session/stack_node's answer: one node's wire form and the
// detail the projection caps or leaves out. Detail comes first so the JSON
// matches the map the handler sent before this type existed.
type StackNodeResult struct {
	Detail NodeDetail `json:"detail"`
	Node   WireNode   `json:"node"`
}

// HookDetail is what a client reads of one hook run in CallDetail.Hooks.
type HookDetail struct {
	Event    string `json:"event"`
	Decision string `json:"decision,omitempty"`
}

// SandboxDetail is what a client reads of CallDetail.Sandbox. That JSON spells
// its keys in Go's field spelling ("Enabled", "KilledReason");
// encoding/json matches keys case-insensitively, so these names decode it.
type SandboxDetail struct {
	Enabled         bool   `json:"enabled,omitempty"`
	Backend         string `json:"backend,omitempty"`
	NetworkIsolated bool   `json:"networkIsolated,omitempty"`
	KilledReason    string `json:"killedReason,omitempty"`
}

// ApprovalNotRequired is the WireCall.Approval value of a call that needed no
// approval.
const ApprovalNotRequired = "not_required"
```

**After Task 3.** Each struct gains these fields, after its last existing field:

```go
// NodeDetail
	Step       *StepDetail       `json:"step,omitempty"`
	Completion *CompletionDetail `json:"completion,omitempty"`

// CallDetail
	Summary string `json:"summary,omitempty"` // the call's own summary, uncut

// WireTool (acpwire/stack.go)
	// Callers says the row's call changed a resolved symbol whose callers
	// Session.Callers can look up.
	Callers bool `json:"callers,omitempty"`
```

and `stacknode.go` gains:

```go
// StepDetail is a step record: a call's own step, or a beat's first. Why is
// the first sentence of the step's narration, emphasis stripped. TodoID is the
// todo the step was bound to when it started.
type StepDetail struct {
	ID       int64  `json:"id,omitempty"`
	Owner    string `json:"owner,omitempty"`
	Model    string `json:"model,omitempty"`
	Provider string `json:"provider,omitempty"`
	Why      string `json:"why,omitempty"`
	TodoID   string `json:"todoId,omitempty"`
}

// CompletionDetail is a completion's retained output and its report, neither
// capped.
type CompletionDetail struct {
	Output string `json:"output,omitempty"`
	Report string `json:"report,omitempty"`
}
```

**`internal/viewmodel`, after Tasks 2 and 3.**

```go
// detail.go (Task 2)

// StackNodeOf finds nodeID in tree, st's built transcript, and returns the
// node's wire form, its parent's ID set, with the detail the projection caps
// or leaves out: what session/stack_node answers. found is false when tree
// has no node with that ID.
func StackNodeOf(st *session.State, tree []*Node, nodeID string) (res acpwire.StackNodeResult, found bool)

// rows.go (Task 3)

// CallersSymbol is the symbol a callers lookup asks about for ev: the first
// resolved symbol of a call that changed it, in a subject-first tool other
// than file.read. ok is false when there is none.
func CallersSymbol(ev registry.AuditEvent) (ref registry.SymbolRef, ok bool)
```

`detail.go` also holds, unexported and moved from `acp/stacknode.go`: `callDetail`, `activeCallDetail`, `executionDetail`, `copyExitCode`, `indentJSON`, `todoDetail` and `keysOf`. `isDiffToolName` and `unixMs` are not moved: `viewmodel`'s own `isDiffTool` (`stack.go:196`) and `ms` (`wire.go`) do the same.

**`internal/acpclient`, after Task 4.** `Session` gains, after `RefreshSheet`:

```go
	// StackNode is one transcript node's wire form with the detail the
	// projection caps or leaves out (C6): a call's whole arguments, output,
	// diff and summary and its step; a beat's narration, reasoning and step;
	// a completion's whole output and report. subagentID 0 reads the
	// session's transcript, and any other ID the transcript of the subagent
	// with that ID anywhere in the session's tree. It fails for an ID that
	// names no child, and for a node the transcript does not hold.
	//
	// ACP: session/stack_node (exists).
	StackNode(ctx context.Context, nodeID string, subagentID int64) (acpwire.StackNodeResult, error)

	// Callers lists what references the symbol a call row changed (C6). It
	// returns nil and no error when the row changed no resolved symbol, when
	// no language server is wired or ready, and when the node is gone. A
	// caller asks once per row and keeps the answer, a nil one included.
	//
	// ACP: session/callers (new in P3).
	Callers(ctx context.Context, nodeID string) ([]string, error)
```

and `Methods` gains, after the `RefreshSheet` row:

```go
	{Iface: "Session", Name: "StackNode", ACP: "session/stack_node", Exists: true},
	{Iface: "Session", Name: "Callers", ACP: "session/callers"},
```

**`internal/acpclient/inproc`, after Task 4.**

```go
// ReferenceFinder resolves what references a symbol. ok is false when no
// language server is ready for the file's language. lsp.SymbolQuery
// satisfies it.
type ReferenceFinder interface {
	References(ctx context.Context, filePath string, line, col int) ([]string, bool)
}

// Deps gains, after ProjectID:
	// ReferenceFinder answers Callers. Nil means no callers lookups.
	ReferenceFinder ReferenceFinder

func (s *Session) StackNode(ctx context.Context, nodeID string, subagentID int64) (acpwire.StackNodeResult, error)
func (s *Session) Callers(ctx context.Context, nodeID string) ([]string, error)
```

`Session` gains `refs ReferenceFinder`, which `newSession` sets from `d.ReferenceFinder`.

**`internal/app/tui`, after Tasks 5 to 7.**

```go
// model.go (Task 5): Model gains, after browseTree:
	// wire indexes the transcript's wire nodes by ID, from the last refresh
	// that changed the content. Browse actions read node fields from it.
	wire map[string]*acpwire.WireNode

// viewport_render.go (Task 5)
// wireIndex keeps tree's nodes by ID. The pointers address tree's slice.
func wireIndex(tree acpwire.WireTree) map[string]*acpwire.WireNode

// browse.go (Task 5)
// wireNode is id's wire form from the last refresh, or nil.
func (m *Model) wireNode(id viewmodel.NodeID) *acpwire.WireNode
// nodeDetail asks the session for id's detail, in the drilled-in subagent's
// transcript while the view is drilled in. ok is false when the call fails.
func (m *Model) nodeDetail(id viewmodel.NodeID) (acpwire.StackNodeResult, bool)

// browse_inspect.go (Task 5)
func (m *Model) inspectorDetail(id viewmodel.NodeID) (inspector.Detail, bool)
func (m *Model) toolDetail(w *acpwire.WireNode, d acpwire.NodeDetail) inspector.Detail
func (m *Model) stepDetail(w *acpwire.WireNode, d acpwire.NodeDetail) inspector.Detail
func completionDetail(c *acpwire.WireCompletion, d *acpwire.CompletionDetail) inspector.Detail
func actorText(owner, model, provider string) string
// callPlace is a call row's first call's place among its beat's calls, and the
// beat's call count. ok is false outside a beat.
func (m *Model) callPlace(w *acpwire.WireNode) (index, total int, ok bool)
// beatCalls counts a beat's calls, a running one included, as
// countToolCalls (steps.go:414) does.
func (m *Model) beatCalls(w *acpwire.WireNode) int

// browse_actions.go (Task 6)
func (m *Model) nodeCopyText(id viewmodel.NodeID) string
func completionCopyText(c *acpwire.WireCompletion, d *acpwire.CompletionDetail) string
// callCopyText is what copy takes from one call: its diff, else its output,
// else its summary.
func callCopyText(c acpwire.CallDetail) string
// openCall is what the open action reads of one call.
type openCall struct {
	tool   string
	args   string   // the whole arguments, as JSON
	files  []string // the files it changed
	output string   // its whole output; "" when it printed nothing
}
// fileIn resolves the file calls are about, under root: a path argument, else
// a changed file, else for a shell call the first path:line in its output.
func fileIn(root string, calls []openCall) (path string, line int)
func (m *Model) nodeFile(id viewmodel.NodeID) (path string, line int)

// browse.go (Task 6)
func subagentIDOf(w *acpwire.WireNode) int64

// callers.go (Task 7)
type callersMsg struct {
	key     viewmodel.NodeID
	callers []string // nil: no callers line, and no second ask
}
func queryCallersCmd(ctx context.Context, sess acpclient.Session, key viewmodel.NodeID) tea.Cmd
```

**Test helpers.**

```go
// inproc/callers_test.go (Task 4) and tui/callers_test.go (Task 7)
// fakeFinder records each lookup and answers refs and ok.
type fakeFinder struct {
	refs   []string
	ok     bool
	calls  int
	asked  []string // "file:line:col" of each lookup
	failIf func()
}
```

`tui/callers_test.go`'s `fakeFinder` exists (`callers_test.go:13`) and gains `asked`. `inproc/callers_test.go` declares a type with the same name and fields.

**Strings the tests pin:**

| Where | Text |
|---|---|
| `TestStackNodeResultJSON`, a result with every field set (Task 1) | `{"detail":{"calls":[{"callId":"c1","stepId":3,"toolName":"shell.run","args":"{\n  \"command\": \"go test\"\n}","originalArgs":"{\n  \"command\": \"go vet\"\n}","output":"FAIL","diff":"--- a/x\n+++ b/x\n","error":"boom","exitCode":1,"model":"qwen3","finishReason":"tool_calls","rewritten":true,"hooks":[{"event":"PreToolUse","decision":"allow","failed_open":true}],"sandbox":{"Enabled":true,"Backend":"restricted","KilledReason":"timeout"},"symbols":[{"file":"a.go","name":"Run","kind":"function"}],"notice":{"kind":"hint","text":"use test.run"},"execution":{"version":1,"executionId":"e1","displayId":"job-1","conversationId":"conv","actor":"parent","turnId":"t1","stepId":"3","toolCallId":"c1","cwd":"/w","generation":2,"pid":42,"state":"failed","outcome":"nonzero_exit","attachment":"background","wait":"30s","detachReason":"wait expired","detachedAt":1000,"stopCause":"user","stopRequestedAt":2000,"exit":1,"truncated":true,"startedAt":500,"endedAt":3000}}],"narration":["Running the tests."],"thinking":[{"text":"hmm","durationMs":700}],"todo":{"id":"t2","content":"Run the tests","status":"in_progress","startedAt":100,"completedAt":200},"relations":{"causedBy":["step:1"],"fixedBy":["step:4"]}},"node":{"id":"tool:3:c1","kind":"tool","parent":"step:3"}}` |
| the same, the zero result | `{"detail":{},"node":{"id":"","kind":""}}` |
| `TestDetailAdditionsJSON`, the new detail fields (Task 3) | `{"calls":[{"toolName":"file.edit","summary":"edit failed"}],"step":{"id":3,"owner":"reviewer #1","model":"qwen3","provider":"local","why":"Running the tests.","todoId":"t2"},"completion":{"output":"ok\n","report":"## Review"}}` |
| the same, a row a callers lookup asks about | `{"id":"tool:3:c1","kind":"tool","tool":{"name":"file.edit","display":"Edit","verb":"edit","callers":true}}` |
| `StackNode` on a closed session, and `Callers` on one | `&acpwire.Error{Code: acpwire.CodeSessionClosed, Message: "inproc: the session was replaced"}` (the existing `errSessionClosed`) |
| the inspector and copy fallbacks (unchanged) | "Nothing to inspect here", "Nothing to copy", "No file to open here" |
| T1b violation (unchanged) | `<file>:<line> reads state.<Name>, which moved behind the seam in plan <plan>: read it from m.sess` |

## Task 1: The detail types move to `acpwire`

**Goal:** `acpwire` holds `NodeDetail` and its parts, `StackNodeResult` and the two decode types. `acp` answers `session/stack_node` with a `StackNodeResult`, and its JSON is unchanged.

**Files:** `internal/acpwire/stacknode.go` (new), `internal/acpwire/stacknode_test.go` (new); `internal/acp/stacknode.go`, `stacknode_test.go`, `stack_test.go`.

**Steps:**

1. **`acpwire/stacknode.go`** holds the moved types and the new declarations exactly as in Contracts.
2. **`acp/stacknode.go`:**
   - delete the moved types;
   - `callDetail` sets `Hooks` to `json.Marshal(ev.Hooks)` only when `len(ev.Hooks) > 0`, ignoring the marshal error as `callDetail` already ignores `json.Marshal(ev.Sandbox)`'s;
   - `StackNode` returns `acpwire.StackNodeResult{Detail: detail, Node: node}` in place of the map (328);
   - the helpers keep building `acpwire` values. They move in Task 2.
3. **Tests:**
   - `callNode` (`stacknode_test.go:57`) returns an `acpwire.StackNodeResult`. Every `got["detail"].(NodeDetail)` becomes `got.Detail`, and `got["node"].(viewmodel.WireNode)` (or `acpwire.WireNode` after plan 3a) becomes `got.Node`. The same applies at `stack_test.go:277`;
   - **`acpwire/stacknode_test.go`**:
     - `TestStackNodeResultJSON` marshals the two values of the strings table and compares each with its literal. It builds the full one from the values its literal spells out. Its `Hooks` and `Sandbox` are the raw JSON the literal shows;
     - `TestHookAndSandboxDetailsDecode` unmarshals `[{"event":"PreToolUse","decision":"block","failed_open":true}]` into `[]HookDetail`, and `{"Enabled":true,"Backend":"restricted","NetworkIsolated":true,"KilledReason":"timeout"}` into `SandboxDetail`. Every field the types declare is set.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go build ./...
grep -nE '^type (NodeDetail|CallDetail|ExecutionDetail|SymbolDetail|NoticeDetail|ThoughtDetail|TodoDetail|RelationDetail) ' internal/acp/*.go
TMPDIR=/private/tmp/p2-3b-suite go test ./internal/acpwire/ -v 2>&1 | grep -E '^(--- |ok|FAIL)'
TMPDIR=/private/tmp/p2-3b-suite go test ./internal/acp/ -run 'StackNode|Stack' -v 2>&1 | grep -E '^(--- FAIL|ok|FAIL)'
go vet ./internal/acpwire/ ./internal/acp/
```

Expected:
- the grep prints nothing;
- the two new tests pass, and so do `TestWireFieldsFollowTheRules` and `TestWireImportsOnlyTheStandardLibrary`, now over the moved types;
- the `acp` stack and node tests pass.

## Task 2: The detail builder moves to `viewmodel`

**Goal:** one function builds a node's `session/stack_node` answer, and `acp`'s handler calls it.

**Files:** `internal/viewmodel/detail.go` (new), `internal/viewmodel/detail_test.go` (new); `internal/acp/stacknode.go`.

**Steps:**

1. **`detail.go`** holds `StackNodeOf` (Contracts) and the moved helpers. `StackNodeOf` has the body of `StackNode` (`acp/stacknode.go:288-329`) after the tree is built:
   - `Find`;
   - the detail switch;
   - the relations;
   - the node projected with `Project([]*Node{n}, st.Workspace().ActiveRoot)`, as plan 3a's Task 4 left it, with its parent's ID set.

   It returns false where the handler returned "unknown node".
2. **`acp/stacknode.go`:**
   - `StackNode` keeps its params check and `treeFor`. It then returns `viewmodel.StackNodeOf(state, tree, p.NodeID)`'s result, or `invalidParamsError("unknown node: %s", p.NodeID)` when it is not found;
   - delete the moved helpers, `isDiffToolName` and `unixMs`. `treeFor` and `StackNodeParams` stay.
3. **`detail_test.go`:**
   - `TestStackNodeOfFindsANodeAndItsParent`: a beat with one `shell.run` call whose output is three times `WireTextCap`. The call's node has its parent set to the beat's key, its detail output is the whole output, and its arguments are indented;
   - `TestStackNodeOfMissesAnUnknownNode`: "nope" is not found;
   - `TestCallDetailCarriesItsHooksAsTheirJSON`: a call with two hooks, one `FailedOpen`, has `Hooks` equal to `json.Marshal` of them. A call with none has no `Hooks`.

   The `acp` tests that cover the handler stay where they are.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go build ./...
grep -nE '^func (callDetail|activeCallDetail|executionDetail|indentJSON|todoDetail|keysOf|isDiffToolName|unixMs)\(' internal/acp/*.go
TMPDIR=/private/tmp/p2-3b-suite go test ./internal/viewmodel/ -run 'StackNodeOf|CallDetail' -v 2>&1 | grep -E '^(--- |ok|FAIL)'
TMPDIR=/private/tmp/p2-3b-suite go test ./internal/viewmodel/ ./internal/acp/
go vet ./internal/viewmodel/ ./internal/acp/
```

Expected: the grep prints nothing. The three new tests pass, and `viewmodel` and `acp` pass unchanged.

## Task 3: What the browse actions need

**Goal:** the detail carries a node's step record, a completion's whole output and report, and a call's summary. A call row says whether a callers lookup applies to it.

**Files:** `internal/acpwire/stacknode.go`, `stack.go`, `stacknode_test.go`; `internal/viewmodel/detail.go`, `rows.go`, `wire.go`, `detail_test.go`, `rows_test.go`, `wire_test.go`.

**Steps:**

1. **`acpwire`** gains the fields and types of Contracts' "After Task 3".
2. **`CallersSymbol`** goes in `rows.go`. Its test is `callerQueryCmds`' (`callers.go:62-69`), and its symbol is `firstResolvedSymbol`'s (`callers.go:86`). It is true when:
   - the tool is subject-first (`SubjectFirstTool`) and not `file.read`;
   - the call carries a symbol with `Resolved` set. The first such symbol is the one returned.
3. **`StackNodeOf`:**
   - `callDetail` sets `Summary` to `ev.ResultSummary`;
   - a node with calls gets `Step` from `st.Step(n.Tools[0].StepID)` when that ID is not 0 and the state has the record. A beat gets `Step` from `n.Step.Step`, even when its ID is 0;
   - each `StepDetail` is filled from the record. `Owner` is `StepOwner(record)`, and `Model` and `Provider` are the record's actor's. `Why` is empty for ID 0, and otherwise `StripEmphasis` of the first sentence (`FirstSentence`) of `st.StepNarration(id)`. `TodoID` is the record's `TodoID`;
   - a completion node gets `Completion` with `n.Completion.Output` and `n.Completion.Report`.
4. **`wire.go`.** In `wireNode`, the branch for a one-call row that sets `SubagentID` from `StartedSubagent` also sets `Callers` from `CallersSymbol(n.Tools[0])`'s `ok`. Plan 3a moved the types out of `wire.go`, so find the branch by that `StartedSubagent` call.
5. **Tests:**
   - `detail_test.go`:
     - `TestStackNodeOfCarriesACallsOwnStep`. A beat whose first response, with the actor `{Label: "reviewer #1", Model: "qwen3", Provider: "local"}`, narrates "Checking the **config**. Then more." and calls `file.read`. A narrationless second response with `Model: "qwen3-mini"` calls `shell.run`. The read's `Step` has the first response's ID, owner "reviewer #1", model "qwen3", provider "local" and why "Checking the config.". The shell call's `Step` has the second response's ID, model "qwen3-mini" and an empty why;
     - `TestStackNodeOfCarriesABeatsStep`: a todo in progress when the step began. The beat's `Step` has its ID, its why and that todo's ID. A loose beat, with narration but no step ID, has a `Step` whose ID is 0 and whose why is empty;
     - `TestStackNodeOfCarriesACompletionsOutputAndReport`. A job exit whose output is three times `WireTextCap` has `Completion.Output` equal to the completion's whole output. A finished subagent's `Completion.Report` is its whole report;
     - `TestCallDetailCarriesItsSummary`: a `file.edit` call with an empty result, an error and the summary "edit failed" has `Summary` "edit failed" and no `Output` or `Diff`;
   - `rows_test.go`, `TestCallersSymbol`. A `file.write_patch` call with an unresolved symbol and then a resolved one gives the resolved one. Each of these gives false: a call whose only symbol is unresolved, a `file.read` with a resolved symbol, a `shell.run`, and a call with no symbols;
   - `wire_test.go`, `TestWireToolFlagsCallersLookups`. A beat whose rows are a `file.write_patch` with a resolved symbol, one with an unresolved symbol, and a `file.read` with a resolved symbol: only the first row's `Callers` is true;
   - `acpwire/stacknode_test.go`, `TestDetailAdditionsJSON`, marshals the two Task 3 values of the strings table and compares each with its literal.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go build ./...
TMPDIR=/private/tmp/p2-3b-suite go test ./internal/acpwire/ ./internal/viewmodel/ -v 2>&1 | grep -E '^(--- FAIL|ok|FAIL)'
TMPDIR=/private/tmp/p2-3b-suite go test ./internal/viewmodel/ -run 'StackNodeOf|CallDetail|CallersSymbol|FlagsCallers' -v 2>&1 | grep -E '^(--- |ok|FAIL)'
TMPDIR=/private/tmp/p2-3b-suite go test ./internal/acp/ ./internal/app/tui/
go vet ./internal/acpwire/ ./internal/viewmodel/
```

Expected:
- the new tests pass, and so do `acpwire` and `viewmodel`;
- `acp` passes: the node tests check fields, and the patch tests compare encodings between flushes;
- `TestTranscriptGoldens` passes, and `tui` fails only on Task 9's baseline list.

## Task 4: `StackNode` and `Callers` on the session

**Goal:** the client seam carries a node's detail and its callers lookup, and the in-process adapter answers both. The reference finder moves from the TUI's options into the adapter's dependencies.

**Files:** `internal/acpclient/acpclient.go`, `methods.go`; `internal/acpclient/inproc/inproc.go`, `events.go`, `stacknode.go` (new), `stacknode_test.go` (new), `callers_test.go` (new); `internal/app/app.go`.

**Steps:**

1. **`acpclient.go` and `methods.go`.** Add `StackNode` and `Callers` to `Session` after `RefreshSheet`, with the docs from Contracts, and their rows to `Methods`.
2. **`inproc.go`.** Add `ReferenceFinder` and `Deps.ReferenceFinder` (Contracts). `Session` gains `refs`. `newSession` (`events.go:17`) sets it from `d.ReferenceFinder`, and `Swap` keeps it because it copies the host's `Deps`.
3. **`inproc/stacknode.go`:**
   - `StackNode`:
     - a closed session returns `errSessionClosed`;
     - the source is `s.state`, or `childState(s.state, subagentID)` for a subagent. A nil child fails with `fmt.Errorf("inproc: no subagent %d", subagentID)`;
     - it builds `viewmodel.Build(viewmodel.SnapshotOf(src, false, time.Now()))` (decision 7);
     - it returns `viewmodel.StackNodeOf`'s result, or `fmt.Errorf("inproc: no node %q", nodeID)` when the node is not found.
   - `Callers`:
     - a closed session returns `errSessionClosed`, and a nil `refs` returns nil;
     - it walks `s.state.Transcript()` for the audit whose `viewmodel.ToolKey` is `nodeID`. When it finds none, or `viewmodel.CallersSymbol` of it is false, it returns nil;
     - it calls `refs.References(ctx, ref.File, ref.Line, ref.Col)` and returns the references when `ok` is true, and nil otherwise.
4. **`app.go`.** Before `inproc.New` (2807), when `rt.LSPManager` is not nil, build `lsp.NewSymbolQuery(rt.LSPManager, nil, 0)` and pass it as `Deps.ReferenceFinder`. Otherwise leave the field nil: never assign a nil `*lsp.SymbolQuery` to the interface. The `tui.WithReferenceFinder` lines (2860-2862) stay until Task 7.
5. **Tests (T5):**
   - `stacknode_test.go`:
     - `TestStackNodeReturnsACallsDetail`: a beat with a `shell.run` call whose output is longer than `WireTextCap`. The result's `Detail.Calls[0].Output` is the whole output, its `Node.Parent` is the beat's key, and its `Detail.Step.ID` is the step's;
     - `TestStackNodeReadsASubagentsTranscript`. A child registered with `RegisterSubagent("scout", child)` holds a beat. `StackNode(key, view.ID)` finds it, and `StackNode(key, 0)` fails, because the parent has no such node;
     - `TestStackNodeFailsForAnUnknownNodeOrSubagent`: an unknown node ID fails, and so does subagent ID 999999;
     - `TestStackNodeOnAReplacedSessionFails`: after `Host.Swap`, the old session's call fails with `acpwire.CodeSessionClosed`, as `sendnow_test.go:93` checks;
   - `callers_test.go` declares its own `fakeFinder` (Contracts). Its edit event is the one `tui/callers_test.go`'s `editEvent()` builds: a `file.write_patch` of `transcript.go` with the resolved symbol `renderSubagentCard` at line 584, column 5. Here it also has a step:
     - `TestCallersAsksTheFinderForTheResolvedSymbol`: the finder answers `["a.go:10"]` and records `transcript.go:584:5`;
     - `TestCallersIsNilWithoutALookup`: each of these gives nil and no error, and the finder records no lookup. They are an unresolved symbol, a `file.read` call, an unknown node ID, and an adapter with no finder. A finder that answers `ok` false gives nil too;
     - `TestCallersOnAReplacedSessionFails`, as above.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go build ./...
TMPDIR=/private/tmp/p2-3b-suite go test ./internal/acpclient/... -v 2>&1 | grep -E '^(--- FAIL|ok|FAIL)'
TMPDIR=/private/tmp/p2-3b-suite go test -race ./internal/acpclient/inproc/
TMPDIR=/private/tmp/p2-3b-suite go test ./internal/acp/ -run 'TestACPClientMethodTableMatchesTheServer' -v 2>&1 | grep -E '^(--- |ok|FAIL)'
go vet ./internal/acpclient/... ./internal/app/
```

Expected: the seven new tests pass, the race run is clean, and `TestMethodTableCoversTheInterfaces`, `TestMethodDocsNameTheirACPMethod` and the server's table test pass with the new rows.

## Task 5: The wire index and the inspector

**Goal:** the TUI keeps the wire nodes of what it renders, and the inspector reads a node from them and its detail from `StackNode`.

**Files:** `internal/app/tui/viewport_render.go`, `model.go`, `browse.go`, `browse_inspect.go`, `browse_inspect_test.go`, `boundary_allowlist_test.go`.

**Steps:**

1. **The index.**
   - `model.go`: `Model` gains `wire` (Contracts) after `browseTree` (575).
   - `viewport_render.go`: add `wireIndex` (Contracts). In `refreshViewport`, set `m.wire = wireIndex(viewmodel.Project(turns, rctx.workdir))` just before `m.setBrowseItems(bitems, tree)` (207). That is after the content-signature check, so an unchanged frame keeps its index (decision 1).
2. **`browse.go`.** Add `wireNode` and `nodeDetail` (Contracts). `nodeDetail` calls `m.sess.StackNode(m.ctx, id.Key, m.drilledSheetID())`, and reports `ok` false on any error. `drilledSheetID` (`model.go:1921`) is the drilled-in view's ID or 0, whatever it is named.
3. **`browse_inspect.go`:**
   - `openInspector` and `stepInspector` call `m.inspectorDetail(m.cursor)`.
   - `inspectorDetail(id)` takes `w := m.wireNode(id)`. It needs a tool node with calls, a beat, or a completion, as today, and then `m.nodeDetail(id)`. Either missing gives `false`, which shows "Nothing to inspect here".
   - **`toolDetail(w, d)`** builds today's fields and sections in today's order, from `call := w.Tool.Calls[0]` and `cd := d.Calls[0]`:

     | Today | From |
     |---|---|
     | subject `toolSubject(ev)` | `w.Tool.Name`, plus a space and `call.Target` when it is not empty |
     | `parentStep` and the event match | `m.callPlace(w)`: `w.Parent`'s node when it is a beat, counting `len(Tool.Calls)` over its children up to `w` |
     | title's step ID | `cd.StepID` |
     | actor `stepOf` + `actorText` | `actorText(d.Step.Owner, d.Step.Model, d.Step.Provider)` when `d.Step` is set, else `call.Role` |
     | why `whyText` | `d.Step.Why` |
     | started `ev.Timestamp` | `acpwire.Time(call.At)`, when `call.At` is not 0 |
     | duration `ev.Duration` | `call.DurationMs` in milliseconds |
     | exit code | `cd.ExitCode` |
     | sandbox `ev.Sandbox` | `cd.Sandbox` decoded into `acpwire.SandboxDetail`, when it is not empty |
     | approval | `call.Approval`, unless empty or `acpwire.ApprovalNotRequired` |
     | hooks `hookIndicatorText` and each hook | `call.HookHint`, and `cd.Hooks` decoded into `[]acpwire.HookDetail` |
     | rewritten, finish reason | `cd.Rewritten`, `cd.FinishReason` |
     | tool call id, notice, risk | `call.CallID`, `cd.Notice.Text`, `call.Risk` |
     | args and original args sections | `cd.Args` and `cd.OriginalArgs`, which `indentJSON` already formats as `prettyJSON` did |
     | output and diff sections | as today, with `cd.Output`, `cd.Diff` and `cd.Error` in place of the event's fields. `isDiffTool(w.Tool.Name)` picks the diff form. |

   - **`stepDetail(w, d)`:**
     - subject `w.Step.Headline`;
     - title from `d.Step.ID`;
     - actor `actorText(w.Step.Owner, w.Step.Model, w.Step.Provider)` and why `d.Step.Why`;
     - started and duration from `w.Step.StartedAt` and `EndedAt`;
     - task `d.Step.TodoID`;
     - tools `m.beatCalls(w)`;
     - narration and thinking sections from `d.Narration` and `d.Thinking`, each trimmed;
     - the tools section is one line per call of each child row, `Name` and `Target` joined by a space.
   - **`completionDetail(c, d)`** is today's body over `c` and `d.Output` and `d.Report`. Times come through `acpwire.Time(…).Local()`, and the duration from `c.DurationMs`. A nil `d` reads as empty.
   - `actorText` takes the three strings. Delete `stepOf`, `whyText`, `prettyJSON`, `toolSubject` and `parentStep`.
   - The imports lose `session` and `registry`.
4. **Tests:**
   - `browse_inspect_test.go`:
     - `TestInspectorShowsACompletionsFullOutput` calls `completionDetail` with a `WireCompletion` built from today's fixture (`DurationMs` 48000) and a `CompletionDetail` holding the output. Its assertions are unchanged;
     - `TestInspectorShowsACallsStepAndPlace`. A beat by `{Label: "reviewer #1", Model: "qwen3", Provider: "local"}` narrates "Checking the **config**. Then more." and calls `file.read` (`c1`) and then `shell.run` (`c2`, args `{"command":"go test"}`, exit 1, 3 s, out "FAIL"). The cursor is on `c2`'s row after a refresh. Its detail has:
       - title "step N · tool 2/2";
       - subject "shell.run go test";
       - actor "reviewer #1 · qwen3 @ local", why "Checking the config.", exit code "1", duration "3s" and tool call id "c2";
       - an args section with the indented arguments, and an output section "FAIL";
     - `TestInspectorShowsABeatsToolsAndTask`. The same beat, with the todo `t1` in progress when it began. Its detail has the subject "Checking the config.", the task "t1", the tools "2", the whole narration, and the tools section "file.read a.go" then "shell.run go test";
     - `TestInspectorShowsASandboxedHookedCall`. A `shell.run` call with the hook `{Event: "PreToolUse", Decision: "block"}`, the sandbox `{Enabled: true, Backend: "restricted", NetworkIsolated: true, KilledReason: "timeout"}` and the approval "approved". Its fields include:
       - "hooks": "hook blocked PreToolUse: block";
       - "sandbox": "restricted, network off, killed: timeout";
       - "approval": "approved".

       A call whose approval is "not_required" has no approval field;
     - `TestInspectorSaysNothingWhenTheNodeIsGone`: the cursor on a key no node has gives `false`.
5. **The allowlist** (`boundary_allowlist_test.go`): delete `marshal/internal/app/session` and `marshal/internal/tools/registry` from `browse_inspect.go`'s entry. Never add an entry: a "uses …, which is agent side" error means this task left an agent-side call behind.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go build ./...
grep -nE 'func \(m \*Model\) (stepOf|whyText|parentStep)|func (prettyJSON|toolSubject)\(' internal/app/tui/*.go
TMPDIR=/private/tmp/p2-3b-suite go test ./internal/app/tui/ -run 'Inspector|TestTranscriptGoldens|TUIStaysOnTheClientSeam' -v 2>&1 | grep -E '^(--- FAIL|ok|FAIL)'
TMPDIR=/private/tmp/p2-3b-suite go test ./internal/app/tui/... ./internal/app/
go vet ./internal/app/tui/...
```

Expected: the grep prints nothing. The named tests pass, `TestTranscriptGoldens` included, and both packages fail only on Task 9's baseline list.

Then run `./marshal` (built with `CGO_ENABLED=1 go build ./cmd/marshal`) in this repository. Run a turn that reads and runs a command, enter browse mode, and press `i` on a call row, a beat and, after a background job, its completion event. Check by eye that each shows what it showed before.

## Task 6: Copy, open, the full subject and the subagent at the cursor

**Goal:** copy, open, the selection's full-subject copy and the drill-in's subagent ID read the wire index and the detail.

**Files:** `internal/app/tui/browse_actions.go`, `browse.go`, `selection.go`, `browse_inspect_test.go`, `browse_test.go`, `boundary_allowlist_test.go`.

**Steps:**

1. **Copy.** `copyCursorNode` calls `m.nodeCopyText(m.cursor)`. `nodeCopyText(id)` reads `w := m.wireNode(id)` and returns:
   - for a tool node with calls: `callCopyText` of each of `m.nodeDetail(id)`'s calls, joined by a blank line;
   - for a beat: each of the detail's narrations trimmed, then a line `- <Name> <Target>` for each call of each child row in `m.wire`, joined by newlines;
   - for a completion: `completionCopyText(w.Completion, detail.Completion)`;
   - for a message: `w.Message.Content`;
   - otherwise, from `m.browseTree[id]` as today: a receipt's `renderReceipt` text, or another item's `renderTranscriptItem` text. Plan 3c ports these renderers.

   A failed `nodeDetail` gives "". Delete the `n.Item.Subagent` case: no node carries a subagent item (`TestSubagentItemIsNeverANode`).
   - `completionCopyText(c, d)` is today's body over `c` and `d`. The body is `d.Report` for a subagent (`acpwire.CompletionSubagent`) and `d.Output` otherwise. A nil `d` gives no body.
   - `callCopyText` is the diff, else the output, else the summary (decision 4). Delete `toolCopyText`.
2. **Open.** `openCursorFile` calls `m.openFile(m.nodeFile(m.cursor))`.
   - `nodeFile(id)` gathers an `openCall` per call, in order:
     - for a tool node, from its wire calls and `m.nodeDetail(id)`'s calls;
     - for a beat, the same for each child tool row in `m.wire`, asking `nodeDetail` per row.
   - Each `openCall` takes:
     - `tool` from `Tool.Name`;
     - `args` from the detail's `Args`;
     - `files` from the wire call's `Files`;
     - `output` from the detail's `Output` when the wire call's `Output` is not empty, and "" otherwise. The wire's `output` is empty exactly when the call printed nothing, and the detail's then holds the summary.
   - It returns `fileIn(m.sess.Status().Workspace.ActiveRoot, calls)`.
   - `fileIn` is today's two passes (`browse_actions.go:189-225`) over `openCall` values. The shell test is `viewmodel.IsShellFamily(c.tool)`.
3. **`selection.go:171`.** `fullToolHead` reads `m.wireNode(id)`. It joins the calls' non-empty `Subject` values with ", ", as plan 3a's subject is today's `callSubject` against the same root.
4. **`browse.go`.**
   - `subagentIDOf(w)` reads `w.Completion.SubagentID`, or `w.Tool.SubagentID` for a tool node with exactly one call.
   - `subagentAtCursor` starts from `m.wireNode(m.cursor)`, and for a beat checks its children in `m.wire`.
   - Delete `currentNode`.
   - The imports of `browse_actions.go` lose `registry`.
5. **Tests:**
   - `browse_inspect_test.go`, `TestCopyTakesTheCompletionHeadAndOutput`, calls `completionCopyText` with today's fixture as a `WireCompletion` (`DurationMs` 48000) and a `CompletionDetail` holding the output. Its `want` is unchanged;
   - `browse_test.go`:
     - `TestOpenResolvesAPathFromArgsAndFromShellOutput` (341) calls `m.nodeFile(m.cursor)`;
     - `TestOpenIgnoresDirectoriesAndPathsOutsideTheWorkspace` (387) calls `fileIn(root, []openCall{{tool: "search.grep", args: …}})` with today's three paths;
     - `TestCopyingAStepDoesNotRepeatItsHeadline` (488) calls `m.nodeCopyText(m.cursor)`;
     - `TestCopyOfAMergedRowJoinsEveryResult`: two merged `file.read` calls with the results "one" and "two" copy as "one\n\ntwo";
     - `TestCopyOfAnEmptyEditTakesItsSummary`: a `file.edit` call with an empty result, an error and the summary "edit failed" copies as "edit failed";
     - `TestOpenFindsAFileInABeatsSecondRow`: a beat whose first row is a `shell.run` with no output and whose second is a `file.read` of an existing `a.go` opens `a.go`;
     - `TestCopyWithoutDetailSaysNothingToCopy`: the cursor on a key no node has copies "".

   The drill-in tests (`drill_test.go`) and the selection tests (`TestSpineRowsCopyFromTheSubjectColumn`, `spine_test.go:348`) cover the subagent ID and the full subject unchanged.
6. **The allowlist:** delete `marshal/internal/tools/registry` from `browse_actions.go`'s entry.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go build ./...
grep -nE 'func toolCopyText|func \(m \*Model\) currentNode|m\.currentNode\(\)' internal/app/tui/*.go
TMPDIR=/private/tmp/p2-3b-suite go test ./internal/app/tui/ -run 'Copy|Open|Drill|Subagent|Spine|Selection|TestTranscriptGoldens|TUIStaysOnTheClientSeam' -v 2>&1 | grep -E '^(--- FAIL|ok|FAIL)'
TMPDIR=/private/tmp/p2-3b-suite go test ./internal/app/tui/... ./internal/app/
go vet ./internal/app/tui/...
```

Expected: the grep prints nothing, the named tests pass, and both packages fail only on Task 9's baseline list.

Then run `./marshal` in this repository. In browse mode, check by eye:
- `y` on a call row, a beat and a completion event copies what it copied before;
- `o` on a read row and on a failing test row opens the file;
- a mouse selection across a truncated row copies the whole subject;
- `f` on a dispatch row drills into the subagent.

## Task 7: Callers through the session

**Goal:** the TUI asks `Session.Callers` for the rows the wire flags, and holds no reference finder.

**Files:** `internal/app/tui/callers.go`, `model.go`, `callers_test.go`, `boundary_allowlist_test.go`; `internal/app/app.go`.

**Steps:**

1. **`callers.go`:**
   - delete `ReferenceFinder`, `WithReferenceFinder` and `firstResolvedSymbol`;
   - `callersMsg` and `queryCallersCmd` are as in Contracts. The command calls `sess.Callers(ctx, key.Key)` and sends nil on an error;
   - `callerQueryCmds` returns nil while the view is drilled in (`len(m.viewStack) > 0`, decision 5). Otherwise it walks `m.wire` for tool nodes whose `Tool.Callers` is true and that `m.callersAsked` does not hold. It marks each as asked under the key `viewmodel.NodeID{Kind: viewmodel.KindTool, Key: id}`, and batches `queryCallersCmd(m.lspCtx, m.sess, key)` for it;
   - `handleCallers` stores `msg.callers`, nil included, so the key is present and never asked again;
   - the doc comments drop the finder. They say the session answers the lookup, and that a nil answer means no callers line.
2. **`model.go`.** Delete `refFinder` with its comment (589-590). `lspCtx` and `lspCancel` stay (decision 10).
   **`app.go`.** Delete the `tui.WithReferenceFinder` lines (2860-2862). The finder now reaches the session through `Deps` alone.
3. **`callers_test.go`:**
   - `fakeFinder` gains `asked` (Contracts);
   - a helper `callersModel(t, f *fakeFinder) Model` builds the state, the options and the size as `newTestModel` (`model_test.go:5719`) does, but through `testModelWith(inproc.Deps{State: state, ReferenceFinder: rf})`. `rf` is `f`, or a nil interface when `f` is nil, never a nil `*fakeFinder`;
   - `TestUnresolvedSymbolIsNeverQueried` and `TestNegativeResultIsCachedAndNotRetried` keep their names and assertions. Each logs its edit and calls `m.refreshViewport()` before `callerQueryCmds`, and the finder is the adapter's. An unresolved symbol is never flagged, so no command is issued at all;
   - `TestNilFinderIsSafe` becomes `TestNoFinderLeavesNoCallersLine`. With no finder, the command for the flagged row answers nil. After `Update` with its message, `m.callers` holds the key with a nil value, and the viewport has no "callers" text;
   - `TestRendererNeverQueries` sets the finder's `failIf`, logs the edit and calls `refreshViewport` twice. The finder is never called;
   - `TestCallersAreAskedOnceForAFlaggedRow`. The finder answers `["a.go:10"]`. The first `callerQueryCmds` returns a command whose message, through `Update`, puts `["a.go:10"]` in `m.callers`. The second returns nil;
   - `TestCallersWaitWhileDrilledIn`. A child registered with `RegisterSubagent` is pushed onto `m.viewStack`. `callerQueryCmds` returns nil while it is there, and returns a command once `popDrill` and a refresh have run.

   `TestCallersLineRendersWhenResolved`, `TestNoCallersLineWhenEmpty`, `TestCallersBustTheTranscriptHash` and `TestStaleCallersArePruned` are unchanged.
4. **The allowlist:** delete `marshal/internal/app/session` and `marshal/internal/tools/registry` from `callers.go`'s entry.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go build ./...
grep -rnE 'ReferenceFinder|refFinder|firstResolvedSymbol' --include='*.go' internal/app/tui | grep -v '_test.go'
grep -n 'tui.WithReferenceFinder' internal/app/app.go
TMPDIR=/private/tmp/p2-3b-suite go test ./internal/app/tui/ -run 'Callers|Querie|Finder|TestTranscriptGoldens|TUIStaysOnTheClientSeam' -v 2>&1 | grep -E '^(--- |ok|FAIL)'
TMPDIR=/private/tmp/p2-3b-suite go test ./internal/app/tui/... ./internal/app/
go vet ./internal/app/tui/... ./internal/app/
```

Expected:
- both greps print nothing: no non-test TUI file names the finder, and `app.go` passes it only through `Deps`;
- the named tests pass, and both packages fail only on Task 9's baseline list.

Then run `./marshal` in this repository with a Go language server configured. Have it edit a function another file calls, and check by eye that the row gains its "↳ N callers" line.

## Task 8: The guard

**Goal:** T1b keeps the transcript read moved.

**Files:** `internal/app/tui/moved_reads_test.go`.

**Steps:**

1. **`movedStateReads`** gains `"Transcript": "3b"`, in a block of its own after the 2c names.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
TMPDIR=/private/tmp/p2-3b-suite go test ./internal/app/tui/ -run 'TestMovedStateReadsStayMoved|TestMovedStateReadsAreRealNames|TestMovedReadsGuardFindsSelectors|TestTUIStaysOnTheClientSeam' -v 2>&1 | grep -E '^(--- |ok|FAIL)'
go vet ./internal/app/tui/
```

Expected: all four pass.

As a check of the guard (no commit), temporarily add `_ = m.state.Transcript()` to `callerQueryCmds`. `TestMovedStateReadsStayMoved` should fail with the T1b string, naming `callers.go` and plan 3b. Then revert it.

## Task 9: Verification and docs

**Goal:** the full suite matches the baseline, and the spec and design record plan 3b.

**Files:** `docs/daemon/p2-client-seam.md`, `docs/daemon/design.md`.

**Steps:**

1. **`p2-client-seam.md`.** Add a plan 3b entry to the Implementation record. It lists:
   - the detail types and `StackNodeResult` moved to `acpwire`, `Hooks` as raw JSON, and the two decode types;
   - `StackNodeOf` and who calls it;
   - `step`, `completion`, `summary` and `callers`;
   - `StackNode` and `Callers` on `Session`, and the finder in `Deps`;
   - the TUI's wire index, and which actions read it;
   - the name added to T1b;
   - the allowlist entries Tasks 5 to 7 removed: five entries across three files, no key dropped.
2. **`design.md` §7.** The P2 row's status adds `plan 3b implemented` after `plan 3a implemented`.
3. **Full verification.**

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
gofmt -l ./internal ./cmd
go vet ./...
TMPDIR=/private/tmp/p2-3b-suite CGO_ENABLED=1 go build ./cmd/marshal
TMPDIR=/private/tmp/p2-3b-suite go test ./... -timeout 20m 2>&1 | grep -E '^(--- FAIL|FAIL|ok)' | grep -v '^ok'
git diff --stat main..HEAD -- internal/app/tui/testdata/transcript
```

Expected:
- `gofmt -l` prints nothing;
- `go vet` is clean;
- the binary builds;
- the failures are exactly the baseline below. A flaky one counts only if it passes alone with `-count=3`;
- the `git diff` prints nothing: no golden changed.

**Baseline**, plan 3a's at `75ee412d`, from a full run with `-timeout 20m`:
- `internal/app`: `TestSubagentChildRegistryBorrowsConversationService`, `TestPipelineRegistryCommandsCaptureDispatchWorktree` and `TestSubagentShellOutputDoesNotReachParent`.
- `internal/app`: `TestDefaultPromptBudget` fails alone too: tool schemas are 15322 bytes, over the 15000 ceiling. It has failed since `0cad39ae`. This plan changes no schema.
- `internal/app`: `TestRuntimeAccessSerializesWithReloadAndQuiesce` and `TestRuntimeCloseRunsNoGitGC` are flaky under load.
- `internal/app/tui`: `TestStatusLineUntrustedIsWarningColored`.
- `internal/tools/desktop/browser`: `TestStandaloneBackendNavigateAndRead`.
- `internal/worktree`: `TestCLIGitOpsCommitAllWithoutARepoIdentity`.
- `internal/snapshot`: `TestRunManagedCancellationKillsTree` counts as pre-existing whether it passes or fails.
- Known to be flaky under load: `TestIntegrationOwnershipReleaseDrainsWritersBeforeUnlock` (`internal/snapshot`), and `TestACPChildApprovalUsesOwningSessionRegistry` and `TestACPSendNowCancelsPromptRPCWithoutCancelingTurn` (`internal/acp`).

Finally, run `./marshal` in this repository and repeat the by-eye checks of Tasks 5 to 7, then drill into a subagent and press `i` and `y` on one of its rows.

## Integration notes

- **Plan 3c** ports the row renderers to `m.wire`. It then deletes `browseTree`, `indexTree` and the two renderer-backed copy paths of Task 6, and adds the expanded rows' detail to `NodeDetail`:
  - a run event's whole body;
  - a completion's own truncation flag;
  - a message's whole reasoning;
  - a running call's whole output.

  It also decides how an expanded row gets its detail without blocking the render. `StackNode` is a call, and P3 makes it a round trip.
- **Plan 3d** replaces `m.wire`'s source with `Session.Stack`. The adapter's `Stack` is `ProjectState`. `StackNode`'s `busy` question (decision 7) does not arise there, because the stack carries the live flags itself.
- **P3** gives `session/callers` a handler. It runs the same `CallersSymbol` test against the session's finder, so the adapter's `Callers` body moves into `acp` when the adapter is deleted.

## Self-review

- Every task commits alone.
  - Tasks 1 to 3 are agent side. Each builds on the one before, and `acp`'s tests run at each.
  - Task 4 adds the seam methods and moves one wiring line in `app.go`.
  - Tasks 5 to 7 each move one group of TUI reads, and each trims its own allowlist entries.
  - Task 8 locks the read in.
- Tasks 1 and 2 span `acpwire`, `viewmodel` and `acp`, because a moved type or function breaks the build until its users follow. The change is mechanical.
- Tasks 5 and 6 are each one package (`tui`) and its tests.
- Cold-executor check:
  - every inspector field is in Task 5's table with its new source;
  - every copy and open case is in Task 6, with what replaces it;
  - every changed test names its new mechanism;
  - every deleted function is named, with the grep that proves it gone;
  - every allowlist entry to delete is named in the task that stops needing it.
- Contracts hold every cross-task declaration: the moved and new wire types, the builder, the seam methods, the adapter's finder, the TUI's new and changed signatures, and the test helper.
- Anchors were checked at `75ee412d` and against plan 3a's tasks. Re-check the line numbers in `viewport_render.go` and `acp/stacknode.go` against the merged plan 3a before starting.
- Nothing here contradicts the spec. Decisions 1 to 5 are in its revision record, and the Migration order's 3b and 3c items were updated to match.
