package native

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"marshal/internal/db"
	"marshal/internal/tools/registry"
)

func TestRepoSearchNestedGitignore(t *testing.T) {
	root := t.TempDir()
	// Root .gitignore: ignore *.log
	writeFile(t, filepath.Join(root, ".gitignore"), "*.log\n")
	// Sub .gitignore: un-ignore important.log
	writeFile(t, filepath.Join(root, "sub", ".gitignore"), "!important.log\n")
	// Files
	writeFile(t, filepath.Join(root, "sub", "debug.log"), "needle\n")
	writeFile(t, filepath.Join(root, "sub", "important.log"), "needle\n")
	writeFile(t, filepath.Join(root, "main.go"), "needle\n")

	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	result, err := invokeTool(t, reg, "repo.search", `{"query":"needle"}`)
	if err != nil {
		t.Fatalf("repo.search returned error: %v", err)
	}
	// sub/debug.log should be ignored by root *.log
	if strings.Contains(result.Content, "sub/debug.log") {
		t.Fatalf("Content included gitignored sub/debug.log:\n%s", result.Content)
	}
	// sub/important.log should be found (un-ignored by sub .gitignore)
	if !strings.Contains(result.Content, "sub/important.log") {
		t.Fatalf("Content missing sub/important.log (should be un-ignored):\n%s", result.Content)
	}
	// main.go should be found
	if !strings.Contains(result.Content, "main.go:1:needle") {
		t.Fatalf("Content missing main.go:\n%s", result.Content)
	}
}

func TestRepoSearchRespectsGitignore(t *testing.T) {
	root := t.TempDir()
	// Root .gitignore: ignore *.log
	writeFile(t, filepath.Join(root, ".gitignore"), "*.log\n")
	// A .log file at root — should be ignored
	writeFile(t, filepath.Join(root, "debug.log"), "needle\n")
	// A .go file at root — should be found
	writeFile(t, filepath.Join(root, "main.go"), "needle\n")

	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	result, err := invokeTool(t, reg, "repo.search", `{"query":"needle"}`)
	if err != nil {
		t.Fatalf("repo.search returned error: %v", err)
	}
	if strings.Contains(result.Content, "debug.log") {
		t.Fatalf("Content included gitignored file debug.log:\n%s", result.Content)
	}
	if !strings.Contains(result.Content, "main.go:1:needle") {
		t.Fatalf("Content missing main.go match:\n%s", result.Content)
	}
}

func TestRepoSearchFindsSubstringMatches(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.txt"), "alpha\nneedle here\n")
	writeFile(t, filepath.Join(root, "sub", "b.txt"), "another needle\n")
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	result, err := invokeTool(t, reg, "repo.search", `{"query":"needle"}`)
	if err != nil {
		t.Fatalf("repo.search returned error: %v", err)
	}
	for _, want := range []string{"a.txt:2:needle here", "sub/b.txt:1:another needle"} {
		if !strings.Contains(result.Content, want) {
			t.Fatalf("Content missing %q:\n%s", want, result.Content)
		}
	}
}

func TestRepoSearchRespectsMaxResults(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.txt"), "needle 1\nneedle 2\nneedle 3\n")
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	result, err := invokeTool(t, reg, "repo.search", `{"query":"needle","max_results":2}`)
	if err != nil {
		t.Fatalf("repo.search returned error: %v", err)
	}
	if strings.Count(result.Content, "a.txt:") != 2 {
		t.Fatalf("Content = %q, want 2 results", result.Content)
	}
	if !strings.Contains(result.Summary, "capped") {
		t.Fatalf("Summary = %q, want capped", result.Summary)
	}
}

func TestRepoSearchSkipsIgnoredDirectories(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".git", "config"), "needle\n")
	writeFile(t, filepath.Join(root, "node_modules", "pkg", "index.js"), "needle\n")
	writeFile(t, filepath.Join(root, "src", "main.go"), "needle\n")
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	result, err := invokeTool(t, reg, "repo.search", `{"query":"needle"}`)
	if err != nil {
		t.Fatalf("repo.search returned error: %v", err)
	}
	if strings.Contains(result.Content, ".git/") || strings.Contains(result.Content, "node_modules/") {
		t.Fatalf("Content included ignored directory:\n%s", result.Content)
	}
	if !strings.Contains(result.Content, "src/main.go:1:needle") {
		t.Fatalf("Content missing src match:\n%s", result.Content)
	}
}

