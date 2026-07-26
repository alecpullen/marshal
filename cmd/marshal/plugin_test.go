package main

import (
	"context"
	"os"
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
