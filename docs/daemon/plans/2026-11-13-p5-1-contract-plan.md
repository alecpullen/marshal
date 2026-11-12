# P5 plan 1: the contract — implementation plan

Date: 2026-11-13. Status: ready for review.

Base: `main` at `d5e63dcd`. Every anchor below was checked there.
- The code at `d5e63dcd` is the code at `7ae94a9b`, where the full suite ran
  (`TMPDIR=/private/tmp/p5-base`, default timeout): 122 packages ok, and
  `internal/acp` failed its two budget tests (`TestNoteCallBudget`,
  `TestStackRoundTripBudget`) under the suite's load. The package passed alone,
  and both tests passed three times in a row.
- `svelte-check` in `web/ui` reports 6 errors at the base: one each in
  `src/lib/fleet.test.ts`, `src/lib/Sidebar.test.ts`, `src/views/Review.test.ts`,
  `src/views/Runs.test.ts`, `src/views/Chat.svelte` and `src/views/Run.svelte`.

**The spec edits are already made,** uncommitted in the main checkout, in
`docs/daemon/p5-fleet-screen.md`:
- the Status line;
- "Where the code is today"'s two new bullets;
- the Behaviour section (F1 to F8) and the Design table;
- Migration order items 1 and 2;
- three new findings;
- the Implementation record's placeholders.

**Plan 2 runs beside this one.** Its Tasks 1 to 3 touch only a new
`web/bridge/inbox.go`, its tests, the route table in `http.go` and the fleet's
`emit` (`notify.go`). This plan changes handler bodies in `http.go`, never the
route table, and does not touch `notify.go`. Plan 2's later tasks wait for this
plan to reach `main`. Both plans fill their own placeholder in the spec's
Implementation record, and nothing else in the spec.

Approved design: [p5-fleet-screen.md](../p5-fleet-screen.md): decision 1, F1,
Migration order item 1, Tests W1.
Previous plans: [P4 plan 10, fleet agents on the project's child](2026-11-12-p4-10-fleet-plan.md),
the last change to the fleet's types, and
[P4 plan 4, project children and the relay](2026-11-10-p4-4-relay-plan.md),
whose `GET /api/daemon` answer `internal/daemon` mirrors.
Authoring workflow: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md).

**How this plan is written.** No prototype was built, and the plan embeds no
implementation code. The Contracts section holds declarations only. Every other
step states its decision in prose. Executing the plan also tests how well
Marshal follows a plan. "As `X` does" means `X` exists and is the model to copy.

**Goal:** the fleet loop's wire types live in one standard-library package,
`web/bridge/contract`.
- The bridge encodes them. `internal/daemon` decodes them.
- `web/ui/src/lib/contract.ts` is generated from them, and a test fails when it
  is stale.
- Not one byte of any answer or delta changes.

**Non-goals:**
- **No new route, field or delta.** The inbox is plan 2's. The answers to
  permissions and questions move in plan 5.
- **Types outside decision 1's list stay where they are,** and so do the bridge's
  out-of-loop deltas: automations, network blocks and reroutes.
- **No change on the agent side.** `internal/acp` gains one test.
- **The TUI does not import the package yet.** `seamImportAllowed` gains its
  entry in plan 3.
- **The six type errors the web UI has today stay.** The gate is that there are
  no more.
- **No changelog entry.**

**Assumptions:**
- Every Go test command below runs with `TMPDIR=/private/tmp/p5-1-suite`. Create
  the directory first. Keep the path short: a long one breaks the unix-socket
  tests in `web/bridge` and `test/daemon`.
- **`web/` is standard library only** (`TestWebIsStdlibOnly`,
  `web/bridge/boundary_test.go:21`). `forbiddenImport` (`:68`) refuses every
  path that starts with `marshal/`. `TestForbiddenImportClassifies` (`:80`) pins
  its cases.