func TestRepoSearch_SkipsSymlinksAndReportsErrors(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("os.Symlink requires elevated privileges on Windows")
	}
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("needle"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	result, err := invokeTool(t, reg, "repo.search", `{"query":"needle"}`)
	if err != nil {
		t.Fatalf("repo.search returned error: %v", err)
	}
	if strings.Contains(result.Content, "secret") {
		t.Errorf("search followed symlink outside root; content:\n%s", result.Content)
	}
}

func TestRepoSearchRejectsEmptyQueryAndTraversal(t *testing.T) {
	root := t.TempDir()
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	if _, err := invokeTool(t, reg, "repo.search", `{"query":""}`); err == nil {
		t.Fatal("repo.search empty query returned nil error")
	}
	if _, err := invokeTool(t, reg, "repo.search", `{"query":"x","path":"../outside"}`); err == nil {
		t.Fatal("repo.search traversal returned nil error")
	}
}

func TestRepoSearchRegexMode(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.txt"), "needle here\nn34dle there\n")
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	result, err := invokeTool(t, reg, "repo.search", `{"query":"n[0-9]+dle","mode":"regex"}`)
	if err != nil {
		t.Fatalf("repo.search returned error: %v", err)
	}
	if !strings.Contains(result.Content, "a.txt:2:n34dle there") {
		t.Fatalf("Content missing regex match:\n%s", result.Content)
	}
	if strings.Contains(result.Content, "needle here") {
		t.Fatalf("substring-only line should not match regex:\n%s", result.Content)
	}
}

func TestRepoSearchInvalidRegex(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.txt"), "needle\n")
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	_, err := invokeTool(t, reg, "repo.search", `{"query":"n[","mode":"regex"}`)
	if err == nil {
		t.Fatal("expected error for invalid regex")
	}
	if !strings.Contains(err.Error(), "invalid regex") {
		t.Fatalf("error = %v, want it to say %q so the model can correct itself", err, "invalid regex")
	}
}

func TestRepoSearchRejectsUnknownMode(t *testing.T) {
	root := t.TempDir()
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	if _, err := invokeTool(t, reg, "repo.search", `{"query":"x","mode":"fuzzy"}`); err == nil {
		t.Fatal("expected error for unknown mode")
	}
}

func TestRepoSearchIncludeGlob(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.go"), "// needle\n")
	writeFile(t, filepath.Join(root, "a.txt"), "needle\n")
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	result, err := invokeTool(t, reg, "repo.search", `{"query":"needle","include":"*.go"}`)
	if err != nil {
		t.Fatalf("repo.search returned error: %v", err)
	}
	if !strings.Contains(result.Content, "a.go:1:// needle") {
		t.Fatalf("Content missing a.go match:\n%s", result.Content)
	}
	if strings.Contains(result.Content, "a.txt") {
		t.Fatalf("include glob should have excluded a.txt:\n%s", result.Content)
	}
}

func TestRepoSearchContextLines(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.txt"), "one\ntwo\nneedle\nfour\nfive\n")
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	result, err := invokeTool(t, reg, "repo.search", `{"query":"needle","context":1}`)
	if err != nil {
		t.Fatalf("repo.search returned error: %v", err)
	}
	for _, want := range []string{"a.txt-2-two", "a.txt:3:needle", "a.txt-4-four"} {
		if !strings.Contains(result.Content, want) {
			t.Fatalf("Content missing %q:\n%s", want, result.Content)
		}
	}
	if strings.Contains(result.Content, "one") || strings.Contains(result.Content, "five") {
		t.Fatalf("context=1 should not include lines 1 or 5:\n%s", result.Content)
	}
}

func TestRepoSearchSubdirectoryRespectsRootGitignore(t *testing.T) {
	root := t.TempDir()
	// Root .gitignore ignores *.log everywhere.
	writeFile(t, filepath.Join(root, ".gitignore"), "*.log\n")
	// Search scoped to sub/ must still apply root rules.
	writeFile(t, filepath.Join(root, "sub", "debug.log"), "needle\n")
	writeFile(t, filepath.Join(root, "sub", "main.go"), "needle\n")

	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	result, err := invokeTool(t, reg, "repo.search", `{"query":"needle","path":"sub"}`)
	if err != nil {
		t.Fatalf("repo.search returned error: %v", err)
	}
	if strings.Contains(result.Content, "debug.log") {
		t.Fatalf("subdirectory search included root-ignored debug.log:\n%s", result.Content)
	}
	if !strings.Contains(result.Content, "sub/main.go:1:needle") {
		t.Fatalf("subdirectory search missing sub/main.go:\n%s", result.Content)
	}
}

