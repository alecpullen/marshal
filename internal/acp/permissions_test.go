package acp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
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
}

func (f *fakePermissionClient) RequestPermission(ctx context.Context, req PermissionRequest) (PermissionDecision, error) {
	f.calls++
	f.gotReq = req
	if f.err != nil {
		return PermissionDecision{}, f.err
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
