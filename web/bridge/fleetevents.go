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
}

func classifyNotification(method string, params json.RawMessage) (fleetDelta, bool) {
	if method != "session/update" {
		return fleetDelta{}, false
	}
	var p struct {
		SessionID string `json:"sessionId"`
		Update    struct {
			Kind         string   `json:"kind"`
			ToolName     string   `json:"toolName"`
			Mode         string   `json:"mode"`
			ChangedFiles []string `json:"changedFiles"`
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
	default:
		return fleetDelta{}, false
	}
	return d, true
}

type agentLive struct {
	activity, mode           string
	contextPct, changedFiles int
	updatedAt                time.Time
}
type liveState struct {
	mu     sync.Mutex
	agents map[string]*agentLive
}

func newLiveState() *liveState { return &liveState{agents: make(map[string]*agentLive)} }
func (s *liveState) apply(d fleetDelta) {
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
	}
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
	ID           string    `json:"id"`
	Project      string    `json:"project"`
	Name         string    `json:"name,omitempty"`
	Mode         string    `json:"mode,omitempty"`
	Status       string    `json:"status"`
	Activity     string    `json:"activity,omitempty"`
	ContextPct   int       `json:"contextPct,omitempty"`
	ChangedFiles int       `json:"changedFiles,omitempty"`
	Interrupted  bool      `json:"interrupted,omitempty"`
	UpdatedAt    time.Time `json:"updatedAt"`
}
