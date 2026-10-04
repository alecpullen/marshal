package bridge

import (
	"encoding/json"
	"sync"
	"time"
)

type fleetDelta struct {
	Kind         string `json:"kind"`
	SessionID    string `json:"sessionId"`
	Activity     string `json:"activity,omitempty"`
	Mode         string `json:"mode,omitempty"`
	ContextPct   int    `json:"contextPct,omitempty"`
	ChangedFiles int    `json:"changedFiles,omitempty"`
	// PendingKind is "approval" or "question" on a "pending" delta. The
	// payload itself is not streamed — the dashboard refetches the
	// snapshot, which is the authority on what is still outstanding.
	PendingKind string `json:"pendingKind,omitempty"`
	// Gate is the new verify result on a "gate" delta.
	Gate *gateRecord `json:"gate,omitempty"`
	// AgentID names the agent on run, watch, budget and reroute deltas.
	// SessionID carries the same agent id for older deltas.
	AgentID string `json:"agentId,omitempty"`
	// Run is the latest run detail on a "run" delta, verbatim.
	Run json.RawMessage `json:"run,omitempty"`
	// Budget is the payload of a "budget" delta.
	Budget *budgetDelta `json:"budget,omitempty"`
	// Usage carries a telemetry update's new usage rows to the ledger. It
	// is not streamed: the fleet SSE only needs the telemetry digest.
	Usage json.RawMessage `json:"-"`
}

func classifyNotification(method string, params json.RawMessage) (fleetDelta, bool) {
	if method != "session/update" {
		return fleetDelta{}, false
	}
	var p struct {
		SessionID string `json:"sessionId"`
		Update    struct {
			Kind         string          `json:"kind"`
			ToolName     string          `json:"toolName"`
			Mode         string          `json:"mode"`
			ChangedFiles []string        `json:"changedFiles"`
			Run          json.RawMessage `json:"run"`
			Usage        json.RawMessage `json:"usage"`
			Context      struct {
				UsedPct int `json:"usedPct"`
			} `json:"context"`
		} `json:"update"`
	}
	if err := json.Unmarshal(params, &p); err != nil || p.SessionID == "" {
		return fleetDelta{}, false
	}
	d := fleetDelta{SessionID: p.SessionID}
	switch p.Update.Kind {
	case "tool_call":
		d.Kind, d.Activity = "activity", p.Update.ToolName
	case "mode_changed":
		d.Kind, d.Mode = "mode", p.Update.Mode
	case "session_telemetry":
		d.Kind, d.ChangedFiles, d.ContextPct = "telemetry", len(p.Update.ChangedFiles), p.Update.Context.UsedPct
		d.Usage = p.Update.Usage
	case "run_progress":
		if len(p.Update.Run) == 0 {
			return fleetDelta{}, false
		}
		d.Kind, d.Run = "run", p.Update.Run
	default:
		return fleetDelta{}, false
	}
	return d, true
}

// PendingRequest is the outstanding approval or question for an agent,
// carried in the fleet snapshot so the dashboard can render a decision
// inline instead of making the user open the chat.
type PendingRequest struct {
	// Kind is "approval" or "question", matching Registry.Pending.
	Kind string `json:"kind"`
	// ID is the toolCallId or questionId to POST the resolution to.
	ID string `json:"id"`
	// Params is the original request payload, verbatim.
	Params json.RawMessage `json:"params,omitempty"`
}

// observedPending is what the classifier saw on the wire. It is a cache,
// not a source of truth: Registry.Pending decides whether anything is
// actually outstanding, so a resolved request stops being advertised even
// though its payload is still cached here.
type observedPending struct {
	kind   string
	id     string
	params json.RawMessage
}

type agentLive struct {
	activity, mode           string
	contextPct, changedFiles int
	updatedAt                time.Time
	pending                  *observedPending
	// run is the latest run_progress payload, kept verbatim. runErr is
	// the final error of the last run the bridge started, if it failed.
	run    json.RawMessage
	runAt  time.Time
	runErr string
}
type liveState struct {
	mu     sync.Mutex
	agents map[string]*agentLive
}

