package agent

type toolCategory string

const (
	catRead   toolCategory = "read"
	catSearch toolCategory = "search"
	catShell  toolCategory = "shell"
	catWrite  toolCategory = "write"
	catPatch  toolCategory = "patch"
	catOther  toolCategory = "other"
)

func categorize(toolName string) toolCategory {
	switch toolName {
	case "file.read", "repo.card", "repo.index", "repo.map", "symbols.find":
		return catRead
	case "repo.search":
		return catSearch
	case "shell.run":
		return catShell
	case "file.write_patch":
		return catPatch
	default:
		return catOther
	}
}

// mutating reports whether a category of tool call can change repository or
// system state. After a mutating call, previously gathered observations are
// stale, so repeating an earlier read counts as fresh progress again.
func mutating(cat toolCategory) bool {
	return cat == catShell || cat == catWrite || cat == catPatch
}

type assessment int

const (
	assessProgressing assessment = iota
	assessStalling
	assessHardStall
)

type callEntry struct {
	name string
	args string
	cat  toolCategory
	// novel is true when this (name, args) pair had not been executed since
	// the last mutating call. Novel work is progress by definition; only
	// repeats of already-gathered results count toward churn.
	novel bool
}

type progressTracker struct {
	history []callEntry
	seen    map[string]struct{}
}

func newProgressTracker() *progressTracker {
	return &progressTracker{seen: make(map[string]struct{})}
}

func (t *progressTracker) record(name, normalizedArgs string) {
	cat := categorize(name)
	if mutating(cat) {
		// State changed: earlier reads are stale, future re-reads are novel.
		t.seen = make(map[string]struct{})
	}
	key := name + "\x00" + normalizedArgs
	_, dup := t.seen[key]
	t.seen[key] = struct{}{}
	t.history = append(t.history, callEntry{
		name:  name,
		args:  normalizedArgs,
		cat:   cat,
		novel: !dup,
	})
}

// exactRepeat reports whether the last n entries are the same call
// (name+args). Novelty is deliberately ignored here: in a 3x repeat the
// first occurrence is novel and the rest are not, yet all three are the
// same call.
func (t *progressTracker) exactRepeat(n int) bool {
	h := t.history
	if len(h) < n {
		return false
	}
	last := h[len(h)-1]
	for i := len(h) - n; i < len(h)-1; i++ {
		if h[i].name != last.name || h[i].args != last.args {
			return false
		}
	}
	return true
}

// duplicateChurn reports whether the last n entries all repeat calls whose
// results were already gathered this turn (no novel work).
func (t *progressTracker) duplicateChurn(n int) bool {
	h := t.history
	if len(h) < n {
		return false
	}
	for i := len(h) - n; i < len(h); i++ {
		if h[i].novel {
			return false
		}
	}
	return true
}

// lastCall returns the most recent recorded call so nudge messages can name
// the specific repeated call. ok is false when nothing has been recorded.
func (t *progressTracker) lastCall() (name, args string, ok bool) {
	if len(t.history) == 0 {
		return "", "", false
	}
	last := t.history[len(t.history)-1]
	return last.name, last.args, true
}

func (t *progressTracker) assess() assessment {
	if len(t.history) < 3 {
		return assessProgressing
	}
	if t.exactRepeat(3) {
		return assessHardStall
	}
	if t.duplicateChurn(5) {
		return assessHardStall
	}
	if t.duplicateChurn(3) {
		return assessStalling
	}
	return assessProgressing
}
