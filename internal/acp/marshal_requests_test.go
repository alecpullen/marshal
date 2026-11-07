package acp

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"marshal/internal/app/session"
	"marshal/internal/pubsub"
)

// TestATurnAsksAMarshalClientNothing pins R5 from the turn's side: for a
// client that declared the marshal capability, the request mirror shows the
// request and nothing in the turn may answer it, deny it or fail it. The
// runner publishes an approval, a question and a skill gate and waits; none of
// them is settled, and neither outbound client is called.
func TestATurnAsksAMarshalClientNothing(t *testing.T) {
	broker := pubsub.NewBroker[session.Event]()

	approvalCh := make(chan session.UserApprovalDecision, 1)
	approval := &session.PendingToolCall{
		ID:           "tool-1",
		Name:         "shell.run",
		Command:      "rm tmp",
		ResponseChan: approvalCh,
	}
	questionCh := make(chan []session.Answer, 1)
	question := &session.PendingQuestion{
		Questions:    []session.Question{{Question: "pick"}},
		ResponseChan: questionCh,
	}
	gateCh := make(chan session.SkillGateChoice, 1)
	gate := &session.PendingSkillGate{
		Skill:        "go-testing",
		Description:  "Load the go-testing skill",
		ResponseChan: gateCh,
	}

	perms := &fakePermissionClient{decision: PermissionDecision{Approved: true}}
	questions := &fakeQuestionClient{resp: QuestionResponse{
		Answers: []session.Answer{{Question: "pick", Answer: "a"}},
	}}
	manager := NewTurnManager(TurnManagerConfig{
		Lookup: func(sessionID string) (*TurnRuntime, bool) {
			return &TurnRuntime{
				SessionID: sessionID,
				BeginWork: identityBeginWork,
				Run: RunnerFunc(func(ctx context.Context, prompt string) error {
					broker.Publish(session.EventPendingApprovalChanged, session.Event{PendingApproval: approval})
					broker.Publish(session.EventPendingQuestionChanged, session.Event{PendingQuestion: question})
					broker.Publish(session.EventPendingSkillGateChanged, session.Event{PendingSkillGate: gate})
					// Give any forwarder that did start a chance to ask.
					select {
					case <-time.After(300 * time.Millisecond):
					case <-ctx.Done():
						return ctx.Err()
					}
					return nil
				}),
				Events: broker,
			}, true
		},
		Notify:    func(method string, params any) error { return nil },
		Perms:     perms,
		Questions: questions,
		Marshal:   func() bool { return true },
	})
	if _, err := manager.PromptTurn(context.Background(), json.RawMessage(`{"sessionId":"sess_test","prompt":[{"type":"text","text":"hi"}]}`)); err != nil {
		t.Fatalf("PromptTurn() error = %v", err)
	}
	select {
	case got := <-approvalCh:
		t.Fatalf("the marshal client's approval was settled: %+v", got)
	default:
	}
	select {
	case got := <-questionCh:
		t.Fatalf("the marshal client's question was answered: %+v", got)
	default:
	}
	select {
	case got := <-gateCh:
		t.Fatalf("the marshal client's skill gate was settled: %v", got)
	default:
	}
	if perms.calls != 0 {
		t.Fatalf("permission client calls = %d, want 0", perms.calls)
	}
	if questions.calls != 0 {
		t.Fatalf("question client calls = %d, want 0", questions.calls)
	}
}

// TestATurnStillAsksAClientWithoutTheCapability is the same turn for a client
// that did not declare the capability: the ordinary bridges still ask it.
func TestATurnStillAsksAClientWithoutTheCapability(t *testing.T) {
	broker := pubsub.NewBroker[session.Event]()

	approvalCh := make(chan session.UserApprovalDecision, 1)
	approval := &session.PendingToolCall{
		ID:           "tool-1",
		Name:         "shell.run",
		Command:      "rm tmp",
		ResponseChan: approvalCh,
	}
	questionCh := make(chan []session.Answer, 1)
	question := &session.PendingQuestion{
		Questions:    []session.Question{{Question: "pick"}},
		ResponseChan: questionCh,
	}
	gateCh := make(chan session.SkillGateChoice, 1)
	gate := &session.PendingSkillGate{
		Skill:        "go-testing",
		Description:  "Load the go-testing skill",
		ResponseChan: gateCh,
	}

	perms := &fakePermissionClient{decision: PermissionDecision{Approved: true}}
	questions := &fakeQuestionClient{resp: QuestionResponse{
		Answers: []session.Answer{{Question: "pick", Answer: "a"}},
	}}
	manager := NewTurnManager(TurnManagerConfig{
		Lookup: func(sessionID string) (*TurnRuntime, bool) {
			return &TurnRuntime{
				SessionID: sessionID,
				BeginWork: identityBeginWork,
				Run: RunnerFunc(func(ctx context.Context, prompt string) error {
					broker.Publish(session.EventPendingApprovalChanged, session.Event{PendingApproval: approval})
					broker.Publish(session.EventPendingQuestionChanged, session.Event{PendingQuestion: question})
					broker.Publish(session.EventPendingSkillGateChanged, session.Event{PendingSkillGate: gate})
					for _, wait := range []func() bool{
						func() bool {
							select {
							case <-approvalCh:
								return true
							case <-ctx.Done():
								return false
							}
						},
						func() bool {
							select {
							case <-questionCh:
								return true
							case <-ctx.Done():
								return false
							}
						},
						func() bool {
							select {
							case <-gateCh:
								return true
							case <-ctx.Done():
								return false
							}
						},
					} {
						if !wait() {
							return ctx.Err()
						}
					}
					return nil
				}),
				Events: broker,
			}, true
		},
		Notify:    func(method string, params any) error { return nil },
		Perms:     perms,
		Questions: questions,
	})
	if _, err := manager.PromptTurn(context.Background(), json.RawMessage(`{"sessionId":"sess_test","prompt":[{"type":"text","text":"hi"}]}`)); err != nil {
		t.Fatalf("PromptTurn() error = %v", err)
	}
	if perms.calls != 2 {
		t.Fatalf("permission client calls = %d, want 2 (the approval and the skill gate)", perms.calls)
	}
	if questions.calls != 1 {
		t.Fatalf("question client calls = %d, want 1", questions.calls)
	}
}

// TestTheChildCollectorLeavesAMarshalClientsApprovals pins R5 for the
// session-lifetime child collector: it never starts for a marshal client, so a
// child approval stays open for the request mirror instead of being asked,
// denied or settled here.
func TestTheChildCollectorLeavesAMarshalClientsApprovals(t *testing.T) {
	client := &recordingPermissionClient{decision: PermissionDecision{Approved: true}}
	h := newChildApprovalHarness(t, client)
	h.tm.marshal = func() bool { return true }
	h.start()

	response := make(chan session.UserApprovalDecision, 1)
	pending := &session.PendingToolCall{ID: "m1", Name: "shell.run", Command: "date", ResponseChan: response}
	if _, err := h.child.RegisterApproval(context.Background(), pending); err != nil {
		t.Fatalf("RegisterApproval: %v", err)
	}

	select {
	case got := <-response:
		t.Fatalf("the marshal client's child approval was settled: %+v", got)
	case <-time.After(300 * time.Millisecond):
	}
	if client.count() != 0 {
		t.Fatalf("permission client calls = %d, want 0", client.count())
	}
	if h.child.PendingApproval() == nil {
		t.Fatal("the child's approval was cleared for a marshal client")
	}
}
