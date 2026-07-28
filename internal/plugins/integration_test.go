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
	loaded, err := plugins.LoadStore(store, lockPath, slog.Default())
	if err != nil {
		t.Fatalf("LoadStore: %v", err)
	}
	idx := skills.NewIndex()
	for _, p := range loaded {
		for _, s := range p.Contents.Skills {
			idx.Set(s.Name, s)
		}
	}

	msg := agent.BuildSystemPrompt(agent.RoleGeneral, nil, idx, nil, false)
	if !strings.Contains(msg.Content, "## Available Skills") {
		t.Fatal("system prompt missing Available Skills section")
	}
	if !strings.Contains(msg.Content, "`e2e-skill`") {
		t.Fatal("system prompt does not advertise the plugin skill")
	}
}

// TestFullBundleLoadsThroughStore verifies a plugin carrying every
// content type survives the install layout + lockfile + verification
// path with all contents intact.
func TestFullBundleLoadsThroughStore(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "full")
	files := map[string]string{
		"plugin.toml":                  "name = \"full\"\ndescription = \"Full bundle\"\n",
		"skills/alpha/SKILL.md":        "+++\nname = \"alpha\"\ndescription = \"Alpha\"\n+++\n\n# Alpha\n",
		"commands/review.md":           "+++\nname = \"review\"\ndescription = \"Review\"\n+++\n\nReview the diff.\n",
		"hooks.toml":                   "[[hooks.entries]]\nevent = \"turn_end\"\ncommand = \"./notify.sh\"\n",
		"mcp.toml":                     "[mcp.servers.docs]\ncommand = \"npx\"\n",
		"skills/alpha/references/x.md": "companion file\n",
	}
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Pin it exactly like the CLI installer does.
	hash, err := plugins.HashDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	store := t.TempDir()
	if err := plugins.CopyDir(dir, filepath.Join(store, "full")); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(t.TempDir(), "plugins-lock.json")
	lf := &plugins.Lockfile{}
	lf.Upsert(plugins.LockEntry{Name: "full", Source: "test", Commit: "abc123", ContentHash: hash})
	if err := lf.Write(lockPath); err != nil {
		t.Fatal(err)
	}

	loaded, err := plugins.LoadStore(store, lockPath, slog.Default())
	if err != nil {
		t.Fatalf("LoadStore: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("loaded = %v, want 1 plugin", loaded)
	}
	c := loaded[0].Contents
	if !c.HasManifest || c.Manifest.Name != "full" {
		t.Fatalf("manifest = %+v", c.Manifest)
	}
	if len(c.Skills) != 1 || len(c.Commands) != 1 || len(c.Hooks) != 1 || len(c.MCPServers) != 1 {
		t.Fatalf("contents = %+v", c)
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
