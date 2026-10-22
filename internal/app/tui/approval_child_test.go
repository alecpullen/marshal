package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/session"
	"marshal/internal/interaction"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
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
	if m.modeSegment() != "Act" {
		t.Errorf("modeSegment = %q, want Act while approval is pending", m.modeSegment())
	}
	if got := stripANSI(m.statusRightSegment()); !strings.Contains(got, "approval") {
		t.Errorf("statusRightSegment = %q, want the approval indicator", got)
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

// TestCanonicalChildApprovalSettlesOnlyChildOwner pins that an exact child
// approval settles against the child's own state and carries its display flag,
// never the parent's pending-approval slot.
func TestCanonicalChildApprovalSettlesOnlyChildOwner(t *testing.T) {
	m := newTestModel(t)
	child := newChildState(t)
	m.state.RegisterSubagent("child-task", child)

	ch := make(chan session.UserApprovalDecision, 1)
	tc := &session.PendingToolCall{
		ID:                   "c1",
		Name:                 "shell.run",
		Command:              "git commit -m x",
		Risk:                 "command",
		Reason:               "commit work",
		ExactCommandApproval: true,
		ResponseChan:         ch,
	}
	child.SetPendingApproval(tc)

	display, label := m.pendingApprovalDisplay()
	if display == nil || label != "child-task" || !display.ExactCommandApproval {
		t.Fatalf("pendingApprovalDisplay = %+v, %q; want the exact child prompt", display, label)
	}

	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})

	select {
	case d := <-ch:
		if !d.Approved {
			t.Fatal("expected the child approval to be approved")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for the child approval response")
	}
	if child.PendingApproval() != nil {
		t.Fatal("child pending approval was not cleared")
	}
	if m.state.PendingApproval() != nil {
		t.Fatal("the child approval touched the parent pending-approval slot")
	}
}

// TestChildDestructiveApprovalAfterAskDoesNotLaunch pins that a child approval
// collected before the owner switched to Ask carries no launch authority: the
// live admission re-evaluates against the child's owner-bounded authority and
// refuses the destructive command.
func TestChildDestructiveApprovalAfterAskDoesNotLaunch(t *testing.T) {
	m := newTestModel(t)
	child := session.New(m.state.Config, m.state.WorkingDir, time.Unix(100, 0), session.Persistence{},
		session.WithDepth(1),
		session.WithInteractionOwner(m.state, interaction.Ceiling{Act: true, Auto: true, System: true}))
	m.state.RegisterSubagent("child-task", child)

	ch := make(chan session.UserApprovalDecision, 1)
	tc := &session.PendingToolCall{
		ID:                   "c1",
		Name:                 "shell.run",
		Command:              "git reset --hard",
		Risk:                 "command",
		Reason:               "destructive child work",
		ExactCommandApproval: true,
		ResponseChan:         ch,
	}
	child.SetPendingApproval(tc)

	ask := interaction.ModeAsk
	if _, err := m.state.ApplyInteraction(interaction.Patch{Mode: &ask}); err != nil {
		t.Fatalf("owner Ask: %v", err)
	}
	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	select {
	case d := <-ch:
		if !d.Approved {
			t.Fatal("the exact child approval should have been collected")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for the child approval response")
	}

	// The collected decision is not launch authority. Admit through the
	// child's live authority exactly as dispatch would.
	reg := registry.New()
	if err := reg.Register(registry.Tool{
		Name: "shell.run",
		Risk: registry.RiskCommand,
		Handler: func(context.Context, registry.ToolCall) (registry.ToolResult, error) {
			t.Fatal("the destructive child command launched after Ask")
			return registry.ToolResult{}, nil
		},
	}); err != nil {
		t.Fatalf("register shell.run: %v", err)
	}
	pol := policy.NewEngine(&m.state.Config, nil)
	pol.WithRegistry(reg)
	az := policy.NewAuthorizer(pol, child.InteractionAuthority())
	call := policy.Call{
		InvocationID: "launch",
		ToolName:     "shell.run",
		Shell:        &policy.ShellInput{Command: tc.Command, Dir: m.state.WorkingDir, WorkspaceRoot: m.state.WorkingDir},
	}
	proof, evaluation, err := az.Evaluate(context.Background(), call)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if evaluation.Decision == policy.DecisionAllow {
		t.Fatalf("destructive child command evaluated Allow after Ask: %+v", evaluation)
	}
	attempt := policy.CommandAttempt{InvocationID: "launch", ToolName: "shell.run", Input: *call.Shell}
	if _, err := az.BeginCommand(policy.WithAuthorization(context.Background(), proof), attempt); err == nil {
		t.Fatal("the destructive child command was admitted after Ask")
	}
}