- **The types that move** (decision 1), with where each is today:
  - `AgentStatus` (`web/bridge/fleetevents.go:355`), `PendingRequest` (`:143`),
    and `fleetDelta` (`:9`), whose two fields kept off the wire are
    `accessSession` (`:22`) and `Usage` (`:54`);
  - `AccessState` (`web/bridge/interaction.go:21`);
  - `AgentWorkspace` (`web/bridge/workspace.go:110`) and `GateOverride`
    (`:154`). Both are also persisted in `fleet.json` inside `Agent`, so their
    encoding is the registry's format too;
  - `ProjectStatus` (`web/bridge/fleet.go:112`);
  - `gateRecord` (`web/bridge/gate.go:10`), `ExitResult`
    (`web/bridge/exit.go:40`) and `gateResult` (`:51`). `ExitOptions` (`:27`)
    is the bridge's own and stays;
  - `Budgets` (`web/bridge/budget.go:41`), `budgetDelta` (`:50`),
    `budgetAgentSpend` (`:321`) and `budgetReport` (`:328`), whose `Daily` is an
    anonymous struct;
  - `DaemonInfo` (`web/bridge/daemoninfo.go:9`), `DaemonRefresh` (`:22`),
    `DaemonChild` (`:28`), `DaemonSession` (`web/bridge/registry.go:102`) and
    `PinState` (`web/bridge/pins.go:70`).
- **None of them has a method in the bridge,** so every one can become an alias.
- **The answers built from maps today** (`web/bridge/http.go`):
  - `project_removed` (`:434`);
  - the spawn answer (`:540`), whose request body is an anonymous struct
    (`:480`);
  - the merge body (`:606`) and the exit body (`:642`), both anonymous;
  - the discard answer (`:638`);
  - `listPending` (`:1248`), whose map writes every key, empty or not;
  - the approve answer (`:1282`) and the deny answer (`:1296`).

  `encoding/json` writes a map's keys in sorted order.
- **The replies the bridge forwards unread:**
  - the diff (`Fleet.Diff`, `web/bridge/fleet.go:1581`), from `acp.DiffFile` and
    `acp.DiffResult` (`internal/acp/worktree.go:194`, `:200`);
  - the merge (`Fleet.Merge`, `:1600`), from `acp.MergeResult` (`:336`);
  - the commit draft (`Fleet.CommitDraft`, `web/bridge/gate.go:88`), a map with
    one key, `message` (`internal/acp/exit.go:154`).
- **How the bridge encodes:**
  - `writeJSON` (`web/bridge/http.go:272`) uses
    `json.NewEncoder` with its defaults.
  - The fleet stream goes through `Fleet.emit` (`web/bridge/notify.go:176`) into
    the fleet log (`EventLog.Append`, `events.go:94`), keyed `fleetStreamKey`
    (`fleet.go:1817`). `Tail` (`events.go:181`) reads the log back.
  - Deltas are made in:
    - `classifyNotification` (`fleetevents.go:67`): activity, mode, telemetry,
      watch and run;
    - `installClassifier` (`fleet.go:1835`), which rewrites the session ID,
      sets `AgentID` and `At`, and makes pending (`:1882`);
    - `storeGate` (`gate.go:33`), which makes gate;
    - `terminal.go:525`, which makes hold;
    - `budget.go:230` and `:243`, which make budget.
  - `liveState.apply` (`fleetevents.go:190`) reads `accessSession`.
    `fleet.go:1857` reads `Usage`.
- **`internal/daemon`** keeps its own copies of the daemon's answer:
  `ChildInfo` (`client.go:27`), `SessionInfo` (`:36`), `RefreshInfo` (`:43`),
  `Info` (`:49`) and `PinState` (`:62`). Their field names match the bridge's.
  `cmd/marshal/daemon.go` and `internal/launch/daemon.go` use them.
- **The web UI** (`web/ui/src/lib/api.ts`) declares these by hand:
  - `PendingRequest` (`:223`), `AgentStatus` (`:230`) and `ProjectStatus`
    (`:262`);
  - `SpawnRequest` (`:273`), `DiffFile` (`:280`), `DiffResult` (`:286`),
    `MergeResult` (`:291`), `GateResult` (`:313`), `ExitResult` (`:320`),
    `GateRecord` (`:487`) and `PendingSubmission` (`:570`);
  - `Budgets` (`:874`), `BudgetAgent` (`:886`) and `BudgetStatus` (`:888`).

  `getCommitDraft` (`:736`) reads `{ message }` inline. `fleet.ts` declares
  `FleetDelta` (`:7`), `RunDelta` (`:32`), `BudgetDelta` (`:34`),
  `ProjectRemovedDelta` (`:42`) and `PendingDelta` (`:48`), joined in
  `FleetEvent` (`:51`). `AccessStateWire` is `interaction.ts:33`.
  `tsconfig.json` is `strict`.
