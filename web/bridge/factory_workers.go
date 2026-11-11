package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type factoryWorkers struct{ fleet *Fleet }

func newFactoryWorkers(fleet *Fleet) *factoryWorkers { return &factoryWorkers{fleet: fleet} }

func (f *Fleet) SpawnFactoryWorker(ctx context.Context, item FactoryItem, role, effectID string) (FactoryWorker, error) {
	if item.ID == "" || effectID == "" || (role != "implementer" && role != "reviewer") {
		return FactoryWorker{}, fmt.Errorf("factory: invalid worker identity")
	}
	if len(item.Approvals) == 0 {
		return FactoryWorker{}, fmt.Errorf("factory: missing approval")
	}
	conf := item.Approvals[len(item.Approvals)-1].Config
	if conf.Native && conf.Workspace != "" {
		return FactoryWorker{}, fmt.Errorf("factory: native and workspace cannot both be selected")
	}
	if conf.RepoID == "" || item.BaseSHA == "" {
		return FactoryWorker{}, fmt.Errorf("factory: repository and base SHA required")
	}
	ref := item.BaseSHA
	mode := "act"
	auto := conf.ImplementerAuto
	routing := conf.ImplementerRouting
	if role == "reviewer" {
		mode = "ask"
		auto = false
		routing = conf.ReviewerRouting
		if len(item.Rounds) == 0 || item.Rounds[len(item.Rounds)-1].HeadSHA == "" {
			return FactoryWorker{}, fmt.Errorf("factory: missing reviewer candidate head")
		}
		ref = item.Rounds[len(item.Rounds)-1].HeadSHA
	}
	opts := SpawnOptions{Name: "Factory " + role + " " + item.ID, Mode: mode, Auto: &auto, RepoID: conf.RepoID, Ref: ref, Routing: routing, Workspace: conf.Workspace}
	id, err := f.spawn(ctx, conf.ProjectRoot, opts, &factorySpawnTag{itemID: item.ID, role: role, effectID: effectID, native: conf.Native})
	if err != nil {
		return FactoryWorker{}, err
	}
	a, ok := f.ws.Agent(id)
	if !ok || a.SessionID == "" {
		return FactoryWorker{}, fmt.Errorf("factory: spawned worker has no saved session")
	}
	return FactoryWorker{AgentID: id, SessionID: a.SessionID, Role: role, SpawnEffectID: effectID}, nil
}

func (w *factoryWorkers) Execute(ctx context.Context, item FactoryItem, effect FactoryEffect) (FactoryOutcome, error) {
	result := FactoryOutcome{EffectID: effect.ID}
	if effect.Kind != "implement" && effect.Kind != "review" {
		return result, fmt.Errorf("factory: unsupported worker effect %s", effect.Kind)
	}
	if effect.Worker == nil {
		return result, fmt.Errorf("factory: effect has no worker")
	}
	worker := *effect.Worker
	if worker.Role != map[string]string{"implement": "implementer", "review": "reviewer"}[effect.Kind] {
		return result, fmt.Errorf("factory: worker role mismatch")
	}
	a, ok := w.fleet.ws.Agent(worker.AgentID)
	if !ok || a.FactoryItemID != item.ID || a.FactoryRole != worker.Role || a.FactorySpawnEffectID != worker.SpawnEffectID || a.FactoryParked {
		return result, fmt.Errorf("factory: worker ownership or state mismatch")
	}
	rt, err := w.fleet.runtimeForAgent(worker.AgentID)
	if err != nil {
		return result, err
	}
	if worker.SessionID == "" || worker.SessionID != w.fleet.sessionIDFor(rt) {
		return result, fmt.Errorf("factory: worker session mismatch")
	}
	prompt, err := BuildFactoryPrompt(item, effect)
	if err != nil {
		return result, err
	}
	before, err := rt.reg.Stack(ctx, worker.SessionID, 0)
	if err != nil {
		return result, fmt.Errorf("factory: read pre-turn stack: %w", err)
	}
	oldFinal := factoryFinalID(before)
	turn, err := rt.reg.PromptResult(ctx, worker.SessionID, prompt)
	result.StopReason = turn.StopReason
	if err != nil {
		return result, err
	}
	if turn.StopReason != "end_turn" {
		return result, fmt.Errorf("factory: worker turn ended with %q", turn.StopReason)
	}
	after, err := rt.reg.Stack(ctx, worker.SessionID, 0)
	if err != nil {
		return result, fmt.Errorf("factory: read post-turn stack: %w", err)
	}
	final := factoryFinalID(after)
	if final == "" || final == oldFinal {
		return result, fmt.Errorf("factory: no new final report in turn")
	}
	report, err := ParseFactoryReport(lastFinalMessage(after), effect.ID, worker.Role, effect.ExpectedBase, effect.ExpectedHead)
	if err != nil {
		return result, err
	}
	result.Report = &report
	result.Worker = &worker
	if report.Outcome == "needs_input" {
		result.Confirmed = true
		return result, nil
	}
	obs, err := w.Observe(ctx, item)
	if err != nil {
		return result, err
	}
	if obs.Dirty && worker.Role == "reviewer" {
		return result, fmt.Errorf("factory: reviewer checkout changed")
	}
	result.HeadSHA, result.TreeSHA, result.BaseSHA = obs.HeadSHA, obs.TreeSHA, item.BaseSHA
	if obs.HeadSHA == "" || obs.TreeSHA == "" {
		return result, fmt.Errorf("factory: checkout Git state unavailable")
	}
	if worker.Role == "reviewer" && obs.HeadSHA != effect.ExpectedHead {
		return result, fmt.Errorf("factory: reviewer checkout head changed")
	}
	result.Confirmed = true
	return result, nil
}

