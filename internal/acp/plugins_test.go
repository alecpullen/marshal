package acp

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"marshal/internal/plugins"
)

func writeTestLockfile(t *testing.T, path string, entries ...plugins.LockEntry) {
	t.Helper()
	lf := &plugins.Lockfile{Plugins: entries}
	if err := lf.Write(path); err != nil {
		t.Fatalf("write lockfile: %v", err)
	}
}

func TestPluginsListIncludesProjectScopeOnlyWhenTrusted(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	globalLock := plugins.GlobalLockPath(home)
	projectLock := plugins.ProjectLockPath(work)
	writeTestLockfile(t, globalLock, plugins.LockEntry{Name: "global-plugin", Source: "github:a/b"})
	writeTestLockfile(t, projectLock, plugins.LockEntry{Name: "project-plugin", Source: "github:c/d"})

	untrusted := NewPluginsManager(PluginsManagerConfig{
		Lookup: func(sessionID string) (*PluginsRuntime, bool) {
			return &PluginsRuntime{HomeDir: home, WorkingDir: work, Trusted: false}, true
		},
	})
	raw, _ := json.Marshal(map[string]any{"sessionId": "sess_1"})
	res, err := untrusted.PluginsList(context.Background(), raw)
	if err != nil {
		t.Fatalf("PluginsList: %v", err)
	}
	result := res.(PluginsListResult)
	if len(result.Plugins) != 1 || result.Plugins[0].Name != "global-plugin" {
		t.Fatalf("untrusted PluginsList = %+v, want only global-plugin", result.Plugins)
	}

	trusted := NewPluginsManager(PluginsManagerConfig{
		Lookup: func(sessionID string) (*PluginsRuntime, bool) {
			return &PluginsRuntime{HomeDir: home, WorkingDir: work, Trusted: true}, true
		},
	})
	res, err = trusted.PluginsList(context.Background(), raw)
	if err != nil {
		t.Fatalf("PluginsList: %v", err)
	}
	result = res.(PluginsListResult)
	if len(result.Plugins) != 2 {
		t.Fatalf("trusted PluginsList = %+v, want 2 entries", result.Plugins)
	}
}

func TestPluginsListRequiresSessionID(t *testing.T) {
	mgr := NewPluginsManager(PluginsManagerConfig{
		Lookup: func(sessionID string) (*PluginsRuntime, bool) { return nil, false },
	})
	_, err := mgr.PluginsList(context.Background(), json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("PluginsList with no sessionId: got nil error, want an error")
	}
}

func TestPluginsListEmptyReturnsEmptySliceNotNil(t *testing.T) {
	mgr := NewPluginsManager(PluginsManagerConfig{
		Lookup: func(sessionID string) (*PluginsRuntime, bool) {
			return &PluginsRuntime{HomeDir: t.TempDir(), WorkingDir: t.TempDir(), Trusted: false}, true
		},
	})
	raw, _ := json.Marshal(map[string]any{"sessionId": "sess_1"})
	res, err := mgr.PluginsList(context.Background(), raw)
	if err != nil {
		t.Fatalf("PluginsList: %v", err)
	}
	if res.(PluginsListResult).Plugins == nil {
		t.Fatal("PluginsList.Plugins = nil, want a non-nil empty slice")
	}
}

// gitPlugin runs a git command in dir, failing the test on error. Mirrors
// internal/plugins/installer_test.go's unexported git() helper, which
// cannot be imported across packages.
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
// unavailable. Mirrors internal/plugins/installer_test.go's initRepo.
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

func TestPluginsInstallScanReturnsContentSummaryAndCommit(t *testing.T) {
	src := filepath.Join(t.TempDir(), "src")
	wantCommit := initPluginRepo(t, src, map[string]string{
		"commands/hello.md": testCommandFile,
	})

	mgr := NewPluginsManager(PluginsManagerConfig{
		Lookup: func(sessionID string) (*PluginsRuntime, bool) {
			return &PluginsRuntime{HomeDir: t.TempDir(), WorkingDir: t.TempDir(), Trusted: true}, true
		},
	})
	raw, _ := json.Marshal(PluginsInstallScanParams{SessionID: "sess_1", Source: src})
	res, err := mgr.PluginsInstallScan(context.Background(), raw)
	if err != nil {
		t.Fatalf("PluginsInstallScan: %v", err)
	}
	result, ok := res.(PluginsInstallScanResult)
	if !ok {
		t.Fatalf("PluginsInstallScan result type = %T, want PluginsInstallScanResult", res)
	}
	if result.ScanToken == "" {
		t.Fatal("PluginsInstallScan: empty ScanToken")
	}
	if result.Commit != wantCommit {
		t.Errorf("Commit = %q, want %q (must not be \"unknown\")", result.Commit, wantCommit)
	}
	if result.Contents.CommandCount != 1 {
		t.Errorf("Contents.CommandCount = %d, want 1", result.Contents.CommandCount)
	}
}

