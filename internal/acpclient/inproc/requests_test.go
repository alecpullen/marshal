package inproc

import (
	"context"
	"reflect"
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

// TestPendingListsTheSessionsRequests registers one prompt of each kind and
// pins what Pending reports: every slot, in wire form, each keyed by the ID its
// registration returned.
func TestPendingListsTheSessionsRequests(t *testing.T) {
	state := newState(t, "s1")
	_, sess := New(Deps{State: state})

	approvalKey, err := state.RegisterApproval(context.Background(), &session.PendingToolCall{
		ID:           "call-1",
		Name:         "shell.run",
		Command:      "go test ./...",
		Risk:         "command",
		Reason:       "policy: ask",
		ResponseChan: approvalChan(),
	})
	if err != nil {
		t.Fatalf("RegisterApproval: %v", err)
	}
	questionKey, err := state.RegisterQuestion(context.Background(), &session.PendingQuestion{
		Questions: []session.Question{{
			Question: "Which?",
			Options: []session.QuestionOption{
				{Label: "A", Description: "first"},
				{Label: "B"},
			},
			Multi: true,
		}},
		ResponseChan: make(chan []session.Answer, 1),
	})
	if err != nil {
		t.Fatalf("RegisterQuestion: %v", err)
	}
	skillKey, err := state.RegisterSkillGate(context.Background(), &session.PendingSkillGate{
		Skill:        "tdd",
		Description:  "Test first",
		Reason:       "Loading skills consumes context.",
		ResponseChan: make(chan session.SkillGateChoice, 1),
	})
	if err != nil {
		t.Fatalf("RegisterSkillGate: %v", err)
	}
	sudoKey, err := state.RegisterSudoAuth(context.Background(), &session.PendingSudoAuth{
		Command:         "apt install jq",
		Reason:          "needs root",
		StepID:          7,
		AllowUnlock:     true,
		Error:           "Sorry, try again.",
		CommandApproval: true,
		ResponseChan:    make(chan session.SudoAuthDecision, 1),
	})
	if err != nil {
		t.Fatalf("RegisterSudoAuth: %v", err)
	}

	want := acpwire.PendingRequests{
		Approvals: []acpwire.ApprovalRequest{{
			Key:        approvalKey.ID,
			ToolCallID: "call-1",
			Tool:       "shell.run",
			ToolLabel:  "Run command",
			Command:    "go test ./...",
			Risk:       "command",
			Reason:     "policy: ask",
		}},
		Question: &acpwire.QuestionRequest{
			Key: questionKey.ID,
			Questions: []acpwire.Question{{
				Question: "Which?",
				Options: []acpwire.QuestionOption{
					{Label: "A", Description: "first"},
					{Label: "B"},
				},
				Multi: true,
			}},
		},
		SkillGate: &acpwire.SkillGateRequest{
			Key:         skillKey.ID,
			Skill:       "tdd",
			Description: "Test first",
			Reason:      "Loading skills consumes context.",
		},
		Sudo: &acpwire.SudoRequest{
			Key:             sudoKey.ID,
			Command:         "apt install jq",
			Reason:          "needs root",
			StepID:          7,
			AllowUnlock:     true,
			Error:           "Sorry, try again.",
			CommandApproval: true,
		},
	}
	if got := sess.Pending(); !reflect.DeepEqual(got, want) {
		t.Errorf("Pending() =\n%+v\nwant\n%+v", got, want)
	}
}

// TestPendingAddsTheRunningSubagentsApprovals pins the approval list: the
// session's own first, then each running subagent's in registration order. A
// finished subagent's approval and a subagent without one add nothing.
func TestPendingAddsTheRunningSubagentsApprovals(t *testing.T) {
	state := newState(t, "s1")
	_, sess := New(Deps{State: state})

	sessionKey, err := state.RegisterApproval(context.Background(), &session.PendingToolCall{
		ID:           "call-s",
		Name:         "shell.run",
		Command:      "go build ./...",
		Risk:         "command",
		ResponseChan: approvalChan(),
	})
	if err != nil {
		t.Fatalf("RegisterApproval: %v", err)
	}

	a := newState(t, "a")
	viewA := state.RegisterSubagent("a", a)
	aKey, err := a.RegisterApproval(context.Background(), &session.PendingToolCall{
		ID:           "call-a",
		Name:         "file.edit",
		Args:         `{"path":"a.go"}`,
		ResponseChan: approvalChan(),
	})
	if err != nil {
		t.Fatalf("child a RegisterApproval: %v", err)
	}

	b := newState(t, "b")
	viewB := state.RegisterSubagent("b", b)
	if _, err := b.RegisterApproval(context.Background(), &session.PendingToolCall{
		ID:           "call-b",
		Name:         "shell.run",
		Command:      "go vet ./...",
		ResponseChan: approvalChan(),
	}); err != nil {
		t.Fatalf("child b RegisterApproval: %v", err)
	}
	state.FinishSubagent(viewB.ID, "", nil)

	state.RegisterSubagent("c", newState(t, "c"))

	got := sess.Pending().Approvals
	if len(got) != 2 {
		t.Fatalf("Pending().Approvals = %+v, want the session's approval and child a's", got)
	}
	if got[0].Key != sessionKey.ID {
		t.Errorf("Approvals[0].Key = %q, want the session's %q", got[0].Key, sessionKey.ID)
	}
	if got[1].Key != aKey.ID {
		t.Errorf("Approvals[1].Key = %q, want child a's %q", got[1].Key, aKey.ID)
	}
	if got[1].SubagentID != viewA.ID {
		t.Errorf("Approvals[1].SubagentID = %d, want child a's %d", got[1].SubagentID, viewA.ID)
	}
	if got[1].Subagent != "a" {
		t.Errorf("Approvals[1].Subagent = %q, want a", got[1].Subagent)
	}
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

// TestPendingMarksForcedApprovals pins decision 4's rule: a prompt only an
// exact decision may answer is marked forced.
func TestPendingMarksForcedApprovals(t *testing.T) {
	cases := []struct {
		name string
		call *session.PendingToolCall
		want bool
	}{
		{name: "privileged command", call: &session.PendingToolCall{Name: "shell.sudo", Command: "apt install jq"}, want: true},
		{name: "cost consent", call: &session.PendingToolCall{Name: "shell.run", Risk: "model-cost-consent"}, want: true},
		{name: "exact command", call: &session.PendingToolCall{Name: "shell.run", ExactCommandApproval: true}, want: true},
		{name: "mode request", call: &session.PendingToolCall{Name: "mode.request"}, want: true},
		{name: "mode elevation reason", call: &session.PendingToolCall{Name: "file.write", Reason: "mode-elevation: needs act"}, want: true},
		{name: "ordinary command", call: &session.PendingToolCall{Name: "shell.run", Risk: "command"}, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state := newState(t, "s1")
			_, sess := New(Deps{State: state})
			state.SetPendingApproval(tc.call)

			approvals := sess.Pending().Approvals
			if len(approvals) != 1 {
				t.Fatalf("Pending().Approvals = %+v, want the one call", approvals)
			}
			if got := approvals[0].Forced; got != tc.want {
				t.Errorf("Forced = %v, want %v", got, tc.want)
			}
		})
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

// TestPendingListsTheStatesAccessRequest pins the wire form of the waiting
// access request, with the tuple it was raised against.
func TestPendingListsTheStatesAccessRequest(t *testing.T) {
	state := newState(t, "s1")
	_, sess := New(Deps{State: state})

	req, _, err := state.RegisterAccess(context.Background(), &session.PendingToolCall{
		ID:           "call-1",
		Name:         "mode.request",
		ResponseChan: approvalChan(),
	}, session.AccessIntent{System: true, Reason: "read outside"})
	if err != nil {
		t.Fatalf("RegisterAccess: %v", err)
	}
	current := state.InteractionState()

	want := &acpwire.AccessRequest{
		RequestID: req.RequestID,
		SessionID: "s1",
		Actor:     req.Actor,
		StepID:    int64(req.StepID),
		Reason:    "read outside",
		Current: acpwire.AccessState{
			Mode:     string(current.Mode),
			Auto:     current.Auto,
			System:   current.System,
			Revision: current.Revision,
		},
		MissingEditing: req.MissingEditing,
		MissingSystem:  true,
	}
	if got := sess.Pending().Access; !reflect.DeepEqual(got, want) {
		t.Errorf("Pending().Access =\n%+v\nwant\n%+v", got, want)
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
