package bridge

import (
	"net/http"
	"sort"

	"marshal/web/bridge/contract"
)

// DaemonInfo is contract.DaemonInfo; see that declaration.
type DaemonInfo = contract.DaemonInfo

// DaemonRefresh is contract.DaemonRefresh; see that declaration.
type DaemonRefresh = contract.DaemonRefresh

// DaemonChild is contract.DaemonChild; see that declaration.
type DaemonChild = contract.DaemonChild

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
