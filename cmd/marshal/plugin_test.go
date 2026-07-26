package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"marshal/internal/plugins"
)

// chdirProject switches the test into a fresh temp working directory.
func chdirProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	return dir
}

func runPluginCmd(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	var out, errOut strings.Builder
	err := runPlugin(context.Background(), args, strings.NewReader(stdin), &out, &errOut)
	return out.String(), err
}

func TestPluginUnknownSubcommand(t *testing.T) {
	if _, err := runPluginCmd(t, "", "bogus"); err == nil {
		t.Fatal("expected error for unknown subcommand")
	}
}

func TestPluginListEmpty(t *testing.T) {
	chdirProject(t)
	out, err := runPluginCmd(t, "", "list", "--project")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out, "No project plugins installed.") {
		t.Fatalf("out = %q", out)
	}
}

func TestPluginRemoveNotInstalled(t *testing.T) {
	chdirProject(t)
	if _, err := runPluginCmd(t, "", "remove", "--project", "ghost"); err == nil {
		t.Fatal("expected error removing a plugin that is not installed")
	}
}

func TestPluginListAndRemove(t *testing.T) {
	work := chdirProject(t)

	// Seed a lockfile + store entry directly.
	lf := &plugins.Lockfile{}
	lf.Upsert(plugins.LockEntry{Name: "widgets", Source: "https://github.com/acme/widgets.git", Commit: "abc1234567890", ContentHash: "sha256:x"})
	if err := lf.Write(plugins.ProjectLockPath(work)); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(plugins.ProjectStoreDir(work), "widgets"), 0o755); err != nil {
		t.Fatal(err)
	}

	out, err := runPluginCmd(t, "", "list", "--project")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out, "widgets") || !strings.Contains(out, "abc12345") {
		t.Fatalf("list out = %q", out)
	}

	out, err = runPluginCmd(t, "", "remove", "--project", "widgets")
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if !strings.Contains(out, `Removed plugin "widgets"`) {
		t.Fatalf("remove out = %q", out)
	}

	out, err = runPluginCmd(t, "", "list", "--project")
	if err != nil {
		t.Fatalf("list after remove: %v", err)
	}
	if !strings.Contains(out, "No project plugins installed.") {
		t.Fatalf("out = %q", out)
	}
}

// initPluginRepo creates a git repo containing one skill bundle and
// returns its path. Mirrors the helper in internal/plugins tests; test
// helpers do not cross package boundaries, so it is duplicated here.
func initPluginRepo(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(filepath.Join(dir, "skills", "alpha"), 0o755); err != nil {
		t.Fatal(err)
	}
	content := "+++\nname = \"alpha\"\ndescription = \"Alpha skill\"\n+++\n\n# Alpha\n"
	if err := os.WriteFile(filepath.Join(dir, "skills", "alpha", "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test")
	run("add", "-A")
	run("commit", "-m", "initial")
	return dir
}

func TestPluginInstallConfirm(t *testing.T) {
	work := chdirProject(t)
	repo := initPluginRepo(t)

	out, err := runPluginCmd(t, "y\n", "install", "--project", "--name", "widgets", repo)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if !strings.Contains(out, "skill alpha — Alpha skill") {
		t.Fatalf("confirmation summary missing skill, out = %q", out)
	}
	if !strings.Contains(out, `Installed plugin "widgets"`) {
		t.Fatalf("out = %q", out)
	}

	// Lockfile records the install with a content hash.
	lf, err := plugins.ReadLockfile(plugins.ProjectLockPath(work))
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := lf.Find("widgets")
	if !ok {
		t.Fatal("lockfile missing widgets entry")
	}
	if entry.Commit == "" || !strings.HasPrefix(entry.ContentHash, "sha256:") {
		t.Fatalf("entry = %+v", entry)
	}

	// Files landed in the store, without .git.
	if _, err := os.Stat(filepath.Join(plugins.ProjectStoreDir(work), "widgets", "skills", "alpha", "SKILL.md")); err != nil {
		t.Fatalf("installed skill missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(plugins.ProjectStoreDir(work), "widgets", ".git")); !os.IsNotExist(err) {
		t.Fatal(".git should be stripped from the installed plugin")
	}
}

func TestPluginInstallDeclined(t *testing.T) {
	work := chdirProject(t)
	repo := initPluginRepo(t)

	out, err := runPluginCmd(t, "n\n", "install", "--project", repo)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if !strings.Contains(out, "Aborted.") {
		t.Fatalf("out = %q", out)
	}
	lf, err := plugins.ReadLockfile(plugins.ProjectLockPath(work))
	if err != nil {
		t.Fatal(err)
	}
	if len(lf.Plugins) != 0 {
		t.Fatal("declined install must not write the lockfile")
	}
	if _, err := os.Stat(plugins.ProjectStoreDir(work)); !os.IsNotExist(err) {
		t.Fatal("declined install must not create the store")
	}
}

func TestPluginInstallGitHubShorthandNormalization(t *testing.T) {
	chdirProject(t)
	// github: shorthand expands to an https URL; the clone itself fails
	// offline, which proves normalization happened by the error text.
	_, err := runPluginCmd(t, "y\n", "install", "--project", "github:acme/widgets")
	if err == nil {
		t.Fatal("expected clone failure for unreachable github source")
	}
	if !strings.Contains(err.Error(), "https://github.com/acme/widgets.git") {
		t.Fatalf("error should reference the normalized URL, got: %v", err)
	}
}

func TestPluginInstallInvalidName(t *testing.T) {
	chdirProject(t)
	repo := initPluginRepo(t)
	if _, err := runPluginCmd(t, "y\n", "install", "--project", "--name", "bad/name", repo); err == nil {
		t.Fatal("expected error for invalid --name")
	}
}
