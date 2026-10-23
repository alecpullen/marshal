package acp

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/interaction"
	"marshal/internal/pubsub"
)

// TestInteractionContractACPReportsSameCommittedTuple pins that the ACP
// session/set_mode response and the session's committed tuple agree, revision
// included, so a client never sees a different state from the one the agent
// will evaluate against.
func TestInteractionContractACPReportsSameCommittedTuple(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})
	broker := pubsub.NewBroker[session.Event]()
	state.SetEventBroker(broker)
	manager := NewTurnManager(TurnManagerConfig{
		Lookup: func(sessionID string) (*TurnRuntime, bool) {
			return &TurnRuntime{
				SessionID:        sessionID,
				State:            state,
				BeginWork:        identityBeginWork,
				Events:           broker,
				SetInteraction:   state.ApplyInteraction,
				InteractionState: func() (interaction.State, bool) { return state.InteractionState(), true },
			}, true
		},
		Notify: func(string, any) error { return nil },
	})

	got, err := manager.SetMode(context.Background(), json.RawMessage(`{"sessionId":"s1","mode":"ask"}`))
	if err != nil {
		t.Fatalf("SetMode: %v", err)
	}
	committed := state.InteractionState()
	if committed.Mode != interaction.ModeAsk || committed.Auto || committed.System {
		t.Fatalf("committed tuple = %+v, want ask/false/false", committed)
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	var resp InteractionState
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("decode response %s: %v", raw, err)
	}
	if resp.Mode != string(committed.Mode) || resp.Auto != committed.Auto ||
		resp.System != committed.System || resp.Revision != committed.Revision {
		t.Fatalf("response = %+v, want the committed %+v", resp, committed)
	}
}
