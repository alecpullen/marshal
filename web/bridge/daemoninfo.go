package bridge

import (
	"net/http"
	"sort"
)

// DaemonInfo is what GET /api/daemon answers about the local daemon.
type DaemonInfo struct {
	Version   string         `json:"version"`
	Profile   string         `json:"profile"`
	PID       int            `json:"pid"`
	StartedAt int64          `json:"startedAt"` // Unix milliseconds
	Socket    string         `json:"socket"`
	Projects  []string       `json:"projects"`          // filled per request
	Pinned    []string       `json:"pinned"`            // filled per request
	Children  []DaemonChild  `json:"children"`          // filled per request
	Refresh   *DaemonRefresh `json:"refresh,omitempty"` // filled per request
}

// DaemonRefresh is the refresh helper as GET /api/daemon reports it.
type DaemonRefresh struct {
	PID      int `json:"pid"`
	Restarts int `json:"restarts"`
}

// DaemonChild is one project child as GET /api/daemon reports it.
type DaemonChild struct {
	Root       string          `json:"root"`
	Version    string          `json:"version"`
	EnvWarning string          `json:"envWarning,omitempty"`
	Pinned     bool            `json:"pinned,omitempty"`
	Sessions   []DaemonSession `json:"sessions"`
}

// SetDaemonInfo makes GET /api/daemon answer info; without it the route
// answers 404 (decision 8).
func (s *Server) SetDaemonInfo(info DaemonInfo) {
	s.daemonInfo = &info
}

// SetRefreshHelper makes GET /api/daemon report h.
func (s *Server) SetRefreshHelper(h *Helper) {
	s.refresh = h
}

// daemon answers what this daemon is. The info is copied and its projects
// replaced with the workspace's current list, so a project added or
// removed since startup is reported without restarting the daemon. The
// children are current for the same reason, and so is the refresh helper.
func (s *Server) daemon(w http.ResponseWriter, r *http.Request) {
	if s.daemonInfo == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not a daemon"})
		return
	}
	out := *s.daemonInfo
	out.Projects = s.projectRoots()
	out.Pinned = s.pinnedRoots()
	out.Children = s.projectChildren()
	out.Refresh = nil
	if s.refresh != nil {
		pid, restarts := s.refresh.State()
		out.Refresh = &DaemonRefresh{PID: pid, Restarts: restarts}
	}
	writeJSON(w, http.StatusOK, out)
}

// projectRoots is the workspace's projects, sorted and never nil so the
// response encodes as [] rather than null.
func (s *Server) projectRoots() []string {
	out := []string{}
	if s.fleet != nil {
		out = append(out, s.fleet.ws.Projects()...)
	}
	sort.Strings(out)
	return out
}

// pinnedRoots is the workspace's pinned projects, sorted and never nil so the
// response encodes as [] rather than null.
func (s *Server) pinnedRoots() []string {
	if s.fleet == nil {
		return []string{}
	}
	return s.fleet.ws.Pinned()
}

// projectChildren is the fleet's project children, never nil so the
// response encodes as [] rather than null.
func (s *Server) projectChildren() []DaemonChild {
	if s.fleet == nil {
		return []DaemonChild{}
	}
	return s.fleet.projectChildren()
}

// projectChildren reports the live project children, sorted by root, each
// with its initialize version, why its environment fell back, and its tracked
// sessions (decisions 9 and 7).
func (f *Fleet) projectChildren() []DaemonChild {
	f.projMu.Lock()
	children := make([]*projectChild, 0, len(f.projects))
	for _, pc := range f.projects {
		children = append(children, pc)
	}
	f.projMu.Unlock()

	out := make([]DaemonChild, 0, len(children))
	for _, pc := range children {
		pc.mu.Lock()
		version, envWarning, reg := pc.version, pc.envWarning, pc.reg
		pc.mu.Unlock()
		child := DaemonChild{
			Root:       pc.root,
			Version:    version,
			EnvWarning: envWarning,
			Pinned:     f.pinned(pc.root),
			Sessions:   []DaemonSession{},
		}
		if reg != nil {
			child.Sessions = reg.summaries()
		}
		out = append(out, child)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Root < out[j].Root })
	return out
}
