package tui

import "testing"

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
