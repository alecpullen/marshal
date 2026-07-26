package skills

import (
	"os"
	"path/filepath"
	"testing"
)

func writeSkillFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write skill file: %v", err)
	}
	return path
}

func skillContent(name, description string) string {
	return "+++\nname = \"" + name + "\"\ndescription = \"" + description + "\"\n+++\n\n# " + name + "\n\nBody for " + name + ".\n"
}

func TestLoadSkillsBothDirs(t *testing.T) {
	globalDir := t.TempDir()
	projectDir := t.TempDir()

	writeSkillFile(t, globalDir, "global-skill.md", skillContent("global-skill", "A global skill"))
	writeSkillFile(t, projectDir, "project-skill.md", skillContent("project-skill", "A project skill"))

	idx, err := LoadSkills(globalDir, projectDir)
	if err != nil {
		t.Fatalf("LoadSkills: %v", err)
	}

	list := idx.List()
	if len(list) != 2 {
		t.Fatalf("List length = %d, want 2", len(list))
	}

	names := make(map[string]bool)
	for _, s := range list {
		names[s.Name] = true
	}
	if !names["global-skill"] || !names["project-skill"] {
		t.Fatalf("missing expected skills in index: %v", names)
	}
}

func TestLoadSkillsProjectOverridesGlobal(t *testing.T) {
	globalDir := t.TempDir()
	projectDir := t.TempDir()

	writeSkillFile(t, globalDir, "same-name.md", skillContent("same-name", "Global version"))
	writeSkillFile(t, projectDir, "same-name.md", skillContent("same-name", "Project version"))

	idx, err := LoadSkills(globalDir, projectDir)
	if err != nil {
		t.Fatalf("LoadSkills: %v", err)
	}

	skill, ok := idx.Load("same-name")
	if !ok {
		t.Fatal("Load(same-name) returned false")
	}
	if skill.Description != "Project version" {
		t.Fatalf("Description = %q, want Project version", skill.Description)
	}
}

func TestLoadSkillsNeitherDirExists(t *testing.T) {
	idx, err := LoadSkills("/nonexistent/global", "/nonexistent/project")
	if err != nil {
		t.Fatalf("LoadSkills should not error for missing dirs: %v", err)
	}
	if len(idx.List()) != 0 {
		t.Fatal("expected empty index for missing dirs")
	}
}

func TestLoadSkillsOnlyProjectDir(t *testing.T) {
	projectDir := t.TempDir()
	writeSkillFile(t, projectDir, "proj.md", skillContent("proj", "Project only"))

	idx, err := LoadSkills("/nonexistent/global", projectDir)
	if err != nil {
		t.Fatalf("LoadSkills: %v", err)
	}
	if len(idx.List()) != 1 {
		t.Fatalf("List length = %d, want 1", len(idx.List()))
	}
}

func TestLoadSkillsSkipsNonMdFiles(t *testing.T) {
	projectDir := t.TempDir()
	writeSkillFile(t, projectDir, "skill.md", skillContent("skill", "A skill"))
	writeSkillFile(t, projectDir, "notes.txt", "not a skill file")
	writeSkillFile(t, projectDir, "README.md", "# Not a skill, no frontmatter")

	idx, err := LoadSkills("/nonexistent/global", projectDir)
	if err != nil {
		t.Fatalf("LoadSkills: %v", err)
	}

	list := idx.List()
	if len(list) != 1 {
		t.Fatalf("List length = %d, want 1", len(list))
	}
	if list[0].Name != "skill" {
		t.Fatalf("List[0].Name = %q, want skill", list[0].Name)
	}
}

func TestLoadSkillsMalformedFileSkipped(t *testing.T) {
	projectDir := t.TempDir()
	writeSkillFile(t, projectDir, "good.md", skillContent("good", "A valid skill"))
	writeSkillFile(t, projectDir, "bad.md", "# No frontmatter here\n\nJust text.")

	idx, err := LoadSkills("/nonexistent/global", projectDir)
	if err != nil {
		t.Fatalf("LoadSkills: %v", err)
	}

	list := idx.List()
	if len(list) != 1 {
		t.Fatalf("List length = %d, want 1", len(list))
	}
	if list[0].Name != "good" {
		t.Fatalf("List[0].Name = %q, want good", list[0].Name)
	}
}