func TestPluginsInstallScanRejectsEmptySource(t *testing.T) {
	mgr := NewPluginsManager(PluginsManagerConfig{
		Lookup: func(sessionID string) (*PluginsRuntime, bool) { return nil, false },
	})
	raw, _ := json.Marshal(PluginsInstallScanParams{SessionID: "sess_1", Source: " "})
	_, err := mgr.PluginsInstallScan(context.Background(), raw)
	if err == nil {
		t.Fatal("PluginsInstallScan with blank source: got nil error, want an error")
	}
}

func TestPluginsInstallDiscardRemovesStagingWithoutInstalling(t *testing.T) {
	src := filepath.Join(t.TempDir(), "src")
	initPluginRepo(t, src, map[string]string{"commands/hello.md": testCommandFile})

	mgr := NewPluginsManager(PluginsManagerConfig{
		Lookup: func(sessionID string) (*PluginsRuntime, bool) {
			return &PluginsRuntime{HomeDir: t.TempDir(), WorkingDir: t.TempDir(), Trusted: true}, true
		},
	})
	previewRaw, _ := json.Marshal(PluginsInstallScanParams{SessionID: "sess_1", Source: src})
	previewRes, err := mgr.PluginsInstallScan(context.Background(), previewRaw)
	if err != nil {
		t.Fatalf("PluginsInstallScan: %v", err)
	}
	token := previewRes.(PluginsInstallScanResult).ScanToken
	tempDir := mgr.scans["sess_1"][token].tempDir

	discardRaw, _ := json.Marshal(PluginsInstallDiscardParams{SessionID: "sess_1", ScanToken: token})
	if _, err := mgr.PluginsInstallDiscard(context.Background(), discardRaw); err != nil {
		t.Fatalf("PluginsInstallDiscard: %v", err)
	}
	if _, err := os.Stat(tempDir); !os.IsNotExist(err) {
		t.Fatalf("temp dir %s still exists after discard", tempDir)
	}
}

func TestPluginsInstallConfirmWritesStoreAndLockfileWithRealCommit(t *testing.T) {
	src := filepath.Join(t.TempDir(), "src")
	wantCommit := initPluginRepo(t, src, map[string]string{"commands/hello.md": testCommandFile})
	home := t.TempDir()
	work := t.TempDir()

	mgr := NewPluginsManager(PluginsManagerConfig{
		Lookup: func(sessionID string) (*PluginsRuntime, bool) {
			return &PluginsRuntime{HomeDir: home, WorkingDir: work, Trusted: true}, true
		},
	})
	scanRaw, _ := json.Marshal(PluginsInstallScanParams{SessionID: "sess_1", Source: src})
	scanRes, err := mgr.PluginsInstallScan(context.Background(), scanRaw)
	if err != nil {
		t.Fatalf("PluginsInstallScan: %v", err)
	}
	scanResult := scanRes.(PluginsInstallScanResult)
	tempDir := mgr.scans["sess_1"][scanResult.ScanToken].tempDir

	confirmRaw, _ := json.Marshal(PluginsInstallConfirmParams{
		SessionID: "sess_1", ScanToken: scanResult.ScanToken, Scope: "global",
	})
	confirmRes, err := mgr.PluginsInstallConfirm(context.Background(), confirmRaw)
	if err != nil {
		t.Fatalf("PluginsInstallConfirm: %v", err)
	}
	if confirmRes.(PluginsInstallResult).Name != scanResult.Name {
		t.Errorf("PluginsInstallConfirm.Name = %q, want %q", confirmRes.(PluginsInstallResult).Name, scanResult.Name)
	}

	if _, err := os.Stat(filepath.Join(plugins.GlobalStoreDir(home), scanResult.Name, "commands", "hello.md")); err != nil {
		t.Fatalf("installed content missing: %v", err)
	}
	lf, err := plugins.ReadLockfile(plugins.GlobalLockPath(home))
	if err != nil {
		t.Fatalf("ReadLockfile: %v", err)
	}
	entry, found := lf.Find(scanResult.Name)
	if !found {
		t.Fatal("lockfile has no entry for the installed plugin")
	}
	if entry.Commit != wantCommit {
		t.Errorf("lockfile Commit = %q, want %q (must not be \"unknown\")", entry.Commit, wantCommit)
	}
	if entry.ContentHash == "" {
		t.Error("lockfile ContentHash is empty")
	}

	if _, err := os.Stat(tempDir); !os.IsNotExist(err) {
		t.Fatalf("temp dir %s still exists after confirm", tempDir)
	}
}

