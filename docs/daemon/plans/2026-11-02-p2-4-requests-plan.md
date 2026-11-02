# P2 plan 4: agent requests — implementation plan

Date: 2026-11-02. Status: ready for review.
Base: `05a0e4a6`, plan 3d's merge (`p2-3d-tree`, squashed onto `main`). Written while plan 3d ran.
- Anchors were first checked at `e866c676` (plan 3c merged, plus the plan 3d docs), read together with plan 3d's text for the files 3d changes. They were re-checked at `05a0e4a6` against 3d's merged code: every named function, test and helper exists with the signature this plan assumes, and every line number cited still holds. Line numbers are given only for files 3d left alone. Everywhere else the function names are the anchors.
- The JSON literals in Contracts were produced by encoding the declared types, not written by hand.

Approved design: [p2-client-seam.md](../p2-client-seam.md): C1 (the boundary), C5 (status: sandbox info), C7 (agent requests), Migration order item 4 and its revision record entry "2026-11-02, against plan 3d (written while it ran)", and tests T1, T1b, T2, T4, T5 and T7.
Previous plan: [plan 3d, the tree](2026-11-02-p2-3d-tree-plan.md).
Authoring workflow: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md).

**How this plan is written.** No prototype was built, and the plan embeds no implementation code. The Contracts section holds declarations and the exact strings the tests pin. Every other step states its decision in prose. Executing the plan also tests how well Marshal follows a plan. "As `X` does" means `X` exists and is the model to copy.

**Goal:** the TUI reads every open request through `Session.Pending` and answers it through the session. The policy the TUI applied on an answer moves into the in-process adapter. Nothing a user sees changes, apart from the edge cases in decision 12.
- `acpwire` gains the request types (approval, question, skill gate, sudo prompt, access request) and their answers, plus `Sandbox`, a new `SessionStatus` field.
- `acpclient.Session` gains `Pending`, `AnswerApproval`, `AnswerQuestion`, `AnswerSkillGate`, `AnswerSudo`, `ReconcileAccess`, `AnswerAccess` and `Rollback`.
- The adapter answers them over the state. It derives an approval's "why" line, tool label and forced class. It saves an "always" rule and adds a session rule. It records a skill gate's remembered choice, and it logs a rollback's audit row.
- The TUI's approval, question, skill-gate, sudo and access surfaces take wire values, and they match a request by its key, not by a pointer. `tui.WithAccessRequests` goes: the adapter's `Deps` carry the runtime's access seam.
- T1b gains 19 names. T1's allowlist loses ten entries across nine files, and six keys with them (decision 13).

**Non-goals:**
- **The SDD human gate stays** until plan 5. Answering it resumes the pipeline run through `startAgentRun`, which the TUI owns until `Prompt` and `StartSDD` exist. `Status().SDDGate` (plan 2b) keeps carrying it, and `gatepanel`'s answer and stop handlers are unchanged (decision 11).
- **Shutdown stays.** `finishQuit`'s `ResolvePendingForShutdown` call moves with `Close` (plan 5).
- **Notes stay.** The system notes the request paths write (`m.state.AddMessage`) stay until plan 6.
- **No event subscription.** A request still reaches the screen on the next update or tick, as it does today (decision 3).
- **No ACP behaviour change.** ACP's `PermissionRequest`, `QuestionRequest` and their bridges stay in `internal/acp` (decision 10).
- **No web change.**
- **No changelog entry.**

**Assumptions:**
- Every Go test command below runs with `TMPDIR=/private/tmp/p2-4-suite`. Create the directory first: `mkdir -p /private/tmp/p2-4-suite`. Keep the path short: a long one breaks the unix-socket tests in `internal/acp` and `web/bridge`.
- **The base is green.** The full suite passed at `05a0e4a6` (117 packages `ok`, no failures). A failure during this plan is new, unless it also fails there, or it is a known load flake (Task 9) that passes on its own with `-count=3`.
- **T6's goldens never change.** No task runs `TestTranscriptGoldens` with `-update`. Every TUI task's Verify runs the `tui` package, which runs the goldens.
- **Only approvals come from subagents.** Children never get `question.ask` (`agent/subagent.go:139`), a child's state is never secret-prompt capable (`app.go:3006` sets it on the root state only), and the TUI and ACP both collect only a running child's approval (`pendingApprovalTarget`; `acp/child_approvals.go`). So `Pending` lists the session's own requests of every kind, plus its running subagents' approvals.
- **Production has one unregistered prompt.** `requestSubagentConsent` (`agent/subagent.go:643`) sets an approval with no registry key (`SetPendingApproval`). Many TUI tests do the same for every kind. The adapter must answer both forms.

