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

func TestTesterPromptDemandsVerdict(t *testing.T) {
	ts := NewTaskState("add a regression test")
	p := testerPrompt(ts)
	if !strings.Contains(p, "VERDICT:") {
		t.Error("tester prompt must instruct the model to emit a VERDICT line")
	}
	lower := strings.ToLower(p)
	if !strings.Contains(lower, "do not") && !strings.Contains(lower, "not modify") {
		t.Error("tester prompt must forbid modifying source")
	}
	if !strings.Contains(p, "Shared task state") {
		t.Error("tester prompt must embed rendered task state")
	}
}

func TestImplementerPromptMentionsTesterFeedback(t *testing.T) {
	ts := NewTaskState("fix the bug")
	p := implementerPrompt(ts)
	if !strings.Contains(strings.ToLower(p), "test") {
		t.Error("implementer prompt should reference tests / tester feedback")
	}
}

// TestTaskStateRendersTestFailures reproduces F-POL-68: the
// implementer prompt should see a structured test-failures block,
// not just free-form prose.
func TestTaskStateRendersTestFailures(t *testing.T) {
	ts := NewTaskState("fix tests")
	ts.AddTestFailure(TestFailure{
		File:    "internal/foo/foo_test.go",
		Line:    42,
		Test:    "TestFooDoesBar",
		Message: "expected 1, got 0",
	})
	rendered := ts.Render()
	if !strings.Contains(rendered, "Test failures:") {
		t.Errorf("rendered state should contain 'Test failures:' section, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "TestFooDoesBar") {
		t.Errorf("rendered state should contain the test name, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "internal/foo/foo_test.go:42") {
		t.Errorf("rendered state should contain file:line, got:\n%s", rendered)
	}
}
