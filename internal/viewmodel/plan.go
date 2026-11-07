package viewmodel

import (
	"encoding/json"
	"fmt"

	"marshal/internal/db"
	"marshal/internal/tools/registry"
)

// PlanUpdate is a beat's plan line: the todo list as the beat's last
// todo.write left it. It is read from that write alone, never from the list
// as it stands now, so a plan line in history does not change when the list
// does.
type PlanUpdate struct {
	// Status is each item's status in list order: "completed",
	// "in_progress" or "pending". Unfinished items the write left out are
	// carried by the tool and appended here as "pending".
	Status []string
	// Active is the 1-based position of the first in-progress item, 0 when
	// none is in progress; ActiveText is that item's content.
	Active     int
	ActiveText string
	// Cleared is true when the write left the list empty.
	Cleared bool
}

// Total is how many items the list holds.
func (p *PlanUpdate) Total() int { return len(p.Status) }

// Done is how many items are completed.
func (p *PlanUpdate) Done() int {
	n := 0
	for _, s := range p.Status {
		if s == "completed" {
			n++
		}
	}
	return n
}

// planOf reads the list a successful todo.write left. ok is false for a
// failed or denied write, which changed nothing, and for arguments that do
// not parse.
func planOf(ev registry.AuditEvent) (*PlanUpdate, bool) {
	if EventFailed(ev) {
		return nil, false
	}
	var args struct {
		Todos []db.TodoItem `json:"todos"`
	}
	if json.Unmarshal(ev.Args, &args) != nil {
		return nil, false
	}
	p := &PlanUpdate{}
	for i, it := range args.Todos {
		st := it.Status
		if st != "completed" && st != "in_progress" {
			st = "pending"
		}
		p.Status = append(p.Status, st)
		if st == "in_progress" && p.Active == 0 {
			p.Active, p.ActiveText = i+1, it.Content
		}
	}
	// The tool keeps unfinished items the write left out and reports the
	// whole list's length in its summary ("tasks updated · 5 items"). Those
	// carried items have no status in the arguments; they are unfinished, so
	// they read as waiting.
	var total int
	if _, err := fmt.Sscanf(ev.ResultSummary, "tasks updated · %d items", &total); err == nil {
		for len(p.Status) < total {
			p.Status = append(p.Status, "pending")
		}
	}
	p.Cleared = len(p.Status) == 0
	return p, true
}

// PlanID is the identity of the plan row a todo.write produced.
func PlanID(ev registry.AuditEvent) NodeID {
	return NodeID{KindPlanUpdate, "plan:" + ToolKey(ev)}
}

// planRow is the plan row for a step's last todo.write that changed the list.
// It is nil when none did.
func planRow(writes []registry.AuditEvent) *Node {
	for i := len(writes) - 1; i >= 0; i-- {
		p, ok := planOf(writes[i])
		if !ok {
			continue
		}
		n := &Node{ID: PlanID(writes[i]), Kind: KindPlanUpdate, PlanUpdate: p}
		n.Version = versionOfPlan(p)
		return n
	}
	return nil
}

// keepLastPlanRow drops every plan row of a beat but the last: several writes
// in one beat show only the state the beat ended with.
func keepLastPlanRow(beat *Node) {
	last := -1
	for i, c := range beat.Children {
		if c.Kind == KindPlanUpdate {
			last = i
		}
	}
	if last < 0 {
		return
	}
	kept := beat.Children[:0]
	for i, c := range beat.Children {
		if c.Kind == KindPlanUpdate && i != last {
			continue
		}
		kept = append(kept, c)
	}
	beat.Children = kept
}

// versionOfPlan hashes the plan's content. Done and Total are counts of
// Status, so WirePlan reads nothing here that a change to Status cannot move:
// a plan node's payload comes from the one audit event that gives the node its
// identity and nothing rewrites it. If a plan ever becomes mutable after the
// fact, this must hash the counts too.
func versionOfPlan(p *PlanUpdate) uint64 {
	b := newHash()
	b.f("plan|%v|%d|%s|%v|", p.Status, p.Active, p.ActiveText, p.Cleared)
	return b.sum()
}
