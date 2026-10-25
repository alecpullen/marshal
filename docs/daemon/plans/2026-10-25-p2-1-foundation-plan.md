# P2 plan 1: foundation — implementation plan

Date: 2026-10-25. Status: ready for review.
Base: `main` at `93088198` or later.
- Every anchor below was checked at `93088198`. Line numbers are there to find the code fast; the function names are the anchors.
- The allowlist counts in Task 7 were measured there with a parser-only scan that applies the same rule Task 7's test applies.

Approved design: [p2-client-seam.md](../p2-client-seam.md): C1 (packages and the boundary), C2 (the interface, error codes), C3 (wire rules), C4 (the adapter), C12 (`$EDITOR`), Migration order item 1, and tests T1, T2 and T4.
Parent design: [design.md](../design.md) §4.5 and §7.
Authoring workflow: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md).

**How this plan is written.** No prototype was built, and the plan embeds no implementation code. The Contracts section holds declarations and the exact strings the tests pin. Every other step states its decision in prose. Executing the plan also tests how well Marshal follows a plan. "As `X` does" means `X` exists and is the model to copy.

**Goal:** the client seam exists, and nothing a user sees changes.
- `internal/acpwire` holds the error codes. A test enforces C3's field rules for every type that lands there later.
- `internal/acpclient` holds the `Host` and `Session` interfaces, the error helpers and the ACP method table.
- `internal/acpclient/inproc` implements both interfaces over today's runtime.
- `tui.New` takes a host and a session. The state still arrives through `WithLegacyState`, and every TUI test builds its model through one helper.
- `gitinfo` and `changedfiles` live under `internal/repo`.
- A boundary test lists every remaining TUI use of the agent side. Plans 2 to 9 shrink that list to nothing.

**Non-goals:**
- **No group moves.** The TUI keeps reading `m.state` everywhere. The one new read through the seam is the `$EDITOR` gate (decision 2).
- **No wire types move yet.** The projection, work and ACP types stay where they are. Each group's plan moves its own (decision 1).
- **No ACP change.** No method is added and no message changes.
- **No `Changed` signal and no `Status`.** Both arrive in plan 2.
- **No changelog entry.** Nothing a user sees changes.

**Assumptions:**
- Every Go test command below runs with `TMPDIR=/private/tmp/p2-1-suite`. Create the directory first: `mkdir -p /private/tmp/p2-1-suite`.
- **Pre-existing failures** at `93088198`, from a full run with `-timeout 20m`:
  - `internal/app`: `TestSubagentChildRegistryBorrowsConversationService`, `TestPipelineRegistryCommandsCaptureDispatchWorktree`, `TestSubagentShellOutputDoesNotReachParent`, `TestAccessTUIOptionsResolveWithoutConfigReload` and `TestRuntimeNewAndResumedSessionResetAccess`;
  - `internal/app/tui`: `TestStatusLineUntrustedIsWarningColored`;
  - `internal/tools/desktop/browser`: `TestStandaloneBackendNavigateAndRead`;
  - `internal/worktree`: `TestCLIGitOpsCommitAllWithoutARepoIdentity`;
  - flaky under full-suite load, passing alone: `TestRuntimeAccessSerializesWithReloadAndQuiesce` (failed in this run, passed alone with `-count=3`), `TestRuntimeCloseRunsNoGitGC` (`internal/app`) and `TestACPChildApprovalUsesOwningSessionRegistry` (`internal/acp`);
  - `web/bridge` is slow under load (95 s in this run). Every full run below uses `-timeout 20m`.

  A flaky test that fails in a full run counts as pre-existing only when it passes on its own with `-count=3`.
- Task 6 fixes two of the `internal/app` failures, `TestAccessTUIOptionsResolveWithoutConfigReload` and `TestRuntimeNewAndResumedSessionResetAccess` (decision 8). From Task 6 on, `internal/app` has three pre-existing failures, not five.

