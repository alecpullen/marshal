package acp

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/pubsub"
)

// recordingPermissionClient is a thread-safe PermissionClient that records
// every request it is asked and can hold one request open per tool-call id, so
// a test can decide when a reply arrives.
type recordingPermissionClient struct {
	mu       sync.Mutex
	requests []PermissionRequest
	decision PermissionDecision
	err      error
	gates    map[string]chan struct{}
}

func (c *recordingPermissionClient) RequestPermission(ctx context.Context, req PermissionRequest) (PermissionDecision, error) {
	c.mu.Lock()
	c.requests = append(c.requests, req)
	gate := c.gates[req.ToolCallID]
	decision := c.decision
	err := c.err
	c.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return PermissionDecision{}, ctx.Err()
		}
	}
	if err != nil {
		return PermissionDecision{}, err
	}
	return decision, nil
}

func (c *recordingPermissionClient) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.requests)
}

func (c *recordingPermissionClient) last() PermissionRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.requests) == 0 {
		return PermissionRequest{}
	}
	return c.requests[len(c.requests)-1]
}

// childApprovalHarness is one root session with one registered child, wired to
// a TurnManager whose Lookup starts the session-lifetime child collector the
// same way the host's Lookup does.
type childApprovalHarness struct {
	tm     *TurnManager
	root   *session.State
	child  *session.State
	broker *pubsub.Broker[session.Event]
	rt     *TurnRuntime
}

func newChildApprovalHarness(t *testing.T, client PermissionClient) *childApprovalHarness {
	t.Helper()
	root := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	broker := pubsub.NewBroker[session.Event]()
	root.SetEventBroker(broker)

	child := session.New(config.Default(), "/repo", time.Unix(100, 0),
		session.Persistence{SessionID: "child-1"}, session.WithDepth(1))
	child.SetEventBroker(pubsub.NewBroker[session.Event]())
	root.RegisterSubagent("child", child)

	rt := &TurnRuntime{
		SessionID: "sess_child",
		BeginWork: identityBeginWork,
		Run:       RunnerFunc(func(ctx context.Context, prompt string) error { return nil }),
		Events:    broker,
		State:     root,
	}
	var tm *TurnManager
	tm = NewTurnManager(TurnManagerConfig{
		Lookup: func(sessionID string) (*TurnRuntime, bool) {
			tm.ensureChildApprovals(sessionID, rt)
			return rt, true
		},
		Notify: func(method string, params any) error { return nil },
		Perms:  client,
	})
	t.Cleanup(tm.CloseChildApprovals)

	h := &childApprovalHarness{tm: tm, root: root, child: child, broker: broker, rt: rt}
	return h
}

// start begins the collector without running a turn, mirroring the host's
// Lookup-time start.
func (h *childApprovalHarness) start() { h.tm.ensureChildApprovals("sess_child", h.rt) }

