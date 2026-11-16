package native

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/db"
	"marshal/internal/filetrack"
	"marshal/internal/tools/registry"
)

// AUD-02: a failed native write must not truncate away the only copy of a
// file, and the rollback evidence must survive the failure. These tests inject
// a failure into the temp-write and the commit (rename) steps and assert the
// destination keeps its pre-write bytes and mode, the session's backup holds
// the original content, no temp file is left behind, and the tool reports the
// failure.
//
// They mutate the package-level seam vars, so they must not call t.Parallel().
// No other test in this package runs in parallel.

const atomicOriginal = "package a\n\nvar x = 1\n"

// newAtomicRegistry builds a registry with a file tracker (file.write requires
// one to overwrite an existing file) and a workspace session state, so the
// backup evidence the write paths record can be asserted.
func newAtomicRegistry(t *testing.T, root string) (*registry.Registry, *session.State) {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "atomic.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if err := database.Migrate(); err != nil {
		t.Fatal(err)
	}
	state := session.New(config.Default(), root, time.Unix(100, 0), session.Persistence{})
	reg := registry.New()
	if err := RegisterAll(reg, Options{
		WorkspaceRoot: root,
		CommandRunner: &fakeRunner{},
		FileTracker:   filetrack.New(database.SQLDB(), "atomic"),
		SessionState:  state,
	}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	return reg, state
}

// writeAtomicFile creates an existing destination with an explicit mode, so a
// mode-preservation assertion is meaningful.
func writeAtomicFile(t *testing.T, root, name, content string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatalf("seed %s: %v", name, err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod %s: %v", name, err)
	}
	return path
}

// failTempWrite makes the payload write into the temp file fail after a
// partial write, the shape of ENOSPC / quota / EIO.
func failTempWrite(t *testing.T) {
	t.Helper()
	prev := writeTempContents
	writeTempContents = func(f *os.File, data []byte) error {
		if len(data) > 1 {
			_, _ = f.Write(data[:len(data)/2])
		}
		return errors.New("injected: no space left on device")
	}
	t.Cleanup(func() { writeTempContents = prev })
}

// failRename makes the commit step fail, so the destination is never replaced.
func failRename(t *testing.T) {
	t.Helper()
	prev := renameTempFile
	renameTempFile = func(oldPath, newPath string) error {
		return errors.New("injected: rename failed")
	}
	t.Cleanup(func() { renameTempFile = prev })
}

func assertFileUnchanged(t *testing.T, path, wantContent string, wantMode os.FileMode) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s after failed write: %v", path, err)
	}
	if string(got) != wantContent {
		t.Fatalf("%s content after failed write = %q, want the original %q", path, got, wantContent)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if info.Mode().Perm() != wantMode.Perm() {
		t.Fatalf("%s mode after failed write = %v, want %v", path, info.Mode().Perm(), wantMode.Perm())
	}
}

func assertNoTempFile(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir %s: %v", dir, err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), "marshal-tmp") {
			t.Fatalf("temp file left behind in %s: %s", dir, e.Name())
		}
	}
}

// assertBackupHolds asserts the session's rollback evidence names the file with
// its pre-write content and mode.
func assertBackupHolds(t *testing.T, st *session.State, rel, content string, mode os.FileMode) {
	t.Helper()
	for _, b := range st.Backup() {
		if b.Path != rel {
			continue
		}
		if !b.Exists {
			t.Fatalf("backup for %s records Exists=false; want the original recorded as existing", rel)
		}
		if b.Content != content {
			t.Fatalf("backup content for %s = %q, want the original %q", rel, b.Content, content)
		}
		if b.Mode.Perm() != mode.Perm() {
			t.Fatalf("backup mode for %s = %v, want %v", rel, b.Mode.Perm(), mode.Perm())
		}
		return
	}
	t.Fatalf("no rollback evidence for %s after the failed write; backup = %#v", rel, st.Backup())
}

