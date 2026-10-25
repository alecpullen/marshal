// Package inproc is the in-process adapter for the client seam (spec C4).
//
// It implements acpclient.Host and acpclient.Session over the runtime the TUI
// already holds, so the TUI can reach the agent side through the interfaces
// before the pipe exists. P3 deletes this package once the TUI talks to a
// separate process.
//
// It must not import internal/app: TUI tests import this package, and app
// imports tui, so an import back into app would be a cycle.
package inproc

import (
	"sync"
	"sync/atomic"

	"marshal/internal/acpclient"
	"marshal/internal/acpwire"
	"marshal/internal/app/session"
	"marshal/internal/llm/schema"
)

// Deps is what the adapter serves. Each P2 plan adds its group's
// dependencies (C4).
type Deps struct {
	State *session.State
}

// Host is the in-process transport: it owns the current session and hands out
// a new one on a swap.
type Host struct {
	mu      sync.Mutex
	current *Session
}

// Session is one conversation over a session.State.
type Session struct {
	state  *session.State
	closed atomic.Bool
}

var (
	_ acpclient.Host    = (*Host)(nil)
	_ acpclient.Session = (*Session)(nil)
)

// New builds the host and the session over d.State.
func New(d Deps) (*Host, *Session) {
	sess := &Session{state: d.State}
	return &Host{current: sess}, sess
}

// Swap closes the current session and returns a new one over state. It serves
// today's swap path; plan 5 replaces it with Host.NewSession and Host.Resume.
func (h *Host) Swap(state *session.State) *Session {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.current != nil {
		h.current.closed.Store(true)
	}
	sess := &Session{state: state}
	h.current = sess
	return sess
}

// SharedFilesystem reports whether a path the agent names is also a path on
// this machine. The in-process adapter runs in the same process, so it does.
func (h *Host) SharedFilesystem() bool { return true }

// ID is the session's ID. A closed session keeps answering with its final
// value (C4), which holds because its state never changes.
func (s *Session) ID() string { return s.state.SessionID() }

// Status is the session's current status (C5). It reads the state one field at
// a time, as the TUI read them before, so the value is not one atomic
// snapshot. A closed session keeps answering with its final value (C4).
func (s *Session) Status() acpwire.SessionStatus {
	st := s.state
	used, window := st.TurnUsage()
	effort := st.EffortOverride()
	access := st.InteractionState()
	act := st.Activity()
	ws := st.Workspace()
	bi := st.BrowserInfo()

	status := acpwire.SessionStatus{
		WorkingDir:    st.WorkingDir,
		GenerationSeq: st.Generation().Seq,
		LeafID:        st.LeafID(),
		Branches:      st.Branches(),
		ActiveRoute:   routeOf(st.ActiveRoute()),
		TurnUsed:      used,
		TurnWindow:    window,
		Quota:         quotaOf(st.TurnQuota()),
		Effort: acpwire.Effort{
			Level:    effort.Level,
			Provider: effort.Provider,
			Model:    effort.Model,
		},
		Interaction: acpwire.Interaction{
			Mode:     string(access.Mode),
			Auto:     access.Auto,
			System:   access.System,
			Revision: access.Revision,
		},
		SudoUnlock: st.SudoUnlockEnabled(),
		Trusted:    st.Trusted(),
		HasBackup:  st.HasBackup(),
		Activity: acpwire.Activity{
			Kind:      string(act.Kind),
			Label:     act.Label,
			StartedAt: acpwire.Millis(act.StartedAt),
		},
		SubagentRunning: st.HasRunningSubagent(),
		Workspace: acpwire.Workspace{
			ProjectRoot:  ws.ProjectRoot,
			ActiveRoot:   ws.ActiveRoot,
			Branch:       ws.Branch,
			BaseSha:      ws.BaseSha,
			TargetBranch: ws.TargetBranch,
		},
		Browser: acpwire.Browser{
			Active:      bi.Active,
			ToolName:    bi.ToolName,
			URL:         bi.URL,
			Title:       bi.Title,
			Mode:        bi.Mode,
			SessionOpen: bi.SessionOpen,
			UpdatedAt:   acpwire.Millis(bi.UpdatedAt),
		},
	}
	if r, ok := st.TurnRoute(); ok {
		route := routeOf(r)
		status.TurnRoute = &route
	}
	return status
}

// routeOf copies a session route onto the wire.
func routeOf(r session.RouteInfo) acpwire.Route {
	return acpwire.Route{
		Role:             string(r.Role),
		Profile:          r.Profile,
		Preset:           r.Preset,
		Provider:         r.Provider,
		Model:            r.Model,
		LocalOnly:        r.LocalOnly,
		Thinking:         r.Thinking,
		ReasoningCapable: r.ReasoningCapable,
		Active:           r.Active,
	}
}

// quotaOf copies the provider's quota onto the wire, converting the seconds
// and minutes it reports into milliseconds. A nil quota stays nil.
func quotaOf(q *schema.QuotaInfo) *acpwire.Quota {
	if q == nil {
		return nil
	}
	return &acpwire.Quota{
		PlanType:              q.PlanType,
		PrimaryUsedPercent:    q.PrimaryUsedPercent,
		PrimaryResetAfterMs:   int64(q.PrimaryResetAfterSecs) * 1000,
		PrimaryWindowMs:       int64(q.PrimaryWindowMinutes) * 60000,
		SecondaryUsedPercent:  q.SecondaryUsedPercent,
		SecondaryResetAfterMs: int64(q.SecondaryResetAfterSecs) * 1000,
		SecondaryWindowMs:     int64(q.SecondaryWindowMinutes) * 60000,
	}
}