- **The built UI is committed** under `web/bridge/static/`, embedded by
  `web/bridge/assets.go:19`.
- **Bridge tests:**
  - `doReq` (`http_test.go:43`) and `decodeBody`;
  - `projectEnv` (`projects_test.go:15`), which is a fleet over `fakeAgent`
    (`fakeagent_test.go:22`) with one project;
  - `testFleetWithClient` and `registerRepo`, as
    `TestPendingSpawnRetainsCanonicalTuplePresence` (`fleet_test.go:866`) uses
    them;
  - `Workspace.PutPending` (`workspace.go:884`).

**Decisions this plan makes beyond the spec** (F1 records them):

1. **The bridge keeps its names, as aliases of the contract's.** No caller
   changes, and the type is the same, so its encoding is too. That includes the
   unexported names: `gateRecord` becomes an alias of `contract.GateRecord`.
2. **`fleetDelta` embeds `contract.FleetDelta`,** and keeps its two off-wire
   fields unexported, so the encoder never sees them. `Usage` becomes `usage`.
3. **A map answer becomes a struct whose fields are in the map's sorted key
   order,** none `omitempty`. `PendingSubmission` also keeps an `ownerId`, which
   is `omitempty` and never set today. The web UI's owner rule reads it, and it
   is never encoded, so no byte changes.
4. **A request body's optional fields are `omitempty`.** Decoding is unchanged,
   the TypeScript marks them optional as `api.ts` does today, and a Go client
   leaves them out.
5. **W1 records encodings, not handlers, wherever a type exists today.**
   - A deterministic filler gives every exported field a value. The test
     records both that encoding and the zero value's.
   - The map answers have no type before this plan, so they are recorded
     through their handlers instead, with the ID or path an answer generates
     replaced by a placeholder.
   - The deltas are recorded as the fleet emits them, read back from its log.
6. **The generator is a package with a test, not a command.**
   `TestTypeScriptContractIsCurrent` compares, and the same test with `-update`
   writes. Nothing else runs it.
7. **`npm run check` is `svelte-check --threshold error`.** It sees the `.svelte`
   files too, which `tsc` does not.

**Execution:**
- In the main checkout, if `git status` shows any of these uncommitted, commit
  them to `main` first, as explicit paths, with the message
  `docs/daemon: record P5's behaviour and plans 1 and 2`:
  - `docs/daemon/p5-fleet-screen.md`;
  - this plan, and `docs/daemon/plans/2026-11-13-p5-2-inbox-plan.md`.

  Plan 2's executor may have committed them already. The main checkout holds
  other sessions' untracked work. Leave it all alone.
- Then branch: `git switch -c p5-1-contract` from `main`, or create a worktree
  on that branch.
- Run the tasks in order. Run each task's Verify and commit it as
  `p5-1-contract: task N — <title>`.
- Stage explicit paths only. Never `git add -A` without a path.
- Run `gofmt -w` on every Go file a task touches before its Verify.
- In `web/ui`, run `npm install` once if `node_modules` is missing.

## Contracts

### The package (`web/bridge/contract`)

