package fleetclient

import (
	"encoding/json"

	"marshal/web/bridge/contract"
)

// overflowType is the nudge the bridge sends when a client's position fell out
// of the event ring: every snapshot must be reread.
const overflowType = "replay_overflow"

// refetchKind is what applying one delta asks the client to reread.
type refetchKind int

const (
	refetchNone refetchKind = iota
	refetchAgents
	refetchBudgets
	// refetchEverything is the overflow: everything the connect read.
	refetchEverything
)

// outcome is what one delta did.
type outcome struct {
	changed bool
	refetch refetchKind
}

// deltaProbe is every payload's first decode. A delta names its kind; the
// overflow nudge names its type instead.
type deltaProbe struct {
	Kind string `json:"kind"`
	Type string `json:"type"`
}

// applyDelta applies one stream payload to the state. It reports whether the
// state changed and what must be reread; a payload it cannot decode changes
// nothing.
//
// The kind is read first, because the bridge's deltas do not share one shape:
// budget, project_removed and inbox have their own, and everything else decodes
// as a FleetDelta.
func (c *Client) applyDelta(payload []byte) outcome {
	var probe deltaProbe
	if err := json.Unmarshal(payload, &probe); err != nil {
		return outcome{}
	}
	if probe.Type == overflowType {
		return outcome{refetch: refetchEverything}
	}

	switch contract.DeltaKind(probe.Kind) {
	case contract.DeltaBudget:
		var d contract.BudgetDelta
		if json.Unmarshal(payload, &d) != nil {
			return outcome{}
		}
		return outcome{refetch: refetchBudgets}
	case contract.DeltaProjectRemoved:
		var d contract.ProjectRemovedDelta
		if json.Unmarshal(payload, &d) != nil {
			return outcome{}
		}
		return c.applyProjectRemoved(d)
	case contract.DeltaInbox:
		var d contract.InboxDelta
		if json.Unmarshal(payload, &d) != nil {
			return outcome{}
		}
		return c.applyInbox(d.Inbox)
	}

	var d contract.FleetDelta
	if json.Unmarshal(payload, &d) != nil {
		return outcome{}
	}
	return c.applyFleetDelta(d)
}

// applyFleetDelta applies one FleetDelta.
func (c *Client) applyFleetDelta(d contract.FleetDelta) outcome {
	switch d.Kind {
	case contract.DeltaGate:
		return c.applyGate(d)
	case contract.DeltaPending:
		return c.applyPending(d)
	case contract.DeltaActivity, contract.DeltaMode, contract.DeltaTelemetry,
		contract.DeltaRun, contract.DeltaHold:
		return c.applyRowDelta(d)
	}
	// watch, reroute, automation, network_block and every unknown kind change
	// nothing on this screen.
	return outcome{}
}

// applyGate records the latest verify result for an agent. Gates is a map, so
// the record lands even before the agent's row does.
func (c *Client) applyGate(d contract.FleetDelta) outcome {
	if d.Gate == nil {
		return outcome{}
	}
	record := *d.Gate
	c.mu.Lock()
	c.state.Gates[d.SessionID] = record
	c.mu.Unlock()
	return outcome{changed: true}
}

// applyPending moves the row on to the status its pending kind names, and asks
// for the agents to be reread: the snapshot is the authority on what is still
// outstanding. That refetch is immediate, every time.
func (c *Client) applyPending(d contract.FleetDelta) outcome {
	out := outcome{refetch: refetchAgents}
	switch d.PendingKind {
	case contract.PendingApproval:
		out.changed = c.setRowStatus(d.SessionID, contract.AgentAwaitingApproval)
	case contract.PendingQuestion:
		out.changed = c.setRowStatus(d.SessionID, contract.AgentAwaitingQuestion)
	}
	return out
}

// setRowStatus sets one row's status.
func (c *Client) setRowStatus(id string, status contract.AgentState) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	row := c.rowLocked(id)
	if row == nil {
		return false
	}
	row.Status = status
	return true
}

// applyRowDelta applies the kinds that only touch one agent's row.
func (c *Client) applyRowDelta(d contract.FleetDelta) outcome {
	c.mu.Lock()
	defer c.mu.Unlock()

	row := c.rowLocked(d.SessionID)
	if row == nil {
		return outcome{}
	}

	switch d.Kind {
	case contract.DeltaActivity:
		row.Activity = d.Activity
		row.UpdatedAt = c.now()
		return outcome{changed: true}

	case contract.DeltaTelemetry:
		if d.ContextPct != nil {
			row.ContextPct = *d.ContextPct
		}
		if d.ChangedFiles != nil {
			row.ChangedFiles = *d.ChangedFiles
		}
		row.UpdatedAt = c.now()
		return outcome{changed: true}

	case contract.DeltaMode:
		// The access revision rule: a stale report leaves the row exactly as
		// it was, so a reordered frame cannot re-advertise a revoked
		// capability. The value is copied; the delta's pointer is never shared.
		if d.Access == nil {
			return outcome{}
		}
		if row.Access != nil && d.Access.Revision < row.Access.Revision {
			return outcome{}
		}
		access := *d.Access
		row.Access = &access
		row.UpdatedAt = c.now()
		return outcome{changed: true}

	case contract.DeltaHold:
		if d.Held == nil {
			return outcome{}
		}
		row.Held = *d.Held
		return outcome{changed: true}

	case contract.DeltaRun:
		// A run's progress lives in the run digest this read-only client does
		// not keep; the delta still moves the row's timestamp.
		row.UpdatedAt = c.now()
		return outcome{changed: true}
	}
	return outcome{}
}

