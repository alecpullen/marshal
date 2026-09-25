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
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

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

func TestFileReadAcceptsAbsolutePathInsideRoot(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "README.md"), "hello\n")
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	args, err := json.Marshal(map[string]string{"path": filepath.Join(root, "README.md")})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	result, err := invokeTool(t, reg, "file.read", string(args))
	if err != nil {
		t.Fatalf("file.read absolute path inside root returned error: %v", err)
	}
	if result.Content != "hello\n" {
		t.Fatalf("Content = %q, want %q", result.Content, "hello\n")
	}
}

func TestFileReadAcceptsAbsolutePathInAdditionalRoot(t *testing.T) {
	projectRoot := t.TempDir()
	worktreePath := t.TempDir()

	docPath := filepath.Join(projectRoot, "docs", "architecture.md")
	writeFile(t, docPath, "architecture\n")

	reg := registry.New()
	if err := RegisterAll(reg, Options{
		WorkspaceRoot:   worktreePath,
		AdditionalRoots: []string{projectRoot},
		CommandRunner:   &fakeRunner{},
	}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	args, err := json.Marshal(map[string]string{"path": docPath})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	result, err := invokeTool(t, reg, "file.read", string(args))
	if err != nil {
		t.Fatalf("file.read absolute path in additional root returned error: %v", err)
	}
	if !strings.Contains(result.Content, "architecture") {
		t.Fatalf("Content should contain the file body, got: %q", result.Content)
	}
}

func TestFileReadRejectsAbsolutePathOutsideAllRoots(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir()
	outsideFile := filepath.Join(other, "secret.txt")
	writeFile(t, outsideFile, "secret\n")

	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	// Absolute path of a file in an unrelated temp dir must fail.
	args, err := json.Marshal(map[string]string{"path": outsideFile})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	_, err = invokeTool(t, reg, "file.read", string(args))
	if err == nil {
		t.Fatal("file.read absolute path outside all roots returned nil error")
	}
	if !strings.Contains(err.Error(), "outside all allowed roots") {
		t.Fatalf("error should name the allowed roots, got: %v", err)
	}

	// /etc/passwd must also fail through the tool.
	_, err = invokeTool(t, reg, "file.read", `{"path":"/etc/passwd"}`)
	if err == nil {
		t.Fatal("file.read /etc/passwd returned nil error")
	}
}

func TestFileReadRejectsSymlinkEscapingAbsolutePath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink tests not supported on Windows")
	}
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(outside, "secret.txt"), "secret\n")

	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	args, err := json.Marshal(map[string]string{"path": filepath.Join(root, "link", "secret.txt")})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	_, err = invokeTool(t, reg, "file.read", string(args))
	if err == nil {
		t.Fatal("file.read symlink-escaping absolute path returned nil error")
	}
	if !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("error should mention escape, got: %v", err)
	}
}