```go
// Package contract is the fleet loop's wire contract (P5 decision 1): the
// types the bridge encodes and its Go clients decode, and the source of
// web/ui/src/lib/contract.ts. It holds types, the constants of their
// enumerations, and data; no functions, no methods, no state. Standard
// library only.
package contract

// Enumerations.
type AgentState string // idle, running, awaiting-approval, awaiting-question, error
type PendingKind string // approval, question
type TrustState string // na, untrusted, trusted
type WorkspaceSource string // studio, repo
type DeltaKind string // activity, mode, telemetry, watch, run, pending, gate, hold, budget, project_removed
type BudgetScope string // daily, agent

const (
	AgentIdle             AgentState = "idle"
	AgentRunning          AgentState = "running"
	AgentAwaitingApproval AgentState = "awaiting-approval"
	AgentAwaitingQuestion AgentState = "awaiting-question"
	AgentError            AgentState = "error"
)
// ...and one constant per value for each of the other five, named
// Pending<Value>, Trust<Value>, WorkspaceFrom<Value>, Delta<Value> and
// Budget<Value>, in the order listed.

// agents.go: AgentStatus, AccessState, PendingRequest, GateOverride and
// AgentWorkspace, each with today's fields, tags and doc comments, with
// these types changed:
//   AgentStatus.Status         AgentState
//   PendingRequest.Kind        PendingKind
//   AgentWorkspace.Source      WorkspaceSource

// projects.go
type ProjectStatus struct { /* today's fields; Trust is a TrustState */ }

// stream.go
type FleetDelta struct {
	Kind         DeltaKind       `json:"kind"`
	SessionID    string          `json:"sessionId"`
	Activity     string          `json:"activity,omitempty"`
	Access       *AccessState    `json:"access,omitempty"`
	ContextPct   *int            `json:"contextPct,omitempty"`
	ChangedFiles *int            `json:"changedFiles,omitempty"`
	PendingKind  PendingKind     `json:"pendingKind,omitempty"`
	Gate         *GateRecord     `json:"gate,omitempty"`
	AgentID      string          `json:"agentId,omitempty"`
	Run          json.RawMessage `json:"run,omitempty"`
	Watch        json.RawMessage `json:"event,omitempty"`
	At           int64           `json:"at,omitempty"`
	ToolStats    json.RawMessage `json:"toolStats,omitempty"`
	Rules        json.RawMessage `json:"rules,omitempty"`
	Held         *bool           `json:"held,omitempty"`
	By           string          `json:"by,omitempty"`
}
type BudgetDelta struct { /* today's budgetDelta; Kind DeltaKind, Scope BudgetScope */ }
type ProjectRemovedDelta struct {
	Kind    DeltaKind `json:"kind"`
	Project string    `json:"project"`
}

// intake.go
type PendingSubmission struct {
	Auto      *bool     `json:"auto"`
	ClientID  string    `json:"clientId"`
	CreatedAt time.Time `json:"createdAt"`
	ExpiresAt time.Time `json:"expiresAt"`
	ID        string    `json:"id"`
	Mode      string    `json:"mode"`
	Origin    string    `json:"origin"`
	// OwnerID is never set by today's bridge; unset is the local owner.
	OwnerID string `json:"ownerId,omitempty"`
	Plan    string `json:"plan"`
	Prompt  string `json:"prompt"`
	Ref     string `json:"ref"`
	RepoID  string `json:"repoId"`
	Title   string `json:"title"`
}
type ApproveResult struct {
	AgentID string `json:"agentId"`
	Status  string `json:"status"`
}
// ActionStatus is the discard and deny answers' {"status": …}.
type ActionStatus struct {
	Status string `json:"status"`
}

// spawn.go
type SpawnRequest struct {
	Project   string          `json:"project"`
	Name      string          `json:"name,omitempty"`
	Mode      string          `json:"mode,omitempty"`
	Auto      *bool           `json:"auto,omitempty"`
	Prompt    string          `json:"prompt,omitempty"`
	Isolated  *bool           `json:"isolated,omitempty"`
	Branch    string          `json:"branch,omitempty"`
	BaseRef   string          `json:"baseRef,omitempty"`
	Routing   json.RawMessage `json:"routing,omitempty"`
	Workspace string          `json:"workspace,omitempty"`
}
type SpawnResult struct {
	AgentID string `json:"agentId"`
	Warning string `json:"warning,omitempty"`
}

// finish.go
type DiffFile struct { /* acp.DiffFile's fields and tags */ }
type DiffResult struct { /* acp.DiffResult's */ }
type CommitDraft struct {
	Message string `json:"message"`
}
type GateResult struct { /* today's gateResult */ }
type GateRecord struct { /* today's gateRecord: Result *GateResult, At */ }
type ExitRequest struct {
	CommitMessage string        `json:"commitMessage"`
	Override      *GateOverride `json:"override,omitempty"`
}
type ExitResult struct { /* today's; Verify is a *GateResult */ }
type MergeRequest struct {
	CommitMessage string `json:"commitMessage,omitempty"`
}
type MergeResult struct { /* acp.MergeResult's */ }

// budgets.go
type Budgets struct { /* today's */ }
type BudgetDaily struct {
	Day      string  `json:"day"`
	SpentUSD float64 `json:"spentUsd"`
	Blocked  bool    `json:"blocked"`
}
type BudgetAgentSpend struct { /* today's budgetAgentSpend */ }
type BudgetReport struct {
	Budgets Budgets            `json:"budgets"`
	Daily   BudgetDaily        `json:"daily"`
	Agents  []BudgetAgentSpend `json:"agents"`
	Loaded  bool               `json:"loaded"`
}

// daemon.go: DaemonInfo, DaemonRefresh, DaemonChild, DaemonSession and
// PinState, each with today's fields, tags and comments.
```

