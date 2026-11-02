package tui

// promptKind is the kind of request a promptRef names.
type promptKind int

const (
	promptApproval promptKind = iota
	promptQuestion
	promptSkill
	promptSudo
)

// promptRef names one open request: its kind and its wire key. The zero
// value names none.
type promptRef struct {
	kind promptKind
	key  string
}
