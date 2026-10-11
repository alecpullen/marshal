package viewmodel

// BeatRowCap is how many rows a beat shows before older ones collapse into a
// "+N earlier calls" line. A model that rarely narrates can pile dozens of
// calls into one beat; the live tail is what matters while watching.
const BeatRowCap = 8

// mergeBeats folds each narrationless step into the beat directly before it,
// when the same actor produced both. Any other block between them (a
// message, a run event, a turn-level card) ends the beat, so the transcript
// stays chronological.
func mergeBeats(nodes []*Node) []*Node {
	out := make([]*Node, 0, len(nodes))
	var beat *Node
	for _, n := range nodes {
		if n.Kind != KindStep || n.Step == nil {
			out = append(out, n)
			beat = nil
			continue
		}
		if beat != nil && len(n.Step.Narration) == 0 && sameActor(beat.Step, n.Step) {
			absorb(beat, n)
			continue
		}
		out = append(out, n)
		beat = n
	}
	return out
}

// sameActor reports whether two beats were produced by the same agent. A
// step-less (legacy) beat has no actor and joins whatever precedes it.
func sameActor(a, b *StepInfo) bool {
	if a.Step.ID == 0 || b.Step.ID == 0 {
		return true
	}
	return a.Step.Actor.Role == b.Step.Actor.Role && a.Step.Actor.Label == b.Step.Actor.Label
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
	} else {
		bi.Step.EndedAt = ni.Step.EndedAt
	}
	bi.StepIDs = append(bi.StepIDs, ni.StepIDs...)
	bi.Thinking = append(bi.Thinking, ni.Thinking...)
	bi.TodoWrites = append(bi.TodoWrites, ni.TodoWrites...)
	if ni.LiveThinking != "" {
		bi.LiveThinking = ni.LiveThinking
	}
	beat.Children = append(beat.Children, n.Children...)
	beat.Live = beat.Live || n.Live
	beat.Version = versionOfStep(beat)
}
