package session

import (
	"context"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/pubsub"
)

func childApprovalFixture(t *testing.T) (root, child *State, events <-chan pubsub.Event[Event]) {
	t.Helper()
	root = newTestState()
	rootBroker := pubsub.NewBroker[Event]()
	root.SetEventBroker(rootBroker)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	events = rootBroker.Subscribe(ctx)

	child = New(config.Default(), "/repo", time.Unix(100, 0), Persistence{}, WithDepth(1))
	child.SetEventBroker(pubsub.NewBroker[Event]())
	return root, child, events
}

func TestChildApprovalForwardingRetainsCanonicalOwner(t *testing.T) {
	root, child, events := childApprovalFixture(t)
	root.RegisterSubagent("child", child)

	pending := &PendingToolCall{ID: "c1", Name: "shell.run", ResponseChan: make(chan UserApprovalDecision, 1)}
	child.SetPendingApproval(pending)

	select {
	case ev := <-events:
		src := ev.Payload.ApprovalSource
		if src == nil {
			t.Fatalf("event = %+v, want an ApprovalSource", ev.Payload)
		}
		if src.Owner != child {
			t.Fatalf("owner = %p, want the child %p", src.Owner, child)
		}
		if src.Pending == nil || src.Pending.ID != "c1" {
			t.Fatalf("pending = %+v, want the child's approval", src.Pending)
		}
		if src.Actor != child.SessionID() {
			t.Fatalf("actor = %q, want %q", src.Actor, child.SessionID())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no forwarded approval event")
	}
}

func TestChildApprovalForwardingDoesNotRelayTranscript(t *testing.T) {
	root, child, events := childApprovalFixture(t)
	root.RegisterSubagent("child", child)

	child.AddMessage(RoleAssistant, "hello", ContentTypePlain)

	select {
	case ev := <-events:
		if ev.Payload.ApprovalSource == nil {
			t.Fatalf("root received a non-approval event: %+v", ev.Payload)
		}
	case <-time.After(150 * time.Millisecond):
		// Expected: transcript events are never relayed.
	}
}

func TestRegisterChildForwardsAlreadyPendingApprovalOnce(t *testing.T) {
	root, child, events := childApprovalFixture(t)

	pending := &PendingToolCall{ID: "c1", Name: "shell.run", ResponseChan: make(chan UserApprovalDecision, 1)}
	child.SetPendingApproval(pending)

	root.RegisterSubagent("child", child)

	select {
	case ev := <-events:
		if ev.Payload.ApprovalSource == nil {
			t.Fatalf("event = %+v, want an ApprovalSource", ev.Payload)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no forwarded already-pending approval")
	}
	select {
	case ev := <-events:
		t.Fatalf("duplicate forwarded event: %+v", ev.Payload)
	case <-time.After(150 * time.Millisecond):
	}
}

func TestChildApprovalForwardingNeverUsesParentPromptSlot(t *testing.T) {
	root, child, events := childApprovalFixture(t)
	root.RegisterSubagent("child", child)

	pending := &PendingToolCall{ID: "c1", Name: "shell.run", ResponseChan: make(chan UserApprovalDecision, 1)}
	child.SetPendingApproval(pending)

	select {
	case <-events:
	case <-time.After(2 * time.Second):
		t.Fatal("no forwarded approval event")
	}
	if root.PendingApproval() != nil {
		t.Fatal("the child's approval occupied the parent's pending slot")
	}
}
