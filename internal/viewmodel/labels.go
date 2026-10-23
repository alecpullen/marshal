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
// previous bound beat's in this turn. "Previous" survives interruptions
// (messages, cards, markers) and resets per turn. A list of any length labels
// its transitions; a todo no longer in the list gets no label.
func labelTransitions(nodes []*Node, todos []db.TodoItem) {
	prev := ""
	for _, n := range nodes {
		if n.Kind != KindStep || n.Step == nil || n.Step.TodoID == "" {
			continue
		}
		id := n.Step.TodoID
		if id != prev {
			for i, td := range todos {
				if td.ID == id {
					n.Step.Todo = &TodoLabel{ID: id, Index: i + 1, Total: len(todos), Content: ScrubText(td.Content)}
					break
				}
			}
		}
		prev = id
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
