package bridge

import (
	"fmt"
	"net/http"
	"sort"

	"marshal/web/bridge/contract"
)

// daemonHostsPath is the tunnel helper's report: PUT, socket only.
const daemonHostsPath = "/api/daemon/hosts"

// DaemonHost is contract.DaemonHost; see that declaration.
type DaemonHost = contract.DaemonHost

// reportHosts takes the tunnel helper's report and replaces the last one
// (one-daemon D7). A report that names no host, or a state the contract does
// not define, changes nothing: the helper and the daemon are one program, so a
// refused report is a bug worth seeing rather than a fact to store.
func (s *Server) reportHosts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		w.Header().Set("Allow", http.MethodPut)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "the hosts report is a PUT"})
		return
	}
	var report contract.DaemonHostsReport
	if !decodeJSON(w, r, &report) {
		return
	}
	for i, h := range report.Hosts {
		if h.Name == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("host %d has no name", i)})
			return
		}
		switch h.State {
		case contract.HostConnecting, contract.HostLive, contract.HostDown:
		default:
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("host %q has an unknown state %q", h.Name, h.State)})
			return
		}
	}
	hosts := make([]DaemonHost, len(report.Hosts))
	copy(hosts, report.Hosts)
	sort.Slice(hosts, func(i, j int) bool { return hosts[i].Name < hosts[j].Name })
	s.hostsMu.Lock()
	s.hosts = hosts
	s.hostsMu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

// reportedHosts is the last report, sorted by name and never nil.
func (s *Server) reportedHosts() []DaemonHost {
	s.hostsMu.Lock()
	defer s.hostsMu.Unlock()
	if len(s.hosts) == 0 {
		return []DaemonHost{}
	}
	out := make([]DaemonHost, len(s.hosts))
	copy(out, s.hosts)
	return out
}
