# P5 plan 5a: the fleet's actions — implementation plan

Date: 2026-11-15. Status: ready for review.

Base: `origin/main` at `bcceadab`, where plans 3 and 4a are merged. Every
anchor below was checked there.

- **Plan 5 became two** while this was written: 5a, the actions and the keys
  that need no new panel, and 5b, the Diff tab and the finish modal. The
  Migration order's item 5 says so. 5b needs this plan's seam.
- **Plan 4b is running beside this plan.** Both add keys to the fleet model,
  and both edit `internal/app/tui/fleet/keys.go`. Execution says how to merge.
- **The baseline** is CI's suite command at `bcceadab`. Its result is in the
  spec's "Plan 4c" record entry, where this batch's baseline is recorded.
- **The spec edits are already made,** uncommitted in the main checkout, in
  `docs/daemon/p5-fleet-screen.md`: F3's and F4's "Decided by plan 5a"
  bullets, Migration order item 5, the Design table and the "Plan 5a" record
  placeholder.

Approved design: [p5-fleet-screen.md](../p5-fleet-screen.md): decisions 1, 3
and 4; F3; F4; Migration order item 5.
Previous plans: [plan 3, the fleet screen](2026-11-14-p5-3-fleet-screen-plan.md),
whose seam, client and model this plan extends.
Sibling plans: [plan 5b, finishing](2026-11-15-p5-5b-finish-plan.md), which
adds the rest of the actions; [plan 4b](2026-11-14-p5-4b-open-agent-plan.md),
which adds `⏎`; [plan 4c](2026-11-15-p5-4c-viewer-turns-plan.md), whose
followed runs this plan's cancel reaches.
Authoring workflow: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md).

**How this plan is written.** No prototype was built, and the plan embeds no
implementation code. The Contracts section holds declarations only. Every other
step states its decision in prose. Executing the plan also tests how well
Marshal follows a plan. "As `X` does" means `X` exists and is the model to copy.

## Goal

From the fleet screen the user can, without opening an agent:

- answer an agent's approval inline (`a`, then `y` or `n`) and its question in
  a modal;
- spawn an agent (`n`), with the project, a name, a mode and a first prompt,
  and the project's recent prompts behind `ctrl+r`;
- cancel an agent's turn (`x`);
- approve or deny an intake submission (`⏎` on it in Needs you).

Each action goes through `acpclient.Fleet`, and a refusal shows on the status
line for 4s and as a line in Events. While the connection is down, every
action is refused with a status-line message.

## Non-goals

- **The Diff tab, verify, and the finish modal** (exit, merge, discard). They
  are plan 5b.
- **Editing an approval's command** (fleet-cli's `[e]dit`). The session screen
  has it; the fleet screen answers yes or no.
- **A typed access request** (an approval carrying `modeAccess`). The fleet
  screen shows it and says to open the agent to answer it. The session
  screen's access panel is where its choices are made.
- **Steering, prompting a running agent, and the mode switch.** They belong to
  the session screen (F4).

## Assumptions

Each was checked at `bcceadab`.

- **The bridge serves every action already:**
  - spawn: `POST /api/agents` (`web/bridge/http.go:496`), with
    `contract.SpawnRequest` and `contract.SpawnResult`
    (`web/bridge/contract/spawn.go`);
  - answers: `POST /api/permissions/{toolCallId}` and
    `POST /api/questions/{questionId}` (`http.go:1121`, `:1141`), with a 410 for
    one already settled;
  - cancel: `POST /api/sessions/{id}/cancel` (`http.go:909`), which takes an
    agent's ID because `registryForSession` resolves one (`http.go:689`);
  - intake: `POST /api/pending/{id}/approve` and `…/deny` (`http.go:1273`,
    `:1288`), with `contract.ApproveResult` and `contract.ActionStatus`;
  - recent prompts: `GET /api/prompts/recent?project=…&limit=…`
    (`web/bridge/review.go:206`), answering `{"prompts": [...]}`.
- **Refusals are JSON.** Every handler's error is `{"error": "…"}` through
  `writeErr` (`http.go:286`). A budget refusal is a 429 whose body names its
  scope; the web UI shows it as "Budget reached (<scope>)"
  (`web/ui/src/lib/api.ts:224`).
- **The answer bodies are not in the contract yet.** `Decision`, `Answer` and
  `Answers` are declared in `web/bridge/registry.go:39`, `:55` and `:63`, and
  `ModeAccessChoice` in `web/bridge/interaction.go:150`. The TUI may import
  only the contract, so they have to move.
