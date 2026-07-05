# Tool Activity Indicator Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the static `WORKING`/`IDLE` badge with an animated spinner and dynamic activity labels, and wire up plan items from the agent runner to the Plan tab.

**Architecture:** The runner sets activity state (thinking/tool/approval/idle) on the shared session at phase boundaries. The TUI reads activity state on each tick, advances a braille spinner, and renders a dynamic badge in the status bar and Plan tab. A 2s `✓ done` transition provides feedback when activity ends.

**Tech Stack:** Go, Bubble Tea, lipgloss, existing `sync.Mutex`-guarded session state pattern.

## Global Constraints

- Must follow existing mutex-guarded get/set pattern on `session.State` (see `SetActiveRoute`/`ActiveRoute`)
- Plan items come from `task.Plan []string` set by the agent runner
- Spinner must advance on `agentTickMsg` (every 150ms)
- Done badge shows for exactly 2 seconds after activity transitions to idle
- Must not break existing layout constraints (terminal width/height bounds)
- All new code must have tests following existing test patterns in `*_test.go` files

---

### Task 1: Add Activity types + Plan storage to session.State

**Files:**
- Modify: `internal/app/session/session.go`

**Interfaces:**
- Produces: `ActivityKind` type, `Activity` struct, `State.SetActivity(a Activity)`, `State.Activity() Activity`, `State.SetPlan(plan []string)`, `State.Plan() []string`

- [ ] **Step 1: Add ActivityKind type and constants**

At the top of session.go, after the `Role` const block, add:

```go
type ActivityKind string

const (
	ActivityIdle     ActivityKind = "idle"
	ActivityThinking ActivityKind = "thinking"
	ActivityTool     ActivityKind = "tool"
	ActivityApproval ActivityKind = "approval"
)

type Activity struct {
	Kind      ActivityKind
	Label     string
	StartedAt time.Time
}
```

- [ ] **Step 2: Add activity and plan fields to State struct**

In the `State` struct, add to the mutex-guarded field block (after `turnToolCache`):

```go
	activity Activity
	plan     []string
```

- [ ] **Step 3: Add SetActivity and Activity methods**

Add after the `activeRoute` field accessor methods:

```go
func (s *State) SetActivity(a Activity) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.activity = a
}

func (s *State) Activity() Activity {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.activity
}
```

- [ ] **Step 4: Add SetPlan and Plan methods**

Add after the Activity methods:

```go
func (s *State) SetPlan(plan []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.plan = make([]string, len(plan))
	copy(s.plan, plan)
}

func (s *State) Plan() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := make([]string, len(s.plan))
	copy(p, s.plan)
	return p
}
```

- [ ] **Step 5: Verify it compiles**

Run: `go build ./cmd/marshal`
Expected: No errors (new methods are not yet called, so no compilation issues)

- [ ] **Step 6: Commit**

```bash
git add internal/app/session/session.go
git commit -m "feat: add Activity and Plan types with mutex-guarded accessors to session.State"
```

---

### Task 2: Write session tests for Activity and Plan

**Files:**
- Modify: `internal/app/session/session_test.go`

**Interfaces:**
- Consumes: `Activity`, `ActivityKind`, `State.SetActivity()`, `State.Activity()`, `State.SetPlan()`, `State.Plan()`

- [ ] **Step 1: Write Activity round-trip test**

Add at the end of session_test.go (before the final closing brace of the last test function):

```go
func TestStateActivityRoundTrip(t *testing.T) {
	state := New(config.Default(), "/repo", time.Unix(100, 0), Persistence{})

	got := state.Activity()
	if got.Kind != ActivityIdle {
		t.Fatalf("initial Activity().Kind = %q, want idle", got.Kind)
	}

	act := Activity{Kind: ActivityTool, Label: "shell.run: go test", StartedAt: time.Unix(200, 0)}
	state.SetActivity(act)

	got = state.Activity()
	if got.Kind != ActivityTool || got.Label != "shell.run: go test" {
		t.Fatalf("Activity() = %#v", got)
	}
}
```

- [ ] **Step 2: Write Activity zero value test**

```go
func TestStateActivityZeroValueIsIdle(t *testing.T) {
	state := New(config.Default(), "/repo", time.Unix(100, 0), Persistence{})

	state.SetActivity(Activity{})
	got := state.Activity()
	if got.Kind != ActivityIdle || got.Label != "" {
		t.Fatalf("Activity() = %#v, want zero/idle", got)
	}
}
```

