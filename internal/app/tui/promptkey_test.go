package tui

import (
	"context"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
)

// promptTestState builds a session state for the prompt-identity tests, the
// way the rest of the package does.
func promptTestState(t *testing.T) *session.State {
	t.Helper()
	return session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
}

// TestFocusedPromptKeyFollowsTheRoutingOrder pins that the focused request is
// the one the update routing resolves first — the sudo prompt before the
// question — and that its identity is the wire key Pending reports.
func TestFocusedPromptKeyFollowsTheRoutingOrder(t *testing.T) {
	state := promptTestState(t)
	m := testModel(state)

	questionKey, err := state.RegisterQuestion(context.Background(), &session.PendingQuestion{
		Questions:    []session.Question{{Question: "Which?"}},
		ResponseChan: make(chan []session.Answer, 1),
	})
	if err != nil {
		t.Fatalf("RegisterQuestion: %v", err)
	}
	state.SetPendingSudoAuth(&session.PendingSudoAuth{
		Command:      "apt install jq",
		ResponseChan: make(chan session.SudoAuthDecision, 1),
	})

	ref, ok := m.focusedPromptKey()
	if !ok {
		t.Fatal("focusedPromptKey reported no request with a sudo prompt open")
	}
	sudoKey := m.sess.Pending().Sudo.Key
	if ref.kind != promptSudo || ref.key != sudoKey {
		t.Fatalf("focusedPromptKey = %+v, want the sudo prompt %q", ref, sudoKey)
	}

	state.SetPendingSudoAuth(nil)
	ref, ok = m.focusedPromptKey()
	if !ok {
		t.Fatal("focusedPromptKey reported no request with a question open")
	}
	if ref.kind != promptQuestion || ref.key != questionKey.ID {
		t.Fatalf("focusedPromptKey = %+v, want the question %q", ref, questionKey.ID)
	}
}

// TestPromptKeyByIDFindsASubagentsApproval pins that a work item naming a
// subagent's approval can navigate to it: the lookup keeps every approval
// Pending lists, not only the session's own.
func TestPromptKeyByIDFindsASubagentsApproval(t *testing.T) {
	state := promptTestState(t)
	m := testModel(state)

	child := promptTestState(t)
	state.RegisterSubagent("scout", child)
	key, err := child.RegisterApproval(context.Background(), &session.PendingToolCall{
		ID:           "call-child",
		Name:         "shell.run",
		Command:      "go vet ./...",
		ResponseChan: make(chan session.UserApprovalDecision, 1),
	})
	if err != nil {
		t.Fatalf("child RegisterApproval: %v", err)
	}

	ref, ok := m.promptKeyByID(key.ID)
	if !ok {
		t.Fatalf("promptKeyByID(%q) reported no request", key.ID)
	}
	if ref.kind != promptApproval || ref.key != key.ID {
		t.Fatalf("promptKeyByID(%q) = %+v, want the subagent's approval", key.ID, ref)
	}
}

// TestPromptStillPendingFollowsTheRequest pins that a request's identity stops
// matching as soon as it settles.
func TestPromptStillPendingFollowsTheRequest(t *testing.T) {
	state := promptTestState(t)
	m := testModel(state)

	key, err := state.RegisterQuestion(context.Background(), &session.PendingQuestion{
		Questions:    []session.Question{{Question: "Which?"}},
		ResponseChan: make(chan []session.Answer, 1),
	})
	if err != nil {
		t.Fatalf("RegisterQuestion: %v", err)
	}
	ref, ok := m.focusedPromptKey()
	if !ok {
		t.Fatal("focusedPromptKey reported no request")
	}
	if !m.promptStillPending(ref) {
		t.Fatalf("promptStillPending(%+v) = false for an open question", ref)
	}

	if !state.ResolveQuestion(key, []session.Answer{{Question: "Which?", Answer: "A"}}) {
		t.Fatal("ResolveQuestion lost the claim")
	}
	if m.promptStillPending(ref) {
		t.Fatalf("promptStillPending(%+v) = true after the question settled", ref)
	}
}