- **The question's shape.** A question request's params carry `questionId` and
  `questions`, each with `question`, `options` (a label and an optional
  description) and `multi` (`internal/acp/questions.go:22`,
  `internal/app/session/pending.go:150`). There is no free-text flag: a
  question with no options takes a typed answer.
- **An approval's params** carry `toolName`, and optionally `command`, `risk`,
  `reason`, `diff`, `skillGate` and `modeAccess`
  (`internal/acp/permissions.go:46`). The fleet's row carries those params
  verbatim (`contract.PendingRequest.Params`).
- **The seam and its table.** `acpclient.Fleet` holds `State` and `Changed`
  (`internal/acpclient/fleet.go:33`). `FleetRoutes` lists each method's
  routes (`fleet.go:54`), and two tests keep the table and the doc lines in
  step (`internal/acpclient/fleet_test.go:20`, `:47`).
- **Two fakes implement the interface:** the fleet model's
  (`internal/app/tui/fleet/fleet_test.go:18`) and launch's
  (`internal/launch/fleet_test.go:23`).
- **The client's request path.** `fleetclient.Client.get` reads one answer
  with the token (`internal/fleetclient/client.go:498`), and a non-200 is a
  `statusError` carrying the status (`client.go:435`).
- **The route test hits each route as written** (`test/daemon/fleetclient_test.go:116`)
  and fails on a 404 or a 405. A route with a path parameter, such as
  `/api/agents/{id}/diff`, would reach its handler with the literal `{id}`
  and be answered 404 by the handler, not the mux.
- **The model's keys** are one switch in `handleKey`
  (`internal/app/tui/fleet/keys.go:11`). The Activity tab's lines come from
  `activityLines` (`view.go:508`), the waiting request's text from
  `describePending` (`view.go:694`), and the bottom row from `footer`
  (`view.go:733`). `textfield` (`internal/app/tui/textfield`) is the TUI's
  text input.

## Decisions this plan makes beyond the spec

1. **The answer bodies move into the contract** in a new `answers.go`:
   `Decision`, `Answer`, `Answers` and `ModeAccessChoice`, with their JSON tags
   unchanged. The bridge keeps its names as aliases, as plan 1 did for the
   others. The generated `contract.ts` gains them, and the web UI's `api.ts`
   re-exports the three answer types from it. `interaction.ts` keeps its own
   `ModeAccessChoice` unless `npm run check` shows the two disagree, in which
   case it re-exports the generated one.
2. **Each action is one method that blocks until the bridge answers,** bounded
   by the caller's context. The model gives each call 30s. A refusal is a
   `*acpclient.FleetError`, which carries the status and the bridge's own
   sentence.
   - A 429 is a budget refusal, and its sentence is "Budget reached
     (<scope>)", as the web UI's is.
   - A 410 on an answer is not an error: the request was settled elsewhere,
     and the model says "already answered".
   - A transport failure is a `FleetError` with status 0 and the dial's text.
3. **The last action's error stays in the model, not the client.** F3 listed
   it as a read. The actions return their error to the caller, so a second
   copy in `FleetState` would be one more thing to keep current. The model
   shows the last failure on its status line and in Events, which is what F4
   asks for.
4. **The approval is answered inline.**
   - `a` on an agent with a pending approval switches the right panel to
     Activity and shows the approval's tool, command, risk and reason with
     `[y]es [n]o`.
   - While that prompt is open, `y` and `n` answer, and `esc` closes it.
     Outside it, `n` is spawn, as fleet-cli's global keys say. A skill gate is
     answered the same way: yes allows once, no is the sticky deny.
   - An approval with `modeAccess` says "open the agent to answer this access
     request" and offers nothing else (Non-goals).
5. **The question is a modal.** Its rows are the questions in order, each with
   its options.
   - `j` and `k` move between options, and `space` toggles one under a multi
     question.
   - `⏎` picks a single option and moves to the next question, or submits
     after the last.
   - A question with no options takes a typed answer in a text field.
   - `d` declines the whole request, and `esc` closes the modal without
     answering.
