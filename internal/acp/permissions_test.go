package acp

import (
	"context"
	"encoding/json"
	"testing"
	"time"

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
	if _, err := bridge.Request(context.Background(), pending); err != nil {
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
	if _, err := bridge.Request(context.Background(), pending); err != nil {
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
	if _, err := bridge.Request(context.Background(), pending); err != nil {
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
			_, err := bridge.Request(ctx, pending)
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
