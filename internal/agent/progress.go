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
}

type progressTracker struct {
	history []callEntry
}

func newProgressTracker() *progressTracker {
	return &progressTracker{}
}

func (t *progressTracker) record(name, normalizedArgs string) {
	t.history = append(t.history, callEntry{
		name: name,
		args: normalizedArgs,
		cat:  categorize(name),
	})
}

// exactRepeat reports whether the last n entries are byte-identical.
func (t *progressTracker) exactRepeat(n int) bool {
	h := t.history
	if len(h) < n {
		return false
	}
	last := h[len(h)-1]
	for i := len(h) - n; i < len(h)-1; i++ {
		if h[i] != last {
			return false
		}
	}
	return true
}

// readOnlyChurn reports whether the last n entries are all read/search.
func (t *progressTracker) readOnlyChurn(n int) bool {
	h := t.history
	if len(h) < n {
		return false
	}
	for i := len(h) - n; i < len(h); i++ {
		if h[i].cat != catRead && h[i].cat != catSearch {
			return false
		}
	}
	return true
}

func (t *progressTracker) assess() assessment {
	if len(t.history) < 3 {
		return assessProgressing
	}
	if t.exactRepeat(3) {
		return assessHardStall
	}
	if t.readOnlyChurn(4) {
		return assessHardStall
	}
	if t.readOnlyChurn(3) {
		return assessStalling
	}
	return assessProgressing
}