6. **The spawn modal** (fleet-cli §3.8) has four fields, moved through with
   `tab`:
   - the project, chosen with `j` and `k` from the available projects;
   - the name;
   - the mode, cycled with `space` through the project's default, `ask`, `act`
     and `act` with auto, which are the request's `mode` and `auto`
     (`internal/interaction/state.go:59`: auto exists only with act). On the
     project's default the request leaves both out, and the bridge applies
     the default (`http.go:511`);
   - the first prompt.

   `⏎` on the prompt field spawns. `ctrl+r` in the prompt field cycles the
   project's recent prompts, read once when the modal opens, at most 20.
   Success selects the new agent; a warning goes to Events.
7. **Cancel** is `x` on a running agent, with no confirmation, as the web UI's
   stop is. It reaches a turn a viewer started too, once plan 4c is merged.
8. **Intake.** `⏎` on an intake item in Needs you opens a two-line prompt,
   "approve <title>? [y]es [n]o, deny", and `esc` closes it. Approving selects
   the spawned agent once it appears.
9. **Refused while down.** With the connection anything but connected, each
   action key sets the status line to "not connected to <server>" and calls
   nothing.

## Execution

- In the main checkout, if `git status` shows any of these uncommitted, commit
  them to `main` first, as explicit paths, with the message
  `docs/daemon: record P5 plans 4c, 5a, 5b and 6`:
  - `docs/daemon/p5-fleet-screen.md`;
  - this plan, `2026-11-15-p5-4c-viewer-turns-plan.md`,
    `2026-11-15-p5-5b-finish-plan.md` and `2026-11-15-p5-6-needs-you-plan.md`.

  Another plan's executor may have committed them already. Leave the main
  checkout's other untracked work alone.
- Then branch: `git switch -c p5-5a-actions` from `main`, or create a worktree
  on that branch.
- Run the tasks in order. They touch:
  - `web/bridge/contract`, `web/bridge`'s `registry.go` and `interaction.go`
    (the aliases only), `web/ui/src/lib/contract.ts`, `api.ts` and possibly
    `interaction.ts`;
  - `internal/acpclient/fleet.go`, `internal/fleetclient`, and the two fakes;
  - `internal/app/tui/fleet`;
  - `test/daemon/fleetclient_test.go`.
- **Plan 4b also edits `internal/app/tui/fleet/keys.go`,** to add `⏎` on an
  agent or a session. Whichever merges second keeps both: `⏎` on an intake
  item opens this plan's prompt, and `⏎` anywhere else is plan 4b's open.
- Run each task's Verify and commit it as `p5-5a-actions: task N — <title>`.
- Stage explicit paths only. Never `git add -A` without a path.
- Run `gofmt -w` on every Go file a task touches before its Verify.
- Keep `TMPDIR` short for every test run: `TMPDIR=/private/tmp/p5-5a-suite`.

## Contracts

### The contract (`web/bridge/contract/answers.go`, Task 1)

```go
// Decision is a POST /api/permissions/{id} body.
type Decision struct {
	Approved   bool              `json:"approved"`
	Edited     string            `json:"edited,omitempty"`
	ModeAccess *ModeAccessChoice `json:"modeAccess,omitempty"`
}

// ModeAccessChoice is a typed capability choice, forwarded untouched.
type ModeAccessChoice struct {
	RequestID string      `json:"requestId"`
	SessionID string      `json:"sessionId"`
	Current   AccessState `json:"current"`
	Mode      string      `json:"mode,omitempty"`
}

// Answer is one question-and-response pair.
type Answer struct {
	Question string `json:"question"`
	Answer   string `json:"answer"`
}

// Answers is a POST /api/questions/{id} body.
type Answers struct {
	Answers  []Answer `json:"answers,omitempty"`
	Declined bool     `json:"declined,omitempty"`
}

// RecentPrompts is the GET /api/prompts/recent answer.
type RecentPrompts struct {
	Prompts []string `json:"prompts"`
}
```

### The seam (`internal/acpclient/fleet.go`, Task 2)

