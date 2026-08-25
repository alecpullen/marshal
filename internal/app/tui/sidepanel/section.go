package sidepanel

import (
	"time"

	"marshal/internal/app/session"
	"marshal/internal/app/tui/gitinfo"
	"marshal/internal/contextpack"
	"marshal/internal/db"
	"marshal/internal/tools/registry"
)

// RepoStats is the indexed-repository summary the repo section renders.
// Populated by the Model from the project DB on turn boundaries.
type RepoStats struct {
	Files     int
	Symbols   int
	Languages []LanguageShare // descending by Share, at most 3 entries
}

// LanguageShare is one language's percentage of the indexed file count.
type LanguageShare struct {
	Name  string
	Share int // 0-100
}

// ChangedFile is one entry in the changed-files section.
type ChangedFile struct {
	Path    string
	Status  rune // 'A' added, 'M' modified, 'D' deleted
	Added   int
	Removed int
}

// Data is everything the rail can render. The Model assembles it once per
// frame. Sections never reach outside it, and everything expensive (DB
// queries, git subprocesses) is cached in on turn boundaries — never
// computed during render.
type Data struct {
	State *session.State
	Git   gitinfo.Info
	Repo  RepoStats
	Turns []db.TurnMetricsRow // most recent first
	// Totals is the session-scoped usage aggregate backing the footer's
	// turn/token counters. Distinct from Turns, which is the recent-rows
	// window other sections read: Turns is capped by its query limit and
	// (before this change) was project-scoped, so it could not be used to
	// count a session's turns.
	Totals  db.UsageTotals
	Changed []ChangedFile
	Now     time.Time // injected so elapsed-time rendering is deterministic

	// Pack is the context pack snapshot. Held directly rather than read
	// from State so tests can build one without a session.
	Pack contextpack.Pack

	// Audit is the session's tool-call log, held directly so sections are
	// testable without a live session.
	Audit []registry.AuditEvent

	// Rules is the session's approval rules, held directly so sections are
	// testable without a live session.
	Rules []string

	// Skills is the session's currently active skills, held directly so the
	// section is testable without a live session. This is cumulative state
	// — what is loaded right now — as distinct from the per-turn transcript
	// line, which records what loaded at one point.
	Skills []string

	// Swarm is the swarm run progress, held directly so tests don't need a
	// live session.
	Swarm session.SwarmProgress

	// SDD is the SDD run progress, held directly so tests don't need a
	// live session.
	SDD session.SDDProgress

	// Spinner is the current shared spinner frame ("" when gated or
	// idle), so live sections tick in lockstep with the main column.
	Spinner string
}

// Section is one read-only block in the rail. Sections are pure functions
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