- [ ] **Step 3: Write Plan round-trip test**

```go
func TestStatePlanRoundTrip(t *testing.T) {
	state := New(config.Default(), "/repo", time.Unix(100, 0), Persistence{})

	if len(state.Plan()) != 0 {
		t.Fatal("initial Plan() should be empty")
	}

	plan := []string{"Refactor layout", "Add tests", "Update docs"}
	state.SetPlan(plan)

	got := state.Plan()
	if len(got) != 3 || got[0] != "Refactor layout" || got[1] != "Add tests" {
		t.Fatalf("Plan() = %v", got)
	}

	plan[0] = "mutated"
	gotAgain := state.Plan()
	if gotAgain[0] != "Refactor layout" {
		t.Fatalf("Plan() returned mutable internal slice: %v", gotAgain)
	}
}
```

- [ ] **Step 4: Write concurrency safety test**

```go
func TestStateActivityIsRaceFree(t *testing.T) {
	state := New(config.Default(), "/repo", time.Unix(100, 0), Persistence{})

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 100; i++ {
			state.SetActivity(Activity{Kind: ActivityThinking, Label: "thinking..."})
			state.SetActivity(Activity{Kind: ActivityTool, Label: "shell.run: go test"})
			state.SetActivity(Activity{})
		}
	}()

	for i := 0; i < 100; i++ {
		_ = state.Activity()
		_ = state.Plan()
	}
	<-done
}
```

- [ ] **Step 5: Run session tests**

Run: `go test ./internal/app/session/ -v -count=1`
Expected: All tests pass, including 4 new ones

- [ ] **Step 6: Commit**

```bash
git add internal/app/session/session_test.go
git commit -m "test: add Activity and Plan round-trip and concurrency tests"
```

---

### Task 3: Create Spinner helper

**Files:**
- Create: `internal/app/tui/spinner.go`
- Create: `internal/app/tui/spinner_test.go`

**Interfaces:**
- Produces: `Spinner` struct with `Next() string` method

- [ ] **Step 1: Create spinner.go**

```go
package tui

type Spinner struct {
	frames []string
	index  int
}

func NewSpinner() Spinner {
	return Spinner{frames: []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}}
}

func (s *Spinner) Next() string {
	frame := s.frames[s.index]
	s.index = (s.index + 1) % len(s.frames)
	return frame
}
```

- [ ] **Step 2: Create spinner_test.go**

```go
package tui

import "testing"

func TestSpinnerFramesWrap(t *testing.T) {
	s := NewSpinner()
	first := s.Next()
	for i := 0; i < 9; i++ {
		s.Next()
	}
	second := s.Next()
	if first != second {
		t.Fatalf("after 10 calls (len(frames)), Next() = %q, want %q (full wrap)", second, first)
	}
}

func TestSpinnerFramesAreNotEmpty(t *testing.T) {
	s := NewSpinner()
	for i := 0; i < 20; i++ {
		f := s.Next()
		if f == "" {
			t.Fatalf("frame %d is empty", i)
		}
	}
}
```

- [ ] **Step 3: Run spinner tests**

Run: `go test ./internal/app/tui/ -run TestSpinner -v -count=1`
Expected: Both tests pass

- [ ] **Step 4: Commit**

```bash
git add internal/app/tui/spinner.go internal/app/tui/spinner_test.go
git commit -m "feat: add Spinner helper with braille frame animation"
```

---

### Task 4: Set activity at runner phase boundaries

**Files:**
- Modify: `internal/agent/runner.go`

**Interfaces:**
- Consumes: `session.Activity`, `session.ActivityKind`, `State.SetActivity()`

- [ ] **Step 1: Add ActivityIdle cleanup defer in Run**

In `Run()` (around line 109), add as the first line after the function signature:

```go
func (r *Runner) Run(ctx context.Context, goal string) error {
	defer r.State.SetActivity(session.Activity{Kind: session.ActivityIdle})
```

- [ ] **Step 2: Set ActivityThinking in chatOnce**

In `chatOnce()` (line 297), after `r.State.BeginStreaming()` and before the `defer r.State.EndStreaming()`:

```go
	r.State.BeginStreaming()
	r.State.SetActivity(session.Activity{Kind: session.ActivityThinking, Label: "thinking...", StartedAt: r.Now()})
	defer r.State.EndStreaming()
	defer r.State.SetActivity(session.Activity{Kind: session.ActivityIdle})
```

