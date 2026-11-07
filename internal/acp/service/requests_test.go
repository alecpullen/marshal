package service

import (
	"context"
	"strings"
	"testing"

	"marshal/internal/acpwire"
	"marshal/internal/app/session"
)

// approvalChan is a buffered response channel of the width a registration
// needs.
func approvalChan() chan session.UserApprovalDecision {
	return make(chan session.UserApprovalDecision, 1)
}

// TestPendingApprovalCarriesItsStepsOwnerAndWhy checks the derivation of the
// owner line and the why line from the step that asked for the approval.
func TestPendingApprovalCarriesItsStepsOwnerAndWhy(t *testing.T) {
	state := newState(t, "s1")
	_, sess := New(Deps{State: state})

	state.AddMessage(session.RoleUser, "go", session.ContentTypePlain)
	id := state.BeginStep(session.Actor{Role: "sdd_implementer", Label: "implementer"})
	state.AddNarration(id, "Installing the dependency the build needs. It is pinned.")
	state.SetPendingApproval(&session.PendingToolCall{
		Name:    "shell.run",
		Command: "go get example.com/x@v1",
		Risk:    "command",
		StepID:  id,
	})

	got := sess.Pending().Approvals[0]
	if got.Actor != "implementer" {
		t.Errorf("Actor = %q, want implementer", got.Actor)
	}
	if got.Why != "Installing the dependency the build needs." {
		t.Errorf("Why = %q, want the first sentence of the narration", got.Why)
	}

	// The orchestrator is not named; a step without narration has no why.
	silent := newState(t, "s2")
	_, silentSess := New(Deps{State: silent})
	silent.AddMessage(session.RoleUser, "go", session.ContentTypePlain)
	silentID := silent.BeginStep(session.Actor{})
	silent.SetPendingApproval(&session.PendingToolCall{Name: "shell.run", Command: "ls", StepID: silentID})
	if got := silentSess.Pending().Approvals[0]; got.Actor != "" || got.Why != "" {
		t.Errorf("orchestrator step with no narration: actor %q why %q, want both empty", got.Actor, got.Why)
	}

	// An approval that names no step has neither.
	unstepped := newState(t, "s3")
	_, unsteppedSess := New(Deps{State: unstepped})
	unstepped.SetPendingApproval(&session.PendingToolCall{Name: "shell.run", Command: "ls"})
	if got := unsteppedSess.Pending().Approvals[0]; got.Actor != "" || got.Why != "" {
		t.Errorf("unstamped approval: actor %q why %q, want both empty", got.Actor, got.Why)
	}
}

// TestASubagentApprovalReadsItsOwnerFromTheChild pins that a child's step IDs
// are resolved against the child, not the parent whose step with the same
// number belongs to someone else.
func TestASubagentApprovalReadsItsOwnerFromTheChild(t *testing.T) {
	state := newState(t, "s1")
	_, sess := New(Deps{State: state})

	state.AddMessage(session.RoleUser, "go", session.ContentTypePlain)
	parentID := state.BeginStep(session.Actor{})
	state.AddNarration(parentID, "Parent narration that must not leak.")

	child := newState(t, "child")
	child.AddMessage(session.RoleUser, "review", session.ContentTypePlain)
	childID := child.BeginStep(session.Actor{Role: "reviewer", Label: "reviewer"})
	child.AddNarration(childID, "Checking the migration. It touches two tables.")
	if childID != parentID {
		t.Fatalf("test needs colliding step IDs, got parent %d child %d", parentID, childID)
	}

	state.RegisterSubagent("reviewer", child)
	child.SetPendingApproval(&session.PendingToolCall{
		Name:    "shell.run",
		Command: "go vet ./...",
		Risk:    "command",
		StepID:  childID,
	})

	got := sess.Pending().Approvals[0]
	if got.Actor != "reviewer" {
		t.Errorf("Actor = %q, want the child's step owner reviewer", got.Actor)
	}
	if got.Why != "Checking the migration." {
		t.Errorf("Why = %q, want the child's first sentence", got.Why)
	}
}

