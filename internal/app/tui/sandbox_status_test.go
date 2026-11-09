package tui

import (
	"strings"
	"testing"
	"time"

	"marshal/internal/acpwire"
	"marshal/internal/app/session"
)

// sandboxStatusStub is the session stub both tests share: its Status reports a
// container sandbox that blocks the network, while the state records no
// sandbox info at all, so a read of the state could not produce this line.
func sandboxStatusStub() *stubSession {
	return &stubSession{patch: func(st *acpwire.SessionStatus) {
		st.Sandbox = acpwire.Sandbox{Backend: "container", NetworkIsolation: true}
	}}
}

// TestTheRunningShellRowReadsTheSandboxFromStatus pins that the isolation line
// under a running shell row comes from Status, not from the state.
func TestTheRunningShellRowReadsTheSandboxFromStatus(t *testing.T) {
	state := stateOf(newTestModel(t))
	stub := sandboxStatusStub()
	m := testModelStub(t, state, stub)
	m.resize(100, 30)

	stateOf(m).SetActiveToolCall(session.ActiveToolCall{
		Name:       "shell.run",
		Args:       "go test ./...",
		StartedAt:  time.Now(),
		ToolCallID: "call-sandbox",
	})
	m.invalidateTranscript()
	m.refreshViewport()

	if plain := stripANSI(m.viewport.GetContent()); !strings.Contains(plain, "sandbox: container · network blocked") {
		t.Fatalf("the running row does not show the sandbox from Status:\n%s", plain)
	}
}

// TestTheApprovalPanelReadsTheSandboxFromStatus pins that the approval panel's
// isolation line comes from Status, not from the state.
func TestTheApprovalPanelReadsTheSandboxFromStatus(t *testing.T) {
	state := stateOf(newTestModel(t))
	stub := sandboxStatusStub()
	m := testModelStub(t, state, stub)
	m.resize(120, 40)

	state.SetPendingApproval(&session.PendingToolCall{
		ID:           "call-sandbox-approval",
		Name:         "shell.run",
		Command:      "go test ./...",
		Risk:         "command",
		ResponseChan: make(chan session.UserApprovalDecision, 1),
	})
	m.invalidateTranscript()
	m.refreshViewport()

	if plain := stripANSI(m.viewString()); !strings.Contains(plain, "sandbox: container · network blocked") {
		t.Fatalf("the approval panel does not show the sandbox from Status:\n%s", plain)
	}
}
