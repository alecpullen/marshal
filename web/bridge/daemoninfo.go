package bridge

import (
	"net/http"
	"sort"
)

// DaemonInfo is what GET /api/daemon answers about the local daemon.
type DaemonInfo struct {
	Version   string        `json:"version"`
	Profile   string        `json:"profile"`
	PID       int           `json:"pid"`
	StartedAt int64         `json:"startedAt"` // Unix milliseconds
	Socket    string        `json:"socket"`
	Projects  []string      `json:"projects"` // filled per request
	Children  []DaemonChild `json:"children"` // filled per request
}

// DaemonChild is one project child as GET /api/daemon reports it.
type DaemonChild struct {
	Root     string          `json:"root"`
	Version  string          `json:"version"`
	Sessions []DaemonSession `json:"sessions"`
}

// SetDaemonInfo makes GET /api/daemon answer info; without it the route
// answers 404 (decision 8).
func (s *Server) SetDaemonInfo(info DaemonInfo) {
	s.daemonInfo = &info
}

// daemon answers what this daemon is. The info is copied and its projects
// replaced with the workspace's current list, so a project added or
// removed since startup is reported without restarting the daemon. The
// children are current for the same reason.
func (s *Server) daemon(w http.ResponseWriter, r *http.Request) {
	if s.daemonInfo == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not a daemon"})
		return
	}
	out := *s.daemonInfo
	out.Projects = s.projectRoots()
	out.Children = s.projectChildren()
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

// projectChildren is the fleet's project children, never nil so the
// response encodes as [] rather than null.
func (s *Server) projectChildren() []DaemonChild {
	if s.fleet == nil {
		return []DaemonChild{}
	}
	return s.fleet.projectChildren()
}

// projectChildren reports the live project children, sorted by root, each
// with its initialize version and its tracked sessions (decision 9).
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
		version, reg := pc.version, pc.reg
		pc.mu.Unlock()
		child := DaemonChild{Root: pc.root, Version: version, Sessions: []DaemonSession{}}
		if reg != nil {
			child.Sessions = reg.summaries()
		}
		out = append(out, child)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Root < out[j].Root })
	return out
}