Note: the order of `defer` calls is LIFO, so `SetActivity` fires before `EndStreaming` -- this is correct.

- [ ] **Step 3: Set ActivityTool in executeToolCall**

In `executeToolCall()` (line 339), add after the policy evaluation block and before the `call`/`tool.Handler` invocation. Insert after the `case policy.DecisionAllow:` block (around line 420) and before the `call :=` line (line 423):

```go
	label := toolName
	if command, ok := argsMap["command"].(string); ok && command != "" {
		label = fmt.Sprintf("%s: %s", toolName, command)
	}
	r.State.SetActivity(session.Activity{Kind: session.ActivityTool, Label: label, StartedAt: r.Now()})
	defer r.State.SetActivity(session.Activity{Kind: session.ActivityIdle})
```

This `defer` must be placed after all the `return` paths in the approval/deny checks. The correct insertion point is right before line 423 (`call := registry.ToolCall{...}`).

- [ ] **Step 4: Set ActivityApproval in requestApproval**

In `requestApproval()` (line 529), after `r.State.SetPendingApproval(tc)` (line 554) and before the `select` block (line 556):

```go
	r.State.SetPendingApproval(tc)

	label := fmt.Sprintf("waiting for approval: %s", command)
	r.State.SetActivity(session.Activity{Kind: session.ActivityApproval, Label: label, StartedAt: r.Now()})
```

Then in the `select` block, add `SetActivity(ActivityIdle)` in both cases:

```go
	select {
	case decision := <-tc.ResponseChan:
		r.State.SetPendingApproval(nil)
		r.State.SetActivity(session.Activity{Kind: session.ActivityIdle})
		return decision.Approved, decision.Edited, nil
	case <-ctx.Done():
		r.State.SetPendingApproval(nil)
		r.State.SetActivity(session.Activity{Kind: session.ActivityIdle})
		return false, "", ctx.Err()
	}
```

- [ ] **Step 5: Set plan on session.State after planning**

In `Run()`, after `task.Plan = splitPlanLines(planText)` (line 135), add:

```go
		task.Plan = splitPlanLines(planText)
		r.State.SetPlan(task.Plan)
```

- [ ] **Step 6: Verify it compiles**

Run: `go build ./cmd/marshal`
Expected: No errors

- [ ] **Step 7: Commit**

```bash
git add internal/agent/runner.go
git commit -m "feat: set activity state at runner phase boundaries and wire plan to session"
```

---

### Task 5: Add and run runner activity tests

**Files:**
- Modify: `internal/agent/runner_test.go`

**Interfaces:**
- Consumes: `session.Activity`, `session.ActivityKind`, `State.SetActivity()`, `State.Activity()`, `State.SetPlan()`, `State.Plan()`

- [ ] **Step 1: Write test for ActivityThinking transition in chatOnce**

Add at the end of runner_test.go:

```go
func TestRunnerChatOnceSetsThinkingActivity(t *testing.T) {
	p := &scriptedProvider{
		responses: []string{`{"rationale":"r","action":{"type":"answer","content":"done"}}`},
	}
	reg := registry.New()
	pol := policy.NewEngine(&config.Config{}, nil)
	state := newTestState(t)
	runner := NewRunner(p, reg, pol, state, "test-model")

	_, err := runner.chatOnce(context.Background(), p, "test-model", []schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}})
	if err != nil {
		t.Fatalf("chatOnce returned error: %v", err)
	}

	act := state.Activity()
	if act.Kind != session.ActivityIdle {
		t.Fatalf("activity after chatOnce = %q, want idle", act.Kind)
	}

	if got := state.InProgress().Reasoning; got == "" {
		t.Fatalf("thinking was not captured")
	}
}
```

- [ ] **Step 2: Write test for ActivityTool transition**

```go
func TestRunnerSetsActivityDuringToolExecute(t *testing.T) {
	p := &scriptedProvider{
		responses: []string{
			`{"rationale":"let me check","action":{"type":"tool_call","tool":"file.read","args":{"path":"main.go"}}}`,
			`{"rationale":"done","action":{"type":"answer","content":"done"}}`,
		},
	}
	reg := registry.New()
	reg.Register(registry.Tool{
		Name:        "file.read",
		Description: "read a file",
		Parameters:  registry.Parameters{Properties: map[string]registry.Property{"path": {Type: "string"}}},
		Risk:        registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{Summary: "ok"}, nil
		},
	})
	pol := policy.NewEngine(&config.Config{}, nil)
	state := newTestState(t)
	runner := NewRunner(p, reg, pol, state, "test-model")
	runner.MaxToolIterations = 2

	err := runner.Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	act := state.Activity()
	if act.Kind != session.ActivityIdle {
		t.Fatalf("activity after run = %q, want idle", act.Kind)
	}
}
```

