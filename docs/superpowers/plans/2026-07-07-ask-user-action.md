# ask_user Action Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give the model a way to ask the user a clarifying question mid-turn instead of guessing: a new `ask_user` action that blocks the loop on a TUI-rendered question until the user answers.

**Architecture:** Mirrors the existing approval protocol exactly. The protocol gains `ActionAskUser`; the runner handles it by setting a `session.PendingQuestion` (question text + response channel) and blocking until the TUI sends the answer or ctx is cancelled; the answer is appended to the transcript and the loop continues. The TUI treats a pending question like the approval panel's edit mode: the text input captures the answer, Enter submits, Esc declines. Only the general role may ask; swarm roles get a correction.

**Tech Stack:** Go stdlib, Bubble Tea/lipgloss (existing TUI deps).

**Prerequisite:** Merge order is telemetry → reliability trio → this plan. It only *hard*-depends on telemetry (the runner test asserts against `p.requests` indices that assume the trio's loop shape is merged; if executing before the trio, run the tests and fix indices per actual call counts — the assertions' intent is stated in each test).

## Global Constraints

- Work on branch `ask-user-action` (from `main` after the reliability trio merges).
- Build/test with `CGO_ENABLED=1 go test ./...`; `gofmt` clean; `go vet` clean except the documented pre-existing app.go mutex-copy warning.
- The TUI renders and routes input only — the blocking/waiting logic lives in `internal/agent` and `internal/app/session`, never in `internal/app/tui`.
- An unanswered question must never deadlock shutdown: the runner select must include `ctx.Done()`, and Esc must always resolve the channel.
- `ask_user` is for the general role only. Swarm roles (planner/scout/implementer/tester/reviewer) run headless under an orchestrator; they receive a correction message instead.
- A question consumes one loop iteration (it is a parsed action) and is not recorded in the progress tracker (it is not a tool).

---

### Task 1: Protocol and prompts

**Files:**
- Modify: `internal/agent/protocol.go` (new action type + validation)
- Modify: `internal/agent/prompts.go` (base rules, output-format example, general role addendum)
- Test: `internal/agent/protocol_test.go`

**Interfaces:**
- Consumes: `ActionType`, `validatePayload`, `ParseAction` (protocol.go).
- Produces: `ActionAskUser ActionType = "ask_user"`; parsed `ModelAction{Type: ActionAskUser, Content: <question>}`; `ErrMissingQuestion`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/agent/protocol_test.go`:

```go
func TestParseActionAskUser(t *testing.T) {
	raw := `{"rationale":"the request is ambiguous","action":{"type":"ask_user","content":"Should the cache be per-session or global?"}}`
	action, err := ParseAction(raw)
	if err != nil {
		t.Fatalf("ParseAction err = %v", err)
	}
	if action.Type != ActionAskUser || action.Content != "Should the cache be per-session or global?" {
		t.Fatalf("action = %+v", action)
	}
}

func TestParseActionAskUserRequiresContent(t *testing.T) {
	raw := `{"rationale":"r","action":{"type":"ask_user","content":"  "}}`
	if _, err := ParseAction(raw); !errors.Is(err, ErrMissingQuestion) {
		t.Fatalf("err = %v, want ErrMissingQuestion", err)
	}
}
```

(Add `"errors"` to the test file's imports if absent.)

- [ ] **Step 2: Run to verify failure**

Run: `CGO_ENABLED=1 go test ./internal/agent/ -run TestParseActionAskUser -v`
Expected: compile error (`ActionAskUser`, `ErrMissingQuestion` undefined). Red; proceed.

- [ ] **Step 3: Implement protocol support**

In `internal/agent/protocol.go`:

```go
const (
	ActionAnswer   ActionType = "answer"
	ActionToolCall ActionType = "tool_call"
	ActionPatch    ActionType = "patch"
	ActionFinal    ActionType = "final"
	ActionAskUser  ActionType = "ask_user"
)
```

add to the `var` error block:

```go
	ErrMissingQuestion = errors.New("agent: ask_user action missing question content")
