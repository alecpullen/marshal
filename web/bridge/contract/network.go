package contract

// The network inspector's routes (web/bridge/network_http.go,
// web/bridge/egress_policy.go, web/bridge/egress_fleet.go). A blocked request
// becomes an ask in F7 decision 7: allow it for this agent, add the host to
// the agent's environment, or block it.

// NetHostRow is one host of GET /api/network?view=hosts, with the rule that
// covers it.
type NetHostRow struct {
	Workspace string `json:"workspace,omitempty"`
	AgentID   string `json:"agentId,omitempty"`
	Host      string `json:"host"`
	// Rule is why the host is reachable or not: allowlisted, granted, open,
	// injected or blocked.
	Rule      string `json:"rule"`
	Requests  int64  `json:"requests"`
	Blocked   int64  `json:"blocked"`
	BytesUp   int64  `json:"bytesUp"`
	BytesDown int64  `json:"bytesDown"`
	LastSeen  int64  `json:"lastSeen"` // Unix milliseconds
	Decision  string `json:"decision"` // allow or block
	Injected  bool   `json:"injected,omitempty"`
}

// NetHosts is that view's answer.
type NetHosts struct {
	ProcessMode bool         `json:"processMode"`
	Rows        []NetHostRow `json:"rows"`
	// EgressError says the proxy is down when it is set; the rows are the
	// last thing it logged.
	EgressError string `json:"egressError,omitempty"`
}

// NetRecord is one connection record, from view=requests.
type NetRecord struct {
	At         int64  `json:"at"` // Unix milliseconds
	AgentID    string `json:"agentId"`
	Workspace  string `json:"workspace,omitempty"`
	Host       string `json:"host"`
	Port       int    `json:"port"`
	Decision   string `json:"decision"` // allow or block
	Injected   bool   `json:"injected,omitempty"`
	BytesUp    int64  `json:"bytesUp"`
	BytesDown  int64  `json:"bytesDown"`
	DurationMs int64  `json:"durationMs"`
}

// NetAgentRow is one agent of view=agents.
type NetAgentRow struct {
	AgentID   string `json:"agentId"`
	Workspace string `json:"workspace,omitempty"`
	Hosts     int64  `json:"hosts"`
	Requests  int64  `json:"requests"`
	Blocked   int64  `json:"blocked"`
	BytesUp   int64  `json:"bytesUp"`
	BytesDown int64  `json:"bytesDown"`
	LastSeen  int64  `json:"lastSeen"` // Unix milliseconds
}

// NetPending is one blocked request still awaiting a decision, in the shape of
// the `network_block` delta (web/bridge/egress_fleet.go).
type NetPending struct {
	Kind      string `json:"kind"` // network_block
	SessionID string `json:"sessionId"`
	AgentID   string `json:"agentId"`
	Host      string `json:"host"`
	Workspace string `json:"workspace,omitempty"`
	At        int64  `json:"at"` // Unix milliseconds
}

// NetPendingList is GET /api/network/pending's answer.
type NetPendingList struct {
	Pending []NetPending `json:"pending"`
}

// NetDecisionKind is one answer to a blocked request.
type NetDecisionKind string

// The three answers: keep the block, allow this agent, or change the
// environment the agent runs in.
const (
	NetDecisionBlock          NetDecisionKind = "block"
	NetDecisionAllowAgent     NetDecisionKind = "allow-agent"
	NetDecisionAddToWorkspace NetDecisionKind = "add-to-workspace"
)

// NetDecision is POST /api/network/decisions' body.
type NetDecision struct {
	AgentID  string          `json:"agentId"`
	Host     string          `json:"host"`
	Decision NetDecisionKind `json:"decision"`
}

// NetDecisionResult is that route's answer. A Studio template's draft changed
// and there is nothing to show but the workspace's name; a repo template
// answers a patch to apply.
type NetDecisionResult struct {
	OK        bool   `json:"ok,omitempty"`
	Patch     string `json:"patch,omitempty"`
	Workspace string `json:"workspace,omitempty"`
	Source    string `json:"source,omitempty"`
}
