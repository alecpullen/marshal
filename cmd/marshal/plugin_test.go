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
	if !strings.Contains(out, "Executable content") {
		t.Fatalf("summary should always include the executable section, out = %q", out)
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

func TestPluginInstallGitHubShorthandExpanded(t *testing.T) {
	// github: shorthand should be split into source + ref and normalized to
	// the HTTPS clone URL without touching the network.
	source, ref, err := plugins.SplitSourceRef("github:acme/widgets@v1")
	if err != nil {
		t.Fatalf("split source ref: %v", err)
	}
	if ref != "v1" {
		t.Fatalf("ref = %q, want v1", ref)
	}
	cloneURL, name, err := plugins.NormalizeSource(source)
	if err != nil {
		t.Fatalf("normalize source: %v", err)
	}
	if cloneURL != "https://github.com/acme/widgets.git" {
		t.Fatalf("cloneURL = %q, want https://github.com/acme/widgets.git", cloneURL)
	}
	if name != "widgets" {
		t.Fatalf("name = %q, want widgets", name)
	}

	// Without an inline ref.
	source, ref, err = plugins.SplitSourceRef("github:acme/widgets")
	if err != nil {
		t.Fatalf("split source ref without ref: %v", err)
	}
	if ref != "" {
		t.Fatalf("ref = %q, want empty", ref)
	}
	cloneURL, name, err = plugins.NormalizeSource(source)
	if err != nil {
		t.Fatalf("normalize source without ref: %v", err)
	}
	if cloneURL != "https://github.com/acme/widgets.git" {
		t.Fatalf("cloneURL = %q, want https://github.com/acme/widgets.git", cloneURL)
	}
	if name != "widgets" {
		t.Fatalf("name = %q, want widgets", name)
	}
}

func TestPluginInstallInvalidName(t *testing.T) {
	chdirProject(t)
	repo := initPluginRepo(t)
	if _, err := runPluginCmd(t, "y\n", "install", "--project", "--name", "bad/name", repo); err == nil {
		t.Fatal("expected error for invalid --name")
	}
}

// commitToRepo adds a file to the repo and commits it.
func commitToRepo(t *testing.T, repo, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("add", "-A")
	run("commit", "-m", "update "+name)
}

func TestPluginUpdateAlreadyUpToDate(t *testing.T) {
	chdirProject(t)
	repo := initPluginRepo(t)
	if _, err := runPluginCmd(t, "y\n", "install", "--project", "--name", "widgets", repo); err != nil {
		t.Fatalf("install: %v", err)
	}

	out, err := runPluginCmd(t, "", "update", "--project", "widgets")
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if !strings.Contains(out, "already up to date") {
		t.Fatalf("out = %q", out)
	}
}

func TestPluginUpdateAppliesNewCommit(t *testing.T) {
	work := chdirProject(t)
	repo := initPluginRepo(t)
	if _, err := runPluginCmd(t, "y\n", "install", "--project", "--name", "widgets", repo); err != nil {
		t.Fatalf("install: %v", err)
	}
	lf, err := plugins.ReadLockfile(plugins.ProjectLockPath(work))
	if err != nil {
		t.Fatal(err)
	}
	before, _ := lf.Find("widgets")

	commitToRepo(t, repo, "NEW.md", "new content")

	out, err := runPluginCmd(t, "y\n", "update", "--project", "widgets")
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if !strings.Contains(out, `Updated plugin "widgets"`) {
		t.Fatalf("out = %q", out)
	}

	lf, err = plugins.ReadLockfile(plugins.ProjectLockPath(work))
	if err != nil {
		t.Fatal(err)
	}
	after, _ := lf.Find("widgets")
	if after.Commit == before.Commit {
		t.Fatal("commit should change after update")
	}
	if _, err := os.Stat(filepath.Join(plugins.ProjectStoreDir(work), "widgets", "NEW.md")); err != nil {
		t.Fatalf("updated files missing: %v", err)
	}
}

func TestPluginUpdateDeclined(t *testing.T) {
	work := chdirProject(t)
	repo := initPluginRepo(t)
	if _, err := runPluginCmd(t, "y\n", "install", "--project", "--name", "widgets", repo); err != nil {
		t.Fatalf("install: %v", err)
	}
	lf, _ := plugins.ReadLockfile(plugins.ProjectLockPath(work))
	before, _ := lf.Find("widgets")

	commitToRepo(t, repo, "NEW.md", "new content")

	out, err := runPluginCmd(t, "n\n", "update", "--project", "widgets")
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if !strings.Contains(out, "Skipped.") {
		t.Fatalf("out = %q", out)
	}
	lf, _ = plugins.ReadLockfile(plugins.ProjectLockPath(work))
	after, _ := lf.Find("widgets")
	if after.Commit != before.Commit {
		t.Fatal("declined update must not change the pinned commit")
	}
}

func TestPluginUpdateAll(t *testing.T) {
	chdirProject(t)
	repo := initPluginRepo(t)
	if _, err := runPluginCmd(t, "y\n", "install", "--project", "--name", "one", repo); err != nil {
		t.Fatalf("install one: %v", err)
	}
	if _, err := runPluginCmd(t, "y\n", "install", "--project", "--name", "two", repo); err != nil {
		t.Fatalf("install two: %v", err)
	}

	out, err := runPluginCmd(t, "", "update", "--project")
	if err != nil {
		t.Fatalf("update all: %v", err)
	}
	if !strings.Contains(out, "one is already up to date") || !strings.Contains(out, "two is already up to date") {
		t.Fatalf("out = %q", out)
	}
}

func TestPluginUpdateNotInstalled(t *testing.T) {
	chdirProject(t)
	if _, err := runPluginCmd(t, "", "update", "--project", "ghost"); err == nil {
		t.Fatal("expected error updating a plugin that is not installed")
	}
}

// initFullPluginRepo creates a git repo with skills, a command, hooks,
// and an MCP server.
func initFullPluginRepo(t *testing.T) string {
	t.Helper()
	dir := initPluginRepo(t) // skills/alpha/SKILL.md, committed
	files := map[string]string{
		"commands/review.md": "+++\nname = \"review\"\ndescription = \"Review the diff\"\n+++\n\nReview the current diff.\n",
		"hooks.toml":         "[[hooks.entries]]\nevent = \"pre_tool_use\"\nmatcher = \"shell.run\"\ncommand = \"./scripts/lint.sh\"\n",
		"mcp.toml":           "[mcp.servers.docs]\ncommand = \"npx\"\nargs = [\"-y\", \"@acme/docs-mcp\"]\n",
	}
	for name, content := range files {
		if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("add", "-A")
	run("commit", "-m", "add content")
	return dir
}

func TestPluginInstallShowsExecutableContent(t *testing.T) {
	chdirProject(t)
	repo := initFullPluginRepo(t)

	out, err := runPluginCmd(t, "y\n", "install", "--project", "--name", "full", repo)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	for _, want := range []string{
		"skill alpha — Alpha skill",
		"command /review — Review the diff",
		"hook pre_tool_use [shell.run]: ./scripts/lint.sh",
		"mcp server docs: npx -y @acme/docs-mcp",
		"Executable content",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("confirmation missing %q:\n%s", want, out)
		}
	}
}
