package loop

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadSkills_ResolutionOrder(t *testing.T) {
	tmpDir := t.TempDir()

	// Create project skills
	marshalDir := filepath.Join(tmpDir, ".marshal", "skills")
	os.MkdirAll(marshalDir, 0755)
	os.WriteFile(filepath.Join(marshalDir, "go.toml"), []byte(`
name = "go-testing"
system_prompt_additions = "Write Go tests."
`), 0644)

	skills, err := LoadSkills(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(skills) != 1 {
		t.Errorf("expected 1 skill, got %d", len(skills))
	}
	if len(skills) > 0 && skills[0].Name != "go-testing" {
		t.Errorf("expected 'go-testing', got %s", skills[0].Name)
	}
}

func TestLoadSkills_InvalidKeyFails(t *testing.T) {
	tmpDir := t.TempDir()
	marshalDir := filepath.Join(tmpDir, ".marshal", "skills")
	os.MkdirAll(marshalDir, 0755)

	// Invalid: has 'custom_key' which is not allowed
	os.WriteFile(filepath.Join(marshalDir, "invalid.toml"), []byte(`
name = "invalid"
custom_key = "not allowed"
`), 0644)

	_, err := LoadSkills(tmpDir)
	// Should not error on custom_key, just ignore it
	// But name is required
	if err == nil {
		t.Error("expected validation to catch empty system_prompt_additions")
	}
}

func TestValidateSkill_EmptyName(t *testing.T) {
	s := Skill{Name: ""}
	err := validateSkill(s)
	if err == nil {
		t.Error("expected error for empty name")
	}
}

func TestValidateSkill_EmptyAdditions(t *testing.T) {
	s := Skill{Name: "test", Description: "A test skill"}
	err := validateSkill(s)
	if err != nil {
		t.Errorf("should allow empty additions with description: %v", err)
	}
}

func TestValidateSkill_Valid(t *testing.T) {
	s := Skill{
		Name:                  "valid-skill",
		Description:           "A valid skill",
		SystemPromptAdditions: "Do this.",
	}
	err := validateSkill(s)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}