```

and in `validatePayload`, extend the type switch's allow-list and add the content check after the existing `ErrMissingTool` check:

```go
	case ActionAnswer, ActionToolCall, ActionPatch, ActionFinal, ActionAskUser:
	...
	if p.Type == ActionAskUser && strings.TrimSpace(p.Content) == "" {
		return ModelAction{}, ErrMissingQuestion
	}
```

- [ ] **Step 4: Prompt changes**

In `internal/agent/prompts.go`:

(a) Add to `baseRules` (after the "If stuck after a few attempts, stop and ask the user." line, replacing that line to make the mechanism concrete):

```
- If the request is ambiguous, or a decision would materially change the outcome, ask the user with an "ask_user" action instead of guessing. Ask one specific question at a time.
```

(b) Add an example to `baseOutputFormat`, after the existing four examples:

```
{"rationale": "Two valid interpretations with different implementations.", "action": {"type": "ask_user", "content": "Should deletion archive the record or remove it permanently?"}}
```

(c) In `roleAddenda`, change only `RoleGeneral`'s `allowedActions` to `[]string{"answer", "tool_call", "patch", "final", "ask_user"}`. Swarm roles stay unchanged.

- [ ] **Step 5: Run, format, vet, commit**

Run: `CGO_ENABLED=1 go test -count=1 ./internal/agent/...` — expect all PASS (prompts tests may assert prompt content; if `prompts_test.go` snapshots `baseRules`/`baseOutputFormat` text, update those assertions to include the new lines — that is the intended change, not a regression).

```bash
gofmt -w internal/agent
go vet ./internal/agent/...
git add internal/agent/protocol.go internal/agent/protocol_test.go internal/agent/prompts.go internal/agent/prompts_test.go
git commit -m "feat(agent): ask_user action type in the protocol and prompts"
```

---

### Task 2: session.PendingQuestion

**Files:**
- Modify: `internal/app/session/session.go`
- Test: `internal/app/session/session_test.go`

**Interfaces:**
- Consumes: the `PendingToolCall` pattern (session.go: struct + `SetPendingApproval`/`PendingApproval` accessors guarded by `s.mu`); `Activity`/`ActivityKind`.
- Produces:
  - `type PendingQuestion struct { Question string; ResponseChan chan string }`
  - `func (s *State) SetPendingQuestion(q *PendingQuestion)` / `func (s *State) PendingQuestion() *PendingQuestion`
  - `ActivityQuestion ActivityKind = "question"` (add beside the existing ActivityKind constants — locate with `grep -n "ActivityApproval\|ActivityKind = " internal/app/session/session.go`).

- [ ] **Step 1: Write the failing test**

Append to `internal/app/session/session_test.go`:

```go
func TestPendingQuestionRoundTrip(t *testing.T) {
	s := newTestState(t) // use this file's existing State constructor helper; if none exists, construct with New(config.Default(), t.TempDir(), time.Now(), Persistence{})
	if s.PendingQuestion() != nil {
		t.Fatal("expected no pending question initially")
	}
	q := &PendingQuestion{Question: "archive or delete?", ResponseChan: make(chan string, 1)}
	s.SetPendingQuestion(q)
	if got := s.PendingQuestion(); got == nil || got.Question != "archive or delete?" {
		t.Fatalf("PendingQuestion = %+v", got)
	}
	s.SetPendingQuestion(nil)
	if s.PendingQuestion() != nil {
		t.Fatal("expected pending question cleared")
	}
}
```

- [ ] **Step 2: Run to verify failure, then implement**

Run: `CGO_ENABLED=1 go test ./internal/app/session/ -run TestPendingQuestion -v` — expect compile error.

In `internal/app/session/session.go`:

(a) Next to `PendingToolCall`:

```go
// PendingQuestion is a clarifying question from the agent awaiting the
// user's free-text answer. The runner blocks on ResponseChan; the TUI sends
// exactly one value ("" means the user declined to answer).
type PendingQuestion struct {
	Question     string
	ResponseChan chan string
}
```

(b) A `pendingQuestion *PendingQuestion` field in `State` (next to `pendingApproval`).

(c) Accessors, mirroring the approval pair exactly:

```go
func (s *State) SetPendingQuestion(q *PendingQuestion) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pendingQuestion = q
}

