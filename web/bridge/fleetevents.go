package bridge

import (
	"encoding/json"
	"sync"
	"time"

	"marshal/web/bridge/contract"
)

// fleetDelta is the contract's FleetDelta plus the bridge's two off-wire
// fields.
type fleetDelta struct {
	contract.FleetDelta
	// accessSession is the ACP session the access state belongs to; never
	// encoded.
	accessSession string
	// usage carries a telemetry update's usage rows to the ledger; never
	// encoded.
	usage json.RawMessage
}

// contextPct is how full the context pack is. The engine reports pack
// tokens against the pack limit rather than a percentage; an explicit
// usedPct, when an agent sends one, wins.
func contextPct(usedPct, used, max int) int {
	if usedPct > 0 || max <= 0 {
		return usedPct
	}
	return min(100, used*100/max)
}

func classifyNotification(method string, params json.RawMessage) (fleetDelta, bool) {
	if method != "session/update" {
		return fleetDelta{}, false
	}
	var p struct {
		SessionID string `json:"sessionId"`
		Update    struct {
			Kind     string `json:"kind"`
			ToolName string `json:"toolName"`
			// Mode, Auto, System and AccessRevision are the full committed
			// interaction tuple a mode_changed update carries. Every field is
			// decoded, including an explicit false, so a revocation is never
			// mistaken for an omission.
			Mode           string `json:"mode"`
			Auto           bool   `json:"auto"`
			System         bool   `json:"system_access"`
			AccessRevision uint64 `json:"access_revision"`
			// ChangedFiles holds objects ({path, added, removed}); only the
			// count is used here, so entries stay undecoded.
			ChangedFiles []json.RawMessage `json:"changedFiles"`
			ToolStats    json.RawMessage   `json:"toolStats"`
			Rules        json.RawMessage   `json:"rules"`
			Run          json.RawMessage   `json:"run"`
			Usage        json.RawMessage   `json:"usage"`
			Event        json.RawMessage   `json:"event"`
			Context      struct {
				UsedPct       int `json:"usedPct"`
				PackTokens    int `json:"packTokens"`
				PackMaxTokens int `json:"packMaxTokens"`
			} `json:"context"`
		} `json:"update"`
	}
	if err := json.Unmarshal(params, &p); err != nil || p.SessionID == "" {
		return fleetDelta{}, false
	}
	d := fleetDelta{FleetDelta: contract.FleetDelta{SessionID: p.SessionID}}
	switch p.Update.Kind {
	case "tool_call":
		d.Kind, d.Activity = "activity", p.Update.ToolName
	case "mode_changed":
		d.Kind = "mode"
		d.Access = &AccessState{
			Mode:     p.Update.Mode,
			Auto:     p.Update.Auto,
			System:   p.Update.System,
			Revision: p.Update.AccessRevision,
		}
		// The fleet row is keyed by agent id (attachClassifier rewrites
		// SessionID), but a revision is only comparable within the session
		// that issued it, so keep the identity the agent reported.
		d.accessSession = p.SessionID
	case "session_telemetry":
		pct := contextPct(p.Update.Context.UsedPct, p.Update.Context.PackTokens, p.Update.Context.PackMaxTokens)
		changed := len(p.Update.ChangedFiles)
		d.Kind, d.ChangedFiles, d.ContextPct = "telemetry", &changed, &pct
		d.ToolStats, d.Rules = p.Update.ToolStats, p.Update.Rules
		d.usage = p.Update.Usage
	case "watch":
		if len(p.Update.Event) == 0 {
			return fleetDelta{}, false
		}
		d.Kind, d.Watch = "watch", p.Update.Event
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

// PendingRequest is contract.PendingRequest; see that declaration.
type PendingRequest = contract.PendingRequest

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
	activity                 string
	contextPct, changedFiles int
	updatedAt                time.Time
	pending                  *observedPending
	// access is the session's last committed interaction state, and
	// accessSession the ACP session it belongs to. A nil access means the
	// bridge cannot vouch for the agent's privileges — it has not reported
	// them, or the read failed — and the snapshot says so rather than
	// inferring a grant from the project's defaults or the persisted
	// starting mode.
	access        *AccessState
	accessSession string
	// run is the latest run_progress payload, kept verbatim. runErr is
	// the final error of the last run the bridge started, if it failed.
	run    json.RawMessage
	runAt  time.Time
	runErr string
	// runGen counts the runs the bridge started on this agent, so a late
	// error from an earlier run cannot overwrite a newer run's state.
	runGen uint64
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
		a.applyAccess(d.accessSession, d.Access)
	case "telemetry":
		if d.ContextPct != nil {
			a.contextPct = *d.ContextPct
		}
		if d.ChangedFiles != nil {
			a.changedFiles = *d.ChangedFiles
		}
	case "run":
		a.run, a.runAt = d.Run, time.Now().UTC()
	}
	a.updatedAt = time.Now().UTC()
}

// applyAccess replaces the whole committed state atomically. A report from a
// replacement session starts its own revision sequence, so the comparison
// resets instead of treating its first report as stale; within one session an
// older revision is dropped, so a reordered notification cannot roll a
// revocation back.
func (a *agentLive) applyAccess(sessionID string, st *AccessState) {
	if st == nil {
		return
	}
	if a.accessSession == sessionID && a.access != nil && st.Revision < a.access.Revision {
		return
	}
	committed := *st
	a.access, a.accessSession = &committed, sessionID
}

// setAccess records the state the agent reported for a session, replacing
// whatever was cached. It is the reattach path: the bridge asks the agent what
// it actually holds instead of reconstructing privileges from the persisted
// starting mode.
func (s *liveState) setAccess(id, sessionID string, st AccessState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.agents[id]
	if a == nil {
		a = &agentLive{}
		s.agents[id] = a
	}
	// The reattach read can race a mode change that committed after the agent
	// answered. Never replace a newer committed revision with the older one it
	// reported, matching applyAccess.
	if a.accessSession == sessionID && a.access != nil && st.Revision < a.access.Revision {
		return
	}
	committed := st
	a.access, a.accessSession = &committed, sessionID
	a.updatedAt = time.Now().UTC()
}

// beginRun starts a new run generation: it clears the previous run's error
// and returns the token the run's final error must present to be recorded.
func (s *liveState) beginRun(id string) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.agents[id]
	if a == nil {
		a = &agentLive{}
		s.agents[id] = a
	}
	a.runGen++
	a.runErr = ""
	return a.runGen
}

// setRunErr records how a run the bridge started ended: the error text, or
// nothing when it finished cleanly. It is dropped when a newer run has
// begun since gen was issued.
func (s *liveState) setRunErr(id string, gen uint64, msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if a := s.agents[id]; a != nil && a.runGen == gen {
		a.runErr = msg
	}
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

// AgentStatus is contract.AgentStatus; see that declaration.
type AgentStatus = contract.AgentStatus
