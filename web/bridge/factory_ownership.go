package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

func factoryOwnsState(state string) bool {
	return state != "draft" && state != "closed" && state != "stopped"
}
func factoryItemOwnsAgent(i FactoryItem, agentID string) bool {
	for _, w := range i.Workers {
		if w.AgentID == agentID {
			return true
		}
	}
	for _, e := range i.Effects {
		if e.Worker != nil && e.Worker.AgentID == agentID {
			return true
		}
	}
	return false
}
func factoryItemOwnsBranch(i FactoryItem, repoID, branch string) bool {
	return i.RepoID != "" && i.RepoID == repoID && i.Branch != "" && i.Branch == branch
}
func (c *FactoryController) OwnsAgent(agentID string) (string, bool) {
	if c == nil || c.store == nil {
		return "", false
	}
	for _, i := range c.store.List() {
		if factoryItemOwnsAgent(i, agentID) {
			return i.ID, factoryOwnsState(i.State)
		}
	}
	return "", false
}
func (c *FactoryController) OwnsBranch(repoID, branch string) bool {
	if c == nil || c.store == nil {
		return false
	}
	for _, i := range c.store.List() {
		if factoryOwnsState(i.State) && factoryItemOwnsBranch(i, repoID, branch) {
			return true
		}
	}
	return false
}

// The token is an unexported context value; wire clients cannot supply it.
type factoryOwnershipKey struct{}

func factoryInternalContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, factoryOwnershipKey{}, true)
}
func (f *Fleet) checkFactoryAction(agentID, action string) error {
	return f.checkFactoryActionContext(context.Background(), agentID, action)
}
func (f *Fleet) checkFactoryActionContext(ctx context.Context, agentID, action string) error {
	if ctx.Value(factoryOwnershipKey{}) == true {
		return nil
	}
	if f.factory != nil {
		if id, active := f.factory.OwnsAgent(agentID); active {
			return fmt.Errorf("%w: agent %s belongs to factory item %s; %s is controlled by the factory", ErrFactoryConflict, agentID, id, action)
		}
	}
	if a, ok := f.ws.Agent(agentID); ok && a.FactoryItemID != "" {
		// A missing controller or unknown journal state must fail closed.
		if f.factory == nil {
			return fmt.Errorf("%w: factory ownership unavailable for agent %s", ErrFactoryConflict, agentID)
		}
		if _, active := f.factory.OwnsAgent(agentID); active {
			return fmt.Errorf("%w: factory worker %s", ErrFactoryConflict, agentID)
		}
	}
	return nil
}
func (f *Fleet) factoryOwnsBranch(repoID, branch string) bool {
	return f.factory != nil && f.factory.OwnsBranch(repoID, branch)
}

// Reattach only an exact persisted factory worker and only to an already
// running container. A native child lost with the bridge stays interrupted.
func (f *Fleet) reattachFactoryWorker(ctx context.Context, itemID string, w FactoryWorker) error {
	if w.Parked {
		return nil
	}
	a, ok := f.ws.Agent(w.AgentID)
	if !ok || a.FactoryItemID != itemID || a.FactoryRole != w.Role || a.FactorySpawnEffectID != w.SpawnEffectID || a.SessionID == "" || a.SessionID != w.SessionID {
		return ErrFactoryConflict
	}
	if _, err := f.runtimeForAgent(w.AgentID); err == nil {
		return nil
	}
	child, err := f.newRuntime(a)
	if err != nil || child == nil || !child.Containerized {
		return ErrFactoryUncertain
	}
	transport, ok := child.Transport.(*containerTransport)
	if !ok {
		return ErrFactoryUncertain
	}
	names, err := transport.listAgentContainers()
	if err != nil {
		return err
	}
	found := false
	for _, name := range names {
		if name == transport.cfg.Name {
			found = true
		}
	}
	if !found {
		return ErrFactoryUncertain
	}
	if err := f.slots.acquire(ctx); err != nil {
		return err
	}
	rt, err := f.startRuntime(ctx, a)
	if err != nil {
		f.releaseAgent(a.ID, false)
		return err
	}
	if err := f.restoreSession(ctx, rt, a); err != nil {
		f.releaseAgent(a.ID, false)
		return err
	}
	return nil
}

