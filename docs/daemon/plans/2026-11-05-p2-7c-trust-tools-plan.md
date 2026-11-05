# P2 plan 7c: Trust and tools — implementation plan

Date: 2026-11-05. Status: ready for review.
Base: `main` at `d9db66e7` (plan 7b merged). Every anchor below was checked there.
- **This plan runs in parallel with plan 7d** (decided with the user, 2026-11-05). The two touch different TUI files. In the files both edit, each inserts at its own anchor, named in its steps:
  - `acpclient/acpclient.go` and `acpclient/methods.go`: this plan inserts after `NewSession` and after `Changed`; 7d changes only `SaveUserProviders` and two doc comments;
  - `inproc/inproc.go`'s `Deps`: this plan adds after `NewSession`; 7d adds nothing;
  - `docs/daemon/p2-client-seam.md`: both entries in its revision record are already written. Only the Implementation record can conflict, and the second plan to merge keeps both entries.

Approved design: [p2-client-seam.md](../p2-client-seam.md): C10 (trust and tools), Migration order item 7c, its revision record entry "2026-11-05, against `d9db66e7` (plan 7b merged)", and tests T1, T4 and T5.
Previous plan: [plan 7b, config](2026-11-05-p2-7b-config-plan.md).
Authoring workflow: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md).

**How this plan is written.** No prototype was built, and the plan embeds no implementation code. The Contracts section holds declarations and the exact strings the tests pin. Every other step states its decision in prose. Executing the plan also tests how well Marshal follows a plan. "As `X` does" means `X` exists and is the model to copy.

**Goal:** the TUI asks the folder-trust question and looks up tool names through the seam.
- `acpwire` gains `TrustDecision`.
- `acpclient.Host` gains `TrustPrompt` and `DecideTrust`, and `acpclient.Session` gains `Tools`.
- The trust panel stops importing `internal/trust`, and the agents roster stops holding the tool registry.
- `WithTrustPrompt`, `WithToolRegistry`, `NewRosterPanelWithRegistry` and `LegacySession.ToolRegistry` are deleted.

**Non-goals:**
- **No skill-gate settings.** They move with the rest of the skills panel in plan 8 (decided with the user, 2026-11-05).
- **No change to the trust flow itself.** The runtime still decides whether to ask, records the answer and restarts after a trust, exactly as `Run` does today (`app.go:2649`, `:2886`).
- **No change to what users see**, apart from decision 5's fix.
- **No ACP behaviour change.** No changelog entry.

**Assumptions:**
- Every Go test command below runs with `TMPDIR=/private/tmp/p2-7c-suite`. Create the directory first: `mkdir -p /private/tmp/p2-7c-suite`. Keep the path short: a long one breaks the unix-socket tests in `internal/acp` and `web/bridge`. Plan 7d uses `/private/tmp/p2-7d-suite`. Never share a directory between the two runs.
- **The base.** Plans 7b and P3 4a each ran the full suite before merging, and `d9db66e7` is the merge of both. This plan was written without rerunning it. `web/bridge`'s `TestRecipeRunMaxUSDSetsAPausingCapForTheRun` has hung about one run in three since `90488446`. The hang is in its fake agent (`fakeagent_test.go`), not in product code, so treat a hang there as known.
- **Two suites may run at once.** Load makes the flakes listed under Task 4 likelier. Rerun a failure alone before treating it as new.
- **The trust prompt today:**
  - `Run` passes `WithTrustPrompt(workingDir, trustDecide)` only when the runtime started with a decision pending (`app.go:2801`, `rt.TrustPromptPending`).
  - `trustDecide` (`app.go:2649`) records a permanent trust in the trust store, or the session trust in the run options. For either, it sets `reloadForTrust`, which restarts the runtime with the project config once the program ends (`:2886` to `:2919`). Declining does nothing.
  - The model opens the trust panel in `New` when the option is set (`model.go:1258`). `/trust` reopens it while the session is untrusted, and notes "No trust decision pending…" when no option was given (`commands_dispatch.go:298` to `314`).
  - The panel calls `decide` synchronously on a choice, then quits the program for a trust, or closes itself for a decline (`trustpanel/trustpanel.go:57`).
  - `TestRunReloadsAfterInlineTrust` (`app_test.go:3051`) drives the whole path through the model's `Update`.