**Decisions this plan makes beyond the spec** (decisions 1 and 3 are now recorded in the spec's C4 and Migration order):
1. **`acpwire` starts with the error codes only.** The projection types move in plan 3, the work types in plan 2, and the request and command types in plans 4 and 6. Each move then sits next to the code that switches to it, and the JSON-identity check runs where it matters. T2's rule test is written now, so it checks every type as it arrives.
2. **The interfaces start with one method each.**
   - `Host.SharedFilesystem()` (C12) gates the TUI's `$EDITOR` action now, so the seam has one real caller from day one.
   - `Session.ID()` maps to `session/new`. It gives T4 one existing method to check, and it is how tests tell sessions apart across a swap.
   - Plan 2 adds `Changed` and `Status`. Its `Status` need not repeat the session ID.
3. **The adapter returns its concrete types**, `inproc.New(Deps) (*Host, *Session)`, which satisfy the interfaces. `(*Host).Swap(state)` serves today's swap path: it closes the old session and returns one over the new state. `tui.SessionSwapResult` gains `Session`, and `app.Run`'s swapper fills it. Plan 5 replaces `Swap` with `Host.NewSession` and `Host.Resume`.
4. **`tui.New` applies its options before it builds the theme and the input.** It needs the state those steps read, and the state now arrives as an option. Every option today only assigns fields, and none reads the input, the theme or the state, so the reordering changes nothing else. `New` panics without a host, a session or the legacy state.
5. **`changedfiles` stops importing the TUI.** `Read` returns the package's own `[]File`, and the TUI converts the rows to `sessionsheet.ChangedFile` in a helper of its own. `File` has every field `Read`'s callers use, so `internal/acp/turn.go` compiles unchanged.
6. **The allowlist is a Go map, generated by the test itself.** It is keyed by file path relative to `internal/app/tui`. Its values are the sorted import paths and `config.<Func>` names that file still uses. On failure the test logs the current list in the map's literal form, so it can be pasted in.
7. **T3 waits** for the first plan that puts `config`, `routing` or `schema` types in an `acpclient` signature.
8. **Task 6 fixes two stale access tests.** Both were written on the system-access branch, when the default mode was `default` and `AccessState` had only a mode and a system flag. Interaction modes then:
   - made the default mode Act (`config.Default()`);
   - mapped the legacy `default` mode to Ask and `copilot` to Act (`interaction.LegacyDefaults`);
   - added `Auto` and a `Revision` counter to `AccessState`.

   Neither test was updated. In both cases the runtime is right and the expectation is stale. The fixes check mode, auto and system and leave the counter alone, as the access tests written since do (`sameAccessState` in `internal/tools/native/mode_request_test.go:179`, `assertAccessState` in `internal/acp/turn_test.go:3530`).
   - **`TestAccessTUIOptionsResolveWithoutConfigReload`** (step 9) expects `{Mode: "default", System: true}` and compares whole structs, so it fails with `effective = {Mode:act Auto:false System:true Revision:2}, want {Mode:default …}`. The effective state is right.
   - **`TestRuntimeNewAndResumedSessionResetAccess`** (step 10) asks for editing and system access, then approves with mode `copilot`. Under Act, the configured default, editing is already granted, so the runtime treats the request as system-only and rejects the mode: `unexpected editing mode "copilot" for a system-only request`. The original test started from the configured `default` mode, granted a different mode with system access, and checked that new and resumed sessions reset to the configured mode. The faithful translation configures Ask, grants Act with system access, and checks the reset to Ask with system off. Starting from Ask also lets the test see a mode leak, which a test configured as Act could not.

   Both fixes were tried on an export of `93088198`, and both tests passed three runs in a row. Two temporary breaks in `Runtime.NewSession` each failed the fixed reset test: a new session that keeps system access, and one that starts in Act.

**Execution:**
- Work on branch `p2-1-foundation`. It branches from `main` at `93088198`, and its first commit, `p2-1-foundation: record the revised P2 spec and plan 1`, holds the revised spec, `design.md` and this plan. Check it out, or work in a worktree on it. Do not create the branch again.
- Run the tasks in order. Run each task's Verify and commit it as `p2-1-foundation: task N — <title>`.
- Stage explicit paths only. Never `git add -A` without a path. For moved files, stage the old and the new path.
- Run `gofmt -w` on every Go file a task touches before its Verify.

## Contracts

**`internal/acpwire`, after Task 1.**

```go
// ErrorCode names an agent-side error the TUI branches on (C2).
type ErrorCode string

const (
	CodeQuiescing          ErrorCode = "quiescing"
	CodeNoTurnStop         ErrorCode = "no_turn_stop"
	CodeHumanGateRequired  ErrorCode = "human_gate_required"
	CodeKeyringUnavailable ErrorCode = "keyring_unavailable"
	CodeAuthRequired       ErrorCode = "auth_required"
	CodeProviderError      ErrorCode = "provider_error"
	CodeStaleWorkCursor    ErrorCode = "stale_work_cursor"
	CodeRequestSettled     ErrorCode = "request_settled"
	CodeBusy               ErrorCode = "busy"
	CodeSessionClosed      ErrorCode = "session_closed"
)

// Error is an agent-side error with a code.
type Error struct {
	Code    ErrorCode       `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *Error) Error() string
```

**`internal/acpclient`, after Task 2.**

```go
type Host interface {
	// SharedFilesystem reports whether a path the agent names is also a
	// path on this machine, so the TUI may open it in $EDITOR (C12).
	//
	// ACP: none (a property of the transport; true for the in-process
	// adapter and for P3's pipe).
	SharedFilesystem() bool
}

type Session interface {
	// ID is the session's ID.
	//
	// ACP: session/new (exists). Its result carries the ID, and every
	// session/* call sends it back.
	ID() string
}

// Method is one row of the seam's ACP mapping (C2, T4).
type Method struct {
	Iface  string // "Host" or "Session"
	Name   string // the interface method's name
	ACP    string // the ACP method that carries it; "" when it has none
	Exists bool   // acp.Server registers ACP today; false means new in P3
}

var Methods = []Method{
	{Iface: "Host", Name: "SharedFilesystem"},
	{Iface: "Session", Name: "ID", ACP: "session/new", Exists: true},
}

func IsCode(err error, code acpwire.ErrorCode) bool
func CodeOf(err error) (acpwire.ErrorCode, bool)
```

The doc comments above are part of the contract: Task 2's doc test reads them.

**`internal/acpclient/inproc`, after Task 3.**

```go
// Deps is what the adapter serves. Each P2 plan adds its group's
// dependencies (C4).
type Deps struct {
	State *session.State
}

type Host struct {
	mu      sync.Mutex
	current *Session
}

type Session struct {
	state  *session.State
	closed atomic.Bool
}

func New(d Deps) (*Host, *Session)
func (h *Host) Swap(state *session.State) *Session
func (h *Host) SharedFilesystem() bool
func (s *Session) ID() string
```

**`internal/repo/changedfiles`, after Task 5.**

```go
func Read(workingDir, baseRef string) []File // was []sessionsheet.ChangedFile
```

**`internal/app/tui`, after Task 6.**

```go
func New(host acpclient.Host, sess acpclient.Session, opts ...Option) Model
func WithLegacyState(state *session.State) Option

// Model fields, next to state:
host acpclient.Host
sess acpclient.Session

// SessionSwapResult gains:
Session acpclient.Session // the client session over State

