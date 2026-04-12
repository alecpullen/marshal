// Package edit implements executor output parsers and applicators for various
// edit formats (whole file, SEARCH/REPLACE, unified diff).
package edit

import (
	"strings"
)

// Edit represents a single file change produced by parsing executor output.
type Edit struct {
	Path    string
	Search  string // empty for whole-file replacement
	Replace string // new content
}

// Result is the outcome of applying edits to a file.
type Result struct {
	Path    string
	Applied bool
	Error   error
}

// Format is the interface for parsing executor responses and applying edits.
// Implementations: WholeFileFormat, SearchReplaceFormat, UdiffFormat.
type Format interface {
	// Name returns the format identifier (e.g., "wholefile", "search-replace", "udiff").
	Name() string

	// Parse extracts file edits from an executor response.
	Parse(response string) []Edit

	// Apply attempts to apply an edit to current file content.
	// Returns (updatedContent, ok) where ok indicates success.
	Apply(current string, edit Edit) (updated string, ok bool)
}

// WholeFileFormat parses fenced code blocks with preceding filenames.
type WholeFileFormat struct{}

func (WholeFileFormat) Name() string { return "wholefile" }

func (f WholeFileFormat) Parse(response string) []Edit {
	files := ParseWhole(response)
	edits := make([]Edit, len(files))
	for i, fe := range files {
		edits[i] = Edit{Path: fe.Path, Search: "", Replace: fe.Content}
	}
	return edits
}

func (WholeFileFormat) Apply(current string, edit Edit) (string, bool) {
	// Whole-file replacement always succeeds (overwrites).
	return edit.Replace, true
}

// SearchReplaceFormat parses SEARCH/REPLACE blocks with fuzzy matching.
type SearchReplaceFormat struct{}

func (SearchReplaceFormat) Name() string { return "search-replace" }

func (f SearchReplaceFormat) Parse(response string) []Edit {
	srs := ParseSearchReplace(response)
	edits := make([]Edit, len(srs))
	for i, sr := range srs {
		edits[i] = Edit{Path: sr.Path, Search: sr.Search, Replace: sr.Replace}
	}
	return edits
}

func (SearchReplaceFormat) Apply(current string, edit Edit) (string, bool) {
	if edit.Search == "" {
		// Creating new file.
		return edit.Replace, true
	}
	sr := SearchReplaceEdit{Path: edit.Path, Search: edit.Search, Replace: edit.Replace}
	return sr.ApplyToContent(current)
}

// UdiffFormat parses unified diffs and applies them.
type UdiffFormat struct{}

func (UdiffFormat) Name() string { return "udiff" }

func (f UdiffFormat) Parse(response string) []Edit {
	udiffs := ParseUdiff(response)
	// Udiff applies directly during parse phase for simplicity.
	// We convert to Edit representation with special handling.
	edits := make([]Edit, 0, len(udiffs))
	for _, ud := range udiffs {
		// Udiff hunks are applied as atomic units; we store them as a special
		// marker that the engine recognizes for direct application.
		// For Format interface compliance, we return an edit that signals
		// the engine to use ApplyUdiff directly.
		edits = append(edits, Edit{
			Path:    ud.Path,
			Search:  "__UDIFF__", // marker for engine to route to special handler
			Replace: encodeHunks(ud.Hunks),
		})
	}
	return edits
}

func (UdiffFormat) Apply(current string, edit Edit) (string, bool) {
	if edit.Search == "__UDIFF__" {
		// This should not happen via Format.Apply; udiff is applied via engine.
		return current, false
	}
	return edit.Replace, true // fallback to whole-file
}

// encodeHunks serializes hunks for the engine to decode.
func encodeHunks(hunks []Hunk) string {
	var sb strings.Builder
	for _, h := range hunks {
		sb.WriteString("@@ hunk @@\n")
		for _, l := range h.Lines {
			sb.WriteByte(l.Op)
			sb.WriteString(l.Text)
			sb.WriteByte('\n')
		}
	}
	return sb.String()
}

// FormatFor returns the appropriate Format implementation based on format name.
// This is used when edit format is selected via config (global setting).
func FormatFor(name string) Format {
	switch name {
	case "search-replace":
		return SearchReplaceFormat{}
	case "udiff":
		return UdiffFormat{}
	default:
		return WholeFileFormat{}
	}
}

// AllFormats returns a list of all available formats for display/help.
func AllFormats() []Format {
	return []Format{WholeFileFormat{}, SearchReplaceFormat{}, UdiffFormat{}}
}
