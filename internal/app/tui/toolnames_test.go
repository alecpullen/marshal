package tui

import "testing"

func TestToolCategoryGlyph(t *testing.T) {
	cases := map[string]string{
		"file.read":        "≡",
		"file.write_patch": "✎",
		"patch.apply":      "✎",
		"shell.run":        "›",
		"test.run":         "›",
		"repo.search":      "◈",
		"codebase.search":  "◈",
		"symbols.find":     "◈",
		"agent.run":        "⧉",
		"web.fetch":        "◇",
		"web.search":       "◇",
		"browser.fetch":    "◇",
		"browser.search":   "◇",
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

func TestDisplayToolName(t *testing.T) {
	cases := map[string]string{
		"file.read": "Read file",
		"agent.run": "Run subagent",
		// agent.await is multipurpose (subagents, jobs, watches): the row
		// describes the action, not the target class.
		"agent.await": "Waiting…",
		// Unknown tools still fall through to the title-case fallback.
		"custom.box": "Custom box",
	}
	for name, want := range cases {
		if got := DisplayToolName(name); got != want {
			t.Errorf("DisplayToolName(%q) = %q, want %q", name, got, want)
		}
	}
}

// Grouped tool headings read as English. The old rule ("append s, unless it
// ends in s/x, then append es") produced "Run testses", "Find symbolses",
// "Apply patchs" and "Codebase searchs".
func TestPluralizeToolName(t *testing.T) {
	cases := map[string]string{
		// Self-plural: a grouped heading must not render "Waiting…s".
		"agent.await":      "Waiting…",
		"file.read":        "Read files",
		"file.write_patch": "Edit files",
		"patch.apply":      "Apply patches",
		"shell.run":        "Run commands",
		"test.run":         "Run tests",
		"symbols.find":     "Find symbols",
		"todos":            "Update todos",
		"codebase.search":  "Codebase searches",
		"web.fetch":        "Fetch pages",
		"git.status":       "Git status",
		"agent.run":        "Run subagents",
		// Unknown tools fall through to the regular rules.
		"custom.box":   "Custom boxes",
		"custom.entry": "Custom entries",
		"custom.thing": "Custom things",
	}
	for name, want := range cases {
		if got := pluralizeToolName(name); got != want {
			t.Errorf("pluralizeToolName(%q) = %q, want %q", name, got, want)
		}
	}
}
