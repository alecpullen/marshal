package sessionsheet

import (
	"time"

	"marshal/internal/acpwire"
)

// Data is everything the session sheet can render. The Model assembles it
// from Session.Sheet and Session.Status on every frame. Sections never reach
// outside it, and the session caches everything expensive off the render path.
type Data struct {
	Git         acpwire.Git
	Repo        acpwire.SheetRepo
	Turns       []acpwire.SheetTurn // most recent first
	Totals      acpwire.SheetTotals
	Changed     []acpwire.SheetChangedFile
	Fleet       []acpwire.SheetFleetRow
	Now         time.Time // injected so elapsed-time rendering is deterministic
	Context     acpwire.SheetContext
	Tools       []acpwire.SheetTool
	Files       []acpwire.SheetFile
	Rules       []string
	Skills      []string
	StartedAt   time.Time // the session's start; zero when there is no session
	ToolBudget  acpwire.SheetToolBudget
	LoadedTools int
	Sandbox     acpwire.SheetSandbox
	Swarm       acpwire.SwarmProgress
	SDD         acpwire.SDDProgress
	Spinner     string
}

// Section is one read-only block in the session sheet. Sections are pure functions
// of Data: no state, no Update, no focus.
type Section interface {
	// ID is the stable identifier used by the config's hidden list.
	ID() string
	// Title is the section header, rendered in small-caps.
	Title() string
	// Relevant reports whether the section has anything to show.
	Relevant(d Data) bool
	// Priority orders collapse: lower numbers stay expanded longest.
	Priority() int
	// Render returns the expanded body, excluding the title row. When
	// maxRows is smaller than the natural height the section clips itself.
	Render(d Data, width, maxRows int) []string
	// OneLine is the collapsed summary. It must be a genuine summary, not
	// a truncation — the fit algorithm depends on it staying legible.
	OneLine(d Data, width int) string
	// Clippable reports whether the section has a meaningful intermediate
	// state between expanded and one-line. List-bearing sections do;
	// fixed-row sections do not.
	Clippable() bool
}