**Decisions this plan makes beyond the spec** (decisions 1 to 11 are also recorded in the spec's revision record):

1. **A request's key is its registry ID.** For a registered prompt that is `PromptKey.ID`. Work items already name a prompt by that ID (`runtime.go:3741-3751`), and the TUI matches them with it (`promptKeyByID`). Two other cases need their own key:
   - a typed access request sits in the approval slot with no registry key. Its key is its `Access.RequestID`, which the registry mirror already uses (`prompts.go`, `mirrorAccessPromptLocked`);
   - an unregistered prompt gets a key the adapter mints, `local-1`, `local-2` and so on. The adapter keeps it while the prompt stays open and forgets it when the prompt leaves. Minted keys cannot collide with registry IDs, which are 26 base32 characters (`rand.Text`).
2. **`Pending` is a struct with one field per kind**, as the state has one slot per kind. Approvals are a list: the session's own first, then each running subagent's, in registration order. That is the order `pendingApprovalTarget` searches today. Only an approval carries an owner (`subagentId` and `subagent`), because only approvals come from subagents (Assumptions).
3. **`Changed` does not fire for requests.** The TUI already re-reads the slots on every update and on the 150 ms tick, and nothing subscribes to the state's event broker today. P3's mirror fires `Changed` when a request message arrives or settles.
4. **The adapter derives what the TUI computed from the owning state.**
   - **The "why" line.** `actor` is the requesting step's owner and `why` is the first sentence of that step's narration, emphasis stripped. Both come from the state that raised the request, as `approvalWhyFor` read them. That is a subagent's own state for its approval, where step IDs collide with the parent's.
   - **The tool label** is `viewmodel.DisplayToolName`, which only the owner line read.
   - **`forced`** is today's `forcedPromptClass` (`approval.go:105`): `shell.sudo`, the `model-cost-consent` risk, an exact-command approval, `mode.request`, or a reason starting `mode-elevation:`. It is computed from the request itself, not the display copy whose reason a subagent prefix changes (decision 12).
5. **A standing answer to a forced approval is a deny**, enforced by the adapter. The form still hides the two choices when `forced` is set, and the adapter's check replaces the TUI's guard against a forged choice (`handleApproval`, `choiceAlways` and `choiceSessionAllow`).
6. **An "always" or "session" answer lands on the session, whoever asked.** The TUI added the session rule and saved the config rule on `m.state`, even for a subagent's approval, and the adapter does the same on its own state. The settlement itself goes to the owning state's registry.
   - "always" saves to `<home>/.config/marshal/config.toml`, the path the TUI's `userConfigDir` computes. That function moves into the adapter unchanged, F-SEC-27 check included. It does not read `MARSHAL_CONFIG_DIR` or `XDG_CONFIG_HOME`, and this plan keeps that.
   - A failed save falls back to a session rule. A successful save applies the new config through `RebaseInteractionConfig`. The policy engine reads the session's live config (`State.PolicySnapshot`), so no runner state is reset. The spec's T5 example is corrected to match.
   - The TUI's own half of `applyNewConfig` (the `/set` registry, completion popups and theme) becomes `configViewsChanged`. The TUI runs it after an "always" answer succeeds.
7. **Access requests keep the runtime's seam, and `Pending` stays a pure read.** `inproc.Deps` gains `AccessRequest` and `ResolveAccess`, bound to `Runtime.AccessRequest` and `Runtime.ResolveAccess` as `accessTUIOptions` binds them today. The TUI reads the access request two ways today, and the seam keeps both:
   - **`Pending.access` is the state's typed request**, read without side effects. It replaces the plain state reads the status line and `Update`'s key guard make (`accessRequestQueued`, and the block that consumes keys while a request is queued). `Pending` never settles anything: the TUI reads it while rendering, and a request settled between the height budget and the drawing would break the frame (`TestFrameInvariants` renders a queued access request without an update).
   - **`ReconcileAccess(ctx)` is the runtime read** that `reconcileAccess` makes once per update. `Runtime.AccessRequest` settles a request that no longer asks for anything. The adapter keeps its answer only when its session ID is the session's: the TUI's foreign-snapshot guard moves into the adapter (C4: a session handed out before a swap never reaches its replacement). Without both callbacks, `ReconcileAccess` settles the state's typed request as failed, with the message "Access resolver unavailable", and reports none. That is `reconcileAccess`'s fail-closed branch, moved.
   - `ReconcileAccess` is new to the spec. Over ACP the agent settles such a request itself, so P3's implementation only reads the mirror: its ACP line is "none".
   - `AnswerAccess` takes the choice, which already names its request and session. No separate key is needed, and the spec's `CancelAccess` is not added: the TUI only ever cancelled on the fail-closed path, which `ReconcileAccess` now owns.
8. **The sandbox info lives in `Status`.** `Status.sandbox` holds the backend, its network isolation, and the shell's configured network allowance (`tools.shell.allow_network`). The isolation line weighs all three, and the running-call row and the approval panel read them together.
   - A drilled view still shows no isolation line. Only the root state records sandbox info (`app.go:934`). A child's commands do run through the conversation's command service, so the session's sandbox would be accurate there, but showing it is a visible change this plan does not make (plan 3d decision 7).
   - `acpwire.SheetSandbox` (plan 2c) stays the sheet's and is not reused. It has no network isolation, which the isolation line needs, and the sheet is re-read only at its refresh points, while these renderers read every frame.
9. **The TUI reads `Pending` once per function**, or once per branch chain. A function that tests several kinds in one expression reads `m.sess.Pending()` into a local first, as plan 2a's rule did for `Status`.
10. **ACP's request types stay in `internal/acp`.** C1 lists `PermissionRequest` among the types that move. Nothing on the TUI side reads it, and its `ModeAccessState` encodes `system_access` and `access_revision`, which break C3's camelCase rule. P3 extends `session/request_permission` with the decision and the request kind, and decides then what crosses. This plan's `acpwire` request types are new, and they serve `Pending`.
11. **The SDD gate's answer moves with plan 5** (Non-goals). C7's `AnswerSDDGate` moves to C8.
12. **What a user could see differ.** Every case is rare.
    - **A subagent's approval can be reached by its key.** Alt+Enter's composer now tracks it as the prompt it displaced. A work item that names it now navigates to it, where today the TUI said "That request is no longer pending." Today the display copy dropped the key.
    - **The model-cost consent has a key** (a minted one), so the composer tracks it too.
    - **A subagent's mode-elevation `file.write` approval no longer offers "always" or "session".** The subagent prefix hid the `mode-elevation:` reason from the form's check. Both choices were denied anyway.
    - **A new request rebuilds the approval form even when it reuses a tool-call ID.** The form is keyed by the request now. Two model-cost consents in a row both use the ID `subagent-model-consent`, and the second used to keep the first one's form and reason.
13. **The allowlist.** T1 fails on a stale entry, so each task trims its own:
    - Task 3 deletes the `accesspanel/accesspanel.go` key;
    - Task 5 deletes the `question.go`, `skillgate.go` and `sudoauth.go` keys;
    - Task 7 removes `marshal/internal/tools/registry` from `keypress.go`'s entry;
    - Task 8 deletes the `approval.go` and `toolnames.go` keys, removes `marshal/internal/app/session` from `steps.go`'s entry, and removes `config.SaveUserConfigRule` and `marshal/internal/permissions` from `model.go`'s.

**Execution:**
- Branch first: `git switch -c p2-4-requests` from `main`, at or after `05a0e4a6` (plan 3d's merge).
- If `git status` shows `docs/daemon/p2-client-seam.md` or this plan uncommitted, commit them first as explicit paths, with the message `p2-4-requests: record the plan 4 revisions and plan 4`. The main checkout holds other untracked files (`docs/fleet-cli/` and others). Leave them alone.
- Run the tasks in order. Run each task's Verify and commit it as `p2-4-requests: task N — <title>`.
- Stage explicit paths only. Never `git add -A` without a path.
- Run `gofmt -w` on every Go file a task touches before its Verify.

## Contracts

**`internal/acpwire`, after Task 1.**

```go
// requests.go (new)

// PendingRequests is every open request a person can answer (C7): the
// session's own, and the approvals of its running subagents. A request's Key
// stays the same while it is open, and a settled request leaves.
type PendingRequests struct {
	// Approvals are the session's own approval first, when it has one, then
	// the approval of each running subagent that has one, in the order the
	// subagents were registered.
	Approvals []ApprovalRequest `json:"approvals,omitempty"`
	Question  *QuestionRequest  `json:"question,omitempty"`
	SkillGate *SkillGateRequest `json:"skillGate,omitempty"`
	Sudo      *SudoRequest      `json:"sudo,omitempty"`
	Access    *AccessRequest    `json:"access,omitempty"`
}

// ApprovalRequest is one tool call waiting for a person's approval.
type ApprovalRequest struct {
	Key        string `json:"key"`
	SubagentID int64  `json:"subagentId,omitempty"` // 0 when the session itself asks
	Subagent   string `json:"subagent,omitempty"`   // the subagent's label
	ToolCallID string `json:"toolCallId,omitempty"`
	Tool       string `json:"tool"`
	ToolLabel  string `json:"toolLabel,omitempty"` // the tool's display name
	Command    string `json:"command,omitempty"`
	Args       string `json:"args,omitempty"`
	Risk       string `json:"risk,omitempty"`
	Reason     string `json:"reason,omitempty"`
	Diff       string `json:"diff,omitempty"`
	Schema     string `json:"schema,omitempty"`
	// Forced marks a prompt only an exact decision may answer: a session or
	// always answer to it is a deny.
	Forced bool `json:"forced,omitempty"`
	// Access marks a typed access request occupying the approval slot. The
	// access panel answers it, through Pending's Access and AnswerAccess.
	Access bool   `json:"access,omitempty"`
	Actor  string `json:"actor,omitempty"` // the requesting step's owner; "" for the orchestrator
	Why    string `json:"why,omitempty"`   // the first sentence of that step's narration
}

// ApprovalDecision is how a person answered an approval.
type ApprovalDecision string

const (
	ApprovalOnce    ApprovalDecision = "once"
	ApprovalSession ApprovalDecision = "session"
	ApprovalAlways  ApprovalDecision = "always"
	ApprovalDeny    ApprovalDecision = "deny"
)

// ApprovalAnswer is a person's answer to an approval.
type ApprovalAnswer struct {
	Decision ApprovalDecision `json:"decision"`
	Edited   string           `json:"edited,omitempty"` // a once answer's edited command or arguments
}

// QuestionRequest is the agent's clarifying questions, asked together.
type QuestionRequest struct {
	Key       string     `json:"key"`
	Questions []Question `json:"questions"`
}

type Question struct {
	Question string           `json:"question"`
	Options  []QuestionOption `json:"options,omitempty"`
	Multi    bool             `json:"multi,omitempty"`
}

type QuestionOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// Answer is a person's answer to one Question, in the questions' order.
type Answer struct {
	Question string `json:"question"`
	Answer   string `json:"answer"`
}

// AnswerUnanswered is the answer recorded for a question a person skipped.
// It equals session.AnswerUnanswered, which the runner tests for.
const AnswerUnanswered = "Unanswered"

// SkillGateRequest asks whether a skill may load while the context window is
// small.
type SkillGateRequest struct {
	Key         string `json:"key"`
	Skill       string `json:"skill"`
	Description string `json:"description,omitempty"`
	Reason      string `json:"reason,omitempty"`
}

// SkillGateChoice is how a person answered a skill gate.
type SkillGateChoice string

const (
	SkillGateAllowOnce  SkillGateChoice = "allow_once"
	SkillGateAllowSkill SkillGateChoice = "allow_skill" // no more prompts for this skill this session
	SkillGateAllowAll   SkillGateChoice = "allow_all"   // the gate is off for this session
	SkillGateDeny       SkillGateChoice = "deny"
)

// SudoRequest asks for the password a privileged command needs.
type SudoRequest struct {
	Key             string `json:"key"`
	Command         string `json:"command,omitempty"`
	Reason          string `json:"reason,omitempty"`
	StepID          int64  `json:"stepId,omitempty"`
	AllowUnlock     bool   `json:"allowUnlock,omitempty"`
	Error           string `json:"error,omitempty"` // the previous attempt's failure; "" on the first
	CommandApproval bool   `json:"commandApproval,omitempty"`
}

// SudoAnswer is a person's answer to a sudo prompt. Password travels only
// here, and the handler zeroes it after use. OK false is a deny, with no
// password.
type SudoAnswer struct {
	OK       bool   `json:"ok"`
	Password []byte `json:"password,omitempty"`
}

// AccessState is the access tuple an access request was raised against. Mode
// keeps the session's spelling, so a choice that echoes it compares equal.
type AccessState struct {
	Mode     string `json:"mode"`
	Auto     bool   `json:"auto,omitempty"`
	System   bool   `json:"system,omitempty"`
	Revision uint64 `json:"revision,omitempty"`
}

// AccessRequest asks a person to grant editing, system access or both.
type AccessRequest struct {
	RequestID      string      `json:"requestId"`
	SessionID      string      `json:"sessionId"`
	Actor          string      `json:"actor,omitempty"`
	StepID         int64       `json:"stepId,omitempty"`
	Reason         string      `json:"reason"`
	Current        AccessState `json:"current"`
	MissingEditing bool        `json:"missingEditing"`
	MissingSystem  bool        `json:"missingSystem"`
}

// AccessChoice is a person's answer to an AccessRequest. Current is the tuple
// the panel showed: a stale one makes the result refresh_required.
type AccessChoice struct {
	RequestID string      `json:"requestId"`
	SessionID string      `json:"sessionId"`
	Current   AccessState `json:"current"`
	Approved  bool        `json:"approved"`
	Mode      string      `json:"mode,omitempty"`
}

// AccessResult is how an access request settled, and the tuple in force after.
type AccessResult struct {
	RequestID string      `json:"requestId,omitempty"`
	SessionID string      `json:"sessionId"`
	Status    string      `json:"status"` // an Access* value
	Effective AccessState `json:"effective"`
	Message   string      `json:"message,omitempty"`
}

// AccessResult.Status values. They equal session.AccessStatus's.
const (
	AccessAvailable = "already_available"
	AccessGranted   = "granted"
	AccessDenied    = "denied"
	AccessCanceled  = "canceled"
	AccessFailed    = "failed"
	AccessRefresh   = "refresh_required"
	AccessExpired   = "expired"
)

// status.go: SessionStatus gains, after Browser:

	Sandbox Sandbox `json:"sandbox"`

// status.go, after Browser:

// Sandbox is the shell sandbox a running shell row and the approval panel
// describe (C5): the backend, whether it isolates the network, and the
// shell's configured network allowance, which the isolation line weighs.
type Sandbox struct {
	Backend          string `json:"backend,omitempty"`
	NetworkIsolation bool   `json:"networkIsolation,omitempty"`
	AllowNetwork     bool   `json:"allowNetwork,omitempty"` // tools.shell.allow_network
}
```

The JSON `TestRequestsJSON` pins:
- a `PendingRequests` with every field set, built from the values listed in Task 1's test step:

```json
{"approvals":[{"key":"K1","toolCallId":"call-1","tool":"shell.run","toolLabel":"Run command","command":"go test ./...","risk":"command","reason":"policy: ask","actor":"implementer","why":"Running the tests."},{"key":"K2","subagentId":3,"subagent":"scout","tool":"file.edit","toolLabel":"Edit file","args":"{\"path\":\"a.go\"}","diff":"@@ -1 +1 @@","schema":"Edit a file","forced":true}],"question":{"key":"K3","questions":[{"question":"Which?","options":[{"label":"A","description":"first"},{"label":"B"}],"multi":true}]},"skillGate":{"key":"K4","skill":"tdd","description":"Test first","reason":"Loading skills consumes context."},"sudo":{"key":"K5","command":"apt install jq","reason":"needs root","stepId":7,"allowUnlock":true,"error":"Sorry, try again.","commandApproval":true},"access":{"requestId":"R1","sessionId":"s1","actor":"orchestrator","stepId":2,"reason":"read outside","current":{"mode":"ask","revision":4},"missingEditing":false,"missingSystem":true}}
```

- the empty `PendingRequests`: `{}`;
- `SudoAnswer{OK: true, Password: []byte("hunter2")}`: `{"ok":true,"password":"aHVudGVyMg=="}`;
- `ApprovalAnswer{Decision: ApprovalOnce, Edited: "go test ./internal/..."}`: `{"decision":"once","edited":"go test ./internal/..."}`;
- `Sandbox{Backend: "container", NetworkIsolation: true}`: `{"backend":"container","networkIsolation":true}`.

**`internal/acpclient`, after Tasks 1 and 2.** `Session` gains, after `Callers`, in this order. Task 1 adds `Pending`, and Task 2 the rest.

```go
	// Pending lists every open request a person can answer (C7): the
	// session's own, and the approvals of its running subagents. Pending does
	// not block and settles nothing, so it is safe to read while rendering.
	// Changed does not fire when a request opens or settles: the TUI reads
	// Pending on every update and tick.
	//
	// ACP: session/request_permission (new in P3 on the client side). The
	// agent already sends it and session/request_question, and P3 adds
	// session/request_secret. P3's mirror keeps each request until it is
	// answered or an update settles it.
	Pending() acpwire.PendingRequests

	// AnswerApproval answers the approval with that key. A session or always
	// answer to a Forced approval is a deny. always saves an allow rule to the
	// user config and applies it to the session's config, or adds a session
	// rule when the save fails; session adds a session rule. Both rules land
	// on the session, whichever subagent asked. A key that names no open
	// approval returns CodeRequestSettled.
	//
	// ACP: session/request_permission (new in P3 on the client side): the
	// answer is that request's result, extended with the decision.
	AnswerApproval(ctx context.Context, key string, answer acpwire.ApprovalAnswer) error

	// AnswerQuestion answers the question with that key, one answer per
	// question in order. A key that names no open question returns
	// CodeRequestSettled.
	//
	// ACP: session/request_question (new in P3 on the client side).
	AnswerQuestion(ctx context.Context, key string, answers []acpwire.Answer) error

	// AnswerSkillGate answers the skill gate with that key, and records an
	// allow_skill or allow_all choice for the rest of the session. A key that
	// names no open gate returns CodeRequestSettled.
	//
	// ACP: session/request_permission (new in P3 on the client side), whose
	// request carries the skill-gate kind.
	AnswerSkillGate(ctx context.Context, key string, choice acpwire.SkillGateChoice) error

	// AnswerSudo answers the sudo prompt with that key. The password is
	// handed to the handler as the same slice, and the handler zeroes it; it
	// is never logged, nor returned by any read. A key that names no open
	// prompt returns CodeRequestSettled.
	//
	// ACP: session/request_secret (new in P3).
	AnswerSudo(ctx context.Context, key string, answer acpwire.SudoAnswer) error

	// ReconcileAccess settles the typed access request that no longer asks
	// for anything, then returns the one the access panel should present, if
	// any. The TUI calls it once per update, so a request surfaces and leaves
	// without a keypress. An implementation that cannot answer access
	// requests settles each one as failed and reports none.
	//
	// ACP: none (in P3 the agent settles such a request itself, and this
	// reads the request from the mirror).
	ReconcileAccess(ctx context.Context) (acpwire.AccessRequest, bool)

	// AnswerAccess answers the access request the choice names. The result
	// says how it settled; refresh_required means the tuple moved while the
	// person decided, and the request stays open.
	//
	// ACP: session/request_permission (new in P3 on the client side), with
	// the typed access choice.
	AnswerAccess(ctx context.Context, choice acpwire.AccessChoice) (acpwire.AccessResult, error)

	// Rollback restores the last backup and logs the rollback as an audit
	// row in the transcript, failed or not. It returns the restore's error.
	//
	// ACP: session/rollback (new in P3).
	Rollback(ctx context.Context) error
```

`Methods` gains these rows after the `Callers` row, all with `Exists` false:

| Name | ACP |
|---|---|
| `Pending` | `session/request_permission` |
| `AnswerApproval` | `session/request_permission` |
| `AnswerQuestion` | `session/request_question` |
| `AnswerSkillGate` | `session/request_permission` |
| `AnswerSudo` | `session/request_secret` |
| `ReconcileAccess` | "" (none) |
| `AnswerAccess` | `session/request_permission` |
| `Rollback` | `session/rollback` |

**`internal/acpclient/inproc`, after Tasks 1 and 2.**

```go
// inproc.go: Session gains, with the other fields (Task 1):

	// localKeys holds the minted key of each open unregistered prompt, by the
	// prompt's pointer; localSeq numbers them. Both are guarded by mu.
	localKeys map[any]string
	localSeq  uint64

// inproc.go: Deps gains, after ReferenceFinder (Task 2):

	// AccessRequest and ResolveAccess are the runtime's access seam:
	// AccessRequest returns the typed access request waiting for a decision,
	// settling one that asks for nothing, and ResolveAccess validates and
	// settles a choice. Without both, ReconcileAccess settles every typed
	// access request as failed instead of presenting it.
	AccessRequest func() (session.AccessRequest, bool)
	ResolveAccess func(session.AccessChoice) (session.AccessResult, error)

// inproc.go: Session gains (Task 2):

	accessRequest func() (session.AccessRequest, bool)
	resolveAccess func(session.AccessChoice) (session.AccessResult, error)

// requests.go (new)

func (s *Session) Pending() acpwire.PendingRequests

// openRequests is what Pending reads, with the state and prompt each request
// answers to. The answer calls find a request by its key here.
type openRequests struct {
	approvals []openApproval
	question  *session.PendingQuestion
	skill     *session.PendingSkillGate
	sudo      *session.PendingSudoAuth
	wire      acpwire.PendingRequests
}

type openApproval struct {
	owner *session.State // the state that raised it: the session's, or a subagent's
	call  *session.PendingToolCall
}

// open reads every open request. It is Pending's body, and it refreshes the
// minted keys.
func (s *Session) open() openRequests

// promptKey is a prompt's wire key: its registry ID when it has one, else the
// key minted for that pointer. seen collects the pointers open read, so the
// keys of prompts that left can be dropped.
func (s *Session) promptKey(prompt any, key session.PromptKey, seen map[any]string) string

// forcedApproval reports whether only an exact decision may answer tc
// (decision 4).
func forcedApproval(tc *session.PendingToolCall) bool

// approvalWhy is the owner and the narration headline of the step that asked
// for tc, read from the state that raised it.
func approvalWhy(owner *session.State, tc *session.PendingToolCall) (actor, why string)

// answers.go (new)

func (s *Session) AnswerApproval(ctx context.Context, key string, answer acpwire.ApprovalAnswer) error
func (s *Session) AnswerQuestion(ctx context.Context, key string, answers []acpwire.Answer) error
func (s *Session) AnswerSkillGate(ctx context.Context, key string, choice acpwire.SkillGateChoice) error
func (s *Session) AnswerSudo(ctx context.Context, key string, answer acpwire.SudoAnswer) error
func (s *Session) ReconcileAccess(ctx context.Context) (acpwire.AccessRequest, bool)
func (s *Session) AnswerAccess(ctx context.Context, choice acpwire.AccessChoice) (acpwire.AccessResult, error)
func (s *Session) Rollback(ctx context.Context) error

// errRequestSettled is what an answer to a request that is no longer open
// returns (C7).
var errRequestSettled = &acpwire.Error{
	Code:    acpwire.CodeRequestSettled,
	Message: "inproc: the request is no longer open",
}

// userConfigDir moves here unchanged from tui/model.go (decision 6).
func userConfigDir() string
```

**`internal/app/tui`, after Task 3.**

```go
// accesspanel/accesspanel.go
type ChoiceMsg struct{ Choice acpwire.AccessChoice }
func New(request acpwire.AccessRequest) *Panel
func (p *Panel) Request() acpwire.AccessRequest

// model.go
func (m *Model) accessRequestSnapshot() (acpwire.AccessRequest, bool)
func accessOutcomeNote(result acpwire.AccessResult) string
```

`tui.WithAccessRequests` and the `accessRequest` and `resolveAccess` fields are deleted.

**`internal/app/tui`, after Task 4.**

```go
// promptkey.go (new). It imports nothing.

// promptKind is the kind of request a promptRef names.
type promptKind int

const (
	promptApproval promptKind = iota
	promptQuestion
	promptSkill
	promptSudo
)

// promptRef names one open request: its kind and its wire key. The zero
// value names none.
type promptRef struct {
	kind promptKind
	key  string
}

// model.go
	composerDisplacedPrompt promptRef // Model field, replacing session.PromptKey

func (m *Model) focusedPromptKey() (promptRef, bool)
func (m *Model) promptStillPending(ref promptRef) bool
func (m Model) promptKeyByID(id string) (promptRef, bool)
func (m *Model) retireStalePromptModels(ref promptRef)
```

**`internal/app/tui`, after Task 5.**

```go
// question.go
func newQuestionModel(q acpwire.QuestionRequest, width int) *questionModel
func (qm *questionModel) Answers() []acpwire.Answer

// unansweredAnswers is one AnswerUnanswered answer per question, in order.
func unansweredAnswers(questions []acpwire.Question) []acpwire.Answer

// skillgate.go
func newSkillGateModel(sg acpwire.SkillGateRequest, width int) *skillGateModel
func (m *skillGateModel) Choice() acpwire.SkillGateChoice
func renderSkillGatePanel(sg acpwire.SkillGateRequest, width int) string

// sudoauth.go
func newSudoAuthModel(sa acpwire.SudoRequest, width int) *sudoAuthModel
func (m *sudoAuthModel) Decision() acpwire.SudoAnswer
func renderSudoAuthPanel(sa acpwire.SudoRequest, width int) string

// transcript.go
func renderQuestionPanel(q acpwire.QuestionRequest, width int) string

// model.go
func (m Model) handleQuestion(msg tea.Msg, q acpwire.QuestionRequest) (tea.Model, tea.Cmd)
func (m Model) handleSkillGate(msg tea.Msg, sg acpwire.SkillGateRequest) (tea.Model, tea.Cmd)
func (m Model) handleSudoAuth(msg tea.Msg, sa acpwire.SudoRequest) (tea.Model, tea.Cmd)
```

The models' `q`, `sg` and `sa` fields hold the wire values. A model is bound to a request by the request's `Key`.

**`internal/app/tui`, after Task 6.**

```go
// transcript.go
func sandboxIsolationText(sb acpwire.Sandbox) string
func renderActiveToolCall(t *acpwire.WireTool, sb acpwire.Sandbox, spinnerFrame string, now time.Time, expanded bool, width int) string
func renderApprovalPanel(tc *session.PendingToolCall, sb acpwire.Sandbox, width int, why ...approvalWhy) string

// approval.go
func newApprovalModel(tc *session.PendingToolCall, sb acpwire.Sandbox, hasBackup bool, width int, why ...approvalWhy) *approvalModel
func approvalSummary(tc *session.PendingToolCall, sb acpwire.Sandbox, width int, why ...approvalWhy) string
```

`stepRenderCtx.sandbox`, `viewFacts.sandbox` and `approvalModel.sb` are `acpwire.Sandbox`. Each struct's `allowNetwork` field is deleted.

**`internal/app/tui`, after Task 8.**

```go
// approval.go
func newApprovalModel(a acpwire.ApprovalRequest, sb acpwire.Sandbox, hasBackup bool, width int) *approvalModel
func approvalSummary(a acpwire.ApprovalRequest, sb acpwire.Sandbox, width int) string

// approvalWhyOf is the who-and-why an approval prompt shows, from the wire.
func approvalWhyOf(a acpwire.ApprovalRequest) approvalWhy
func (w approvalWhy) ownerLine(a acpwire.ApprovalRequest) string

// shownApproval is a as a prompt displays it, every model-written field's
// controls made visible (replacing shownToolCall).
func shownApproval(a acpwire.ApprovalRequest) acpwire.ApprovalRequest

// transcript.go
func renderApprovalPanel(a acpwire.ApprovalRequest, sb acpwire.Sandbox, width int) string
func riskText(a acpwire.ApprovalRequest) string

// model.go

// displayApproval is a as the dialog shows it. A subagent's approval has its
// reason prefixed with the subagent's label and its Access mark cleared, as
// the display copy had.
func displayApproval(a acpwire.ApprovalRequest) acpwire.ApprovalRequest

func (m *Model) pendingApprovalTarget() (acpwire.ApprovalRequest, bool)
func (m *Model) pendingApprovalDisplay() (acpwire.ApprovalRequest, bool)
func (m *Model) hasPendingApproval() bool
func (m Model) handleApproval(msg tea.Msg, a acpwire.ApprovalRequest) (tea.Model, tea.Cmd)
func (m *Model) settleApproval(a acpwire.ApprovalRequest, answer acpwire.ApprovalAnswer) error

type approvalEdit struct {
	input textarea.Model
	key   string // the approval's key
}

func newApprovalEdit(a acpwire.ApprovalRequest, width int) *approvalEdit

// configViewsChanged resets what the TUI caches from the config, and reloads
// the theme: applyNewConfig's TUI half.
func (m *Model) configViewsChanged(cfg config.Config)
```

`approvalModel.tc` becomes `a acpwire.ApprovalRequest`, the display copy the form shows.

**Test helpers.**

```go
// tui: answers_seam_test.go (new, Task 5)

// answerRecorder is a session whose answer calls are recorded before the
// wrapped session answers them. Every other method is the wrapped session's.
type answerRecorder struct {
	acpclient.Session
	approvals []recordedApproval
	questions []recordedQuestion
	skills    []recordedSkill
	sudo      []recordedSudo
	rollbacks int
}

type recordedApproval struct {
	key    string
	answer acpwire.ApprovalAnswer
}
type recordedQuestion struct {
	key     string
	answers []acpwire.Answer
}
type recordedSkill struct {
	key    string
	choice acpwire.SkillGateChoice
}
type recordedSudo struct {
	key    string
	answer acpwire.SudoAnswer
}

// testModelRecording builds a model over state whose session is rec,
// wrapped around an adapter session, as testModelStub builds one.
func testModelRecording(state *session.State, rec *answerRecorder, opts ...Option) Model

// app: tui_seam_test.go (Task 3)

// testTUIModelWith builds a model over an adapter made from d.
func testTUIModelWith(d inproc.Deps, opts ...tui.Option) tui.Model
```

## Task 1: The session lists its requests, and Status carries the sandbox

**Goal:** `Session.Pending` returns every open request, with keys, owners, the "why" line, the tool label and the forced class, and settles nothing. `Status` carries the sandbox info. The TUI does not call `Pending` yet.

**Files:** `internal/acpwire/requests.go` (new), `requests_test.go` (new), `status.go`; `internal/acpclient/acpclient.go`, `methods.go`; `internal/acpclient/inproc/inproc.go`, `events.go`, `requests.go` (new), `requests_test.go` (new), `inproc_test.go`.

**Steps:**

1. **`acpwire/requests.go`** (new) holds every request type of the Contracts, the answer types included, which Task 2 uses.
2. **`acpwire/status.go`.** Add `Sandbox` after `Browser`, and the `Sandbox` field to `SessionStatus` after `Browser`. In `SessionStatus`'s doc comment, the sentence naming what later plans add becomes "It arrives over several plans: plan 6 adds the current notice." (Plan 3d left it as "plan 4 adds the sandbox info, and plan 6 the current notice".)
3. **`acpclient.go`.** Add `Pending` after `Callers`, with its Contracts doc comment. **`methods.go`:** add its row after the `Callers` row.
4. **`inproc/inproc.go`:**
   - `Session` gains `localKeys` and `localSeq` (Contracts), and `newSession` (`events.go`) makes `localKeys`;
   - `Status` sets `Sandbox`: the backend and the network isolation from `st.SandboxInfo()`, and `AllowNetwork` from `st.Config.Tools.Shell.AllowNetwork`, the two values the TUI reads today.
5. **`inproc/requests.go`** (new). **`open`** builds the `openRequests` under these rules, and **`Pending`** returns its `wire`.
   - **Approvals.** First the state's `PendingApproval()`, when set, with owner the session's state. Then, for each view of `s.state.Subagents()` in order, whose status is `session.SubagentRunning`, whose `Child` is not nil, and whose child has a `PendingApproval()`: that approval, with owner the child, `SubagentID` the view's ID and `Subagent` its label. This is `pendingApprovalTarget`'s search, every match kept.
   - **An approval's wire value:** its key (below), `ToolCallID` from `ID`, `Tool` from `Name`, and `Command`, `Args`, `Risk`, `Reason`, `Diff` and `Schema` copied. `ToolLabel` is `viewmodel.DisplayToolName(Name)`, `Forced` is `forcedApproval`, `Access` is `call.Access != nil`, and `Actor` and `Why` come from `approvalWhy(owner, call)`.
   - **`approvalWhy`** is `approvalWhyFor` (`tui/steps.go`) moved: nothing when the step ID is 0 or `owner.Step` finds no step. Otherwise the actor is `viewmodel.StepOwner(step)`. The why is the head of `strutil.FirstSentence(owner.StepNarration(id))` with `strutil.StripEmphasis` applied, or "" when the head is empty.
   - **`forcedApproval`** is decision 4's rule, the body of `forcedPromptClass` and `isModeElevationApproval` together: `Name` is `shell.sudo` or `mode.request`, `Risk` is `model-cost-consent`, `ExactCommandApproval` is set, or `Reason` starts with `mode-elevation:`.
   - **The question, skill gate and sudo prompt** are the state's `PendingQuestion()`, `PendingSkillGate()` and `PendingSudoAuth()`, each converted field for field into its wire type with its key. `SudoRequest` carries no `Attempt`, which nothing reads.
   - **Keys** (decision 1). `promptKey` returns `key.ID` when it is not empty. A typed access approval's key is `call.Access.RequestID`, which `open` passes in as the key. Otherwise `promptKey` looks the pointer up in `s.localKeys`. When it finds none, it mints one: it increments `localSeq` and forms `fmt.Sprintf("local-%d", s.localSeq)`. Every minted key it returns is recorded in `seen`. At the end of `open`, `s.localKeys` becomes `seen`, so a prompt that left loses its key.
   - **Locking.** `open` reads every slot from the state first, holding no adapter lock. It then takes `mu` once, to assign the keys and replace `localKeys`.
   - **Access** (decision 7) is `s.state.AccessRequest()` in wire form, when it reports a request. `StepID` converts to `int64`, and `Current` copies its four fields. `Pending` never settles it, and never calls a runtime callback.
   - A closed session still answers `Pending`, as it answers `Status`.
6. **Tests.**
   - **`acpwire/requests_test.go`** (new): **`TestRequestsJSON`** encodes five values and compares the bytes with the Contracts literals, in the table style of `TestSessionSheetJSON`:
     - the full `PendingRequests`: approval K1 (tool call `call-1`, `shell.run`, label "Run command", command "go test ./...", risk "command", reason "policy: ask", actor "implementer", why "Running the tests."); approval K2 (subagent 3 "scout", `file.edit`, label "Edit file", args `{"path":"a.go"}`, diff "@@ -1 +1 @@", schema "Edit a file", forced); question K3 ("Which?", options A "first" and B, multi); skill gate K4 ("tdd", "Test first", "Loading skills consumes context."); sudo K5 ("apt install jq", "needs root", step 7, unlock allowed, error "Sorry, try again.", command approval); access R1 (session "s1", actor "orchestrator", step 2, reason "read outside", current mode "ask" at revision 4, system missing);
     - the empty `PendingRequests`, the `SudoAnswer`, the `ApprovalAnswer` and the `Sandbox` of the Contracts.
   - **`inproc/inproc_test.go`.** In `TestStatusMirrorsTheState`, the state gains `SetSandboxInfo(session.SandboxInfo{Backend: "container", NetworkIsolation: true})`, and the want gains `Sandbox: acpwire.Sandbox{Backend: "container", NetworkIsolation: true}` (`config.Default()` leaves `AllowNetwork` false).
   - **`inproc/requests_test.go`** (new). Each test builds its state with `newState(t, "s1")`, and registers prompts with `context.Background()` and a buffered `ResponseChan`.
     - **`TestPendingListsTheSessionsRequests`.** The state registers an approval (`RegisterApproval`: ID "call-1", `shell.run`, "go test ./...", risk "command", reason "policy: ask"), a question (`RegisterQuestion`: "Which?" with options A "first" and B, multi), a skill gate (`RegisterSkillGate`: "tdd", "Test first", "Loading skills consumes context.") and a sudo prompt (`RegisterSudoAuth`: "apt install jq", "needs root", step 7, unlock, error, command approval). `Pending()` equals the wire value built from those fields, each key the `PromptKey.ID` its registration returned. The approval's label is "Run command", with no owner, not forced, and no actor or why.
     - **`TestPendingAddsTheRunningSubagentsApprovals`.** The state registers its own approval. Child `a` (label "a") is registered and registers an approval. Child `b` (label "b") is registered, registers an approval, and is finished (`FinishSubagent(b.ID, "", nil)`). Child `c` is registered with none. `Approvals` holds the session's approval and `a`'s, in that order, `a`'s with `SubagentID` `a.ID` and `Subagent` "a".
     - **`TestPendingApprovalCarriesItsStepsOwnerAndWhy`.** Moved from `tui/steps_test.go`'s `TestApprovalShowsOwnerAndWhy`, its derivation half:
       - a step by `session.Actor{Role: "sdd_implementer", Label: "implementer"}` narrated "Installing the dependency the build needs. It is pinned." asks for a `shell.run` approval with its step ID (`SetPendingApproval`). The approval's actor is "implementer" and its why "Installing the dependency the build needs.";
       - on a fresh state, an orchestrator step with no narration asks: actor and why are empty;
       - an approval with no step ID has neither.
     - **`TestASubagentApprovalReadsItsOwnerFromTheChild`.** Moved from `tui/steps_test.go`'s `TestSubagentApprovalResolvesOwnerAgainstChildState`. The parent and a child begin steps with the same ID. The parent's narration is "Parent narration that must not leak.", and the child's step, by "reviewer", is narrated "Checking the migration. It touches two tables." The child is registered as "reviewer" and asks with its step ID. The approval's actor is "reviewer" and its why "Checking the migration."
     - **`TestPendingMarksForcedApprovals`.** A table of approvals, each set on a fresh state with `SetPendingApproval`, and whether `Forced` is set: `shell.sudo` (yes); `shell.run` with risk `model-cost-consent` (yes); `shell.run` with `ExactCommandApproval` (yes); `mode.request` (yes); `file.write` with reason "mode-elevation: needs act" (yes); plain `shell.run` with risk "command" (no).
     - **`TestAnUnregisteredPromptKeepsOneKeyWhileOpen`.** An approval set with `SetPendingApproval` reads with the same key, starting "local-", on two `Pending` calls. After `SetPendingApproval(nil)` and a second unregistered approval, the key differs from the first.
     - **`TestATypedAccessApprovalIsKeyedByItsRequest`.** A `mode.request` call is registered with `RegisterAccess` (intent: system, reason "read outside"). `Approvals[0]` has `Access` set, and its key is the returned request's ID.
     - **`TestPendingListsTheStatesAccessRequest`.** After `RegisterAccess`, `Pending().Access` is the returned request in wire form: its ID, session "s1", the reason, `Current` with the state's mode and revision, and system missing.
     - **`TestPendingNeverSettlesAnAccessRequest`.** With no access callbacks in `Deps`, a registered access request is listed by two `Pending` calls, `state.AccessRequest()` still reports it, and its `ResponseChan` holds nothing.
     - **`TestAnswerUnansweredIsTheSessions`.** `acpwire.AnswerUnanswered` equals `session.AnswerUnanswered`, and each `acpwire.Access*` value equals the `session.AccessStatus` of the same name.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go build ./...
TMPDIR=/private/tmp/p2-4-suite go test ./internal/acpwire/ ./internal/acpclient/...
TMPDIR=/private/tmp/p2-4-suite go test ./internal/acpclient/inproc/ -run 'Pending|Unregistered|Typed|Unanswered|StatusMirrors' -v 2>&1 | grep -E '^(--- |ok|FAIL)'
TMPDIR=/private/tmp/p2-4-suite go test ./internal/acp/ -run 'TestACPClientMethodTableMatchesTheServer'
TMPDIR=/private/tmp/p2-4-suite go test ./internal/app/tui/...
go vet ./internal/acpwire/ ./internal/acpclient/...
```

Expected: every listed test passes. T2 accepts the new types. T4 passes on both sides: none of the new ACP names is a method `acp.Server` registers. `tui` builds and passes unchanged, because its test sessions embed `acpclient.Session`.

## Task 2: The session answers its requests

**Goal:** every answer call, `ReconcileAccess` and `Rollback` work in the adapter, with the policy the TUI applied: the "always" and "session" rules, the forced deny, the skill gate's remembered choice, the runtime's access seam and the rollback's audit row. Nothing in the TUI calls them yet.

**Files:** `internal/acpclient/acpclient.go`, `methods.go`; `internal/acpclient/inproc/inproc.go`, `events.go`, `answers.go` (new), `answers_test.go` (new); `internal/app/app.go`.

**Steps:**

1. **`acpclient.go`.** Add `AnswerApproval`, `AnswerQuestion`, `AnswerSkillGate`, `AnswerSudo`, `ReconcileAccess`, `AnswerAccess` and `Rollback` after `Pending`, with their Contracts doc comments. **`methods.go`:** add their rows after `Pending`'s, `ReconcileAccess`'s with no ACP name.
2. **`inproc/inproc.go`.** `Deps` gains `AccessRequest` and `ResolveAccess`, and `Session` gains `accessRequest` and `resolveAccess` (Contracts). `newSession` (`events.go`) copies them.
   **`internal/app/app.go`.** The `inproc.Deps` literal in `Run` (2814) gains `AccessRequest: rt.AccessRequest` and `ResolveAccess: rt.ResolveAccess`. `accessTUIOptions` is unchanged until Task 3, because nothing calls `ReconcileAccess` yet.
3. **`inproc/answers.go`** (new). Every answer call and `Rollback` returns `errSessionClosed` on a closed session, before anything else. A call that finds its request settled returns `errRequestSettled`.
4. **`AnswerApproval`:**
   1. Find the approval with that key in `s.open().approvals`. None: `errRequestSettled`.
   2. A typed access approval (`call.Access != nil`) settles nothing and returns an error: "inproc: an access request is answered with AnswerAccess".
   3. Map the decision: `once` is `UserApprovalDecision{Approved: true, Edited: answer.Edited}`, `deny` is `{Approved: false}`, and `session` and `always` are `{Approved: true}` with their apply callback. Any other decision settles nothing and returns an error naming it.
   4. A `session` or `always` answer to a `forcedApproval` call becomes `{Approved: false}` with no callback (decision 5).
   5. **The `always` callback** is `handleApproval`'s, moved. The rule's permission is `permissions.PermissionForTool(call.Name)`, its pattern `permissions.PatternForApproval(call)`, and its action `permissions.ActionAllow`. It saves with `config.SaveUserConfigRule(filepath.Join(userConfigDir(), "config.toml"), …)`.
      - When the save fails, it calls `s.state.AddSessionRule(call.Command)` and returns nil.
      - Otherwise it appends the rule to a copy of `s.state.Config`'s `Permissions.Rules` and calls `s.state.RebaseInteractionConfig` with that copy, ignoring its results as `applyNewConfig` did.
   6. **The `session` callback** calls `s.state.AddSessionRule(call.Command)`.
   7. **A registered call** (`call.Key.ID` set) settles with `owner.ResolveApproval(call.Key, decision, apply)`. On an error, `owner.ClearPrompt(call.Key)` and return the error. When the claim lost, return `errRequestSettled`.
   8. **An unregistered call** settles as `settleApproval`'s fallback did: run the callback (ignoring its error), then `call.Respond(decision)` if `owner.PendingApproval()` is still `call`, then `owner.SetPendingApproval(nil)`.
   9. **`userConfigDir`** moves from `tui/model.go` with its doc comment, unchanged. Task 8 deletes the TUI's copy.
5. **`AnswerQuestion`.** Find the question by key, or return `errRequestSettled`. Convert the answers to `[]session.Answer`. A registered question settles with `s.state.ResolveQuestion`, and a lost claim returns `errRequestSettled`. An unregistered one gets `Respond(answers)` and `SetPendingQuestion(nil)`.
6. **`AnswerSkillGate`.**
   - Map the choice onto `session.SkillGateAllowOnce`, `SkillGateAllowSkill`, `SkillGateAllowAll` or `SkillGateDeny`. Any other choice settles nothing and returns an error naming it.
   - The callback is `handleSkillGate`'s: `allow_skill` calls `s.state.SkillGateRecordAllow(skill, false)`, and `allow_all` calls it with `true`.
   - A registered gate settles with `s.state.ResolveSkillGate(key, choice, apply)`. On an error, `ClearPrompt` and return it. A lost claim returns `errRequestSettled`.
   - An unregistered gate runs the callback, then gets `Respond(choice)` and `SetPendingSkillGate(nil)`.
7. **`AnswerSudo`.** The decision is `session.SudoAuthDecision{OK: answer.OK, Password: answer.Password}`, the same slice. A registered prompt settles with `s.state.ResolveSudoAuth`, and a lost claim returns `errRequestSettled`. An unregistered one gets `Respond` and `SetPendingSudoAuth(nil)`. Nothing here logs, formats or copies the password.
8. **`ReconcileAccess`** (decision 7). A closed session reports none.
   - When `s.accessRequest` or `s.resolveAccess` is nil: if `s.state.AccessRequest()` reports a request, settle it with `s.state.CancelAccess(id, session.AccessFailed, "Access resolver unavailable")`. Report none.
   - Otherwise call `s.accessRequest()`. Report its request in wire form, as `Pending` converts it, only when it reports one whose `SessionID` is `s.state.SessionID()`.
9. **`AnswerAccess`.**
   - Without both callbacks, settle the state's typed request as failed, as `ReconcileAccess` does. Then return a zero result and the error "inproc: no access resolver".
   - Otherwise convert the choice to `session.AccessChoice` (the four `Current` fields copied), call `s.resolveAccess`, and return the result converted to wire form with the error.
10. **`Rollback`** is the two TUI rollback paths, moved. Build `registry.AuditEvent{Timestamp: time.Now(), ToolName: "rollback", ResultSummary: "Rollback applied successfully"}`. When `s.state.RollbackBackup()` fails, set its `Error` to the message and its `ResultSummary` to "Rollback failed". Log it with `s.state.LogToolCall`, and return the restore's error.
11. **Tests (`inproc/answers_test.go`, new).** States come from `newState(t, "s1")`, and prompts are registered as in Task 1's tests. `testenv.Run` points `HOME` at a temporary directory, which `os.UserHomeDir` returns.
   - **`TestAnswerApprovalOnceAndDeny`.** A table: `once` delivers an approved decision, and `deny` one that is not. After each answer, `Pending().Approvals` is empty.
   - **`TestAnswerApprovalCarriesTheEdit`.** `once` with `Edited` "go test ./internal/..." delivers that edit.
   - **`TestAnAlwaysAnswerSavesAndAppliesTheRule`.** An `always` answer to a `shell.run` approval of "go test ./..." delivers approval. The file `<home>/.config/marshal/config.toml` exists and contains the pattern `permissions.PatternForApproval` gives for the call. `state.Config.Permissions.Rules` holds a rule with permission "shell", that pattern and action "allow".
   - **`TestAnAlwaysAnswerFallsBackToASessionRule`.** A regular file sits at `<home>/.config/marshal`, so the save fails. The answer still delivers approval, `state.SessionRules()` holds the command, and `state.Config.Permissions.Rules` gains nothing.
   - **`TestASessionAnswerAddsASessionRule`.** `session` delivers approval, and `state.SessionRules()` holds the command.
   - **`TestAStandingAnswerToAForcedApprovalDenies`.** For `session` and for `always`, an exact-command approval of "git push" gets a decision that is not approved. Neither adds a session rule, and no user config file is written.
   - **`TestAChildsApprovalSettlesOnTheChild`.** A running child registers an approval of "go vet ./...". `AnswerApproval` with its key and `session` delivers approval on the child's channel. The command lands in the parent's `SessionRules()`, not the child's.
   - **`TestAnAnswerToASettledApprovalIsRequestSettled`.** A second answer with the same key returns an error for which `acpclient.IsCode(err, acpwire.CodeRequestSettled)` holds.
   - **`TestAnUnregisteredApprovalIsAnsweredOnItsChannel`.** An approval set with `SetPendingApproval` is answered `once` with the key `Pending` gave. Its channel delivers approval, and the slot is empty.
   - **`TestAnAccessApprovalIsNotAnsweredAsAnApproval`.** After `RegisterAccess`, `AnswerApproval` with `Approvals[0].Key` returns an error, and `state.AccessRequest()` still reports the request.
   - **`TestAnswerQuestionDeliversTheAnswers`.** The answers arrive on the question's channel in order. A second answer returns the request-settled code.
   - **`TestAnswerSkillGateRecordsTheChoice`.** A table over the four choices, each on a fresh state:
     - every choice is delivered on the channel;
     - after `allow_skill`, `SkillGateDecisions()` names the skill;
     - after `allow_all`, `SkillGateEnabled()` is false;
     - `allow_once` and `deny` record nothing.

     A fifth row, the choice "maybe", returns an error, and the gate is still pending.
   - **`TestAnswerSudoHandsThePasswordOver`.** `AnswerSudo` with `OK` true and the password `[]byte("hunter2")` delivers a decision whose `Password` is that slice: its first element has the same address as the test's.
   - **`TestASudoPasswordIsInNoRead`** (T7). `slog`'s default logger writes to a buffer for the test, and the old default is restored afterwards. After `AnswerSudo` with "hunter2", neither "hunter2" nor its base64 "aHVudGVyMg==" appears in the JSON of `Status()`, in the JSON of `Pending()`, or in the log buffer.
   - **`TestReconcileAccessReadsTheRuntime`.** `Deps.AccessRequest` counts its calls and returns `state.AccessRequest()`, and `Deps.ResolveAccess` is a stub. After `RegisterAccess`, `ReconcileAccess` reports the request in wire form, and the getter was called once.
   - **`TestReconcileAccessSkipsAnotherSessionsRequest`.** `Deps.AccessRequest` returns the state's request with its `SessionID` replaced by "other". `ReconcileAccess` reports none, and `state.AccessRequest()` still reports the request.
   - **`TestReconcileAccessNeedsBothCallbacks`.** Subtests "no callbacks", "getter only" and "resolver only", as `tui`'s `TestAccessPanelUnavailableResolverFailsClosed` has them. Each registers an access request, and `ReconcileAccess` reports none. The call's `ResponseChan` delivers, within five seconds, a decision that is not approved and whose `Access.Status` is `session.AccessFailed`.
   - **`TestAnswerAccessResolvesThroughTheRuntime`.** `Deps.ResolveAccess` records its choice and returns a granted result, effective mode "act" with system access. The returned wire result carries the same status and tuple, and the recorded choice equals the wire choice converted.
   - **`TestAnswerAccessWithoutAResolverFailsClosed`.** With no callbacks, `AnswerAccess` returns an error. The request's channel delivers a typed failure that is not approved.
   - **`TestRollbackRestoresAndLogs`.**
     - The state stores the backup `{Path: "app.go", Content: "original", Exists: true, Mode: 0o644}`, and `app.go` in the working directory holds "changed". `Rollback` returns nil, `app.go` reads "original", and the audit log's last row has the tool name "rollback" and the summary "Rollback applied successfully".
     - A backup of `blocker/x.go`, where `blocker` is a regular file, makes `Rollback` return an error. The last row's summary is then "Rollback failed", with the error set.
   - **`TestAClosedSessionAnswersNothing`.** After `host.Swap`, each answer call and `Rollback` on the old session returns an error with `acpwire.CodeSessionClosed`, and its `ReconcileAccess` reports none. The old state's registered approval is still pending.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go build ./...
TMPDIR=/private/tmp/p2-4-suite go test ./internal/acpclient/...
TMPDIR=/private/tmp/p2-4-suite go test ./internal/acpclient/inproc/ -run 'Answer|Always|Standing|Child|Settled|Unregistered|AccessApproval|Reconcile|Sudo|Rollback|Closed' -v 2>&1 | grep -E '^(--- |ok|FAIL)'
TMPDIR=/private/tmp/p2-4-suite go test ./internal/acp/ -run 'TestACPClientMethodTableMatchesTheServer'
TMPDIR=/private/tmp/p2-4-suite go test ./internal/app/ -run 'Access'
TMPDIR=/private/tmp/p2-4-suite go test ./internal/app/tui/...
go vet ./internal/acpclient/... ./internal/app/
```

Expected: every listed test passes, `TestAdapterDoesNotImportApp` included, and `tui` passes unchanged.

## Task 3: Access requests go through the session

**Goal:** the access panel presents the request `ReconcileAccess` reports and submits its choice through `AnswerAccess`. The status line and the key guard read `Pending`. `WithAccessRequests` is gone, and `accesspanel` imports no agent-side package.

**Files:** `internal/app/tui/accesspanel/accesspanel.go`, `accesspanel_test.go`; `internal/app/tui/model.go`, `status.go`; `system_access_test.go`, `model_test.go`, `frame_invariants_test.go`, `moved_reads_test.go`, `boundary_allowlist_test.go`; `internal/app/app.go`, `app_test.go`, `tui_seam_test.go`.

**Steps:**

1. **`accesspanel.go`.** `Panel.request` is an `acpwire.AccessRequest`, `ChoiceMsg` carries an `acpwire.AccessChoice`, and `New`, `Request` and `decision` use the wire types (Contracts). The `session` import goes.
2. **`model.go`:**
   - delete the `accessRequest` and `resolveAccess` fields, their comment, and `WithAccessRequests`;
   - **`accessRequestSnapshot`** returns `m.sess.ReconcileAccess(m.ctx)`. Its three callers, `reconcileAccess`, `handleAccessChoice`'s refresh and `restoreComposerSurface`, all run during an update, as the runtime read did;
   - **`reconcileAccess`** loses its fail-closed branch (`m.state.AccessRequest` and `CancelAccess`) and its foreign-snapshot guard. `ReconcileAccess` does both (decision 7). The rest is unchanged;
   - **`handleAccessChoice`** loses its `m.resolveAccess == nil` branch and calls `m.sess.AnswerAccess(m.ctx, choice)`. It compares the status with `acpwire.AccessRefresh`;
   - **`accessOutcomeNote`** takes an `acpwire.AccessResult` and switches on the `acpwire.Access*` values;
   - in **`Update`**, the block that consumes keys while an access request is queued tests `m.sess.Pending().Access != nil`, the plain read it made of the state.
3. **`status.go`.** `accessRequestQueued` tests `m.sess.Pending().Access == nil`. It runs while rendering, so it must not call `ReconcileAccess`.
4. **`app.go`.** `accessTUIOptions` loses `WithAccessRequests` and keeps the interaction and effort setters. Its doc comment says access requests reach the TUI through the adapter's `Deps` (Task 1).
5. **TUI tests:**
   - **`accesspanel_test.go`:** requests and choices are the wire types;
   - **`system_access_test.go`:**
     - `newAccessTestFixture` builds its model with `testModelWith(inproc.Deps{State: state, AccessRequest: …, ResolveAccess: …}, WithInteractionSetter(…))`, the two callbacks being the ones it passed to `WithAccessRequests`;
     - `TestAccessPanelUnavailableResolverFailsClosed`'s three cases become `inproc.Deps` values (no callbacks, `AccessRequest` only, `ResolveAccess` only), each built with `testModelWith`;
     - the choices the tests send are `acpwire.AccessChoice` values, built from the request's wire form;
   - **`model_test.go`:**
     - `TestPostmortemConsentResizeInvalidatesVisibility` builds its model with `testModelWith`, its two callbacks moved into the `Deps`;
     - `TestSendNowComposerRoutesSuspendedAsyncResultsOnce` replaces its two field assignments with a session built over the fixture's state with the same callbacks (`_, sess := inproc.New(inproc.Deps{State: g.state, AccessRequest: …, ResolveAccess: …})`, then `g.m.sess = sess`);
   - **`frame_invariants_test.go`:** `TestFrameInvariants`' two `accesspanel.New` calls take `acpwire.AccessRequest{MissingEditing: true}`. Its queued-request case, which sets the request on the state, is unchanged.
6. **`app` tests:**
   - **`tui_seam_test.go`** gains `testTUIModelWith` (Contracts), as `testTUIModel` builds one over `d.State`;
   - **`accessTUIChoice`** builds an `acpwire.AccessChoice`, copying `req.Current`'s four fields;
   - **`TestAccessTUIOptionsResolveWithoutConfigReload`** becomes **`TestAdapterAccessResolvesWithoutConfigReload`**: its model comes from `testTUIModelWith(inproc.Deps{State: state, AccessRequest: rt.AccessRequest, ResolveAccess: rt.ResolveAccess}, accessTUIOptions(rt)...)`. The rest is unchanged;
   - **`TestAccessTUIOptionsFollowNewSession`** becomes **`TestAdapterAccessFollowsNewSession`**. It builds one host from those `Deps` over the old state (`inproc.New`), and the old model over its session. After `rt.NewSession`, the new model is built over `host.Swap(newState)`, with `tui.WithLegacyState(newState)`. A retired choice still grants nothing on the replacement, and the replacement's own request still resolves. Its comments say the `Deps` are built once, as production builds them.
7. **`moved_reads_test.go`.** `movedStateReads` gains `"AccessRequest": "4"` and `"CancelAccess": "4"`, with a comment: the access panel presents what `ReconcileAccess` reports and answers through `AnswerAccess`.
8. **`boundary_allowlist_test.go`:** delete the `accesspanel/accesspanel.go` key.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go build ./...
grep -rn 'WithAccessRequests\|m\.accessRequest\b\|m\.resolveAccess\b' internal --include='*.go'
TMPDIR=/private/tmp/p2-4-suite go test ./internal/app/tui/ -run 'Access|Postmortem|SendNowComposer|MovedStateReads|TUIStaysOnTheClientSeam' -v 2>&1 | grep -E '^(--- |ok|FAIL)'
TMPDIR=/private/tmp/p2-4-suite go test ./internal/app/tui/...
TMPDIR=/private/tmp/p2-4-suite go test ./internal/app/ -run 'Access' -v 2>&1 | grep -E '^(--- |ok|FAIL)'
go vet ./internal/app/...
```

Expected: the grep prints nothing. The listed tests pass, and `tui` passes, the goldens included.

## Task 4: Prompt identity is the wire key

**Goal:** the TUI names an open request by its kind and wire key. The composer's displaced prompt, the work navigation lookup and the stale-form retirement read `Pending`, and the TUI names no `session.PromptKey`.

**Files:** `internal/app/tui/promptkey.go` (new), `model.go`, `promptkey_test.go` (new).

**Steps:**

1. **`promptkey.go`** (new) holds `promptKind` and `promptRef` (Contracts).
2. **`model.go`:**
   - `composerDisplacedPrompt` is a `promptRef`. Its resets assign `promptRef{}`, and `restoreComposerSurface` tests `ref.key != ""`;
   - **`focusedPromptKey`** reads `p := m.sess.Pending()` once. It returns, in today's order, the sudo prompt's key (`promptSudo`), the skill gate's (`promptSkill`), the first approval's (`promptApproval`) or the question's (`promptQuestion`). It returns false when there is none, or when the key is empty;
   - **`promptStillPending(ref)`** reads `Pending` once. It is true when the request of `ref.kind` has `ref.key`. For approvals that is the first approval, as today;
   - **`promptKeyByID(id)`** reads `Pending` once. It returns the first request in `focusedPromptKey`'s order whose key is `id`, the first approval included;
   - **`retireStalePromptModels(ref)`** switches on `ref.kind`, with the same effects.

   The first approval now includes a subagent's, and an unregistered prompt now has a key (decision 12).
3. **Tests (`promptkey_test.go`, new):**
   - **`TestFocusedPromptKeyFollowsTheRoutingOrder`.** The state registers a question (`RegisterQuestion`), and sets an unregistered sudo prompt (`SetPendingSudoAuth`). `focusedPromptKey` is the sudo prompt's, with kind `promptSudo` and the key `m.sess.Pending().Sudo.Key`. After `SetPendingSudoAuth(nil)` it is the question's, whose key is the `PromptKey.ID` its registration returned.
   - **`TestPromptKeyByIDFindsASubagentsApproval`.** A running child registers an approval. `promptKeyByID` of that key's ID returns `promptApproval` and the ID.
   - **`TestPromptStillPendingFollowsTheRequest`.** For a registered question, `focusedPromptKey`'s ref is still pending. After `state.ResolveQuestion(key, answers)` it is not.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go build ./...
grep -rn 'session\.Prompt' internal/app/tui --include='*.go' | grep -v '_test.go'
TMPDIR=/private/tmp/p2-4-suite go test ./internal/app/tui/ -run 'FocusedPromptKey|PromptKeyByID|PromptStillPending|SendNowComposer' -v 2>&1 | grep -E '^(--- |ok|FAIL)'
TMPDIR=/private/tmp/p2-4-suite go test ./internal/app/tui/...
go vet ./internal/app/tui/...
```

Expected: the grep prints nothing, and the listed tests and `tui` pass.

## Task 5: The question, the skill gate and the sudo prompt go through the session

**Goal:** the three prompts render wire values from `Pending` and answer through `AnswerQuestion`, `AnswerSkillGate` and `AnswerSudo`. `question.go`, `skillgate.go` and `sudoauth.go` import no agent-side package, and nothing in the TUI reads those three slots from the state.

**Files:** `internal/app/tui/question.go`, `skillgate.go`, `sudoauth.go`, `transcript.go`, `model.go`, `console.go`, `status.go`, `view.go`, `keypress.go`; `answers_seam_test.go` (new), and the tests that build these forms or panels (step 9); `moved_reads_test.go`, `boundary_allowlist_test.go`.

**Steps:**

1. **`question.go`.** `questionModel.q` is an `acpwire.QuestionRequest` and `answers` an `[]acpwire.Answer`. `questionOptionLabel`, `buildQuestionOptions` and `questionOptionKeys` take `[]acpwire.QuestionOption`. `unansweredAnswers` (Contracts) replaces both `session.UnansweredAnswers` calls, and `acpwire.AnswerUnanswered` replaces `session.AnswerUnanswered`. The `session` import goes.
2. **`transcript.go`.** `renderQuestionPanel` takes the wire value and returns "" when it holds no questions.
3. **`skillgate.go`.** The choice table uses the `acpwire.SkillGate*` values. `skillGateModel.sg` is an `acpwire.SkillGateRequest`, and `choice`, `Choice` and `renderSkillGatePanel` use the wire types. The `session` import goes.
4. **`sudoauth.go`.** `sudoAuthModel.sa` is an `acpwire.SudoRequest`. `Decision` returns `acpwire.SudoAnswer{OK: false}` on a deny, and `{OK: true, Password: []byte(m.input.Value())}` otherwise. `renderSudoAuthPanel` takes the wire value. The `session` import goes.
5. **`model.go`, the handlers:**
   - **`handleQuestion(msg, q)`** rebuilds its form when `m.questionModel` is nil or bound to another key. When the form is done, it calls `m.sess.AnswerQuestion(m.ctx, q.Key, answers)` in place of its two-case settlement, ignoring the error: the form closes either way, as today;
   - **`handleSkillGate(msg, sg)`** rebuilds on a different key. When done, it calls `m.sess.AnswerSkillGate(m.ctx, sg.Key, choice)` and refreshes the viewport. Its apply callback and both settlement paths are deleted: the adapter records the choice;
   - **`handleSudoAuth(msg, sa)`** rebuilds on a different key. When done, it calls `m.sess.AnswerSudo(m.ctx, sa.Key, decision)` and refreshes the viewport.
6. **`model.go`, the readers** (decision 9):
   - **`New`**'s eager build reads `Pending` once for the three forms. The approval line is unchanged until Task 8;
   - **`update`** reads `p := m.sess.Pending()` once, before the block that refuses Ctrl+O and Ctrl+K. The block, and the sudo, skill-gate and question branches of the routing chain, use `p`: `handleSudoAuth(msg, *p.Sudo)`, `handleSkillGate(msg, *p.SkillGate)` and `handleQuestion(msg, *p.Question)`. The approval branch is unchanged;
   - **`inputChromeRows`**, **`inputAreaRows`**, **`surfaceOwnsInput`** and **`settingsBlockReason`** each read `Pending` once.
7. **`console.go`** (`consoleColor`, `inputSections`), **`status.go`** (`transientStatusSegment`, `footerHints`, `ctrlXOwned`), **`view.go`** (`suggestionGhost`) and **`keypress.go`** (`handleKeypress`'s Enter guard, and its Tab and Shift+Tab guards) test `Pending`'s fields, read once per function. `handleKeypress` reads it in each case that tests it.
8. **`answers_seam_test.go`** (new) holds `answerRecorder`, its record types and `testModelRecording` (Contracts). Each answer method appends what it was given, then calls the wrapped session's. `Rollback` counts its calls, then does the same.
9. **Tests.** Every test that builds a question form, a skill-gate dialog, a sudo prompt or their panels from a `session` value builds it from the wire value with the same fields: `question_test.go`, `skillgate_test.go`, `sudoauth_test.go`, `approval_controls_test.go` (`TestSudoPromptShowsControlCharacters`, `TestSudoPromptShowsCombinedConsentMetadata`) and any other file the compiler names.
   - A test that compares a form's bound request compares keys.
   - A test that sets a prompt on the state and drives the model is unchanged. Those tests now exercise the adapter: `TestHandleSkillGateAllowOnceRespondsAndClears`, `TestHandleSkillGateAllowAllDisablesGate`, `TestHandleSkillGateDenyDoesNotRecord`, `TestSudoAuthEnterSubmitsTypedBytes`, `TestSudoAuthEscDenies` and `TestQuestionSettlementPreservesComposer`.
   - **New, in `answers_seam_test.go`:**
     - **`TestTheQuestionFormAnswersThroughTheSession`.** A registered question with one free-text question, answered through the model as `TestQuestionSettlementPreservesComposer` answers it. The recorder holds one question answer, with the question's key and the form's answers.
     - **`TestTheSkillGateAnswersThroughTheSession`.** A registered gate, answered "Allow always (this skill)" as `TestHandleSkillGateAllowOnceRespondsAndClears` drives it. The recorder holds one skill answer, with the gate's key and `acpwire.SkillGateAllowSkill`, and `state.SkillGateDecisions()` names the skill.
     - **`TestTheSudoPromptAnswersThroughTheSession`.** A registered sudo prompt, where the password "pw" is typed and submitted as `TestSudoAuthEnterSubmitsTypedBytes` does. The recorder holds one sudo answer, with the prompt's key, `OK` true and the password bytes "pw".
10. **`moved_reads_test.go`.** `movedStateReads` gains, at plan `"4"`: `PendingQuestion`, `PendingSkillGate`, `PendingSudoAuth`, `SetPendingQuestion`, `SetPendingSkillGate`, `SetPendingSudoAuth`, `ResolveQuestion`, `ResolveSkillGate`, `ResolveSudoAuth` and `SkillGateRecordAllow`. The comment says the three prompts read `Pending` and answer through the session.
11. **`boundary_allowlist_test.go`:** delete the `question.go`, `skillgate.go` and `sudoauth.go` keys.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go build ./...
grep -rn 'session\.\(PendingQuestion\|PendingSkillGate\|PendingSudoAuth\|SkillGateChoice\|SudoAuthDecision\|QuestionOption\|Answer\b\|UnansweredAnswers\|AnswerUnanswered\)' internal/app/tui --include='*.go' | grep -v '_test.go'
TMPDIR=/private/tmp/p2-4-suite go test ./internal/app/tui/ -run 'ThroughTheSession|SkillGate|Sudo|Question|MovedStateReads|TUIStaysOnTheClientSeam' -v 2>&1 | grep -E '^(--- |ok|FAIL)'
TMPDIR=/private/tmp/p2-4-suite go test ./internal/app/tui/...
go vet ./internal/app/tui/...
```

Expected: the grep prints nothing. The listed tests pass, and `tui` passes, the goldens included.

## Task 6: The sandbox comes from Status

**Goal:** the running shell row and the approval panel read the sandbox from `Status().Sandbox`, and the TUI reads no sandbox info or shell network flag from the state.

**Files:** `internal/app/tui/transcript.go`, `spine.go`, `steps.go`, `viewport_render.go`, `approval.go`, `model.go`, `console.go`; `drill_test.go`, `sandbox_status_test.go` (new), and the tests that call the changed functions (step 6); `moved_reads_test.go`.

**Steps:**

1. **`transcript.go`.**
   - `sandboxIsolationText` takes an `acpwire.Sandbox` and reads its `AllowNetwork`.
   - `renderActiveToolCall` and `renderApprovalPanel` take an `acpwire.Sandbox` in place of the info and the flag (Contracts).
2. **`steps.go`.** `stepRenderCtx.sandbox` is an `acpwire.Sandbox`, and `allowNetwork` is deleted. **`spine.go`:** the running row calls `sandboxIsolationText(c.sandbox)`.
3. **`viewport_render.go`:**
   - `viewFacts.sandbox` is an `acpwire.Sandbox`, and `allowNetwork` is deleted;
   - **`viewFacts`** reads it from `st.Sandbox` when not drilled in, and leaves it zero when drilled in (decision 8);
   - the render context sets `sandbox` from the facts. `drawNode`'s running-call case passes `c.sandbox`. `contentSignature` does not fold the sandbox, and this plan does not add it: only a running row reads it, and a running row re-renders every tick.
4. **`approval.go`.** `approvalModel.sb` is an `acpwire.Sandbox`, and `allowNetwork` is deleted. `newApprovalModel` and `approvalSummary` take the struct (Contracts), and the form's isolation line calls `sandboxIsolationText(am.sb)`.
5. **Callers.** `New`, `handleApproval` and `inputChromeRows` in `model.go`, and `inputSections` in `console.go`, pass `st.Sandbox` from a `Status` read. A function without one reads `Status` once. No `m.state.SandboxInfo()` or `m.state.Config.Tools.Shell.AllowNetwork` read is left at these sites.
6. **Tests:**
   - every call to the changed functions drops the flag and passes an `acpwire.Sandbox`, its `AllowNetwork` set to the flag it passed, and `session.SandboxInfo{}` becomes `acpwire.Sandbox{}`;
   - `drill_test.go`'s `TestDrilledViewReadsTheChildsFacts` expects a zero `acpwire.Sandbox` and no longer checks a separate network flag;
   - **`sandbox_status_test.go`** (new). Both tests use `testModelStub`, whose `patch` sets `Status().Sandbox` to `{Backend: "container", NetworkIsolation: true}` while the state records no sandbox info:
     - **`TestTheRunningShellRowReadsTheSandboxFromStatus`.** A beat with a running `shell.run` (`SetActiveToolCall`) renders the line "sandbox: container · network blocked".
     - **`TestTheApprovalPanelReadsTheSandboxFromStatus`.** A pending `shell.run` approval (`SetPendingApproval`) shows "sandbox: container · network blocked" in the input area (`viewString` after `resize(120, 40)` and a refresh).
7. **`moved_reads_test.go`.** `movedStateReads` gains `"SandboxInfo": "4"`.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go build ./...
grep -rn 'SandboxInfo\|Shell\.AllowNetwork' internal/app/tui --include='*.go' | grep -v '_test.go'
TMPDIR=/private/tmp/p2-4-suite go test ./internal/app/tui/ -run 'SandboxFromStatus|Drill|MovedStateReads' -v 2>&1 | grep -E '^(--- |ok|FAIL)'
TMPDIR=/private/tmp/p2-4-suite go test ./internal/app/tui/...
go vet ./internal/app/tui/...
```

Expected: the grep prints nothing. The listed tests pass, and `tui` passes, the goldens included.

## Task 7: Rollback goes through the session

**Goal:** both rollback paths, Ctrl+R twice and the approval form's "Rollback last change", call `Session.Rollback`. `keypress.go` imports no `registry`.

**Files:** `internal/app/tui/keypress.go`, `model.go`; `answers_seam_test.go`; `moved_reads_test.go`, `boundary_allowlist_test.go`.

**Steps:**

1. **`keypress.go`.** The second Ctrl+R calls `m.sess.Rollback(m.ctx)` in place of the audit event, `RollbackBackup` and `LogToolCall`, ignoring the error (the audit row shows it, as today). It then refreshes the viewport as today. The `registry` import goes, and so does `time` if nothing else uses it.
2. **`model.go`.** `handleApproval`'s `choiceRollback` case does the same while `st.HasBackup`, then invalidates and refreshes as today. The approval stays open.
3. **Tests.** **`TestCtrlRRollsBackThroughTheSession`** (new, `answers_seam_test.go`). The state stores a backup as `TestTUIRollbackFlow` does, in a model from `testModelRecording`. After two Ctrl+R presses the recorder counts one rollback, `state.HasBackup()` is false, and the audit log's last row is the "rollback" one. `TestTUIRollbackFlow` and `TestRollbackRequiresConfirmation` are unchanged.
4. **`moved_reads_test.go`.** `movedStateReads` gains `"RollbackBackup": "4"` and `"LogToolCall": "4"`.
5. **`boundary_allowlist_test.go`.** `keypress.go`'s entry becomes `{"marshal/internal/app/session"}`.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go build ./...
grep -rn 'RollbackBackup\|LogToolCall' internal/app/tui --include='*.go' | grep -v '_test.go'
TMPDIR=/private/tmp/p2-4-suite go test ./internal/app/tui/ -run 'Rollback|MovedStateReads|TUIStaysOnTheClientSeam' -v 2>&1 | grep -E '^(--- |ok|FAIL)'
TMPDIR=/private/tmp/p2-4-suite go test ./internal/app/tui/...
go vet ./internal/app/tui/...
```

Expected: the grep prints nothing, and the listed tests and `tui` pass.

## Task 8: Approvals go through the session

**Goal:** the approval form, its fallback panel and the edit sub-mode take wire approvals from `Pending`, and every decision goes through `AnswerApproval`. The TUI holds no owner state, derives no "why" line, saves no rule, and reads no approval slot. `approval.go` and `toolnames.go` are off the allowlist.

**Files:** `internal/app/tui/approval.go`, `transcript.go`, `model.go`, `console.go`, `steps.go`, `toolnames.go` (deleted), `toolnames_test.go` (deleted); `internal/viewmodel/labels_test.go`; the TUI tests that build or drive approvals (step 7); `moved_reads_test.go`, `boundary_allowlist_test.go`.

**Steps:**

1. **`approval.go`:**
   - `approvalModel.tc` becomes `a acpwire.ApprovalRequest`, the display copy the form shows. Every `tc.Name` reads `a.Tool`, and the other fields keep their names;
   - **`forcedPromptClass` is deleted.** `newApprovalModel` offers "always" and "session" unless `a.Forced`;
   - **`approvalWhyOf(a)`** is `approvalWhy{owner: a.Actor, why: a.Why}`. `newApprovalModel` and `approvalSummary` lose their variadic `why` parameter and call it;
   - **`ownerLine(a)`** says "wants to run a command" when `a.Tool` is `shell.run`, else "wants to " plus `strings.ToLower(a.ToolLabel)`;
   - **`shownApproval`** replaces `shownToolCall`. It returns a copy with `strutil.ShowControls` applied to `Args`, `Command`, `Risk`, `Reason`, `Diff` and `Schema`, the fields `shownToolCall` escaped.
2. **`transcript.go`.** `renderApprovalPanel(a, sb, width)` derives its why with `approvalWhyOf` and shows `shownApproval(a)`. `riskText` takes the wire value. The rest is unchanged.
3. **`model.go`, the lookups:**
   - **`pendingApprovalTarget`** returns the first of `m.sess.Pending().Approvals`, or false;
   - **`hasPendingApproval`** reports whether `Approvals` holds any;
   - **`displayApproval`** (Contracts) builds the subagent copy with the prefix `fmt.Sprintf("subagent %q: %s", a.Subagent, a.Reason)`, as the field-by-field copy did. **`pendingApprovalDisplay`** returns `displayApproval` of the target, and its callers drop the label;
   - delete **`approvalOwner`** and **`isModeElevationApproval`**.
4. **`model.go`, the edit sub-mode.** `approvalEdit` holds the input and the approval's key (Contracts). `newApprovalEdit(a, width)` prefills `a.Command` for `shell.run`, else `a.Args`. `approvalEditLive` is true when the target's key equals the edit's.
5. **`model.go`, `handleApproval(msg, a)`.** `a` is the target, not the display copy.
   - **The raw `mode.request`.** When `a.SubagentID` is 0, `a.Tool` is `mode.request` and `a.Access` is false, Enter calls `m.settleApproval(a, acpwire.ApprovalAnswer{Decision: acpwire.ApprovalDeny})` in place of `Respond` and `SetPendingApproval(nil)`. Then it writes the same note and refreshes, as today.
   - **The edit's Enter** settles with `{Decision: acpwire.ApprovalOnce, Edited: value}`.
   - **The form** is built from `displayApproval(a)`, with `st.Sandbox` and `st.HasBackup`. It is rebuilt when `m.approvalModel` is nil or bound to another key.
   - **The choices:**
     - approve settles `ApprovalOnce`, and deny settles `ApprovalDeny`;
     - always settles `ApprovalAlways`. When that returns nil, the TUI calls `m.configViewsChanged(m.state.Config)`. Its forced guard and its rule-building callback are deleted;
     - session settles `ApprovalSession`, and its forced guard and callback are deleted;
     - edit opens `newApprovalEdit(a, …)`, and rollback is Task 7's.
6. **`model.go`, the rest:**
   - **`settleApproval(a, answer)`** returns `m.sess.AnswerApproval(m.ctx, a.Key, answer)`. Its registered and fallback paths are deleted;
   - **`applyNewConfig`** calls `m.state.RebaseInteractionConfig(cfg)`, then `m.configViewsChanged(cfg)`. `configViewsChanged` holds the four resets and `loadTheme(cfg.TUI)`;
   - delete **`userConfigDir`** (Task 2 moved it), and drop the imports the deletions leave unused (`permissions` and others);
   - **`New`**'s eager build uses `pendingApprovalDisplay` with `st.Sandbox` and `st.HasBackup`;
   - **`update`**'s approval branch calls `handleApproval(msg, a)` with the target;
   - **`inputChromeRows`** and **`console.go`'s `inputSections`** take the display copy, test `a.Access` where they tested `tc.Access != nil`, and call `renderApprovalPanel(a, st.Sandbox, …)`.
7. **`steps.go`.** Delete `approvalWhyFor`, `stepOwner`, `firstSentence` and `stripEmphasis`, which have no callers now, and the `session` import.
8. **The tool label.** Delete `toolnames.go` and `toolnames_test.go`. `TestDisplayToolName` moves to `viewmodel/labels_test.go`, calling `DisplayToolName`, with `TestDisplayToolNameKnown`'s two cases (`file.write_patch` and `shell.run`) added to its table. Delete `TestDisplayToolNameKnown` from `tui/transcript_test.go`.
9. **Tests:**
   - **`steps_test.go`:**
     - `TestApprovalShowsOwnerAndWhy` keeps its rendering half. Its approvals are wire values: one with actor "implementer" and why "Installing the dependency the build needs.", and one with neither. Task 1 moved the derivation half;
     - `TestSubagentApprovalResolvesOwnerAgainstChildState` keeps its fixture. Its check is that `pendingApprovalDisplay` has actor "reviewer", why "Checking the migration." and `SubagentID` the registered view's ID;
     - delete `TestFirstSentence`, whose functions went. `strutil`'s tests cover them since plan 3d;
   - **`approval_controls_test.go`, `TestExactCommandApprovalOmitsStandingChoices`:**
     - the classification check reads `m.sess.Pending().Approvals[0].Forced`, after the call is set on the state;
     - the model is built from that approval;
     - the forged choice goes through `handleApproval(msg, a)`.

     Its decision and session-rule assertions are unchanged;
   - **`approval_test.go`, `TestForcedPromptsHideAlwaysAndSession`:** builds wire approvals with `Forced` set. Task 1's `TestPendingMarksForcedApprovals` covers the classification;
   - **every other test** that builds an approval form or panel from a `session.PendingToolCall` builds it from an `acpwire.ApprovalRequest` with the same fields (`Tool` for `Name`, and `Forced` where the call was a forced class). A test that passed an `approvalWhy` sets the approval's `Actor` and `Why` instead. A test that sets a call on the state and then drives the model reads the approval from `m.sess.Pending()`. Each `handleApproval(msg, m.state, tc, "")` call becomes `handleApproval(msg, a)`, and a test that compared `m.approvalModel.tc` compares `m.approvalModel.a`'s key;
   - **`TestTheApprovalFormAnswersThroughTheSession`** (new, `answers_seam_test.go`). An unregistered `shell.run` approval is set on the state, in a model from `testModelRecording`. The form finishes three times, as `TestExactCommandApprovalOmitsStandingChoices` finishes it, with the approval set again before each: approve, always, and the edit sub-mode's Enter with "go test ./internal/...". The recorder holds three answers with the approval's key each time: `ApprovalOnce`, `ApprovalAlways`, then `ApprovalOnce` with that edit.
10. **`moved_reads_test.go`.** `movedStateReads` gains `PendingApproval`, `SetPendingApproval`, `ResolveApproval` and `ClearPrompt`, at plan `"4"`. The comment says approvals read `Pending` and answer through `AnswerApproval`.
11. **`boundary_allowlist_test.go`:**
    - delete the `approval.go` and `toolnames.go` keys;
    - `steps.go`'s entry becomes `{"marshal/internal/viewmodel"}`, for the work types (plan 5);
    - remove `config.SaveUserConfigRule` and `marshal/internal/permissions` from `model.go`'s entry.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go build ./...
grep -rn 'session\.PendingToolCall\|approvalWhyFor\|forcedPromptClass\|userConfigDir\|DisplayToolName' internal/app/tui --include='*.go' | grep -v '_test.go'
grep -rn '\.Child\b' internal/app/tui --include='*.go' | grep -v '_test.go'
TMPDIR=/private/tmp/p2-4-suite go test ./internal/viewmodel/ -run 'DisplayToolName'
TMPDIR=/private/tmp/p2-4-suite go test ./internal/app/tui/ -run 'Approval|Forced|ExactCommand|OwnerAndWhy|MovedStateReads|TUIStaysOnTheClientSeam' -v 2>&1 | grep -E '^(--- |ok|FAIL)'
TMPDIR=/private/tmp/p2-4-suite go test ./internal/app/tui/...
go vet ./internal/app/tui/... ./internal/viewmodel/
```

Expected:
- the first grep prints nothing;
- the second prints only `jumpToWorkOrigin`'s match of a child by its session ID (plan 5);
- the listed tests pass, and `tui` passes, the goldens included.

## Task 9: Verification and docs

**Goal:** the full suite is green, and the spec and design record plan 4.

**Files:** `docs/daemon/p2-client-seam.md`, `docs/daemon/design.md`.

**Steps:**

1. **`p2-client-seam.md`.** Add a plan 4 entry to the Implementation record, after plan 3d's. It lists:
   - the `acpwire` request and answer types and `Sandbox`, with `Status.sandbox`;
   - the eight `Session` methods and their `Methods` rows, `ReconcileAccess`'s with no ACP name;
   - what the adapter does: keys, the "why" line, the tool label and the forced class; a `Pending` that settles nothing; the always and session rules on the session; the skill gate's record; the rollback's audit row; access through the runtime's callbacks in `ReconcileAccess` and `AnswerAccess`, failing closed without them;
   - that the TUI's prompts read `Pending` and answer through the session, that it names a request by `promptRef`, and that `WithAccessRequests` is gone;
   - T1b's 19 new names at `4`, and the allowlist's ten entries across nine files, with the six keys dropped: `accesspanel/accesspanel.go`, `question.go`, `skillgate.go`, `sudoauth.go`, `approval.go` and `toolnames.go`;
   - decision 12's edge cases, in one sentence each.
2. **`design.md` §7.** In the P2 row, "plans 1, 2a, 2b, 2c, 3a, 3b, 3c and 3d implemented" becomes "plans 1, 2a, 2b, 2c, 3a, 3b, 3c, 3d and 4 implemented".
3. **Full verification.**

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
gofmt -l ./internal ./cmd
go vet ./...
TMPDIR=/private/tmp/p2-4-suite CGO_ENABLED=1 go build ./cmd/marshal
TMPDIR=/private/tmp/p2-4-suite go test ./... -timeout 20m 2>&1 | grep -E '^(--- FAIL|FAIL|ok)' | grep -v '^ok'
git diff --stat main..HEAD -- internal/app/tui/testdata/transcript
```

Expected:
- `gofmt -l` prints nothing;
- `go vet` is clean;
- the binary builds;
- the test line prints nothing. Treat a failure as new unless it also fails at `05a0e4a6`, or it is one of the load flakes below and passes on its own with `-count=3`;
- the `git diff` prints nothing: no golden changed.

**Baseline.** At `05a0e4a6`, plan 3d's merge, every package passed (117 `ok`), as at `025242f9`. Earlier full runs saw these fail under load and pass alone:
- `TestRuntimeAccessSerializesWithReloadAndQuiesce` and `TestRuntimeCloseRunsNoGitGC` (`internal/app`);
- `TestRunManagedCancellationKillsTree` and `TestIntegrationOwnershipReleaseDrainsWritersBeforeUnlock` (`internal/snapshot`);
- `TestACPChildApprovalUsesOwningSessionRegistry` and `TestACPSendNowCancelsPromptRPCWithoutCancelingTurn` (`internal/acp`).

Finally, run `./marshal` in this repository and check by eye:
- ask for a command that needs approval. The panel shows the owner and why lines and the sandbox line. Approve once, then trigger it again and choose "Allow this session"; the next run of that command is not asked about;
- edit a command in the approval's edit sub-mode, and check the edited command runs;
- have the agent ask a question (`question.ask`), skip one with Esc, and answer the other;
- in Ask mode, have the agent request editing access (`mode.request`). The access panel opens, and granting it switches the mode;
- run a `shell.sudo` command, enter a wrong password once, then the right one;
- dispatch a subagent whose command needs approval. The panel names the subagent, and approving it lets the child continue.

## Integration notes

- **Plan 5 (turns and runs)**:
  - moves the SDD gate's answer (`AnswerSDDGate`, `session/sdd_answer`) and its stop with the runs, and `ResolvePendingForShutdown` with `Close`;
  - `jumpToWorkOrigin` still matches a child through `m.state.Subagents()`.
- **Plan 6** moves the notes the request paths write ("Access granted — …", the raw `mode.request` note, the Ctrl+O and Ctrl+K refusals).
- **Plan 7** moves the config the TUI still reads from `m.state.Config`, including the `configViewsChanged(m.state.Config)` call after an "always" answer.
- **P3**:
  - the client answers `session/request_permission` and `session/request_question` and records them in the mirror `Pending` reads, and adds `session/request_secret` and `session/rollback`;
  - it extends `session/request_permission` with the decision and the request kind, and decides then whether ACP's `PermissionRequest` moves to `acpwire` (decision 10);
  - its mirror fires `Changed` when a request arrives or settles.

## Self-review

- **Every task verifiable alone.** The adapter tasks (1 and 2) change no TUI behaviour. `Pending` settles nothing, so reading it while rendering, from Task 3 on, cannot change a frame mid-draw. Task 4 converts the shared lookups before the kinds move. Tasks 3 and 5 to 8 each move one group and keep T1, T1b and the goldens green.
- **Cold executor.** Each judgment step names the function, what it does after the change, and the new helpers with their signatures (Contracts). Each new test names its setup and the one behaviour it pins.
- **Contracts.** The wire types, the eight `Session` methods and their rows, `Deps`' new fields, `open` and its helpers, `promptRef`, the changed renderer signatures, `displayApproval`, `configViewsChanged`, the recorder and the two model helpers are declared there.
- **Anchors.** Every path, symbol and line was checked at `e866c676`, then re-checked at `05a0e4a6` after plan 3d merged. That pass added `events.go` (where `newSession` lives) to Tasks 1 and 2 and `frame_invariants_test.go` to Task 3, and corrected Task 6's `contentSignature` step. Files that plan 3d changed are named by function only. The JSON literals were encoded from the declared types.
- **Spec.** C1, C5, C7, C8, T5 and the Migration order are updated in the revision record "2026-11-02, against plan 3d (written while it ran)", with decisions 1 to 11.
- **No placeholders.**
