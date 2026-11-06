# P2 plan 9: Close-out — implementation plan

Date: 2026-11-06. Status: ready for review.
Base: `main` once both `p2-8a-history` and `p2-8b-browsers` have merged. Written at `133f005b` while both ran.
- **Anchors are symbol names, not line numbers.** Plans 8a and 8b move lines in `model.go`, `commands_dispatch.go` and `app.go`, so the plan names the function, option or field each step changes. Where it gives a line, the line is `133f005b`'s and only a hint.
- **The Execution section's first step checks the base.** If 8a or 8b left anything this plan assumes gone, the plan stops there.
- **This plan runs in parallel with P3 plan 3a** ([ordering](2026-11-06-p3-3a-ordering-plan.md)). Plan 3a touches `internal/acp` and `p3-acp-transport.md`: its header's `Depends on:` and `Status:` clauses, R2, Migration order item 3 and the Implementation record. This plan touches no file in `internal/acp`. In the P3 spec it edits only the last sentence of the `Depends on:` paragraph, "Where the code is today" and a new section.

Approved design: [p2-client-seam.md](../p2-client-seam.md): Migration order item 9, Done when, and tests T1 and T1b. [design.md](../design.md) §4.5, §7 and §8. [p3-acp-transport.md](../p3-acp-transport.md)'s header, which waits for the list of methods this plan writes.
Previous plans: [8a, history](2026-11-06-p2-8a-history-plan.md) and [8b, browsers](2026-11-06-p2-8b-browsers-plan.md).
Authoring workflow: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md).

**How this plan is written.** No prototype was built, and the plan embeds no implementation code. The Contracts section holds declarations and the exact strings the tests pin. Every other step states its decision in prose. Executing the plan also tests how well Marshal follows a plan. "As `X` does" means `X` exists and is the model to copy.

**Goal:** P2 is done. The TUI holds no session state, the boundary test runs with no allowlist, and the methods P3 must add are listed.
- `WithLegacyState`, `WithLegacySession`, `LegacySession` and `WithMemoryStore` go, with the model's `state`, `legacySession`, `memoryDB` and `memoryProject` fields.
- TUI tests reach a model's state through its session, in one helper P3 can change.
- T1 has no allowlist, and T1b is deleted.
- The P3 spec lists every method marked "new in P3", grouped by the P3 plan that adds it. Design §4.5, §7 and §8, `AGENTS.md` and `docs/ARCHITECTURE.md` describe P2 as done.

**Non-goals:**
- **No behaviour change.** No interface method, ACP method or wire type is added or changed.
- **`inproc` stays.** P3 deletes it.
- **Test assertions do not change.** Only how a test reaches the state changes.
- **The R6 inventory stays with P3's layering plan.** This plan lists methods. It does not classify calls as synchronous or asynchronous.
- **No manual run.** The spec's Done-when checklist is the user's. The last task lists it in the report.
- No changelog entry: nothing a user sees changes.

