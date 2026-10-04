package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Run kinds accepted by StartRun.
const (
	RunSDD   = "sdd"
	RunSwarm = "swarm"
)

// errInvalidRun marks a run request the bridge refuses before it reaches
// an agent. The HTTP layer maps it to 400.
var errInvalidRun = errors.New("bridge: invalid run request")

// runDispatchGrace is how long a run call is watched for an early
// failure. The ACP run methods block until the run ends, so the bridge
// cannot wait for them; but "a turn is already active" or "no such plan"
// come back at once, and surfacing those beats a silent 202.
var runDispatchGrace = 750 * time.Millisecond

// RunRequest asks an agent to execute a plan or a swarm goal.
type RunRequest struct {
	Kind string
	// Plan is markdown the bridge writes into the workspace (sdd).
	Plan string
	// PlanPath names a plan already on disk, as the bridge sees it (sdd).
	PlanPath string
	// Goal is the swarm's objective.
	Goal string
}

// StartRun starts an SDD or swarm run on an existing agent.
//
// An SDD plan goes to a file rather than over the wire because
// session/sdd_start hands it to pipeline.ParsePlan, which reads markdown
// from disk; that also keeps the bridge out of plan semantics — it writes
// bytes. The run itself continues after StartRun returns; its final error
// is recorded on the agent's live state.
func (f *Fleet) StartRun(ctx context.Context, agentID string, req RunRequest) error {
	return f.startRun(ctx, agentID, req, newAgentID())
}

// startRun is StartRun with the plan file's name chosen by the caller, so
// intake keeps naming plans after their pending submission.
func (f *Fleet) startRun(ctx context.Context, agentID string, req RunRequest, planID string) error {
	a, ok := f.ws.Agent(agentID)
	if !ok {
		return fmt.Errorf("%w: agent %s", ErrUnknownAgent, agentID)
	}
	var method string
	switch req.Kind {
	case RunSDD:
		if req.Plan == "" && req.PlanPath == "" {
			return fmt.Errorf("%w: sdd needs plan or planPath", errInvalidRun)
		}
		method = "session/sdd_start"
	case RunSwarm:
		if strings.TrimSpace(req.Goal) == "" {
			return fmt.Errorf("%w: swarm needs a goal", errInvalidRun)
		}
		method = "session/swarm_start"
	default:
		return fmt.Errorf("%w: kind must be %q or %q", errInvalidRun, RunSDD, RunSwarm)
	}
	rt, err := f.runtimeForAgent(agentID)
	if err != nil {
		return err
	}
	params := map[string]any{"sessionId": f.sessionIDFor(rt)}
	if req.Kind == RunSwarm {
		params["goal"] = req.Goal
	} else {
		path := req.PlanPath
		if req.Plan != "" {
			path = planPathFor(a.Project, planID)
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				return fmt.Errorf("create intake dir: %w", err)
			}
			if err := os.WriteFile(path, []byte(req.Plan), 0o600); err != nil {
				return fmt.Errorf("write plan: %w", err)
			}
		}
		// path is a bridge-side location. Translating it — rather than
		// rebuilding the agent's view by hand — keeps knowledge of what
		// /work means in exactly one place.
		inAgent, perr := rt.agentPath(path)
		if perr != nil {
			return fmt.Errorf("resolve plan path for agent %s: %w", agentID, perr)
		}
		params["planPath"] = string(inAgent)
	}
	f.live.setRunErr(agentID, "")
	_, err = f.dispatchRun(ctx, rt, agentID, method, params)
	return err
}

// dispatchRun sends a run method that blocks until the run ends. The call
// continues in the background on a context detached from ctx's
// cancellation; an error that arrives within runDispatchGrace is returned
// to the caller, and any final error is recorded on the agent.
func (f *Fleet) dispatchRun(ctx context.Context, rt *agentRuntime, agentID, method string, params map[string]any) (json.RawMessage, error) {
	bg := context.WithoutCancel(ctx)
	type outcome struct {
		raw json.RawMessage
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		raw, err := rt.child.Request(bg, method, params)
		if err != nil {
			f.live.setRunErr(agentID, err.Error())
		}
		done <- outcome{raw, err}
	}()
	select {
	case o := <-done:
		return o.raw, o.err
	case <-time.After(runDispatchGrace):
		return nil, nil
	}
}

