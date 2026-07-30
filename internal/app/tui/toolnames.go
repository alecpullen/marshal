package tui

import (
	"strings"

	"marshal/internal/app/tui/glyph"
)

var toolDisplayNames = map[string]string{
	"file.read":        "Read file",
	"file.write_patch": "Edit file",
	"patch.apply":      "Apply patch",
	"shell.run":        "Run command",
	"test.run":         "Run tests",
	"repo.search":      "Search repo",
	"codebase.search":  "Codebase search",
	"json.query":       "Query JSON",
	"csv.inspect":      "Inspect CSV",
	"web.fetch":        "Fetch page",
	"web.search":       "Search web",
	"git.status":       "Git status",
	"git.diff":         "Git diff",
	"symbols.find":     "Find symbols",
	"todos":            "Update todos",
	"mode.request":     "Switch mode",
	"question.ask":     "Ask question",
	"ask_user":         "Ask user",
	"agent.run":        "Run subagent",
}

// toolCategoryGlyphs maps a tool-name prefix to the gutter glyph for its
// category. Order matters: the first matching prefix wins, so write tools
// come before the broader file prefix. Single-cell glyphs only — state
// glyphs (▸ running, ✗ error) always win over these.
var toolCategoryGlyphs = []struct {
	prefix string
	glyph  string
}{
	{"file.write", glyph.Edit},
	{"patch.", glyph.Edit},
	{"file.", glyph.File},
	{"shell.", glyph.Shell},
	{"test.", glyph.Shell},
	{"repo.search", glyph.Search},
	{"codebase.search", glyph.Search},
	{"json.", glyph.Search},
	{"csv.", glyph.Search},
	{"symbols.", glyph.Search},
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
func DisplayToolName(name string) string {
	if label, ok := toolDisplayNames[name]; ok {
		return label
	}
	parts := strings.Split(name, ".")
	if len(parts) > 0 && len(parts[0]) > 0 {
		parts[0] = strings.ToUpper(parts[0][:1]) + parts[0][1:]
	}
	return strings.Join(parts, " ")
}

// pluralizeToolName returns the display name for a tool in plural form,
// suitable for grouped tool call headings (e.g. "Read file" → "Read files",
// "Run command" → "Run commands").
func pluralizeToolName(name string) string {
	singular := DisplayToolName(name)
	// Simple pluralization: append "s". This covers all current display
	// names (file, command, tests, page, status, diff, symbols, todos,
	// mode, question, user, subagent) since none end in s/x/y/z/ch/sh.
	if strings.HasSuffix(singular, "s") || strings.HasSuffix(singular, "x") {
		return singular + "es"
	}
	return singular + "s"
}