func TestRepoSearchExcludesGitignoreFiles(t *testing.T) {
	root := t.TempDir()
	// Put the search term inside a .gitignore file.
	writeFile(t, filepath.Join(root, ".gitignore"), "needle\n")
	writeFile(t, filepath.Join(root, "main.go"), "needle\n")

	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	result, err := invokeTool(t, reg, "repo.search", `{"query":"needle"}`)
	if err != nil {
		t.Fatalf("repo.search returned error: %v", err)
	}
	if strings.Contains(result.Content, ".gitignore") {
		t.Fatalf("search should not return matches inside .gitignore files:\n%s", result.Content)
	}
	if !strings.Contains(result.Content, "main.go:1:needle") {
		t.Fatalf("search missing main.go match:\n%s", result.Content)
	}
}

func TestRepoSearchAutoModeSubstring(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.txt"), "alpha\nneedle here\n")
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	// Bare word with no mode: auto picks substring, and echoes the choice.
	result, err := invokeTool(t, reg, "repo.search", `{"query":"needle"}`)
	if err != nil {
		t.Fatalf("repo.search returned error: %v", err)
	}
	if !strings.Contains(result.Summary, "(mode: substring)") {
		t.Fatalf("Summary = %q, want (mode: substring)", result.Summary)
	}
	if !strings.Contains(result.Content, "a.txt:2:needle here") {
		t.Fatalf("Content missing substring match:\n%s", result.Content)
	}
}

func TestRepoSearchAutoModeRegex(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.txt"), "n34dle there\n")
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	// Pattern-shaped query with no mode: auto compiles it as RE2.
	result, err := invokeTool(t, reg, "repo.search", `{"query":"n[0-9]+dle"}`)
	if err != nil {
		t.Fatalf("repo.search returned error: %v", err)
	}
	if !strings.Contains(result.Summary, "(mode: regex)") {
		t.Fatalf("Summary = %q, want (mode: regex)", result.Summary)
	}
	if !strings.Contains(result.Content, "a.txt:1:n34dle there") {
		t.Fatalf("Content missing regex match:\n%s", result.Content)
	}
}

func TestRepoSearchZeroMatchSubCoach(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.txt"), "alpha\nbeta\n")
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	// Substring mode with a regex-shaped query and no hits: coach the caller
	// toward regex mode.
	result, err := invokeTool(t, reg, "repo.search", `{"query":"foo|bar","mode":"substring"}`)
	if err != nil {
		t.Fatalf("repo.search returned error: %v", err)
	}
	if !strings.Contains(result.Content, `retry with mode:"regex"`) {
		t.Fatalf("Content missing regex-mode coaching footer:\n%s", result.Content)
	}
	if result.Notice == nil || result.Notice.Kind != registry.NoticeZeroMatchCoach {
		t.Fatalf("Notice = %+v, want kind %q", result.Notice, registry.NoticeZeroMatchCoach)
	}
}

func TestRepoSearchZeroMatchRegexCoach(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.txt"), "alpha\nbeta\n")
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	// Regex mode with no hits: coach the caller toward substring mode.
	result, err := invokeTool(t, reg, "repo.search", `{"query":"needle","mode":"regex"}`)
	if err != nil {
		t.Fatalf("repo.search returned error: %v", err)
	}
	if !strings.Contains(result.Content, `try mode:"substring"`) {
		t.Fatalf("Content missing substring-mode coaching footer:\n%s", result.Content)
	}
	if result.Notice == nil || result.Notice.Kind != registry.NoticeZeroMatchCoach {
		t.Fatalf("Notice = %+v, want kind %q", result.Notice, registry.NoticeZeroMatchCoach)
	}
}

