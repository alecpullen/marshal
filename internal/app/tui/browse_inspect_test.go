package tui

import (
	"encoding/json"
	"testing"
	"time"

	"marshal/internal/acpwire"
	"marshal/internal/app/session"
	"marshal/internal/app/tui/inspector"
	"marshal/internal/db"
	"marshal/internal/strutil"
	"marshal/internal/tools/registry"
)

// The inspector is where the whole retained output lives: the report in the
// transcript is a bounded tail, and `i` is how a reader gets the rest.
func TestInspectorShowsACompletionsFullOutput(t *testing.T) {
	code := 1
	output := "line one\nline two\nline three"
	c := &acpwire.WireCompletion{
		Source: acpwire.CompletionJob, ID: "job-3", Label: "Harness tests",
		Outcome: "exit 1", Failed: true, ExitCode: &code,
		DurationMs: 48000,
	}
	d := completionDetail(c, &acpwire.CompletionDetail{Output: output})
	if d.Title != "job-3" || d.Subject != "Harness tests" {
		t.Fatalf("detail = %+v", d)
	}
	if got := sectionOf(d, "output"); got != strutil.ScrubBlock(output) {
		t.Fatalf("output section = %q", got)
	}
	if got := fieldOf(d, "exit"); got != "1" {
		t.Fatalf("exit field = %q, want 1", got)
	}
}

// `y` on an event copies its head line and the output behind it, so a failure
// can be pasted into an issue without retyping the command.
func TestCopyTakesTheCompletionHeadAndOutput(t *testing.T) {
	c := &acpwire.WireCompletion{
		Source: acpwire.CompletionJob, ID: "job-3", Label: "Harness tests",
		Outcome: "exit 1", Failed: true, DurationMs: 48000,
		Measure: "210 passed, 2 failed",
	}
	d := &acpwire.CompletionDetail{Output: "--- FAIL: TestThresholds"}
	want := "job-3 · Harness tests · exit 1 · 210 passed, 2 failed · 48s\n\n--- FAIL: TestThresholds"
	if got := completionCopyText(c, d); got != want {
		t.Fatalf("copy = %q, want %q", got, want)
	}
}

func fieldOf(d inspector.Detail, key string) string {
	for _, f := range d.Fields {
		if f.Key == key {
			return f.Value
		}
	}
	return ""
}

func sectionOf(d inspector.Detail, name string) string {
	for _, s := range d.Sections {
		if s.Name == name {
			return s.Body
		}
	}
	return ""
}

// wireToolKey is the browse cursor's identity for the row holding callID.
func wireToolKey(t *testing.T, m Model, callID string) nodeID {
	t.Helper()
	for _, w := range m.wire {
		if w.Tool == nil {
			continue
		}
		for _, c := range w.Tool.Calls {
			if c.CallID == callID {
				return nodeID{Kind: acpwire.KindTool, Key: w.ID}
			}
		}
	}
	t.Fatalf("the wire index holds no row for call %q", callID)
	return nodeID{}
}

// wireBeatKey is the browse cursor's identity for the only beat.
func wireBeatKey(t *testing.T, m Model) nodeID {
	t.Helper()
	for _, w := range m.wire {
		if w.Kind == acpwire.KindStep {
			return nodeID{Kind: acpwire.KindStep, Key: w.ID}
		}
	}
	t.Fatal("the wire index holds no beat")
	return nodeID{}
}

// withBeat logs a beat by "reviewer #1" that narrates and then reads a.go and
// runs the tests.
func withBeat(t *testing.T) Model {
	t.Helper()
	m := newTestModel(t)
	stateOf(m).AddMessage(session.RoleUser, "go", session.ContentTypePlain)
	id := stateOf(m).BeginStep(session.Actor{Label: "reviewer #1", Model: "qwen3", Provider: "local"})
	stateOf(m).AddNarration(id, "Checking the **config**. Then more.")
	stateOf(m).LogToolCall(registry.AuditEvent{
		Timestamp: time.Unix(102, 0), ToolName: "file.read", StepID: id, ToolCallID: "c1",
		Args: json.RawMessage(`{"path":"a.go"}`), ResultSummary: "package a",
	})
	exit := 1
	stateOf(m).LogToolCall(registry.AuditEvent{
		Timestamp: time.Unix(103, 0), ToolName: "shell.run", StepID: id, ToolCallID: "c2",
		Args: json.RawMessage(`{"command":"go test"}`), ResultContent: "FAIL",
		CommandExitCode: &exit, Duration: 3 * time.Second,
	})
	m.refreshViewport()
	return m
}

