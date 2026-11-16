package contract

// WatchSample is one reading of a watch's condition.
type WatchSample struct {
	At      int64   `json:"at"`
	Value   float64 `json:"value"`
	Tripped bool    `json:"tripped"`
}

// RerouteRule names the role a tripped watch rebinds and the preset it binds
// it to.
type RerouteRule struct {
	Role   string `json:"role"`
	Preset string `json:"preset"`
}

// OnTrip is what a watch does when it trips. On a row it is what the bridge
// started it with; on a create request only `reroute` is sent.
type OnTrip struct {
	Notify  *bool        `json:"notify,omitempty"`
	Resume  *bool        `json:"resume,omitempty"`
	Reroute *RerouteRule `json:"reroute,omitempty"`
}

// WatchInfo is one watch as GET /api/watches answers it: what the engine
// reports about it, the agent that started it, and what it does on trip
// (web/bridge/watches.go, internal/acp/watch.go). Times are Unix
// milliseconds.
type WatchInfo struct {
	// AgentID is the agent whose session started the watch, or `studio` for
	// the bridge's own control session.
	AgentID     string        `json:"agentId"`
	ID          string        `json:"id"`
	Name        string        `json:"name"`
	Kind        string        `json:"kind"`
	State       string        `json:"state"`
	Condition   string        `json:"condition,omitempty"`
	Mode        string        `json:"mode"`
	IntervalMs  int64         `json:"intervalMs"`
	Owner       string        `json:"owner,omitempty"`
	FireCount   int           `json:"fireCount"`
	LastSample  string        `json:"lastSample,omitempty"`
	LastError   string        `json:"lastError,omitempty"`
	CreatedAt   int64         `json:"createdAt"`
	LastFiredAt int64         `json:"lastFiredAt,omitempty"`
	Samples     []WatchSample `json:"samples"`
	// OnTrip, Notify and Resume are held by the bridge, so they appear only
	// on watches it started.
	OnTrip *OnTrip `json:"onTrip,omitempty"`
	Notify *bool   `json:"notify,omitempty"`
	Resume *bool   `json:"resume,omitempty"`
}