func TestRepoSearchCappedNotice(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.txt"), "needle 1\nneedle 2\nneedle 3\nneedle 4\n")

	// Lower the hard cap so the capped-results path is reachable cheaply.
	old := hardSearchMaxResults
	hardSearchMaxResults = 2
	defer func() { hardSearchMaxResults = old }()

	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	result, err := invokeTool(t, reg, "repo.search", `{"query":"needle"}`)
	if err != nil {
		t.Fatalf("repo.search returned error: %v", err)
	}
	if !strings.Contains(result.Content, "result capped at 2 matches") {
		t.Fatalf("Content missing capped footer:\n%s", result.Content)
	}
	if result.Notice == nil || result.Notice.Kind != registry.NoticeCappedResults {
		t.Fatalf("Notice = %+v, want kind %q", result.Notice, registry.NoticeCappedResults)
	}
}

func TestRepoSearchRejectsInvalidMode(t *testing.T) {
	root := t.TempDir()
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	_, err := invokeTool(t, reg, "repo.search", `{"query":"x","mode":"fuzzy"}`)
	if err == nil {
		t.Fatal("expected error for invalid mode")
	}
	// Schema enum validation rejects the mode before the handler switch, so
	// the message is the schema's "value must be one of ..." form. Assert it
	// advertises the full accepted set so the model can correct itself.
	for _, want := range []string{"auto", "substring", "regex"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %v, want it to advertise the accepted set (missing %q)", err, want)
		}
	}
}

// newKindSearchRegistry builds a registry with a fresh in-memory DB and the
// given symbols saved under the temp workspace root. Symbols are seeded so
// kind queries have something to resolve; body extraction still reads the
// real fixture files written by the caller.
func newKindSearchRegistry(t *testing.T, root string, symbols []db.Symbol) *registry.Registry {
	t.Helper()
	return newKindSearchRegistryWithRoots(t, root, nil, symbols)
}

// newKindSearchRegistryWithRoots is newKindSearchRegistry plus configured
// additional roots, so a test can exercise a path that resolves outside the
// workspace root (the linked-worktree / multi-root case).
func newKindSearchRegistryWithRoots(t *testing.T, root string, additionalRoots []string, symbols []db.Symbol) *registry.Registry {
	t.Helper()
	dbConn, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { dbConn.Close() })
	if err := dbConn.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	projectID, err := dbConn.GetOrCreateProject(root, "test")
	if err != nil {
		t.Fatalf("get or create project: %v", err)
	}
	if len(symbols) > 0 {
		if err := dbConn.SaveSymbols(projectID, symbols); err != nil {
			t.Fatalf("save symbols: %v", err)
		}
	}
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, DB: dbConn, ProjectID: projectID, AdditionalRoots: additionalRoots}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	return reg
}

func TestRepoSearchKindTypeSkeleton(t *testing.T) {
	root := t.TempDir()
	// A type symbol spanning lines 1-10 with three one-level field lines, a
	// blank line, a nested struct (deeper indent), and a closing brace.
	writeFile(t, filepath.Join(root, "foo.go"), "type Foo struct {\n"+
		"\tName string\n"+
		"\tAge  int\n"+
		"\tActive bool\n"+
		"\n"+
		"\t// nested\n"+
		"\tNested struct {\n"+
		"\t\tX int\n"+
		"\t}\n"+
		"}\n")

	reg := newKindSearchRegistry(t, root, []db.Symbol{
		{FilePath: "foo.go", Kind: "type", Name: "Foo", Signature: "type Foo struct", LineStart: 1, LineEnd: 10},
	})

	result, err := invokeTool(t, reg, "repo.search", `{"query":"Foo","kind":"type"}`)
	if err != nil {
		t.Fatalf("repo.search kind type failed: %v", err)
	}
	for _, want := range []string{"foo.go:1-10  type Foo struct", "Name string", "Age  int", "Active bool"} {
		if !strings.Contains(result.Content, want) {
			t.Fatalf("Content missing %q:\n%s", want, result.Content)
		}
	}
	if !strings.Contains(result.Content, "+3 more lines — file.read foo.go:1-10") {
		t.Fatalf("Content missing +N more lines marker:\n%s", result.Content)
	}
}