func TestLoadSkillsBodyPreserved(t *testing.T) {
	projectDir := t.TempDir()
	content := "+++\nname = \"test-skill\"\ndescription = \"A test\"\n+++\n\n## Section 1\n\nSome markdown content.\n\n## Section 2\n\nMore content.\n"
	writeSkillFile(t, projectDir, "test.md", content)

	idx, err := LoadSkills("/nonexistent/global", projectDir)
	if err != nil {
		t.Fatalf("LoadSkills: %v", err)
	}

	skill, ok := idx.Load("test-skill")
	if !ok {
		t.Fatal("Load(test-skill) returned false")
	}
	if skill.Body != "## Section 1\n\nSome markdown content.\n\n## Section 2\n\nMore content.\n" {
		t.Fatalf("Body = %q", skill.Body)
	}
}

func writeBundleSkill(t *testing.T, dir, bundleName, content string) {
	t.Helper()
	bundleDir := filepath.Join(dir, bundleName)
	if err := os.MkdirAll(bundleDir, 0o755); err != nil {
		t.Fatalf("create bundle dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(bundleDir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write bundle SKILL.md: %v", err)
	}
}

func TestLoadSkillsBundleDir(t *testing.T) {
	projectDir := t.TempDir()
	writeBundleSkill(t, projectDir, "bundled", skillContent("bundled-skill", "A bundled skill"))

	idx, err := LoadSkills("/nonexistent/global", projectDir)
	if err != nil {
		t.Fatalf("LoadSkills: %v", err)
	}

	skill, ok := idx.Load("bundled-skill")
	if !ok {
		t.Fatal("Load(bundled-skill) returned false")
	}
	if skill.Description != "A bundled skill" {
		t.Fatalf("Description = %q, want A bundled skill", skill.Description)
	}
}

func TestLoadSkillsBundleAndFlatCoexist(t *testing.T) {
	projectDir := t.TempDir()
	writeSkillFile(t, projectDir, "flat.md", skillContent("flat-skill", "A flat skill"))
	writeBundleSkill(t, projectDir, "bundle", skillContent("bundle-skill", "A bundle skill"))

	idx, err := LoadSkills("/nonexistent/global", projectDir)
	if err != nil {
		t.Fatalf("LoadSkills: %v", err)
	}
	if len(idx.List()) != 2 {
		t.Fatalf("List length = %d, want 2", len(idx.List()))
	}
}

func TestLoadSkillsIgnoresDirsWithoutSkillMD(t *testing.T) {
	projectDir := t.TempDir()
	writeSkillFile(t, projectDir, "skill.md", skillContent("skill", "A skill"))
	if err := os.MkdirAll(filepath.Join(projectDir, "random-dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeSkillFile(t, filepath.Join(projectDir, "random-dir"), "notes.md", "# not a bundle\n")

	idx, err := LoadSkills("/nonexistent/global", projectDir)
	if err != nil {
		t.Fatalf("LoadSkills: %v", err)
	}
	if len(idx.List()) != 1 {
		t.Fatalf("List length = %d, want 1", len(idx.List()))
	}
}

func TestLoadSkillsMalformedBundleSkipped(t *testing.T) {
	projectDir := t.TempDir()
	writeSkillFile(t, projectDir, "good.md", skillContent("good", "A valid skill"))
	writeBundleSkill(t, projectDir, "bad", "# No frontmatter\n")

	idx, err := LoadSkills("/nonexistent/global", projectDir)
	if err != nil {
		t.Fatalf("LoadSkills: %v", err)
	}
	list := idx.List()
	if len(list) != 1 || list[0].Name != "good" {
		t.Fatalf("List = %v, want only [good]", list)
	}
}

func TestParseExported(t *testing.T) {
	skill, err := Parse(skillContent("parsed", "Parsed via export"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if skill.Name != "parsed" {
		t.Fatalf("Name = %q, want parsed", skill.Name)
	}
}
