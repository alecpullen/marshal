package tui

import "strings"

var toolDisplayNames = map[string]string{
	"file.read":        "Read file",
	"file.write_patch": "Edit file",
	"patch.apply":      "Apply patch",
	"shell.run":        "Run command",
	"test.run":         "Run tests",
	"repo.search":      "Search repo",
	"codebase.search":  "Codebase search",
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
	{"file.write", "✎"},
	{"patch.", "✎"},
	{"file.", "≡"},
	{"shell.", "›"},
	{"test.", "›"},
	{"repo.search", "⌕"},
	{"codebase.search", "⌕"},
	{"symbols.", "⌕"},
	{"agent.", "⧉"},
	{"web.", "◇"},
	{"browser.", "◇"},
}

// toolCategoryGlyph returns the gutter glyph for a tool's category, or the
// generic dot for tools outside every category.
func toolCategoryGlyph(name string) string {
	for _, c := range toolCategoryGlyphs {
		if strings.HasPrefix(name, c.prefix) {
			return c.glyph
		}
	}
	return "·"
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
