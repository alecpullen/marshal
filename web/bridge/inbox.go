package bridge

import (
	"sort"
	"time"

	"marshal/web/bridge/contract"
)

// inboxView is the fleet's inbox (P5 F2). Task 4 moves it into the contract
// as contract.Inbox.
type inboxView struct {
	Groups inboxGroups `json:"groups"`
	Queue  []inboxItem `json:"queue"`
}

// inboxGroups are agent IDs, each list in urgency order.
type inboxGroups struct {
	NeedsYou []string `json:"needsYou"`
	Running  []string `json:"running"`
	Ready    []string `json:"ready"`
	Earlier  []string `json:"earlier"`
}

// inboxItem is one entry in the needs-you queue, oldest first.
type inboxItem struct {
	Kind      string    `json:"kind"` // "agent" or "intake"
	AgentID   string    `json:"agentId,omitempty"`
	PendingID string    `json:"pendingId,omitempty"` // replaced in Task 4
	At        time.Time `json:"at"`
}

// computeInbox applies F2's rules to a snapshot and the intake list.
//
// Each agent lands in the first group it qualifies for: Needs you (pending),
// Running, Ready to ship (idle with changes and no PR), else Earlier. Each
// group is sorted by pending kind, then status rank, then most recently
// updated; the sorts are stable, so equal keys keep the snapshot's order.
//
// The queue merges the Needs-you agents at their UpdatedAt with the intake
// submissions at their CreatedAt, oldest first. Agents are appended before
// submissions, so a stable sort puts an agent first on a tie.
//
// Every list is non-nil: an empty inbox encodes as [] and never null.
func computeInbox(agents []AgentStatus, pending []PendingSpawn) inboxView {
	buckets := map[inboxGroupKey][]AgentStatus{
		inboxNeedsYou: nil,
		inboxRunning:  nil,
		inboxReady:    nil,
		inboxEarlier:  nil,
	}
	for _, a := range agents {
		var key inboxGroupKey
		switch {
		case a.Pending != nil:
			key = inboxNeedsYou
		case a.Status == contract.AgentRunning:
			key = inboxRunning
		case a.Status == contract.AgentIdle && a.ChangedFiles > 0 && a.PRUrl == "":
			key = inboxReady
		default:
			key = inboxEarlier
		}
		buckets[key] = append(buckets[key], a)
	}

	view := inboxView{
		Groups: inboxGroups{
			NeedsYou: make([]string, 0, len(buckets[inboxNeedsYou])),
			Running:  make([]string, 0, len(buckets[inboxRunning])),
			Ready:    make([]string, 0, len(buckets[inboxReady])),
			Earlier:  make([]string, 0, len(buckets[inboxEarlier])),
		},
		Queue: make([]inboxItem, 0, len(buckets[inboxNeedsYou])+len(pending)),
	}

	// Agents first, so a stable sort keeps them ahead of a submission that
	// arrived at the same instant.
	for _, a := range buckets[inboxNeedsYou] {
		view.Queue = append(view.Queue, inboxItem{Kind: "agent", AgentID: a.ID, At: a.UpdatedAt})
	}
	for _, p := range pending {
		view.Queue = append(view.Queue, inboxItem{Kind: "intake", PendingID: p.ID, At: p.CreatedAt})
	}
	sort.SliceStable(view.Queue, func(i, j int) bool { return view.Queue[i].At.Before(view.Queue[j].At) })

	for key, list := range buckets {
		sort.SliceStable(list, func(i, j int) bool {
			pi, pj := inboxPendingRank(list[i]), inboxPendingRank(list[j])
			if pi != pj {
				return pi < pj
			}
			si, sj := inboxStatusRank(list[i].Status), inboxStatusRank(list[j].Status)
			if si != sj {
				return si < sj
			}
			return list[i].UpdatedAt.After(list[j].UpdatedAt)
		})
		ids := make([]string, 0, len(list))
		for _, a := range list {
			ids = append(ids, a.ID)
		}
		switch key {
		case inboxNeedsYou:
			view.Groups.NeedsYou = ids
		case inboxRunning:
			view.Groups.Running = ids
		case inboxReady:
			view.Groups.Ready = ids
		case inboxEarlier:
			view.Groups.Earlier = ids
		}
	}
	return view
}

// inboxGroupKey names one of the four groups.
type inboxGroupKey string

// The inbox's groups, in the order an agent is tested for them.
const (
	inboxNeedsYou inboxGroupKey = "needsYou"
	inboxRunning  inboxGroupKey = "running"
	inboxReady    inboxGroupKey = "ready"
	inboxEarlier  inboxGroupKey = "earlier"
)

// inboxPendingRank is web/ui/src/lib/fleet.ts's pendingRank: an approval
// outranks a question, and both outrank an agent with nothing pending.
func inboxPendingRank(a AgentStatus) int {
	if a.Pending == nil {
		return 2
	}
	if a.Pending.Kind == contract.PendingApproval {
		return 0
	}
	return 1
}

// inboxStatusRank is web/ui/src/lib/fleet.ts's rank table. A state outside
// it ranks with idle, last.
func inboxStatusRank(s contract.AgentState) int {
	switch s {
	case contract.AgentAwaitingApproval, contract.AgentAwaitingQuestion:
		return 0
	case contract.AgentError:
		return 1
	case contract.AgentRunning:
		return 2
	default:
		return 3
	}
}

// Inbox is the fleet's inbox now.
func (f *Fleet) Inbox() inboxView { return computeInbox(f.Snapshot(), f.ws.Pending()) }
