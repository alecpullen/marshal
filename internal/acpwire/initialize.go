package acpwire

import "encoding/json"

// MarshalProtocolVersion is the marshal-only protocol both ends declare in
// initialize (P3 R2).
const MarshalProtocolVersion = 1

// The protocol's own methods (P3 R2).
const (
	MethodProgress      = "$/progress"
	MethodCancelRequest = "$/cancel_request"
)

// MarshalCapability declares the marshal-only protocol in initialize.
type MarshalCapability struct {
	ProtocolVersion int `json:"protocolVersion"`
}

// ClientCapabilities is the part of initialize's clientCapabilities the
// marshal client sends.
type ClientCapabilities struct {
	Marshal *MarshalCapability `json:"marshal,omitempty"`
}

// InitializeParams is initialize's params as the marshal client sends them.
type InitializeParams struct {
	ProtocolVersion    int                 `json:"protocolVersion"`
	ClientCapabilities *ClientCapabilities `json:"clientCapabilities,omitempty"`
}

// AgentInfo names the agent in initialize's result. Version is empty for a
// build from source.
type AgentInfo struct {
	Name    string `json:"name"`
	Title   string `json:"title,omitempty"`
	Version string `json:"version"`
}

// AgentCapabilities is the part of initialize's agentCapabilities the
// marshal client reads.
type AgentCapabilities struct {
	Marshal *MarshalCapability `json:"marshal,omitempty"`
}

// InitializeResult is the part of initialize's result the marshal client
// reads.
type InitializeResult struct {
	ProtocolVersion   int               `json:"protocolVersion"`
	AgentInfo         AgentInfo         `json:"agentInfo"`
	AgentCapabilities AgentCapabilities `json:"agentCapabilities"`
}

// ProgressParams is $/progress's params: one progress value for the request
// whose id it names.
type ProgressParams struct {
	ID    json.RawMessage `json:"id"`
	Value json.RawMessage `json:"value"`
}

// CancelRequestParams is $/cancel_request's params.
type CancelRequestParams struct {
	ID json.RawMessage `json:"id"`
}