func TestInspectorShowsACallsStepAndPlace(t *testing.T) {
	m := withBeat(t)
	key := wireToolKey(t, m, "c2")
	d, ok := m.inspectorDetail(key)
	if !ok {
		t.Fatal("the shell row is not inspectable")
	}
	if want := "step 1 · tool 2/2"; d.Title != want {
		t.Fatalf("title = %q, want %q", d.Title, want)
	}
	if d.Subject != "shell.run go test" {
		t.Fatalf("subject = %q, want %q", d.Subject, "shell.run go test")
	}
	fields := map[string]string{
		"actor":        "reviewer #1 · qwen3 @ local",
		"why":          "Checking the config.",
		"exit code":    "1",
		"duration":     "3s",
		"tool call id": "c2",
	}
	for k, want := range fields {
		if got := fieldOf(d, k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if want := "{\n  \"command\": \"go test\"\n}"; sectionOf(d, "args") != want {
		t.Fatalf("args section = %q, want %q", sectionOf(d, "args"), want)
	}
	if got := sectionOf(d, "output"); got != "FAIL" {
		t.Fatalf("output section = %q, want FAIL", got)
	}
}

func TestInspectorShowsABeatsToolsAndTask(t *testing.T) {
	m := newTestModel(t)
	if err := stateOf(m).SetTodos([]db.TodoItem{{ID: "t1", Content: "Run the tests", Status: "in_progress"}}); err != nil {
		t.Fatal(err)
	}
	stateOf(m).AddMessage(session.RoleUser, "go", session.ContentTypePlain)
	id := stateOf(m).BeginStep(session.Actor{Label: "reviewer #1", Model: "qwen3", Provider: "local"})
	stateOf(m).AddNarration(id, "Checking the **config**. Then more.")
	stateOf(m).LogToolCall(registry.AuditEvent{
		Timestamp: time.Unix(102, 0), ToolName: "file.read", StepID: id, ToolCallID: "c1",
		Args: json.RawMessage(`{"path":"a.go"}`), ResultSummary: "package a",
	})
	stateOf(m).LogToolCall(registry.AuditEvent{
		Timestamp: time.Unix(103, 0), ToolName: "shell.run", StepID: id, ToolCallID: "c2",
		Args: json.RawMessage(`{"command":"go test"}`), ResultContent: "FAIL",
	})
	m.refreshViewport()

	d, ok := m.inspectorDetail(wireBeatKey(t, m))
	if !ok {
		t.Fatal("the beat is not inspectable")
	}
	if d.Subject != "Checking the config." {
		t.Fatalf("subject = %q, want the emphasis-stripped headline", d.Subject)
	}
	if got := fieldOf(d, "task"); got != "t1" {
		t.Fatalf("task = %q, want t1", got)
	}
	if got := fieldOf(d, "tools"); got != "2" {
		t.Fatalf("tools = %q, want 2", got)
	}
	if got := sectionOf(d, "narration"); got != "Checking the **config**. Then more." {
		t.Fatalf("narration = %q, want the whole narration", got)
	}
	if got := sectionOf(d, "tools"); got != "file.read a.go\nshell.run go test" {
		t.Fatalf("tools section = %q", got)
	}
}

func TestInspectorShowsASandboxedHookedCall(t *testing.T) {
	m := newTestModel(t)
	stateOf(m).AddMessage(session.RoleUser, "go", session.ContentTypePlain)
	id := stateOf(m).BeginStep(session.Actor{Model: "qwen3"})
	stateOf(m).LogToolCall(registry.AuditEvent{
		Timestamp: time.Unix(102, 0), ToolName: "shell.run", StepID: id, ToolCallID: "c1",
		Args:     json.RawMessage(`{"command":"go test"}`),
		Hooks:    []registry.HookMetadata{{Event: "PreToolUse", Decision: "block"}},
		Sandbox:  registry.SandboxMeta{Enabled: true, Backend: "restricted", NetworkIsolated: true, KilledReason: "timeout"},
		Approval: registry.ApprovalApproved,
	})
	stateOf(m).LogToolCall(registry.AuditEvent{
		Timestamp: time.Unix(104, 0), ToolName: "shell.run", StepID: id, ToolCallID: "c2",
		Args: json.RawMessage(`{"command":"go vet"}`), Approval: registry.ApprovalNotRequired,
	})
	m.refreshViewport()

	d, ok := m.inspectorDetail(wireToolKey(t, m, "c1"))
	if !ok {
		t.Fatal("the hooked row is not inspectable")
	}
	if got := fieldOf(d, "hooks"); got != "hook blocked PreToolUse: block" {
		t.Fatalf("hooks = %q", got)
	}
	if got := fieldOf(d, "sandbox"); got != "restricted, network off, killed: timeout" {
		t.Fatalf("sandbox = %q", got)
	}
	if got := fieldOf(d, "approval"); got != "approved" {
		t.Fatalf("approval = %q", got)
	}

	plain, ok := m.inspectorDetail(wireToolKey(t, m, "c2"))
	if !ok {
		t.Fatal("the unapproved row is not inspectable")
	}
	if got := fieldOf(plain, "approval"); got != "" {
		t.Fatalf("approval = %q for a call that needed none, want no field", got)
	}
}

func TestInspectorSaysNothingWhenTheNodeIsGone(t *testing.T) {
	m := withBeat(t)
	if _, ok := m.inspectorDetail(nodeID{Kind: acpwire.KindTool, Key: "tool:gone"}); ok {
		t.Fatal("a key no node has must not be inspectable")
	}
}
