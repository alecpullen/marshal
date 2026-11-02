package viewmodel

import "time"

// mergeBeats folds each narrationless step into the beat directly before it,
// when the same actor produced both and it did not move to a different todo.
// Any other block between them (a message, a run event, a turn-level card)
// ends the beat, so the transcript stays chronological; a todo change ends it
// too, so the new todo gets its divider and its own work time.
//
// A completion event follows the beat whose row launched its work. Any other
// event that ends while a beat is still open comes before that beat, because
// the beat joins history only when it settles — the next block that does not
// merge into it starts, or the turn ends. An event that ends after a finished
// turn's last beat ended follows it.
//
// open says the turn is the newest one and the session is busy, so its last
// beat is still open.
func mergeBeats(nodes []*Node, open bool) []*Node {
	out := make([]*Node, 0, len(nodes))
	var beat *Node
	var held []*Node // completions that landed while the beat was pending

	settle := func(atEnd bool) {
		if beat == nil {
			return
		}
		before, after := splitHeld(beat, held, atEnd, open)
		out = append(out, before...)
		out = append(out, beat)
		out = append(out, after...)
		beat, held = nil, nil
	}

	for _, n := range nodes {
		if n.Kind == KindCompletion {
			if beat != nil {
				held = append(held, n)
			} else {
				out = append(out, n)
			}
			continue
		}
		if n.Kind != KindStep || n.Step == nil {
			settle(false)
			out = append(out, n)
			continue
		}
		if beat != nil && len(n.Step.Narration) == 0 && sameActor(beat.Step, n.Step) && sameTodo(beat.Step, n.Step) {
			absorb(beat, n)
			continue
		}
		settle(false)
		beat = n
	}
	settle(true)
	return out
}

// splitHeld divides the completions held while a beat was pending into those
// that follow the beat and those that come before it.
func splitHeld(beat *Node, held []*Node, atEnd, open bool) (before, after []*Node) {
	launched := make(map[NodeID]bool, len(beat.Children))
	for _, c := range beat.Children {
		launched[c.ID] = true
	}
	ended := beat.Step.Step.EndedAt
	for _, n := range held {
		c := n.Completion
		if c == nil {
			before = append(before, n)
			continue
		}
		if c.Launch.Key != "" && launched[c.Launch] {
			after = append(after, n)
			continue
		}
		if atEnd && !open && !ended.IsZero() && completionTime(c).After(ended) {
			after = append(after, n)
			continue
		}
		before = append(before, n)
	}
	return before, after
}

// completionTime is when a completion event happened: its end, or its start
// when no end was recorded (a lost job).
func completionTime(c *CompletionInfo) time.Time {
	if !c.EndedAt.IsZero() {
		return c.EndedAt
	}
	return c.StartedAt
}

// sameActor reports whether two beats were produced by the same agent. A
// step-less (legacy) beat has no actor and joins whatever precedes it.
func sameActor(a, b *StepInfo) bool {
	if a.Step.ID == 0 || b.Step.ID == 0 {
		return true
	}
	return a.Step.Actor.Role == b.Step.Actor.Role && a.Step.Actor.Label == b.Step.Actor.Label
}

// sameTodo reports whether n continues the beat's todo: n bound to no todo
// stays with whatever the beat is working on.
func sameTodo(beat, n *StepInfo) bool {
	return n.TodoID == "" || n.TodoID == beat.TodoID
}

// absorb moves n's content into beat. The beat keeps its own ID, so expand
// state and the render cache carry over as responses join it.
func absorb(beat, n *Node) {
	bi, ni := beat.Step, n.Step
	if bi.Step.ID == 0 && ni.Step.ID != 0 {
		started := bi.Step.StartedAt
		bi.Step = ni.Step
		if !started.IsZero() && started.Before(bi.Step.StartedAt) {
			bi.Step.StartedAt = started
		}
	} else if ni.Step.ID != 0 || ni.Step.EndedAt.After(bi.Step.EndedAt) {
		// A stamped response's zero EndedAt means it is still open, so the
		// beat is too. A loose (legacy) response's zero EndedAt means only
		// that it was never recorded, and must not reopen a finished beat.
		bi.Step.EndedAt = ni.Step.EndedAt
	}
	bi.StepIDs = append(bi.StepIDs, ni.StepIDs...)
	bi.Thinking = append(bi.Thinking, ni.Thinking...)
	bi.TodoWrites = append(bi.TodoWrites, ni.TodoWrites...)
	beat.Children = append(beat.Children, n.Children...)
	beat.Live = beat.Live || n.Live
	beat.Version = versionOfStep(beat)
}
