package bridge

import (
	"net/http"
	"sort"
)

// DaemonInfo is what GET /api/daemon answers about the local daemon.
type DaemonInfo struct {
	Version   string   `json:"version"`
	Profile   string   `json:"profile"`
	PID       int      `json:"pid"`
	StartedAt int64    `json:"startedAt"` // Unix milliseconds
	Socket    string   `json:"socket"`
	Projects  []string `json:"projects"` // filled per request
}

// SetDaemonInfo makes GET /api/daemon answer info; without it the route
// answers 404 (decision 8).
func (s *Server) SetDaemonInfo(info DaemonInfo) {
	s.daemonInfo = &info
}

// daemon answers what this daemon is. The info is copied and its projects
// replaced with the workspace's current list, so a project added or
// removed since startup is reported without restarting the daemon.
func (s *Server) daemon(w http.ResponseWriter, r *http.Request) {
	if s.daemonInfo == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not a daemon"})
		return
	}
	out := *s.daemonInfo
	out.Projects = s.projectRoots()
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
