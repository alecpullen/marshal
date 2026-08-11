package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/session"
)

// TestChildApprovalSurfacedAndAnswered verifies that a subagent's pending
// approval is surfaced in the TUI and that answering it routes the decision
// into the child's ResponseChan, without ever setting the parent's pending
// approval.
func TestChildApprovalSurfacedAndAnswered(t *testing.T) {
	m := newTestModel(t)
	child := newChildState(t)
	m.state.RegisterSubagent("child-task", child)

	ch := make(chan session.UserApprovalDecision, 1)
	tc := &session.PendingToolCall{
		ID:           "c1",
		Name:         "shell.run",
		Command:      "git commit -m x",
		Risk:         "command",
		Reason:       "commit work",
		ResponseChan: ch,
	}
	child.SetPendingApproval(tc)

	// The parent must never have a pending approval.
	if m.state.PendingApproval() != nil {
		t.Fatal("parent state should not have a pending approval")
	}

	// Drive the inline chooser: two Enters approve the default selection.
	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})

	select {
	case dec := <-ch:
		if !dec.Approved {
			t.Fatal("expected child approval to be approved")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for child approval response")
	}
	if child.PendingApproval() != nil {
		t.Fatal("child pending approval should be cleared after decision")
	}
	if m.state.PendingApproval() != nil {
		t.Fatal("parent pending approval should never have been set")
	}
}

// TestParentApprovalWinsOverChild verifies that when both the parent and a
// child have a pending approval, the parent's is the one answered.
func TestParentApprovalWinsOverChild(t *testing.T) {
	m := newTestModel(t)
	child := newChildState(t)
	m.state.RegisterSubagent("child-task", child)

	parentCh := make(chan session.UserApprovalDecision, 1)
	parentTC := &session.PendingToolCall{
		ID:           "p1",
		Name:         "shell.run",
		Command:      "echo parent",
		Risk:         "command",
		Reason:       "parent",
		ResponseChan: parentCh,
	}
	childCh := make(chan session.UserApprovalDecision, 1)
	childTC := &session.PendingToolCall{
		ID:           "c1",
		Name:         "shell.run",
		Command:      "echo child",
		Risk:         "command",
		Reason:       "child",
		ResponseChan: childCh,
	}
	m.state.SetPendingApproval(parentTC)
	child.SetPendingApproval(childTC)

	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})

	select {
	case dec := <-parentCh:
		if !dec.Approved {
			t.Fatal("expected parent approval to be approved")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for parent approval response")
	}
	// The child's approval must be untouched.
	select {
	case <-childCh:
		t.Fatal("child approval was answered even though parent should win")
	default:
	}
	if child.PendingApproval() != childTC {
		t.Fatal("child pending approval should remain pending")
	}
}

// TestChildApprovalRenderedInInputArea verifies that a subagent approval is
// visible to the user: the input area renders the approval panel and the
// status bar reports an approval, even though only the child state (not the
// parent) has a pending approval. Regression test for the "routed but not
// rendered" review finding.
func TestChildApprovalRenderedInInputArea(t *testing.T) {
	m := newTestModel(t)
	child := newChildState(t)
	m.state.RegisterSubagent("child-task", child)

	tc := &session.PendingToolCall{
		ID:           "c1",
		Name:         "shell.run",
		Command:      "git commit -m x",
		Risk:         "command",
		Reason:       "commit work",
		ResponseChan: make(chan session.UserApprovalDecision, 1),
	}
	child.SetPendingApproval(tc)

	if m.state.PendingApproval() != nil {
		t.Fatal("parent state should not have a pending approval")
	}
	if !m.hasPendingApproval() {
		t.Fatal("hasPendingApproval should be true for a child approval")
	}

	// The input area must render the approval panel (or the eager huh form)
	// rather than a plain input, and the mode/status must read "approval".
	rendered := m.renderInputArea()
	if strings.Contains(rendered, "Ask Marshal") {
		t.Errorf("child approval should not render a plain input area; got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "git commit -m x") {
		t.Errorf("child approval panel should surface the command; got:\n%s", rendered)
	}
	if m.modeSegment() != "approval" {
		t.Errorf("modeSegment = %q, want %q", m.modeSegment(), "approval")
	}
}

// TestChildApprovalDisplayNamesSubagent verifies that the approval model
// built for a child approval carries a reason prefix naming the subagent.
func TestChildApprovalDisplayNamesSubagent(t *testing.T) {
	m := newTestModel(t)
	child := newChildState(t)
	m.state.RegisterSubagent("child-task", child)

	tc := &session.PendingToolCall{
		ID:           "c1",
		Name:         "shell.run",
		Command:      "git commit -m x",
		Risk:         "command",
		Reason:       "commit work",
		ResponseChan: make(chan session.UserApprovalDecision, 1),
	}
	child.SetPendingApproval(tc)

	// Drive one message so handleApproval lazily builds the approval model
	// from the display copy.
	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.approvalModel == nil {
		t.Fatal("approval model was not built")
	}
	if m.approvalModel.tc == nil {
		t.Fatal("approval model has no tool call")
	}
	want := `subagent "child-task": commit work`
	if m.approvalModel.tc.Reason != want {
		t.Errorf("approval model reason = %q, want %q", m.approvalModel.tc.Reason, want)
	}
}