func TestPluginsInstallConfirmRejectsProjectScopeWhenUntrusted(t *testing.T) {
	src := filepath.Join(t.TempDir(), "src")
	initPluginRepo(t, src, map[string]string{"commands/hello.md": testCommandFile})

	mgr := NewPluginsManager(PluginsManagerConfig{
		Lookup: func(sessionID string) (*PluginsRuntime, bool) {
			return &PluginsRuntime{HomeDir: t.TempDir(), WorkingDir: t.TempDir(), Trusted: false}, true
		},
	})
	scanRaw, _ := json.Marshal(PluginsInstallScanParams{SessionID: "sess_1", Source: src})
	scanRes, err := mgr.PluginsInstallScan(context.Background(), scanRaw)
	if err != nil {
		t.Fatalf("PluginsInstallScan: %v", err)
	}
	token := scanRes.(PluginsInstallScanResult).ScanToken

	confirmRaw, _ := json.Marshal(PluginsInstallConfirmParams{SessionID: "sess_1", ScanToken: token, Scope: "project"})
	_, err = mgr.PluginsInstallConfirm(context.Background(), confirmRaw)
	if err == nil {
		t.Fatal("PluginsInstallConfirm with scope=project on an untrusted session: got nil error, want an error")
	}
}

func TestPluginsInstallConfirmRejectsUnknownToken(t *testing.T) {
	mgr := NewPluginsManager(PluginsManagerConfig{
		Lookup: func(sessionID string) (*PluginsRuntime, bool) {
			return &PluginsRuntime{HomeDir: t.TempDir(), WorkingDir: t.TempDir(), Trusted: true}, true
		},
	})
	raw, _ := json.Marshal(PluginsInstallConfirmParams{SessionID: "sess_1", ScanToken: "stg_nonexistent", Scope: "global"})
	_, err := mgr.PluginsInstallConfirm(context.Background(), raw)
	if err == nil {
		t.Fatal("PluginsInstallConfirm with unknown token: got nil error, want an error")
	}
}

func TestPluginsInstallConfirmRejectsInvalidScope(t *testing.T) {
	mgr := NewPluginsManager(PluginsManagerConfig{
		Lookup: func(sessionID string) (*PluginsRuntime, bool) { return nil, false },
	})
	raw, _ := json.Marshal(PluginsInstallConfirmParams{SessionID: "sess_1", ScanToken: "stg_x", Scope: "nowhere"})
	_, err := mgr.PluginsInstallConfirm(context.Background(), raw)
	if err == nil {
		t.Fatal("PluginsInstallConfirm with invalid scope: got nil error, want an error")
	}
}

func TestPluginsInstallDiscardRejectsUnknownToken(t *testing.T) {
	mgr := NewPluginsManager(PluginsManagerConfig{
		Lookup: func(sessionID string) (*PluginsRuntime, bool) { return nil, false },
	})
	raw, _ := json.Marshal(PluginsInstallDiscardParams{SessionID: "sess_1", ScanToken: "stg_nonexistent"})
	_, err := mgr.PluginsInstallDiscard(context.Background(), raw)
	if err == nil {
		t.Fatal("PluginsInstallDiscard with unknown token: got nil error, want an error")
	}
}

