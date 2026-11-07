package acp

import (
	"context"
	"encoding/base64"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"marshal/internal/acp/service"
	"marshal/internal/acpclient"
	"marshal/internal/acpclient/rpc"
	"marshal/internal/acpwire"
	"marshal/internal/app/session"
)

// requestApprovalChan is a buffered response channel of the width a
// registration needs.
func requestApprovalChan() chan session.UserApprovalDecision {
	return make(chan session.UserApprovalDecision, 1)
}

// registerRequestApproval publishes one ordinary approval and returns its key
// and the channel its decision arrives on.
func registerRequestApproval(t *testing.T, state *session.State, call *session.PendingToolCall) (session.PromptKey, chan session.UserApprovalDecision) {
	t.Helper()
	ch := requestApprovalChan()
	call.ResponseChan = ch
	key, err := state.RegisterApproval(context.Background(), call)
	if err != nil {
		t.Fatalf("RegisterApproval: %v", err)
	}
	return key, ch
}

// requestApprovalDecision reads one decision off a settled approval's channel.
func requestApprovalDecision(t *testing.T, ch chan session.UserApprovalDecision) session.UserApprovalDecision {
	t.Helper()
	select {
	case d := <-ch:
		return d
	case <-time.After(5 * time.Second):
		t.Fatal("the approval was never settled; the tool would block forever")
		return session.UserApprovalDecision{}
	}
}

// accessSeamDeps is the runtime's access seam over a state: it presents the
// state's request without settling it, and resolves a choice to nothing. A
// session without both callbacks settles every access request as failed, so a
// case that compares one needs them on both arms.
func accessSeamDeps(state *session.State) service.Deps {
	return service.Deps{
		State: state,
		AccessRequest: func() (session.AccessRequest, bool) {
			return state.AccessRequest()
		},
		ResolveAccess: func(session.AccessChoice) (session.AccessResult, error) {
			return session.AccessResult{}, nil
		},
	}
}

// TestRequestConformance runs every request case against both arms of the
// seam: the service itself, and the client's mirror of it over a pipe. The
// same answers from both is V1.
func TestRequestConformance(t *testing.T) {
	cases := []struct {
		name string
		run  func(t *testing.T, open func(t *testing.T, d service.Deps) seam)
	}{
		{name: "the session's requests are listed", run: pendingListsTheSessionsRequests},
		{name: "a running subagent's approval is listed", run: pendingAddsTheRunningSubagentsApprovals},
		{name: "a forced approval is marked", run: pendingMarksForcedApprovals},
		{name: "the state's access request is listed", run: pendingListsTheStatesAccessRequest},
		{name: "the access request to present", run: reconcileAccessReadsTheRuntime},
		{name: "once and deny", run: answerApprovalOnceAndDeny},
		{name: "a child's approval settles on the child", run: aChildsApprovalSettlesOnTheChild},
		{name: "an answer to a settled approval is request-settled", run: anAnswerToASettledApprovalIsRequestSettled},
		{name: "a question's answers are delivered", run: answerQuestionDeliversTheAnswers},
		{name: "a skill gate's choice is recorded", run: answerSkillGateRecordsTheChoice},
		{name: "an access answer resolves through the runtime", run: answerAccessResolvesThroughTheRuntime},
		{name: "a sudo answer reaches the prompt", run: aSudoAnswerReachesThePrompt},
		{name: "a rollback restores and logs", run: rollbackRestoresAndLogs},
	}
	for _, arm := range seamArms() {
		for _, tc := range cases {
			t.Run(arm.name+"/"+tc.name, func(t *testing.T) {
				tc.run(t, arm.open)
			})
		}
	}
}

