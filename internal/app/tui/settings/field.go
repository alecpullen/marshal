package settings

// fieldKind discriminates the row types a fieldList can render and edit.
type fieldKind int

const (
	kindToggle fieldKind = iota // bool: Space flips
	kindScalar                  // string/number/duration: Enter opens inline edit
	kindEnum                    // one of options(): ←/→ cycle, Enter opens picker
	kindDrill                   // Enter pushes build() as a new frame
)

// field is one row in a fieldList. Exactly one kind-group of closures is
// set. Setters write straight into the working config (the settings screen
// is a single transaction guarded by Ctrl+S / double-Esc at the top).
type field struct {
	id       string // stable id for the search registry ("shell.allow_network")
	title    string
	desc     string   // one-liner rendered under the row while the cursor is on it
	keywords []string // extra search terms beyond the title

	kind fieldKind

	// kindToggle
	getBool func() bool
	setBool func(bool)

	// kindScalar and kindEnum current-value access. setStr validates and
	// applies; a non-nil error renders under the row and blocks apply.
	// setStr == nil marks the row read-only (Enter does nothing).
	getStr func() string
	setStr func(string) error
	masked bool // render via maskKey; edits replace, empty input keeps

	// kindEnum
	options func() []string

	// kindDrill
	summary func() string // right-cell summary, e.g. "3 items"
	build   func() *frame

	// optional per-row delete: entry rows, list items, map keys, and
	// masked-secret clear all hang their removal behavior here (key: d).
	del func()
}

// frame is one level of a pane's drill-down stack: a titled fieldList plus
// optional add behavior for collection frames. (Stack management lives in
// pane.go — Task 4.)
type frame struct {
	title     string // breadcrumb segment, e.g. "github" in "MCP › github"
	list      *fieldList
	keyPrompt string             // add prompt label; "" with onAdd set = add without prompt
	onAdd     func(string) error // nil = 'a' disabled in this frame
}