func (s *State) PendingQuestion() *PendingQuestion {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pendingQuestion
}
```

(d) `ActivityQuestion ActivityKind = "question"` beside the other ActivityKind constants.

- [ ] **Step 3: Run, format, vet, commit**

Run: `CGO_ENABLED=1 go test -count=1 ./internal/app/session/` — expect PASS.

```bash
gofmt -w internal/app/session
go vet ./internal/app/session/...
git add internal/app/session/session.go internal/app/session/session_test.go
git commit -m "feat(session): pending-question state for ask_user"
```

---

### Task 3: Runner handling

**Files:**
- Modify: `internal/agent/runner.go` (`ActionAskUser` case in the action dispatch, `requestAnswer` helper)
- Test: `internal/agent/runner_test.go`

**Interfaces:**
- Consumes: `ActionAskUser` (Task 1), `session.PendingQuestion` / `SetPendingQuestion` / `ActivityQuestion` (Task 2).
- Produces: turn behavior — question rendered to transcript as an assistant message, answer as a user message; loop continues with `User answered: <answer>` appended to `messages`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/agent/runner_test.go`:

```go
// answerPendingQuestion polls state until the runner blocks on a question,
// then answers it. Returns a channel that yields the question text.
func answerPendingQuestion(state *session.State, answer string) <-chan string {
	questionCh := make(chan string, 1)
	go func() {
		for {
			if q := state.PendingQuestion(); q != nil {
				questionCh <- q.Question
				q.ResponseChan <- answer
				state.SetPendingQuestion(nil)
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()
	return questionCh
}

func TestRunHandlesAskUserAction(t *testing.T) {
	state := newTestState(t)
	p := &scriptedProvider{responses: []string{
		`{"rationale":"ambiguous","action":{"type":"ask_user","content":"Archive or delete?"}}`,
		`{"rationale":"done","action":{"type":"final","content":"Archived as requested."}}`,
	}}
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.SetForceClass(string(ClassQuestion))

	questionCh := answerPendingQuestion(state, "archive")

	task, err := r.RunTask(context.Background(), "clean up old records")
	if err != nil {
		t.Fatalf("RunTask err = %v", err)
	}
	if got := <-questionCh; got != "Archive or delete?" {
		t.Fatalf("question = %q", got)
	}
	if task.Summary != "Archived as requested." {
		t.Fatalf("Summary = %q", task.Summary)
	}
	// The answer must reach the model on the next call.
	second := p.requests[len(p.requests)-1]
	found := false
	for _, m := range second.Messages {
		if strings.Contains(m.Content, "User answered: archive") {
			found = true
		}
	}
	if !found {
		t.Fatal("answer not fed back to the model")
	}
	// And the transcript must show both sides.
	var sawQuestion, sawAnswer bool
	for _, m := range state.Messages() {
		if m.Role == session.RoleAssistant && strings.Contains(m.Content, "Archive or delete?") {
			sawQuestion = true
		}
		if m.Role == session.RoleUser && m.Content == "archive" {
			sawAnswer = true
		}
	}
	if !sawQuestion || !sawAnswer {
		t.Fatalf("transcript missing question(%v)/answer(%v)", sawQuestion, sawAnswer)
	}
}

func TestRunAskUserDeclinedContinues(t *testing.T) {
	state := newTestState(t)
	p := &scriptedProvider{responses: []string{
		`{"rationale":"ambiguous","action":{"type":"ask_user","content":"Archive or delete?"}}`,
		`{"rationale":"done","action":{"type":"final","content":"Proceeded with best judgment."}}`,
	}}
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.SetForceClass(string(ClassQuestion))

	_ = answerPendingQuestion(state, "") // empty answer = declined

	task, err := r.RunTask(context.Background(), "clean up old records")
	if err != nil {
		t.Fatalf("RunTask err = %v", err)
	}
	if task.Summary != "Proceeded with best judgment." {
		t.Fatalf("Summary = %q", task.Summary)
	}
	second := p.requests[len(p.requests)-1]
	found := false
	for _, m := range second.Messages {
		if strings.Contains(m.Content, "declined to answer") {
			found = true
		}
	}
	if !found {
		t.Fatal("declined marker not fed back to the model")
	}
}

func TestRunAskUserCancelledByContext(t *testing.T) {
	state := newTestState(t)
	p := &scriptedProvider{responses: []string{
		`{"rationale":"ambiguous","action":{"type":"ask_user","content":"Archive or delete?"}}`,
	}}
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.SetForceClass(string(ClassQuestion))

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for state.PendingQuestion() == nil {
			time.Sleep(time.Millisecond)
		}
		cancel()
	}()
	if _, err := r.RunTask(ctx, "clean up"); err == nil {
		t.Fatal("expected error on cancelled question wait")
	}
	if state.PendingQuestion() != nil {
		t.Fatal("pending question must be cleared on cancellation")
	}
}

func TestSwarmRolesCannotAskUser(t *testing.T) {
	state := newTestState(t)
	p := &scriptedProvider{responses: []string{
		`{"rationale":"ambiguous","action":{"type":"ask_user","content":"Which file?"}}`,
		`{"rationale":"done","action":{"type":"final","content":"Findings reported."}}`,
	}}
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.Role = RoleRepoScout
	r.SetForceClass(string(ClassQuestion))

	if _, err := r.RunTask(context.Background(), "scout the repo"); err != nil {
		t.Fatalf("RunTask err = %v", err)
	}
	second := p.requests[len(p.requests)-1]
	found := false
	for _, m := range second.Messages {
		if strings.Contains(m.Content, "ask_user is not available") {
			found = true
		}
	}
	if !found {
		t.Fatal("expected a correction telling the role ask_user is unavailable")
	}
}
```

