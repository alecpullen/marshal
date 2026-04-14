package skills_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/alecpullen/marshal/internal/skills"
)

func TestRegistry_RegisterAndFind(t *testing.T) {
	r := skills.New()
	s := &skills.Skill{Name: "test", Trigger: "/test", Description: "write tests"}
	if err := r.Register(s); err != nil {
		t.Fatal(err)
	}

	got, ok := r.Find("/test")
	if !ok {
		t.Fatal("expected to find /test")
	}
	if got.Name != "test" {
		t.Errorf("name: got %q", got.Name)
	}
}

func TestRegistry_FindMissing(t *testing.T) {
	r := skills.New()
	_, ok := r.Find("/missing")
	if ok {
		t.Error("expected not found")
	}
}

func TestRegistry_NilFind(t *testing.T) {
	var r *skills.Registry
	_, ok := r.Find("/anything")
	if ok {
		t.Error("nil registry should always return not found")
	}
}

func TestRegistry_DuplicateTrigger(t *testing.T) {
	r := skills.New()
	_ = r.Register(&skills.Skill{Name: "a", Trigger: "/dup"})
	err := r.Register(&skills.Skill{Name: "b", Trigger: "/dup"})
	if err == nil {
		t.Error("expected error on duplicate trigger")
	}
}

func TestRegistry_TriggerMustStartWithSlash(t *testing.T) {
	r := skills.New()
	err := r.Register(&skills.Skill{Name: "bad", Trigger: "noslash"})
	if err == nil {
		t.Error("expected error for trigger without '/'")
	}
}

func TestRegistry_EmptyTrigger(t *testing.T) {
	r := skills.New()
	err := r.Register(&skills.Skill{Name: "no-trigger"})
	if err == nil {
		t.Error("expected error for empty trigger")
	}
}

func TestRegistry_All(t *testing.T) {
	r := skills.New()
	_ = r.Register(&skills.Skill{Name: "a", Trigger: "/a"})
	_ = r.Register(&skills.Skill{Name: "b", Trigger: "/b"})
	if len(r.All()) != 2 {
		t.Errorf("expected 2 skills, got %d", len(r.All()))
	}
}

func TestLoad_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	r, err := skills.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.All()) != 0 {
		t.Errorf("expected 0 skills from empty dir, got %d", len(r.All()))
	}
}

func TestLoad_MissingDir(t *testing.T) {
	r, err := skills.Load("/nonexistent/path/skills")
	if err != nil {
		t.Fatalf("missing dir should not error, got: %v", err)
	}
	if len(r.All()) != 0 {
		t.Errorf("expected 0 skills")
	}
}

func TestLoad_ValidTOML(t *testing.T) {
	dir := t.TempDir()
	content := `
name        = "test"
description = "Write or update tests"
trigger     = "/test"

[executor]
system_extra = "Focus on writing tests."

[critic]
system_extra = "PASS only if tests are comprehensive."
`
	if err := os.WriteFile(filepath.Join(dir, "test.toml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	r, err := skills.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.All()) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(r.All()))
	}
	s, ok := r.Find("/test")
	if !ok {
		t.Fatal("expected to find /test")
	}
	if s.Name != "test" {
		t.Errorf("name: got %q", s.Name)
	}
	if s.Executor.SystemExtra != "Focus on writing tests." {
		t.Errorf("executor extra: got %q", s.Executor.SystemExtra)
	}
	if s.Critic.SystemExtra != "PASS only if tests are comprehensive." {
		t.Errorf("critic extra: got %q", s.Critic.SystemExtra)
	}
}

func TestLoad_ReadOnlySkill(t *testing.T) {
	dir := t.TempDir()
	content := `
name        = "Security Audit"
description = "Review code for security vulnerabilities"
trigger     = "/audit"
read_only   = true

[executor]
system_extra = "Check for security issues."
`
	if err := os.WriteFile(filepath.Join(dir, "audit.toml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	r, err := skills.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	s, ok := r.Find("/audit")
	if !ok {
		t.Fatal("expected to find /audit")
	}
	if !s.ReadOnly {
		t.Error("expected ReadOnly to be true")
	}
}

func TestLoad_InvalidTOML(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bad.toml"), []byte("not valid toml [[["), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := skills.Load(dir)
	if err == nil {
		t.Error("expected error for invalid TOML")
	}
}

func TestLoad_NonTOMLIgnored(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "readme.md"), []byte("# skills"), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := skills.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.All()) != 0 {
		t.Errorf("non-toml files should be ignored, got %d skills", len(r.All()))
	}
}

func TestLoadBuiltins(t *testing.T) {
	r := skills.New()
	if err := skills.LoadBuiltins(r); err != nil {
		t.Fatalf("LoadBuiltins failed: %v", err)
	}

	// Should have 3 built-in skills
	all := r.All()
	if len(all) != 3 {
		t.Errorf("expected 3 built-in skills, got %d", len(all))
	}

	// Check for specific triggers
	expected := []string{"/schema", "/audit", "/testgen"}
	for _, trigger := range expected {
		if _, ok := r.Find(trigger); !ok {
			t.Errorf("expected to find built-in skill %q", trigger)
		}
	}

	// Security audit should be read-only
	if audit, ok := r.Find("/audit"); ok {
		if !audit.ReadOnly {
			t.Error("expected /audit skill to have ReadOnly=true")
		}
	}
}

func TestLoadBuiltins_UserOverride(t *testing.T) {
	r := skills.New()

	// Load built-ins first
	if err := skills.LoadBuiltins(r); err != nil {
		t.Fatalf("LoadBuiltins failed: %v", err)
	}

	// Register a skill with same trigger (simulating user override)
	userSkill := &skills.Skill{Name: "User Schema", Trigger: "/schema"}
	err := r.Register(userSkill)
	// Should get duplicate error, but that's expected behavior
	if err == nil {
		t.Error("expected duplicate trigger error when overriding built-in")
	}

	// Built-in should still be there
	s, ok := r.Find("/schema")
	if !ok {
		t.Error("expected /schema to still exist after failed override")
	}
	if s.Name == "User Schema" {
		t.Error("built-in should not be replaced by user skill in this flow")
	}
}
