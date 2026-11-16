# P5 plan 5b: the Diff tab and finishing an agent — implementation plan

Date: 2026-11-15. Status: ready for review.

Base: `origin/main` at `bcceadab`. Every anchor below was checked there.

- **This plan needs plan 5a on `main`** for every task: it extends 5a's seam
  methods, its client's `post`, its status line and its modal type. Anchors in
  plan 5a's files are its Contracts, re-checked then (see Execution).
- **The baseline** is CI's suite command at `bcceadab`. Its result is in the
  spec's "Plan 4c" record entry, where this batch's baseline is recorded.
- **The spec edits are already made,** uncommitted in the main checkout, in
  `docs/daemon/p5-fleet-screen.md`: F4's "Decided by plan 5b" bullets,
  Migration order item 5, the Design table and the "Plan 5b" record
  placeholder.

Approved design: [p5-fleet-screen.md](../p5-fleet-screen.md): decisions 3 and
4; F3; F4; Migration order item 5; Test W7.
Previous plans: [plan 5a, the fleet's actions](2026-11-15-p5-5a-actions-plan.md),
whose seam, client and modal this plan extends.
Sibling plans: [plan 6](2026-11-15-p5-6-needs-you-plan.md), which also starts
agents' own children differently: see Task 5's note on the harness.
Authoring workflow: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md).

**How this plan is written.** No prototype was built, and the plan embeds no
implementation code. The Contracts section holds declarations only. Every other
step states its decision in prose. Executing the plan also tests how well
Marshal follows a plan. "As `X` does" means `X` exists and is the model to copy.

## Goal

From the fleet screen the user can review an agent's diff and finish it:

- the right panel's Diff tab (`2`) lists the changed files and shows the
  selected file's diff, coloured;
- the Info tab's `v` runs the agent's verify gate;
- `f` opens the finish modal (fleet-cli §3.7): a commit message pre-filled
  from the agent's draft, then a merge or a push. A push runs the gate, shows
  a failure, and offers the override. The result names where the work went;
- the same modal discards, once the agent's name is typed.

W7 holds: against a real bridge, a failed gate, then the override, then the
result.

## Non-goals

- **Downloading a patch.** A read-only agent's work leaves as a patch. The
  modal says to download it from the web UI and still offers discard. A
  terminal program that writes files on its own would be a new side effect for
  the TUI.