func newLiveState() *liveState { return &liveState{agents: make(map[string]*agentLive)} }
func (s *liveState) apply(d fleetDelta) {
	if d.Kind == "gate" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.agents[d.SessionID]
	if a == nil {
		a = &agentLive{}
		s.agents[d.SessionID] = a
	}
	switch d.Kind {
	case "activity":
		a.activity = d.Activity
	case "mode":
		a.mode = d.Mode
	case "telemetry":
		a.contextPct, a.changedFiles = d.ContextPct, d.ChangedFiles
	case "run":
		a.run, a.runAt = d.Run, time.Now().UTC()
	}
	a.updatedAt = time.Now().UTC()
}

// setRunErr records how the last run the bridge started ended: empty for
// a clean finish, the error text otherwise.
func (s *liveState) setRunErr(id, msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.agents[id]
	if a == nil {
		a = &agentLive{}
		s.agents[id] = a
	}
	a.runErr = msg
}

// classifyRegistryEvent maps a bridge-originated registry event (from
// Registry.emitEvent) to an observed pending request. Permission and
// question requests arrive as child-initiated REQUESTS, not
// notifications, so they never reach classifyNotification — this is the
// only path by which the fleet learns about them.
func classifyRegistryEvent(payload any) (observedPending, bool) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return observedPending{}, false
	}
	var p struct {
		Type       string          `json:"type"`
		ToolCallID string          `json:"toolCallId"`
		QuestionID string          `json:"questionId"`
		Params     json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return observedPending{}, false
	}
	switch p.Type {
	case "permission_request":
		if p.ToolCallID == "" {
			return observedPending{}, false
		}
		return observedPending{kind: "approval", id: p.ToolCallID, params: p.Params}, true
	case "question_request":
		if p.QuestionID == "" {
			return observedPending{}, false
		}
		return observedPending{kind: "question", id: p.QuestionID, params: p.Params}, true
	default:
		return observedPending{}, false
	}
}

// observePending records the pending request seen for a session.
func (s *liveState) observePending(sessionID string, p observedPending) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.agents[sessionID]
	if a == nil {
		a = &agentLive{}
		s.agents[sessionID] = a
	}
	a.pending = &p
	a.updatedAt = time.Now().UTC()
}

func (s *liveState) remove(id string) {
	s.mu.Lock()
	delete(s.agents, id)
	s.mu.Unlock()
}

func (s *liveState) removeProject(ids []string) {
	s.mu.Lock()
	for _, id := range ids {
		delete(s.agents, id)
	}
	s.mu.Unlock()
}

func (s *liveState) get(id string) agentLive {
	s.mu.Lock()
	defer s.mu.Unlock()
	if a := s.agents[id]; a != nil {
		return *a
	}
	return agentLive{}
}

type AgentStatus struct {
	ID      string `json:"id"`
	Project string `json:"project"`
	Name    string `json:"name,omitempty"`
	// OwnerID, Origin and ClientID mirror Agent.OwnerID, Agent.Origin and
	// Agent.ClientID in workspace.go.
	OwnerID      string    `json:"ownerId,omitempty"`
	Origin       string    `json:"origin,omitempty"`
	ClientID     string    `json:"clientId,omitempty"`
	Mode         string    `json:"mode,omitempty"`
	Status       string    `json:"status"`
	Activity     string    `json:"activity,omitempty"`
	ContextPct   int       `json:"contextPct,omitempty"`
	ChangedFiles int       `json:"changedFiles,omitempty"`
	Interrupted  bool      `json:"interrupted,omitempty"`
	Isolated     bool      `json:"isolated,omitempty"`
	Branch       string    `json:"branch,omitempty"`
	UpdatedAt    time.Time `json:"updatedAt"`
	// SourceKind is "local" or "git"; the exit panel routes on it and
	// ReadOnly to decide between merge, push, and patch.
	SourceKind string `json:"sourceKind,omitempty"`
	// ReadOnly marks an arbitrary-URL clone with no push path.
	ReadOnly bool `json:"readOnly,omitempty"`
	// TargetBranch is the repo's default branch at spawn time.
	TargetBranch string `json:"targetBranch,omitempty"`
	// PRUrl is the validated pull-request URL from the last push.
	PRUrl string `json:"prUrl,omitempty"`
	// PushedAt is when the agent's branch last reached the remote.
	PushedAt *time.Time `json:"pushedAt,omitempty"`
	// GateOverride, when non-nil, records a pushed-despite-failure decision.
	GateOverride *GateOverride `json:"gateOverride,omitempty"`
	// Pending is set only while the agent is genuinely parked on an
	// approval or question, so the dashboard can resolve it in place.
	Pending *PendingRequest `json:"pending,omitempty"`
}
