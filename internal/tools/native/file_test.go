package native

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/db"
	"marshal/internal/filetrack"
	"marshal/internal/tools/registry"
)

// TestFileWriteNilTrackerWarns verifies that file.write logs a warning
// when fileTracker is nil and the file already exists (TOOLS-MOD-F13).
func TestFileWriteNilTrackerWarns(t *testing.T) {
	dir := t.TempDir()
	existingPath := filepath.Join(dir, "exists.txt")
	os.WriteFile(existingPath, []byte("old"), 0644)

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))
	slog.SetDefault(logger)
	defer slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))

	ts := &toolSet{
		root: dir,
		// fileTracker is nil — the gap we're warning about
	}
	tool := ts.fileWriteTool()
	_, err := tool.Handler(context.Background(), registry.ToolCall{
		Name: "file.write",
		Args: json.RawMessage(`{"path":"exists.txt","content":"new"}`),
	})
	if err == nil {
		t.Fatal("expected error for existing file with nil tracker")
	}
	if !strings.Contains(logBuf.String(), "nil fileTracker") {
		t.Errorf("expected warning about nil fileTracker in log, got: %s", logBuf.String())
	}
}

func TestFileReadReadsWholeFile(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "README.md"), "one\ntwo\nthree\n")
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	result, err := invokeTool(t, reg, "file.read", `{"path":"README.md"}`)
	if err != nil {
		t.Fatalf("file.read returned error: %v", err)
	}
	if result.Content != "one\ntwo\nthree\n" {
		t.Fatalf("Content = %q", result.Content)
	}
	if !strings.Contains(result.Summary, "README.md") {
		t.Fatalf("Summary = %q, want path", result.Summary)
	}
}

func TestFileReadReadsLineRange(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "notes.txt"), "one\ntwo\nthree\nfour\n")
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	result, err := invokeTool(t, reg, "file.read", `{"path":"notes.txt","start_line":2,"end_line":3}`)
	if err != nil {
		t.Fatalf("file.read returned error: %v", err)
	}
	if result.Content != "two\nthree" {
		t.Fatalf("Content = %q, want selected lines", result.Content)
	}
}

func TestFileReadRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	_, err := invokeTool(t, reg, "file.read", `{"path":"../secret.txt"}`)
	if err == nil {
		t.Fatal("file.read traversal returned nil error")
	}
}

func TestFileReadRejectsInvalidRange(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "notes.txt"), "one\ntwo\n")
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	_, err := invokeTool(t, reg, "file.read", `{"path":"notes.txt","start_line":3,"end_line":2}`)
	if err == nil {
		t.Fatal("file.read invalid range returned nil error")
	}
}

func TestFilePageReadsFirstPage(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "notes.txt"), "one\ntwo\nthree\nfour\nfive\n")
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	result, err := invokeTool(t, reg, "file.page", `{"path":"notes.txt","page":1,"page_size":2}`)
	if err != nil {
		t.Fatalf("file.page returned error: %v", err)
	}
	if result.Content != "one\ntwo" {
		t.Fatalf("Content = %q, want first two lines", result.Content)
	}
	if !strings.Contains(result.Summary, "page 1") || !strings.Contains(result.Summary, "lines 1-2 of 5") {
		t.Fatalf("Summary = %q, want page and line info", result.Summary)
	}
}

func TestFilePageReadsSecondPage(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "notes.txt"), "one\ntwo\nthree\nfour\nfive\n")
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	result, err := invokeTool(t, reg, "file.page", `{"path":"notes.txt","page":2,"page_size":2}`)
	if err != nil {
		t.Fatalf("file.page returned error: %v", err)
	}
	if result.Content != "three\nfour" {
		t.Fatalf("Content = %q, want middle two lines", result.Content)
	}
	if !strings.Contains(result.Summary, "lines 3-4 of 5") {
		t.Fatalf("Summary = %q, want lines 3-4 of 5", result.Summary)
	}
}