### The bridge's names (`web/bridge`)

```go
// One alias per moved type, in the file that declares the type today:
type AgentStatus = contract.AgentStatus
type gateRecord = contract.GateRecord
type budgetReport = contract.BudgetReport
// ...and so on for every type in the Assumptions' list but fleetDelta.

// fleetevents.go
type fleetDelta struct {
	contract.FleetDelta
	// accessSession is the ACP session the access state belongs to; never
	// encoded.
	accessSession string
	// usage carries a telemetry update's usage rows to the ledger; never
	// encoded.
	usage json.RawMessage
}
```

### The generator (`web/bridge/contract/tsgen`)

```go
// Package tsgen writes the TypeScript module for a contract package (P5 F1).
// Standard library only: it reads the package's syntax, never its compiled
// types.
package tsgen

// Generate reads the Go files of the package in dir, test files excluded,
// and returns the TypeScript module: a header line, then each named string
// type as a union and each struct as an interface, files in name order and
// declarations in source order.
func Generate(dir string) ([]byte, error)
```

### The web boundary (`web/bridge/boundary_test.go`)

`forbiddenImport` keeps its signature. It allows a path that starts with
`marshal/web/`, and still refuses every other `marshal/` path.

## Task 1 — record today's wire

**Files:** a new `web/bridge/contractwire_test.go`, and a new directory,
`web/bridge/testdata/contract/`.

1. A test helper, `fillValue`, sets every exported field of a struct
   deterministically, and returns it.
   - A string is its field's Go name.
   - Every number is 1. A bool is true.
   - A `time.Time` is `2026-01-02T03:04:05.000000006Z`.
   - A `json.RawMessage` is the object `{"k":"v"}`.
   - A pointer points to a filled value. A slice holds one filled element. A
     map holds one entry, keyed `"k"`.
   - An embedded struct is filled the same way.
2. `TestTheWireIsUnchanged` has one subtest per recording. Each subtest
   compares the recording with `testdata/contract/<name>.json`, which holds the
   bytes exactly.
   - With `-update`, the test writes the files instead of comparing them.
   - On a mismatch it reports both encodings.
3. The recordings of types, each encoded through `writeJSON` into a
   `httptest.ResponseRecorder`, filled and zero:
   - every type in the Assumptions' list of moves, under its bridge name;
   - `fleetDelta` too, whose off-wire fields must never appear in its
     encoding, filled or not.
4. The recordings of the map answers, through the handlers with `doReq`:
   - `GET /api/pending`, with two submissions put with `PutPending`. One has
     every field set, with an explicit `Auto` false. The other has only what
     `PutPending` needs. Both have fixed IDs and times.
   - `POST /api/pending/{id}/deny`.
   - `POST /api/agents` on `projectEnv`, with the generated agent ID replaced
     by `<agent>`.
   - `POST /api/agents/{id}/discard` on that agent.
   - `POST /api/pending/{id}/approve`, as
     `TestPendingSpawnRetainsCanonicalTuplePresence` sets up its submission,
     with the agent ID replaced by `<agent>`.

   An answer that cannot be driven to a deterministic result with today's
   fixtures, such as the spawn answer's `warning`, is left out. List it in the
   test's comment.