func sheetChangedFiles(files []changedfiles.File) []sessionsheet.ChangedFile
```

Test helpers (package `tui`, `seam_test.go`):

```go
func testModel(state *session.State, opts ...Option) Model
func testSession(state *session.State) acpclient.Session
type fakeHost struct{ shared bool }
func (h fakeHost) SharedFilesystem() bool
```

Test helper (package `app`, `tui_seam_test.go`):

```go
func testTUIModel(state *session.State, opts ...tui.Option) tui.Model
```

**Boundary test (package `tui`, after Task 7).**

```go
var boundaryAllowlist = map[string][]string{ /* generated in Task 7 */ }

func seamImportAllowed(path string) bool
func deniedConfigFuncs(configDir string) (map[string]bool, error)
func seamViolations(root string, denied map[string]bool) (map[string][]string, error)
func formatAllowlist(v map[string][]string) string
```

**Strings the tests pin:**

| Where | Text |
|---|---|
| `$EDITOR` gate flash | `$EDITOR is unavailable: the files are on the agent's machine` |
| `New` without a host or session | panic `tui.New: nil client host or session` |
| `New` without the legacy state | panic `tui.New: WithLegacyState is required until P2 plan 9` |
| T1, new violation | `<file> uses <entry>, which is agent side (P2 C1): move it behind acpclient instead of allowlisting it` |
| T1, stale entry | `<file> no longer uses <entry>: delete it from boundaryAllowlist` |
| T4, existing method missing | `acpclient.Methods marks <Iface>.<Name>'s <ACP> as existing, but acp.Server does not register it` |
| T4, new method already there | `acp.Server registers <ACP> now: mark <Iface>.<Name> as existing in acpclient.Methods` |

## Task 1: The wire package

**Goal:** `internal/acpwire` exists with the error codes, and its tests enforce C3's rules on everything that lands in it.

**Files:** `internal/acpwire/doc.go`, `internal/acpwire/errors.go`, `internal/acpwire/errors_test.go`, `internal/acpwire/rules_test.go`.

**Steps:**

1. **`doc.go`.** The package comment says:
   - this package holds the types both sides of the client seam exchange (spec C1);
   - it imports only the standard library, like `web/`;
   - every field is exported and JSON-tagged with a camelCase name. Its type is a bool, a number or a string, a slice, string-keyed map or pointer of those or of this package's types, or `json.RawMessage` (C3);
   - `TestWireImportsOnlyTheStandardLibrary` and `TestWireFieldsFollowTheRules` enforce this.
2. **`errors.go`.** Declare `ErrorCode`, its constants and `Error` as in Contracts. Each constant gets a one-line doc saying when the agent side returns it:
   - `quiescing`: the session is quiescing for an end or a swap;
   - `no_turn_stop`: no turn-stop service is attached;
   - `human_gate_required`: the action needs a person's answer;
   - `keyring_unavailable`: the OS keychain cannot be reached;
   - `auth_required`: a provider or MCP server needs a login;
   - `provider_error`: a provider rejected the call. `Data` carries its fields, which plan 7 defines;
   - `stale_work_cursor`: the work cursor is older than the work snapshot;
   - `request_settled`: the request was already answered, cancelled or settled elsewhere;
   - `busy`: a turn is running;
   - `session_closed`: a swap replaced the session (C4).

   `(*Error).Error` returns `Message`, or `string(Code)` when `Message` is empty.
3. **`errors_test.go`.**
   - `TestErrorMessageFallsBackToTheCode`: `(&Error{Code: CodeBusy}).Error()` is `busy`, and with `Message: "a turn is running"` it is that message.
   - `TestErrorRoundTripsAsJSON`: `&Error{Code: CodeBusy, Message: "m"}` marshals to exactly `{"code":"busy","message":"m"}` and unmarshals back to an equal value.
4. **`rules_test.go`.**
   - `TestWireImportsOnlyTheStandardLibrary`: parse every non-test `.go` file in the package directory with `parser.ImportsOnly`, as `TestWebIsStdlibOnly` (`web/bridge/boundary_test.go:21`) does. Fail on any import that starts with `marshal/`, or whose first path element contains a dot.
   - A helper `wireRuleViolations(fset *token.FileSet, files []*ast.File) []string` returns one message per broken rule, as `<Type>.<Field>: <reason>`. First it collects every type name the files declare. Then it checks each type declaration:
     - a struct field must have a name (no embedded fields), and the name must be exported;
     - its tag must have a `json` key. The name before the comma matches `^[a-z][A-Za-z0-9]*$`, and the only option allowed is `omitempty`;
     - its type must be allowed, and so must a non-struct type's underlying type (such as `ErrorCode string`).

     An allowed type is one of:
     - `bool`, `string`, `byte`, `int`, `int8`, `int16`, `int32`, `int64`, `uint`, `uint8`, `uint16`, `uint32`, `uint64`, `float32` or `float64`;
     - a type the package declares;
     - `json.RawMessage`;
     - a pointer or a slice (not a fixed-length array) of an allowed type;
     - a map with key `string` and an allowed value.

     Anything else is rejected, including interfaces, `any`, funcs, channels and other packages' types such as `time.Time`.
   - `TestWireFieldsFollowTheRules`: parse the package's non-test files (with comments off) and require that `wireRuleViolations` returns nothing.
   - `TestWireRuleCatchesViolations` guards the guard. Parse a source string holding one struct with five bad fields:
     - an unexported field;
     - a field with no tag;
     - a field tagged `json:"snake_case"`;
     - a `time.Time` field;
     - a `map[int]string` field.

     It must report exactly five messages, one naming each field.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
