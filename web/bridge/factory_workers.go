package bridge

import (
	"context"
	"encoding/json"
	"errors"
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
	if role == "implementer" && len(item.PRs) > 0 && len(item.Rounds) > 0 && item.Rounds[len(item.Rounds)-1].HeadSHA != "" {
		ref = item.Rounds[len(item.Rounds)-1].HeadSHA
	}
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

func (w *factoryWorkers) Execute(ctx context.Context, item FactoryItem, effect FactoryEffect) (result FactoryOutcome, err error) {
	result = FactoryOutcome{EffectID: effect.ID}
	dispatched := false
	defer func() {
		if err != nil && !errors.Is(err, ErrFactoryRefused) && !errors.Is(err, ErrFactoryUncertain) && !errors.Is(err, ErrFactoryInvalidResult) && !errors.Is(err, ErrFactoryWorkerFailed) {
			if dispatched {
				err = fmt.Errorf("%w: %v", ErrFactoryUncertain, err)
			} else {
				err = fmt.Errorf("%w: %v", ErrFactoryRefused, err)
			}
		}
	}()
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
	oldFinal, _, _ := factoryFinal(before)
	dispatched = true
	turn, err := rt.reg.PromptResult(ctx, worker.SessionID, prompt)
	result.StopReason = turn.StopReason
	if err != nil {
		return result, err
	}
	if turn.StopReason != "end_turn" {
		return result, fmt.Errorf("%w: worker turn ended with %q", ErrFactoryWorkerFailed, turn.StopReason)
	}
	after, err := rt.reg.Stack(ctx, worker.SessionID, 0)
	if err != nil {
		return result, fmt.Errorf("factory: read post-turn stack: %w", err)
	}
	final, message, hasMessage := factoryFinal(after)
	if final == "" || final == oldFinal || !hasMessage || strings.TrimSpace(message) == "" {
		return result, fmt.Errorf("%w: no new final report in turn", ErrFactoryInvalidResult)
	}
	report, err := ParseFactoryReport(message, effect.ID, worker.Role, effect.ExpectedBase, effect.ExpectedHead)
	if err != nil {
		return result, fmt.Errorf("%w: %v", ErrFactoryInvalidResult, err)
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

func factoryFinal(raw json.RawMessage) (id, message string, hasMessage bool) {
	var tree struct {
		Nodes []struct {
			ID, Kind string
			Message  *struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"nodes"`
	}
	if json.Unmarshal(raw, &tree) != nil {
		return "", "", false
	}
	for i := len(tree.Nodes) - 1; i >= 0; i-- {
		if tree.Nodes[i].Kind == "final" {
			if tree.Nodes[i].Message == nil {
				return tree.Nodes[i].ID, "", false
			}
			return tree.Nodes[i].ID, tree.Nodes[i].Message.Content, true
		}
	}
	return "", "", false
}

// EndWork.Items is omitempty in the existing ACP wire contract: {} is a
// successful empty snapshot. A present items field must be an array; null or
// an unrelated object cannot establish idleness.
func decodeFactoryWork(raw json.RawMessage) (known, idle bool) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return false, false
	}
	if len(fields) == 0 {
		return true, true
	}
	items, ok := fields["items"]
	if !ok || len(fields) != 1 {
		return false, false
	}
	items = []byte(strings.TrimSpace(string(items)))
	if len(items) == 0 || items[0] != '[' {
		return false, false
	}
	var entries []json.RawMessage
	if json.Unmarshal(items, &entries) != nil {
		return false, false
	}
	return true, len(entries) == 0
}

func (w *factoryWorkers) Observe(ctx context.Context, item FactoryItem) (FactoryObservation, error) {
	obs := FactoryObservation{ObservedAt: time.Now().UTC(), BaseSHA: item.BaseSHA}
	for _, worker := range item.Workers {
		a, ok := w.fleet.ws.Agent(worker.AgentID)
		if !ok || a.FactoryItemID != item.ID || a.FactoryRole != worker.Role || a.FactorySpawnEffectID != worker.SpawnEffectID {
			continue
		}
		worker.SessionID = a.SessionID
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
			obs.ActiveWorkKnown, obs.WorkIdle = decodeFactoryWork(raw)
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
	known, idle := decodeFactoryWork(raw)
	if !known || !idle {
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

// stopFactoryRuntimeForRetry removes only the newly started compute after a
// saved-session load failure. The parked workspace and session record survive.
func (f *Fleet) stopFactoryRuntimeForRetry(rt *agentRuntime, a Agent) error {
	if rt == nil || rt.child == nil {
		return fmt.Errorf("factory: missing runtime to stop")
	}
	if err := rt.child.stopConfirmed(); err != nil {
		return err
	}
	if c, ok := rt.child.Transport.(*containerTransport); ok {
		names, err := c.listAgentContainers()
		if err != nil {
			return err
		}
		for _, name := range names {
			if name == c.cfg.Name {
				return fmt.Errorf("factory: resumed container still running")
			}
		}
	}
	f.closeTerminals(a.ID)
	rt.stopTimeout()
	if f.egress != nil {
		f.egress.remove(a.ID)
	}
	f.mu.Lock()
	if f.runtimes[a.ID] != rt {
		f.mu.Unlock()
		return fmt.Errorf("factory: runtime bookkeeping changed during failed resume")
	}
	delete(f.runtimes, a.ID)
	delete(f.sessionAgent, a.SessionID)
	f.mu.Unlock()
	f.slots.release()
	return nil
}