func TestFilePageDefaultsPageSize(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "notes.txt"), "one\ntwo\nthree\n")
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	result, err := invokeTool(t, reg, "file.page", `{"path":"notes.txt","page":1}`)
	if err != nil {
		t.Fatalf("file.page returned error: %v", err)
	}
	if result.Content != "one\ntwo\nthree\n" {
		t.Fatalf("Content = %q, want all lines with default page size", result.Content)
	}
}

func TestFilePageRejectsPastEnd(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "notes.txt"), "one\ntwo\n")
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	_, err := invokeTool(t, reg, "file.page", `{"path":"notes.txt","page":5,"page_size":2}`)
	if err == nil {
		t.Fatal("file.page past end returned nil error")
	}
}

func TestFilePageRejectsInvalidPage(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "notes.txt"), "one\ntwo\n")
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	_, err := invokeTool(t, reg, "file.page", `{"path":"notes.txt","page":0}`)
	if err == nil {
		t.Fatal("file.page page 0 returned nil error")
	}
}

func TestFilePageReadsFileLargerThanOutputLimit(t *testing.T) {
	root := t.TempDir()
	var sb strings.Builder
	for i := 0; i < 20000; i++ {
		fmt.Fprintf(&sb, "line %05d\n", i)
	}
	writeFile(t, filepath.Join(root, "big.txt"), sb.String())

	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	// file.read should reject the whole file because it exceeds the default
	// per-tool output limit.
	if _, err := invokeTool(t, reg, "file.read", `{"path":"big.txt"}`); err == nil {
		t.Fatal("file.read should reject a file larger than max output bytes")
	}

	// file.page should still be able to page through it.
	result, err := invokeTool(t, reg, "file.page", `{"path":"big.txt","page":1,"page_size":3}`)
	if err != nil {
		t.Fatalf("file.page returned error: %v", err)
	}
	want := "line 00000\nline 00001\nline 00002"
	if result.Content != want {
		t.Fatalf("Content = %q, want %q", result.Content, want)
	}
	if !strings.Contains(result.Summary, "lines 1-3 of 20000") {
		t.Fatalf("Summary = %q, want lines 1-3 of 20000", result.Summary)
	}
}

func TestFileReadMissingFileSuggestsClosestPaths(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "README.md"), "hello\n")

	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()
	if err := database.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	projectID, err := database.GetOrCreateProject(root, "test")
	if err != nil {
		t.Fatalf("GetOrCreateProject: %v", err)
	}
	indexedAt := time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC)
	files := []db.FileIndex{
		{Path: "internal/app/main.go", Language: "go", Hash: "a", SizeBytes: 1, LastIndexedAt: indexedAt},
		{Path: "internal/db/files.go", Language: "go", Hash: "b", SizeBytes: 2, LastIndexedAt: indexedAt},
	}
	if err := database.SaveFileIndex(projectID, files); err != nil {
		t.Fatalf("SaveFileIndex: %v", err)
	}

	reg := registry.New()
	if err := RegisterAll(reg, Options{
		WorkspaceRoot: root,
		CommandRunner: &fakeRunner{},
		DB:            database,
		ProjectID:     projectID,
	}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	_, err = invokeTool(t, reg, "file.read", `{"path":"src/main.go"}`)
	if err == nil {
		t.Fatal("expected error for missing file")
	}
	errMsg := err.Error()
	if !strings.Contains(errMsg, "closest indexed paths") {
		t.Fatalf("error should mention closest indexed paths, got: %s", errMsg)
	}
	if !strings.Contains(errMsg, "internal/app/main.go") {
		t.Fatalf("error should suggest internal/app/main.go, got: %s", errMsg)
	}
}