- [ ] **Step 3: Write test for ActivityApproval transition**

```go
func TestRunnerSetsActivityDuringApproval(t *testing.T) {
	p := &scriptedProvider{
		responses: []string{
			`{"rationale":"need to run","action":{"type":"tool_call","tool":"shell.run","args":{"command":"go test"}}}`,
			`{"rationale":"done","action":{"type":"answer","content":"done"}}`,
		},
	}
	reg := registry.New()
	reg.Register(registry.Tool{
		Name:        "shell.run",
		Description: "run a command",
		Parameters:  registry.Parameters{Properties: map[string]registry.Property{"command": {Type: "string"}}},
		Risk:        registry.RiskWrite,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{Summary: "ok"}, nil
		},
	})
	pol := policy.NewEngine(&config.Config{}, nil) // default config returns DecisionConfirm for shell.run
	state := newTestState(t)
	runner := NewRunner(p, reg, pol, state, "test-model")
	runner.MaxToolIterations = 2

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		defer close(done)
		err := runner.Run(ctx, "hi")
		if err != nil {
			t.Logf("Run returned: %v", err)
		}
	}()

	time.Sleep(100 * time.Millisecond)

	act := state.Activity()
	if act.Kind == session.ActivityApproval {
		t.Logf("activity is approval: %v", act.Label)
	}

	tc := state.PendingApproval()
	if tc == nil {
		t.Fatalf("expected pending approval")
	}

	tc.ResponseChan <- session.UserApprovalDecision{Approved: false}

	time.Sleep(100 * time.Millisecond)

	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return")
	}

	act = state.Activity()
	if act.Kind != session.ActivityIdle {
		t.Fatalf("activity after approval = %q, want idle", act.Kind)
	}
}
```

- [ ] **Step 4: Write test for plan wiring**

```go
func TestRunnerSetsPlanAfterPlanningPhase(t *testing.T) {
	p := &scriptedProvider{
		responses: []string{
			"Refactor the layout\nAdd tests\nupdate docs",
			`{"rationale":"done","action":{"type":"answer","content":"done"}}`,
		},
	}
	reg := registry.New()
	pol := policy.NewEngine(&config.Config{}, nil)
	state := newTestState(t)
	runner := NewRunner(p, reg, pol, state, "test-model")
	runner.MaxToolIterations = 2

	err := runner.Run(context.Background(), "build a feature")
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	plan := state.Plan()
	if len(plan) != 3 {
		t.Fatalf("Plan() length = %d, want 3: %v", len(plan), plan)
	}
	if plan[0] != "Refactor the layout" || plan[1] != "Add tests" || plan[2] != "update docs" {
		t.Fatalf("Plan() = %v", plan)
	}
}
```

- [ ] **Step 5: Run runner tests**

Run: `go test ./internal/agent/ -run "TestRunnerChatOnceSetsThinking|TestRunnerSetsActivityDuring|TestRunnerSetsPlan" -v -count=1`
Expected: All 4 tests pass

- [ ] **Step 6: Commit**

```bash
git add internal/agent/runner_test.go
git commit -m "test: verify activity transitions and plan wiring in agent runner"
```

---

### Task 6: Add spinner, activity tracking fields, and tick logic to TUI Model

**Files:**
- Modify: `internal/app/tui/model.go`

**Interfaces:**
- Consumes: `Spinner` (from spinner.go), `session.Activity`, `session.ActivityKind`

- [ ] **Step 1: Add new fields to Model struct**

In the `Model` struct (around line 43), add after the existing fields within the struct:

```go
	spinner           Spinner
	spinnerFrame      string
	lastActivityLabel string
	lastActivityDone  time.Time
	lastActivityKind  session.ActivityKind
```

- [ ] **Step 2: Initialize spinner in New**

In the `New()` function (line 112), in the `Model{...}` literal, add:

```go
		spinner: NewSpinner(),
```