```go
// Fleet gains, each with a Routes: doc line:

	// Spawn starts an agent.
	//
	// Routes: POST /api/agents.
	Spawn(ctx context.Context, req contract.SpawnRequest) (contract.SpawnResult, error)

	// AnswerPermission answers an agent's approval. One settled elsewhere
	// answers ErrSettled.
	//
	// Routes: POST /api/permissions/{id}.
	AnswerPermission(ctx context.Context, id string, d contract.Decision) error

	// AnswerQuestion answers an agent's question, as AnswerPermission does.
	//
	// Routes: POST /api/questions/{id}.
	AnswerQuestion(ctx context.Context, id string, a contract.Answers) error

	// Cancel stops an agent's turn.
	//
	// Routes: POST /api/sessions/{id}/cancel.
	Cancel(ctx context.Context, agentID string) error

	// ApproveIntake spawns a submission's agent.
	//
	// Routes: POST /api/pending/{id}/approve.
	ApproveIntake(ctx context.Context, id string) (contract.ApproveResult, error)

	// DenyIntake discards a submission without spawning.
	//
	// Routes: POST /api/pending/{id}/deny.
	DenyIntake(ctx context.Context, id string) error

	// RecentPrompts is a project's last prompts, newest first.
	//
	// Routes: GET /api/prompts/recent.
	RecentPrompts(ctx context.Context, project string, limit int) ([]string, error)

// FleetError is a refused action (decision 2).
type FleetError struct {
	Status  int    // the HTTP status; 0 when the bridge was not reached
	Message string // the bridge's sentence, or the budget's
}

func (e *FleetError) Error() string

// ErrSettled is an answer to a request settled elsewhere (a 410).
var ErrSettled = errors.New("already answered")
```

### The client (`internal/fleetclient/actions.go`, Task 3)

```go
// post sends one action: body encoded as JSON when it is not nil, out
// decoded when it is not nil. A non-2xx answer is a *acpclient.FleetError.
func (c *Client) post(ctx context.Context, path string, body, out any) error

// actionError turns a refused answer into the seam's error (decision 2).
func actionError(resp *http.Response) error
```

### The model (`internal/app/tui/fleet`, Tasks 4 to 6)

```go
// The model gains:
	modal  modal  // nil when no modal is open
	answer string // the agent whose approval is open inline; "" for none
	status statusLine

// modal is one open modal: the question, the spawn form or the intake
// prompt. Each takes the keys while it is open.
type modal interface {
	update(m Model, msg tea.KeyPressMsg) (Model, tea.Cmd)
	view(m Model, width, height int) string
}

// statusLine is the footer's message: an action's failure, or a refusal
// while down, shown until until.
type statusLine struct {
	text  string
	until time.Time
}

// actionDoneMsg is one action's answer.
type actionDoneMsg struct {
	what string // the Events line's verb: "answer", "spawn", "cancel", …
	name string // the agent's or submission's name
	err  error
	// selectID is the row to select once it appears; "" for none.
	selectID string
	warning  string
}
```

## Task 1 — the answer bodies join the contract

**Files:** `web/bridge/contract/answers.go` (new), `web/bridge/registry.go`,
`web/bridge/interaction.go`, `web/bridge/review.go`,
`web/ui/src/lib/contract.ts` (generated), `web/ui/src/lib/api.ts`, possibly
`web/ui/src/lib/interaction.ts`.

1. Declare the Contracts' types in `answers.go`. Replace the bridge's
   declarations at `registry.go:39`, `:55`, `:63` and `interaction.go:150` with
   aliases, each with a one-line doc comment saying which contract type it is,
   as the plan-1 aliases read.
2. `recentPrompts` (`review.go:206`) encodes `contract.RecentPrompts` in place
   of its map, with the same bytes.
3. Regenerate `contract.ts` with the command in `AGENTS.md`. Re-export
   `Decision`, `Answer` and `Answers` from it in `api.ts` in place of the
   hand-written interfaces (`api.ts:490`, `:504`, `:509`), as plan 1 did for
   the others. Apply decision 1's rule to `ModeAccessChoice`.
4. Tests:
   - `TestTheAnswerBodiesKeepTheirBytes` in `web/bridge/contract`: a decision
     with an edit and a typed choice, a declined answer, and a set of answers
     each encode to the bytes the bridge's handlers decoded before the move.
   - The bridge's existing permission and question tests pass unchanged.

**Verify:** `TMPDIR=/private/tmp/p5-5a-suite go test ./web/... -count=1 -timeout 20m`,
`go test ./web/bridge/contract/tsgen -run TestTypeScriptContractIsCurrent`,
then `cd web/ui && npm run check && npm test`.

## Task 2 — the actions on the seam

**Files:** `internal/acpclient/fleet.go`, `internal/acpclient/fleet_test.go`,
`internal/app/tui/fleet/fleet_test.go`, `internal/launch/fleet_test.go`.

1. Add the Contracts' methods, `FleetError` and `ErrSettled`. Each method's
   doc ends with its `Routes:` line, and `FleetRoutes` gains one row per
   method with the same routes, in the interface's order.
