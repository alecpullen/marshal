package plugins_test

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"marshal/internal/agent"
	"marshal/internal/plugins"
	"marshal/internal/skills"
)

// TestPluginSkillReachesSystemPrompt installs a fixture plugin through the
// real installer + lockfile path, loads the store the way the runtime
// does, and asserts the plugin's skill is advertised in the system prompt.
func TestPluginSkillReachesSystemPrompt(t *testing.T) {
	ctx := context.Background()

	// Build a source repo using the real git plumbing.
	src := filepath.Join(t.TempDir(), "repo")
	skillMD := "+++\nname = \"e2e-skill\"\ndescription = \"End to end skill\"\n+++\n\n# E2E\n"
	if err := os.MkdirAll(filepath.Join(src, "skills", "e2e"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "skills", "e2e", "SKILL.md"), []byte(skillMD), 0o644); err != nil {
		t.Fatal(err)
	}
	inst, err := plugins.NewInstaller()
	if err != nil {
		t.Fatalf("NewInstaller: %v", err)
	}
	initGit(t, src)

	// Install: clone, strip .git, copy into the store, pin the lockfile.
	tmpClone := filepath.Join(t.TempDir(), "clone")
	commit, err := inst.Clone(ctx, src, "", tmpClone)
	if err != nil {
		t.Fatalf("Clone: %v", err)
	}
	if err := os.RemoveAll(filepath.Join(tmpClone, ".git")); err != nil {
		t.Fatal(err)
	}
	store := t.TempDir()
	if err := plugins.CopyDir(tmpClone, filepath.Join(store, "e2e-plugin")); err != nil {
		t.Fatal(err)
	}
	hash, err := plugins.HashDir(filepath.Join(store, "e2e-plugin"))
	if err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(t.TempDir(), "plugins-lock.json")
	lf := &plugins.Lockfile{}
	lf.Upsert(plugins.LockEntry{Name: "e2e-plugin", Source: src, Commit: commit, ContentHash: hash})
	if err := lf.Write(lockPath); err != nil {
		t.Fatal(err)
	}

	// Load the way runtime.go does.
	idx := skills.NewIndex()
	if err := plugins.LoadStore(idx, store, lockPath, slog.Default()); err != nil {
		t.Fatalf("LoadStore: %v", err)
	}

	msg := agent.BuildSystemPrompt(agent.RoleGeneral, nil, idx, nil, false)
	if !strings.Contains(msg.Content, "## Available Skills") {
		t.Fatal("system prompt missing Available Skills section")
	}
	if !strings.Contains(msg.Content, "`e2e-skill`") {
		t.Fatal("system prompt does not advertise the plugin skill")
	}
}

func initGit(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "Test"},
		{"add", "-A"},
		{"commit", "-m", "initial"},
	} {
		cmd := execCommand(dir, args...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

func execCommand(dir string, args ...string) *exec.Cmd {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	return cmd
}