func TestFileReadRefusesHugeFile(t *testing.T) {
	tmp := t.TempDir()
	big := filepath.Join(tmp, "big.txt")
	// Write a 100 MB file. The default maxOutputBytes in toolset is
	// much smaller (8 KB or so); we expect a clear error.
	if err := os.WriteFile(big, make([]byte, 100*1024*1024), 0644); err != nil {
		t.Fatalf("write big: %v", err)
	}

	reg := registry.New()
	if err := RegisterAll(reg, Options{
		WorkspaceRoot:  tmp,
		CommandRunner:  &fakeRunner{},
		MaxOutputBytes: 8 * 1024,
	}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	_, err := invokeTool(t, reg, "file.read", `{"path":"big.txt"}`)
	if err == nil {
		t.Fatal("expected error for huge file, got success")
	}
	if !strings.Contains(err.Error(), "too large") && !strings.Contains(err.Error(), "limit") {
		t.Fatalf("expected size-related error, got: %v", err)
	}
}

func writeFile(t *testing.T, path string, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

func TestFileWritePatchTool(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "app.go")
	orig := "package main\n\nfunc main() {\n\tprintln(\"hello\")\n}\n"
	if err := os.WriteFile(filePath, []byte(orig), 0644); err != nil {
		t.Fatalf("WriteFile error: %v", err)
	}

	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("RegisterAll error: %v", err)
	}

	args := `{"patch": "File: app.go\n<<<<<<< SEARCH\n\tprintln(\"hello\")\n=======\n\tprintln(\"patched\")\n>>>>>>> REPLACE"}`
	res, err := invokeTool(t, reg, "file.write_patch", args)
	if err != nil {
		t.Fatalf("handler failed: %v", err)
	}

	if res.Summary == "" {
		t.Fatal("expected non-empty summary")
	}
	if !reflect.DeepEqual(res.FilesChanged, []string{"app.go"}) {
		t.Fatalf("FilesChanged = %#v, want %#v", res.FilesChanged, []string{"app.go"})
	}

	// Verify file was patched
	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("read file failed: %v", err)
	}
	if !strings.Contains(string(data), "println(\"patched\")") {
		t.Fatalf("file content not patched: %s", string(data))
	}
}

func TestWritePatch_NewFileCreation(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "new.txt")

	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("RegisterAll error: %v", err)
	}

	// Patch with empty SEARCH block — signals new file creation.
	args := `{"patch": "File: new.txt\n<<<<<<< SEARCH\n=======\nhello\n>>>>>>> REPLACE"}`
	res, err := invokeTool(t, reg, "file.write_patch", args)
	if err != nil {
		t.Fatalf("write_patch failed: %v", err)
	}

	if res.Summary == "" {
		t.Fatal("expected non-empty summary")
	}
	if !reflect.DeepEqual(res.FilesChanged, []string{"new.txt"}) {
		t.Fatalf("FilesChanged = %#v, want %#v", res.FilesChanged, []string{"new.txt"})
	}

	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read created file failed: %v", err)
	}
	if string(data) != "hello" {
		t.Fatalf("file content = %q, want %q", string(data), "hello")
	}
}

func TestFileWritePatchRollbackIntegration(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "app.go")
	orig := "package main\r\n\r\nfunc main() {\r\n\tprintln(\"hello\")\r\n}\r\n"
	if err := os.WriteFile(filePath, []byte(orig), 0755); err != nil {
		t.Fatalf("WriteFile error: %v", err)
	}

	state := session.New(config.Default(), root, time.Unix(100, 0), session.Persistence{})

	reg := registry.New()
	if err := RegisterAll(reg, Options{
		WorkspaceRoot: root,
		CommandRunner: &fakeRunner{},
		SessionState:  state,
	}); err != nil {
		t.Fatalf("RegisterAll error: %v", err)
	}

	args := `{"patch": "File: app.go\n<<<<<<< SEARCH\n\tprintln(\"hello\")\n=======\n\tprintln(\"patched\")\n>>>>>>> REPLACE"}`
	_, err := invokeTool(t, reg, "file.write_patch", args)
	if err != nil {
		t.Fatalf("write_patch failed: %v", err)
	}

	// 1. Verify backup was saved in session state
	if !state.HasBackup() {
		t.Fatal("expected session state to contain backup after patch")
	}

	backup := state.Backup()
	if len(backup) != 1 || backup[0].Path != "app.go" || backup[0].Content != orig || backup[0].Mode != 0755 {
		t.Fatalf("unexpected backup contents/mode: %#v", backup)
	}

	// 2. Verify line endings were preserved as CRLF
	patchedData, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("failed to read patched file: %v", err)
	}
	if !strings.Contains(string(patchedData), "\r\n") {
		t.Fatal("expected CRLF line endings to be preserved in patched file")
	}

	// 3. Perform Rollback
	err = state.RollbackBackup()
	if err != nil {
		t.Fatalf("rollback failed: %v", err)
	}

	// 4. Verify file reverted completely including permissions and CRLF
	revertedData, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("failed to read reverted file: %v", err)
	}
	if string(revertedData) != orig {
		t.Fatalf("reverted file content mismatch: got %q, want %q", string(revertedData), orig)
	}

	info, err := os.Stat(filePath)
	if err != nil {
		t.Fatalf("failed to stat reverted file: %v", err)
	}
	if info.Mode() != 0755 {
		t.Fatalf("expected reverted permissions to be 0755, got %v", info.Mode())
	}
}

