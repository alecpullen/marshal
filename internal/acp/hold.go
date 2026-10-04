package acp

import (
	"context"
	"encoding/json"
	"log/slog"
)

// HoldParams is the JSON-RPC body for session/hold.
type HoldParams struct {
	SessionID string `json:"sessionId"`
	On        bool   `json:"on"`
}

// Hold handles session/hold: it pauses (on) or resumes (off) the session's
// agent before its next tool or model call.
func (m *TurnManager) Hold(ctx context.Context, params json.RawMessage) (any, error) {
	var p HoldParams
	if err := decodeParams(params, &p, "session/hold"); err != nil {
		return nil, err
	}
	if p.SessionID == "" {
		return nil, invalidParamsError("session/hold requires sessionId")
	}
	rt, ok := m.lookup(p.SessionID)
	if !ok || rt.State == nil {
		return nil, serverErrorf("unknown session: %s", p.SessionID)
	}
	changed := rt.State.Held() != p.On
	rt.State.SetHold(p.On)
	// A live turn forwards EventHoldChanged itself; with no turn nobody is
	// subscribed, so the client is told directly.
	if changed && !m.turnActive(p.SessionID) {
		if err := m.notify("session/update", SessionUpdateParams{
			SessionID: p.SessionID,
			Update:    map[string]any{"kind": "hold", "held": p.On},
		}); err != nil {
			slog.Default().Warn("acp: hold notify failed", "session_id", p.SessionID, "error", err)
		}
	}
	return map[string]any{"held": rt.State.Held()}, nil
}