TMPDIR=/private/tmp/p2-1-suite go test ./internal/acpwire/ -v 2>&1 | grep -E '^(--- |ok|FAIL)'
go vet ./internal/acpwire/
```

Expected: all five tests pass.

## Task 2: The client package

**Goal:** `internal/acpclient` holds the two interfaces, the method table and the error helpers. Its tests keep the table, the doc comments and its imports honest.

**Files:** `internal/acpclient/acpclient.go`, `internal/acpclient/methods.go`, `internal/acpclient/errors.go`, `internal/acpclient/methods_test.go`, `internal/acpclient/errors_test.go`, `internal/acpclient/imports_test.go`.

**Steps:**

1. **`acpclient.go`.** The package comment summarizes C2:
   - the TUI reaches the agent side only through `Host` and `Session`;
   - reads are synchronous and calls take a context;
   - each method's doc ends with an `ACP:` line;
   - the TUI may import this package and `acpwire`, never `inproc`.

   Declare `Host` and `Session` exactly as in Contracts, with those doc comments. Each interface's own doc says it gains its groups' methods plan by plan (spec Migration order).
2. **`methods.go`.** Declare `Method` and `Methods` as in Contracts. The doc on `Methods` says two tests keep it in step: `TestMethodTableCoversTheInterfaces` here, and `TestACPClientMethodTableMatchesTheServer` in `internal/acp`.
3. **`errors.go`.** `IsCode` reports whether `errors.As` finds an `*acpwire.Error` with that code. `CodeOf` returns the code `errors.As` finds, and `"", false` when it finds none (a nil error included).
4. **`methods_test.go`.**
   - `TestMethodTableCoversTheInterfaces`: walk the method sets of `reflect.TypeOf((*Host)(nil)).Elem()` and of `Session`. Every method has exactly one row with its `Iface` and `Name`, every row names a method that exists, and no row repeats.
   - `TestMethodDocsNameTheirACPMethod`: parse `acpclient.go` with `parser.ParseComments`. For each method in the `Host` and `Session` interface declarations, find the doc line that starts with `ACP: `:
     - a row with `ACP == ""` must read `ACP: none`, followed by anything;
     - any other row must read `ACP: <row.ACP> (exists)` when `Exists` is true, and `ACP: <row.ACP> (new in P3)` when it is false.

     A method with no such line fails.
5. **`errors_test.go`.**
   - `TestIsCodeSeesThroughWrapping`: wrap `&acpwire.Error{Code: acpwire.CodeBusy}` with `fmt.Errorf("wrap: %w", …)`. `IsCode` with `CodeBusy` is true and with `CodeQuiescing` is false, and `CodeOf` returns `CodeBusy, true`.
   - `TestCodeOfAnUncodedError`: `CodeOf(errors.New("x"))` and `CodeOf(nil)` both return `"", false`.
6. **`imports_test.go`.** `TestClientImportsStayOnTheWireSide` parses the non-test files directly in `internal/acpclient`, not its subdirectories. Each may import only:
   - the standard library;
   - `marshal/internal/acpwire`;
   - the data packages C3 allows in signatures: `marshal/internal/app/config`, `marshal/internal/llm/routing`, `marshal/internal/llm/schema` and `marshal/internal/interaction`.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
TMPDIR=/private/tmp/p2-1-suite go test ./internal/acpclient/ -v 2>&1 | grep -E '^(--- |ok|FAIL)'
go vet ./internal/acpclient/
```

Expected: all five tests pass.

## Task 3: The in-process adapter

**Goal:** `inproc` implements `Host` and `Session` over a `session.State`, and `Swap` gives the legacy swap path a fresh session.

**Files:** `internal/acpclient/inproc/inproc.go`, `internal/acpclient/inproc/inproc_test.go`, `internal/acpclient/inproc/main_test.go`.

**Steps:**

1. **`inproc.go`.**
   - The package comment says: this is the in-process adapter (spec C4); P3 deletes it; it must not import `internal/app`, because TUI tests import it and `app` imports `tui`.
   - Declare `Deps`, `Host` and `Session` as in Contracts.
   - `New` builds a `Session` over `d.State`, records it as the host's `current`, and returns both.
   - `Swap` locks `mu` and marks `current` closed. It then builds a `Session` over the new state, stores it as `current`, and returns it.
   - `SharedFilesystem` returns `true`.
   - `ID` returns `state.SessionID()`. Its doc says a closed session keeps answering with its final value (C4), which holds because its state never changes.
   - Add the compile-time assertions `var _ acpclient.Host = (*Host)(nil)` and `var _ acpclient.Session = (*Session)(nil)`.
2. **`main_test.go`.** `func TestMain(m *testing.M) { os.Exit(testenv.Run(m)) }`, as `internal/acp/main_test.go` does.
3. **`inproc_test.go`** (package `inproc`). Build states as the TUI tests do: `session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{SessionID: "s1"})`.
   - `TestSessionIDIsTheStateID`: `New` over `s1` gives a session whose `ID()` is `s1`.
   - `TestSwapClosesTheOldSession`: after `New` over `s1`, `Swap` over a state with ID `s2` returns a session whose `ID()` is `s2`, which is now `h.current`. The old session's `closed` is true and the new one's is false. The old session's `ID()` is still `s1`.
   - `TestHostSharesTheFilesystem`: `SharedFilesystem()` is true.
   - `TestAdapterDoesNotImportApp`: parse the package's non-test files. Fail on an import of exactly `marshal/internal/app`, or of `marshal/internal/app/tui` or anything under it.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