// ResumeFactoryWorkerFresh retains the owned checkout but creates a new ACP
// session after a recorded operator attestation. Its effect is journaled by
// the controller before this method is called.
func (f *Fleet) ResumeFactoryWorkerFresh(ctx context.Context, item FactoryItem, previous FactoryWorker) (FactoryWorker, error) {
	a, ok := f.ws.Agent(previous.AgentID)
	if !ok || a.FactoryItemID != item.ID || a.FactoryRole != previous.Role || a.FactorySpawnEffectID != previous.SpawnEffectID || a.SessionID != previous.SessionID || a.Project == "" || len(item.Approvals) == 0 {
		return FactoryWorker{}, ErrFactoryConflict
	}
	if _, err := f.runtimeForAgent(a.ID); err == nil {
		return FactoryWorker{}, ErrFactoryConflict
	}
	if f.git != nil {
		_, _, branch, _, err := newFactoryGit(f).state(ctx, a.Project)
		if err != nil || branch != item.Branch {
			return FactoryWorker{}, fmt.Errorf("%w: retained checkout branch unconfirmed", ErrFactoryConflict)
		}
	}
	child, err := f.newRuntime(a)
	if err != nil {
		return FactoryWorker{}, err
	}
	if child != nil && child.Containerized {
		transport, ok := child.Transport.(*containerTransport)
		if !ok {
			return FactoryWorker{}, ErrFactoryUncertain
		}
		names, err := transport.listAgentContainers()
		if err != nil {
			return FactoryWorker{}, err
		}
		for _, name := range names {
			if name == transport.cfg.Name {
				return FactoryWorker{}, fmt.Errorf("%w: previous container still running", ErrFactoryConflict)
			}
		}
	}
	if err := f.slots.acquire(ctx); err != nil {
		return FactoryWorker{}, err
	}
	rt, err := f.startRuntime(ctx, a)
	if err != nil {
		f.releaseAgent(a.ID, false)
		return FactoryWorker{}, err
	}
	cwd, err := rt.agentPath(a.Project)
	if err != nil {
		return FactoryWorker{}, err
	}
	conf := item.Approvals[len(item.Approvals)-1].Config
	mode, auto, route := "act", conf.ImplementerAuto, conf.ImplementerRouting
	if previous.Role == "reviewer" {
		mode, auto, route = "ask", false, conf.ReviewerRouting
	}
	params := map[string]any{"cwd": cwd, "mcpServers": []any{}, "name": "Factory recovery " + item.ID, "policy": map[string]any{"mode": mode, "auto": auto}}
	if len(route) > 0 && string(route) != "null" {
		params["routing"] = route
	}
	raw, err := rt.child.Request(ctx, "session/new", params)
	if err != nil {
		return FactoryWorker{}, err
	}
	var result struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(raw, &result); err != nil || strings.TrimSpace(result.SessionID) == "" || result.SessionID == previous.SessionID {
		return FactoryWorker{}, fmt.Errorf("%w: fresh session identity unconfirmed", ErrFactoryUncertain)
	}
	rt.reg.track(result.SessionID, cwd)
	a.SessionID = result.SessionID
	if err := f.ws.PutAgent(a); err != nil {
		return FactoryWorker{}, fmt.Errorf("%w: persist fresh worker session: %v", ErrFactoryUncertain, err)
	}
	f.mu.Lock()
	rt.sessionID = result.SessionID
	f.sessionAgent[result.SessionID] = a.ID
	delete(f.sessionAgent, previous.SessionID)
	f.mu.Unlock()
	previous.SessionID = result.SessionID
	return previous, nil
}
