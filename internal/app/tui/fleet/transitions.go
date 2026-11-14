package fleet

import (
	"marshal/internal/acpclient"
	"marshal/web/bridge/contract"
)

// transitionKind is one change the Events panel reports, and plan 6 notifies.
type transitionKind int

const (
	transitionNeedsYou transitionKind = iota // an agent entered groups.needsYou, or a submission entered the queue
	transitionReady                          // an agent entered groups.ready
	transitionFailed                         // an agent's status became error
)

// transition is one change between two states of the same epoch.
type transition struct {
	Kind    transitionKind
	AgentID string               // "" for a submission
	Name    string               // the agent's name, or the submission's title
	Pending contract.PendingKind // for transitionNeedsYou on an agent
}

// transitions compares two states of one epoch. A state from another epoch,
// or one that is not loaded, gives none (P5 decision 7): a fresh full read is
// not a batch of changes.
func transitions(prev, next acpclient.FleetState) []transition {
	if !prev.Loaded || !next.Loaded || prev.Epoch != next.Epoch {
		return nil
	}

	var out []transition

	neededBefore := idSet(prev.Inbox.Groups.NeedsYou)
	for _, id := range next.Inbox.Groups.NeedsYou {
		if neededBefore[id] {
			continue
		}
		out = append(out, transition{
			Kind:    transitionNeedsYou,
			AgentID: id,
			Name:    agentNameIn(next, id),
			Pending: pendingKindOf(next, id),
		})
	}

	queuedBefore := submissionIDs(prev.Inbox.Queue)
	for _, item := range next.Inbox.Queue {
		if item.Kind != contract.InboxItemIntake || item.Submission == nil {
			continue
		}
		if queuedBefore[item.Submission.ID] {
			continue
		}
		out = append(out, transition{
			Kind: transitionNeedsYou,
			Name: item.Submission.Title,
		})
	}

	readyBefore := idSet(prev.Inbox.Groups.Ready)
	for _, id := range next.Inbox.Groups.Ready {
		if readyBefore[id] {
			continue
		}
		out = append(out, transition{
			Kind:    transitionReady,
			AgentID: id,
			Name:    agentNameIn(next, id),
		})
	}

	failedBefore := errorIDs(prev.Agents)
	for _, a := range next.Agents {
		if a.Status != contract.AgentError || failedBefore[a.ID] {
			continue
		}
		out = append(out, transition{
			Kind:    transitionFailed,
			AgentID: a.ID,
			Name:    agentName(a),
		})
	}

	return out
}

// idSet is ids as a set.
func idSet(ids []string) map[string]bool {
	set := make(map[string]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	return set
}

// submissionIDs is the IDs of the intake submissions in queue.
func submissionIDs(queue []contract.InboxItem) map[string]bool {
	ids := map[string]bool{}
	for _, item := range queue {
		if item.Kind == contract.InboxItemIntake && item.Submission != nil {
			ids[item.Submission.ID] = true
		}
	}
	return ids
}

// errorIDs is the IDs of the agents whose status is error.
func errorIDs(agents []contract.AgentStatus) map[string]bool {
	ids := map[string]bool{}
	for _, a := range agents {
		if a.Status == contract.AgentError {
			ids[a.ID] = true
		}
	}
	return ids
}

// agentNameIn is the name of the agent with id in state, or the shortened ID
// when no row carries it.
func agentNameIn(state acpclient.FleetState, id string) string {
	for _, a := range state.Agents {
		if a.ID == id {
			return agentName(a)
		}
	}
	return shortID(id)
}

// pendingKindOf is what the agent with id is parked on, or "" when its row
// carries no pending request.
func pendingKindOf(state acpclient.FleetState, id string) contract.PendingKind {
	for _, a := range state.Agents {
		if a.ID != id {
			continue
		}
		if a.Pending == nil {
			return ""
		}
		return a.Pending.Kind
	}
	return ""
}
