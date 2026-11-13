package contract

import "encoding/json"

// DeltaKind is the discriminator of a fleet-stream delta.
type DeltaKind string

// The fleet stream's delta kinds.
const (
	DeltaActivity       DeltaKind = "activity"
	DeltaMode           DeltaKind = "mode"
	DeltaTelemetry      DeltaKind = "telemetry"
	DeltaWatch          DeltaKind = "watch"
	DeltaRun            DeltaKind = "run"
	DeltaPending        DeltaKind = "pending"
	DeltaGate           DeltaKind = "gate"
	DeltaHold           DeltaKind = "hold"
	DeltaBudget         DeltaKind = "budget"
	DeltaProjectRemoved DeltaKind = "project_removed"
)

// FleetDelta is one entry on the fleet stream.
type FleetDelta struct {
	Kind         DeltaKind       `json:"kind"`
	SessionID    string          `json:"sessionId"`
	Activity     string          `json:"activity,omitempty"`
	Access       *AccessState    `json:"access,omitempty"`
	ContextPct   *int            `json:"contextPct,omitempty"`
	ChangedFiles *int            `json:"changedFiles,omitempty"`
	PendingKind  PendingKind     `json:"pendingKind,omitempty"`
	Gate         *GateRecord     `json:"gate,omitempty"`
	AgentID      string          `json:"agentId,omitempty"`
	Run          json.RawMessage `json:"run,omitempty"`
	Watch        json.RawMessage `json:"event,omitempty"`
	At           int64           `json:"at,omitempty"`
	ToolStats    json.RawMessage `json:"toolStats,omitempty"`
	Rules        json.RawMessage `json:"rules,omitempty"`
	Held         *bool           `json:"held,omitempty"`
	By           string          `json:"by,omitempty"`
}

// BudgetScope is which cap a budget delta reports on.
type BudgetScope string

// The budget scopes.
const (
	BudgetScopeDaily BudgetScope = "daily"
	BudgetScopeAgent BudgetScope = "agent"
)

// BudgetDelta is a "budget" fleet delta. Its fields are flat, as the spec
// lists them.
type BudgetDelta struct {
	Kind      DeltaKind   `json:"kind"`
	SessionID string      `json:"sessionId"`
	Scope     BudgetScope `json:"scope"`
	AgentID   string      `json:"agentId,omitempty"`
	SpentUSD  float64     `json:"spentUsd"`
	CapUSD    float64     `json:"capUsd"`
	Action    string      `json:"action"`
}

// ProjectRemovedDelta is a "project_removed" fleet delta.
type ProjectRemovedDelta struct {
	Kind    DeltaKind `json:"kind"`
	Project string    `json:"project"`
}