Add `"time"` to runner_test.go's imports if absent.

- [ ] **Step 2: Run to verify failure**

Run: `CGO_ENABLED=1 go test ./internal/agent/ -run 'TestRunHandlesAskUser|TestRunAskUser|TestSwarmRolesCannotAskUser' -v -timeout 30s`
Expected: FAIL — the loop hits the `default:` unsupported-action branch, so the model never sees the question; tests fail on their assertions (not by hanging; keep the `-timeout`).

- [ ] **Step 3: Implement the runner case**

In `internal/agent/runner.go`, in the `switch action.Type` dispatch inside `RunTask`, add a case before `default:`:

```go
		case ActionAskUser:
			if r.role() != RoleGeneral {
				messages = append(messages, BuildCorrectionMessage(fmt.Errorf("ask_user is not available for the %s role; proceed with your best judgment or report findings", r.role())))
				continue
			}
			answer, waitErr := r.requestAnswer(ctx, action.Content)
			if waitErr != nil {
				return task, r.fail(task, waitErr)
			}
			if strings.TrimSpace(answer) == "" {
				messages = append(messages, schema.ChatMessage{Role: schema.RoleUser, Content: "The user declined to answer. Proceed with your best judgment and state the assumption you made."})
			} else {
				r.State.AddMessage(session.RoleUser, answer, session.ContentTypePlain)
				messages = append(messages, schema.ChatMessage{Role: schema.RoleUser, Content: "User answered: " + answer})
			}
```

