package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"marshal/internal/interaction"
)

// InteractionState is the ACP-owned wire projection of a session's committed
// interaction tuple. Every field is always present: a client must be able to
// tell an explicit false from an omitted value, so no field carries omitempty.
// The transport owns this contract — no wire field aliases an internal struct.
type InteractionState struct {
	Mode     string `json:"mode"`
	Auto     bool   `json:"auto"`
	System   bool   `json:"system_access"`
	Revision uint64 `json:"access_revision"`
}

// interactionStateFrom projects the canonical tuple onto the wire. It copies
// every field; the result never aliases the session's own state.
func interactionStateFrom(s interaction.State) InteractionState {
	return InteractionState{
		Mode:     string(s.Mode),
		Auto:     s.Auto,
		System:   s.System,
		Revision: s.Revision,
	}
}

// SetModeParams is the JSON-RPC body for session/set_mode. The three patch
// fields are pointers so an omitted field stays distinguishable from an
// explicit false — "revoke system access" and "leave it alone" are different
// requests. UnmarshalJSON is the only decoder, and it is strict.
type SetModeParams struct {
	SessionID string  `json:"sessionId"`
	Mode      *string `json:"mode,omitempty"`
	Auto      *bool   `json:"auto,omitempty"`
	System    *bool   `json:"system_access,omitempty"`
}

// UnmarshalJSON decodes the params strictly: unknown keys, explicit nulls and
// wrong JSON types are rejected rather than silently ignored, because a client
// that misspells a field must not be told its patch was applied. Presence is
// preserved for every known field.
func (p *SetModeParams) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("params must be an object: %w", err)
	}
	*p = SetModeParams{}
	for key, val := range raw {
		switch key {
		case "sessionId":
			s, err := decodeWireString(key, val)
			if err != nil {
				return err
			}
			p.SessionID = s
		case "mode":
			s, err := decodeWireString(key, val)
			if err != nil {
				return err
			}
			p.Mode = &s
		case "auto":
			b, err := decodeWireBool(key, val)
			if err != nil {
				return err
			}
			p.Auto = &b
		case "system_access":
			b, err := decodeWireBool(key, val)
			if err != nil {
				return err
			}
			p.System = &b
		default:
			return fmt.Errorf("unknown field %q", key)
		}
	}
	return nil
}

// isWireNull reports whether a raw JSON value is the literal null.
func isWireNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

// decodeWireString decodes one string field, rejecting null and non-strings.
func decodeWireString(field string, raw json.RawMessage) (string, error) {
	if isWireNull(raw) {
		return "", fmt.Errorf("%s must not be null", field)
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", fmt.Errorf("%s must be a string", field)
	}
	return s, nil
}

// decodeWireBool decodes one boolean field, rejecting null and non-booleans.
func decodeWireBool(field string, raw json.RawMessage) (bool, error) {
	if isWireNull(raw) {
		return false, fmt.Errorf("%s must not be null", field)
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err != nil {
		return false, fmt.Errorf("%s must be a boolean", field)
	}
	return b, nil
}

// ingressMode translates one client-supplied mode value into the canonical
// pair. The retired five-mode spelling is accepted at ingress only; the
// canonical Ask/Act pair is what the session commits and what every report
// carries. "auto" is the one legacy value that also implies Auto.
func ingressMode(raw string) (interaction.Mode, bool, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "ask", "plan", "default":
		return interaction.ModeAsk, false, nil
	case "act", "edit", "copilot":
		return interaction.ModeAct, false, nil
	case "auto":
		return interaction.ModeAct, true, nil
	default:
		return "", false, fmt.Errorf("invalid mode %q: want one of ask, act, plan, default, edit, copilot, auto", raw)
	}
}

// interactionPatch converts the decoded params into exactly one canonical
// patch. It is the only place legacy ingress is translated, and it validates
// the whole patch before any runtime is consulted: an empty patch, an unknown
// mode and the impossible Ask+Auto pair are all rejected here.
func (p SetModeParams) interactionPatch() (interaction.Patch, error) {
	var patch interaction.Patch
	if p.Mode != nil {
		mode, derivedAuto, err := ingressMode(*p.Mode)
		if err != nil {
			return interaction.Patch{}, err
		}
		patch.Mode = &mode
		if derivedAuto {
			on := true
			patch.Auto = &on
		}
	}
	// An explicit auto overrides whatever the legacy mode implied, in both
	// directions: "auto" with auto:false is Act without Auto.
	if p.Auto != nil {
		on := *p.Auto
		patch.Auto = &on
	}
	// system_access false is a real patch value (a revocation), not omission.
	if p.System != nil {
		on := *p.System
		patch.System = &on
	}
	if patch.Mode == nil && patch.Auto == nil && patch.System == nil {
		return interaction.Patch{}, fmt.Errorf("empty patch: set at least one of mode, auto, system_access")
	}
	if patch.Mode != nil && *patch.Mode == interaction.ModeAsk && patch.Auto != nil && *patch.Auto {
		return interaction.Patch{}, fmt.Errorf("Ask cannot enable Auto")
	}
	return patch, nil
}

// SetMode handles session/set_mode: it applies one validated interaction patch
// to the session's runtime and reports the full committed state, both as the
// result and as the mode_changed session/update every attached client sees.
//
// The transport makes no policy decision and calls no runner setter: the
// runtime validates and commits the patch, and this handler only projects the
// committed tuple onto the wire.
func (m *TurnManager) SetMode(ctx context.Context, params json.RawMessage) (any, error) {
	var p SetModeParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, invalidParamsError("session/set_mode: %v", err)
		}
	}
	if p.SessionID == "" {
		return nil, invalidParamsError("session/set_mode requires sessionId")
	}
	// Validate the whole patch before the lookup, so a rejected request has no
	// side effect at all.
	patch, err := p.interactionPatch()
	if err != nil {
		return nil, invalidParamsError("session/set_mode: %v", err)
	}
	rt, ok := m.lookup(p.SessionID)
	if !ok {
		return nil, serverErrorf("unknown session: %s", p.SessionID)
	}
	if rt.SetInteraction == nil || rt.InteractionState == nil {
		return nil, serverErrorf("session %s does not support interaction changes", p.SessionID)
	}
	committed, err := rt.SetInteraction(patch)
	if err != nil {
		return nil, serverErrorf("set mode: %v", err)
	}
	state := interactionStateFrom(committed)
	if err := m.notify("session/update", SessionUpdateParams{
		SessionID: p.SessionID,
		Update: map[string]any{
			"kind":            "mode_changed",
			"mode":            state.Mode,
			"auto":            state.Auto,
			"system_access":   state.System,
			"access_revision": state.Revision,
		},
	}); err != nil {
		return nil, err
	}
	return state, nil
}

// AccessState handles session/access_state: a read-only authoritative read of
// the session's committed interaction state. It is the bootstrap a client uses
// instead of inferring privileges from defaults or a transcript.
func (m *TurnManager) AccessState(ctx context.Context, params json.RawMessage) (any, error) {
	var p sessionIDParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, invalidParamsError("session/access_state: %v", err)
		}
	}
	if p.SessionID == "" {
		return nil, invalidParamsError("session/access_state requires sessionId")
	}
	rt, ok := m.lookup(p.SessionID)
	if !ok {
		return nil, serverErrorf("unknown session: %s", p.SessionID)
	}
	if rt.InteractionState == nil {
		return nil, serverErrorf("session %s does not report interaction state", p.SessionID)
	}
	st, ok := rt.InteractionState()
	if !ok {
		return nil, serverErrorf("session %s does not report interaction state", p.SessionID)
	}
	return interactionStateFrom(st), nil
}