func factoryFinalID(raw json.RawMessage) string {
	var tree struct {
		Nodes []struct{ ID, Kind string } `json:"nodes"`
	}
	if json.Unmarshal(raw, &tree) != nil {
		return ""
	}
	for i := len(tree.Nodes) - 1; i >= 0; i-- {
		if tree.Nodes[i].Kind == "final" {
			return tree.Nodes[i].ID
		}
	}
	return ""
}

func (w *factoryWorkers) Observe(ctx context.Context, item FactoryItem) (FactoryObservation, error) {
	obs := FactoryObservation{ObservedAt: time.Now().UTC(), BaseSHA: item.BaseSHA}
	for _, worker := range item.Workers {
		a, ok := w.fleet.ws.Agent(worker.AgentID)
		if !ok || a.FactoryItemID != item.ID {
			continue
		}
		worker.Parked = a.FactoryParked
		obs.Workers = append(obs.Workers, worker)
		if worker.Parked {
			continue
		}
		rt, err := w.fleet.runtimeForAgent(worker.AgentID)
		if err != nil {
			continue
		}
		obs.RuntimeKnown = true
		if info, ok := rt.reg.lookup(worker.SessionID); ok {
			obs.TurnIdle = !info.Busy && rt.reg.Pending(worker.SessionID) == ""
		}
		if raw, err := rt.child.Request(ctx, "session/end_work", map[string]any{"sessionId": worker.SessionID}); err == nil {
			var work struct {
				Items []json.RawMessage `json:"items"`
			}
			if len(raw) > 0 && string(raw) != "null" && json.Unmarshal(raw, &work) == nil {
				obs.ActiveWorkKnown = true
				obs.WorkIdle = len(work.Items) == 0
			}
		}
		if w.fleet.git != nil {
			out, err := w.fleet.git.runCtx(ctx, a.Project, Credential{}, "rev-parse", "HEAD")
			if err == nil {
				obs.HeadSHA = strings.TrimSpace(string(out))
			}
			out, err = w.fleet.git.runCtx(ctx, a.Project, Credential{}, "rev-parse", "HEAD^{tree}")
			if err == nil {
				obs.TreeSHA = strings.TrimSpace(string(out))
			}
			out, err = w.fleet.git.runCtx(ctx, a.Project, Credential{}, "status", "--porcelain")
			if err == nil {
				obs.Dirty = strings.TrimSpace(string(out)) != ""
			}
			if a.SourceKind == "git" {
				if repo, ok := w.fleet.ws.Repo(item.RepoID); ok {
					if cred, e := w.fleet.creds.Resolve(ctx, DefaultOwnerID, repo.CredRef); e == nil {
						obs.RemoteHead, _ = newFactoryGit(w.fleet).remoteHead(ctx, a.Project, item.Branch, cred)
					}
				}
			}
		}
	}
	return obs, nil
}

func (f *Fleet) ParkFactoryWorker(ctx context.Context, worker FactoryWorker) error {
	a, ok := f.ws.Agent(worker.AgentID)
	if !ok || a.FactoryItemID == "" || a.FactoryRole != worker.Role || a.FactorySpawnEffectID != worker.SpawnEffectID || a.SessionID == "" || a.SessionID != worker.SessionID || a.FactoryParked {
		return fmt.Errorf("factory: worker ownership or saved session unconfirmed")
	}
	rt, err := f.runtimeForAgent(worker.AgentID)
	if err != nil || rt.child == nil {
		return fmt.Errorf("factory: runtime unavailable for parking: %v", err)
	}
	if f.sessionIDFor(rt) != worker.SessionID {
		return fmt.Errorf("factory: live session mismatch")
	}
	info, ok := rt.reg.lookup(worker.SessionID)
	if !ok || info.Busy || rt.reg.Pending(worker.SessionID) != "" {
		return fmt.Errorf("factory: turn is not confirmed idle")
	}
	raw, err := rt.child.Request(ctx, "session/end_work", map[string]any{"sessionId": worker.SessionID})
	if err != nil {
		return fmt.Errorf("factory: active work unknown: %w", err)
	}
	var work struct {
		Items []json.RawMessage `json:"items"`
	}
	if len(raw) == 0 || string(raw) == "null" || json.Unmarshal(raw, &work) != nil || len(work.Items) > 0 {
		return fmt.Errorf("factory: active work is unknown or non-idle")
	}
	if err := rt.child.stopConfirmed(); err != nil {
		return fmt.Errorf("factory: runtime stop unconfirmed: %w", err)
	}
	if c, ok := rt.child.Transport.(*containerTransport); ok {
		names, err := c.listAgentContainers()
		if err != nil {
			return fmt.Errorf("factory: container exit unconfirmed: %w", err)
		}
		for _, n := range names {
			if n == c.cfg.Name {
				return fmt.Errorf("factory: container still running")
			}
		}
	}
	a.FactoryParked = true
	if err := f.ws.PutAgent(a); err != nil {
		return fmt.Errorf("factory: persist parked worker: %w", err)
	}
	f.closeTerminals(worker.AgentID)
	rt.stopTimeout()
	f.mu.Lock()
	if f.runtimes[worker.AgentID] == rt {
		delete(f.runtimes, worker.AgentID)
		delete(f.sessionAgent, worker.SessionID)
	}
	f.mu.Unlock()
	f.slots.release()
	return nil
}