- **Review comments** and the review drafts the web UI has. They stay in the
  web UI (fleet-cli §1's out-of-v1 list).
- **Choosing the destination.** It follows the agent's source, as the web UI's
  does.

## Assumptions

Each was checked at `bcceadab`.

- **The routes.**
  - `GET /api/agents/{id}/diff`, with an optional `path`
    (`web/bridge/http.go:547`). Without `path` it answers the files with their
    counts. With one, it answers that file's unified diff too
    (`internal/acp/worktree.go:299`).
  - `GET /api/agents/{id}/commit-draft` (`http.go:576`), whose answer is
    `contract.CommitDraft`. An agent without it answers the bridge's
    unsupported error.
  - `POST /api/agents/{id}/verify` (`http.go:586`) runs the gate and answers
    the stored record. The record also arrives as a `gate` delta, which plan 3
    keeps in `FleetState.Gates`.
  - `POST /api/agents/{id}/merge` (`http.go:605`), with `contract.MergeRequest`.
    A refusal is a 409 whose body is the `contract.MergeResult`, with its
    reason: `dirty`, `project_dirty`, `target_moved` or `conflicts`
    (`internal/acp/worktree.go:327`).
  - `POST /api/agents/{id}/exit` (`http.go:639`), with `contract.ExitRequest`,
    answering `contract.ExitResult`.
  - `POST /api/agents/{id}/discard` (`http.go:631`).
- **Where work goes.** The destination follows the source
  (`web/bridge/exit.go:18`): a local agent merges, a git agent pushes, and a
  read-only git agent leaves a patch. A project's ship target may change a git
  agent's destination (`web/bridge/projects.go:100`), which `exit` then
  reports without pushing (`exit.go:55`).
- **Only a push runs the gate.** `exit` commits, verifies agent-side, stores
  the gate, and stops with `blocked` when the gate failed or was skipped and
  no override was given (`exit.go:68` to `:84`). An override needs a reason
  (`exit.go:59`). A merge runs no gate.
- **The web UI's finish panel** (`web/ui/src/lib/ExitPanel.svelte`) is the
  behaviour to match: a dirty merge asks for a commit message and retries;
  every other refusal is a sentence (`web/ui/src/lib/diff.ts:16`); a push
  shows the gate's result and offers the override; discard is confirmed.
- **The diff renderer.** `diffview.Render` (`internal/diffview/diffview.go:91`)
  styles a unified diff for the TUI, and the TUI may import it
  (`internal/app/tui/boundary_test.go:44`).
- **The verify gate's commands** are inferred from the tree when the project
  configures none (`internal/pipeline/verify.go:193`): a Go module verifies
  with `go build ./...` and `go test ./...`.
- **A git agent on the local profile runs on its own child**
  (`web/bridge/fleet.go:835`), started as a host process with no arguments
  (`fleet.go:462`). `test/daemon`'s child entry accepts only
  `acp --viewers …` (`test/daemon/main_test.go:36`).
- **A registered repository is a record with a URL** (`web/bridge/workspace.go:808`),
  and a spawn names it with `SpawnOptions.RepoID` (`fleet.go:1102`). The
  checkout is a mirror clone of that URL (`fleet.go:1290`), which git accepts
  as a local path.

## Decisions this plan makes beyond the spec

1. **The Diff tab is `2`,** as plan 3 reserved it.
   - It reads the files when it is shown, when the selection moves to another
     agent while it is shown, and when the shown agent's changed-files count
     changes.
   - `j` and `k` move between files while the right panel has focus, and
     reading a file's diff follows the cursor. `J` and `K` scroll the diff.
   - The header reads "<N> files +<added> −<removed>".
   - A Sessions row has no Diff tab.
   - A failed read shows its sentence in the panel, not on the status line: it
     is a read, not an action.
2. **`v` on the Info tab** runs the gate. The line reads "verifying…" until the
   call returns. The result shows through the gate delta, and a refused call is
   an action failure, as plan 5a shows them.
3. **`f` opens the finish modal on an idle agent.** On one that is running or
   waiting, the status line says "finish needs <name> idle: cancel its turn
   first (x)".
4. **The modal's steps:**
   - **Message.** A text field pre-filled from the commit draft, read when the
     modal opens. A failed or unsupported read leaves it empty.
   - **Ship.** `⏎` ships to the destination decision 5 picks.
   - **Discard.** `ctrl+d` asks for the agent's name. Discarding needs it typed
     exactly, then `⏎`.
   - **Close.** `esc` closes the modal at any step, without acting.
5. **The destination** is the web UI's rule, read from the row: a local agent
   merges, a read-only git agent leaves a patch, any other git agent exits.
   An exit that answers another destination (a project's ship target) shows
   that destination's result rather than pretending it pushed.
6. **A merge's refusals** are the web UI's sentences, in the TUI's own words:
   - `dirty` asks for a commit message and retries with it;
   - `project_dirty`, `target_moved` and `conflicts` say what to do and leave
     the modal open on the message step;
   - any other reason reads "merge refused (<reason>)".
7. **A blocked push** shows the failed command and the last 20 lines of its
   output, then a reason field. `⏎` on a non-empty reason exits again with
   the override. An empty reason is refused in the field, as the bridge would
   refuse it.
8. **The result** names where the work went: the merged branch and target, or
   the pushed branch and the pull request's URL when there is one. Any key
   closes it. Every result and every failure is also an Events line.
9. **`test/daemon`'s child accepts plain `acp`** as well as `acp --viewers`,
   running `acp.Run` for it, so an agent on its own child can run in the
   harness. Plan 6 starts those children with `--viewers`, which the entry
   already handles.

## Execution

- In the main checkout, if `git status` shows any of these uncommitted, commit
  them to `main` first, as explicit paths, with the message
  `docs/daemon: record P5 plans 4c, 5a, 5b and 6`:
  - `docs/daemon/p5-fleet-screen.md`;
  - this plan, `2026-11-15-p5-4c-viewer-turns-plan.md`,
    `2026-11-15-p5-5a-actions-plan.md` and `2026-11-15-p5-6-needs-you-plan.md`.
- **Wait for plan 5a on `main`.** Then branch: `git switch -c p5-5b-finish`
  from `main`, or create a worktree on that branch. Check this plan's anchors
  in plan 5a's files against its code.
- Run the tasks in order. They touch `internal/acpclient/fleet.go`,
  `internal/fleetclient`, `internal/app/tui/fleet`, and `test/daemon`'s
  `main_test.go` and one new file.
- Run each task's Verify and commit it as `p5-5b-finish: task N — <title>`.
- Stage explicit paths only. Never `git add -A` without a path.
- Run `gofmt -w` on every Go file a task touches before its Verify.
- Keep `TMPDIR` short for every test run: `TMPDIR=/private/tmp/p5-5b-suite`.

## Contracts

### The seam (`internal/acpclient/fleet.go`, Task 1)

```go
// Fleet gains, each with a Routes: doc line:

	// Diff is an agent's changed files and, when path is not "", that
	// file's diff.
	//
	// Routes: GET /api/agents/{id}/diff.
	Diff(ctx context.Context, agentID, path string) (contract.DiffResult, error)

	// CommitDraft is the agent's suggested commit message.
	//
	// Routes: GET /api/agents/{id}/commit-draft.
	CommitDraft(ctx context.Context, agentID string) (contract.CommitDraft, error)

	// Verify runs the agent's gate and answers its record.
	//
	// Routes: POST /api/agents/{id}/verify.
	Verify(ctx context.Context, agentID string) (contract.GateRecord, error)

	// Merge merges a local agent's branch. A refusal is a result with
	// Merged false and its reason, not an error.
	//
	// Routes: POST /api/agents/{id}/merge.
	Merge(ctx context.Context, agentID string, req contract.MergeRequest) (contract.MergeResult, error)

	// Exit commits, verifies and pushes a git agent's work.
	//
	// Routes: POST /api/agents/{id}/exit.
	Exit(ctx context.Context, agentID string, req contract.ExitRequest) (contract.ExitResult, error)

	// Discard throws the agent's work away and retires it.
	//
	// Routes: POST /api/agents/{id}/discard.
	Discard(ctx context.Context, agentID string) error
```

### The model (`internal/app/tui/fleet`, Tasks 3 and 4)

```go
// The model gains:
	diff diffView // the Diff tab's state for the shown agent

// diffView is the Diff tab's files, cursor, the selected file's diff and
// its scroll, for one agent.
type diffView struct{ /* the executor's */ }

// diffReadMsg is one diff read's answer.
type diffReadMsg struct {
	agentID string
	path    string // "" for the file list
	result  contract.DiffResult
	err     error
}

// finishModal is the finish flow (decision 4). It implements plan 5a's
// modal.
type finishModal struct{ /* the executor's */ }

// destination is decision 5's rule over a row.
func destination(a contract.AgentStatus) string // "merge", "push" or "patch"

// mergeRefusal is decision 6's sentence for a refused merge.
func mergeRefusal(r contract.MergeResult) string
```

## Task 1 — the finishing actions on the seam and the client

**Files:** `internal/acpclient/fleet.go`, `internal/fleetclient/actions.go`,
`internal/fleetclient/actions_test.go`, the two fakes.

1. Add the Contracts' methods, their doc lines and their `FleetRoutes` rows.
2. In the client, each is a call on plan 5a's `post` or the client's `get`,
   with the path escaped. `Merge` decodes a 409's body as the result and
   returns no error for it; any other refusal is a `FleetError`.
3. The fakes gain recording implementations, as for plan 5a's actions.
4. Tests in `actions_test.go`, added to plan 5a's:
   - `TestEachFinishingActionSendsItsRoute`.
   - `TestARefusedMergeIsAResult`: a 409 with `{"merged": false, "reason":
     "dirty"}` answers that result and no error.
   - `TestADiffReadNamesItsPath`: the path is sent as `path`, escaped.

   The route test in `test/daemon` covers the new rows unchanged.

**Verify:** `TMPDIR=/private/tmp/p5-5b-suite CGO_ENABLED=1 go test ./internal/acpclient/ ./internal/fleetclient/ ./internal/app/tui/... ./internal/launch/ ./test/daemon/ -count=1 -timeout 15m`.

## Task 2 — the Diff tab

**Files:** `internal/app/tui/fleet/model.go`, `keys.go`, `view.go`, a new
`diff.go`, and a new `diff_test.go`.

1. Decision 1's tab, behind `2`, with reads as commands that answer
   `diffReadMsg`. An answer for an agent or a path no longer shown is dropped.
2. The diff renders through `diffview.Render` at the panel's inner width,
   with the theme's colours, and is cut to the panel with the scroll offset.
   Under `NO_COLOR` the `+` and `−` gutters carry the meaning.
3. The footer's hints for the right panel name `j/k file` and `J/K scroll`
   while the tab is shown.
4. Tests in `diff_test.go`:
   - `TestTheDiffTabReadsTheFilesOnce`: showing the tab reads the list once,
     and a second state with the same count reads nothing.
   - `TestAChangedCountRereadsTheFiles`.
   - `TestTheCursorReadsTheFilesDiff`: `j` reads the next file's diff.
   - `TestALateDiffForAnotherAgentIsDropped`.
   - `TestTheDiffHeaderTotals`: two files, +5 −2, read "2 files +5 −2".
   - Two goldens, `fleet-100x30-diff` and `fleet-140x40-diff`, written and
     compared as plan 3's are.

**Verify:** `TMPDIR=/private/tmp/p5-5b-suite CGO_ENABLED=1 go test ./internal/app/tui/fleet/ -count=1`.

## Task 3 — verify, and the finish modal

**Files:** `internal/app/tui/fleet/keys.go`, `view.go`, a new `finish.go`, and
a new `finish_test.go`.

1. Decision 2's `v` on the Info tab.
2. Decisions 3 to 8's modal, in `finish.go`, built on plan 5a's modal type
   and drawn over the right panel.
3. The help screen and the footer gain `v` and `f` where they apply.
4. Tests in `finish_test.go`, over the fake fleet:
   - `TestFinishIsRefusedWhileRunning`.
   - `TestTheMessageStartsFromTheDraft`, and
     `TestAFailedDraftLeavesTheMessageEmpty`.
   - `TestALocalAgentMerges`: `⏎` calls `Merge` with the message, and the
     result names the branch and the target.
   - `TestADirtyMergeAsksForAMessageAndRetries`.
   - `TestAConflictSaysWhere`: the conflicts are listed, and nothing retries.
   - `TestAGitAgentExits`: `⏎` calls `Exit` with the message.
   - `TestABlockedExitOffersTheOverride`: a blocked result shows the failed
     command and output; a reason then calls `Exit` again with the override.
   - `TestAnEmptyOverrideReasonIsRefused`.
   - `TestAnExitToAnotherDestinationSaysSo`: an answer naming `patch` shows
     the patch sentence.
   - `TestDiscardNeedsTheNameTyped`: a wrong name does not call `Discard`;
     the right one does.
   - `TestEscClosesTheModalWithoutActing`.
   - `TestDestinationFollowsTheSource`: local merges, read-only git is a
     patch, other git pushes.
   - Goldens `fleet-100x30-finish` (the message step) and
     `fleet-100x30-blocked` (a blocked push with its override field).

**Verify:** Task 2's command.

**Mutation check:** send the override on the first exit; `TestAGitAgentExits`
must fail, because the first exit must carry none. Restore.

## Task 4 — W7 against a real bridge

**Files:** `test/daemon/main_test.go`, `test/daemon/finish_test.go` (new).

1. Decision 9: the child entry runs `acp.Run` for a plain `acp`, and keeps
   `RunWithViewers` for `acp --viewers <url>`. Anything else is still an error.
2. `TestTheFinishFlowOverridesAFailedGate` (W7):
   - make a bare repository whose `main` holds a Go module with one failing
     test, and register it with the harness's workspace;
   - spawn a git agent from it with `h.fleet.Spawn` and `SpawnOptions.RepoID`,
     in process, as the harness's other tests reach the fleet;
   - give the agent one change through its session, as the provider fixture
     scripts a turn, so there is work to push;
   - through a `fleetclient` over the harness's socket, `Exit` with a message:
     the result is blocked, its gate names `go test ./...` as the failed
     command, and nothing reached the bare repository;
   - `Exit` again with an override reason: the destination is `push`, the
     branch exists in the bare repository with the commit, and the agent's
     row records the override.
3. **If the harness cannot spawn a git agent from a local bare repository,**
   stop and record why in the plan's record. Do not turn W7 into a merge,
   which runs no gate.
4. `TestALocalAgentsMergeLandsOnItsTarget`, beside it: an isolated local agent
   with one change merges through the client, and the project's target branch
   has the change.

**Verify:** `TMPDIR=/private/tmp/p5-5b-suite CGO_ENABLED=1 go test ./test/daemon/ -count=1 -timeout 15m`.

**Mutation check:** make the client send the override on the first `Exit`; W7
must fail on the first exit's result. Restore.

## Task 5 — the docs, the record and full verification

**Files:** `docs/daemon/p5-fleet-screen.md`, `AGENTS.md`.

1. Fill the "Plan 5b" record, as plan 3's is written.
2. `AGENTS.md`'s `internal/app/tui/fleet/` line says the screen also shows an
   agent's diff and finishes it.
3. **The manual run.** Against a real local daemon with a real model: spawn
   an isolated local agent with a small task, read its diff on the Diff tab,
   run `v`, and merge it from the modal. Then discard a second agent. If a git
   agent can be run against a real remote, push one with a failing gate and
   override it; otherwise say W7 covers it.
4. Full verification: `gofmt -l internal cmd test web` prints nothing; `go vet
   ./...` is clean; CI's suite command with `TMPDIR=/private/tmp/p5-5b-suite`
   passes; `CGO_ENABLED=1 go build -o marshal ./cmd/marshal`; the usability
   suite passes.

## Done when

- The Diff tab lists an agent's files and shows each diff, coloured.
- `v` runs the gate, and `f` merges, pushes with the gate and its override, or
  discards after the name is typed.
- W7 passes against a real bridge, and a local agent's merge lands.
- Every test named above passes, each mutation check fails as described, and
  CI's suite command passes.