func TestRepoSearchKindFunctionHeaderOnly(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "bar.go"), "func Bar(a, b int) int {\n"+
		"\treturn a + b\n"+
		"}\n")

	reg := newKindSearchRegistry(t, root, []db.Symbol{
		{FilePath: "bar.go", Kind: "function", Name: "Bar", Signature: "func Bar(a, b int) int", LineStart: 1, LineEnd: 3},
	})

	result, err := invokeTool(t, reg, "repo.search", `{"query":"Bar","kind":"function"}`)
	if err != nil {
		t.Fatalf("repo.search kind function failed: %v", err)
	}
	if !strings.Contains(result.Content, "bar.go:1-3  func Bar(a, b int) int") {
		t.Fatalf("Content missing header:\n%s", result.Content)
	}
	if strings.Contains(result.Content, "return a + b") {
		t.Fatalf("function body should not be emitted:\n%s", result.Content)
	}
	if !strings.Contains(result.Content, "file.read bar.go:1-3") {
		t.Fatalf("Content missing file.read hint:\n%s", result.Content)
	}
}

func TestRepoSearchKindRejectsContext(t *testing.T) {
	root := t.TempDir()
	reg := newKindSearchRegistry(t, root, nil)
	if _, err := invokeTool(t, reg, "repo.search", `{"query":"x","kind":"type","context":1}`); err == nil {
		t.Fatal("expected error when context is set with kind")
	} else if !strings.Contains(err.Error(), "context does not apply when kind is set") {
		t.Fatalf("error = %v, want it to mention context does not apply", err)
	}
}

func TestRepoSearchKindRejectsRegexMode(t *testing.T) {
	root := t.TempDir()
	reg := newKindSearchRegistry(t, root, nil)
	if _, err := invokeTool(t, reg, "repo.search", `{"query":"x","kind":"type","mode":"regex"}`); err == nil {
		t.Fatal("expected error when mode is set with kind")
	} else if !strings.Contains(err.Error(), "mode does not apply when kind is set") {
		t.Fatalf("error = %v, want it to mention mode does not apply", err)
	}
}

func TestRepoSearchKindInvalid(t *testing.T) {
	root := t.TempDir()
	reg := newKindSearchRegistry(t, root, nil)
	_, err := invokeTool(t, reg, "repo.search", `{"query":"x","kind":"struct"}`)
	if err == nil {
		t.Fatal("expected error for invalid kind")
	}
	for _, want := range []string{"function", "method", "type", "import"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %v, want it to name the valid set (missing %q)", err, want)
		}
	}
}

func TestRepoSearchKindNoDB(t *testing.T) {
	root := t.TempDir()
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	_, err := invokeTool(t, reg, "repo.search", `{"query":"x","kind":"type"}`)
	if err == nil {
		t.Fatal("expected error when DB not configured")
	}
	if !strings.Contains(err.Error(), "database not configured for repo.search kind queries") {
		t.Fatalf("error = %v, want database not configured", err)
	}
}

func TestRepoSearchKindZeroResults(t *testing.T) {
	root := t.TempDir()
	reg := newKindSearchRegistry(t, root, nil)
	result, err := invokeTool(t, reg, "repo.search", `{"query":"missing","kind":"type"}`)
	if err != nil {
		t.Fatalf("repo.search kind zero-results failed: %v", err)
	}
	if result.Summary != "No matching symbols" {
		t.Fatalf("Summary = %q, want %q", result.Summary, "No matching symbols")
	}
	if result.Content != "No matching symbols — run repo.index first if the index may be missing" {
		t.Fatalf("Content = %q", result.Content)
	}
	if result.Notice != nil {
		t.Fatalf("Notice = %+v, want nil (kind queries do not coach)", result.Notice)
	}
}

// kindFilterSymbols seeds four function symbols spread across two
// directories so path/include filters are observable and cannot pass
// vacuously. LineStart == LineEnd keeps each skeleton header-only, so no
// fixture files need to back the symbol bodies.
func kindFilterSymbols() []db.Symbol {
	return []db.Symbol{
		{FilePath: "alpha/a.go", Kind: "function", Name: "NeedleAlpha", Signature: "func NeedleAlpha()", LineStart: 1, LineEnd: 1},
		{FilePath: "alpha/a_test.go", Kind: "function", Name: "NeedleAlphaTest", Signature: "func NeedleAlphaTest()", LineStart: 1, LineEnd: 1},
		{FilePath: "beta/b.go", Kind: "function", Name: "NeedleBeta", Signature: "func NeedleBeta()", LineStart: 1, LineEnd: 1},
		{FilePath: "beta/b_test.go", Kind: "function", Name: "NeedleBetaTest", Signature: "func NeedleBetaTest()", LineStart: 1, LineEnd: 1},
	}
}