5. The recordings of the deltas, read back from `Tail(fleetStreamKey)`:
   - activity, mode, telemetry, watch and run, each from a sample
     notification through `classifyNotification` and the fleet's `emit`;
   - pending, gate and hold, from the code that makes them, on a fleet from
     `testFleet`;
   - one budget delta of each scope;
   - `project_removed`, through `DELETE /api/projects` on `projectEnv`.

   Times are replaced by `<time>`, and the root by `<root>`, before comparing.
6. Record the files with `-update` at the base, and commit them with the test.

**Verify:**
- `go test ./web/bridge/ -run TestTheWireIsUnchanged -count=1` passes without
  `-update`, twice in a row.
- Mutation check: rename one JSON tag in `AgentStatus`, such as `prUrl` to
  `prURL`. The test must fail. Restore it.

## Task 2 — the package and the web boundary

**Files:** a new `web/bridge/contract/` (`doc.go`, `agents.go`, `projects.go`,
`stream.go`, `intake.go`, `spawn.go`, `finish.go`, `budgets.go`, `daemon.go`),
and `web/bridge/boundary_test.go`.

1. Write the package as the Contracts section declares it.
   - Copy each moved type's fields, tags and doc comments from where it is
     today.
   - Give each enumeration its constants.
   - The package compiles with no import from the bridge.
2. `forbiddenImport` allows a path under `marshal/web/`, with a reason in its
   comment: `web/`'s own packages, never the agent's.
   `TestForbiddenImportClassifies` gains these cases:
   - `marshal/web/bridge/contract` is allowed;
   - `marshal/internal/acp` and `marshal/cmd/marshal` are still refused.

**Verify:**
- `go build ./web/...`
- `go test ./web/bridge/ -run 'TestWebIsStdlibOnly|TestForbiddenImportClassifies' -count=1`
- `GOOS=linux go vet ./web/bridge/contract/` and
  `GOOS=windows go vet ./web/bridge/contract/`

## Task 3 — the bridge on the contract

**Files:**
- `web/bridge/fleetevents.go`, `interaction.go`, `workspace.go`, `fleet.go`,
  `gate.go`, `exit.go`, `budget.go`, `daemoninfo.go`, `registry.go`, `pins.go`
  and `http.go`;
- any bridge test that builds a `fleetDelta` or a moved type with a composite
  literal.

1. Replace each moved declaration with its alias (decision 1). Keep each doc
   comment as a one-line pointer to the contract's.
2. `fleetDelta` embeds `contract.FleetDelta` (decision 2).
   - `accessSession` stays as it is. `Usage` becomes `usage`, read where
     `fleet.go:1857` reads it today, and set where `fleetevents.go:123` sets it.
   - A composite literal names the embedded value. That covers the pending,
     gate and hold sites, and the bridge's tests.
3. The enumerations:
   - `Snapshot`'s statuses and the classifier's kinds stay untyped constants,
     which the named types accept.
   - Where a value goes into a plain `string`, convert it there. Do not widen
     the contract's field back to `string`.
4. `budgetReport`'s `Daily` becomes `contract.BudgetDaily`. `budgetReport()`
   fills it as before.
5. The map answers become the contract's structs: `project_removed` as
   `ProjectRemovedDelta`, the spawn answer as `SpawnResult`, discard and deny as
   `ActionStatus`, approve as `ApproveResult`, and `listPending` as
   `PendingSubmission`. Build that last one with a small function beside the
   handler, which plan 2 reuses.
6. The anonymous request bodies become `contract.SpawnRequest`,
   `contract.ExitRequest` and `contract.MergeRequest`. The handlers read them as
   before.
7. `TestTheWireIsUnchanged` passes without `-update`: not one recording changes.