TMPDIR=/private/tmp/p2-1-suite go test ./internal/acpclient/... -v 2>&1 | grep -E '^(--- |ok|FAIL)'
go vet ./internal/acpclient/...
```

Expected: the four new tests pass, and so do Task 2's.

## Task 4: The ACP side of the method table

**Goal:** a test in `internal/acp` fails when `acpclient.Methods` says a method exists and the server does not register it, or the other way round.

**Files:** `internal/acp/acpclient_methods_test.go`.

**Steps:**

1. `TestACPClientMethodTableMatchesTheServer` builds the fully registered server as `TestWorkACPAvailableWithoutModelRunner` (`internal/acp/host_test.go:262`) does. That means `newAgentHost` with a `startRuntime` that returns an error and a discarding logger, then `NewServer(strings.NewReader(""), io.Discard)`, then `registerHandlers` with a live `atomic.Bool`, deferring the cleanup.
2. For each row of `acpclient.Methods` with a non-empty `ACP`:
   - when `Exists` is true and `srv.handlers[row.ACP]` is missing, fail with the T4 "existing method missing" string from Contracts;
   - when `Exists` is false and the handler is registered, fail with the T4 "new method already there" string.

   Rows with no `ACP` are skipped.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
TMPDIR=/private/tmp/p2-1-suite go test ./internal/acp/ -run 'TestACPClientMethodTableMatchesTheServer|TestWorkACPAvailableWithoutModelRunner' -v 2>&1 | grep -E '^(--- |ok|FAIL)'
```

Expected: both pass. As a check of the test (no commit): temporarily set the `ID` row's `ACP` to `session/nope`, see the test fail with the first string, then revert.

## Task 5: `gitinfo` and `changedfiles` move to `internal/repo`

**Goal:** the two packages that read the project's git state live on the agent side, and neither imports a TUI package.

**Files:**
- moved: `internal/app/tui/gitinfo/` → `internal/repo/gitinfo/`, and `internal/app/tui/changedfiles/` → `internal/repo/changedfiles/` (all four files of each, tests included);
- edited: `internal/repo/changedfiles/changedfiles.go`, `internal/acp/turn.go`, `internal/app/tui/model.go`, `internal/app/tui/pump.go`, `internal/app/tui/commands_dispatch.go`, `internal/app/tui/sessionsheet/section.go`;
- tests: `internal/app/tui/model_test.go`, `internal/app/tui/status_test.go`, `internal/app/tui/sessionsheet/section_repo_test.go`.

**Steps:**

1. Move both directories with `git mv`. The package names stay `gitinfo` and `changedfiles`.
2. **`changedfiles.go`.**
   - `Read` returns `[]File`: `snap.Files` when the snapshot's status is `StatusOK` and it has files, and `nil` for a clean tree or a failure, as today.
   - Drop the `sessionsheet` import and the conversion loop.
   - The comment's last sentence, about what `sessionsheet.ChangedFile` has no field for, now says that `File.CountsKnown` carries that distinction.
