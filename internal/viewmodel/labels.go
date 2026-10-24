package viewmodel

import "marshal/internal/db"

// TodoLabel marks the beat where work moves to a different todo. It is shown
// once per transition, never repeated for later beats of the same todo, so a
// todo the model forgets to advance costs one line rather than one header
// per interruption.
type TodoLabel struct {
	ID      string
	Index   int // 1-based position in the current list
	Total   int
	Content string
}

// labelTransitions sets Todo on each beat whose bound todo differs from the
// previous bound beat's in this turn, and returns the newest plan in effect
// once the turn is included. "Previous" survives interruptions (messages,
// cards, completion events) and resets per turn.
//
// The tag reads the plan in effect — the plan left by the write that re-bound
// the beat, else the newest plan line before it — so a list rewritten later
// does not change a row that already settled. The current list is the fallback
// only when no plan with an active item is known.
func labelTransitions(nodes []*Node, todos []db.TodoItem, plan *PlanUpdate) *PlanUpdate {
	prev := ""
	cur := plan
	for _, n := range nodes {
		if n.Kind != KindStep || n.Step == nil {
			continue
		}
		own := ownPlan(n)
		if n.Step.TodoID == "" {
			if own != nil {
				cur = own
			}
			continue
		}
		id := n.Step.TodoID
		if id != prev {
			src := n.Step.labelPlan
			if src == nil {
				src = cur
			}
			if src != nil && src.Active > 0 {
				n.Step.Todo = &TodoLabel{ID: id, Index: src.Active, Total: src.Total(), Content: ScrubText(src.ActiveText)}
			} else {
				for i, td := range todos {
					if td.ID == id {
						n.Step.Todo = &TodoLabel{ID: id, Index: i + 1, Total: len(todos), Content: ScrubText(td.Content)}
						break
					}
				}
			}
		}
		prev = id
		if own != nil {
			cur = own
		}
	}
	return cur
}

// ownPlan is the plan row a beat wrote itself, if any. keepLastPlanRow leaves
// at most one.
func ownPlan(n *Node) *PlanUpdate {
	for _, c := range n.Children {
		if c.Kind == KindPlanUpdate && c.PlanUpdate != nil {
			return c.PlanUpdate
		}
	}
	return nil
}