**Verify:**
- `go build ./... && go vet ./web/...`
- `go test ./web/bridge/ -run TestTheWireIsUnchanged -count=1`
- `go test ./web/bridge/... -count=1 -timeout 20m`. A hang in
  `TestRecipeRunMaxUSDSetsAPausingCapForTheRun` is the known intermittent one:
  rerun the package and record it.
- `go test ./cmd/webbridge/ ./test/daemon/ -count=1`
- Mutation check: give `PendingSubmission`'s `Title` `omitempty`. The pending
  recording with only the required fields must fail. Restore it.

## Task 4 — the Go clients on the contract

**Files:** `internal/daemon/client.go`, and a new
`internal/acp/contract_test.go`.

1. `internal/daemon`'s `ChildInfo`, `SessionInfo`, `RefreshInfo`, `Info` and
   `PinState` become aliases of the contract's types. Their comments say they
   are the contract's. The attach constants stay: they are the handshake's
   strings, not a type.
2. `TestForwardedRepliesDecodeIntoTheContract`, in `internal/acp`, takes three
   replies:
   - a filled `DiffResult` with two files;
   - a refused `MergeResult` with its conflicts;
   - the commit draft's map.

   It encodes each, and decodes it into the contract's type with
   `DisallowUnknownFields`. Re-encoding gives the same bytes.

**Verify:**
- `go build ./cmd/marshal ./internal/launch/ ./internal/daemon/`
- `go test ./internal/daemon/ ./cmd/marshal/ ./internal/launch/ -count=1`
- `go test ./internal/acp/ -run TestForwardedRepliesDecodeIntoTheContract -count=1`
- Mutation check: add a field to `acp.MergeResult`. The test must fail. Remove
  it.

## Task 5 — the generator

**Files:**
- a new `web/bridge/contract/tsgen/` (`tsgen.go`, `tsgen_test.go`, and a
  `testdata/rules/` fixture package with its expected `testdata/rules.ts`);
- a new, generated `web/ui/src/lib/contract.ts`.

1. `Generate` parses the directory with `go/parser`, comments included, and
   skips `_test.go` files. It writes F1's mapping:
   - a struct becomes an interface;
   - an `omitempty` field is optional;
   - a pointer that is not `omitempty` is `| null`;
   - a field tagged `-`, or unexported, is left out;
   - `time.Time` becomes `string`, and `json.RawMessage` becomes `unknown`;
   - a slice becomes an array, and a map with string keys a `Record`;
   - a named string type with typed constants becomes a union of their values,
     in declaration order;
   - another named type keeps its name;
   - doc comments become `/** */` comments.

   An unsupported construct is an error naming the file, line and construct,
   never a silent `any`: an anonymous struct field, a channel, an interface,
   embedding, a map keyed by a non-string, or a generic type.
2. The header line says the file is generated, by
   `web/bridge/contract/tsgen`, and gives the command that regenerates it. The
   output ends with one newline. Two runs give the same bytes.
3. `TestGeneratorRules` generates the fixture package and compares the result
   with `rules.ts`. The fixture has one case for each rule, and one file with
   each unsupported construct, which must fail with its line.
4. `TestTypeScriptContractIsCurrent` generates `../` (the contract package) and
   compares the result with `../../../ui/src/lib/contract.ts`. On a difference
   it fails, and names the command:
   `go test ./web/bridge/contract/tsgen -run TestTypeScriptContractIsCurrent -update`.
   With `-update`, it writes the file.
5. Generate `contract.ts` with `-update`, and commit it.

**Verify:**
- `go test ./web/bridge/contract/... -count=1`
- `go test ./web/bridge/ -run TestWebIsStdlibOnly -count=1`. The fixture
  package is under `web/`, so the walk parses it too.
- Mutation check: add a field to `contract.ProjectStatus`. The staleness test
  must fail, naming the command. Remove it.

## Task 6 — the web UI on the generated types

**Files:**
- `web/ui/package.json`, `web/ui/src/lib/api.ts`, `web/ui/src/lib/fleet.ts`,
  and `web/ui/src/lib/interaction.ts`;
- the files the type check names;
- `web/bridge/static/`, if the build changes it.

1. Add `"check": "svelte-check --threshold error"` to the scripts (decision 7).
   Run it at the task's start: it reports the base's 6 errors.
