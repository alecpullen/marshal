package skills

import (
	"strings"
	"testing"
)

func TestLoadSkillsIncludesBuiltInSDDAuthoringSkill(t *testing.T) {
	idx, err := LoadSkills(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatalf("LoadSkills: %v", err)
	}
	skill, ok := idx.Load("marshal-sdd-plan-authoring")
	if !ok {
		t.Fatal("built-in SDD authoring skill is missing")
	}
	if skill.Description == "" || !strings.Contains(skill.Body, "marshal.patch") {
		t.Fatalf("built-in skill is incomplete: %+v", skill)
	}
}

func TestProjectSkillOverridesBuiltInSDDAuthoringSkill(t *testing.T) {
	project := t.TempDir()
	writeSkillFile(t, project, "marshal-sdd-plan-authoring.md", skillContent(
		"marshal-sdd-plan-authoring", "project override"))

	idx, err := LoadSkills(t.TempDir(), project)
	if err != nil {
		t.Fatalf("LoadSkills: %v", err)
	}
	skill, ok := idx.Load("marshal-sdd-plan-authoring")
	if !ok || skill.Description != "project override" {
		t.Fatalf("skill = %+v, want project override", skill)
	}
}

func TestLoadSkillsIncludesBuiltInExecutingPlansSkill(t *testing.T) {
	idx, err := LoadSkills(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatalf("LoadSkills: %v", err)
	}
	skill, ok := idx.Load("marshal-executing-plans")
	if !ok {
		t.Fatal("built-in executing-plans skill is missing")
	}
	if skill.Description == "" || !strings.Contains(skill.Body, "Commit after each verified task") {
		t.Fatalf("built-in skill is incomplete: %+v", skill)
	}
}

func TestLoadSkillsIncludesBuiltInWritingPlansSkill(t *testing.T) {
	idx, err := LoadSkills(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatalf("LoadSkills: %v", err)
	}
	skill, ok := idx.Load("marshal-writing-plans")
	if !ok {
		t.Fatal("built-in writing-plans skill is missing")
	}
	if skill.Description == "" || !strings.Contains(skill.Body, "marshal-executing-plans") {
		t.Fatalf("built-in skill is incomplete: %+v", skill)
	}
}

func TestProjectSkillOverridesBuiltInWritingPlansSkill(t *testing.T) {
	project := t.TempDir()
	writeSkillFile(t, project, "marshal-writing-plans.md", skillContent(
		"marshal-writing-plans", "project override"))

	idx, err := LoadSkills(t.TempDir(), project)
	if err != nil {
		t.Fatalf("LoadSkills: %v", err)
	}
	skill, ok := idx.Load("marshal-writing-plans")
	if !ok || skill.Description != "project override" {
		t.Fatalf("skill = %+v, want project override", skill)
	}
}

func TestProjectSkillOverridesBuiltInExecutingPlansSkill(t *testing.T) {
	project := t.TempDir()
	writeSkillFile(t, project, "marshal-executing-plans.md", skillContent(
		"marshal-executing-plans", "project override"))

	idx, err := LoadSkills(t.TempDir(), project)
	if err != nil {
		t.Fatalf("LoadSkills: %v", err)
	}
	skill, ok := idx.Load("marshal-executing-plans")
	if !ok || skill.Description != "project override" {
		t.Fatalf("skill = %+v, want project override", skill)
	}
}

func TestBuiltInSkillsAllLoad(t *testing.T) {
	idx := NewIndex()
	if err := loadBuiltIns(idx); err != nil {
		t.Fatalf("loadBuiltIns: %v", err)
	}
	want := []string{
		"brainstorming",
		"dispatching-parallel-agents",
		"marshal-executing-plans",
		"marshal-sdd-plan-authoring",
		"marshal-writing-plans",
		"systematic-debugging",
		"test-driven-development",
		"using-skills",
		"verification-before-completion",
		"work-decomposition",
	}
	for _, name := range want {
		sk, ok := idx.Load(name)
		if !ok {
			t.Errorf("built-in %q not registered", name)
			continue
		}
		if sk.Description == "" {
			t.Errorf("built-in %q has no description", name)
		}
		if strings.TrimSpace(sk.Body) == "" {
			t.Errorf("built-in %q has an empty body", name)
		}
	}
	if got := len(idx.List()); got != len(want) {
		t.Errorf("built-in count = %d, want %d", got, len(want))
	}
}

// A user-installed skill of the same name must win: built-ins load first and
// the index is keyed by name, so a later Set overwrites.
func TestUserSkillOverridesBuiltIn(t *testing.T) {
	idx := NewIndex()
	if err := loadBuiltIns(idx); err != nil {
		t.Fatalf("loadBuiltIns: %v", err)
	}
	idx.Set("brainstorming", Skill{Name: "brainstorming", Description: "mine", Body: "custom"})
	sk, _ := idx.Load("brainstorming")
	if sk.Body != "custom" {
		t.Error("user-installed skill must override the built-in")
	}
}

// TestBuiltInSkillWorkflowContracts pins the workflow contracts of the
// rewritten builtin skills so future edits can't silently drop the artifact
// paths, the interrogation/approval gates, or the verbatim-encoding rule.
// Substrings are matched case-insensitively against the embedded skill bodies.
func TestBuiltInSkillWorkflowContracts(t *testing.T) {
	idx := NewIndex()
	if err := loadBuiltIns(idx); err != nil {
		t.Fatalf("loadBuiltIns: %v", err)
	}

	body := func(name string) string {
		sk, ok := idx.Load(name)
		if !ok {
			t.Fatalf("built-in %q not registered", name)
		}
		return strings.ToLower(sk.Body)
	}

	// brainstorming: interrogation, spec artifact, review gates.
	brainstorming := body("brainstorming")
	for _, want := range []string{
		"docs/specs/",
		"question.ask",
		"one question per call",
		"user review",
		"self-review",
	} {
		if !strings.Contains(brainstorming, want) {
			t.Errorf("brainstorming body missing %q", want)
		}
	}

	// marshal-writing-plans: plan artifact, hybrid encoding, approval gate.
	writingPlans := body("marshal-writing-plans")
	for _, want := range []string{
		"docs/plans/",
		"verbatim",
		"anchor",
		"approval",
	} {
		if !strings.Contains(writingPlans, want) {
			t.Errorf("marshal-writing-plans body missing %q", want)
		}
	}

	// marshal-executing-plans: verbatim application rule.
	executingPlans := body("marshal-executing-plans")
	if !strings.Contains(executingPlans, "verbatim") {
		t.Error("marshal-executing-plans body missing \"verbatim\"")
	}

	// using-skills: chain description references the artifacts and encoding.
	usingSkills := body("using-skills")
	for _, want := range []string{
		"docs/specs/",
		"docs/plans/",
		"verbatim",
	} {
		if !strings.Contains(usingSkills, want) {
			t.Errorf("using-skills body missing %q", want)
		}
	}
}
