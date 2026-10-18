package skills

import (
	"os"
	"path/filepath"
	"testing"
)

// projectSkills parses this repository's own project skills in .marshal/skills
// (tracked in git through an exception in the root .gitignore).
func projectSkills(t *testing.T) []Skill {
	t.Helper()
	dir := filepath.Join("..", "..", ".marshal", "skills")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read project skills dir: %v", err)
	}
	var out []Skill
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".md" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		skill, err := Parse(string(raw))
		if err != nil {
			t.Fatalf("parse %s: %v", e.Name(), err)
		}
		out = append(out, skill)
	}
	return out
}

func TestProjectSkillsParse(t *testing.T) {
	idx := NewIndex()
	for _, skill := range projectSkills(t) {
		idx.Set(skill.Name, skill)
	}
	for _, want := range []string{"debugging-marshal", "marshal-skill-authoring"} {
		if _, ok := idx.Load(want); !ok {
			t.Errorf("%s not found in project skills", want)
		}
	}
}

// TestProjectSkillsDoNotShadowBuiltIns guards against a project skill
// silently replacing a built-in of the same name: project skills load last,
// so a copied skill hides every later improvement to the built-in.
func TestProjectSkillsDoNotShadowBuiltIns(t *testing.T) {
	builtins := NewIndex()
	if err := loadBuiltIns(builtins); err != nil {
		t.Fatalf("loadBuiltIns: %v", err)
	}
	for _, skill := range projectSkills(t) {
		if _, ok := builtins.Load(skill.Name); ok {
			t.Errorf("project skill %q shadows the built-in of the same name; give it a distinct name", skill.Name)
		}
	}
}