2. `api.ts` deletes its declarations of the moved types, and re-exports the
   generated ones under today's names. `BudgetStatus` is `BudgetReport`, and
   `BudgetAgent` is `BudgetAgentSpend`.
   - `getCommitDraft` reads the generated `CommitDraft`.
   - `AccessStateWire` in `interaction.ts` becomes the generated `AccessState`,
     re-exported under its current name. The parsed `AccessState` there keeps
     its name.
3. `fleet.ts` declares `FleetDelta`, `PendingDelta`, `RunDelta`, `BudgetDelta`
   and `ProjectRemovedDelta` from the generated `FleetDelta`, `BudgetDelta` and
   `ProjectRemovedDelta`:
   - each picks its fields from the generated type;
   - each fixes `kind` to its one value;
   - each narrows only a field the web UI types more tightly than the wire
     does: `run` as a `RunDetail`, `access` as `AccessStateWire`.

   `FleetEvent` keeps its members. The out-of-loop deltas stay hand-written.
4. Fix what the check reports from the stricter generated types:
   - a `PendingRequest`'s `params` is `unknown`, so narrow it at each read, as
     `describePending` already does;
   - a `GateRecord`'s `result` may be `null`;
   - an `AgentWorkspace`'s `version` is optional.

   Fix each at the reading site. Never loosen a generated type, and never use
   `as any`.
5. `npm run check` reports at most 6 errors, every one in the base's six files.
   `npm test` passes.
6. `npm run build`. If `web/bridge/static/` changed, commit the change: removed
   files as removals, and new files by path.

**Verify:**
- In `web/ui`: `npm run check`, `npm test`, and `npm run build`
- `go build ./cmd/webbridge`
- Mutation check: change `contract.AgentStatus`'s `prUrl` tag, regenerate with
  `-update`, and run `npm run check`. It must report the readers of `prUrl`.
  Restore the tag and regenerate.

## Task 7 — the docs, the record and full verification

**Files:**
- `AGENTS.md` and `docs/ARCHITECTURE.md`;
- `cmd/webbridge/README.md`, if it describes the HTTP contract's types;
- `docs/daemon/p5-fleet-screen.md`: the "Plan 1" placeholder only.

1. `AGENTS.md`:
   - Under "Web control plane": `web/bridge/contract` holds the fleet loop's
     wire types. `web/ui/src/lib/contract.ts` is generated from it with the
     `-update` command, and `npm run check` type-checks the web UI.
   - In the "`web/` is standard library only" constraint: a package under
     `web/` may import another one under `web/`. "Extend the contract" now
     names `web/bridge/contract`.
2. `docs/ARCHITECTURE.md`: in the web bridge row, one sentence on the
   contract package and the generated types.
3. Fill the "Plan 1" placeholder in the spec's Implementation record:
   - what was built;
   - where the plan was short, and how that was filled;
   - which answers W1 could not record;
   - the type check's count;
   - the verification, with each command's result.
4. Full verification, with each result recorded:
   - `gofmt -l internal cmd test web` prints nothing;
   - `go vet ./...` is clean, and so are `GOOS=linux go vet ./web/...` and
     `GOOS=windows go vet ./web/bridge/...`;
   - `go test ./... -timeout 20m`, with every failure named and compared with
     the base's known failures;
   - in `web/ui`: `npm run check`, `npm test` and `npm run build`;
   - `CGO_ENABLED=1 go build -o marshal ./cmd/marshal`, then
     `go test ./test/usability/... -run TestScripted -timeout 5m`.

**Verify:** the commands above, each recorded with its result.

## Done when

- `web/bridge/contract` holds decision 1's types.
- The bridge and `internal/daemon` use them through aliases.
- `TestTheWireIsUnchanged` passes against the recordings made at the base.
- `contract.ts` is generated, and `TestTypeScriptContractIsCurrent` passes.
- The web UI uses the generated types, `npm run check` reports no new error,
  and `npm test` passes.
- `TestWebIsStdlibOnly` passes, allowing `web/`'s own packages and nothing
  else.
- The record is filled.
