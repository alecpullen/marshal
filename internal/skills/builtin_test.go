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