func TestWritePatch_AtomicOnConcurrentModification(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "test.txt")
	if err := os.WriteFile(filePath, []byte("v1\n"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	dbPath := filepath.Join(t.TempDir(), "filetrack.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()
	if err := database.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	ft := filetrack.New(database.SQLDB(), "test-session")

	reg := registry.New()
	if err := RegisterAll(reg, Options{
		WorkspaceRoot: root,
		CommandRunner: &fakeRunner{},
		FileTracker:   ft,
	}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	// Read the file first to register a read-time in the file tracker.
	_, err = invokeTool(t, reg, "file.read", `{"path":"test.txt"}`)
	if err != nil {
		t.Fatalf("file.read failed: %v", err)
	}

	// Launch a goroutine that modifies the file concurrently.
	done := make(chan struct{})
	go func() {
		defer close(done)
		time.Sleep(10 * time.Millisecond)
		if wErr := os.WriteFile(filePath, []byte("v1-modified\n"), 0644); wErr != nil {
			t.Logf("concurrent write failed: %v", wErr)
		}
	}()

	// Give the goroutine time to fire and modify the file before calling
	// write_patch. The goroutine fires at ~10ms; this sleep ensures it has
	// already modified the file so the tool's validate loop detects the change.
	time.Sleep(20 * time.Millisecond)

	// Patch v1 -> v2. The file was modified concurrently so the tool should
	// reject it with "changed on disk".
	args := `{"patch": "File: test.txt\n<<<<<<< SEARCH\nv1\n=======\nv2\n>>>>>>> REPLACE"}`
	_, err = invokeTool(t, reg, "file.write_patch", args)

	<-done // wait for the goroutine to finish

	if err == nil {
		t.Fatal("expected error for concurrent modification, got nil")
	}
	if !strings.Contains(err.Error(), "changed on disk") {
		t.Fatalf("error should mention 'changed on disk', got: %v", err)
	}
}

// The "changed on disk" error used to only tell the model to re-read the
// file, forcing a separate file.read round-trip before it could retry the
// patch. Live testing showed this pattern recurring during multi-step
// edits (a file changes from the agent's own earlier patch, then a later
// patch attempt against stale content fails) -- embedding the current
// content directly in the error lets the model retry immediately instead
// of spending an extra iteration on a follow-up read.
func TestWritePatch_ChangedOnDiskErrorIncludesCurrentContent(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "test.txt")
	if err := os.WriteFile(filePath, []byte("v1\n"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	dbPath := filepath.Join(t.TempDir(), "filetrack.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()
	if err := database.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	ft := filetrack.New(database.SQLDB(), "test-session")

	reg := registry.New()
	if err := RegisterAll(reg, Options{
		WorkspaceRoot: root,
		CommandRunner: &fakeRunner{},
		FileTracker:   ft,
	}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	if _, err := invokeTool(t, reg, "file.read", `{"path":"test.txt"}`); err != nil {
		t.Fatalf("file.read failed: %v", err)
	}

	// Modify the file AFTER the read, with a deterministic later mtime.
	time.Sleep(20 * time.Millisecond)
	if err := os.WriteFile(filePath, []byte("v1-modified-on-disk\n"), 0644); err != nil {
		t.Fatalf("WriteFile (modify): %v", err)
	}

	args := `{"patch": "File: test.txt\n<<<<<<< SEARCH\nv1\n=======\nv2\n>>>>>>> REPLACE"}`
	_, err = invokeTool(t, reg, "file.write_patch", args)
	if err == nil {
		t.Fatal("expected error for stale read, got nil")
	}
	if !strings.Contains(err.Error(), "changed on disk") {
		t.Fatalf("error should mention 'changed on disk', got: %v", err)
	}
	if !strings.Contains(err.Error(), "v1-modified-on-disk") {
		t.Fatalf("error should include the current file content so the model can retry without a separate read, got: %v", err)
	}
}

func TestFileWriteCreatesNewFile(t *testing.T) {
	root := t.TempDir()
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	res, err := invokeTool(t, reg, "file.write", `{"path":"new.txt","content":"hello\nworld\n"}`)
	if err != nil {
		t.Fatalf("file.write: %v", err)
	}
	if !reflect.DeepEqual(res.FilesChanged, []string{"new.txt"}) {
		t.Fatalf("FilesChanged = %#v, want [new.txt]", res.FilesChanged)
	}
	if !strings.Contains(res.Content, "new.txt") {
		t.Fatalf("diff content should reference the file, got %q", res.Content)
	}
	data, err := os.ReadFile(filepath.Join(root, "new.txt"))
	if err != nil {
		t.Fatalf("read created file: %v", err)
	}
	if string(data) != "hello\nworld\n" {
		t.Fatalf("content = %q, want %q", string(data), "hello\nworld\n")
	}
}

func TestFileWriteWithoutTrackerRejectsExistingFile(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "app.go")
	if err := os.WriteFile(filePath, []byte("package main\n"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	_, err := invokeTool(t, reg, "file.write", `{"path":"app.go","content":"package main\n"}`)
	if err == nil {
		t.Fatal("expected error overwriting existing file without tracker")
	}
	if !strings.Contains(err.Error(), "requires a tracker-backed session") {
		t.Fatalf("error should mention tracker-backed session, got: %v", err)
	}
}

func TestFileWriteOverwritesExistingAfterRead(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "app.go")
	orig := "package main\n\nfunc main() {}\n"
	if err := os.WriteFile(filePath, []byte(orig), 0755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	dbPath := filepath.Join(t.TempDir(), "filetrack.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()
	if err := database.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	ft := filetrack.New(database.SQLDB(), "test-session")

	state := session.New(config.Default(), root, time.Unix(100, 0), session.Persistence{})
	reg := registry.New()
	if err := RegisterAll(reg, Options{
		WorkspaceRoot: root,
		CommandRunner: &fakeRunner{},
		FileTracker:   ft,
		SessionState:  state,
	}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	// Read first to satisfy the stale-file contract.
	if _, err := invokeTool(t, reg, "file.read", `{"path":"app.go"}`); err != nil {
		t.Fatalf("file.read: %v", err)
	}

	newContent := "package main\n\nfunc main() { println(\"new\") }\n"
	argsJSON, err := json.Marshal(map[string]string{"path": "app.go", "content": newContent})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	res, err := invokeTool(t, reg, "file.write", string(argsJSON))
	if err != nil {
		t.Fatalf("file.write: %v", err)
	}
	if !reflect.DeepEqual(res.FilesChanged, []string{"app.go"}) {
		t.Fatalf("FilesChanged = %#v, want [app.go]", res.FilesChanged)
	}

	// Backup holds the old content and mode.
	if !state.HasBackup() {
		t.Fatal("expected a backup after file.write")
	}
	backup := state.Backup()
	if len(backup) != 1 || backup[0].Path != "app.go" || backup[0].Content != orig || backup[0].Mode != 0755 {
		t.Fatalf("unexpected backup: %#v", backup)
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if string(data) != newContent {
		t.Fatalf("content = %q, want %q", string(data), newContent)
	}
	info, err := os.Stat(filePath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode() != 0755 {
		t.Fatalf("mode = %v, want 0755 preserved", info.Mode())
	}
}

// TestFileWritePreservesCRLFWithoutCorruption pins that writing to a CRLF
// file converts LF to CRLF without turning existing CRLF sequences into
// CRCRLF.
func TestFileWritePreservesCRLFWithoutCorruption(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "app.go")
	orig := "package main\r\n\r\nfunc main() {\r\n\tprintln(\"hello\")\r\n}\r\n"
	if err := os.WriteFile(filePath, []byte(orig), 0755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	dbPath := filepath.Join(t.TempDir(), "filetrack.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()
	if err := database.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	ft := filetrack.New(database.SQLDB(), "test-session")

	state := session.New(config.Default(), root, time.Unix(100, 0), session.Persistence{})
	reg := registry.New()
	if err := RegisterAll(reg, Options{
		WorkspaceRoot: root,
		CommandRunner: &fakeRunner{},
		FileTracker:   ft,
		SessionState:  state,
	}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	// Read first to satisfy the stale-file contract.
	if _, err := invokeTool(t, reg, "file.read", `{"path":"app.go"}`); err != nil {
		t.Fatalf("file.read: %v", err)
	}

	// Content already contains a CRLF sequence; a naive LF->CRLF conversion
	// would turn it into CRCRLF.
	newContent := "package main\r\n\r\nfunc main() {\r\n\tprintln(\"patched\")\r\n}\r\n"
	argsJSON, err := json.Marshal(map[string]string{"path": "app.go", "content": newContent})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	if _, err := invokeTool(t, reg, "file.write", string(argsJSON)); err != nil {
		t.Fatalf("file.write: %v", err)
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if strings.Contains(string(data), "\r\r\n") {
		t.Fatalf("CRLF corrupted into CRCRLF: %q", string(data))
	}
	if !strings.Contains(string(data), "\r\n") {
		t.Fatalf("expected CRLF line endings preserved, got %q", string(data))
	}
}

func TestFileWriteCRLFDiffMatchesOnDiskBytes(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "app.go")
	orig := "line one\r\nline two\r\n"
	if err := os.WriteFile(filePath, []byte(orig), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	dbPath := filepath.Join(t.TempDir(), "filetrack.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()
	if err := database.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	ft := filetrack.New(database.SQLDB(), "test-session")

	reg := registry.New()
	if err := RegisterAll(reg, Options{
		WorkspaceRoot: root,
		CommandRunner: &fakeRunner{},
		FileTracker:   ft,
	}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	if _, err := invokeTool(t, reg, "file.read", `{"path":"app.go"}`); err != nil {
		t.Fatalf("file.read: %v", err)
	}

	// Propose LF-only content to a CRLF file; the tool normalizes it to
	// CRLF on write and the returned diff should reflect the bytes that
	// are actually written.
	res, err := invokeTool(t, reg, "file.write", `{"path":"app.go","content":"line one\nline three\n"}`)
	if err != nil {
		t.Fatalf("file.write: %v", err)
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	written := string(data)
	if !strings.Contains(written, "\r\n") {
		t.Fatalf("expected CRLF on disk, got %q", written)
	}
	if strings.Contains(res.Content, "line one\nline three\n") && !strings.Contains(res.Content, "line one\r\nline three\r\n") {
		t.Fatalf("diff shows LF content %q instead of CRLF bytes %q", res.Content, written)
	}
}

func TestFileWriteRequiresReadBeforeOverwrite(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "app.go")
	if err := os.WriteFile(filePath, []byte("package main\n"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	dbPath := filepath.Join(t.TempDir(), "filetrack.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()
	if err := database.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	ft := filetrack.New(database.SQLDB(), "test-session")

	reg := registry.New()
	if err := RegisterAll(reg, Options{
		WorkspaceRoot: root,
		CommandRunner: &fakeRunner{},
		FileTracker:   ft,
	}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	_, err = invokeTool(t, reg, "file.write", `{"path":"app.go","content":"package main\n"}`)
	if err == nil {
		t.Fatal("expected error for overwriting a file never read this session")
	}
	if !strings.Contains(err.Error(), "never read this session") {
		t.Fatalf("error should mention 'never read this session', got: %v", err)
	}
}

func TestFileWriteRejectsStaleRead(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "test.txt")
	if err := os.WriteFile(filePath, []byte("v1\n"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	dbPath := filepath.Join(t.TempDir(), "filetrack.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()
	if err := database.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	ft := filetrack.New(database.SQLDB(), "test-session")

	reg := registry.New()
	if err := RegisterAll(reg, Options{
		WorkspaceRoot: root,
		CommandRunner: &fakeRunner{},
		FileTracker:   ft,
	}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	if _, err := invokeTool(t, reg, "file.read", `{"path":"test.txt"}`); err != nil {
		t.Fatalf("file.read: %v", err)
	}
	// Modify the file after the read.
	time.Sleep(20 * time.Millisecond)
	if err := os.WriteFile(filePath, []byte("v1-modified\n"), 0644); err != nil {
		t.Fatalf("WriteFile (modify): %v", err)
	}

	_, err = invokeTool(t, reg, "file.write", `{"path":"test.txt","content":"v2\n"}`)
	if err == nil {
		t.Fatal("expected error for stale read")
	}
	if !strings.Contains(err.Error(), "changed on disk") {
		t.Fatalf("error should mention 'changed on disk', got: %v", err)
	}
}

func TestFileWriteRejectsPathEscapingRoot(t *testing.T) {
	root := t.TempDir()
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	_, err := invokeTool(t, reg, "file.write", `{"path":"../escape.txt","content":"x"}`)
	if err == nil {
		t.Fatal("expected error for path escaping the root")
	}
}

func TestFileWritePatchToolAcceptsUnifiedDiff(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "app.go")
	orig := "package main\n\nfunc main() {\n\tprintln(\"hello\")\n}\n"
	writeFile(t, filePath, orig)

	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("RegisterAll error: %v", err)
	}

	args := `{"patch": "--- a/app.go\n+++ b/app.go\n@@ -3,3 +3,3 @@\n func main() {\n-\tprintln(\"hello\")\n+\tprintln(\"patched\")\n }"}`
	res, err := invokeTool(t, reg, "file.write_patch", args)
	if err != nil {
		t.Fatalf("handler failed: %v", err)
	}
	if !strings.Contains(res.Content, "converted unified diff") {
		t.Fatalf("expected conversion repair note in result content, got: %s", res.Content)
	}
	if !reflect.DeepEqual(res.FilesChanged, []string{"app.go"}) {
		t.Fatalf("FilesChanged = %#v, want %#v", res.FilesChanged, []string{"app.go"})
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("read file failed: %v", err)
	}
	if !strings.Contains(string(data), "println(\"patched\")") {
		t.Fatalf("file content not patched: %s", string(data))
	}
}

func TestFileWritePatchToolUnifiedDiffSearchMiss(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "app.go")
	writeFile(t, filePath, "package main\n\nfunc main() {}\n")

	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("RegisterAll error: %v", err)
	}

	args := `{"patch": "--- a/app.go\n+++ b/app.go\n@@ -1,2 +1,2 @@\n-no such line\n+replacement\n tail"}`
	_, err := invokeTool(t, reg, "file.write_patch", args)
	if err == nil || !strings.Contains(err.Error(), "search block not found") {
		t.Fatalf("err = %v, want the existing search-miss error with nearest-region hint", err)
	}
}

func TestFileWritePatchDescriptionMentionsUnifiedDiff(t *testing.T) {
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: t.TempDir(), CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("RegisterAll error: %v", err)
	}
	tool, ok := reg.Lookup("file.write_patch")
	if !ok {
		t.Fatal("file.write_patch not registered")
	}
	if !strings.Contains(tool.Description, "Unified diff") {
		t.Fatalf("description missing unified-diff acceptance: %s", tool.Description)
	}
}