// pendingListsTheSessionsRequests registers one prompt of each kind and pins
// what Pending reports: every slot, in wire form, each keyed by the ID its
// registration returned.
func pendingListsTheSessionsRequests(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	state := seamState(t, "s1")
	approvalKey, err := state.RegisterApproval(context.Background(), &session.PendingToolCall{
		ID:           "call-1",
		Name:         "shell.run",
		Command:      "go test ./...",
		Risk:         "command",
		Reason:       "policy: ask",
		ResponseChan: requestApprovalChan(),
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

	sess := open(t, service.Deps{State: state})

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

// pendingAddsTheRunningSubagentsApprovals pins the approval list: the
// session's own first, then each running subagent's in registration order. A
// finished subagent's approval and a subagent without one add nothing.
func pendingAddsTheRunningSubagentsApprovals(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	state := seamState(t, "s1")
	sessionKey, err := state.RegisterApproval(context.Background(), &session.PendingToolCall{
		ID:           "call-s",
		Name:         "shell.run",
		Command:      "go build ./...",
		Risk:         "command",
		ResponseChan: requestApprovalChan(),
	})
	if err != nil {
		t.Fatalf("RegisterApproval: %v", err)
	}

	a := seamState(t, "a")
	viewA := state.RegisterSubagent("a", a)
	aKey, err := a.RegisterApproval(context.Background(), &session.PendingToolCall{
		ID:           "call-a",
		Name:         "file.edit",
		Args:         `{"path":"a.go"}`,
		ResponseChan: requestApprovalChan(),
	})
	if err != nil {
		t.Fatalf("child a RegisterApproval: %v", err)
	}

	b := seamState(t, "b")
	viewB := state.RegisterSubagent("b", b)
	if _, err := b.RegisterApproval(context.Background(), &session.PendingToolCall{
		ID:           "call-b",
		Name:         "shell.run",
		Command:      "go vet ./...",
		ResponseChan: requestApprovalChan(),
	}); err != nil {
		t.Fatalf("child b RegisterApproval: %v", err)
	}
	state.FinishSubagent(viewB.ID, "", nil)

	state.RegisterSubagent("c", seamState(t, "c"))

	sess := open(t, service.Deps{State: state})

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

// pendingMarksForcedApprovals pins decision 4's rule: a prompt only an exact
// decision may answer is marked forced. Every prompt is registered before
// open, so the pipe arm's first read carries it.
func pendingMarksForcedApprovals(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
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
			state := seamState(t, "s1")
			state.SetPendingApproval(tc.call)
			sess := open(t, service.Deps{State: state})

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

// pendingListsTheStatesAccessRequest pins the wire form of the waiting access
// request, with the tuple it was raised against.
func pendingListsTheStatesAccessRequest(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	state := seamState(t, "s1")
	req, _, err := state.RegisterAccess(context.Background(), &session.PendingToolCall{
		ID:           "call-1",
		Name:         "mode.request",
		ResponseChan: requestApprovalChan(),
	}, session.AccessIntent{System: true, Reason: "read outside"})
	if err != nil {
		t.Fatalf("RegisterAccess: %v", err)
	}
	current := state.InteractionState()

	sess := open(t, accessSeamDeps(state))

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

// reconcileAccessReadsTheRuntime pins what the access panel is handed: the
// request the runtime presents, its ID, session and flags. The service's own
// test keeps the read count; the forwarder reconciles on every pass.
func reconcileAccessReadsTheRuntime(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	state := seamState(t, "s1")
	req, waiting, err := state.RegisterAccess(context.Background(), &session.PendingToolCall{
		ID:           "call-1",
		Name:         "mode.request",
		ResponseChan: requestApprovalChan(),
	}, session.AccessIntent{System: true, Reason: "read outside"})
	if err != nil || !waiting {
		t.Fatalf("RegisterAccess = (%v, %v), want a published request", waiting, err)
	}

	sess := open(t, accessSeamDeps(state))

	got, ok := sess.ReconcileAccess(context.Background())
	if !ok {
		t.Fatal("ReconcileAccess reported no request, want the registered one")
	}
	if got.RequestID != req.RequestID {
		t.Errorf("RequestID = %q, want %q", got.RequestID, req.RequestID)
	}
	if got.SessionID != state.SessionID() {
		t.Errorf("SessionID = %q, want %q", got.SessionID, state.SessionID())
	}
	if got.Reason != req.Reason || !got.MissingSystem || got.MissingEditing {
		t.Errorf("reconciled request = %+v, want the registered request's reason and flags", got)
	}
}

// answerApprovalOnceAndDeny pins the two exact decisions: once approves, deny
// does not, and each settles the request.
func answerApprovalOnceAndDeny(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	for _, tc := range []struct {
		decision acpwire.ApprovalDecision
		approved bool
	}{
		{decision: acpwire.ApprovalOnce, approved: true},
		{decision: acpwire.ApprovalDeny, approved: false},
	} {
		t.Run(string(tc.decision), func(t *testing.T) {
			state := seamState(t, "s1")
			key, ch := registerRequestApproval(t, state, &session.PendingToolCall{
				ID:      "call-1",
				Name:    "shell.run",
				Command: "go test ./...",
				Risk:    "command",
			})
			sess := open(t, service.Deps{State: state})

			if err := sess.AnswerApproval(context.Background(), key.ID, acpwire.ApprovalAnswer{Decision: tc.decision}); err != nil {
				t.Fatalf("AnswerApproval: %v", err)
			}
			if got := requestApprovalDecision(t, ch); got.Approved != tc.approved {
				t.Errorf("delivered decision = %+v, want Approved %v", got, tc.approved)
			}
			if got := sess.Pending().Approvals; len(got) != 0 {
				t.Errorf("Pending().Approvals = %+v after the answer, want none", got)
			}
		})
	}
}

// aChildsApprovalSettlesOnTheChild pins decision 6: the settlement goes to the
// state that raised the call, and the rule lands on the session.
func aChildsApprovalSettlesOnTheChild(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	parent := seamState(t, "s1")
	child := seamState(t, "child")
	parent.RegisterSubagent("reviewer", child)
	key, ch := registerRequestApproval(t, child, &session.PendingToolCall{
		ID:      "call-c",
		Name:    "shell.run",
		Command: "go vet ./...",
		Risk:    "command",
	})
	sess := open(t, service.Deps{State: parent})

	if err := sess.AnswerApproval(context.Background(), key.ID, acpwire.ApprovalAnswer{Decision: acpwire.ApprovalSession}); err != nil {
		t.Fatalf("AnswerApproval: %v", err)
	}
	if got := requestApprovalDecision(t, ch); !got.Approved {
		t.Errorf("delivered decision = %+v, want an approval", got)
	}
	if got := parent.SessionRules(); len(got) != 1 || got[0] != "go vet ./..." {
		t.Errorf("the parent's SessionRules() = %v, want the child's command", got)
	}
	if got := child.SessionRules(); len(got) != 0 {
		t.Errorf("the child's SessionRules() = %v, want none", got)
	}
}

// anAnswerToASettledApprovalIsRequestSettled pins the key of an answer to a
// request that is no longer open.
func anAnswerToASettledApprovalIsRequestSettled(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	state := seamState(t, "s1")
	key, ch := registerRequestApproval(t, state, &session.PendingToolCall{
		ID:      "call-1",
		Name:    "shell.run",
		Command: "go test ./...",
		Risk:    "command",
	})
	sess := open(t, service.Deps{State: state})

	if err := sess.AnswerApproval(context.Background(), key.ID, acpwire.ApprovalAnswer{Decision: acpwire.ApprovalOnce}); err != nil {
		t.Fatalf("AnswerApproval: %v", err)
	}
	requestApprovalDecision(t, ch)

	err := sess.AnswerApproval(context.Background(), key.ID, acpwire.ApprovalAnswer{Decision: acpwire.ApprovalDeny})
	if !acpclient.IsCode(err, acpwire.CodeRequestSettled) {
		t.Errorf("second AnswerApproval = %v, want a request-settled code", err)
	}
}

// answerQuestionDeliversTheAnswers pins the delivery, in the questions' order,
// and the settled key after it.
func answerQuestionDeliversTheAnswers(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	state := seamState(t, "s1")
	ch := make(chan []session.Answer, 1)
	key, err := state.RegisterQuestion(context.Background(), &session.PendingQuestion{
		Questions: []session.Question{
			{Question: "Which file?"},
			{Question: "Which mode?"},
		},
		ResponseChan: ch,
	})
	if err != nil {
		t.Fatalf("RegisterQuestion: %v", err)
	}
	sess := open(t, service.Deps{State: state})

	answers := []acpwire.Answer{
		{Question: "Which file?", Answer: "app.go"},
		{Question: "Which mode?", Answer: "act"},
	}
	if err := sess.AnswerQuestion(context.Background(), key.ID, answers); err != nil {
		t.Fatalf("AnswerQuestion: %v", err)
	}
	select {
	case got := <-ch:
		want := []session.Answer{
			{Question: "Which file?", Answer: "app.go"},
			{Question: "Which mode?", Answer: "act"},
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("delivered answers = %+v, want %+v", got, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the question was never answered; the tool would block forever")
	}

	err = sess.AnswerQuestion(context.Background(), key.ID, answers)
	if !acpclient.IsCode(err, acpwire.CodeRequestSettled) {
		t.Errorf("second AnswerQuestion = %v, want a request-settled code", err)
	}
}

// answerSkillGateRecordsTheChoice pins each skill-gate answer's delivery and
// the sticky record the allow choices leave behind.
func answerSkillGateRecordsTheChoice(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	for _, tc := range []struct {
		choice acpwire.SkillGateChoice
		mapped session.SkillGateChoice
	}{
		{choice: acpwire.SkillGateAllowOnce, mapped: session.SkillGateAllowOnce},
		{choice: acpwire.SkillGateAllowSkill, mapped: session.SkillGateAllowSkill},
		{choice: acpwire.SkillGateAllowAll, mapped: session.SkillGateAllowAll},
		{choice: acpwire.SkillGateDeny, mapped: session.SkillGateDeny},
	} {
		t.Run(string(tc.choice), func(t *testing.T) {
			state := seamState(t, "s1")
			ch := make(chan session.SkillGateChoice, 1)
			key, err := state.RegisterSkillGate(context.Background(), &session.PendingSkillGate{
				Skill:        "tdd",
				Description:  "Test first",
				Reason:       "Loading skills consumes context.",
				ResponseChan: ch,
			})
			if err != nil {
				t.Fatalf("RegisterSkillGate: %v", err)
			}
			sess := open(t, service.Deps{State: state})

			if err := sess.AnswerSkillGate(context.Background(), key.ID, tc.choice); err != nil {
				t.Fatalf("AnswerSkillGate: %v", err)
			}
			select {
			case got := <-ch:
				if got != tc.mapped {
					t.Errorf("delivered choice = %v, want %v", got, tc.mapped)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("the gate was never answered; the runner would block forever")
			}

			switch tc.choice {
			case acpwire.SkillGateAllowSkill:
				got := state.SkillGateDecisions()
				if len(got) != 1 || got[0].Skill != "tdd" || !got[0].Allowed {
					t.Errorf("SkillGateDecisions() = %+v, want tdd allowed", got)
				}
			case acpwire.SkillGateAllowAll:
				if state.SkillGateEnabled() {
					t.Error("SkillGateEnabled() = true after allow_all, want the gate off")
				}
			default:
				if got := state.SkillGateDecisions(); len(got) != 0 {
					t.Errorf("SkillGateDecisions() = %+v, want none recorded", got)
				}
				if !state.SkillGateEnabled() {
					t.Error("SkillGateEnabled() = false, want the gate untouched")
				}
			}

			if err := sess.AnswerSkillGate(context.Background(), key.ID, tc.choice); !acpclient.IsCode(err, acpwire.CodeRequestSettled) {
				t.Errorf("second AnswerSkillGate = %v, want a request-settled code", err)
			}
		})
	}
}

// answerAccessResolvesThroughTheRuntime pins that the choice reaches the
// runtime converted, and its result comes back as the answer.
func answerAccessResolvesThroughTheRuntime(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	state := seamState(t, "s1")
	var recorded session.AccessChoice
	deps := service.Deps{
		State: state,
		AccessRequest: func() (session.AccessRequest, bool) {
			return state.AccessRequest()
		},
		ResolveAccess: func(choice session.AccessChoice) (session.AccessResult, error) {
			recorded = choice
			return session.AccessResult{
				RequestID: choice.RequestID,
				SessionID: choice.SessionID,
				Status:    session.AccessGranted,
				Effective: session.AccessState{Mode: "act", System: true},
				Message:   "access granted",
			}, nil
		},
	}
	req, waiting, err := state.RegisterAccess(context.Background(), &session.PendingToolCall{
		ID:           "call-1",
		Name:         "mode.request",
		ResponseChan: requestApprovalChan(),
	}, session.AccessIntent{System: true, Reason: "read outside"})
	if err != nil || !waiting {
		t.Fatalf("RegisterAccess = (%v, %v), want a published request", waiting, err)
	}
	sess := open(t, deps)

	choice := acpwire.AccessChoice{
		RequestID: req.RequestID,
		SessionID: req.SessionID,
		Current: acpwire.AccessState{
			Mode: req.Current.Mode, Auto: req.Current.Auto,
			System: req.Current.System, Revision: req.Current.Revision,
		},
		Approved: true,
	}
	result, err := sess.AnswerAccess(context.Background(), choice)
	if err != nil {
		t.Fatalf("AnswerAccess: %v", err)
	}
	if result.Status != acpwire.AccessGranted {
		t.Errorf("result.Status = %q, want %q", result.Status, acpwire.AccessGranted)
	}
	if got := result.Effective; got.Mode != "act" || !got.System {
		t.Errorf("result.Effective = %+v, want act with system access", got)
	}
	want := session.AccessChoice{
		RequestID: choice.RequestID,
		SessionID: choice.SessionID,
		Current: session.AccessState{
			Mode: choice.Current.Mode, Auto: choice.Current.Auto,
			System: choice.Current.System, Revision: choice.Current.Revision,
		},
		Approved: choice.Approved,
	}
	if !reflect.DeepEqual(recorded, want) {
		t.Errorf("the runtime recorded %+v, want the wire choice converted %+v", recorded, want)
	}
}

// aSudoAnswerReachesThePrompt pins that the decision on the prompt's channel
// is OK and carries the same password bytes, and that a second answer is
// request-settled. The service's own test keeps the in-process rule: the
// handler gets the very slice.
func aSudoAnswerReachesThePrompt(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	state := seamState(t, "s1")
	ch := make(chan session.SudoAuthDecision, 1)
	key, err := state.RegisterSudoAuth(context.Background(), &session.PendingSudoAuth{
		Command:      "apt install jq",
		Reason:       "needs root",
		ResponseChan: ch,
	})
	if err != nil {
		t.Fatalf("RegisterSudoAuth: %v", err)
	}
	sess := open(t, service.Deps{State: state})

	const wantPassword = "p3-sudo-pw"
	if err := sess.AnswerSudo(context.Background(), key.ID, acpwire.SudoAnswer{OK: true, Password: []byte(wantPassword)}); err != nil {
		t.Fatalf("AnswerSudo: %v", err)
	}
	select {
	case got := <-ch:
		if !got.OK {
			t.Errorf("delivered decision = %+v, want OK", got)
		}
		// The client zeroes the caller's slice when the call returns, so the
		// delivered bytes are compared with the intended secret, not with it.
		if string(got.Password) != wantPassword {
			t.Errorf("delivered password = %q, want the same bytes", got.Password)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the sudo prompt was never answered; the command would block forever")
	}

	err = sess.AnswerSudo(context.Background(), key.ID, acpwire.SudoAnswer{OK: false})
	if !acpclient.IsCode(err, acpwire.CodeRequestSettled) {
		t.Errorf("second AnswerSudo = %v, want a request-settled code", err)
	}
}

// rollbackRestoresAndLogs pins the restore and its audit row, failed or not.
func rollbackRestoresAndLogs(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	state := seamState(t, "s1")
	state.StoreBackup([]session.BackupFile{{Path: "app.go", Content: "original", Exists: true, Mode: 0o644}})
	path := filepath.Join(state.WorkingDir, "app.go")
	if err := os.WriteFile(path, []byte("changed"), 0o644); err != nil {
		t.Fatalf("write app.go: %v", err)
	}
	sess := open(t, service.Deps{State: state})

	if err := sess.Rollback(context.Background()); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read app.go: %v", err)
	}
	if string(data) != "original" {
		t.Errorf("app.go = %q after the rollback, want the backed-up content", data)
	}
	log := state.AuditLog()
	if len(log) == 0 {
		t.Fatal("the rollback logged no audit row")
	}
	last := log[len(log)-1]
	if last.ToolName != "rollback" || last.ResultSummary != "Rollback applied successfully" {
		t.Errorf("last audit row = %+v, want a successful rollback", last)
	}

	// A path this session cannot write makes the restore fail, and the failure
	// reaches the audit trail rather than being hidden.
	if err := os.WriteFile(filepath.Join(state.WorkingDir, "blocker"), []byte("regular file"), 0o644); err != nil {
		t.Fatalf("write the blocker: %v", err)
	}
	state.StoreBackup([]session.BackupFile{{Path: "blocker/x.go", Content: "restored", Exists: true, Mode: 0o644}})
	if err := sess.Rollback(context.Background()); err == nil {
		t.Fatal("Rollback reported success for a restore that could not succeed")
	}
	log = state.AuditLog()
	last = log[len(log)-1]
	if last.ToolName != "rollback" || last.ResultSummary != "Rollback failed" || last.Error == "" {
		t.Errorf("last audit row = %+v, want a failed rollback carrying its error", last)
	}
}

// tappedStream is the agent's end of a pipe, copying every byte it reads and
// writes into its buffers so a test can look for a secret.
type tappedStream struct {
	inner   io.ReadWriteCloser
	read    *syncBuffer
	written *syncBuffer
}

func (s *tappedStream) Read(p []byte) (int, error) {
	n, err := s.inner.Read(p)
	if n > 0 {
		_, _ = s.read.Write(p[:n])
	}
	return n, err
}

func (s *tappedStream) Write(p []byte) (int, error) {
	if len(p) > 0 {
		_, _ = s.written.Write(p)
	}
	return s.inner.Write(p)
}

func (s *tappedStream) Close() error { return s.inner.Close() }

// TestASudoPasswordCrossesTheWireOnce is V5: the password travels in the
// answer's params and nowhere else, and neither the updates nor the response
// nor the log carry it.
func TestASudoPasswordCrossesTheWireOnce(t *testing.T) {
	const password = "p3-sudo-canary-7f3a"
	const sessionID = "s1"

	state := seamState(t, sessionID)
	ch := make(chan session.SudoAuthDecision, 1)
	key, err := state.RegisterSudoAuth(context.Background(), &session.PendingSudoAuth{
		Command:      "apt install jq",
		Reason:       "needs root",
		ResponseChan: ch,
	})
	if err != nil {
		t.Fatalf("RegisterSudoAuth: %v", err)
	}

	_, served := service.New(service.Deps{State: state})

	client, agent := rpc.Pipe()
	tap := &tappedStream{inner: agent, read: &syncBuffer{}, written: &syncBuffer{}}
	logs := &syncBuffer{}
	srv := NewServer(tap, tap, WithLogger(slog.New(slog.NewTextHandler(logs, nil))))
	// The agent's end sets its own cap; neither end inherits the default (P3 R2).
	srv.MaxLineBytes = rpc.MaxFrameBytes
	registerInitialize(srv)
	statuses := NewStatusManager(StatusManagerConfig{
		Lookup: func(id string) (*service.Session, bool) {
			return served, id == sessionID
		},
		Notify:  srv.Notify,
		Marshal: srv.marshalClient,
	})
	registerStatus(srv, statuses)
	requests := serveRequests(t, srv, served, sessionID)
	srv.SetResponseBarrier(responseBarrier(statuses, requests))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Serve(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		_ = tap.Close()
		_ = client.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("the server did not stop")
		}
	})

	host, err := rpc.Dial(ctx, client, rpc.DialOptions{SharedFilesystem: true})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = host.Close() })

	sess, err := host.Attach(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("attach %q: %v", sessionID, err)
	}
	if got := sess.Pending(); got.Sudo == nil || got.Sudo.Key != key.ID {
		t.Fatalf("Pending().Sudo = %+v, want the sudo prompt with key %q", got.Sudo, key.ID)
	}
	if err := sess.AnswerSudo(context.Background(), key.ID, acpwire.SudoAnswer{OK: true, Password: []byte(password)}); err != nil {
		t.Fatalf("AnswerSudo: %v", err)
	}
	select {
	case got := <-ch:
		if !got.OK || string(got.Password) != password {
			t.Fatalf("delivered decision = %+v, want OK with the password", got.OK)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the sudo prompt was never answered")
	}

	encoded := base64.StdEncoding.EncodeToString([]byte(password))
	read := tap.read.String()
	written := tap.written.String()
	logged := logs.String()

	if n := strings.Count(read, encoded); n != 1 {
		t.Errorf("the password's encoding appears %d times in the bytes the agent read, want exactly 1", n)
	}
	// The one occurrence is inside the answer's request, not an earlier frame.
	answerAt := strings.Index(read, "session/answer_sudo")
	passwordAt := strings.Index(read, encoded)
	if answerAt < 0 || passwordAt < answerAt {
		t.Errorf("the password's encoding at %d is not inside the session/answer_sudo request at %d", passwordAt, answerAt)
	}
	if strings.Contains(read, password) {
		t.Error("the agent read the password in clear text, want only its encoding")
	}
	// A pending_requests update listing the prompt was written.
	if !strings.Contains(written, `"kind":"pending_requests"`) {
		t.Error("no pending_requests update was written")
	}
	if !strings.Contains(written, key.ID) {
		t.Errorf("the updates the agent wrote never list the sudo prompt %q", key.ID)
	}
	// Nothing the agent wrote and nothing it logged carries the secret.
	for name, got := range map[string]string{"the bytes the agent wrote": written, "the log": logged} {
		if strings.Contains(got, encoded) {
			t.Errorf("the password's encoding appears in %s", name)
		}
		if strings.Contains(got, password) {
			t.Errorf("the password appears in %s", name)
		}
	}
}

// TestAnswerCallBudget holds R10's 1ms budget for an answer over the pipe on
// the 2,000-call session: the call includes its barrier and its update.
func TestAnswerCallBudget(t *testing.T) {
	if testing.Short() || raceEnabled {
		t.Skip("timing budget: skipped under -short and with the race detector")
	}
	st := largeStackSession(t)
	logLargeStackShape(t, st)
	sess := openPipeSeamTB(t, service.Deps{State: st})

	const runs = 30
	ctx := context.Background()
	durations := make([]time.Duration, 0, runs)
	for i := 0; i < runs; i++ {
		key, err := st.RegisterApproval(ctx, &session.PendingToolCall{
			ID:           "budget-" + string(rune('a'+i)),
			Name:         "shell.run",
			Command:      "go test ./...",
			Risk:         "command",
			ResponseChan: requestApprovalChan(),
		})
		if err != nil {
			t.Fatalf("RegisterApproval: %v", err)
		}
		// The mirror carries the request by the forwarder's next pass; the
		// call itself is what is timed, not this wait.
		deadline := time.Now().Add(5 * time.Second)
		for {
			listed := false
			for _, a := range sess.Pending().Approvals {
				if a.Key == key.ID {
					listed = true
					break
				}
			}
			if listed {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("the mirror never listed the approval %q", key.ID)
			}
			time.Sleep(2 * time.Millisecond)
		}

		start := time.Now()
		if err := sess.AnswerApproval(ctx, key.ID, acpwire.ApprovalAnswer{Decision: acpwire.ApprovalOnce}); err != nil {
			t.Fatalf("AnswerApproval: %v", err)
		}
		durations = append(durations, time.Since(start))
	}
	median := medianDuration(durations)
	t.Logf("answer call: median %v over %d runs (budget 1ms)", median, runs)
	if median >= time.Millisecond {
		t.Fatalf("answer call median = %v, budget 1ms (R10): do not loosen it", median)
	}
}