// rowLocked is the row for an agent, or nil. c.mu must be held.
func (c *Client) rowLocked(id string) *contract.AgentStatus {
	for i := range c.state.Agents {
		if c.state.Agents[i].ID == id {
			return &c.state.Agents[i]
		}
	}
	return nil
}

// applyProjectRemoved drops a project and every row it owns.
func (c *Client) applyProjectRemoved(d contract.ProjectRemovedDelta) outcome {
	c.mu.Lock()
	defer c.mu.Unlock()

	changed := false

	if len(c.state.Projects) > 0 {
		projects := make([]contract.ProjectStatus, 0, len(c.state.Projects))
		for _, p := range c.state.Projects {
			if p.Root == d.Project {
				changed = true
				continue
			}
			projects = append(projects, p)
		}
		if changed {
			c.state.Projects = projects
		}
	}

	if len(c.state.Agents) > 0 {
		dropped := false
		agents := make([]contract.AgentStatus, 0, len(c.state.Agents))
		for _, a := range c.state.Agents {
			if a.Project == d.Project {
				dropped = true
				continue
			}
			agents = append(agents, a)
		}
		if dropped {
			c.state.Agents = agents
			changed = true
		}
	}

	return outcome{changed: changed}
}

// applyInbox replaces the inbox. When the new inbox names an agent the rows do
// not have, or moves one to another group, the rows behind it are newer than
// ours: refetch them, at most once per WithRefetchEvery.
func (c *Client) applyInbox(inbox contract.Inbox) outcome {
	c.mu.Lock()
	defer c.mu.Unlock()

	previous := c.state.Inbox
	out := outcome{changed: !sameInbox(previous, inbox)}
	c.state.Inbox = inbox

	if !c.inboxNeedsAgentsLocked(previous, inbox) {
		return out
	}
	now := c.now()
	if !c.refetchedAt.IsZero() && now.Sub(c.refetchedAt) < c.refetchEvery {
		return out
	}
	c.refetchedAt = now
	out.refetch = refetchAgents
	return out
}

// inboxNeedsAgentsLocked reports whether the inbox names a row the snapshot
// lacks, or puts an agent in a different group than the previous inbox did.
// c.mu must be held.
func (c *Client) inboxNeedsAgentsLocked(previous, next contract.Inbox) bool {
	known := make(map[string]bool, len(c.state.Agents))
	for _, a := range c.state.Agents {
		known[a.ID] = true
	}

	was := inboxGroups(previous)
	now := inboxGroups(next)
	for id := range now {
		if !known[id] {
			return true
		}
		if group, ok := was[id]; ok && group != now[id] {
			return true
		}
	}
	return false
}

// inboxGroups maps every agent the inbox names to its group. An ID in more than
// one list keeps the first (most urgent) one. A queue item that is an intake
// names no agent.
func inboxGroups(in contract.Inbox) map[string]string {
	groups := make(map[string]string)
	add := func(group string, ids []string) {
		for _, id := range ids {
			if id == "" {
				continue
			}
			if _, ok := groups[id]; !ok {
				groups[id] = group
			}
		}
	}
	add("needsYou", in.Groups.NeedsYou)
	add("running", in.Groups.Running)
	add("ready", in.Groups.Ready)
	add("earlier", in.Groups.Earlier)
	for _, item := range in.Queue {
		if item.Kind != contract.InboxItemAgent || item.AgentID == "" {
			continue
		}
		if _, ok := groups[item.AgentID]; !ok {
			groups[item.AgentID] = "queue"
		}
	}
	return groups
}

// sameInbox reports whether two inboxes are equal.
func sameInbox(a, b contract.Inbox) bool {
	return sameStrings(a.Groups.NeedsYou, b.Groups.NeedsYou) &&
		sameStrings(a.Groups.Running, b.Groups.Running) &&
		sameStrings(a.Groups.Ready, b.Groups.Ready) &&
		sameStrings(a.Groups.Earlier, b.Groups.Earlier) &&
		sameQueue(a.Queue, b.Queue)
}

// sameStrings compares two string slices.
func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// sameQueue compares two inbox queues. Submission payloads are compared by
// their presence, since a queue item's own fields are what the screen shows.
func sameQueue(a, b []contract.InboxItem) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Kind != b[i].Kind || a[i].AgentID != b[i].AgentID || !a[i].At.Equal(b[i].At) {
			return false
		}
		if (a[i].Submission == nil) != (b[i].Submission == nil) {
			return false
		}
		// Submission carries a raw payload whose shape varies by intake; two
		// items differing only there are the same item for the fleet screen.
	}
	return true
}