func TestFilePageAcceptsAbsolutePathInsideRoot(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "notes.txt"), "one\ntwo\nthree\nfour\nfive\n")
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	args, err := json.Marshal(map[string]any{"path": filepath.Join(root, "notes.txt"), "page": 1})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	result, err := invokeTool(t, reg, "file.page", string(args))
	if err != nil {
		t.Fatalf("file.page absolute path inside root returned error: %v", err)
	}
	if !strings.Contains(result.Content, "one") {
		t.Fatalf("Content should contain first-page content, got: %q", result.Content)
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

	// file.read no longer rejects the whole file: it exceeds the default
	// per-tool output limit, so it falls back to showing the head with a
	// footer telling the model how to continue.
	head, err := invokeTool(t, reg, "file.read", `{"path":"big.txt"}`)
	if err != nil {
		t.Fatalf("file.read should fall back to a head read, got error: %v", err)
	}
	if !strings.Contains(head.Content, "showing lines") {
		t.Fatalf("file.read oversized fallback missing footer: %q", head.Content)
	}
	if head.Notice == nil || head.Notice.Kind != registry.NoticeOversizeFallback {
		t.Fatalf("file.read oversized Notice = %#v, want %q", head.Notice, registry.NoticeOversizeFallback)
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

// TestFileReadHugeFileFallsBackToHead pins the Task 2 contract: a file above
// the per-tool read budget no longer errors; file.read shows the head plus a
// footer telling the model how to continue. (This replaced the older
// TestFileReadRefusesHugeFile, which asserted the pre-Task-2 error behavior.)
func TestFileReadHugeFileFallsBackToHead(t *testing.T) {
	tmp := t.TempDir()
	big := filepath.Join(tmp, "big.txt")
	// A file comfortably larger than the configured 8 KiB read budget.
	var sb strings.Builder
	for i := 0; i < 2000; i++ {
		fmt.Fprintf(&sb, "line %05d\n", i)
	}
	if err := os.WriteFile(big, []byte(sb.String()), 0644); err != nil {
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

	result, err := invokeTool(t, reg, "file.read", `{"path":"big.txt"}`)
	if err != nil {
		t.Fatalf("file.read oversized file should fall back to a head read, got error: %v", err)
	}
	if !strings.Contains(result.Content, "showing lines") {
		t.Fatalf("Content should contain the head-fallback footer, got: %q", result.Content)
	}
	if result.Notice == nil || result.Notice.Kind != registry.NoticeOversizeFallback {
		t.Fatalf("Notice = %#v, want kind %q", result.Notice, registry.NoticeOversizeFallback)
	}
}

func TestFileReadOversizedHeadFallback(t *testing.T) {
	root := t.TempDir()
	var sb strings.Builder
	for i := 0; i < 300; i++ {
		fmt.Fprintf(&sb, "line %05d padding padding\n", i)
	}
	writeFile(t, filepath.Join(root, "big.txt"), sb.String())

	reg := registry.New()
	if err := RegisterAll(reg, Options{
		WorkspaceRoot:  root,
		CommandRunner:  &fakeRunner{},
		MaxOutputBytes: 1024,
	}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	result, err := invokeTool(t, reg, "file.read", `{"path":"big.txt"}`)
	if err != nil {
		t.Fatalf("file.read returned error: %v", err)
	}
	if !strings.Contains(result.Content, "showing lines") {
		t.Fatalf("Content should contain the head-plus-footer, got: %q", result.Content)
	}
	if !strings.Contains(result.Summary, "oversized head fallback") {
		t.Fatalf("Summary = %q, want it to mention the oversized head fallback", result.Summary)
	}
	if result.Notice == nil {
		t.Fatal("expected a Notice on the oversized head fallback")
	}
	if result.Notice.Kind != registry.NoticeOversizeFallback {
		t.Fatalf("Notice.Kind = %q, want %q", result.Notice.Kind, registry.NoticeOversizeFallback)
	}
	if got := result.Notice.Data["total_lines"]; got != 300 {
		t.Fatalf("Notice.Data[total_lines] = %v, want 300", got)
	}
}

// oversizedLinesFixture writes a file of n lines ("line %05d padding" style)
// whose byte size comfortably exceeds the configured read budget, and returns
// the path. The line format is stable so tests can assert real line content.
func oversizedLinesFixture(t *testing.T, dir, name string, n int) string {
	t.Helper()
	var sb strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&sb, "line %05d\n", i)
	}
	path := filepath.Join(dir, name)
	writeFile(t, path, sb.String())
	return path
}

// TestFileReadOversizedRangedReadReturnsRequestedWindow pins the approved fix:
// a range request against an oversized file returns the requested window
// (real line content), not the head.
func TestFileReadOversizedRangedReadReturnsRequestedWindow(t *testing.T) {
	root := t.TempDir()
	oversizedLinesFixture(t, root, "big.txt", 2000)

	reg := registry.New()
	if err := RegisterAll(reg, Options{
		WorkspaceRoot:  root,
		CommandRunner:  &fakeRunner{},
		MaxOutputBytes: 8 * 1024,
	}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	result, err := invokeTool(t, reg, "file.read", `{"path":"big.txt","start_line":101,"end_line":103}`)
	if err != nil {
		t.Fatalf("file.read ranged oversized returned error: %v", err)
	}
	// Line 101 is index 100: "line 00100".
	for _, want := range []string{"line 00100", "line 00101", "line 00102"} {
		if !strings.Contains(result.Content, want) {
			t.Fatalf("Content missing %q:\n%s", want, result.Content)
		}
	}
	if strings.Contains(result.Content, "line 00000") {
		t.Fatalf("ranged read must not return the head:\n%s", result.Content)
	}
	if result.Notice == nil || result.Notice.Kind != registry.NoticeOversizeFallback {
		t.Fatalf("Notice = %#v, want kind %q", result.Notice, registry.NoticeOversizeFallback)
	}
	if got := result.Notice.Data["total_lines"]; got != 2000 {
		t.Fatalf("Notice.Data[total_lines] = %v, want 2000", got)
	}
	if got := result.Notice.Data["shown_lines"]; got != 3 {
		t.Fatalf("Notice.Data[shown_lines] = %v, want 3", got)
	}
	if got := result.Notice.Data["start_line"]; got != 101 {
		t.Fatalf("Notice.Data[start_line] = %v, want 101", got)
	}
}

// TestFileReadOversizedHeadContinuationAdvances is the regression that
// motivated the fix: the head fallback's advertised continuation
// (file.read start_line=N+1) must return a *different* window, not the same
// head forever.
func TestFileReadOversizedHeadContinuationAdvances(t *testing.T) {
	root := t.TempDir()
	oversizedLinesFixture(t, root, "big.txt", 2000)

	reg := registry.New()
	if err := RegisterAll(reg, Options{
		WorkspaceRoot:  root,
		CommandRunner:  &fakeRunner{},
		MaxOutputBytes: 8 * 1024,
	}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	head, err := invokeTool(t, reg, "file.read", `{"path":"big.txt"}`)
	if err != nil {
		t.Fatalf("file.read head returned error: %v", err)
	}
	if !strings.Contains(head.Content, "line 00000") {
		t.Fatalf("head should contain the first line:\n%s", head.Content)
	}

	// Parse the advertised continuation from the footer.
	idx := strings.Index(head.Content, "continue with file.read start_line=")
	if idx < 0 {
		t.Fatalf("head footer missing advertised continuation:\n%s", head.Content)
	}
	rest := head.Content[idx+len("continue with file.read start_line="):]
	digits := rest
	for i, r := range rest {
		if r < '0' || r > '9' {
			digits = rest[:i]
			break
		}
	}
	next, err := strconv.Atoi(digits)
	if err != nil {
		t.Fatalf("could not parse advertised start_line %q: %v", digits, err)
	}

	cont, err := invokeTool(t, reg, "file.read", fmt.Sprintf(`{"path":"big.txt","start_line":%d}`, next))
	if err != nil {
		t.Fatalf("file.read continuation returned error: %v", err)
	}
	// The continuation window starts at the advertised line, not at line 1.
	first := fmt.Sprintf("line %05d", next-1)
	if !strings.Contains(cont.Content, first) {
		t.Fatalf("continuation window should contain %q:\n%s", first, cont.Content)
	}
	if strings.Contains(cont.Content, "line 00000") {
		t.Fatalf("continuation returned the head again (infinite loop):\n%s", cont.Content)
	}
	if cont.Content == head.Content {
		t.Fatal("continuation returned identical content to the head")
	}
}

// TestFileReadOversizedRangeStartPastEOF mirrors selectLines' in-budget
// behavior: a start beyond the last line is an empty window, not an error.
func TestFileReadOversizedRangeStartPastEOF(t *testing.T) {
	root := t.TempDir()
	oversizedLinesFixture(t, root, "big.txt", 300)

	reg := registry.New()
	if err := RegisterAll(reg, Options{
		WorkspaceRoot:  root,
		CommandRunner:  &fakeRunner{},
		MaxOutputBytes: 1024,
	}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	result, err := invokeTool(t, reg, "file.read", `{"path":"big.txt","start_line":5000,"end_line":5100}`)
	if err != nil {
		t.Fatalf("file.read start past EOF returned error: %v", err)
	}
	if result.Content != "" {
		t.Fatalf("Content = %q, want empty window", result.Content)
	}
	if !strings.Contains(result.Summary, "past the end of the file") {
		t.Fatalf("Summary = %q, want a past-EOF note", result.Summary)
	}
}

// TestFileReadOversizedRangeByteBudgetTruncation covers a window that is cut
// by the output byte budget: it carries the slice-truncation footer and the
// NoticeSliceTruncated notice (with accurate window/total fields).
func TestFileReadOversizedRangeByteBudgetTruncation(t *testing.T) {
	root := t.TempDir()
	oversizedLinesFixture(t, root, "big.txt", 400)

	const limit = 1024
	reg := registry.New()
	if err := RegisterAll(reg, Options{
		WorkspaceRoot:  root,
		CommandRunner:  &fakeRunner{},
		MaxOutputBytes: limit,
	}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	// Whole-file window: its byte size far exceeds the limit, so it is cut.
	result, err := invokeTool(t, reg, "file.read", `{"path":"big.txt","start_line":1,"end_line":400}`)
	if err != nil {
		t.Fatalf("file.read returned error: %v", err)
	}
	if result.Notice == nil || result.Notice.Kind != registry.NoticeSliceTruncated {
		t.Fatalf("Notice = %#v, want kind %q", result.Notice, registry.NoticeSliceTruncated)
	}
	if !strings.Contains(result.Content, "output truncated at") {
		t.Fatalf("Content should contain the truncation footer, got: %q", result.Content)
	}
	if got := result.Notice.Data["total_lines"]; got != 400 {
		t.Fatalf("Notice.Data[total_lines] = %v, want 400", got)
	}
	// shown_lines must reflect the emitted window, which is cut short of 400.
	// Each line is 11 bytes ("line %05d" + '\n'), so the 1024 emitted bytes
	// end at the newline of line 93; line 94 is cut mid-line and is therefore
	// only partially shown. Exactly 93 lines are fully shown. Reporting more
	// would advertise part of line 94 as shown and send a caller following the
	// continuation past bytes it never received.
	shown, ok := result.Notice.Data["shown_lines"].(int)
	if !ok {
		t.Fatalf("Notice.Data[shown_lines] = %#v, want int", result.Notice.Data["shown_lines"])
	}
	if shown != 93 {
		t.Fatalf("Notice.Data[shown_lines] = %d, want 93", shown)
	}
	if end, _ := result.Notice.Data["end_line"].(int); end != 93 {
		t.Fatalf("Notice.Data[end_line] = %v, want 93", result.Notice.Data["end_line"])
	}
	if start, _ := result.Notice.Data["start_line"].(int); start != 1 {
		t.Fatalf("Notice.Data[start_line] = %v, want 1", result.Notice.Data["start_line"])
	}
	// The footer text must agree with the data it advertises.
	if !strings.Contains(result.Content, "showing lines 1-93 of 400") {
		t.Fatalf("Content footer should advertise lines 1-93, got:\n%s", result.Content)
	}
	if len(result.Content) < limit {
		t.Fatalf("emitted prefix is %d bytes, want at least %d", len(result.Content), limit)
	}
	if !strings.Contains(result.Content, "continue with file.read start_line=94") {
		t.Fatalf("Content footer should advertise start_line=94, got:\n%s", result.Content)
	}
	// The emitted prefix must end mid-line: that is the partially shown line 94,
	// so the last body byte before the marker is not a line break.
	if result.Content[limit-1] == '\n' {
		t.Fatalf("the truncated body should end mid-line (line 94), not at a line break")
	}
}

// TestFileReadOversizedRangeSingleOverlongLineFooter pins that the degenerate
// branch (the window's first line alone exceeds the whole output budget) does
// not recommend file.page: file.page applies the same output budget, so it
// cannot recover the tail of the line either. The footer must point at a route
// that can actually work.
func TestFileReadOversizedRangeSingleOverlongLineFooter(t *testing.T) {
	root := t.TempDir()
	// One line far longer than the budget; the trailing newline keeps the file
	// oversized and gives the streaming path a window to work with.
	writeFile(t, filepath.Join(root, "wide.txt"), strings.Repeat("x", 200)+"\n")

	const limit = 64
	reg := registry.New()
	if err := RegisterAll(reg, Options{
		WorkspaceRoot:  root,
		CommandRunner:  &fakeRunner{},
		MaxOutputBytes: limit,
	}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	result, err := invokeTool(t, reg, "file.read", `{"path":"wide.txt","start_line":1,"end_line":1}`)
	if err != nil {
		t.Fatalf("file.read returned error: %v", err)
	}
	if !strings.Contains(result.Content, "not reachable with file.read") {
		t.Fatalf("degenerate branch footer missing the unreachable-warning, got:\n%s", result.Content)
	}
	if strings.Contains(result.Content, "use file.page") {
		t.Fatalf("footer must not recommend file.page, which cannot recover the tail:\n%s", result.Content)
	}
	if !strings.Contains(result.Content, "shell.run") {
		t.Fatalf("footer should offer a route that can work (shell.run byte range), got:\n%s", result.Content)
	}
}

// TestClipLinesRuneSafe pins that the per-line clip never splits a multi-byte
// UTF-8 rune and that the reported elision count is in runes.
func TestClipLinesRuneSafe(t *testing.T) {
	const runeCount = 2000
	line := strings.Repeat("→", runeCount) // 3 bytes each; > maxLineChars runes.
	clipped, did := clipLines(line)
	if !did {
		t.Fatal("clipLines reported no clip for an over-long line")
	}
	if !utf8.ValidString(clipped) {
		t.Fatalf("clipLines produced invalid UTF-8: %q", clipped)
	}
	marker := fmt.Sprintf(" … [%d more chars]", runeCount-maxLineChars)
	if !strings.HasSuffix(clipped, marker) {
		t.Fatalf("clipped line should end with %q, got: %q", marker, clipped)
	}
	kept := strings.TrimSuffix(clipped, marker)
	if got := utf8.RuneCountInString(kept); got != maxLineChars {
		t.Fatalf("kept %d runes, want %d", got, maxLineChars)
	}
}

func TestFileReadRangedTruncationFooter(t *testing.T) {
	root := t.TempDir()
	// readWorkspaceFile admits files up to limit+1 bytes and selectLines can
	// only ever return a subset of what was read, so a ranged read can only
	// exceed the byte budget at this exact boundary.
	const limit = 64
	writeFile(t, filepath.Join(root, "ranged.txt"), strings.Repeat("x", limit)+"\n")

	reg := registry.New()
	if err := RegisterAll(reg, Options{
		WorkspaceRoot:  root,
		CommandRunner:  &fakeRunner{},
		MaxOutputBytes: limit,
	}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	result, err := invokeTool(t, reg, "file.read", `{"path":"ranged.txt","start_line":1,"end_line":1}`)
	if err != nil {
		t.Fatalf("file.read returned error: %v", err)
	}
	if !strings.Contains(result.Content, "output truncated at") {
		t.Fatalf("Content should contain the truncation footer, got: %q", result.Content)
	}
	if result.Notice == nil || result.Notice.Kind != registry.NoticeSliceTruncated {
		t.Fatalf("Notice = %#v, want kind %q", result.Notice, registry.NoticeSliceTruncated)
	}
}

// TestFileReadRangedTruncationAdvertisesFullyShownLines is the exact
// reproduction of the silent-data-loss bug in the streaming ranged read: with
// a 64-byte budget and 63-'x' lines the body is cut one byte into line 2, so
// line 2 is emitted only in part. The footer must not claim line 2 as fully
// shown — its "start_line=shownEnd+1" advice would then skip the remaining 62
// bytes of line 2 — and the rest of the output must be reachable by following
// the advertised continuation.
func TestFileReadRangedTruncationAdvertisesFullyShownLines(t *testing.T) {
	root := t.TempDir()
	const limit = 64
	line := strings.Repeat("x", 63) // 64 bytes per line, exactly the budget.
	writeFile(t, filepath.Join(root, "wide.txt"), strings.Repeat(line+"\n", 3))

	reg := registry.New()
	if err := RegisterAll(reg, Options{
		WorkspaceRoot:  root,
		CommandRunner:  &fakeRunner{},
		MaxOutputBytes: limit,
	}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	result, err := invokeTool(t, reg, "file.read", `{"path":"wide.txt","start_line":1,"end_line":3}`)
	if err != nil {
		t.Fatalf("file.read returned error: %v", err)
	}
	if result.Notice == nil || result.Notice.Kind != registry.NoticeSliceTruncated {
		t.Fatalf("Notice = %#v, want kind %q", result.Notice, registry.NoticeSliceTruncated)
	}

	// The cut lands one byte into line 2, so line 1 is the last FULLY shown
	// line. Pre-fix this reported end_line 2.
	if end, _ := result.Notice.Data["end_line"].(int); end != 1 {
		t.Fatalf("Notice.Data[end_line] = %#v, want 1 (the last fully shown line)", result.Notice.Data["end_line"])
	}
	if shown, _ := result.Notice.Data["shown_lines"].(int); shown != 1 {
		t.Fatalf("Notice.Data[shown_lines] = %#v, want 1", result.Notice.Data["shown_lines"])
	}
	if strings.Contains(result.Content, "showing lines 1-2 ") {
		t.Fatalf("footer claims line 2 is fully shown, but it was cut mid-line:\n%s", result.Content)
	}
	if !strings.Contains(result.Content, "showing lines 1-1 of 3") {
		t.Fatalf("footer should advertise the fully shown line only, got:\n%s", result.Content)
	}
	if !strings.Contains(result.Content, "continue with file.read start_line=2") {
		t.Fatalf("footer should advertise start_line=2, got:\n%s", result.Content)
	}

	// Following the advice must reach the bytes the cut dropped, not jump
	// past them.
	cont, err := invokeTool(t, reg, "file.read", `{"path":"wide.txt","start_line":2,"end_line":3}`)
	if err != nil {
		t.Fatalf("file.read continuation returned error: %v", err)
	}
	if !strings.Contains(cont.Content, line) {
		t.Fatalf("continuation must reach the remainder of line 2:\n%s", cont.Content)
	}
	if !strings.Contains(cont.Content, "showing lines 2-2 of 3") {
		t.Fatalf("continuation should report line 2 (not 3) as the last fully shown line, got:\n%s", cont.Content)
	}
}

// TestFileReadOversizedRangeKeepsCarriageReturns pins that the ranged path
// trims exactly what selectLines trims. selectLines splits on '\n' and keeps a
// trailing '\r', so a CRLF file must read the same above and below the size
// limit instead of silently losing the carriage returns.
func TestFileReadOversizedRangeKeepsCarriageReturns(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "crlf.txt"), "alpha\r\nbeta\r\ngamma\r\n")

	// The budget forces file.read down the streaming path; file.page still
	// reads the same file whole, so the two can be compared directly.
	reg := registry.New()
	if err := RegisterAll(reg, Options{
		WorkspaceRoot:  root,
		CommandRunner:  &fakeRunner{},
		MaxOutputBytes: 16,
	}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	ranged, err := invokeTool(t, reg, "file.read", `{"path":"crlf.txt","start_line":1,"end_line":2}`)
	if err != nil {
		t.Fatalf("file.read ranged returned error: %v", err)
	}
	// The window is lines 1-2, which do not reach EOF, so the body keeps the
	// carriage returns but has no trailing newline of its own.
	if !strings.HasPrefix(ranged.Content, "alpha\r\nbeta\r") {
		t.Fatalf("ranged read should keep the carriage returns, got: %q", ranged.Content)
	}
	if !strings.Contains(ranged.Content, "showing lines 1-2 of 3") {
		t.Fatalf("ranged read should carry the window footer, got: %q", ranged.Content)
	}
	if strings.Contains(ranged.Content, "alpha\nbeta") {
		t.Fatalf("ranged read dropped the '\\r' that selectLines keeps: %q", ranged.Content)
	}

	// page_size 2 keeps the page inside the budget (13 bytes), so file.page
	// emits the same two lines whole for comparison.
	page, err := invokeTool(t, reg, "file.page", `{"path":"crlf.txt","page":1,"page_size":2}`)
	if err != nil {
		t.Fatalf("file.page returned error: %v", err)
	}
	if page.Content != "alpha\r\nbeta\r" {
		t.Fatalf("file.page content = %q, want %q", page.Content, "alpha\r\nbeta\r")
	}
	if !strings.HasPrefix(ranged.Content, page.Content) {
		t.Fatalf("ranged read %q should start with the same lines file.page emits (%q)", ranged.Content, page.Content)
	}
}

// TestFileReadMarkerLiteralIsNotTruncation pins that the truncation footer is
// driven by the emitted-length comparison and not by looking for the marker
// text: a file that is exactly the limit and whose own last line is the
// literal marker must come back whole and with a nil Notice.
func TestFileReadMarkerLiteralIsNotTruncation(t *testing.T) {
	root := t.TempDir()
	const limit = 64
	content := strings.Repeat("z", limit-len(truncationMarker)) + truncationMarker
	if len(content) != limit {
		t.Fatalf("fixture is %d bytes, want exactly %d", len(content), limit)
	}
	writeFile(t, filepath.Join(root, "marker.txt"), content)

	reg := registry.New()
	if err := RegisterAll(reg, Options{
		WorkspaceRoot:  root,
		CommandRunner:  &fakeRunner{},
		MaxOutputBytes: limit,
	}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	result, err := invokeTool(t, reg, "file.read", `{"path":"marker.txt"}`)
	if err != nil {
		t.Fatalf("file.read returned error: %v", err)
	}
	if result.Notice != nil {
		t.Fatalf("Notice = %#v, want nil for a file returned whole", result.Notice)
	}
	if strings.Contains(result.Content, "output truncated at") {
		t.Fatalf("Content should not carry a truncation footer, got:\n%q", result.Content)
	}
	if result.Content != content {
		t.Fatalf("Content = %q, want the file verbatim", result.Content)
	}
}

func TestFileReadLineClip(t *testing.T) {
	root := t.TempDir()
	longLine := strings.Repeat("y", 2000)
	writeFile(t, filepath.Join(root, "long.txt"), longLine+"\n")

	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	result, err := invokeTool(t, reg, "file.read", `{"path":"long.txt"}`)
	if err != nil {
		t.Fatalf("file.read returned error: %v", err)
	}
	if !strings.Contains(result.Content, "more chars]") {
		t.Fatalf("expected an elision marker on the clipped line, got: %q", result.Content)
	}
	if strings.Contains(result.Content, longLine) {
		t.Fatalf("the 2000-char line should have been clipped, got: %q", result.Content)
	}
	if !strings.Contains(result.Content, longLine[:maxLineChars]) {
		t.Fatalf("expected the first %d chars to be retained", maxLineChars)
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

// Attribution must reach the ToolResult, and must never fail a write that
// otherwise succeeded.
func TestWritePatchRecordsSymbols(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "app.go")
	orig := "package main\n\nfunc Alpha() int {\n\treturn 1\n}\n\nfunc Beta() int {\n\treturn 2\n}\n"
	if err := os.WriteFile(filePath, []byte(orig), 0644); err != nil {
		t.Fatalf("WriteFile error: %v", err)
	}

	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("RegisterAll error: %v", err)
	}

	// Patch Alpha's body.
	args := `{"patch": "File: app.go\n<<<<<<< SEARCH\n\treturn 1\n=======\n\treturn 11\n>>>>>>> REPLACE"}`
	res, err := invokeTool(t, reg, "file.write_patch", args)
	if err != nil {
		t.Fatalf("handler failed: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("write_patch reported an error: %s", res.Error)
	}
	if len(res.Symbols) == 0 {
		t.Fatal("expected symbols on the ToolResult")
	}
	if res.Symbols[0].Name != "Alpha" {
		t.Fatalf("attributed to %q, want Alpha", res.Symbols[0].Name)
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

func TestFileWriteRejectsAbsolutePathInsideRoot(t *testing.T) {
	root := t.TempDir()
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	absPath := filepath.Join(root, "w.txt")
	args, err := json.Marshal(map[string]string{"path": absPath, "content": "x"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	_, err = invokeTool(t, reg, "file.write", string(args))
	if err == nil {
		t.Fatal("file.write absolute path inside root returned nil error")
	}
	if !strings.Contains(err.Error(), "must be relative") {
		t.Fatalf("error should mention 'must be relative', got: %v", err)
	}
	// The file must NOT have been written.
	if _, statErr := os.Stat(absPath); !os.IsNotExist(statErr) {
		t.Fatalf("file.write absolute path should not create the file, stat err = %v", statErr)
	}
}

func TestFileWritePatchRejectsAbsolutePathInsideRoot(t *testing.T) {
	root := t.TempDir()
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	absPath := filepath.Join(root, "w.txt")
	args, err := json.Marshal(map[string]string{
		"patch": "File: " + absPath + "\n<<<<<<< SEARCH\n=======\nnew\n>>>>>>> REPLACE\n",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	_, err = invokeTool(t, reg, "file.write_patch", string(args))
	if err == nil {
		t.Fatal("file.write_patch absolute path inside root returned nil error")
	}
	if !strings.Contains(err.Error(), "must be relative") {
		t.Fatalf("error should mention 'must be relative', got: %v", err)
	}
	// No file should have been created at the absolute path.
	if _, statErr := os.Stat(absPath); !os.IsNotExist(statErr) {
		t.Fatalf("file.write_patch absolute path should not create the file, stat err = %v", statErr)
	}
}

func TestFileWriteRejectsAbsolutePathOutsideRoot(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir()
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	absPath := filepath.Join(other, "w.txt")
	args, err := json.Marshal(map[string]string{"path": absPath, "content": "x"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	_, err = invokeTool(t, reg, "file.write", string(args))
	if err == nil {
		t.Fatal("file.write absolute path outside root returned nil error")
	}
	if !strings.Contains(err.Error(), "must be relative") {
		t.Fatalf("error should mention 'must be relative', got: %v", err)
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

func TestReadToolSchemasDocumentAbsolutePaths(t *testing.T) {
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: t.TempDir(), CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("RegisterAll error: %v", err)
	}

	readTools := []string{"file.read", "file.page"}
	writeTools := []string{"file.write", "file.write_patch"}

	for _, name := range readTools {
		tool, ok := reg.Lookup(name)
		if !ok {
			t.Fatalf("%s not registered", name)
		}
		if !strings.Contains(string(tool.Schema), "absolute paths that resolve inside an allowed root") {
			t.Fatalf("%s schema should document absolute-path acceptance, got: %s", name, tool.Schema)
		}
		var v any
		if err := json.Unmarshal(tool.Schema, &v); err != nil {
			t.Fatalf("%s schema is not valid JSON: %v", name, err)
		}
	}

	for _, name := range writeTools {
		tool, ok := reg.Lookup(name)
		if !ok {
			t.Fatalf("%s not registered", name)
		}
		if strings.Contains(string(tool.Schema), "absolute path") {
			t.Fatalf("%s schema should not mention absolute paths, got: %s", name, tool.Schema)
		}
		if !strings.Contains(string(tool.Schema), "relative to the workspace") {
			t.Fatalf("%s schema should still say paths are workspace-relative, got: %s", name, tool.Schema)
		}
		var v any
		if err := json.Unmarshal(tool.Schema, &v); err != nil {
			t.Fatalf("%s schema is not valid JSON: %v", name, err)
		}
	}
}