and add the helper next to `requestApproval` (same protocol shape):

```go
// requestAnswer blocks until the TUI resolves the pending question or ctx is
// cancelled. It mirrors requestApproval's protocol: set pending state, wait
// on the channel, clear pending state.
func (r *Runner) requestAnswer(ctx context.Context, question string) (string, error) {
	r.State.AddMessage(session.RoleAssistant, question, session.ContentTypeMarkdown)
	q := &session.PendingQuestion{
		Question:     question,
		ResponseChan: make(chan string, 1),
	}
	r.State.SetPendingQuestion(q)
	r.State.SetActivity(session.Activity{Kind: session.ActivityQuestion, Label: "waiting for your answer", StartedAt: r.Now()})

	select {
	case answer := <-q.ResponseChan:
		r.State.SetPendingQuestion(nil)
		r.State.SetActivity(session.Activity{Kind: session.ActivityIdle})
		return answer, nil
	case <-ctx.Done():
		r.State.SetPendingQuestion(nil)
		r.State.SetActivity(session.Activity{Kind: session.ActivityIdle})
		return "", ctx.Err()
	}
}
```

- [ ] **Step 4: Run, format, vet, commit**

Run: `CGO_ENABLED=1 go test -count=1 ./internal/agent/... -timeout 60s` — expect all PASS.

```bash
gofmt -w internal/agent
go vet ./internal/agent/...
git add internal/agent/runner.go internal/agent/runner_test.go
git commit -m "feat(agent): runner blocks on ask_user and feeds the answer back"
```

---

### Task 4: TUI question panel and input routing

