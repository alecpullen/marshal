package bridge

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// This file owns the bridge's access-transport wire contracts. They are
// declared here rather than imported from the agent: web/ is an external ACP
// client, and the JSON contract — not a shared Go type — is what binds the two
// halves. The bridge forwards atomic patches and full committed state; it
// never validates policy or grants a capability locally.

// AccessState is the bridge-owned projection of a session's committed
// interaction tuple. Every field is always present on the wire: a client must
// be able to tell an explicit false from an omitted value, so no field carries
// omitempty.
type AccessState struct {
	Mode     string `json:"mode"`
	Auto     bool   `json:"auto"`
	System   bool   `json:"system_access"`
	Revision uint64 `json:"access_revision"`
}

// AccessPatch is one atomic access change. The fields are pointers so an
// omitted field stays distinguishable from an explicit false: "revoke system
// access" and "leave it alone" are different requests. UnmarshalJSON is the
// only decoder, and it is strict.
type AccessPatch struct {
	Mode   *string `json:"mode,omitempty"`
	Auto   *bool   `json:"auto,omitempty"`
	System *bool   `json:"system_access,omitempty"`
}

// UnmarshalJSON decodes a patch strictly: a null body, unknown keys, explicit
// nulls, wrong JSON types, an unknown mode spelling and an empty patch are all
// rejected rather than silently ignored, because a client that misspells a
// field must not be told its patch was applied. Presence is preserved for
// every known field.
func (p *AccessPatch) UnmarshalJSON(data []byte) error {
	if isWireNull(data) {
		return errors.New("access patch must be a JSON object, not null")
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("access patch must be a JSON object: %w", err)
	}
	*p = AccessPatch{}
	for key, val := range raw {
		switch key {
		case "mode":
			s, err := decodeWireString(key, val)
			if err != nil {
				return err
			}
			if !knownIngressMode(s) {
				return fmt.Errorf("unknown mode %q: want one of ask, act, plan, default, edit, copilot, auto", s)
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
	if p.Mode == nil && p.Auto == nil && p.System == nil {
		return errors.New("empty patch: set at least one of mode, auto, system_access")
	}
	return nil
}

// knownIngressMode reports whether raw is a mode spelling the agent accepts at
// ingress. The bridge validates the value's shape only; translating a legacy
// spelling into the canonical pair is the runtime's job, so the value is
// forwarded exactly as received.
func knownIngressMode(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "ask", "act", "plan", "default", "edit", "copilot", "auto":
		return true
	default:
		return false
	}
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

// decodeAccessState decodes the agent's committed access state. It is
// deliberately lenient about missing fields: the agent owns the state, and the
// bridge reports exactly what it committed rather than inventing defaults.
func decodeAccessState(raw json.RawMessage) (AccessState, error) {
	var st AccessState
	if err := json.Unmarshal(raw, &st); err != nil {
		return AccessState{}, fmt.Errorf("bridge: decode access state: %w", err)
	}
	return st, nil
}

// ModeAccessRequest is the typed presentation of one capability request,
// forwarded verbatim from the agent so a client can answer it without parsing
// the textual disclosure in Reason.
type ModeAccessRequest struct {
	RequestID      string      `json:"requestId"`
	SessionID      string      `json:"sessionId"`
	Actor          string      `json:"actor,omitempty"`
	StepID         int64       `json:"stepId,omitempty"`
	Reason         string      `json:"reason"`
	Current        AccessState `json:"current"`
	MissingEditing bool        `json:"missingEditing"`
	MissingSystem  bool        `json:"missingSystem"`
}

// ModeAccessChoice is the typed answer to a ModeAccessRequest. The outer
// Decision.Approved remains the decision; this carries identity, the presented
// tuple and the selected editing variant only. The bridge forwards it without
// judging it: the agent validates the identity and applies the grant.
type ModeAccessChoice struct {
	RequestID string      `json:"requestId"`
	SessionID string      `json:"sessionId"`
	Current   AccessState `json:"current"`
	Mode      string      `json:"mode,omitempty"`
}
