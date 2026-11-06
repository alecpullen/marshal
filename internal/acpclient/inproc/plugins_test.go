package inproc

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/plugins"
)

// gitPlugin runs a git command in dir, failing the test on error. Mirrors
// internal/acp/plugins_test.go's helper, which cannot be imported across
// packages.
func gitPlugin(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// initPluginRepo creates a git repo in dir with one commit containing the
// given files, and returns the commit hash. Skips the test if git is
// unavailable.
func initPluginRepo(t *testing.T, dir string, files map[string]string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git not available: %v", err)
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	gitPlugin(t, dir, "init")
	gitPlugin(t, dir, "config", "user.email", "test@example.com")
	gitPlugin(t, dir, "config", "user.name", "Test")
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	gitPlugin(t, dir, "add", "-A")
	gitPlugin(t, dir, "commit", "-m", "initial")
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v", err)
	}
	return strings.TrimSpace(string(out))
}

const testCommandFile = `+++
name = "hello"
description = "says hello"
+++
echo hello
`

// pluginState builds a session state over work.
func pluginState(t *testing.T, work string) *session.State {
	t.Helper()
	return session.New(config.Default(), work, time.Unix(100, 0), session.Persistence{SessionID: "s1"})
}

// pluginSession builds a session over work with home as its home directory.
func pluginSession(t *testing.T, home, work string) *Session {
	t.Helper()
	_, sess := New(Deps{State: pluginState(t, work), HomeDir: home})
	return sess
}

// stagedScan returns the staged scan behind token, failing the test when the
// session does not hold one.
func stagedScan(t *testing.T, s *Session, token string) *pluginScan {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	staged, ok := s.pluginScans[token]
	if !ok {
		t.Fatalf("no staged scan %q in %v", token, s.pluginScans)
	}
	return staged
}

// requireGone fails the test when path still exists.
func requireGone(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("%s still exists (stat err = %v)", path, err)
	}
}

// pluginRepo builds a repository with one command and returns its path and
// commit.
func pluginRepo(t *testing.T) (src, commit string) {
	t.Helper()
	src = filepath.Join(t.TempDir(), "hello-plugin")
	commit = initPluginRepo(t, src, map[string]string{"commands/hello.md": testCommandFile})
	return src, commit
}