**Files:**
- Modify: `internal/app/tui/model.go` (`Update` key routing, view branch)
- Modify: `internal/app/tui/transcript.go` (add `renderQuestionPanel`)
- Test: `internal/app/tui/model_test.go` (or the tui package's existing test file for Update-level tests — find where approval-flow tests live with `grep -rn "PendingApproval" internal/app/tui/*_test.go` and put these beside them)

**Interfaces:**
- Consumes: `state.PendingQuestion()` (Task 2), the approval edit-mode routing pattern (model.go — `m.editingCommand` block), `renderApprovalPanel` styling (transcript.go).
- Produces: while a question is pending — typing goes to the input, Enter sends the answer, Esc sends `""`; the panel renders above the input.

- [ ] **Step 1: Write the failing test**

Add beside the tui package's existing approval-flow tests (mirror their Model construction — they build a `Model` via `New(state, ...)` and drive `Update` with `tea.KeyMsg`; copy that setup exactly):

```go
func TestPendingQuestionEnterSubmitsAnswer(t *testing.T) {
	state := newTestState(t) // this package's existing helper; if named differently, use that
	m := New(state)

	q := &session.PendingQuestion{Question: "Archive or delete?", ResponseChan: make(chan string, 1)}
	state.SetPendingQuestion(q)

	// Type "archive" then Enter.
	for _, r := range "archive" {
		model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = model.(Model)
	}
	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = model.(Model)

	select {
	case got := <-q.ResponseChan:
		if got != "archive" {
			t.Fatalf("answer = %q, want archive", got)
		}
	default:
		t.Fatal("no answer sent on Enter")
	}
	if state.PendingQuestion() != nil {
		t.Fatal("pending question not cleared after submit")
	}
}

func TestPendingQuestionEscDeclines(t *testing.T) {
	state := newTestState(t)
	m := New(state)
	q := &session.PendingQuestion{Question: "Archive or delete?", ResponseChan: make(chan string, 1)}
	state.SetPendingQuestion(q)

	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	_ = model

	select {
	case got := <-q.ResponseChan:
		if got != "" {
			t.Fatalf("answer = %q, want empty (declined)", got)
		}
	default:
		t.Fatal("no decline sent on Esc")
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `CGO_ENABLED=1 go test ./internal/app/tui/ -run TestPendingQuestion -v`
Expected: FAIL — Enter currently submits the input as a new user prompt / Esc does nothing; no value arrives on the channel.

- [ ] **Step 3: Implement Update routing**

In `internal/app/tui/model.go`, inside the `case tea.KeyMsg:` handler, AFTER the settings/memory-overlay blocks and BEFORE the `if tc != nil` approval block, add:

```go
		if q := m.state.PendingQuestion(); q != nil {
			switch msg.Type {
			case tea.KeyEnter:
				q.ResponseChan <- strings.TrimSpace(m.input.Value())
				m.state.SetPendingQuestion(nil)
				m.input.Reset()
				m.input.Placeholder = "Ask Marshal..."
				m.resizeInputHeight()
				m.updateViewportHeight()
				m.lastTranscriptHash = 0
				return m, nil
			case tea.KeyEsc:
				q.ResponseChan <- ""
				m.state.SetPendingQuestion(nil)
				m.input.Reset()
				m.input.Placeholder = "Ask Marshal..."
				m.resizeInputHeight()
				m.updateViewportHeight()
				m.lastTranscriptHash = 0
				return m, nil
			}
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			m.resizeInputHeight()
			m.updateViewportHeight()
			return m, cmd
		}
```

Also set the placeholder when the panel appears: in the same file's periodic refresh (the `agentTickMsg` case), after the activity handling, add:

```go
		if m.state.PendingQuestion() != nil && m.input.Placeholder != "Type your answer..." {
			m.input.Placeholder = "Type your answer..."
		}
```

- [ ] **Step 4: Implement the panel and view branch**

In `internal/app/tui/transcript.go`, next to `renderApprovalPanel`:

```go
// renderQuestionPanel renders the agent's pending clarifying question above
// the input. Styling follows renderApprovalPanel's frame conventions —
// compare with it when adjusting.
func renderQuestionPanel(q *session.PendingQuestion, width int) string {
	title := lipgloss.NewStyle().Bold(true).Render("Marshal asks:")
	body := lipgloss.NewStyle().Width(max(width-2, 1)).Render(q.Question)
	hint := lipgloss.NewStyle().Faint(true).Render("type your answer and press Enter · Esc to skip")
	return lipgloss.JoinVertical(lipgloss.Left, title, body, hint)
}
```

In `internal/app/tui/model.go`'s `View`, find the branch `if tc := m.state.PendingApproval(); tc != nil { content = renderApprovalPanel(tc, inputInnerWidth) }` and add before it:

```go
	if q := m.state.PendingQuestion(); q != nil {
		content = renderQuestionPanel(q, inputInnerWidth)
	} else ...
```

(keep the existing approval branch as the `else if`; read the surrounding code — the exact variable names for the panel slot are visible there and must be reused).

- [ ] **Step 5: Run, format, vet, commit**

Run: `CGO_ENABLED=1 go test -count=1 ./internal/app/tui/...` — expect all PASS.

Manual check (required — this is user-facing): `go run ./cmd/marshal` with any configured model, or if none is configured, temporarily verify via the test only and note it. Preferred: trigger a turn whose model asks a question and confirm the panel renders, typing works, Enter resumes the turn, Esc resumes with the declined message.

```bash
gofmt -w internal/app/tui
go vet ./internal/app/tui/...
git add internal/app/tui/model.go internal/app/tui/transcript.go internal/app/tui/model_test.go
git commit -m "feat(tui): render pending ask_user questions and route the answer"
```

---

## Verification

`CGO_ENABLED=1 go test -count=1 ./...` green. End-to-end: a model emitting `{"action":{"type":"ask_user","content":"..."}}` blocks the turn on a visible question panel; answering resumes the turn with `User answered: ...` in the next request; Esc resumes with the declined marker; Ctrl+C during a pending question still shuts down cleanly (ctx cancellation path). Swarm roles that try to ask get corrected and continue.
