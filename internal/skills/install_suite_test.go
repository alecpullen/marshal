package skills

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

// TestInstallBundleDirRecursive verifies that installBundleDir copies all
// files including those in subdirectories, not just top-level files.
func TestInstallBundleDirRecursive(t *testing.T) {
	src := t.TempDir()
	// Create a skill bundle with subdirectories and supporting files.
	writeFile(t, filepath.Join(src, "SKILL.md"), "---\nname: test-skill\n---\n# Test")
	writeFile(t, filepath.Join(src, "reference.md"), "reference content")
	writeFile(t, filepath.Join(src, "scripts", "helper.sh"), "#!/bin/sh\necho hi")
	writeFile(t, filepath.Join(src, "examples", "demo.md"), "demo content")

	targetDir := filepath.Join(t.TempDir(), "skills")
	dest, err := installBundleDir(src, targetDir, "test-skill")
	if err != nil {
		t.Fatalf("installBundleDir: %v", err)
	}

	// Verify SKILL.md was copied.
	if _, err := os.Stat(filepath.Join(dest, "SKILL.md")); err != nil {
		t.Errorf("SKILL.md not copied: %v", err)
	}
	// Verify top-level supporting file was copied.
	if _, err := os.Stat(filepath.Join(dest, "reference.md")); err != nil {
		t.Errorf("reference.md not copied: %v", err)
	}
	// Verify file in subdirectory was copied.
	if _, err := os.Stat(filepath.Join(dest, "scripts", "helper.sh")); err != nil {
		t.Errorf("scripts/helper.sh not copied: %v", err)
	}
	// Verify file in nested subdirectory was copied.
	if _, err := os.Stat(filepath.Join(dest, "examples", "demo.md")); err != nil {
		t.Errorf("examples/demo.md not copied: %v", err)
	}
}

// TestInstallLocalBundleDirWithSupportingFiles verifies that installing a
// local bundle directory preserves supporting files, not just SKILL.md.
func TestInstallLocalBundleDirWithSupportingFiles(t *testing.T) {
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "SKILL.md"), "---\nname: my-skill\n---\n# My Skill")
	writeFile(t, filepath.Join(src, "extra.md"), "extra content")
	writeFile(t, filepath.Join(src, "scripts", "run.sh"), "#!/bin/sh")

	targetDir := filepath.Join(t.TempDir(), "skills")
	dest, err := Install(nil, src, targetDir, "")
	if err != nil {
		t.Fatalf("Install: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dest, "SKILL.md")); err != nil {
		t.Errorf("SKILL.md not copied: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "extra.md")); err != nil {
		t.Errorf("extra.md not copied: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "scripts", "run.sh")); err != nil {
		t.Errorf("scripts/run.sh not copied: %v", err)
	}
}