// kindFilterRoot creates a workspace whose alpha/ and beta/ directories
// exist, so resolveReadToolPath can resolve a path filter.
func kindFilterRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, dir := range []string{"alpha", "beta"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatalf("MkdirAll %s: %v", dir, err)
		}
	}
	return root
}

func TestRepoSearchKindPathFilter(t *testing.T) {
	reg := newKindSearchRegistry(t, kindFilterRoot(t), kindFilterSymbols())

	result, err := invokeTool(t, reg, "repo.search", `{"query":"Needle","kind":"function","path":"alpha"}`)
	if err != nil {
		t.Fatalf("repo.search kind path filter failed: %v", err)
	}
	for _, want := range []string{"alpha/a.go:1-1", "alpha/a_test.go:1-1"} {
		if !strings.Contains(result.Content, want) {
			t.Fatalf("Content missing %q:\n%s", want, result.Content)
		}
	}
	if strings.Contains(result.Content, "beta/") {
		t.Fatalf("path filter should have excluded beta/ symbols:\n%s", result.Content)
	}
	// The count must reflect the filtered set, not the unfiltered DB rows.
	if !strings.Contains(result.Summary, "found 2 symbols") {
		t.Fatalf("Summary = %q, want it to count the 2 filtered symbols", result.Summary)
	}
	if !strings.Contains(result.Summary, `path "alpha"`) {
		t.Fatalf("Summary = %q, want it to name the applied path filter", result.Summary)
	}
}

func TestRepoSearchKindIncludeGlob(t *testing.T) {
	reg := newKindSearchRegistry(t, kindFilterRoot(t), kindFilterSymbols())

	// *_test.go keeps only the test files, in both directories.
	result, err := invokeTool(t, reg, "repo.search", `{"query":"Needle","kind":"function","include":"*_test.go"}`)
	if err != nil {
		t.Fatalf("repo.search kind include filter failed: %v", err)
	}
	for _, want := range []string{"alpha/a_test.go:1-1", "beta/b_test.go:1-1"} {
		if !strings.Contains(result.Content, want) {
			t.Fatalf("Content missing %q:\n%s", want, result.Content)
		}
	}
	for _, unwanted := range []string{"alpha/a.go:", "beta/b.go:"} {
		if strings.Contains(result.Content, unwanted) {
			t.Fatalf("include *_test.go should have excluded %q:\n%s", unwanted, result.Content)
		}
	}
	if !strings.Contains(result.Summary, "found 2 symbols") {
		t.Fatalf("Summary = %q, want it to count the 2 matching files", result.Summary)
	}

	// *.go matches all four seeded files, so the run above proves the glob
	// discriminates rather than matching everything (or nothing).
	all, err := invokeTool(t, reg, "repo.search", `{"query":"Needle","kind":"function","include":"*.go"}`)
	if err != nil {
		t.Fatalf("repo.search kind include *.go failed: %v", err)
	}
	if !strings.Contains(all.Summary, "found 4 symbols") {
		t.Fatalf("Summary = %q, want it to count all 4 symbols", all.Summary)
	}
}

func TestRepoSearchKindFilterMatchesNothing(t *testing.T) {
	reg := newKindSearchRegistry(t, kindFilterRoot(t), kindFilterSymbols())

	// Both filters together exclude every seeded symbol: the query itself
	// still matches, so the empty result must be attributable to filtering.
	result, err := invokeTool(t, reg, "repo.search", `{"query":"Needle","kind":"function","path":"alpha","include":"*_nope.go"}`)
	if err != nil {
		t.Fatalf("over-filtered kind query should not error: %v", err)
	}
	if !strings.Contains(result.Summary, "No matching symbols") {
		t.Fatalf("Summary = %q, want a clean no-match summary", result.Summary)
	}
	for _, want := range []string{`path "alpha"`, "*_nope.go"} {
		if !strings.Contains(result.Content, want) {
			t.Fatalf("Content = %q, want it to name the filter %q so the model can tell it over-filtered", result.Content, want)
		}
	}
	if result.Notice != nil {
		t.Fatalf("Notice = %+v, want nil (kind queries do not coach)", result.Notice)
	}
}

