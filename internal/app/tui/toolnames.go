package tui

import (
	"strings"

	"marshal/internal/app/tui/glyph"
	"marshal/internal/viewmodel"
)

// toolCategoryGlyphs maps a tool-name prefix to the gutter glyph for its
// category. Order matters: the first matching prefix wins, so write tools
// come before the broader file prefix. Single-cell glyphs only — state
// glyphs (▸ running, ✗ error) always win over these.
var toolCategoryGlyphs = []struct {
	prefix string
	glyph  string
}{
	{"file.write", glyph.Edit},
	{"file.edit", glyph.Edit},
	{"patch.", glyph.Edit},
	{"file.", glyph.File},
	{"shell.", glyph.Shell},
	{"test.", glyph.Shell},
	{"repo.search", glyph.Search},
	{"codebase.search", glyph.Search},
	{"json.", glyph.Search},
	{"csv.", glyph.Search},
	{"symbols", glyph.Search},
	{"agent.", glyph.Agent},
	{"web.", glyph.Web},
	{"browser.", glyph.Web},
}

// toolCategoryGlyph returns the gutter glyph for a tool's category, or the
// generic dot for tools outside every category.
func toolCategoryGlyph(name string) string {
	for _, c := range toolCategoryGlyphs {
		if strings.HasPrefix(name, c.prefix) {
			return c.glyph
		}
	}
	return glyph.Ambient
}

// DisplayToolName returns a human-readable label for a tool. Unknown tools
// get a title-cased, dot-to-space transformation (only the first segment
// is capitalized).
func DisplayToolName(name string) string { return viewmodel.DisplayToolName(name) }