**Assumptions:**
- Every Go test command below runs with `TMPDIR=/private/tmp/p2-9-suite`. Create it first: `mkdir -p /private/tmp/p2-9-suite`. Keep the path short: a long one breaks the unix-socket tests in `internal/acp` and `web/bridge`. P3 plan 3a uses `/private/tmp/p3-3a-suite`. Never share a directory between the two runs.
- **What 8a and 8b leave** (from their plans' allowlist and T1b steps):
  - `boundaryAllowlist` holds one key, `model.go`, with exactly `marshal/internal/app/session` and `marshal/internal/db`.
  - `deferredStateReads` is empty. T1b's `movedStateReads` names every state read the TUI made.
  - No non-test TUI file has a `<x>.state.<Name>` selector on the model's state.
  - The model's remaining uses of its `state` field are its declaration, `WithLegacyState`, `New`'s check and its `state := m.state` local, `reconcileAccess`'s nil guard, and the legacy re-read in `startNewSession` (`commands_dispatch.go`).
  - `memoryDB` and `memoryProject` are read nowhere but `WithMemoryStore`.
- **The base's suite.** Whichever of 8a and 8b merged second ran the full suite after merging `main` (their Task 7). That run is this plan's baseline. Do not rerun it before Task 1. The last recorded result, at `6c6d16b2`, was 117 packages ok, with two P3 4a budget tests (`TestStackPatchApplyBudget`, `TestStackRoundTripBudget`) failing under full-suite load and passing alone.
- **Two suites may run at once** if plan 3a runs alongside. Load makes the flakes under Task 4 likelier. Rerun a failure alone before treating it as new.
- **Tests read the model's state directly, a lot.** At `133f005b`, about 1,160 selectors in about 90 test files of `internal/app/tui` read `m.state`, `mm.state` or `m2.state`. 8a and 8b change a few of them. Other receivers named `state` in tests (`f.state`, `p.state`, `g.state`, `r.state`, `h.state`) belong to panels and fakes, not to the model.
- **Every test model is built over the in-process adapter.** The helpers (`testModel`, `testModelWith`, and the seam tests' own) and the few direct `New` calls all pass a session from `inproc.New`. So the model's session always knows the state the test built.
- **A session's state never changes.** `inproc`'s `newSession` (`events.go:20`) sets it once, and `Host.NewSession` returns a new session rather than swapping one (`ending.go:130`). The session's state is therefore exactly what `m.state` tracked through `WithLegacySession`.
- **The TUI's production code cannot import `inproc`.** T1 already denies it (`boundary_test.go`, in `TestSeamImportAllowedClassifies`'s denied list). So an accessor on `inproc.Session` is reachable only from tests and from `app`.
- **The P2 spec cannot be edited in the main checkout before 8a and 8b merge.** Both plans' Task 7 edits `p2-client-seam.md`, and an uncommitted edit there makes their merges refuse. So this plan's spec text sits under "Spec edits" below, and the Execution section applies it.

**Decisions this plan makes beyond the spec** (Execution records them in the spec):

1. **Tests reach the state through the session.** `inproc.Session` gains an exported `State()`, and the TUI tests gain one helper, `stateOf`, that returns the state behind a model's session. Every test read of the model's state goes through it. P3's switch then changes one helper, as it changes the model helpers: its test agent keeps the state, and `stateOf` returns it.
2. **The allowlist goes entirely.** The spec says plan 9 empties it. This plan deletes the map and its file instead, and T1 fails on any crossing. With no map, the rule "never add an entry" is structural: there is nothing to add to.
3. **T1b goes with its file,** as the spec says. Its job was to show a group had moved while files still mixed groups. With no state on the model, there is nothing left to read.
4. **The P3 list lives in the P3 spec,** as a new section after "In scope". One row per distinct ACP name marked new, with its interface methods and the P3 plan that adds it. It is built from `acpclient.Methods` and the interfaces' `ACP:` doc lines, which T4 and `TestMethodDocsNameTheirACPMethod` keep honest, not typed from memory.
5. **Design §4.5 describes what exists.** Its migration sketch (six groups, "82 `session.State` calls") is replaced by the seam as built, with a pointer to the spec's nine plans.

**Execution:**
1. **Check the base.** From the main checkout, after both merges:
   - `git log --oneline -3` shows the second of the two merges at the top.
   - `sed -n '/^var boundaryAllowlist/,/^}/p' internal/app/tui/boundary_allowlist_test.go` shows one entry: `"model.go": {"marshal/internal/app/session", "marshal/internal/db"},`.
   - `grep -nE '\.state\.[A-Z]' internal/app/tui/*.go internal/app/tui/*/*.go | grep -v _test.go` prints nothing.

   If any of these differ, stop and report what 8a or 8b left.
2. **Branch:** `git switch -c p2-9-close-out` from `main`.
3. **Commit the plan and its spec edits.** Apply the two edits under "Spec edits" to `docs/daemon/p2-client-seam.md`. If `git status` shows this plan untracked, commit it with the spec as explicit paths, with the message `p2-9-close-out: record plan 9`. The main checkout holds other untracked files (`scripts/lmstudio-bench/` and others), and possibly P3 plan 3a's uncommitted edits to `p3-acp-transport.md`. Leave them alone.
4. Run the tasks in order. Run each task's Verify and commit it as `p2-9-close-out: task N — <title>`.
5. Stage explicit paths only. Never `git add -A` without a path.
6. Run `gofmt -w` on every Go file a task touches before its Verify.

## Spec edits

Applied by Execution step 3, to `docs/daemon/p2-client-seam.md`.

1. **Migration order, item 9.** Replace its text with: "**Close-out** ([plan](plans/2026-11-06-p2-9-close-out-plan.md)). Delete `WithLegacyState`, `WithLegacySession` and `WithMemoryStore`, delete the allowlist and T1b, route the tests' state reads through the session, write the list of methods P3 must add, and update design §4.5, §7 and §8."
2. **Revision record.** Add this entry after the last entry, before `## Implementation record`:

```markdown
### 2026-11-06, against `133f005b` and plans 8a and 8b (plan 9)

Plan 9 was written while 8a and 8b ran, against what their plans leave.

- **Tests reach the state through the session** (T1, T8). About 1,160
  test reads of the model's state go through one helper, `stateOf`, over
  a new `inproc.Session.State()`. A session's state never changes (a new
  session is a new value), so the helper returns exactly what the model's
  field tracked. P3's switch changes only the helper.
- **The allowlist is deleted, not emptied** (T1). T1 fails on any
  crossing, and there is no map to add an entry to.
- **T1b is deleted** with `WithLegacyState`, as T1b says.
- **The P3 list** is a section of the P3 spec, one row per ACP name
  marked new, grouped by the P3 plan that adds it.
- **Design §4.5** now describes the seam as built, and points here for
  the migration.
```

## Contracts

### Adapter (`internal/acpclient/inproc/inproc.go`)

Added directly after `(*Session).ID`:

```go
// State returns the session state this session serves. Tests use it to
// reach the state behind a model, and the TUI cannot: T1 denies it inproc.
func (s *Session) State() *session.State
```

### TUI tests (`internal/app/tui/seam_test.go`)

Added directly after `testSession`:

```go
// stateOf returns the state behind a model's session: the one place TUI
// tests reach agent-side state, so P3 changes only this helper.
func stateOf(m interface{ Session() acpclient.Session }) *session.State
```

It type-asserts the session to `*inproc.Session`, and fails the conversion loudly (a panic with the session's type) for any other session, because every test model is built over the adapter.

### Pinned strings

- T1's failure, per crossing: `%s uses %s, which is agent side (P2 C1): move it behind acpclient`.
- `New` with a nil host or session still panics with `tui.New: nil client host or session`.

## Task 1: Tests reach the state through the session

**Goal:** no TUI test reads the model's `state` field. Every test read goes through `stateOf`, and the suite still passes with the field in place.

**Files:**
- `internal/acpclient/inproc/inproc.go`;
- `internal/app/tui/seam_test.go`;
- every test file directly under `internal/app/tui/` that reads `m.state`, `mm.state` or `m2.state`, except `moved_reads_test.go`, which Task 3 deletes.

**Steps:**

1. **The accessor.** Add `State` after `ID`, as the Contracts give it. It returns the field `ID` reads.
2. **The helper.** Add `stateOf` after `testSession`.
3. **The rewrite.** It is mechanical. Over the test files directly under `internal/app/tui/` (not its subpackages, whose `p.state` are panels), replace each whole-word `m.state`, `mm.state` and `m2.state` with `stateOf(m)`, `stateOf(mm)` and `stateOf(m2)`. A one-line `perl -pi` over those files does it. Exclude `moved_reads_test.go`.
   - A rewritten comment is fine.
   - If a site does not compile because its `m` is not a model, restore that site and read the state from the test's own variable instead.
4. **Leave `WithLegacyState` in place.** The field still exists, so the suite passes before Task 2 removes it.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
grep -rnE '\b(m|mm|m2)\.state\b' internal/app/tui --include='*_test.go' | grep -v moved_reads_test.go
go vet ./internal/acpclient/... ./internal/app/tui/
TMPDIR=/private/tmp/p2-9-suite go test ./internal/acpclient/... ./internal/app/tui/... -count=1
```

Expected:
- the grep prints nothing;
- every package passes.

Mutation check: make `State` return a fresh `session.New` state instead of the session's. A broad set of TUI tests fails (for example the approval and status tests). Restore it.

## Task 2: The TUI holds no session state

**Goal:** the model has no `state`, `legacySession`, `memoryDB` or `memoryProject` field, the four legacy options are gone, and `app.Run` passes none of them.

**Files:**
- `internal/app/tui/model.go`, `internal/app/tui/commands_dispatch.go`;
- `internal/app/app.go`;
- the TUI tests that pass a deleted option: `seam_test.go`, `answers_seam_test.go`, `controls_seam_test.go`, `ending_seam_test.go`, `turns_seam_test.go`, `newsession_seam_test.go`, `model_test.go`, and `history_test.go` if 8a left a `WithMemoryStore` there;
- `internal/app/tui_seam_test.go`, `internal/app/app_test.go`;
- `internal/app/tui/boundary_allowlist_test.go`.

**Steps:**

1. **`model.go`, by symbol.** Delete:
   - the `state` field and its comment;
   - the `legacySession` field and its comment;
   - the `memoryDB` and `memoryProject` fields;
   - `WithLegacyState` and its comment;
   - the `LegacySession` type, `WithLegacySession` and its comment;
   - `WithMemoryStore` and its comment.
2. **`New`.** Delete the `m.state == nil` check with its panic, and the `state := m.state` local. Nothing else in `New` may read that local: 8a moved the prompt-history load. If something still does, stop and report.
3. **`reconcileAccess`.** Delete its `m.state == nil` guard. `New` already refuses a nil session, and the function reads only the session.
4. **`startNewSession`** (`commands_dispatch.go`). Delete the block that reads `m.legacySession` and replaces `m.state`, with the comment above it ("The groups not yet behind the seam re-read…"). The model keeps `m.sess = sess` and the watch-resume reset.
5. **Imports.** `model.go` no longer imports `marshal/internal/app/session` or `marshal/internal/db`. If either is still needed for something else, stop and report: 8a or 8b left a use.
6. **`app.go`.** In `Run`'s TUI options:
   - `tuiOpts` starts empty. The `WithLegacyState` and `WithMemoryStore` lines go.
   - The `WithLegacySession` call goes, with its closure and the comment above it ("/new and /clear create a brand-new session…").
   - Everything else in the block stays as 8a and 8b left it.
7. **Tests.**
   - Every helper that prepends `WithLegacyState` to its options builds with the caller's options alone: `testModel` and `testModelWith` in `seam_test.go` (and the third helper there that does the same), and one helper each in `answers_seam_test.go`, `controls_seam_test.go`, `ending_seam_test.go` and `turns_seam_test.go`. Update `testModel`'s comment: it no longer hands the state in; `stateOf` reaches it.
   - `TestNewRequiresTheLegacyStateUntilPlan9` becomes `TestNewRequiresAHostAndSession`, keeping only the nil-host case and its pinned panic.
   - `TestOpenFileNeedsASharedFilesystem` and the direct `New` call in `model_test.go` drop the option.
   - `newsession_seam_test.go`: `testModelReplacing` drops both options and its comment's mention of the legacy read. `fakeNewSession.legacy` and its comment go.
   - `internal/app`: the two helpers in `tui_seam_test.go` and the two `tui.New` calls in `app_test.go` drop the option.
   - A test that still passes `WithMemoryStore` checks behaviour 8a or 8b moved behind the session. Drop the option. Where the test needs the database, it sets it on the state it builds, as 8a's and 8b's own tests do. If such a test only checked the option's wiring, delete it, and name it in the commit message.
8. **The allowlist.** Delete `model.go`'s key from `boundaryAllowlist`, leaving the map empty, so T1 stays green. Task 3 deletes the map.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
grep -rnE 'WithLegacyState|WithLegacySession|LegacySession|WithMemoryStore|memoryDB|memoryProject|legacySession' internal cmd --include='*.go'
go vet ./internal/app/... ./internal/acpclient/...
TMPDIR=/private/tmp/p2-9-suite go test ./internal/app/ ./internal/app/tui/... ./internal/acpclient/... -count=1
```

Expected:
- the grep prints only `internal/app/tui/moved_reads_test.go`'s comment, which Task 3 deletes;
- every package passes, `TestTUIStaysOnTheClientSeam` and `TestMovedStateReadsStayMoved` included.

## Task 3: The boundary closes

**Goal:** T1 has no allowlist and passes, and T1b is gone.

**Files:**
- `internal/app/tui/boundary_test.go`;
- `internal/app/tui/boundary_allowlist_test.go` (deleted);
- `internal/app/tui/moved_reads_test.go` (deleted).

**Steps:**

1. **Delete `boundary_allowlist_test.go`.**
2. **`TestTUIStaysOnTheClientSeam`.**
   - Every crossing `seamViolations` finds is an error, with the pinned message. The stale-entry loop goes, and so does the closing log of current violations.
   - Its comment says it fails on any TUI use of the agent side, and that there is no allowlist (P2 plan 9): the fix is always to move the call behind `acpclient`.
   - Delete `formatAllowlist` and `stringInSlice` if nothing else uses them. `sortedKeysOf` stays: the errors come out in file order.
3. **Delete `moved_reads_test.go`**, with `TestMovedStateReadsStayMoved`, both maps and its helpers. Nothing else uses them.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
grep -rn 'boundaryAllowlist\|movedStateReads\|deferredStateReads' internal --include='*.go'
go vet ./internal/app/tui/
TMPDIR=/private/tmp/p2-9-suite go test ./internal/app/tui/ -run 'Seam|Boundary|DeniedConfig' -count=1 -v 2>&1 | grep -E '^(=== RUN|--- |ok|FAIL)' | head -20
TMPDIR=/private/tmp/p2-9-suite go test ./internal/app/tui/... -count=1
```

Expected:
- the grep prints nothing;
- `TestTUIStaysOnTheClientSeam` and the classifier tests pass, and so does the whole tree.

Mutation check: add a file `internal/app/tui/zz_probe.go` in package `tui` that blank-imports `marshal/internal/db`. `TestTUIStaysOnTheClientSeam` fails, naming `zz_probe.go` with the pinned message. Delete the file.

## Task 4: Docs and full verification

**Goal:** the specs, the design and the repo guides describe P2 as done, the P3 list exists, and the whole suite passes.

**Files:**
- `docs/daemon/p2-client-seam.md`;
- `docs/daemon/p3-acp-transport.md` (header, "Where the code is today", and a new section after "In scope", nothing else);
- `docs/daemon/design.md`;
- `AGENTS.md`, `docs/ARCHITECTURE.md`.

**Steps:**

1. **Build the P3 list from the code.**
   - Print every row of `acpclient.Methods` whose `Exists` is false and whose `ACP` is not empty, for example with `grep -nE 'ACP: "[^"]+"\}' internal/acpclient/methods.go`, and the `ACP: … (new in P3)` doc lines of `internal/acpclient/acpclient.go`. Each distinct ACP name is one row.
   - Find the P3 plan for each from the P2 spec's C-section that names the method:
     - C5 (status, notices, the sheet): plan 3b;
     - C6 (transcript): plan 4b;
     - C7 (agent requests): plan 5;
     - C8 (turns, runs, the end flow and sessions): plan 6;
     - C9 to C11 (notes and commands; config, providers and trust; project data): plan 7.

     A method no C-section names takes the group of the Migration order item that added it.
   - Count the rows, the distinct names and the interface methods they carry. At `133f005b` there were 52 distinct new names over 85 table rows. Plans 8a and 8b add 8 and 4 new names. Record the counts you find.
2. **`p3-acp-transport.md`.**
   - A new section `## Methods P3 adds`, directly after `## In scope`. One subsection per P3 plan, in plan order. In each, one bullet per ACP name: the name in code font, then the interface methods it carries (`Session.X`, `Host.Y`). Mark the five R6 conversions (`Command`, `PlanPreflight`, `Host.NewSession`, `Postmortem`, `CanProbeModel`) with "(asynchronous, R6)" where their method appears. Open the section with the counts and one sentence: the table in `acpclient` is the source, and T4 ends P3 with no row marked new.
   - The header's `Depends on:` paragraph: P2 is done, so the plans after 1, 4a and 3a now wait only for each other. Replace "the boundary test's allowlist is empty, `WithLegacyState` is gone, and P2 plan 9 has written the list" with a statement that P2 closed with plan 9, naming the new section.
   - "Where the code is today", first bullet: replace its counts with the ones you found.
3. **`p2-client-seam.md`.**
   - Status: P2 is delivered. Replace "Delivered as nine plans … sub-plans." with the same sentence in the past tense, plus: "plan 9 closed it on <date>".
   - Implementation record: a plan 9 entry after plan 8b's. It lists:
     - `inproc.Session.State` and `stateOf`, with the number of test reads rewritten;
     - the four options and four fields deleted, and `app.Run`'s three options gone;
     - the allowlist and T1b deleted, and T1's new message;
     - the P3 list's counts, and where the list is;
     - that the Done-when manual run is left to the user.
4. **`design.md`.**
   - §4.5: keep the first paragraph. Replace the paragraphs from "During migration, an in-process adapter covers the 82 `session.State` calls…" to the end of the section with a short description of what exists:
     - the TUI's only path is `acpclient.Host` and `Session`, with `acpwire` for the wire types;
     - `inproc` implements them in process until P3;
     - `TestTUIStaysOnTheClientSeam` fails on any crossing, with no allowlist;
     - the spec's nine plans did the migration.

     Keep the two transports paragraph (pipe, bridge) and the fleet sentence: they are still the plan for P3 and P4.
   - §7, the P2 row: its Goal cell says "implemented (see the spec's implementation record)". Its plan list covers every plan: 1, 2a–2c, 3a–3d, 4, 5a–5e, 6, 7a–7d, 8a, 8b and 9. Leave the Done-when cell as it is.
   - §8, the transcript bullet: "settled (P2)" becomes "settled and implemented (P2)". T6's goldens pin the ported renderer.
5. **`AGENTS.md`.** In the "The TUI renders only" bullet, drop ", in progress" from "(daemon P2, in progress)". Change "the fix is to move the call behind the interface, never to add an allowlist entry" to "the fix is to move the call behind the interface; there is no allowlist".
6. **`docs/ARCHITECTURE.md`.** In the "Client seam" row, "with a shrinking allowlist" becomes "with no allowlist".
7. **Main may have moved.** If P3 plan 3a merged while this branch ran, merge `main` into the branch before the full verification. Expect conflicts only in `p3-acp-transport.md`, and only if both plans changed the same lines of its header. Keep both sides.
8. **Full verification:**

```bash
cd /Users/alecpullen/projects/marshal
mkdir -p /private/tmp/p2-9-suite
go vet ./...
TMPDIR=/private/tmp/p2-9-suite go test ./... -timeout 20m 2>&1 | tail -150
```

Expected: `go vet` is clean, and every package passes. Known load flakes, each to be rerun alone before treating it as new:
- `internal/acp`: `TestStackPatchApplyBudget`, `TestStackRoundTripBudget`;
- `internal/app`: `TestRuntimeAccessSerializesWithReloadAndQuiesce`, `TestRuntimeCloseRunsNoGitGC`;
- `internal/snapshot`: `TestRunManagedCancellationKillsTree`;
- `web/bridge`: `TestRecipeRunMaxUSDSetsAPausingCapForTheRun`, which hangs about one run in three in its fake agent. A hang there is known.

9. **Report** the commits, the counts from step 1, and the Done-when manual checklist from the P2 spec for the user to run: a turn with an approval, a question and a sudo prompt; an Ask/Act switch; a `/settings` change and `/connect` with preset setup; `/new` and resume; a subagent and a background job in the tray, and a drill-in; a swarm or SDD run; exit with a postmortem.

## Self-review

- **Spec coverage.** Migration order item 9: Tasks 2 (the options), 3 (allowlist and T1b) and 4 (the list, design §4.5, §7, §8). Done when: T1 with no allowlist and no `WithLegacyState` (Tasks 2 and 3); "the methods marked new are listed for P3" (Task 4); `go test` and `go vet` (Task 4). The manual run is the user's.
- **What could go wrong.** The rewrite in Task 1 is the largest change by count. Its Verify greps for leftovers, and its mutation check proves the tests read the session's state. Task 2's Verify greps every deleted name across `internal` and `cmd`. Task 3's mutation check proves T1 still catches a crossing with no allowlist.
- **Parallel safety.** This plan touches no file plan 3a touches, apart from `p3-acp-transport.md`, where the two edit different sections.