// assertNoMissingFileBackup asserts a failed write did not store an
// Exists=false record for a file that was never created: RollbackBackup would
// then try to remove a nonexistent file and fail.
func assertNoMissingFileBackup(t *testing.T, st *session.State, rel string) {
	t.Helper()
	for _, b := range st.Backup() {
		if b.Path == rel && !b.Exists {
			t.Fatalf("backup records Exists=false for %s, which was never created", rel)
		}
	}
}

func atomicWriteArgs(t *testing.T, path, content string) string {
	t.Helper()
	b, err := json.Marshal(map[string]string{"path": path, "content": content})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	return string(b)
}

func atomicPatchArgs(t *testing.T, patchText string) string {
	t.Helper()
	b, err := json.Marshal(map[string]string{"patch": patchText})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	return string(b)
}

func TestFileWriteAtomicTempWriteFailureKeepsOriginal(t *testing.T) {
	root := t.TempDir()
	path := writeAtomicFile(t, root, "app.go", atomicOriginal, 0o640)
	reg, st := newAtomicRegistry(t, root)
	if _, err := invokeTool(t, reg, "file.read", `{"path":"app.go"}`); err != nil {
		t.Fatalf("file.read: %v", err)
	}

	failTempWrite(t)
	_, err := invokeTool(t, reg, "file.write", atomicWriteArgs(t, "app.go", "package a\n\nvar x = 2\n"))
	if err == nil {
		t.Fatal("file.write returned no error for a failed temp-file write")
	}

	assertFileUnchanged(t, path, atomicOriginal, 0o640)
	assertBackupHolds(t, st, "app.go", atomicOriginal, 0o640)
	assertNoTempFile(t, root)
}

func TestFileWriteAtomicRenameFailureKeepsOriginal(t *testing.T) {
	root := t.TempDir()
	path := writeAtomicFile(t, root, "app.go", atomicOriginal, 0o640)
	reg, st := newAtomicRegistry(t, root)
	if _, err := invokeTool(t, reg, "file.read", `{"path":"app.go"}`); err != nil {
		t.Fatalf("file.read: %v", err)
	}

	failRename(t)
	_, err := invokeTool(t, reg, "file.write", atomicWriteArgs(t, "app.go", "package a\n\nvar x = 2\n"))
	if err == nil {
		t.Fatal("file.write returned no error for a failed commit")
	}

	assertFileUnchanged(t, path, atomicOriginal, 0o640)
	assertBackupHolds(t, st, "app.go", atomicOriginal, 0o640)
	assertNoTempFile(t, root)
}

func TestFileWriteAtomicNewFileFailureLeavesNoFile(t *testing.T) {
	root := t.TempDir()
	reg, st := newAtomicRegistry(t, root)

	failTempWrite(t)
	_, err := invokeTool(t, reg, "file.write", atomicWriteArgs(t, "new.go", "package new\n"))
	if err == nil {
		t.Fatal("file.write returned no error for a failed temp-file write")
	}

	if _, statErr := os.Stat(filepath.Join(root, "new.go")); !os.IsNotExist(statErr) {
		t.Fatalf("new.go exists after a failed write (stat err = %v)", statErr)
	}
	assertNoTempFile(t, root)
	assertNoMissingFileBackup(t, st, "new.go")
}

func TestFileWriteAtomicSuccessPreservesModeAndLeavesNoTemp(t *testing.T) {
	root := t.TempDir()
	path := writeAtomicFile(t, root, "app.go", atomicOriginal, 0o640)
	reg, _ := newAtomicRegistry(t, root)
	if _, err := invokeTool(t, reg, "file.read", `{"path":"app.go"}`); err != nil {
		t.Fatalf("file.read: %v", err)
	}

	const want = "package a\n\nvar x = 2\n"
	res, err := invokeTool(t, reg, "file.write", atomicWriteArgs(t, "app.go", want))
	if err != nil {
		t.Fatalf("file.write: %v", err)
	}
	if len(res.FilesChanged) != 1 || res.FilesChanged[0] != "app.go" {
		t.Fatalf("FilesChanged = %#v, want [app.go]", res.FilesChanged)
	}
	if !strings.Contains(res.Content, "+var x = 2") {
		t.Fatalf("result content has no diff: %q", res.Content)
	}

	assertFileUnchanged(t, path, want, 0o640)
	assertNoTempFile(t, root)
}