func TestACPChildApprovalUsesOwningSessionRegistry(t *testing.T) {
	client := &recordingPermissionClient{decision: PermissionDecision{Approved: true}}
	h := newChildApprovalHarness(t, client)
	h.start()

	response := make(chan session.UserApprovalDecision, 1)
	pending := &session.PendingToolCall{ID: "c1", Name: "shell.run", Command: "date", ResponseChan: response}
	if _, err := h.child.RegisterApproval(context.Background(), pending); err != nil {
		t.Fatalf("RegisterApproval: %v", err)
	}

	select {
	case got := <-response:
		if !got.Approved {
			t.Fatalf("decision = %+v, want approved", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("child approval was not settled through the owning registry")
	}
	if h.child.PendingApproval() != nil {
		t.Fatal("the child's pending slot was not cleared by the canonical settlement")
	}
	if h.root.PendingApproval() != nil {
		t.Fatal("the child's approval occupied the root pending slot")
	}
}

func TestACPPipelineChildDestructiveApprovalIsPresented(t *testing.T) {
	client := &recordingPermissionClient{decision: PermissionDecision{Approved: false}}
	h := newChildApprovalHarness(t, client)
	h.start()

	response := make(chan session.UserApprovalDecision, 1)
	pending := &session.PendingToolCall{
		ID: "c-destructive", Name: "shell.run", Command: "rm -rf build",
		Risk: "destructive", Reason: "removes build output", ResponseChan: response,
	}
	if _, err := h.child.RegisterApproval(context.Background(), pending); err != nil {
		t.Fatalf("RegisterApproval: %v", err)
	}

	select {
	case got := <-response:
		if got.Approved {
			t.Fatalf("decision = %+v, want denied", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("destructive child approval was not presented")
	}

	req := client.last()
	if req.SessionID != "sess_child" {
		t.Fatalf("SessionID = %q, want sess_child", req.SessionID)
	}
	if req.ToolName != "shell.run" || req.Command != "rm -rf build" {
		t.Fatalf("request = %+v, want the child's shell.run command", req)
	}
	if req.Risk != "destructive" {
		t.Fatalf("Risk = %q, want destructive", req.Risk)
	}
	if !strings.Contains(req.Reason, `subagent "child-1"`) {
		t.Fatalf("Reason = %q, want the child actor attribution", req.Reason)
	}
	if !strings.Contains(req.Reason, "removes build output") {
		t.Fatalf("Reason = %q, want the child's own reason preserved", req.Reason)
	}
}

func TestACPChildApprovalAfterParentTurnStillResolves(t *testing.T) {
	client := &recordingPermissionClient{decision: PermissionDecision{Approved: true}}
	h := newChildApprovalHarness(t, client)

	raw := json.RawMessage(`{"sessionId":"sess_child","prompt":[{"type":"text","text":"hi"}]}`)
	if _, err := h.tm.PromptTurn(context.Background(), raw); err != nil {
		t.Fatalf("PromptTurn: %v", err)
	}
	if h.tm.HasActiveTurn("sess_child") {
		t.Fatal("the parent turn is still active")
	}

	response := make(chan session.UserApprovalDecision, 1)
	pending := &session.PendingToolCall{ID: "c-late", Name: "shell.run", Command: "date", ResponseChan: response}
	if _, err := h.child.RegisterApproval(context.Background(), pending); err != nil {
		t.Fatalf("RegisterApproval: %v", err)
	}

	select {
	case got := <-response:
		if !got.Approved {
			t.Fatalf("decision = %+v, want approved", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("child approval after the parent turn ended was not resolved")
	}
}

func TestACPChildApprovalDuplicateEventsAskOnce(t *testing.T) {
	client := &recordingPermissionClient{decision: PermissionDecision{Approved: true}}
	h := newChildApprovalHarness(t, client)
	h.start()

	response := make(chan session.UserApprovalDecision, 1)
	pending := &session.PendingToolCall{ID: "c-dup", Name: "shell.run", Command: "date", ResponseChan: response}
	key, err := h.child.RegisterApproval(context.Background(), pending)
	if err != nil {
		t.Fatalf("RegisterApproval: %v", err)
	}
	// The registration already forwarded one event; publish the same identity
	// twice more, as a duplicate forward would.
	src := &session.ApprovalSource{Owner: h.child, Key: key, Pending: pending, Actor: h.child.SessionID()}
	h.broker.Publish(session.EventPendingApprovalChanged, session.Event{ApprovalSource: src})
	h.broker.Publish(session.EventPendingApprovalChanged, session.Event{ApprovalSource: src})

	select {
	case <-response:
	case <-time.After(2 * time.Second):
		t.Fatal("child approval was not settled")
	}
	// Give any duplicate dispatch a chance to run before counting.
	time.Sleep(100 * time.Millisecond)
	if got := client.count(); got != 1 {
		t.Fatalf("client asked %d times, want 1", got)
	}
}

func TestACPChildApprovalCancellationLeavesNewerRequestUntouched(t *testing.T) {
	gateA := make(chan struct{})
	gateB := make(chan struct{})
	client := &recordingPermissionClient{
		decision: PermissionDecision{Approved: true},
		gates:    map[string]chan struct{}{"c-a": gateA, "c-b": gateB},
	}
	h := newChildApprovalHarness(t, client)
	h.start()

	first := make(chan session.UserApprovalDecision, 1)
	pendingA := &session.PendingToolCall{ID: "c-a", Name: "shell.run", Command: "date", ResponseChan: first}
	keyA, err := h.child.RegisterApproval(context.Background(), pendingA)
	if err != nil {
		t.Fatalf("RegisterApproval(A): %v", err)
	}
	waitFor(t, "the client to be asked for the first approval", func() bool { return client.count() == 1 })

	// The child's own operation is interrupted: its canonical registry settles
	// A as canceled.
	if !h.child.CancelPrompt(keyA, session.ErrSendNow) {
		t.Fatal("CancelPrompt did not win the first request")
	}

	// A newer request occupies the child's slot.
	second := make(chan session.UserApprovalDecision, 1)
	pendingB := &session.PendingToolCall{ID: "c-b", Name: "shell.run", Command: "ls", ResponseChan: second}
	if _, err := h.child.RegisterApproval(context.Background(), pendingB); err != nil {
		t.Fatalf("RegisterApproval(B): %v", err)
	}
	waitFor(t, "the client to be asked for the newer approval", func() bool { return client.count() == 2 })

	// The late reply for the canceled request arrives now.
	close(gateA)

	select {
	case got := <-second:
		t.Fatalf("a late reply for the canceled request settled the newer one: %+v", got)
	case <-time.After(200 * time.Millisecond):
	}
	if h.child.PendingApproval() == nil {
		t.Fatal("the newer request was cleared by a late reply")
	}

	// The newer request still settles on its own answer.
	close(gateB)
	select {
	case got := <-second:
		if !got.Approved {
			t.Fatalf("newer decision = %+v, want approved", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the newer request never settled")
	}
}

func TestACPChildApprovalMissingResponderFailsClosed(t *testing.T) {
	h := newChildApprovalHarness(t, nil)
	h.start()

	response := make(chan session.UserApprovalDecision, 1)
	pending := &session.PendingToolCall{ID: "c-nobridge", Name: "shell.run", Command: "date", ResponseChan: response}
	if _, err := h.child.RegisterApproval(context.Background(), pending); err != nil {
		t.Fatalf("RegisterApproval: %v", err)
	}

	select {
	case got := <-response:
		if got.Approved {
			t.Fatalf("decision = %+v, want a denial without a responder", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("child approval was not settled without a responder")
	}
	if h.child.PendingApproval() != nil {
		t.Fatal("the child's pending slot was not cleared")
	}
}
