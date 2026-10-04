package bridge

import (
	"context"
	"encoding/json"
	"time"
)

// gateRecord is an agent's latest verify result and when it ran.
type gateRecord struct {
	Result *gateResult `json:"result"`
	At     time.Time   `json:"at"`
}

// storeGate records a verify result and announces it on the fleet stream.
func (f *Fleet) storeGate(agentID string, res *gateResult) gateRecord {
	rec := gateRecord{Result: res, At: time.Now().UTC()}
	f.gatesMu.Lock()
	if f.gates == nil {
		f.gates = map[string]gateRecord{}
	}
	f.gates[agentID] = rec
	f.gatesMu.Unlock()
	if f.fleetLog != nil {
		_, _ = f.fleetLog.Append(fleetStreamKey, fleetDelta{Kind: "gate", SessionID: agentID, Gate: &rec})
	}
	return rec
}

// Gate returns the stored verify result for an agent, if one has run.
func (f *Fleet) Gate(agentID string) (gateRecord, bool) {
	f.gatesMu.Lock()
	defer f.gatesMu.Unlock()
	rec, ok := f.gates[agentID]
	return rec, ok
}

// RunGate runs the agent's verify gate now and stores the result.
func (f *Fleet) RunGate(ctx context.Context, id string) (gateRecord, error) {
	rt, err := f.RuntimeForSession(id)
	if err != nil {
		return gateRecord{}, err
	}
	res, err := f.verifySession(ctx, rt)
	if err != nil {
		return gateRecord{}, err
	}
	return f.storeGate(rt.id, res), nil
}

// agentCall sends one session-scoped request to an agent's child. A
// method-not-found reply becomes ErrUnsupported{feature}.
func (f *Fleet) agentCall(ctx context.Context, id, method, feature string, extra map[string]any) (json.RawMessage, error) {
	rt, err := f.RuntimeForSession(id)
	if err != nil {
		return nil, err
	}
	params := map[string]any{"sessionId": f.sessionIDFor(rt)}
	for k, v := range extra {
		params[k] = v
	}
	out, err := rt.child.Request(ctx, method, params)
	if isMethodNotFound(err) {
		return nil, ErrUnsupported{Feature: feature}
	}
	return out, err
}

// Files lists a directory under the agent's active root.
func (f *Fleet) Files(ctx context.Context, id, path string) (json.RawMessage, error) {
	return f.agentCall(ctx, id, "session/files", "files", map[string]any{"path": path})
}

// File reads one file under the agent's active root.
func (f *Fleet) File(ctx context.Context, id, path string) (json.RawMessage, error) {
	return f.agentCall(ctx, id, "session/file", "file", map[string]any{"path": path})
}

// CommitDraft asks the agent to draft a commit message without committing.
func (f *Fleet) CommitDraft(ctx context.Context, id string) (json.RawMessage, error) {
	return f.agentCall(ctx, id, "session/commit_draft", "commit_draft", nil)
}