// TestAnUnregisteredPromptKeepsOneKeyWhileOpen pins the minted key: it stays
// the same while the prompt is open, and a prompt that replaced it gets its
// own.
func TestAnUnregisteredPromptKeepsOneKeyWhileOpen(t *testing.T) {
	state := newState(t, "s1")
	_, sess := New(Deps{State: state})

	state.SetPendingApproval(&session.PendingToolCall{Name: "shell.run", Command: "go build ./..."})
	first := sess.Pending().Approvals[0].Key
	if !strings.HasPrefix(first, "local-") {
		t.Fatalf("minted key = %q, want a local- key", first)
	}
	if again := sess.Pending().Approvals[0].Key; again != first {
		t.Errorf("key changed while the prompt stayed open: %q, then %q", first, again)
	}

	state.SetPendingApproval(nil)
	state.SetPendingApproval(&session.PendingToolCall{Name: "shell.run", Command: "go vet ./..."})
	if got := sess.Pending().Approvals[0].Key; got == first {
		t.Errorf("the second prompt reuses the first prompt's key %q", got)
	}
}

// TestATypedAccessApprovalIsKeyedByItsRequest pins that a typed access request
// in the approval slot is keyed by its own request ID and marked as access.
func TestATypedAccessApprovalIsKeyedByItsRequest(t *testing.T) {
	state := newState(t, "s1")
	_, sess := New(Deps{State: state})

	req, waiting, err := state.RegisterAccess(context.Background(), &session.PendingToolCall{
		ID:           "call-1",
		Name:         "mode.request",
		ResponseChan: approvalChan(),
	}, session.AccessIntent{System: true, Reason: "read outside"})
	if err != nil {
		t.Fatalf("RegisterAccess: %v", err)
	}
	if !waiting {
		t.Fatalf("RegisterAccess reported nothing missing, want a request")
	}

	approvals := sess.Pending().Approvals
	if len(approvals) != 1 {
		t.Fatalf("Pending().Approvals = %+v, want the access request in the approval slot", approvals)
	}
	if !approvals[0].Access {
		t.Errorf("Approvals[0].Access = false, want true for a typed access request")
	}
	if approvals[0].Key != req.RequestID {
		t.Errorf("Approvals[0].Key = %q, want the request's ID %q", approvals[0].Key, req.RequestID)
	}
}

// TestPendingNeverSettlesAnAccessRequest pins that a read is only a read: no
// callback runs, the request stays waiting and nothing lands on its channel.
func TestPendingNeverSettlesAnAccessRequest(t *testing.T) {
	state := newState(t, "s1")
	// No access callbacks in Deps: reading must not need one.
	_, sess := New(Deps{State: state})

	response := approvalChan()
	req, _, err := state.RegisterAccess(context.Background(), &session.PendingToolCall{
		ID:           "call-1",
		Name:         "mode.request",
		ResponseChan: response,
	}, session.AccessIntent{System: true, Reason: "read outside"})
	if err != nil {
		t.Fatalf("RegisterAccess: %v", err)
	}

	for i := 0; i < 2; i++ {
		got := sess.Pending().Access
		if got == nil || got.RequestID != req.RequestID {
			t.Fatalf("read %d: Pending().Access = %+v, want %q", i, got, req.RequestID)
		}
	}
	if _, ok := state.AccessRequest(); !ok {
		t.Errorf("the access request is no longer waiting after two reads")
	}
	if len(response) != 0 {
		t.Errorf("a read answered the request: %d decision(s) on the channel", len(response))
	}
}

// TestAnswerUnansweredIsTheSessions pins the wire constants against the
// session vocabulary they stand for.
func TestAnswerUnansweredIsTheSessions(t *testing.T) {
	if acpwire.AnswerUnanswered != session.AnswerUnanswered {
		t.Errorf("acpwire.AnswerUnanswered = %q, want session.AnswerUnanswered %q",
			acpwire.AnswerUnanswered, session.AnswerUnanswered)
	}
	statuses := []struct {
		wire    string
		session session.AccessStatus
	}{
		{acpwire.AccessAvailable, session.AccessAvailable},
		{acpwire.AccessGranted, session.AccessGranted},
		{acpwire.AccessDenied, session.AccessDenied},
		{acpwire.AccessCanceled, session.AccessCanceled},
		{acpwire.AccessFailed, session.AccessFailed},
		{acpwire.AccessRefresh, session.AccessRefresh},
		{acpwire.AccessExpired, session.AccessExpired},
	}
	for _, s := range statuses {
		if s.wire != string(s.session) {
			t.Errorf("wire access status %q, want session's %q", s.wire, string(s.session))
		}
	}
}
