package bridge

import (
	"context"
	"fmt"
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
