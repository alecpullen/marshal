package plugins

import (
	"path/filepath"
	"testing"
)

const testSkillMD = "+++\nname = \"alpha\"\ndescription = \"Alpha skill\"\n+++\n\n# Alpha\n\nDo alpha things.\n"

// writePluginSkill writes skills/<bundle>/SKILL.md under dir.
func writePluginSkill(t *testing.T, dir, bundle, content string) {
	t.Helper()
	writeFile(t, filepath.Join(dir, "skills", bundle, "SKILL.md"), content)
}

func TestScanBundleFindsSkills(t *testing.T) {
	dir := t.TempDir()
	writePluginSkill(t, dir, "alpha", testSkillMD)
	writePluginSkill(t, dir, "beta", "+++\nname = \"beta\"\ndescription = \"Beta skill\"\nrisk = \"write\"\n+++\n\nBeta body.\n")

	skills, err := ScanBundle(dir)
	if err != nil {
		t.Fatalf("ScanBundle: %v", err)
	}
	if len(skills) != 2 {
		t.Fatalf("skills length = %d, want 2", len(skills))
	}
	byName := map[string]string{}
	for _, s := range skills {
		byName[s.Name] = s.Description
	}
	if byName["alpha"] != "Alpha skill" || byName["beta"] != "Beta skill" {
		t.Fatalf("skills = %v", byName)
	}
}

func TestScanBundleSkipsNonSkillDirsAndFiles(t *testing.T) {
	dir := t.TempDir()
	writePluginSkill(t, dir, "alpha", testSkillMD)
	// A directory without SKILL.md, and a stray file under skills/.
	writeFile(t, filepath.Join(dir, "skills", "docs", "notes.md"), "not a skill")
	writeFile(t, filepath.Join(dir, "skills", "stray.md"), "not in a bundle dir")

	skills, err := ScanBundle(dir)
	if err != nil {
		t.Fatalf("ScanBundle: %v", err)
	}
	if len(skills) != 1 || skills[0].Name != "alpha" {
		t.Fatalf("skills = %v, want only [alpha]", skills)
	}
}

func TestScanBundleSkipsMalformedSkill(t *testing.T) {
	dir := t.TempDir()
	writePluginSkill(t, dir, "alpha", testSkillMD)
	writePluginSkill(t, dir, "bad", "# no frontmatter\n")

	skills, err := ScanBundle(dir)
	if err != nil {
		t.Fatalf("ScanBundle: %v", err)
	}
	if len(skills) != 1 || skills[0].Name != "alpha" {
		t.Fatalf("skills = %v, want only [alpha]", skills)
	}
}

func TestScanBundleNoSkillsDir(t *testing.T) {
	if _, err := ScanBundle(t.TempDir()); err == nil {
		t.Fatal("ScanBundle should error when skills/ is missing")
	}
}