// manyKindFilterSymbols seeds n function symbols under alpha/ so a filtered
// kind query can exceed the default result limit. LineStart == LineEnd keeps
// every skeleton header-only, so no fixture files need to back the bodies.
func manyKindFilterSymbols(n int) []db.Symbol {
	symbols := make([]db.Symbol, 0, n)
	for i := 0; i < n; i++ {
		symbols = append(symbols, db.Symbol{
			FilePath:  fmt.Sprintf("alpha/f%03d.go", i),
			Kind:      "function",
			Name:      fmt.Sprintf("Needle%03d", i),
			Signature: fmt.Sprintf("func Needle%03d()", i),
			LineStart: 1,
			LineEnd:   1,
		})
	}
	return symbols
}

func TestRepoSearchKindFilterDisclosesPostFilterTrim(t *testing.T) {
	// 60 symbols match under alpha/ and the default limit is 50. The DB scan
	// completes (60 < the 200 over-fetch ceiling), so only the post-filter trim
	// makes the result incomplete — the trim must be disclosed rather than
	// silently dropping 10 matches and implying completeness.
	reg := newKindSearchRegistry(t, kindFilterRoot(t), manyKindFilterSymbols(60))

	result, err := invokeTool(t, reg, "repo.search", `{"query":"Needle","kind":"function","path":"alpha"}`)
	if err != nil {
		t.Fatalf("repo.search kind trim failed: %v", err)
	}
	if !strings.Contains(result.Summary, "found 50 symbols") {
		t.Fatalf("Summary = %q, want the trimmed count of 50", result.Summary)
	}
	if !strings.Contains(result.Content, "more matching symbols may exist") {
		t.Fatalf("Content missing incompleteness disclosure after the post-filter trim:\n%s", result.Content)
	}
}

func TestRepoSearchKindFilterNoDisclosureWhenComplete(t *testing.T) {
	// Two symbols match under alpha/, well under the limit, and the DB scan was
	// complete: the incompleteness disclaimer must NOT appear, so that its
	// presence in the trim test above stays meaningful rather than always-on.
	reg := newKindSearchRegistry(t, kindFilterRoot(t), kindFilterSymbols())

	result, err := invokeTool(t, reg, "repo.search", `{"query":"Needle","kind":"function","path":"alpha"}`)
	if err != nil {
		t.Fatalf("repo.search kind complete filter failed: %v", err)
	}
	if !strings.Contains(result.Summary, "found 2 symbols") {
		t.Fatalf("Summary = %q, want the 2 matching symbols", result.Summary)
	}
	if strings.Contains(result.Content, "more matching symbols may exist") {
		t.Fatalf("a complete filtered result must not carry the incompleteness disclaimer:\n%s", result.Content)
	}
}

func TestRepoSearchKindFilterDisclosesTruncatedScan(t *testing.T) {
	// 300 symbols total but only 10 under alpha/. The DB over-fetch (200 rows)
	// stops before exhausting the table, so even though the filtered result (10)
	// is under the limit the caller must still be told the scan was cut short.
	symbols := manyKindFilterSymbols(10)
	for i := 0; i < 290; i++ {
		symbols = append(symbols, db.Symbol{
			FilePath:  fmt.Sprintf("beta/f%03d.go", i),
			Kind:      "function",
			Name:      fmt.Sprintf("Needle%03d", i),
			Signature: fmt.Sprintf("func Needle%03d()", i),
			LineStart: 1,
			LineEnd:   1,
		})
	}
	reg := newKindSearchRegistry(t, kindFilterRoot(t), symbols)

	result, err := invokeTool(t, reg, "repo.search", `{"query":"Needle","kind":"function","path":"alpha"}`)
	if err != nil {
		t.Fatalf("repo.search kind truncated-scan filter failed: %v", err)
	}
	if !strings.Contains(result.Summary, "found 10 symbols") {
		t.Fatalf("Summary = %q, want the 10 alpha/ symbols", result.Summary)
	}
	if !strings.Contains(result.Content, "more matching symbols may exist") {
		t.Fatalf("Content missing disclosure for a truncated DB scan:\n%s", result.Content)
	}
}