2. Both fakes keep compiling by embedding the interface, so a test that calls
   an action it did not set up fails loudly. The fleet model's fake then gains
   recording implementations for the actions, which Tasks 4 to 6 use.
3. The two existing table tests cover the new rows. Add
   `TestAFleetErrorNamesTheBridgesSentence`: the error's text is the message,
   with the status only when the message is empty.

**Verify:** `TMPDIR=/private/tmp/p5-5a-suite CGO_ENABLED=1 go test ./internal/acpclient/ ./internal/app/tui/... ./internal/launch/ -count=1`.

## Task 3 — the client's actions

**Files:** `internal/fleetclient/actions.go` (new),
`internal/fleetclient/actions_test.go` (new),
`test/daemon/fleetclient_test.go`.

1. `post` is `get`'s twin (`client.go:498`): the token, the JSON body, the
   status check and the decode. It is bounded by the caller's context only.
2. `actionError` reads the body's `error` field, or for a 429 its `scope`,
   and builds decision 2's error. A 410 is `ErrSettled`.
3. Each action is a thin call on `post`, or on `get` for the recent prompts,
   with every path segment escaped.
4. An action never changes the client's state. The stream reports the result,
   as it reports every other change.
5. The route test (`test/daemon/fleetclient_test.go:116`) learns path
   parameters: it substitutes `missing` for each `{…}` segment, and counts a
   404 or 405 only when its body is not the bridge's JSON error. The mux's own
   404 is plain text, so a route the mux does not serve still fails.
6. Tests in `actions_test.go`, over an `httptest` server that records each
   request:
   - `TestEachActionSendsItsRoute`: every action's method, path and body, for
     one call each.
   - `TestARefusalCarriesTheBridgesSentence`: a 409 with `{"error": "…"}` is a
     `FleetError` with that sentence and status.
   - `TestABudgetRefusalSaysWhichBudget`: a 429 naming `daily` reads "Budget
     reached (daily)".
   - `TestALateAnswerIsSettled`: a 410 answers `ErrSettled`.
   - `TestAnActionSendsTheToken`: with a token function, the action carries
     its bearer header.
   - `TestTheClientsActionsReachARealBridge`, in `test/daemon`: against the
     harness, spawn an agent with a prompt, cancel its turn, and see its
     status leave running. Approve one intake submission and deny another,
     each added in process with `h.fleet.Submit` (`web/bridge/intake.go:52`).

**Verify:** `TMPDIR=/private/tmp/p5-5a-suite CGO_ENABLED=1 go test ./internal/fleetclient/ ./test/daemon/ -count=1 -timeout 15m`.

**Mutation check:** make `actionError` drop the body's sentence;
`TestARefusalCarriesTheBridgesSentence` must fail. Restore.

## Task 4 — answering from the fleet

**Files:** `internal/app/tui/fleet/model.go`, `keys.go`, `view.go`, a new
`answer.go`, and a new `answer_test.go`.

1. Add the model's Contracts fields. Every action runs as a command calling
   the fleet with a 30s context, and comes back as an `actionDoneMsg`.
2. `actionDoneMsg` with an error sets the status line for 4s, with the
   model's clock, and adds an Events line, "<verb> <name> failed: <sentence>".
   `ErrSettled` is the line "<name>: already answered" and nothing on the
   status line. The footer draws the status line in place of the hints while
   it lasts.
3. Decision 4's inline approval, drawn by `activityLines` under the agent's
   status when `answer` names the shown agent. The text is
   `describePending`'s, plus the risk and the reason when present.
4. Decision 5's question modal in `answer.go`, drawn over the right panel.
5. Decision 9's refusal for both.
6. Tests in `answer_test.go`, each over the fake fleet:
   - `TestYesApprovesTheAgentsRequest` and `TestNoDeniesIt`: `a`, then `y` or
     `n`, call `AnswerPermission` with the pending request's ID and the right
     decision, and close the prompt.
   - `TestNOutsideTheApprovalIsSpawn`: with no approval open, `n` opens the
     spawn modal and answers nothing.
   - `TestAnAccessRequestIsNotAnsweredHere`: an approval with `modeAccess`
     shows the sentence and ignores `y`.
   - `TestTheQuestionModalAnswersInOrder`: a single and a multi question are
     answered with the chosen labels, in order.
   - `TestAQuestionWithoutOptionsTakesText`: the typed answer is sent.
   - `TestDDeclinesTheQuestion`: `d` sends a declined answer.
   - `TestAFailedAnswerShowsForFourSeconds`: a `FleetError` puts its sentence
     on the status line and in Events, and the line clears after 4s of the
     model's clock.
   - `TestAnAnswerSettledElsewhereSaysSo`: `ErrSettled` adds the
     already-answered line and sets no status.
   - `TestActionsAreRefusedWhileDown`: with the connection down, `a` sets the
     status line and calls nothing.

