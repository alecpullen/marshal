package viewmodel

// SplitLive separates the live part from history. While busy, the live part is
// the newest turn's last beat — whether or not its current step has ended,
// because a narrationless step can still merge into it — plus the completion
// events after it, which are only ever events of work that beat launched, plus
// the streaming reasoning node, which Build always makes the newest turn's last
// child. Everything else is history.
//
// The input is never mutated: the returned history shares the turn pointers
// except for the last turn, which is a shallow copy with a new Children slice.
func SplitLive(turns []*Node, busy bool) (history []*Node, live []*Node) {
	if !busy || len(turns) == 0 {
		return turns, nil
	}
	last := turns[len(turns)-1]
	children := last.Children
	if len(children) == 0 {
		return turns, nil
	}
	var thinking *Node
	if children[len(children)-1].ID == LiveThinkingID {
		thinking = children[len(children)-1]
		children = children[:len(children)-1]
	}
	// The live beat is the last one whose trailing events are all completions
	// of work it launched.
	i := len(children)
	for k := len(children) - 1; k >= 0; k-- {
		if children[k].Kind != KindStep {
			continue
		}
		ok := true
		for _, c := range children[k+1:] {
			if c.Kind != KindCompletion {
				ok = false
				break
			}
		}
		if ok {
			i = k
		}
		break
	}
	if i == len(children) && thinking == nil {
		return turns, nil
	}
	live = append([]*Node{}, children[i:]...)
	if thinking != nil {
		live = append(live, thinking)
	}
	history = make([]*Node, len(turns))
	copy(history, turns)
	trimmed := *last
	trimmed.Children = append([]*Node{}, children[:i]...)
	history[len(history)-1] = &trimmed
	return history, live
}