func TestRepoSearchKindUnfilteredDisclosesCapping(t *testing.T) {
	// An UNFILTERED kind query: fetchLimit equals the default limit (50), so a
	// stub DB (the real in-memory index) returning exactly 50 rows cannot be
	// distinguished from a scan the DB LIMIT cut short. The result must disclose
	// the possible truncation rather than silently claiming completeness.
	reg := newKindSearchRegistry(t, kindFilterRoot(t), manyKindFilterSymbols(50))

	result, err := invokeTool(t, reg, "repo.search", `{"query":"Needle","kind":"function"}`)
	if err != nil {
		t.Fatalf("repo.search unfiltered kind query failed: %v", err)
	}
	if !strings.Contains(result.Summary, "found 50 symbols") {
		t.Fatalf("Summary = %q, want the 50 returned symbols", result.Summary)
	}
	if !strings.Contains(result.Summary, "(capped)") {
		t.Fatalf("Summary = %q, want it to mark capping like the line-search path", result.Summary)
	}
	if !strings.Contains(result.Content, "more matching symbols may exist") {
		t.Fatalf("Content missing incompleteness disclosure for an unfiltered capped kind query:\n%s", result.Content)
	}
	if !strings.Contains(result.Content, "result capped at 50 symbols") {
		t.Fatalf("Content missing capped footer:\n%s", result.Content)
	}
}

func TestRepoSearchKindUnfilteredNoDisclosureWhenComplete(t *testing.T) {
	// 49 symbols, one under the default limit, so the DB scan provably
	// finished: the incompleteness disclaimer must NOT appear, keeping its
	// presence in the capped test above meaningful rather than always-on.
	reg := newKindSearchRegistry(t, kindFilterRoot(t), manyKindFilterSymbols(49))

	result, err := invokeTool(t, reg, "repo.search", `{"query":"Needle","kind":"function"}`)
	if err != nil {
		t.Fatalf("repo.search complete unfiltered kind query failed: %v", err)
	}
	if !strings.Contains(result.Summary, "found 49 symbols") {
		t.Fatalf("Summary = %q, want the 49 returned symbols", result.Summary)
	}
	if strings.Contains(result.Summary, "(capped)") {
		t.Fatalf("a complete unfiltered result must not be marked capped: %q", result.Summary)
	}
	if strings.Contains(result.Content, "more matching symbols may exist") {
		t.Fatalf("a complete unfiltered result must not carry the incompleteness disclaimer:\n%s", result.Content)
	}
}

func TestRepoSearchKindPathOutsideWorkspaceIsInformative(t *testing.T) {
	// A path that resolves into an additional root (the linked-worktree /
	// multi-root case) is a valid read path, but the symbol index stores only
	// workspace-relative paths, so it can match nothing. The call must return
	// an explanatory no-match rather than the old hard error, which was also
	// inconsistent with the same query run without kind.
	root := kindFilterRoot(t)
	extra := t.TempDir()
	reg := newKindSearchRegistryWithRoots(t, root, []string{extra}, kindFilterSymbols())

	rel := "../" + filepath.Base(extra)
	result, err := invokeTool(t, reg, "repo.search", fmt.Sprintf(`{"query":"Needle","kind":"function","path":%q}`, rel))
	if err != nil {
		t.Fatalf("kind query with an out-of-workspace path must not error: %v", err)
	}
	if !strings.Contains(result.Summary, "No matching symbols") {
		t.Fatalf("Summary = %q, want an informative no-match summary", result.Summary)
	}
	if !strings.Contains(result.Content, "outside the indexed workspace") {
		t.Fatalf("Content = %q, want it to explain the path is outside the indexed workspace", result.Content)
	}
	if result.Notice != nil {
		t.Fatalf("Notice = %+v, want nil", result.Notice)
	}
}

func TestRepoSearchAutoModeInvalidRegexFallsBackToSubstring(t *testing.T) {
	root := t.TempDir()
	// The literal query text is present, so a substring fallback must find it.
	writeFile(t, filepath.Join(root, "a.txt"), "n[0-9 here\n")
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	// "n[0-9" is pattern-shaped (it contains "[") but does not compile as
	// RE2. Auto mode must degrade to substring rather than failing the call.
	result, err := invokeTool(t, reg, "repo.search", `{"query":"n[0-9"}`)
	if err != nil {
		t.Fatalf("auto mode should degrade softly for an uncompilable pattern, got error: %v", err)
	}
	if !strings.Contains(result.Summary, "(mode: substring)") {
		t.Fatalf("Summary = %q, want it to echo (mode: substring) after the fallback", result.Summary)
	}
	if !strings.Contains(result.Content, "a.txt:1:n[0-9 here") {
		t.Fatalf("Content missing the literal substring match:\n%s", result.Content)
	}
}