3. **Imports.** Replace `marshal/internal/app/tui/gitinfo` with `marshal/internal/repo/gitinfo`, and `…/tui/changedfiles` with `marshal/internal/repo/changedfiles`, in every file that imports them: `internal/acp/turn.go`, and in `internal/app/tui` the files `model.go`, `pump.go`, `commands_dispatch.go`, `sessionsheet/section.go`, `model_test.go`, `status_test.go` and `sessionsheet/section_repo_test.go`. `turn.go`'s `buildChangedFiles` reads only `Path`, `Added` and `Removed`, which `File` has, so its body does not change.
4. **The TUI's conversion.** Add `sheetChangedFiles` to `pump.go` (Contracts). It returns nil for nil input, and otherwise one `sessionsheet.ChangedFile` per file with `Path`, `Status`, `Added` and `Removed` copied. Wrap both `changedfiles.Read` calls in it: `sheetBaseRefCmd` (`pump.go:134`) and the `m.sheetChanged` assignment (`model.go:1994`).

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go build ./...
TMPDIR=/private/tmp/p2-1-suite go test ./internal/repo/... ./internal/acp/... ./internal/app/tui/...
go vet ./internal/repo/... ./internal/app/tui/... ./internal/acp/
grep -rn 'tui/gitinfo\|tui/changedfiles' --include='*.go' internal cmd
```

Expected:
- the build succeeds;
- `internal/repo/gitinfo` and `internal/repo/changedfiles` pass, with every test that moved;
- `internal/app/tui` fails only on the Assumptions list;
- the grep prints nothing.

## Task 6: The TUI takes a host and a session

**Goal:** `tui.New(host, sess, opts...)` is the only constructor. The state arrives through `WithLegacyState`, a swap hands the model a new session, and `$EDITOR` is gated on the host. Every test builds its model through a helper.

**Files:**
- `internal/app/tui/model.go`, `internal/app/tui/commands_dispatch.go`, `internal/app/tui/browse_actions.go`;
- `internal/app/app.go`;
- new tests: `internal/app/tui/seam_test.go`, `internal/app/tui_seam_test.go`;
- edited tests: in `internal/app/tui`, the 19 files that call `New(` (listed in step 6), plus `internal/app/app_test.go` and `internal/app/runtime_test.go`.

**Steps:**

1. **`model.go`, the API.**
   - Add the fields `host acpclient.Host` and `sess acpclient.Session` beside `state` (line 333), with a comment saying they are the client seam (P2). `state` gets a comment saying it is the legacy path, which P2 plans 2 to 9 empty.
   - Add `WithLegacyState` after the `Option` type (line 904). It sets `m.state`. Its doc says it is temporary: plan 1 adds it, and plan 9 deletes it once no group reads the state.
   - `SessionSwapResult` (line 119) gains `Session acpclient.Session`, documented as the client session over `State`.
2. **`model.go`, `New` (line 1698)** takes `(host acpclient.Host, sess acpclient.Session, opts ...Option)`. Reorder its start (decision 4):
   1. If `host` or `sess` is nil, panic with the Contracts string.
   2. Build the `Model` literal without `state`, `input` and `syncedAccessMode`, and with `host` and `sess` set. Keep its other fields as today: `editingCommand`, `ctx`, `viewport`, `spinner`, `now`, `viewportFollow` and `discovered`.
   3. Apply the options, as today.
   4. If `m.state` is nil, panic with the Contracts string. Then declare `state := m.state`, so the rest of the body keeps its references unchanged.
   5. Call `loadTheme(state.Config.TUI)` and build the textarea exactly as today, every line from `textarea.New()` to `input.SetStyles(styles)`. Then set `m.input = input`, and set `m.syncedAccessMode` with its existing comment.
   6. The rest of the body continues unchanged from `m.heldSince = m.now()`.
3. **`commands_dispatch.go`, `startNewSession` (line 187).** Right after `m.state = snap.State`, set `m.sess = snap.Session`.
4. **`browse_actions.go`, `openFile` (line 276).** Its first check is now `!m.host.SharedFilesystem()`, which returns `m.setFlash` with the Contracts string. The `$EDITOR` and empty-path checks follow as today.
5. **`app.go`, `Run`.**
   - Right after `state := rt.State` (line 2799), build `seamHost, seamSession := inproc.New(inproc.Deps{State: state})`.
   - `var tuiOpts []tui.Option` (line 2804) becomes a slice whose first element is `tui.WithLegacyState(state)`. It must come first, so no later option can see a nil state.
   - In the swapper's `tui.SessionSwapResult` literal (line 2946), add `Session: seamHost.Swap(state)`. Inside that closure, `state` is the new session's.
   - The program call (line 3023) becomes `tui.New(seamHost, seamSession, tuiOpts...)`.
6. **The package `tui` test helpers (`seam_test.go`, new).**
   - `testSession` returns the session `inproc.New` builds over the state.
   - `testModel` builds a host and a session with `inproc.New`, then returns `New(host, sess, …)` with `WithLegacyState(state)` first and the caller's options after. Its doc says it is the one place tests build a model, so P3 changes only this helper.
   - `fakeHost` implements `acpclient.Host` with `SharedFilesystem` returning its `shared` field.
   - In every package `tui` test file, replace each unqualified call `New(` with `testModel(`. Package-qualified calls such as `session.New(`, `commands.New(` and `picker.New(` stay. At `93088198` that is 223 calls in 19 files:

     | File | Calls | File | Calls |
     |---|---|---|---|
     | `model_test.go` | 172 | `smoke_pickers_test.go` | 2 |
     | `spinner_test.go` | 8 | `verifygate_test.go` | 2 |
     | `interrupt_test.go` | 6 | `approval_mode_test.go` | 1 |
     | `system_access_test.go` | 6 | `commands_plan_test.go` | 1 |
     | `commands_dispatch_test.go` | 5 | `layers_refresh_test.go` | 1 |
     | `history_test.go` | 4 | `livestrip_test.go` | 1 |
     | `view_test.go` | 4 | `pump_test.go` | 1 |
     | `status_test.go` | 3 | `sdd_authoring_test.go` | 1 |
     | `keys_cancel_test.go` | 2 | `sessionsheet_wiring_test.go` | 1 |
     | `skills_plugins_dispatch_test.go` | 2 | | |

     Every call passes a `*session.State` first, so the arguments do not change.
   - `fakeSessionSwapper.NewSession` (`model_test.go:68`) also sets `Session: testSession(f.newState)`, but only when `newState` is not nil. `frame_invariants_test.go:134` builds one with no state.
7. **New tests in `seam_test.go`.**
   - `TestNewRequiresTheLegacyStateUntilPlan9`: `New(fakeHost{shared: true}, testSession(state))` with no options panics with the legacy-state string. `New(nil, testSession(state), WithLegacyState(state))` panics with the nil string.
   - `TestOpenFileNeedsASharedFilesystem`: `t.Setenv("EDITOR", "vi")`, then build the model with `New(fakeHost{shared: false}, testSession(state), WithLegacyState(state))`. `openFile("main.go", 1)` returns a non-nil command, and `m.flash` is the gate string.
   - `TestOpenFileWithASharedFilesystemReachesTheEditorChecks`: `t.Setenv("EDITOR", "")`, `testModel(state)`, `openFile("main.go", 1)`. `m.flash` is `$EDITOR is not set`.
   - `TestNewSessionSwapsTheClientSession`: as `TestSlashCommandClearMessages` (`model_test.go:2718`) drives `/new`, but with the old state's `Persistence{SessionID: "old"}` and the new state's `Persistence{SessionID: "new"}`. Before the swap `m.sess.ID()` is `old`. After it, `m.state` is the new state and `m.sess.ID()` is `new`.
8. **The package `app` test helper (`internal/app/tui_seam_test.go`, new).** `testTUIModel` does in package `app` what `testModel` does in package `tui`. In `app_test.go`, its four `tui.New(` calls (lines 6465, 6540, 6576 and 6676) become `testTUIModel(` with the same arguments.
9. **Fix `TestAccessTUIOptionsResolveWithoutConfigReload` (`app_test.go:6454`, decision 8).** Replace its effective-state check (line 6494) with one that reads `d.Access.Effective`. It fails unless the mode is `"act"`, `Auto` is false and `System` is true. The message is `effective = %+v, want mode act with system access and no auto`, with the value it got. It compares no `Revision`. Use the literal `"act"` as the neighbouring access tests do, so `app_test.go` gains no import. The doc comment does not mention a mode, so it stays. Nothing else in the test changes.
10. **Fix `TestRuntimeNewAndResumedSessionResetAccess` (`runtime_test.go:1703`, decision 8).**
    - Add a helper `withoutRevision(s session.AccessState) session.AccessState` just above the test. It returns `s` with `Revision` set to 0, and its doc says the counter counts commits and is not part of what a test means by "the same access".
    - After `cfg.Skills.Autoload = nil` (line 1706), set `cfg.Agent.Mode = interaction.ModeAsk`. The file already imports `interaction`.
    - Both `approve(req, "copilot")` calls in this test (lines 1725 and 1740) become `approve(req, "act")`. The other tests in the file that approve with `"copilot"` pass today and stay as they are.
    - The granted-pair check (line 1728) compares `withoutRevision(rt.State.AccessState())` with `session.AccessState{Mode: "act", System: true}`.
    - The three checks against `want` (lines 1737, 1743 and 1758) wrap their state's `AccessState()` in `withoutRevision`. `want` itself (line 1736) stays `{Mode: string(cfg.Agent.Mode), System: false}`, which is now Ask.
    - The doc comment above the test already describes this behaviour and stays.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go build ./...
go vet ./internal/app/... ./internal/acpclient/...
TMPDIR=/private/tmp/p2-1-suite go test ./internal/app/tui/ -run 'TestNewRequiresTheLegacyState|TestOpenFile|TestNewSessionSwapsTheClientSession|TestSlashCommandClearMessages' -v 2>&1 | grep -E '^(--- |ok|FAIL)'
TMPDIR=/private/tmp/p2-1-suite go test ./internal/app/ -run 'TestAccessTUIOptions|TestRuntimeNewAndResumedSessionResetAccess' -count=3 -v 2>&1 | grep -E '^(--- |ok|FAIL)'
TMPDIR=/private/tmp/p2-1-suite go test ./internal/app/tui/... ./internal/app/
grep -rnE '(^|[^.[:alnum:]_])New\(' internal/app/tui/*_test.go
```

Expected:
- the named tests pass, including `TestAccessTUIOptionsResolveWithoutConfigReload`, `TestAccessTUIOptionsFollowNewSession` and `TestRuntimeNewAndResumedSessionResetAccess` in all three runs;
- both packages fail only on the Assumptions list, without the two fixed tests;
- the grep prints only `seam_test.go`'s own `New(` calls (in `testModel` and the two tests that pass a `fakeHost`) and lines whose `New(` is in a comment or a string.

## Task 7: The boundary test

**Goal:** `TestTUIStaysOnTheClientSeam` fails on any new TUI use of the agent side, and on any allowlist entry that is no longer needed (T1).

**Files:** `internal/app/tui/boundary_test.go`, `internal/app/tui/boundary_allowlist_test.go`.

**Steps:**

1. **`seamImportAllowed`** implements C1's rule for one import path. It returns true for:
   - a standard library path (its first element has no dot) that does not start with `marshal/`;
   - a third-party path (its first element has a dot);
   - `marshal/internal/app/tui` and anything under it (check for `marshal/internal/app/tui/` as a prefix, so a sibling such as `…/tuix` is not allowed);
   - exactly `marshal/internal/acpwire` and exactly `marshal/internal/acpclient` (so `…/acpclient/inproc` is not allowed);
   - exactly the data packages `marshal/internal/app/config`, `…/llm/routing`, `…/llm/schema`, `…/interaction`, `…/app/clipboard`, `…/strutil`, `…/diffview`, `…/jsonextract` and `…/progress`.

   Every other `marshal/` path is agent side.
2. **`deniedConfigFuncs`** parses the non-test files in `internal/app/config`. From the test's directory that is `../config`. It returns the names of every top-level function with no receiver whose name starts with `Save` or `Load`, plus `UserDir`, `DataDir`, `UserConfigPath` and `ProjectConfigPath`. All four exist at `93088198`, and the set has 16 names.
3. **`seamViolations`** walks `root`, skipping `testdata` directories and `_test.go` files, and parses each `.go` file. For each file (keyed by its slash path relative to `root`):
   - every import that `seamImportAllowed` rejects is an entry;
   - if the file imports `marshal/internal/app/config`, take that import's local name (`config`, or its alias), and every selector `<name>.<F>` where `F` is in the denied set is an entry `config.<F>`. No file aliases the package today.

   Entries are de-duplicated and sorted per file.
4. **`formatAllowlist`** renders the map as the body of a Go map literal, one line per file in sorted order: `"<file>": {"<entry>", …},`.
5. **`boundaryAllowlist`** (`boundary_allowlist_test.go`). Its doc comment says:
   - it lists every TUI file that still crosses the seam, by import and by config function;
   - each P2 plan deletes its group's entries, and plan 9 empties it;
   - an entry is never added to make the test pass. A new use belongs behind `acpclient`.

   Start it empty.
6. **`TestTUIStaysOnTheClientSeam`** computes the violations for `.` with the denied set, then compares them with `boundaryAllowlist`:
   - for each violation the allowlist lacks, `t.Errorf` with the T1 "new violation" string;
   - for each allowlist entry no longer found, `t.Errorf` with the T1 "stale entry" string;
   - if anything failed, `t.Logf` the output of `formatAllowlist` for the current violations, so the list can be regenerated.
7. **Guard tests.**
   - `TestSeamImportAllowedClassifies`:
     - allowed: `fmt`, `charm.land/lipgloss/v2`, `marshal/internal/app/tui/glyph`, `marshal/internal/acpwire`, `marshal/internal/acpclient`, `marshal/internal/app/config`;
     - denied: `marshal/internal/acpclient/inproc`, `marshal/internal/app/session`, `marshal/internal/viewmodel`, `marshal/internal/repo/gitinfo`, `marshal/internal/app/tuix`.
   - `TestDeniedConfigFuncsFindsTheFileFunctions`: the set contains `SaveUserConfigValue`, `UserConfigPath`, `DataDir`, `UserDir` and `ProjectConfigPath`, and not `Default`, `Diagnose` or `LookupPath`.
8. **Fill the allowlist.** Run the test with the empty map, paste the logged literal into `boundaryAllowlist`, gofmt, and run it again. At `93088198`, after Tasks 5 and 6, the list has:
   - 66 files;
   - 140 import entries, including `marshal/internal/repo/gitinfo` in `commands_dispatch.go`, `model.go`, `pump.go` and `sessionsheet/section.go`, and `marshal/internal/repo/changedfiles` in `model.go` and `pump.go`;
   - 22 `config.*` entries, such as `model.go`'s nine and `settings/browser.go`'s five.

   A later base may differ. Record the actual counts in Task 8.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
TMPDIR=/private/tmp/p2-1-suite go test ./internal/app/tui/ -run 'TestTUIStaysOnTheClientSeam|TestSeamImportAllowedClassifies|TestDeniedConfigFuncs' -v 2>&1 | grep -E '^(--- |ok|FAIL)'
go vet ./internal/app/tui/
```

Expected: all three pass.

As checks of the test (no commit):
- temporarily add `import _ "marshal/internal/acpclient/inproc"` to `browse_actions.go`, and see the test fail with the new-violation string naming that file;
- temporarily delete one `boundaryAllowlist` entry and see the same message;
- temporarily add a made-up entry and see the stale-entry string;
- revert all three.

## Task 8: Verification and docs

**Goal:** the full suite matches the baseline, and the docs describe the seam's packages.

**Files:** `AGENTS.md`, `docs/ARCHITECTURE.md`, `docs/daemon/p2-client-seam.md`, `docs/daemon/design.md`.

**Steps:**

1. **`AGENTS.md`.**
   - In "Where things live", drop `changedfiles,gitinfo` from the `internal/app/tui/{sddreview,…}` line.
   - Under "Repo intelligence", add `internal/repo/{gitinfo,changedfiles}   git branch/worktree info, changed files against a base ref`.
   - Under "App shell & config", add:
     - `internal/acpwire/       wire types and error codes for the TUI client seam (stdlib only)`;
     - `internal/acpclient/     the TUI's Host/Session interfaces; inproc/ is the in-process adapter (deleted in P3)`.
   - In "Design constraints", extend the "The TUI renders only" bullet: the TUI reaches the agent side through `internal/acpclient` (daemon P2, in progress). `TestTUIStaysOnTheClientSeam` (`internal/app/tui/boundary_test.go`) fails on any new direct use. As with `TestWebIsStdlibOnly`, the fix is to move the call behind the interface, never to add an allowlist entry.
2. **`docs/ARCHITECTURE.md`.** After the TUI row of the layer table (line 81), add a "Client seam" row:
   - packages: `internal/acpwire/`, `internal/acpclient/` and `internal/acpclient/inproc/`;
   - responsibility: the TUI's only path to the agent side. That is the wire types and error codes, the `Host` and `Session` interfaces, and the in-process adapter that implements them until P3. `TestTUIStaysOnTheClientSeam` enforces it with a shrinking allowlist.
3. **`docs/daemon/p2-client-seam.md`.** After the Revision record, add a section `## Implementation record` with one entry: plan 1 (foundation), branch `p2-1-foundation`, the packages it added, and the allowlist's file, import and config-entry counts as Task 7 measured them.
4. **`docs/daemon/design.md` §7.** The P2 row's status reads `specified (revised 2026-10-25), nine plans; plan 1 implemented (see the spec's implementation record)`.
5. **Full verification.**

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
gofmt -l ./internal ./cmd
go vet ./...
TMPDIR=/private/tmp/p2-1-suite CGO_ENABLED=1 go build ./cmd/marshal
TMPDIR=/private/tmp/p2-1-suite go test ./... -timeout 20m 2>&1 | grep -E '^(--- FAIL|FAIL|ok)' | grep -v '^ok'
```

Expected:
- `gofmt -l` prints nothing;
- `go vet` is clean;
- the binary builds;
- the failures are exactly the Assumptions list without `TestAccessTUIOptionsResolveWithoutConfigReload` and `TestRuntimeNewAndResumedSessionResetAccess`, and a flaky one counts only if it passes alone with `-count=3`.

Finally, run `./marshal` briefly in this repository: open a session, browse to a call row with a file, and press `o`. `$EDITOR` opens as before.

## Integration notes

- **Plan 2** (status and events) starts from this branch's merge. It adds `Session.Changed` and `Session.Status`, moves the work types into `acpwire`, deletes the broker pumps' allowlist entries, and adds T3 if `Status` carries `routing` types.
- **The allowlist is the progress meter.** Each later plan states which entries it deletes, and its Verify runs `TestTUIStaysOnTheClientSeam`.
- **`WithLegacyState` stays first** in every option list until plan 9 deletes it.

## Self-review

- Every task has its own Verify and commits alone. Tasks 1 to 4 add packages and tests only; Task 5 is a move; Task 6 is the one cross-package task, because changing `tui.New` breaks `app.go`'s build until both change, and every edit in it is pinned.
- Cold-executor check: each task names its functions, test names, strings and line anchors. The one generated artifact, the allowlist, comes from the test's own output, and its expected counts are given.
- Contracts hold every cross-task declaration: the codes, the interfaces, the table, the adapter, the changed `Read`, the new `New` and the helpers.
- Anchors were verified at `93088198`. The 223 calls in 19 files and the 66/140/22 allowlist counts were measured there.
- Nothing here contradicts the spec. Decisions 1, 2 and 3 refine C1, C2 and C4, and the spec's C4 and Migration order already say so.