const atomicPatch = "File: app.go\n<<<<<<< SEARCH\nvar x = 1\n=======\nvar x = 2\n>>>>>>> REPLACE"

func TestFileWritePatchAtomicTempWriteFailureKeepsOriginal(t *testing.T) {
	root := t.TempDir()
	path := writeAtomicFile(t, root, "app.go", atomicOriginal, 0o640)
	reg, st := newAtomicRegistry(t, root)
	if _, err := invokeTool(t, reg, "file.read", `{"path":"app.go"}`); err != nil {
		t.Fatalf("file.read: %v", err)
	}

	failTempWrite(t)
	_, err := invokeTool(t, reg, "file.write_patch", atomicPatchArgs(t, atomicPatch))
	if err == nil {
		t.Fatal("file.write_patch returned no error for a failed temp-file write")
	}

	assertFileUnchanged(t, path, atomicOriginal, 0o640)
	assertBackupHolds(t, st, "app.go", atomicOriginal, 0o640)
	assertNoTempFile(t, root)
}

func TestFileWritePatchAtomicRenameFailureKeepsOriginal(t *testing.T) {
	root := t.TempDir()
	path := writeAtomicFile(t, root, "app.go", atomicOriginal, 0o640)
	reg, st := newAtomicRegistry(t, root)
	if _, err := invokeTool(t, reg, "file.read", `{"path":"app.go"}`); err != nil {
		t.Fatalf("file.read: %v", err)
	}

	failRename(t)
	_, err := invokeTool(t, reg, "file.write_patch", atomicPatchArgs(t, atomicPatch))
	if err == nil {
		t.Fatal("file.write_patch returned no error for a failed commit")
	}

	assertFileUnchanged(t, path, atomicOriginal, 0o640)
	assertBackupHolds(t, st, "app.go", atomicOriginal, 0o640)
	assertNoTempFile(t, root)
}

func TestFileWritePatchAtomicNewFileFailureLeavesNoFile(t *testing.T) {
	root := t.TempDir()
	reg, st := newAtomicRegistry(t, root)

	failTempWrite(t)
	args := atomicPatchArgs(t, "File: new.go\n<<<<<<< SEARCH\n=======\npackage new\n>>>>>>> REPLACE")
	if _, err := invokeTool(t, reg, "file.write_patch", args); err == nil {
		t.Fatal("file.write_patch returned no error for a failed temp-file write")
	}

	if _, statErr := os.Stat(filepath.Join(root, "new.go")); !os.IsNotExist(statErr) {
		t.Fatalf("new.go exists after a failed patch (stat err = %v)", statErr)
	}
	assertNoTempFile(t, root)
	assertNoMissingFileBackup(t, st, "new.go")
}

func TestFileWritePatchAtomicSuccessPreservesModeAndLeavesNoTemp(t *testing.T) {
	root := t.TempDir()
	path := writeAtomicFile(t, root, "app.go", atomicOriginal, 0o640)
	reg, _ := newAtomicRegistry(t, root)
	if _, err := invokeTool(t, reg, "file.read", `{"path":"app.go"}`); err != nil {
		t.Fatalf("file.read: %v", err)
	}

	res, err := invokeTool(t, reg, "file.write_patch", atomicPatchArgs(t, atomicPatch))
	if err != nil {
		t.Fatalf("file.write_patch: %v", err)
	}
	if len(res.FilesChanged) != 1 || res.FilesChanged[0] != "app.go" {
		t.Fatalf("FilesChanged = %#v, want [app.go]", res.FilesChanged)
	}

	assertFileUnchanged(t, path, "package a\n\nvar x = 2\n", 0o640)
	assertNoTempFile(t, root)
}