**Verify:** `TMPDIR=/private/tmp/p5-5a-suite CGO_ENABLED=1 go test ./internal/app/tui/fleet/ -count=1`.

## Task 5 — spawn, cancel and intake

**Files:** `internal/app/tui/fleet/keys.go`, `view.go`, a new `spawn.go`, a new
`intake.go`, and a new `actions_test.go`.

1. Decision 6's spawn modal in `spawn.go`. Its projects are
   `State().Projects`, available ones only, starting on the selected agent's
   project when it has one. With no available project it says "add a project
   from the web UI or `marshal daemon pin`" and spawns nothing.
2. Decision 7's cancel, on the selected agent when its status is running.
   Anywhere else `x` does nothing.
3. Decision 8's intake prompt in `intake.go`.
4. The help screen (`helpScreen`) and the footer's hints gain `a`, `n`, `x`
   and `⏎` on an intake item, each only where it applies.
5. Tests in `actions_test.go`:
   - `TestTheSpawnModalSpawnsWithItsFields`: project, name, mode and prompt
     reach `Spawn`, and the new agent is selected when the next state names
     it.
   - `TestASpawnWarningGoesToEvents`.
   - `TestCtrlRCyclesRecentPrompts`: two presses walk the fake's prompts,
     newest first.
   - `TestTheSpawnModalNeedsAProject`: with no available project it says
     decision-6's sentence on `⏎`.
   - `TestXCancelsARunningAgent` and `TestXDoesNothingOnAnIdleAgent`.
   - `TestEnterOnAnIntakeItemApprovesOrDenies`: `⏎` then `y` calls
     `ApproveIntake`; `⏎` then `n` calls `DenyIntake`.

**Verify:** Task 4's command.

## Task 6 — the goldens

**Files:** `internal/app/tui/fleet/view_test.go`, new goldens in
`internal/app/tui/fleet/testdata`.

1. Add four goldens at 100×30, written and compared as the existing ones
   are: `fleet-100x30-approval` (the inline approval), `fleet-100x30-question`
   (the question modal with a multi question), `fleet-100x30-spawn` (the spawn
   modal on its prompt field) and `fleet-100x30-status` (a failed action's
   status line).
2. Add the same four at 80×24 under `NO_COLOR`, so the modals' narrowest
   layout is pinned: each suffixed `-nocolor` and at `80x24`.
3. `TestColourAddsNoText` covers the new frames.

**Verify:** Task 4's command, then the same with `-run TestTheFleetScreenMatchesItsGoldens -update`
only if a golden is new, and never to make a changed one pass.

## Task 7 — the docs, the record and full verification

**Files:** `docs/daemon/p5-fleet-screen.md`, `AGENTS.md`.

1. Fill the "Plan 5a" record, as plan 3's is written.
2. `AGENTS.md`'s `internal/app/tui/fleet/` line says the screen answers,
   spawns, cancels and handles intake.
3. **The manual run.** Against a real local daemon with a real model, as plan
   4a's run did: spawn from the modal, answer an approval inline, answer a
   question if the model asks one, cancel a turn, and approve an intake
   submission if one can be raised. Record what each step showed, and what
   could not be driven and why.
4. Full verification: `gofmt -l internal cmd test web` prints nothing; `go vet
   ./...` is clean; CI's suite command with `TMPDIR=/private/tmp/p5-5a-suite`
   passes; `cd web/ui && npm run check && npm test && npm run build`;
   `CGO_ENABLED=1 go build -o marshal ./cmd/marshal`.

## Done when

- The fleet screen answers approvals and questions, spawns, cancels and
  approves or denies intake, each through `acpclient.Fleet`.
- A refusal shows its sentence on the status line for 4s and in Events, and
  every action is refused while the connection is down.
- The route table names every action's route, and the route test proves each
  is served.
- Every test named above passes, the mutation check fails as described, and
  CI's suite command passes.
