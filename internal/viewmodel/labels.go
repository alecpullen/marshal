package viewmodel

import "marshal/internal/db"

// FoldKeep is how many of the live turn's newest finished beats stay open.
const FoldKeep = 3

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
// previous bound beat's in this turn. "Previous" survives interruptions
// (messages, cards, markers) and resets per turn. Lists shorter than
// MinTaskTodos and todos no longer in the list get no label.
func labelTransitions(nodes []*Node, todos []db.TodoItem, wroteList bool) {
	if !wroteList && !taskMode(todos) {
		return
	}
	prev := ""
	for _, n := range nodes {
		if n.Kind != KindStep || n.Step == nil || n.Step.TodoID == "" {
			continue
		}
		id := n.Step.TodoID
		if id != prev {
			for i, td := range todos {
				if td.ID == id {
					n.Step.Todo = &TodoLabel{ID: id, Index: i + 1, Total: len(todos), Content: td.Content}
					break
				}
			}
		}
		prev = id
	}
}

// foldBeats marks finished beats folded. In the live (last) turn the newest
// FoldKeep finished beats stay open; earlier turns fold every finished beat.
// A beat that is live or holds anything live (a running subagent card, an
// in-flight call) never folds.
func foldBeats(nodes []*Node, lastTurn bool) {
	keep := 0
	if lastTurn {
		keep = FoldKeep
	}
	for i := len(nodes) - 1; i >= 0; i-- {
		n := nodes[i]
		if n.Kind != KindStep || n.Step == nil || n.AnyLive() {
			continue
		}
		if keep > 0 {
			keep--
			continue
		}
		n.Step.Folded = true
	}
}

// DrivenByTodos reports whether a turn is working from the todo list, which
// is when the band shows todo progress: some beat in it was labelled or
// bound to a todo.
func DrivenByTodos(turn *Node) bool {
	for _, n := range turn.Children {
		if n.Kind == KindStep && n.Step != nil && (n.Step.Todo != nil || n.Step.TodoID != "") {
			return true
		}
	}
	return false
}