- [ ] **Step 3: Advance spinner in agentTickMsg handler**

In the `Update()` method, in the `case agentTickMsg:` block (line 213), update to:

```go
	case agentTickMsg:
		if !m.busy {
			return m, nil
		}
		m.spinnerFrame = m.spinner.Next()
		act := m.state.Activity()
		if act.Kind == session.ActivityIdle && m.lastActivityKind != session.ActivityIdle && m.lastActivityKind != "" {
			m.lastActivityDone = time.Now()
		}
		m.lastActivityKind = act.Kind
		if act.Kind != session.ActivityIdle && act.Label != "" {
			m.lastActivityLabel = act.Label
		}
		m.refreshViewport()
		return m, tickCmd()
```

- [ ] **Step 4: Capture done state in agentFinishedMsg handler**

In the `case agentFinishedMsg:` block (line 206), update to:

```go
	case agentFinishedMsg:
		m.busy = false
		if msg.err != nil {
			m.state.SetProviderError(msg.err)
		}
		if m.lastActivityKind != session.ActivityIdle && m.lastActivityKind != "" {
			m.lastActivityDone = time.Now()
			m.lastActivityKind = session.ActivityIdle
		}
		m.refreshViewport()
		return m, nil
```

- [ ] **Step 5: Add doneDisplayDuration constant**

At the top of the file, in the `const` block that contains `minTerminalWidth`, add:

```go
	doneDisplayDuration = 2 * time.Second
```

- [ ] **Step 6: Verify it compiles**

