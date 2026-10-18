package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/pubsub"
)

type fakePermissionClient struct {
	decision PermissionDecision
	err      error
	calls    int
	gotReq   PermissionRequest
	// decide, when set, computes the decision from the request instead of
	// returning the fixed decision. It lets a test answer with the typed
	// shape (echoing the request's identity) or change state mid-decision.
	decide func(PermissionRequest) PermissionDecision
}

func (f *fakePermissionClient) RequestPermission(ctx context.Context, req PermissionRequest) (PermissionDecision, error) {
	f.calls++
	f.gotReq = req
	if f.err != nil {
		return PermissionDecision{}, f.err
	}
	if f.decide != nil {
		return f.decide(req), nil
	}
	return f.decision, f.err
}

func TestPermissionApprovalResolvesPendingToolCall(t *testing.T) {
	response := make(chan session.UserApprovalDecision, 1)
	pending := &session.PendingToolCall{Name: "shell.run", Command: "date", ResponseChan: response}
	client := &fakePermissionClient{decision: PermissionDecision{Approved: true}}
	bridge := NewPermissionBridge(client)
	if _, err := bridge.Request(context.Background(), "sess_test", pending); err != nil {
		t.Fatalf("Request() error = %v", err)
	}
	select {
	case got := <-response:
		if !got.Approved {
			t.Fatalf("Approved = false, want true")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for approval decision")
	}
	if client.calls != 1 {
		t.Fatalf("client.calls = %d, want 1", client.calls)
	}
	if client.gotReq.SessionID != "sess_test" {
		t.Fatalf("client.gotReq.SessionID = %q, want sess_test", client.gotReq.SessionID)
	}
	if client.gotReq.ToolName != "shell.run" {
		t.Fatalf("client.gotReq.ToolName = %q, want shell.run", client.gotReq.ToolName)
	}
	if client.gotReq.Command != "date" {
		t.Fatalf("client.gotReq.Command = %q, want date", client.gotReq.Command)
	}
}

func TestPermissionDenialResolvesPendingToolCall(t *testing.T) {
	response := make(chan session.UserApprovalDecision, 1)
	pending := &session.PendingToolCall{Name: "shell.run", Command: "rm -rf /", ResponseChan: response}
	client := &fakePermissionClient{decision: PermissionDecision{Approved: false}}
	bridge := NewPermissionBridge(client)
	if _, err := bridge.Request(context.Background(), "sess_test", pending); err != nil {
		t.Fatalf("Request() error = %v", err)
	}
	select {
	case got := <-response:
		if got.Approved {
			t.Fatalf("Approved = true, want false")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for approval decision")
	}
}

func TestPermissionEditedCommandPropagatesToDecision(t *testing.T) {
	response := make(chan session.UserApprovalDecision, 1)
	pending := &session.PendingToolCall{Name: "shell.run", Command: "rm foo", ResponseChan: response}
	client := &fakePermissionClient{decision: PermissionDecision{Approved: true, Edited: "rm foo.bak"}}
	bridge := NewPermissionBridge(client)
	if _, err := bridge.Request(context.Background(), "sess_test", pending); err != nil {
		t.Fatalf("Request() error = %v", err)
	}
	select {
	case got := <-response:
		if !got.Approved {
			t.Fatalf("Approved = false, want true")
		}
		if got.Edited != "rm foo.bak" {
			t.Fatalf("Edited = %q, want rm foo.bak", got.Edited)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for approval decision")
	}
}

func TestPermissionRequestContextCancelGuardsResponseChan(t *testing.T) {
	response := make(chan session.UserApprovalDecision)
	pending := &session.PendingToolCall{Name: "shell.run", Command: "ls", ResponseChan: response}
	// blocking client — never returns
	client := blockingPermissionClient{}
	bridge := NewPermissionBridge(client)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- func() error {
			_, err := bridge.Request(ctx, "sess_test", pending)
			return err
		}()
	}()

	// Give the bridge a moment to enter Request.
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case <-response:
		t.Fatal("ResponseChan was written to after context cancel")
	case <-done:
		// Request returned because of context cancel; ResponseChan untouched.
	}
	// Even after Request returns, ResponseChan must not have been written.
	select {
	case <-response:
		t.Fatal("ResponseChan was written to after Request returned from cancel")
	default:
	}
}

type blockingPermissionClient struct{}

func (blockingPermissionClient) RequestPermission(ctx context.Context, req PermissionRequest) (PermissionDecision, error) {
	<-ctx.Done()
	return PermissionDecision{}, ctx.Err()
}

func TestPromptTurnBridgesPendingApprovalToPermissionClient(t *testing.T) {
	broker := pubsub.NewBroker[session.Event]()
	pendingCh := make(chan session.UserApprovalDecision, 1)
	pending := &session.PendingToolCall{
		ID:           "tool-1",
		Name:         "shell.run",
		Command:      "rm tmp",
		ResponseChan: pendingCh,
	}
	client := &fakePermissionClient{decision: PermissionDecision{Approved: true, Edited: "rm tmp.bak"}}
	manager := NewTurnManager(TurnManagerConfig{
		Lookup: func(sessionID string) (*TurnRuntime, bool) {
			return &TurnRuntime{
				SessionID: sessionID,
				BeginWork: identityBeginWork,
				Run: RunnerFunc(func(ctx context.Context, prompt string) error {
					broker.Publish(session.EventPendingApprovalChanged, session.Event{PendingApproval: pending})
					// Wait for the bridge to deliver the decision.
					select {
					case <-pendingCh:
					case <-ctx.Done():
						return ctx.Err()
					}
					return nil
				}),
				Events: broker,
			}, true
		},
		Notify: func(method string, params any) error { return nil },
		Perms:  client,
	})
	if _, err := manager.PromptTurn(context.Background(), json.RawMessage(`{"sessionId":"sess_test","prompt":[{"type":"text","text":"hi"}]}`)); err != nil {
		t.Fatalf("PromptTurn() error = %v", err)
	}
	if client.calls != 1 {
		t.Fatalf("client.calls = %d, want 1", client.calls)
	}
	if client.gotReq.ToolCallID != "tool-1" || client.gotReq.ToolName != "shell.run" || client.gotReq.Command != "rm tmp" {
		t.Fatalf("client.gotReq = %+v", client.gotReq)
	}
}

func TestRequestSkillGateMapsApproveToAllowOnce(t *testing.T) {
	response := make(chan session.SkillGateChoice, 1)
	pending := &session.PendingSkillGate{
		Skill:        "go-testing",
		Description:  "Load the go-testing skill",
		Reason:       "small-context model",
		ResponseChan: response,
	}
	client := &fakePermissionClient{decision: PermissionDecision{Approved: true}}
	bridge := NewPermissionBridge(client)
	got, err := bridge.RequestSkillGate(context.Background(), "sess_test", pending)
	if err != nil {
		t.Fatalf("RequestSkillGate() error = %v", err)
	}
	if got != session.SkillGateAllowOnce {
		t.Fatalf("RequestSkillGate() = %v, want SkillGateAllowOnce", got)
	}
	select {
	case delivered := <-response:
		if delivered != session.SkillGateAllowOnce {
			t.Fatalf("ResponseChan delivered %v, want SkillGateAllowOnce", delivered)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for skill-gate choice")
	}
	if client.calls != 1 {
		t.Fatalf("client.calls = %d, want 1", client.calls)
	}
	if client.gotReq.SessionID != "sess_test" {
		t.Fatalf("client.gotReq.SessionID = %q, want sess_test", client.gotReq.SessionID)
	}
	if !client.gotReq.SkillGate {
		t.Fatalf("client.gotReq.SkillGate = false, want true")
	}
	if client.gotReq.ToolName != "skill.load" {
		t.Fatalf("client.gotReq.ToolName = %q, want skill.load", client.gotReq.ToolName)
	}
	if client.gotReq.Command != "go-testing" {
		t.Fatalf("client.gotReq.Command = %q, want go-testing", client.gotReq.Command)
	}
	if client.gotReq.ToolCallID != "skillgate-go-testing" {
		t.Fatalf("client.gotReq.ToolCallID = %q, want skillgate-go-testing", client.gotReq.ToolCallID)
	}
	if client.gotReq.Reason != "small-context model" {
		t.Fatalf("client.gotReq.Reason = %q, want small-context model", client.gotReq.Reason)
	}
}

func TestRequestSkillGateMapsDenyToDeny(t *testing.T) {
	response := make(chan session.SkillGateChoice, 1)
	pending := &session.PendingSkillGate{
		Skill:        "go-testing",
		Description:  "Load the go-testing skill",
		ResponseChan: response,
	}
	client := &fakePermissionClient{decision: PermissionDecision{Approved: false}}
	bridge := NewPermissionBridge(client)
	got, err := bridge.RequestSkillGate(context.Background(), "sess_test", pending)
	if err != nil {
		t.Fatalf("RequestSkillGate() error = %v", err)
	}
	if got != session.SkillGateDeny {
		t.Fatalf("RequestSkillGate() = %v, want SkillGateDeny", got)
	}
	select {
	case delivered := <-response:
		if delivered != session.SkillGateDeny {
			t.Fatalf("ResponseChan delivered %v, want SkillGateDeny", delivered)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for skill-gate choice")
	}
}

func TestRequestSkillGateContextCancelLeavesChannel(t *testing.T) {
	response := make(chan session.SkillGateChoice)
	pending := &session.PendingSkillGate{
		Skill:        "go-testing",
		Description:  "Load the go-testing skill",
		ResponseChan: response,
	}
	// blocking client — never returns
	client := blockingPermissionClient{}
	bridge := NewPermissionBridge(client)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- func() error {
			_, err := bridge.RequestSkillGate(ctx, "sess_test", pending)
			return err
		}()
	}()

	// Give the bridge a moment to enter RequestSkillGate.
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case <-response:
		t.Fatal("ResponseChan was written to after context cancel")
	case err := <-done:
		if err == nil {
			t.Fatal("RequestSkillGate() error = nil, want context error")
		}
	}
	// Even after RequestSkillGate returns, ResponseChan must not have been
	// written or closed.
	select {
	case got, ok := <-response:
		if ok {
			t.Fatalf("ResponseChan was written to after cancel: got %v", got)
		}
		t.Fatal("ResponseChan was closed after cancel")
	default:
	}
}

func TestRequestSkillGateNilPending(t *testing.T) {
	bridge := NewPermissionBridge(&fakePermissionClient{})
	if _, err := bridge.RequestSkillGate(context.Background(), "sess_test", nil); !errors.Is(err, ErrNilPending) {
		t.Fatalf("RequestSkillGate(nil) error = %v, want ErrNilPending", err)
	}
}

func TestRequestSkillGateNilResponseChan(t *testing.T) {
	bridge := NewPermissionBridge(&fakePermissionClient{})
	pending := &session.PendingSkillGate{Skill: "go-testing"}
	if _, err := bridge.RequestSkillGate(context.Background(), "sess_test", pending); !errors.Is(err, ErrPendingMissingResponseChan) {
		t.Fatalf("RequestSkillGate(no ResponseChan) error = %v, want ErrPendingMissingResponseChan", err)
	}
}

// ---- typed access collection ----

// accessRequestFixture builds a session with one published typed request.
func accessRequestFixture(t *testing.T, intent session.AccessIntent) (*session.State, session.AccessRequest, chan session.UserApprovalDecision) {
	t.Helper()
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	call := &session.PendingToolCall{
		ID:           "call-1",
		Name:         "mode.request",
		Reason:       intent.Reason,
		ResponseChan: make(chan session.UserApprovalDecision, 1),
	}
	req, waiting, err := state.RegisterAccess(context.Background(), call, intent)
	if err != nil {
		t.Fatalf("RegisterAccess: %v", err)
	}
	if !waiting {
		t.Fatalf("RegisterAccess published no request for intent %+v", intent)
	}
	return state, req, call.ResponseChan
}

// The typed payloads round-trip through JSON with the exact identity, current
// tuple and missing flags, and the outer Approved field carries the decision.
func TestCollectAccessTypedWireAndChoice(t *testing.T) {
	_, req, _ := accessRequestFixture(t, session.AccessIntent{
		Editing: true, System: true, Reason: "edit and read outside",
	})

	// The request payload the bridge sends.
	wire := &ModeAccessRequest{
		RequestID: req.RequestID, SessionID: req.SessionID,
		Actor: req.Actor, StepID: req.StepID, Reason: req.Reason,
		Current:        ModeAccessState{Mode: req.Current.Mode, System: req.Current.System},
		MissingEditing: req.MissingEditing, MissingSystem: req.MissingSystem,
	}
	encoded, err := json.Marshal(PermissionRequest{
		SessionID: req.SessionID, ToolCallID: "call-1", ToolName: "mode.request",
		Risk: "read_only", ModeAccess: wire,
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	var decodedReq PermissionRequest
	if err := json.Unmarshal(encoded, &decodedReq); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}
	if decodedReq.ModeAccess == nil {
		t.Fatal("modeAccess did not survive the round trip")
	}
	if got := decodedReq.ModeAccess; got.RequestID != req.RequestID ||
		got.SessionID != req.SessionID ||
		got.Current.Mode != req.Current.Mode ||
		got.Current.System != req.Current.System ||
		got.MissingEditing != req.MissingEditing ||
		got.MissingSystem != req.MissingSystem {
		t.Fatalf("round-tripped modeAccess = %+v, want the request's identity and flags", got)
	}

	// The choice payload the client returns.
	encodedChoice, err := json.Marshal(PermissionDecision{
		Approved: true,
		ModeAccess: &ModeAccessChoice{
			RequestID: req.RequestID, SessionID: req.SessionID,
			Current: ModeAccessState{Mode: req.Current.Mode, System: req.Current.System},
			Mode:    "copilot",
		},
	})
	if err != nil {
		t.Fatalf("marshal decision: %v", err)
	}
	var decodedChoice PermissionDecision
	if err := json.Unmarshal(encodedChoice, &decodedChoice); err != nil {
		t.Fatalf("unmarshal decision: %v", err)
	}
	if !decodedChoice.Approved {
		t.Fatal("the outer Approved field did not survive the round trip")
	}
	if decodedChoice.ModeAccess == nil || decodedChoice.ModeAccess.Mode != "copilot" {
		t.Fatalf("round-tripped modeAccess choice = %+v, want the selected variant", decodedChoice.ModeAccess)
	}

	// The bridge maps the wire choice onto the session choice.
	client := &fakePermissionClient{decision: decodedChoice}
	bridge := NewPermissionBridge(client)
	choice, err := bridge.CollectAccess(context.Background(), req.SessionID, "call-1", req)
	if err != nil {
		t.Fatalf("CollectAccess: %v", err)
	}
	if !choice.Approved {
		t.Fatal("the outer Approved field did not control approval")
	}
	if choice.RequestID != req.RequestID || choice.SessionID != req.SessionID {
		t.Fatalf("choice identity = %q/%q, want the request's", choice.RequestID, choice.SessionID)
	}
	if choice.Current != req.Current {
		t.Fatalf("choice current = %+v, want the presented %+v", choice.Current, req.Current)
	}
	if choice.Mode != "copilot" {
		t.Fatalf("choice mode = %q, want copilot", choice.Mode)
	}
}

// Collection never acknowledges the canonical waiter: only the runtime's own
// resolution delivers a result.
func TestCollectAccessNeverAcknowledgesPendingTool(t *testing.T) {
	state, req, decisions := accessRequestFixture(t, session.AccessIntent{
		System: true, Reason: "read outside the workspace",
	})

	client := &fakePermissionClient{decision: PermissionDecision{Approved: true}}
	bridge := NewPermissionBridge(client)
	choice, err := bridge.CollectAccess(context.Background(), req.SessionID, "call-1", req)
	if err != nil {
		t.Fatalf("CollectAccess: %v", err)
	}
	if !choice.Approved {
		t.Fatal("the collected choice lost the approval")
	}

	// The waiter is still blocked: collection is not settlement.
	select {
	case d := <-decisions:
		t.Fatalf("collection acknowledged the pending tool: %+v", d)
	default:
	}
	if _, pending := state.AccessRequest(); !pending {
		t.Fatal("collection cleared the pending request")
	}

	// Only the runtime's resolution delivers.
	result, err := state.ResolveAccess(choice, func(r session.AccessRequest, c session.AccessChoice) (session.AccessCommit, error) {
		target := r.Current
		target.System = true
		return session.AccessCommit{Effective: target, Apply: func() {}}, nil
	})
	if err != nil {
		t.Fatalf("ResolveAccess: %v", err)
	}
	if result.Status != session.AccessGranted {
		t.Fatalf("status = %q, want %q", result.Status, session.AccessGranted)
	}
	select {
	case d := <-decisions:
		if !d.Approved || d.Access == nil || d.Access.Status != session.AccessGranted {
			t.Fatalf("delivered decision = %+v, want the committed grant", d)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the runtime's resolution never delivered")
	}
}

// A legacy client's Edited text is used only where an editing selector was
// actually presented.
func TestCollectAccessLegacySelectionIsScopeBound(t *testing.T) {
	t.Run("editing with empty Edited defaults to edit", func(t *testing.T) {
		_, req, _ := accessRequestFixture(t, session.AccessIntent{Editing: true, Reason: "edit a file"})
		bridge := NewPermissionBridge(&fakePermissionClient{decision: PermissionDecision{Approved: true}})
		choice, err := bridge.CollectAccess(context.Background(), req.SessionID, "call-1", req)
		if err != nil {
			t.Fatalf("CollectAccess: %v", err)
		}
		if choice.Mode != "edit" {
			t.Fatalf("mode = %q, want the edit default", choice.Mode)
		}
	})

	t.Run("editing relays the client's variant", func(t *testing.T) {
		_, req, _ := accessRequestFixture(t, session.AccessIntent{Editing: true, Reason: "edit a file"})
		bridge := NewPermissionBridge(&fakePermissionClient{decision: PermissionDecision{Approved: true, Edited: "auto"}})
		choice, err := bridge.CollectAccess(context.Background(), req.SessionID, "call-1", req)
		if err != nil {
			t.Fatalf("CollectAccess: %v", err)
		}
		if choice.Mode != "auto" {
			t.Fatalf("mode = %q, want auto", choice.Mode)
		}
	})

	t.Run("system-only ignores Edited", func(t *testing.T) {
		for _, edited := range []string{"auto", "rm -rf /", "copilot"} {
			_, req, _ := accessRequestFixture(t, session.AccessIntent{System: true, Reason: "read outside"})
			bridge := NewPermissionBridge(&fakePermissionClient{decision: PermissionDecision{Approved: true, Edited: edited}})
			choice, err := bridge.CollectAccess(context.Background(), req.SessionID, "call-1", req)
			if err != nil {
				t.Fatalf("CollectAccess: %v", err)
			}
			if choice.Mode != "" {
				t.Fatalf("system-only choice mode = %q for Edited %q, want empty", choice.Mode, edited)
			}
		}
	})

	t.Run("denial selects no mode", func(t *testing.T) {
		_, req, _ := accessRequestFixture(t, session.AccessIntent{Editing: true, Reason: "edit a file"})
		bridge := NewPermissionBridge(&fakePermissionClient{decision: PermissionDecision{Approved: false, Edited: "auto"}})
		choice, err := bridge.CollectAccess(context.Background(), req.SessionID, "call-1", req)
		if err != nil {
			t.Fatalf("CollectAccess: %v", err)
		}
		if choice.Approved {
			t.Fatal("a denial was collected as approved")
		}
		if choice.Mode != "" {
			t.Fatalf("denied choice mode = %q, want empty", choice.Mode)
		}
	})
}

// A client that ignores modeAccess still receives a full textual disclosure.
func TestCollectAccessDisclosureSurvivesLegacyClient(t *testing.T) {
	_, req, _ := accessRequestFixture(t, session.AccessIntent{System: true, Reason: "read outside the workspace"})
	client := &fakePermissionClient{decision: PermissionDecision{Approved: true}}
	bridge := NewPermissionBridge(client)
	if _, err := bridge.CollectAccess(context.Background(), req.SessionID, "call-1", req); err != nil {
		t.Fatalf("CollectAccess: %v", err)
	}

	if client.gotReq.ToolName != "mode.request" {
		t.Fatalf("ToolName = %q, want mode.request", client.gotReq.ToolName)
	}
	for _, want := range []string{
		"read outside the workspace",
		"Current mode: default; system access: false.",
		"interaction mode stays unchanged",
		"This conversation only",
		"no sudo",
	} {
		if !strings.Contains(client.gotReq.Reason, want) {
			t.Fatalf("Reason %q does not disclose %q", client.gotReq.Reason, want)
		}
	}
}

// A foreign identity or a client error yields no applied decision and never
// touches the canonical channel.
func TestCollectAccessRejectsForeignIdentityAndClientError(t *testing.T) {
	t.Run("foreign session", func(t *testing.T) {
		_, req, decisions := accessRequestFixture(t, session.AccessIntent{System: true, Reason: "read outside"})
		client := &fakePermissionClient{decision: PermissionDecision{Approved: true}}
		bridge := NewPermissionBridge(client)
		if _, err := bridge.CollectAccess(context.Background(), "sess_other", "call-1", req); err == nil {
			t.Fatal("a foreign session id was accepted")
		}
		if client.calls != 0 {
			t.Fatalf("client.calls = %d, want 0 for a rejected identity", client.calls)
		}
		select {
		case d := <-decisions:
			t.Fatalf("a rejected identity acknowledged the tool: %+v", d)
		default:
		}
	})

	t.Run("empty request id", func(t *testing.T) {
		_, req, _ := accessRequestFixture(t, session.AccessIntent{System: true, Reason: "read outside"})
		req.RequestID = ""
		bridge := NewPermissionBridge(&fakePermissionClient{decision: PermissionDecision{Approved: true}})
		if _, err := bridge.CollectAccess(context.Background(), req.SessionID, "call-1", req); err == nil {
			t.Fatal("an empty request id was accepted")
		}
	})

	t.Run("client error", func(t *testing.T) {
		_, req, decisions := accessRequestFixture(t, session.AccessIntent{System: true, Reason: "read outside"})
		bridge := NewPermissionBridge(&fakePermissionClient{err: errors.New("transport down")})
		if _, err := bridge.CollectAccess(context.Background(), req.SessionID, "call-1", req); err == nil {
			t.Fatal("a client error was swallowed")
		}
		select {
		case d := <-decisions:
			t.Fatalf("a client error acknowledged the tool: %+v", d)
		default:
		}
	})

	t.Run("mismatched typed echo", func(t *testing.T) {
		_, req, decisions := accessRequestFixture(t, session.AccessIntent{System: true, Reason: "read outside"})
		bridge := NewPermissionBridge(&fakePermissionClient{decision: PermissionDecision{
			Approved:   true,
			ModeAccess: &ModeAccessChoice{RequestID: "other", SessionID: req.SessionID},
		}})
		if _, err := bridge.CollectAccess(context.Background(), req.SessionID, "call-1", req); err == nil {
			t.Fatal("a mismatched typed echo was accepted")
		}
		select {
		case d := <-decisions:
			t.Fatalf("a mismatched echo acknowledged the tool: %+v", d)
		default:
		}
	})
}

// A typed echo that misdescribes the presented tuple is ignored: the choice
// keeps the snapshot the server actually showed. Trusting the echo would let a
// client report a tuple that was never displayed, which the session reads as
// "the state moved" and answers with a refresh — re-asking a client that keeps
// misdescribing it, and never settling what the user decided.
func TestCollectAccessIgnoresMisdescribedEchoedTuple(t *testing.T) {
	_, req, decisions := accessRequestFixture(t, session.AccessIntent{
		System: true, Reason: "read outside the workspace",
	})
	bridge := NewPermissionBridge(&fakePermissionClient{decision: PermissionDecision{
		Approved: true,
		ModeAccess: &ModeAccessChoice{
			RequestID: req.RequestID, SessionID: req.SessionID,
			Current: ModeAccessState{Mode: "auto", System: true},
		},
	}})
	choice, err := bridge.CollectAccess(context.Background(), req.SessionID, "call-1", req)
	if err != nil {
		t.Fatalf("CollectAccess: %v", err)
	}
	if choice.Current != req.Current {
		t.Fatalf("choice current = %+v, want the presented %+v", choice.Current, req.Current)
	}
	if !choice.Approved {
		t.Fatal("the approval was dropped along with the echoed tuple")
	}
	select {
	case d := <-decisions:
		t.Fatalf("collection acknowledged the tool: %+v", d)
	default:
	}
}

// The ordinary Request path cannot acknowledge a typed access request, even
// when the pending call carries a usable response channel.
func TestPermissionBridgeRejectsTypedAccessDelivery(t *testing.T) {
	state, req, decisions := accessRequestFixture(t, session.AccessIntent{System: true, Reason: "read outside"})
	_ = req

	canonical := state.PendingApproval()
	if canonical == nil || canonical.Access == nil {
		t.Fatal("the canonical pending call carries no access metadata")
	}
	client := &fakePermissionClient{decision: PermissionDecision{Approved: true}}
	bridge := NewPermissionBridge(client)

	if _, err := bridge.Request(context.Background(), state.SessionID(), canonical); err == nil {
		t.Fatal("Request acknowledged a typed access request")
	}
	if client.calls != 0 {
		t.Fatalf("client.calls = %d, want 0: the request must be rejected before the client is asked", client.calls)
	}
	select {
	case d := <-decisions:
		t.Fatalf("Request delivered on the canonical channel: %+v", d)
	default:
	}
	if _, pending := state.AccessRequest(); !pending {
		t.Fatal("Request cleared the pending access request")
	}
}

// ---- Send now: canonical prompt delivery and the access apply race ----

// gatedPermissionClient is a PermissionClient whose calls park until the test
// releases them. With ignoreCtx it also ignores context cancellation, so a test
// can deliver a late answer after an interruption.
type gatedPermissionClient struct {
	mu        sync.Mutex
	ignoreCtx bool
	ctxs      []context.Context
	gates     []chan PermissionDecision
}

func (c *gatedPermissionClient) RequestPermission(ctx context.Context, req PermissionRequest) (PermissionDecision, error) {
	c.mu.Lock()
	gate := make(chan PermissionDecision, 1)
	c.ctxs = append(c.ctxs, ctx)
	c.gates = append(c.gates, gate)
	ignore := c.ignoreCtx
	c.mu.Unlock()
	if ignore {
		return <-gate, nil
	}
	select {
	case d := <-gate:
		return d, nil
	case <-ctx.Done():
		return PermissionDecision{}, ctx.Err()
	}
}

func (c *gatedPermissionClient) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.gates)
}

func (c *gatedPermissionClient) ctx(i int) context.Context {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ctxs[i]
}

func (c *gatedPermissionClient) release(i int, d PermissionDecision) {
	c.mu.Lock()
	gate := c.gates[i]
	c.mu.Unlock()
	gate <- d
}

// testSendNowCancelsApprovalRPC pins that an interrupted approval RPC ends only
// that request: the turn continues, and a late grant on the settled prompt
// cannot settle a newer one.
func testSendNowCancelsApprovalRPC(t *testing.T) {
	gated := &gatedPermissionClient{ignoreCtx: true}
	h := newSendNowHarnessWith(t, true, gated, &gatedQuestionClient{})

	firstCanceled := make(chan struct{})
	secondPrompt := make(chan struct{})
	secondAnswered := make(chan struct{})
	h.run = RunnerFunc(func(ctx context.Context, prompt string) error {
		turnID, err := h.state.BeginSteeringTurn(ctx)
		if err != nil {
			return err
		}
		defer h.state.EndSteeringTurn(turnID, "test complete")
		opCtx, ref, err := h.state.BeginSteeringOperation(ctx, turnID, session.OperationModel)
		if err != nil {
			return err
		}
		tc1 := &session.PendingToolCall{
			ID: "call-1", Name: "shell.run", Command: "ls",
			ResponseChan: make(chan session.UserApprovalDecision, 1),
		}
		key1, err := h.state.RegisterApproval(opCtx, tc1)
		if err != nil {
			return err
		}
		select {
		case d := <-tc1.ResponseChan:
			if d.Canceled {
				close(firstCanceled)
			}
		case <-ctx.Done():
			return ctx.Err()
		}
		h.state.ClearPrompt(key1)
		h.state.FinishSteeringOperation(ref, nil)

		// A newer, independent prompt under the turn itself.
		tc2 := &session.PendingToolCall{
			ID: "call-2", Name: "shell.run", Command: "pwd",
			ResponseChan: make(chan session.UserApprovalDecision, 1),
		}
		key2, err := h.state.RegisterApproval(ctx, tc2)
		if err != nil {
			return err
		}
		defer h.state.ClearPrompt(key2)
		close(secondPrompt)
		select {
		case <-tc2.ResponseChan:
			close(secondAnswered)
		case <-ctx.Done():
		}
		return ctx.Err()
	})
	done := h.start()
	waitFor(t, "the first approval RPC", func() bool { return gated.count() >= 1 })
	turnID := h.status().TurnID

	if _, err := h.steerNow(turnID, "r1", "stop"); err != nil {
		t.Fatalf("steerNow: %v", err)
	}
	waitClosed(t, firstCanceled, "the interrupted approval to settle")
	waitClosed(t, secondPrompt, "the newer prompt")

	// The interruption cancelled the prompt's RPC context but not the turn.
	if !h.manager.HasActiveTurn(h.session) {
		t.Fatal("a steering cancellation of an approval RPC cancelled the whole turn")
	}
	if err := gated.ctx(0).Err(); err == nil {
		t.Fatal("the outbound approval RPC context was not cancelled by the interruption")
	}
	// A late grant on the settled prompt cannot settle the newer one.
	gated.release(0, PermissionDecision{Approved: true})
	select {
	case <-secondAnswered:
		t.Fatal("a late grant settled a newer prompt")
	case <-time.After(100 * time.Millisecond):
	}
	gated.release(1, PermissionDecision{Approved: false})
	h.cancel()
	if err := h.awaitTurn(done); err != nil {
		t.Fatalf("PromptTurn: %v", err)
	}
}

// The access apply race has two sides, and both are decided by the one
// canonical boundary: a grant whose application already started completes and
// stays committed even when Send now is accepted during it, while a
// cancellation accepted first prevents the grant outright.
func TestACPSendNowAccessApplyRaceKeepsCommittedGrant(t *testing.T) {
	newManager := func(state *session.State) *TurnManager {
		return NewTurnManager(TurnManagerConfig{
			Lookup: func(sessionID string) (*TurnRuntime, bool) {
				return &TurnRuntime{
					SessionID:        sessionID,
					State:            state,
					SteeringSnapshot: state.SteeringSnapshot,
					SendNow:          state.SubmitSendNow,
				}, true
			},
			Notify: func(string, any) error { return nil },
		})
	}
	newState := func(t *testing.T) *session.State {
		t.Helper()
		state := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{SessionID: sendNowSessionID})
		state.SetOperationInterrupt(func(context.Context, session.OperationRef) (session.OperationStop, error) {
			return session.OperationStop{}, nil
		})
		return state
	}
	registerAccess := func(t *testing.T, state *session.State, turnID string) (session.AccessRequest, *session.PendingToolCall) {
		t.Helper()
		opCtx, _, err := state.BeginSteeringOperation(context.Background(), turnID, session.OperationModel)
		if err != nil {
			t.Fatalf("BeginSteeringOperation: %v", err)
		}
		call := &session.PendingToolCall{
			ID: "call-1", Name: "mode.request", Reason: "read outside",
			ResponseChan: make(chan session.UserApprovalDecision, 1),
		}
		req, waiting, err := state.RegisterAccess(opCtx, call, session.AccessIntent{System: true, Reason: "read outside"})
		if err != nil || !waiting {
			t.Fatalf("RegisterAccess = (%v, %v), want a published request", waiting, err)
		}
		return req, call
	}
	steer := func(manager *TurnManager, state *session.State, turnID string) (any, error) {
		return manager.Steer(context.Background(), json.RawMessage(fmt.Sprintf(
			`{"sessionId":%q,"delivery":"now","turnId":%q,"requestId":"r1","text":"stop"}`,
			state.SessionID(), turnID)))
	}

	t.Run("grant committed first stays committed", func(t *testing.T) {
		state := newState(t)
		turnID, err := state.BeginSteeringTurn(context.Background())
		if err != nil {
			t.Fatalf("BeginSteeringTurn: %v", err)
		}
		req, call := registerAccess(t, state, turnID)

		applying := make(chan struct{})
		release := make(chan struct{})
		resolved := make(chan session.AccessResult, 1)
		go func() {
			res, _ := state.ResolveAccess(session.AccessChoice{
				RequestID: req.RequestID, SessionID: req.SessionID,
				Current: req.Current, Approved: true,
			}, func(r session.AccessRequest, c session.AccessChoice) (session.AccessCommit, error) {
				target := r.Current
				target.System = true
				return session.AccessCommit{Effective: target, Apply: func() {
					close(applying)
					<-release
				}}, nil
			})
			resolved <- res
		}()
		<-applying

		manager := newManager(state)
		type outcome struct {
			receipt any
			err     error
		}
		steered := make(chan outcome, 1)
		go func() {
			r, err := steer(manager, state, turnID)
			steered <- outcome{receipt: r, err: err}
		}()
		// Send now serializes behind the access gate the commit still holds.
		select {
		case o := <-steered:
			t.Fatalf("send now completed while the grant held the access gate: %+v", o)
		case <-time.After(100 * time.Millisecond):
		}

		close(release)
		res := <-resolved
		if res.Status != session.AccessGranted {
			t.Fatalf("grant status = %q, want granted", res.Status)
		}
		select {
		case d := <-call.ResponseChan:
			if !d.Approved || d.Access == nil || d.Access.Status != session.AccessGranted {
				t.Fatalf("delivered decision = %+v, want the committed grant", d)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the committed grant was never acknowledged")
		}
		select {
		case o := <-steered:
			if o.err != nil {
				t.Fatalf("send now after the grant: %v", o.err)
			}
			w, ok := o.receipt.(SteeringReceiptWire)
			if !ok || w.Status != string(session.SteeringAccepted) {
				t.Fatalf("send now receipt = %#v, want accepted", o.receipt)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("send now never completed after the grant")
		}
		if !state.SystemAccess() {
			t.Fatal("the accepted send now rolled back the committed grant")
		}
		state.EndSteeringTurn(turnID, "done")
	})

	t.Run("steering accepted first prevents the grant", func(t *testing.T) {
		state := newState(t)
		turnID, err := state.BeginSteeringTurn(context.Background())
		if err != nil {
			t.Fatalf("BeginSteeringTurn: %v", err)
		}
		req, call := registerAccess(t, state, turnID)

		manager := newManager(state)
		if _, err := steer(manager, state, turnID); err != nil {
			t.Fatalf("send now: %v", err)
		}
		// The accepted cancellation reaches the canonical request: the tool is
		// answered canceled, never granted.
		select {
		case d := <-call.ResponseChan:
			if d.Approved || d.Access == nil || d.Access.Status != session.AccessCanceled {
				t.Fatalf("delivered decision = %+v, want a typed cancellation", d)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the interrupted access request was never settled")
		}
		// A late resolution attempt cannot grant what the cancellation already
		// settled.
		if res, err := state.ResolveAccess(session.AccessChoice{
			RequestID: req.RequestID, SessionID: req.SessionID,
			Current: req.Current, Approved: true,
		}, nil); err == nil {
			t.Fatalf("a late resolution granted after cancellation: %+v", res)
		}
		if state.SystemAccess() {
			t.Fatal("a cancelled access request granted system access")
		}
		state.EndSteeringTurn(turnID, "done")
	})
}