- **`trust.Decision`'s values** are the strings `trust_permanent`, `trust_session` and `dont_trust` (`trust/trust.go:15` to `20`).
- **The tool lookup today:**
  - The agents roster validates a custom agent's tool denylist with `reg.Lookup(name)`. Without a registry, every name fails with `unknown tool: %q (no registry available)` (`agents/panel.go:323`).
  - `Run` passes `WithToolRegistry` only when the provider was built at startup (`app.go:2798`).
  - After `/new`, the model re-reads the runtime's registry through `LegacySession` (`commands_dispatch.go:201`). A reload that builds the runner later does not reach the model, so the roster keeps saying "no registry available" until `/new`.
- **`Update` has a value receiver** (`model.go:1459`). A closure that runs later captures the host, the session and the context values, never `m`.

**Decisions this plan makes beyond the spec** (all are recorded in the spec's revision record):

1. **Skill-gate settings go to plan 8** (decided with the user, 2026-11-05). They live in the skills panel, which plan 8 moves as a whole, so 7c and plan 8 stay disjoint.
2. **The pending prompt is a host read.** `Host.TrustPrompt` returns the directory whose trust decision is pending, or "" when none is. It does not change while the host runs. The option was set once at startup, and declining left `/trust` able to ask again.
3. **`DecideTrust` checks what it answers.** It refuses when no prompt is pending, when the directory is not the pending one, and for an unknown decision. Otherwise it converts the decision and calls the runtime's `trustDecide`, unchanged.
4. **The panel keeps its shape.** It takes a `func(acpwire.TrustDecision)`, which the model builds over `DecideTrust`. It logs a refusal, which only a broken wiring can cause, and quits or closes as today.
5. **`Tools` reads the runtime's current registry.**
   - It returns the registered tool names, sorted, and reports false while the runtime has no runner or no registry, as a start without a provider has today.
   - Because it reads the live runtime, a reload that builds the runner makes the roster's validation work at once, instead of after `/new`. This is the one visible difference, and it is a fix.
6. **The allowlist loses 4 entries across 3 files, and two keys go entirely:**
   - `trustpanel/trustpanel.go` loses `marshal/internal/trust`, its only entry;
   - `agents/panel.go` loses `marshal/internal/tools/registry`, its only entry;
   - `model.go` loses `marshal/internal/trust` and `marshal/internal/tools/registry`.

   T1b gains no names.
7. **The new ACP methods are named** `trust/prompt` (`TrustPrompt`), `trust/decide` (`DecideTrust`) and `session/tools` (`Tools`). None is registered on `acp.Server` today.

**Execution:**
- Branch first: `git switch -c p2-7c-trust-tools` from `main`.
- If `git status` shows `docs/daemon/p2-client-seam.md` or this plan uncommitted, commit them first as explicit paths, with the message `p2-7c-trust-tools: record the plan 7c and 7d revisions and plan 7c`. If plan 7d's run committed the spec already, commit only this plan. Plan 7d's plan file may also be uncommitted: leave it to plan 7d. The main checkout holds other untracked files (`scripts/lmstudio-bench/` and others). Leave them alone.
- Run the tasks in order. Run each task's Verify and commit it as `p2-7c-trust-tools: task N — <title>`.
- Stage explicit paths only. Never `git add -A` without a path.
- Run `gofmt -w` on every Go file a task touches before its Verify.

## Contracts

### Wire type (`internal/acpwire/trust.go`, new)

```go
// TrustDecision answers the folder-trust prompt (C10, plan 7c).
type TrustDecision string

const (
	TrustPermanent TrustDecision = "trust_permanent"
	TrustSession   TrustDecision = "trust_session"
	TrustDecline   TrustDecision = "dont_trust"
)
```

The values are `trust.Decision`'s, so the adapter converts by value.

### Interface (`internal/acpclient`)

`Host` gains, directly after `NewSession`:

```go
	// TrustPrompt is the directory whose project config waits for a trust
	// decision, or "" when none does. It does not change while the host
	// runs.
	//
	// ACP: trust/prompt (new in P3).
	TrustPrompt() string

	// DecideTrust answers the trust prompt for dir. After a trust the client
	// ends its program, and the app restarts with the project config in
	// force; after a decline the session carries on.
	//
	// ACP: trust/decide (new in P3).
	DecideTrust(ctx context.Context, dir string, decision acpwire.TrustDecision) error
```

`Session` gains, directly after `Changed`:

```go
	// Tools names every tool the session's registry holds, sorted. It reports
	// false while the session has no registry: its provider is not built.
	//
	// ACP: session/tools (new in P3).
	Tools() ([]string, bool)
```

`Methods` gains three rows: `TrustPrompt` and `DecideTrust` directly after the `Host`/`NewSession` row, and `Tools` directly after the `Session`/`Changed` row.

| Iface | Name | ACP | Exists |
|---|---|---|---|
| Host | TrustPrompt | trust/prompt | false |
| Host | DecideTrust | trust/decide | false |
| Session | Tools | session/tools | false |

### Adapter (`internal/acpclient/inproc`)

`Deps` gains, directly after `NewSession`:

```go
	// TrustPrompt is the directory whose trust decision is pending, "" when
	// none is.
	TrustPrompt string
	// DecideTrust records the user's trust decision, as Run's trustDecide
	// does. Without it, DecideTrust fails.
	DecideTrust func(trust.Decision)
	// Tools reads the runtime's current tool names; false while the runtime
	// has no runner or no registry. Nil means the session has no tools.
	Tools func() ([]string, bool)
```

A new file, `trust.go`, holds `TrustPrompt`, `DecideTrust` and `Tools`. `Session` keeps `Deps.Tools` as a field, set in `newSession` (`events.go:20`), as the other `Deps` values are.

### App (`internal/app`)

- `(*Runtime).ToolNames() ([]string, bool)`, in `turns.go` beside `TurnRunner` (`:23`). It reads under `rt.mu`, as `TurnRunner` does, and reports false when `rt.Runner` or `rt.ToolRegistry` is nil. Otherwise it returns the names of `rt.ToolRegistry.List()`, which is sorted.

### TUI

- `trustpanel.New(workingDir string, decide func(acpwire.TrustDecision)) *Panel`.
- `agents.NewRosterPanel(cfg config.Config, sess acpclient.Session, arg string, dispatch DispatchFn) *Panel`. `NewRosterPanelWithRegistry` is deleted.
- `(*Model).openTrustPanel() bool`: opens the trust panel for `m.host.TrustPrompt()` and reports true, or does nothing and reports false when that is "".
- **Deleted:** `WithTrustPrompt` and the `trustPromptDir` and `trustDecide` fields; `WithToolRegistry` and the `toolRegistry` field; `LegacySession.ToolRegistry`; the roster's `reg` field.

### Pinned strings

- `DecideTrust` with nothing pending: `inproc: no trust decision is pending`.
- `DecideTrust` for another directory: `inproc: no trust decision is pending for <dir>`.
- `DecideTrust` with an unknown decision: `inproc: unknown trust decision "<decision>"`.
- The roster's validation keeps both of today's messages: `unknown tool: "<name>" (no registry available)` and `unknown tool: "<name>"`.

## Task 1: The seam carries trust and tools

**Goal:** the wire type, the three methods and the adapter exist, and `Run` wires them. The TUI uses none of them yet.

**Files:**
- `internal/acpwire/trust.go` (new);
- `internal/acpclient/acpclient.go`, `internal/acpclient/methods.go`;
- `internal/acpclient/inproc/trust.go` (new), `internal/acpclient/inproc/trust_test.go` (new), `internal/acpclient/inproc/inproc.go`, `internal/acpclient/inproc/events.go`;
- `internal/app/turns.go`, `internal/app/app.go`, `internal/app/app_test.go`.

**Steps:**

1. **Declarations.** Add the wire type, the three interface methods with their doc comments, and their `Methods` rows, at the anchors Contracts names.
2. **`Deps`.** Add the three fields after `NewSession`. `newSession` copies `Tools` onto the session.
3. **`TrustPrompt`** returns `Deps.TrustPrompt`, read under the host's mutex.
4. **`DecideTrust`.**
   - With `TrustPrompt` empty or `DecideTrust` nil, return the "nothing pending" error.
   - With `dir` not equal to `TrustPrompt`, return the "another directory" error.
   - A decision other than the three constants is the "unknown decision" error.
   - Otherwise call `Deps.DecideTrust` with `trust.Decision` of the same value, and return nil.
5. **`Tools`** returns false without `Deps.Tools`. Otherwise it returns its result, with the names copied so that a caller cannot change the runtime's. A closed session keeps answering, as `Status` does.
6. **`ToolNames`** on the runtime, as Contracts says.
7. **`Run`** (`app.go:2735`):
   - `Deps.TrustPrompt` is `workingDir` when `rt.TrustPromptPending`, otherwise "";
   - `Deps.DecideTrust` is `trustDecide`;
   - `Deps.Tools` is `rt.ToolNames`.

   The TUI options stay wired until Tasks 2 and 3 delete them.
8. **Adapter tests** (`trust_test.go`):
   - `TestDecideTrustPassesTheDecision`: each of the three decisions reaches `Deps.DecideTrust` as the matching `trust.Decision`.
   - `TestDecideTrustRefusesWhatIsNotPending`: no prompt, another directory, an unknown decision and a nil `DecideTrust` each give their pinned error, and nothing is called.
   - `TestTrustPromptIsTheDeps`: `TrustPrompt` returns `Deps.TrustPrompt`, and "" without one.
   - `TestToolsReadsTheRuntime`: without `Deps.Tools` it reports false. With it, it returns its names, and changing the returned slice does not change the next read.
9. **`app` test.** `TestRunWiresToolNames`, built like `TestRunPassesAuthResolverToTheSeam` (`app_test.go:6168`), with no provider configured. Inside the program runner:
   - `model.(tui.Model).Session().Tools()` reports false;
   - the host's `TrustPrompt()` is "" for a directory with no project config.

   `TestRunReloadsAfterInlineTrust` covers `DecideTrust`'s wiring once Task 2 routes the panel through it.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go vet ./internal/acpwire/ ./internal/acpclient/... ./internal/app/
TMPDIR=/private/tmp/p2-7c-suite go test ./internal/acpwire/ ./internal/acpclient/... -count=1
TMPDIR=/private/tmp/p2-7c-suite go test ./internal/acp/ -run TestACPClientMethodTableMatchesTheServer -count=1
TMPDIR=/private/tmp/p2-7c-suite go test ./internal/app/ -run 'TestRunWiresToolNames|TestRunReloadsAfterInlineTrust' -count=1
TMPDIR=/private/tmp/p2-7c-suite go test ./internal/app/tui/... -count=1
```

Expected: all pass. `TestWireFieldsFollowTheRules`, `TestMethodTableCoversTheInterfaces` and `TestMethodDocsNameTheirACPMethod` pass with the new declarations.

## Task 2: Trust goes through the seam

**Goal:** the trust panel and `/trust` reach the runtime only through `DecideTrust`.

**Files:**
- `internal/app/tui/trustpanel/trustpanel.go`, `internal/app/tui/trustpanel/trustpanel_test.go`;
- `internal/app/tui/model.go`, `internal/app/tui/commands_dispatch.go`;
- `internal/app/tui/model_test.go`, `internal/app/tui/frame_invariants_test.go`;
- `internal/app/tui/boundary_allowlist_test.go`;
- `internal/app/app.go`.

**Steps:**

1. **The panel.** `decisions` holds the three `acpwire` constants, and `decide` takes an `acpwire.TrustDecision`. The keys, labels, view, and the quit or close after a choice are unchanged. The `internal/trust` import goes.
2. **`openTrustPanel`.**
   - It reads `m.host.TrustPrompt()`, and returns false when that is "".
   - Otherwise it opens `trustpanel.New(dir, decide)` and returns true. `decide` captures the host, the model's context, the directory and the logger, calls `DecideTrust`, and logs a warning with the error when it refuses.
3. **The model.**
   - `New` (`model.go:1258`) calls `openTrustPanel` in place of its check.
   - `/trust` (`commands_dispatch.go:298`) keeps its trusted check first. It then calls `openTrustPanel`, and notes today's "No trust decision pending…" sentence when that reports false. The panel shows the prompt's directory, which `Run` sets to the working directory `/trust` used.
4. **Deletions.** Delete `WithTrustPrompt` and the two fields. `Run` stops passing the option. `model.go`'s `internal/trust` import goes.
5. **Tests.**
   - A test that passed `WithTrustPrompt(dir, fn)` (`model_test.go:8296`, `:8381`) passes `inproc.Deps{TrustPrompt: dir, DecideTrust: fn}` through `testModelWith`. `fn` keeps its `trust.Decision` parameter: the test files may import `internal/trust`.
   - `frame_invariants_test.go:165` opens the panel through `openTrustPanel`, over a model whose `Deps` name a prompt.
   - `trustpanel_test.go` passes `func(acpwire.TrustDecision)` recorders, and compares with the `acpwire` constants.
6. **Allowlist.** `trustpanel/trustpanel.go`'s key goes, and `model.go` loses `marshal/internal/trust`.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go vet ./internal/app/...
TMPDIR=/private/tmp/p2-7c-suite go test ./internal/app/tui/... -count=1
TMPDIR=/private/tmp/p2-7c-suite go test ./internal/app/ -count=1
```

Expected: all pass, `TestTUIStaysOnTheClientSeam` and `TestRunReloadsAfterInlineTrust` included. Mutation check: drop `Deps.DecideTrust` from `Run`, and `TestRunReloadsAfterInlineTrust` fails, because the program runs only once. Restore it.

## Task 3: Tools go through the seam

**Goal:** the agents roster validates tool names through `Session.Tools`, and the TUI holds no tool registry.

**Files:**
- `internal/app/tui/agents/panel.go`, `internal/app/tui/agents/panel_test.go`;
- `internal/app/tui/model.go`, `internal/app/tui/commands_dispatch.go`;
- `internal/app/tui/model_test.go`, `internal/app/tui/commands_dispatch_test.go`, and any other test that passes `WithToolRegistry`;
- `internal/app/tui/boundary_allowlist_test.go`;
- `internal/app/app.go`.

**Steps:**

1. **The roster.**
   - Delete `NewRosterPanelWithRegistry` and the `reg` field. `NewRosterPanel` is now the only constructor.
   - `ValidateToolDenylist` (`agents/panel.go:323`) reads `p.sess.Tools()` once per call. A nil session, or a false result, gives the "no registry available" message for the first name. Otherwise a name not among the tools gives the plain message. An empty list validates, as today.
   - The `internal/tools/registry` import goes.
2. **The model.**
   - `openAgentsRoster` (`model.go:1026`) calls `NewRosterPanel` without the registry.
   - Delete `WithToolRegistry`, the `toolRegistry` field and `LegacySession.ToolRegistry`.
   - `/new`'s re-read (`commands_dispatch.go:201`) stops copying a registry.
   - `model.go`'s `internal/tools/registry` import goes. Check with the compiler that nothing else used it.
3. **`Run`** stops passing `WithToolRegistry` (`app.go:2798`), and builds `LegacySession` with the state only (`:2813`).
4. **Tests.**
   - A test that passed a registry, to the roster or through `WithToolRegistry`, gives the adapter `Deps.Tools` returning that registry's names and true instead. A roster built directly gets a session from an `inproc.New` with those `Deps`.
   - Add `TestRosterValidationFollowsTheSessionsTools`: with `Tools` reporting false, a name fails with the "no registry available" message. With `Tools` naming `file.read`, `file.read` passes and `nope.tool` fails with the plain message.
5. **Allowlist.** `agents/panel.go`'s key goes, and `model.go` loses `marshal/internal/tools/registry`.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
grep -rn 'tools/registry\|WithToolRegistry\|toolRegistry\|NewRosterPanelWithRegistry' internal/app/tui --include='*.go' | grep -v '_test.go'
go vet ./internal/app/...
TMPDIR=/private/tmp/p2-7c-suite go test ./internal/app/tui/... -count=1
TMPDIR=/private/tmp/p2-7c-suite go test ./internal/app/ -count=1
```

Expected:
- the grep prints nothing;
- the packages pass, `TestTUIStaysOnTheClientSeam` included.

Mutation check: make `ValidateToolDenylist` accept every name when `Tools` reports false, and `TestRosterValidationFollowsTheSessionsTools` fails. Restore it.

## Task 4: Verification and docs

**Goal:** the full suite is green, and the spec records plan 7c.

**Files:** `docs/daemon/p2-client-seam.md`.

**Steps:**

1. **`p2-client-seam.md`.** Add a plan 7c entry to the Implementation record, after plan 7b's. If plan 7d's entry is already there, add 7c's before it, so the record stays in plan order. It lists:
   - the wire type;
   - the three methods and their `Methods` rows, all on new ACP methods;
   - the three `Deps` fields and `Runtime.ToolNames`;
   - that the trust panel and `/trust` answer through `DecideTrust`, and the roster validates through `Tools`;
   - the fix in decision 5;
   - the options, fields and constructor deleted;
   - the allowlist's 4 entries across 3 files and two keys entirely, and no T1b names.
2. **Main may have moved.** If plan 7d merged while this branch ran, merge `main` into the branch before the full verification. Expect conflicts only in the files this plan's header names. Keep both sides: each plan's methods, rows and record entries.
3. **Full verification.**

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
gofmt -l ./internal ./cmd ./web
go vet ./...
TMPDIR=/private/tmp/p2-7c-suite CGO_ENABLED=1 go build ./cmd/marshal
TMPDIR=/private/tmp/p2-7c-suite go test ./... -timeout 20m 2>&1 | grep -E '^(--- FAIL|FAIL|ok)' | grep -v '^ok'
git diff --stat main..HEAD -- internal/app/tui/testdata internal/app/tui/livebox/testdata
```

Expected:
- `gofmt -l` prints nothing, and `go vet` is clean;
- the binary builds;
- the test line prints nothing, or only `web/bridge`'s known hang (Assumptions). Treat any other failure as new unless it also fails at `d9db66e7`, or it is one of the load flakes below and passes on its own with `-count=3`;
- the `git diff` prints nothing.

**Baseline.** Earlier full runs saw these fail under load and pass alone:
- `TestRuntimeAccessSerializesWithReloadAndQuiesce` and `TestRuntimeCloseRunsNoGitGC` (`internal/app`);
- `TestRunManagedCancellationKillsTree` and `TestIntegrationOwnershipReleaseDrainsWritersBeforeUnlock` (`internal/snapshot`);
- `TestACPChildApprovalUsesOwningSessionRegistry` and `TestACPSendNowCancelsPromptRPCWithoutCancelingTurn` (`internal/acp`).

Finally, check by eye with `./marshal`:
- in a scratch git repository that has a `.marshal/config.toml` and has never been trusted, the trust question opens at start;
- "Trust this session only" restarts with the project config in force;
- declining, then `/trust`, asks again;
- in `/agents`, setting a custom agent's tool denylist to a real tool name saves, and a made-up name shows "unknown tool".

## Integration notes

- **Plan 7d** runs alongside. See the header for the shared files.
- **Plan 8** moves the skills panel, the skill-gate settings with it, and the remaining project data.
- **P3:**
  - `trust/prompt`, `trust/decide` and `session/tools` need handlers;
  - `launch` owns the startup trust prompt (R1). The host's `TrustPrompt` is how it learns one is pending.

## Self-review

- **Every task verifiable alone.** Task 1 adds the seam and its wiring, which nothing uses. Task 2 moves trust, and the inline-trust test proves the path end to end. Task 3 moves the tool lookup.
- **Cold executor.** Each step names the function, what it calls after the change, and what stays.
- **Contracts.** The wire type, the methods and their rows, `Deps`' fields, the runtime method, the TUI's changed signatures and every error string are declared there.
- **Anchors.** Every path, symbol and line was checked at `d9db66e7`.
- **Spec.** C10 and the Migration order are updated, with the revision record "2026-11-05, against `d9db66e7` (plan 7b merged)".
- **No placeholders.**
