package swarm

import (
	"strings"
	"testing"
)

func TestRolePromptsEmbedSharedTaskState(t *testing.T) {
	ts := NewTaskState("fix flaky TestParse")
	ts.SetPlan([]string{"1. reproduce", "2. fix"})

	prompts := map[string]string{
		"planner":     plannerPrompt(ts),
		"scout":       scoutPrompt(ts, DefaultScoutFocuses[0]),
		"implementer": implementerPrompt(ts),
		"reviewer":    reviewerPrompt(ts),
	}
	for name, prompt := range prompts {
		if !strings.Contains(prompt, "Goal: fix flaky TestParse") {
			t.Errorf("%s prompt missing shared task state:\n%s", name, prompt)
		}
	}
	if !strings.Contains(prompts["scout"], DefaultScoutFocuses[0].Area) {
		t.Error("scout prompt missing its focus area")
	}
	if !strings.Contains(prompts["planner"], "numbered plan") {
		t.Error("planner prompt missing plan instruction")
	}
	if !strings.Contains(prompts["reviewer"], "git.diff") {
		t.Error("reviewer prompt should point at git.diff")
	}
}

func TestDefaultScoutFocusesCoverCodeTestsDocs(t *testing.T) {
	if len(DefaultScoutFocuses) != 3 {
		t.Fatalf("len(DefaultScoutFocuses) = %d, want 3", len(DefaultScoutFocuses))
	}
	areas := map[string]bool{}
	for _, f := range DefaultScoutFocuses {
		areas[f.Area] = true
	}
	for _, want := range []string{"code", "tests", "docs"} {
		if !areas[want] {
			t.Fatalf("missing scout focus %q", want)
		}
	}
}
