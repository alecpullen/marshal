package acp

import (
	"context"
	"encoding/json"
	"testing"

	"marshal/internal/app/session"
	"marshal/internal/pubsub"
)

func TestHoldTogglesAndNotifies(t *testing.T) {
	s := newSyncStack(t)
	for _, on := range []bool{true, false} {
		got, err := s.Hold(context.Background(), json.RawMessage(`{"sessionId":"s1","on":`+map[bool]string{true: "true", false: "false"}[on]+`}`))
		if err != nil {
			t.Fatal(err)
		}
		if held := got.(map[string]any)["held"]; held != on {
			t.Fatalf("held = %v, want %v", held, on)
		}
		if s.st.Held() != on {
			t.Fatalf("State.Held = %v, want %v", s.st.Held(), on)
		}
	}
	var holds []bool
	for _, n := range s.notices() {
		u := n.params.Update
		if u["kind"] == "hold" {
			holds = append(holds, u["held"].(bool))
		}
	}
	if len(holds) != 2 || !holds[0] || holds[1] {
		t.Fatalf("hold updates = %v, want [true false]", holds)
	}
}

func TestHoldUnknownSession(t *testing.T) {
	s := newSyncStack(t)
	if _, err := s.Hold(context.Background(), json.RawMessage(`{"sessionId":"nope","on":true}`)); err == nil {
		t.Fatal("want error for unknown session")
	}
}

func TestHoldEventProjectsToUpdate(t *testing.T) {
	ev := pubsub.Event[session.Event]{Type: session.EventHoldChanged, Payload: session.Event{Held: true}}
	u, ok := eventToSessionUpdate(ev, &turnProjection{})
	if !ok || u["kind"] != "hold" || u["held"] != true {
		t.Fatalf("update = %v, %v", u, ok)
	}
}

