package tui

import "testing"

func TestToolCategoryGlyph(t *testing.T) {
	cases := map[string]string{
		"file.read":        "≡",
		"file.write_patch": "✎",
		"patch.apply":      "✎",
		"shell.run":        "›",
		"test.run":         "›",
		"repo.search":      "⌕",
		"codebase.search":  "⌕",
		"symbols.find":     "⌕",
		"agent.run":        "⧉",
		"web.fetch":        "◇",
		"web.search":       "◇",
		"git.status":       "·",
		"todos":            "·",
		"unknown.thing":    "·",
	}
	for name, want := range cases {
		if got := toolCategoryGlyph(name); got != want {
			t.Errorf("toolCategoryGlyph(%q) = %q, want %q", name, got, want)
		}
	}
}
