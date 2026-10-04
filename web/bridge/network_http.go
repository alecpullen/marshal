package bridge

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// netRow is one row of the hosts view.
type netRow struct {
	NetAgg
	// Rule says why the host is reachable or not, from the current
	// policy: allowlisted, granted, open, injected or blocked.
	Rule string `json:"rule"`
}

// ruleFor names the policy rule that covers host.
func ruleFor(p EgressAgentPolicy, host string) string {
	if _, ok := p.injectionFor(host); ok && allowed(p, host) {
		return "injected"
	}
	if !allowed(p, host) {
		return "blocked"
	}
	if p.Mode == EgressModeOpen {
		return "open"
	}
	for _, g := range p.Grants {
		if hostMatches(g, host) {
			return "granted"
		}
	}
	return "allowlisted"
}

// policyFor finds the policy to judge rows by: the named agent's, else
// any live agent of the workspace. ok is false when none is live.
func (f *Fleet) policyFor(workspace, agent string) (EgressAgentPolicy, bool) {
	h := f.egress
	if h == nil {
		return EgressAgentPolicy{}, false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if agent != "" {
		a, ok := h.agents[agent]
		if !ok {
			return EgressAgentPolicy{}, false
		}
		return a.policy, true
	}
	var best string
	for id, a := range h.agents {
		if workspace == "" || a.workspace == workspace {
			if best == "" || id < best {
				best = id
			}
		}
	}
	if best == "" {
		return EgressAgentPolicy{}, false
	}
	return h.agents[best].policy, true
}

// networkView serves GET /api/network?workspace=&agent=&view=.
func (s *Server) networkView(w http.ResponseWriter, r *http.Request) {
	if !s.requireFleet(w) {
		return
	}
	q := r.URL.Query()
	ws, agent := q.Get("workspace"), q.Get("agent")
	f := s.fleet
	switch q.Get("view") {
	case "", "hosts":
		rows := []netRow{}
		for _, a := range f.netlog.Hosts(ws, agent) {
			row := netRow{NetAgg: a}
			scopeWS := a.Workspace
			if agent != "" {
				scopeWS = ""
			}
			if pol, ok := f.policyFor(scopeWS, agent); ok {
				row.Rule = ruleFor(pol, a.Host)
			} else {
				row.Rule = ruleFromDecision(a)
			}
			rows = append(rows, row)
		}
		writeJSON(w, http.StatusOK, map[string]any{"processMode": f.egressProcessMode(), "rows": rows})
	case "requests":
		writeJSON(w, http.StatusOK, f.netlog.Requests(ws, agent))
	case "agents":
		writeJSON(w, http.StatusOK, f.netlog.Agents(ws))
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "view must be hosts, requests or agents"})
	}
}

// ruleFromDecision is the fallback when no live agent can answer: the
// aggregate's own last decision.
func ruleFromDecision(a NetAgg) string {
	switch {
	case a.Decision == "block":
		return "blocked"
	case a.Injected:
		return "injected"
	}
	return "allowed"
}

// AddToWorkspaceFunc applies the add-to-workspace decision for an agent's
// workspace template. A Studio template yields a changed draft; a repo
// template yields a patch for the user to apply.
type AddToWorkspaceFunc func(r *http.Request, a Agent, host string) (any, error)

var errAddToWorkspaceUnsupported = errors.New("add_to_workspace_unsupported")

// networkDecision serves POST /api/network/decisions.
func (s *Server) networkDecision(w http.ResponseWriter, r *http.Request) {
	if !s.requireFleet(w) {
		return
	}
	var body struct {
		AgentID  string `json:"agentId"`
		Host     string `json:"host"`
		Decision string `json:"decision"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	bad := func(msg string) { writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg}) }
	host := normalizeHost(body.Host)
	if host == "" || strings.ContainsAny(host, "/ \t\n:") || strings.HasPrefix(host, "*") {
		bad("host must be a bare hostname")
		return
	}
	f := s.fleet
	a, ok := f.ws.Agent(body.AgentID)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": ErrUnknownAgent.Error()})
		return
	}
	audit := func() {
		f.auditf(AuditEvent{Event: AuditNetworkDecision, OwnerID: DefaultOwnerID, AgentID: a.ID,
			Reason: body.Decision, Detail: host})
	}
	switch body.Decision {
	case "block":
		audit()
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	case "allow-agent":
		if f.egress == nil || !f.egress.grant(a.ID, host) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "agent is not behind the egress proxy"})
			return
		}
		audit()
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	case "add-to-workspace":
		if f.addToWorkspace == nil {
			writeJSON(w, http.StatusNotImplemented, map[string]string{"error": errAddToWorkspaceUnsupported.Error()})
			return
		}
		res, err := f.addToWorkspace(r, a, host)
		if err != nil {
			writeErr(w, fmt.Errorf("add to workspace: %w", err))
			return
		}
		audit()
		writeJSON(w, http.StatusOK, res)
	default:
		bad("decision must be block, allow-agent or add-to-workspace")
	}
}
