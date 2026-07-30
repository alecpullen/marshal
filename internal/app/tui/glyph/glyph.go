// Package glyph is the TUI's single glyph vocabulary.
//
// It is a leaf package so that both the tui package and its sub-packages
// (chrome, listpanel, sidepanel, picker, …) reference the same constants;
// sub-packages cannot import tui itself.
package glyph

// Each glyph has exactly one meaning, and every call site references a
// constant here rather than a literal.
//
// The vocabulary previously drifted badly: ▍ carried five unrelated roles
// (final-answer gutter, thinking body, input bar, panel rail, chrome gutter),
// · carried eight, ▶ and ▸ both meant "current" in different panels, three
// separate idioms drew a left rail (▍, │, bare space), and "web" was ◇ in the
// tool-category table but 🌐 in the live strip and status line. Meaning is
// what a glyph earns; the same shape meaning several things costs the reader
// the ability to skim.
//
// Rule: state glyphs (Running, Error) always win over category
// glyphs. Single-cell glyphs only — a double-width rune breaks the gutter's
// column math, which is why 🌐 was retired in favour of Web.
const (
	// Rail is the structural rail. It marks a contained region — panel
	// gutters, the input state bar, stacked-panel rails, nested bodies — and
	// is never a transcript item marker.
	Rail = "▍"

	// User marks a user turn.
	User = "❯"

	// Running marks anything in flight or currently selected: an active
	// tool call, the cursor in a completion popup, the armed approval action,
	// the in-progress todo.
	Running = "▸"

	// Ambient marks secondary, non-actionable content: system notices,
	// queued messages, tool results, pending todos, uncategorized tools.
	Ambient = "·"

	// Terminal states.
	OK       = "✓"
	Error    = "✗"
	Warning  = "⚠"
	Question = "?"
	Thinking = "⚙"

	// Tool categories. Referenced by toolCategoryGlyphs.
	Edit   = "✎"
	File   = "≡"
	Shell  = "›"
	Search = "⌕"
	Agent  = "⧉"
	// Web marks web and browser activity. It replaced 🌐, which is
	// double-width and blew the status line's width budget.
	Web = "◇"

	// Brand is the startup banner mark, and marks a bound preset or the
	// currently-selected item in a picker badge.
	Brand = "●"
	// CustomAgent marks a cast entry bound to a user-defined agent.
	CustomAgent = "◆"
)