// TestPluginScanReviewsAndInstalls pins the review an install consumes: the
// scanned command, the token naming the staged clone, the cloned commit, and
// the entry an install writes into the global lockfile.
func TestPluginScanReviewsAndInstalls(t *testing.T) {
	home := t.TempDir()
	sess := pluginSession(t, home, t.TempDir())
	ctx := context.Background()
	src, commit := pluginRepo(t)

	scan, err := sess.ScanPlugin(ctx, src)
	if err != nil {
		t.Fatalf("ScanPlugin: %v", err)
	}
	if scan.Token == "" {
		t.Fatal("ScanPlugin returned an empty token")
	}
	if scan.Commit != commit {
		t.Errorf("Commit = %q, want the repository's %q", scan.Commit, commit)
	}
	if scan.Source != src {
		t.Errorf("Source = %q, want the normalized %q", scan.Source, src)
	}
	if scan.Name != "hello-plugin" {
		t.Errorf("Name = %q, want the derived hello-plugin", scan.Name)
	}
	if len(scan.Contents.Commands) != 1 {
		t.Fatalf("Commands = %+v, want the scanned one", scan.Contents.Commands)
	}
	if got := scan.Contents.Commands[0]; got.Name != "hello" || got.Description != "says hello" {
		t.Errorf("Commands[0] = %+v, want hello/says hello", got)
	}
	parent := stagedScan(t, sess, scan.Token).parent

	if err := sess.InstallPlugin(ctx, scan.Token, "global"); err != nil {
		t.Fatalf("InstallPlugin: %v", err)
	}
	entries, err := sess.Plugins(ctx)
	if err != nil {
		t.Fatalf("Plugins: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("Plugins = %+v, want the one installed plugin", entries)
	}
	entry := entries[0]
	if entry.Name != "hello-plugin" || entry.Scope != "global" || entry.Commit != commit {
		t.Errorf("Plugins[0] = %+v, want hello-plugin in global scope at %s", entry, commit)
	}
	// The install consumed the staged clone, parent and all.
	requireGone(t, parent)
}

// TestPluginInstallRejectsAChangedClone pins the review's hash check: a clone
// rewritten after the scan installs nothing, and the staged parent is gone
// either way.
func TestPluginInstallRejectsAChangedClone(t *testing.T) {
	home := t.TempDir()
	sess := pluginSession(t, home, t.TempDir())
	ctx := context.Background()
	src, _ := pluginRepo(t)

	scan, err := sess.ScanPlugin(ctx, src)
	if err != nil {
		t.Fatalf("ScanPlugin: %v", err)
	}
	staged := stagedScan(t, sess, scan.Token)
	// Replace the reviewed command with a different one behind the review's
	// back: the staged copy no longer hashes as it did when it was scanned.
	changed := filepath.Join(staged.cloneDir, "commands", "hello.md")
	if err := os.WriteFile(changed, []byte("+++\nname = \"hello\"\n+++\necho changed\n"), 0644); err != nil {
		t.Fatalf("rewrite the staged clone: %v", err)
	}

	if err := sess.InstallPlugin(ctx, scan.Token, "global"); err == nil {
		t.Fatal("InstallPlugin of a changed clone: got nil error, want the hash check to refuse it")
	}
	entries, err := sess.Plugins(ctx)
	if err != nil {
		t.Fatalf("Plugins: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("Plugins = %+v, want nothing installed", entries)
	}
	requireGone(t, staged.parent)
}

// TestPluginScanWithAnEndedContextStagesNothing pins cancellation: a scan that
// cannot be reviewed stages no clone.
func TestPluginScanWithAnEndedContextStagesNothing(t *testing.T) {
	sess := pluginSession(t, t.TempDir(), t.TempDir())
	src, _ := pluginRepo(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := sess.ScanPlugin(ctx, src); err == nil {
		t.Fatal("ScanPlugin with a cancelled context: got nil error, want a failure")
	}
	sess.mu.Lock()
	staged := len(sess.pluginScans)
	sess.mu.Unlock()
	if staged != 0 {
		t.Errorf("%d scans staged after a cancelled context, want none", staged)
	}
}

// TestDiscardPluginScanDeletesTheClone pins the discard: the parent goes away
// and the token cannot be taken twice.
func TestDiscardPluginScanDeletesTheClone(t *testing.T) {
	sess := pluginSession(t, t.TempDir(), t.TempDir())
	ctx := context.Background()
	src, _ := pluginRepo(t)

	scan, err := sess.ScanPlugin(ctx, src)
	if err != nil {
		t.Fatalf("ScanPlugin: %v", err)
	}
	parent := stagedScan(t, sess, scan.Token).parent

	if err := sess.DiscardPluginScan(ctx, scan.Token); err != nil {
		t.Fatalf("DiscardPluginScan: %v", err)
	}
	requireGone(t, parent)

	err = sess.DiscardPluginScan(ctx, scan.Token)
	if err == nil {
		t.Fatal("second DiscardPluginScan: got nil error, want the unknown-token error")
	}
	if want := fmt.Sprintf("inproc: no plugin scan %q", scan.Token); err.Error() != want {
		t.Errorf("second DiscardPluginScan error = %q, want %q", err, want)
	}
}

// TestClosingDiscardsPluginScans pins the close path: a staged clone does not
// outlive the session that staged it.
func TestClosingDiscardsPluginScans(t *testing.T) {
	sess := pluginSession(t, t.TempDir(), t.TempDir())
	ctx := context.Background()
	src, _ := pluginRepo(t)

	scan, err := sess.ScanPlugin(ctx, src)
	if err != nil {
		t.Fatalf("ScanPlugin: %v", err)
	}
	parent := stagedScan(t, sess, scan.Token).parent

	if err := sess.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	requireGone(t, parent)
}

// TestPluginProjectScopeNeedsTrust pins the trust gate: a project-scope write
// on an untrusted session is refused, and the project lockfile is invisible.
func TestPluginProjectScopeNeedsTrust(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	lf := &plugins.Lockfile{Plugins: []plugins.LockEntry{{Name: "project-plugin", Source: "github:c/d"}}}
	if err := lf.Write(plugins.ProjectLockPath(work)); err != nil {
		t.Fatalf("write the project lockfile: %v", err)
	}
	sess := pluginSession(t, home, work)
	ctx := context.Background()
	const want = "project scope requires a trusted session"

	if err := sess.InstallPlugin(ctx, "scan-1", "project"); err == nil || err.Error() != want {
		t.Errorf("InstallPlugin project scope error = %v, want %q", err, want)
	}
	if err := sess.RemovePlugin(ctx, "project-plugin", "project"); err == nil || err.Error() != want {
		t.Errorf("RemovePlugin project scope error = %v, want %q", err, want)
	}
	entries, err := sess.Plugins(ctx)
	if err != nil {
		t.Fatalf("Plugins: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("Plugins = %+v, want no project entry on an untrusted session", entries)
	}
}

// TestRemovePluginDropsTheLockEntry pins the removal: the installed directory
// and the lockfile entry both go.
func TestRemovePluginDropsTheLockEntry(t *testing.T) {
	home := t.TempDir()
	sess := pluginSession(t, home, t.TempDir())
	ctx := context.Background()
	src, _ := pluginRepo(t)

	scan, err := sess.ScanPlugin(ctx, src)
	if err != nil {
		t.Fatalf("ScanPlugin: %v", err)
	}
	if err := sess.InstallPlugin(ctx, scan.Token, "global"); err != nil {
		t.Fatalf("InstallPlugin: %v", err)
	}
	storeDir := filepath.Join(plugins.GlobalStoreDir(home), scan.Name)
	if _, err := os.Stat(storeDir); err != nil {
		t.Fatalf("installed directory %s: %v", storeDir, err)
	}

	if err := sess.RemovePlugin(ctx, scan.Name, "global"); err != nil {
		t.Fatalf("RemovePlugin: %v", err)
	}
	requireGone(t, storeDir)
	entries, err := sess.Plugins(ctx)
	if err != nil {
		t.Fatalf("Plugins: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("Plugins = %+v, want nothing after the removal", entries)
	}
}