func TestPluginsRemoveDeletesStoreAndLockEntry(t *testing.T) {
	src := filepath.Join(t.TempDir(), "src")
	initPluginRepo(t, src, map[string]string{"commands/hello.md": testCommandFile})
	home := t.TempDir()
	work := t.TempDir()

	mgr := NewPluginsManager(PluginsManagerConfig{
		Lookup: func(sessionID string) (*PluginsRuntime, bool) {
			return &PluginsRuntime{HomeDir: home, WorkingDir: work, Trusted: true}, true
		},
	})
	scanRaw, _ := json.Marshal(PluginsInstallScanParams{SessionID: "sess_1", Source: src})
	scanRes, err := mgr.PluginsInstallScan(context.Background(), scanRaw)
	if err != nil {
		t.Fatalf("PluginsInstallScan: %v", err)
	}
	scanResult := scanRes.(PluginsInstallScanResult)
	confirmRaw, _ := json.Marshal(PluginsInstallConfirmParams{SessionID: "sess_1", ScanToken: scanResult.ScanToken, Scope: "global"})
	if _, err := mgr.PluginsInstallConfirm(context.Background(), confirmRaw); err != nil {
		t.Fatalf("PluginsInstallConfirm: %v", err)
	}

	removeRaw, _ := json.Marshal(PluginsRemoveParams{SessionID: "sess_1", Name: scanResult.Name, Scope: "global"})
	if _, err := mgr.PluginsRemove(context.Background(), removeRaw); err != nil {
		t.Fatalf("PluginsRemove: %v", err)
	}

	if _, err := os.Stat(filepath.Join(plugins.GlobalStoreDir(home), scanResult.Name)); !os.IsNotExist(err) {
		t.Fatalf("plugin store dir still exists after remove (stat err = %v)", err)
	}
	lf, err := plugins.ReadLockfile(plugins.GlobalLockPath(home))
	if err != nil {
		t.Fatalf("ReadLockfile: %v", err)
	}
	if _, found := lf.Find(scanResult.Name); found {
		t.Fatal("lockfile still has an entry after remove")
	}
}

func TestPluginsRemoveInvalidName(t *testing.T) {
	mgr := NewPluginsManager(PluginsManagerConfig{
		Lookup: func(sessionID string) (*PluginsRuntime, bool) {
			return &PluginsRuntime{HomeDir: t.TempDir()}, true
		},
	})
	for _, name := range []string{"../escape", "/abs/path", "a/b", ".", "..", ""} {
		raw, _ := json.Marshal(PluginsRemoveParams{SessionID: "sess_1", Name: name, Scope: "global"})
		_, err := mgr.PluginsRemove(context.Background(), raw)
		if err == nil {
			t.Errorf("PluginsRemove with name %q should error, got nil", name)
		}
	}
}

func TestPluginsRemoveRejectsProjectScopeWhenUntrusted(t *testing.T) {
	mgr := NewPluginsManager(PluginsManagerConfig{
		Lookup: func(sessionID string) (*PluginsRuntime, bool) {
			return &PluginsRuntime{HomeDir: t.TempDir(), WorkingDir: t.TempDir(), Trusted: false}, true
		},
	})
	raw, _ := json.Marshal(PluginsRemoveParams{SessionID: "sess_1", Name: "x", Scope: "project"})
	_, err := mgr.PluginsRemove(context.Background(), raw)
	if err == nil {
		t.Fatal("PluginsRemove with scope=project on an untrusted session: got nil error, want an error")
	}
}

func TestPluginsCloseSessionRemovesScannedTempDirs(t *testing.T) {
	src := filepath.Join(t.TempDir(), "src")
	initPluginRepo(t, src, map[string]string{"commands/hello.md": testCommandFile})

	mgr := NewPluginsManager(PluginsManagerConfig{
		Lookup: func(sessionID string) (*PluginsRuntime, bool) {
			return &PluginsRuntime{HomeDir: t.TempDir(), WorkingDir: t.TempDir(), Trusted: true}, true
		},
	})
	scanRaw, _ := json.Marshal(PluginsInstallScanParams{SessionID: "sess_close", Source: src})
	scanRes, err := mgr.PluginsInstallScan(context.Background(), scanRaw)
	if err != nil {
		t.Fatalf("PluginsInstallScan: %v", err)
	}
	token := scanRes.(PluginsInstallScanResult).ScanToken
	tempDir := mgr.scans["sess_close"][token].tempDir

	mgr.CloseSession("sess_close")

	if _, err := os.Stat(tempDir); !os.IsNotExist(err) {
		t.Fatalf("temp dir %s still exists after CloseSession", tempDir)
	}
}

func TestPluginsCloseSessionIsNoOpForSessionWithNoScans(t *testing.T) {
	mgr := NewPluginsManager(PluginsManagerConfig{
		Lookup: func(sessionID string) (*PluginsRuntime, bool) { return nil, false },
	})
	mgr.CloseSession("sess_never_scanned") // must not panic
}