type runListEntry struct {
	AgentID string          `json:"agentId"`
	Name    string          `json:"name,omitempty"`
	Project string          `json:"project"`
	Run     json.RawMessage `json:"run"`
	At      time.Time       `json:"at"`
	Error   string          `json:"error,omitempty"`
}

// listRuns reports every agent that has produced a run digest, newest
// first. The digest is the last run_progress the agent sent, so the Runs
// page lists runs without calling every agent.
func (f *Fleet) listRuns() []runListEntry {
	out := make([]runListEntry, 0)
	for _, a := range f.ws.Agents() {
		live := f.live.get(a.ID)
		if live.run == nil {
			continue
		}
		out = append(out, runListEntry{
			AgentID: a.ID, Name: a.Name, Project: a.Project,
			Run: live.run, At: live.runAt, Error: live.runErr,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out
}

func (s *Server) listRuns(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.fleet.listRuns())
}

func (s *Server) getRun(w http.ResponseWriter, r *http.Request) {
	reg, _, sid, err := s.registryForSession(r.PathValue("agentId"))
	if err != nil {
		writeErr(w, err)
		return
	}
	raw, err := reg.Run(r.Context(), sid)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeRaw(w, raw)
}

// startRun starts a run, spawning an agent for it when none is named.
func (s *Server) startRun(w http.ResponseWriter, r *http.Request) {
	var body struct {
		AgentID  string `json:"agentId"`
		Project  string `json:"project"`
		Kind     string `json:"kind"`
		Plan     string `json:"plan"`
		PlanPath string `json:"planPath"`
		Goal     string `json:"goal"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	req := RunRequest{Kind: body.Kind, Plan: body.Plan, PlanPath: body.PlanPath, Goal: body.Goal}
	// Validate before spawning so a bad request never leaves an idle agent.
	if err := validateRunRequest(req); err != nil {
		writeErr(w, err)
		return
	}
	if err := s.fleet.budgetGate(body.AgentID); err != nil {
		writeErr(w, err)
		return
	}
	agentID := body.AgentID
	if agentID == "" {
		if err := ValidateProjectRoot(body.Project); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		id, err := s.fleet.Spawn(r.Context(), body.Project, SpawnOptions{
			Name: runName(req), Origin: OriginUI, Isolated: true,
		})
		if id == "" {
			writeErr(w, err)
			return
		}
		agentID = id
	} else if _, err := s.fleet.RuntimeForSession(agentID); err != nil {
		writeErr(w, err)
		return
	}
	if err := s.fleet.StartRun(r.Context(), agentID, req); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"agentId": agentID})
}

func validateRunRequest(req RunRequest) error {
	switch req.Kind {
	case RunSDD:
		if req.Plan == "" && req.PlanPath == "" {
			return fmt.Errorf("%w: sdd needs plan or planPath", errInvalidRun)
		}
	case RunSwarm:
		if strings.TrimSpace(req.Goal) == "" {
			return fmt.Errorf("%w: swarm needs a goal", errInvalidRun)
		}
	default:
		return fmt.Errorf("%w: kind must be %q or %q", errInvalidRun, RunSDD, RunSwarm)
	}
	return nil
}

// runName is the display name of an agent spawned for a run.
func runName(req RunRequest) string {
	if req.Kind == RunSwarm {
		return "Swarm: " + truncateRunes(strings.TrimSpace(req.Goal), 60)
	}
	return "SDD run"
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// answerRun resolves a pending human gate. The resumed run blocks like
// the original, so it is dispatched in the background; an immediate
// refusal (no gate pending) still reaches the caller.
func (s *Server) answerRun(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Answer string `json:"answer"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	id := r.PathValue("agentId")
	if err := s.fleet.budgetGate(id); err != nil {
		writeErr(w, err)
		return
	}
	rt, err := s.fleet.RuntimeForSession(id)
	if err != nil {
		writeErr(w, err)
		return
	}
	_, err = s.fleet.dispatchRun(r.Context(), rt, rt.id, "session/sdd_answer", map[string]any{
		"sessionId": s.fleet.sessionIDFor(rt), "answer": body.Answer,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"agentId": rt.id})
}