Run: `go build ./cmd/marshal`
Expected: No errors (unused fields are fine; they'll be used in Task 7 and 8)

- [ ] **Step 7: Commit**

```bash
git add internal/app/tui/model.go
git commit -m "feat: add spinner, activity tracking fields, and tick logic to TUI model"
```

---

### Task 7: Replace static status bar with dynamic activity badge

**Files:**
- Modify: `internal/app/tui/model.go`

**Interfaces:**
- Consumes: `Model.spinnerFrame`, `Model.lastActivityLabel`, `Model.lastActivityDone`, `Model.lastActivityKind`, `session.State.Activity()`

- [ ] **Step 1: Rewrite renderStatusBar as a method using activity state**

Replace the existing `renderStatusBar` function (lines 662-691) with:

```go
func (m Model) renderStatusBar(width int) string {
	route := m.state.ActiveRoute()
	role := "inactive"
	modelProvider := "no model"
	locality := "remote-ok"
	if route.Active {
		role = string(route.Role)
		modelProvider = fmt.Sprintf("%s @ %s", route.Model, route.Provider)
		if route.LocalOnly {
			locality = "local"
		}
	} else if !m.state.Config.Privacy.RemoteProvidersAllowed {
		locality = "local"
	}

	activity := m.state.Activity()
	var busyText string
	switch activity.Kind {
	case session.ActivityIdle:
		if m.lastActivityLabel != "" && time.Since(m.lastActivityDone) < doneDisplayDuration {
			busyText = fmt.Sprintf("✓ %s", truncateRunes(m.lastActivityLabel, 30))
		} else {
			busyText = "IDLE"
		}
	case session.ActivityThinking:
		busyText = fmt.Sprintf("%s thinking...", m.spinnerFrame)
	case session.ActivityTool, session.ActivityApproval:
		label := activity.Label
		if label == "" {
			label = string(activity.Kind)
		}
		busyText = fmt.Sprintf("%s %s", m.spinnerFrame, truncateRunes(label, 30))
	default:
		busyText = "IDLE"
	}

	parts := []string{
		statusBarBrand.Render("MARSHAL"),
		" Auto ",
		fmt.Sprintf(" %s ", truncateRunes(role, 16)),
		fmt.Sprintf(" %s ", truncateRunes(modelProvider, 28)),
		fmt.Sprintf(" %s ", locality),
		statusBarBusy.Width(9).MaxWidth(9).Render(truncateRunes(busyText, 9)),
	}
	line := lipgloss.JoinHorizontal(lipgloss.Top, parts...)
	return statusBarBg.Width(width).MaxWidth(width).Render(truncateRunes(line, width))
}
```

- [ ] **Step 2: Update the call site in View()**

In `View()` (line 797), change:

```go
	statusBar := renderStatusBar(m.width, m.state, m.busy)
```

To:

```go
	statusBar := m.renderStatusBar(m.width)
```

- [ ] **Step 3: Verify it compiles**

Run: `go build ./cmd/marshal`
Expected: No errors

- [ ] **Step 4: Commit**

```bash
git add internal/app/tui/model.go
git commit -m "feat: replace static WORKING/IDLE with dynamic spinner+label activity badge"
```

---

### Task 8: Update Plan tab with plan items and activity indicator

**Files:**
- Modify: `internal/app/tui/model.go`

**Interfaces:**
- Consumes: `session.State.Plan()`, `session.State.Activity()`, `Model.spinnerFrame`

- [ ] **Step 1: Rewrite renderPlanTab to show plan items and dynamic activity**

Replace the existing `renderPlanTab` function (lines 966-978) with:

```go
func (m Model) renderPlanTab(width int, height int, tc *session.PendingToolCall) string {
	plan := m.state.Plan()
	activity := m.state.Activity()

	var b strings.Builder

	if len(plan) == 0 && activity.Kind == session.ActivityIdle {
		b.WriteString(mutedStyle.Render("No active plan. Ask the agent what to do next."))
		b.WriteString("\n\n→  Ready for input")
		return lipgloss.NewStyle().Width(width).MaxWidth(width).Height(height).MaxHeight(height).Render(b.String())
	}

	if len(plan) > 0 {
		b.WriteString(mutedStyle.Render("Current Plan:"))
		b.WriteString("\n")
		for _, item := range plan {
			b.WriteString(fmt.Sprintf(" ● %s\n", truncateRunes(item, max(width-4, 1))))
		}
	}

	if tc != nil {
		b.WriteString(fmt.Sprintf("\n→  Pending approval: %s", truncateRunes(tc.Command, max(width-22, 1))))
	} else if activity.Kind != session.ActivityIdle {
		label := activity.Label
		if activity.Kind == session.ActivityThinking {
			label = "thinking..."
		}
		b.WriteString(fmt.Sprintf("\n→  %s %s", m.spinnerFrame, truncateRunes(label, max(width-6, 1))))
	} else {
		b.WriteString("\n→  Ready for input")
	}

	return lipgloss.NewStyle().Width(width).MaxWidth(width).Height(height).MaxHeight(height).Render(b.String())
}
```

- [ ] **Step 2: Update renderPlanTab call site in renderRightInfoPanel**

In `renderRightInfoPanel()` (line 1033), change the call on line 1041:

```go
	case 0:
		body = m.renderPlanTab(innerWidth, bodyHeight, tc)
```

- [ ] **Step 3: Verify it compiles**

Run: `go build ./cmd/marshal`
Expected: No errors

- [ ] **Step 4: Commit**

```bash
git add internal/app/tui/model.go
git commit -m "feat: render plan items and dynamic activity indicator in Plan tab"
```

---

### Task 9: Write TUI tests for spinner, status bar, and plan tab

**Files:**
- Modify: `internal/app/tui/model_test.go`

**Interfaces:**
- Consumes: All from Tasks 6-8

- [ ] **Step 1: Write test for status bar showing spinner+thinking label when busy**

Add at the end of model_test.go:

```go
func TestStatusBarShowsSpinnerAndThinkingWhenBusy(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	state.SetActivity(session.Activity{Kind: session.ActivityThinking, Label: "thinking...", StartedAt: time.Now()})
	m := New(state)
	m.spinnerFrame = "⠋"
	m.busy = true
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 32})
	m = updated.(Model)

	view := m.View()
	if !strings.Contains(view, "⠋") {
		t.Fatalf("View() missing spinner frame in status bar:\n%s", view)
	}
	if !strings.Contains(view, "thinking...") {
		t.Fatalf("View() missing thinking label in status bar:\n%s", view)
	}
}
```

- [ ] **Step 2: Write test for status bar showing tool label**

```go
func TestStatusBarShowsToolLabel(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	state.SetActivity(session.Activity{Kind: session.ActivityTool, Label: "shell.run: go test ./...", StartedAt: time.Now()})
	m := New(state)
	m.spinnerFrame = "⠹"
	m.busy = true
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 32})
	m = updated.(Model)

	view := m.View()
	if !strings.Contains(view, "⠹") {
		t.Fatalf("View() missing spinner frame in status bar:\n%s", view)
	}
	if !strings.Contains(view, "shell.run") {
		t.Fatalf("View() missing tool label in status bar:\n%s", view)
	}
}
```

- [ ] **Step 3: Write test for done badge appearing after activity finishes**

```go
func TestStatusBarShowsDoneBadgeAfterActivity(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	m := New(state)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 32})
	m = updated.(Model)

	m.busy = true
	m.spinnerFrame = "⠏"
	state.SetActivity(session.Activity{Kind: session.ActivityTool, Label: "shell.run: go test", StartedAt: time.Now()})
	m.lastActivityKind = session.ActivityTool

	updated, _ = m.Update(agentFinishedMsg{})
	m = updated.(Model)

	view := m.View()
	if !strings.Contains(view, "✓") {
		t.Fatalf("View() missing done checkmark in status bar:\n%s", view)
	}
	if !strings.Contains(view, "shell.run") {
		t.Fatalf("View() missing tool label in done badge:\n%s", view)
	}
}
```

- [ ] **Step 4: Write test for done badge reverting to IDLE after 2s**

```go
func TestStatusBarDoneBadgeExpiresAfterDuration(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	m := New(state)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 32})
	m = updated.(Model)

	m.busy = true
	m.spinnerFrame = "⠏"
	state.SetActivity(session.Activity{Kind: session.ActivityTool, Label: "shell.run: go test", StartedAt: time.Now()})
	m.lastActivityKind = session.ActivityTool

	updated, _ = m.Update(agentFinishedMsg{})
	m = updated.(Model)

	if !strings.Contains(m.View(), "✓") {
		t.Fatal("expected done badge immediately after finish")
	}

	m.lastActivityDone = m.lastActivityDone.Add(-doneDisplayDuration).Add(-time.Millisecond)

	view := m.View()
	if strings.Contains(view, "✓") {
		t.Fatalf("done badge should have expired after %v:\n%s", doneDisplayDuration, view)
	}
	if !strings.Contains(view, "IDLE") {
		t.Fatalf("View() missing IDLE after done badge expiry:\n%s", view)
	}
}
```

- [ ] **Step 5: Write test for Plan tab showing plan items**

```go
func TestPlanTabShowsPlanItemsAndSpinner(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	state.SetPlan([]string{"Refactor layout", "Add tests", "Update docs"})
	state.SetActivity(session.Activity{Kind: session.ActivityTool, Label: "shell.run: go test", StartedAt: time.Now()})
	m := New(state)
	m.spinnerFrame = "⠙"
	m.busy = true
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 32})
	m = updated.(Model)

	view := m.View()
	for _, want := range []string{"Current Plan:", "Refactor layout", "Add tests", "Update docs", "shell.run: go test"} {
		if !strings.Contains(view, want) {
			t.Fatalf("Plan tab missing %q:\n%s", want, view)
		}
	}
}
```

- [ ] **Step 6: Write test for the "No active plan" fallback**

```go
func TestPlanTabShowsNoActivePlanWhenIdleAndEmpty(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	m := New(state)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 32})
	m = updated.(Model)

	view := m.View()
	if !strings.Contains(view, "No active plan") {
		t.Fatalf("Plan tab missing 'No active plan':\n%s", view)
	}
	if !strings.Contains(view, "Ready for input") {
		t.Fatalf("Plan tab missing 'Ready for input':\n%s", view)
	}
}
```

- [ ] **Step 7: Verify existing tests still pass**

The `TestPolishedRightPanelTracksActiveTab` test (line 239) checks for `"No active plan."` which matches the new text `"No active plan. Ask the agent what to do next."`. No change needed.

- [ ] **Step 8: Run all TUI tests**

Run: `go test ./internal/app/tui/ -v -count=1`
Expected: All tests pass, including new ones

- [ ] **Step 9: Commit**

```bash
git add internal/app/tui/model_test.go
git commit -m "test: verify dynamic status bar, done badge, and plan tab rendering"
```

---

### Task 10: Full build, vet, and final smoke test

**Files:**
- (none -- verification only)

- [ ] **Step 1: Run go vet**

Run: `go vet ./...`
Expected: No output (clean)

- [ ] **Step 2: Run full test suite**

Run: `go test ./... -count=1`
Expected: All tests pass

- [ ] **Step 3: Build binary**

Run: `go build ./cmd/marshal`
Expected: No errors

- [ ] **Step 4: Commit if any formatting/lint fixes were needed**

```bash
git add -A
git diff --cached --stat
git commit -m "chore: final formatting and vet fixes for activity indicator"
```
