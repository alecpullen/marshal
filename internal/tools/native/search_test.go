package native

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

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

func TestRepoSearchZeroMatchRegexNamesTheLiteralHits(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.go"), "func (m Model) Update() {}\n")
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	// The parentheses are regex groups, so this valid regex matches nothing
	// even though the literal line exists. The footer must name the literal
	// hit count rather than leaving the model to guess and flee to shell grep.
	result, err := invokeTool(t, reg, "repo.search", `{"query":"func (m Model) Update","mode":"regex"}`)
	if err != nil {
		t.Fatalf("repo.search returned error: %v", err)
	}
	if !strings.Contains(result.Content, "the literal text matches 1 line(s)") {
		t.Fatalf("Content missing literal-hit coaching footer:\n%s", result.Content)
	}
	if !strings.Contains(result.Content, `retry with mode:"substring"`) {
		t.Fatalf("Content missing substring retry:\n%s", result.Content)
	}
	if result.Notice == nil || result.Notice.Kind != registry.NoticeZeroMatchCoach {
		t.Fatalf("Notice = %+v, want kind %q", result.Notice, registry.NoticeZeroMatchCoach)
	}
}

func TestRepoSearchZeroMatchRegexKeepsGenericFooterWhenLiteralAlsoMisses(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.txt"), "alpha\nbeta\n")
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	// Neither the regex nor the literal text matches: keep the generic footer
	// rather than claiming a literal hit that does not exist.
	result, err := invokeTool(t, reg, "repo.search", `{"query":"needle","mode":"regex"}`)
	if err != nil {
		t.Fatalf("repo.search returned error: %v", err)
	}
	if strings.Contains(result.Content, "the literal text matches") {
		t.Fatalf("Content claims a literal hit that does not exist:\n%s", result.Content)
	}
	if !strings.Contains(result.Content, `try mode:"substring"`) {
		t.Fatalf("Content missing substring-mode coaching footer:\n%s", result.Content)
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

func TestRepoSearchContextClamp(t *testing.T) {
	root := t.TempDir()
	// Ten numbered lines so a clamp to 3 is observable: a match on line5 with
	// three leading context lines must surface line2.
	writeFile(t, filepath.Join(root, "a.txt"), "line1\nline2\nline3\nline4\nline5\nline6\nline7\nline8\nline9\nline10\n")

	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	cases := []struct {
		name string
		// contextArg is the raw value the caller put on the wire.
		contextArg int
		// wantNotice selects the clamped vs. in-range shape.
		wantNotice bool
		// wantRequested / wantClamped are the notice payload values, only
		// meaningful when wantNotice is set.
		wantRequested int
		wantClamped   int
		// wantContentSubstr, when non-empty, must appear in Content and proves
		// the (possibly clamped) context value actually drove the search.
		wantContentSubstr string
	}{
		{
			name:              "over-cap clamps to 3 and searches 3 lines back",
			contextArg:        4,
			wantNotice:        true,
			wantRequested:     4,
			wantClamped:       3,
			wantContentSubstr: "a.txt-2-line2",
		},
		{name: "zero context is in range", contextArg: 0},
		{name: "max context is in range", contextArg: 3, wantContentSubstr: "a.txt-2-line2"},
		{name: "negative context clamps to zero", contextArg: -1, wantNotice: true, wantRequested: -1, wantClamped: 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := fmt.Sprintf(`{"query":"line5","context":%d}`, tc.contextArg)
			result, err := invokeTool(t, reg, "repo.search", args)
			if err != nil {
				t.Fatalf("repo.search returned error: %v", err)
			}

			if !tc.wantNotice {
				if result.Notice != nil {
					t.Fatalf("Notice = %+v, want nil for an in-range context", result.Notice)
				}
				if strings.Contains(result.Content, "context clamped") {
					t.Fatalf("in-range context must not carry a clamp footer:\n%s", result.Content)
				}
			} else {
				if result.Notice == nil || result.Notice.Kind != registry.NoticeContextClamped {
					t.Fatalf("Notice = %+v, want kind %q", result.Notice, registry.NoticeContextClamped)
				}
				wantFooter := fmt.Sprintf("context clamped to %d (requested %d)", tc.wantClamped, tc.wantRequested)
				if !strings.Contains(result.Content, wantFooter) {
					t.Fatalf("Content missing clamp footer %q:\n%s", wantFooter, result.Content)
				}
				// fmt.Sprint keeps the assertion agnostic to whether the Data
				// values arrive as ints or (after a JSON hop) float64.
				if got := fmt.Sprint(result.Notice.Data["requested_context"]); got != fmt.Sprint(tc.wantRequested) {
					t.Fatalf("Notice.Data[requested_context] = %v, want %v", result.Notice.Data["requested_context"], tc.wantRequested)
				}
				if got := fmt.Sprint(result.Notice.Data["clamped_context"]); got != fmt.Sprint(tc.wantClamped) {
					t.Fatalf("Notice.Data[clamped_context] = %v, want %v", result.Notice.Data["clamped_context"], tc.wantClamped)
				}
			}

			if tc.wantContentSubstr != "" && !strings.Contains(result.Content, tc.wantContentSubstr) {
				t.Fatalf("Content missing context line %q (the context window did not drive the search):\n%s", tc.wantContentSubstr, result.Content)
			}
		})
	}
}

// TestRepoSearchContextClampComposition covers the three paths a clamped
// context can be composed into: a zero-match coaching footer, a bare
// zero-match footer, and capped results. In every case the clamp footer must
// ride Content exactly once — the composed branches append it themselves, so
// this is the guard against a second unconditional append.
func TestRepoSearchContextClampComposition(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.txt"), "alpha\nbeta\nneedle 1\nneedle 2\n")

	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	cases := []struct {
		name string
		args string
		// wantNoticeKind is the kind the handler must report. The clamp notice
		// is a fallback rather than an override: when the call also composes
		// with another condition, that condition keeps the Notice slot and the
		// clamp footer still rides Content.
		wantNoticeKind string
		// wantFooters must each appear in Content exactly once.
		wantFooters []string
		// hardCap, when > 0, temporarily lowers hardSearchMaxResults to make
		// the capped path reachable cheaply.
		hardCap int
	}{
		{
			// Clamped + zero matches + regex coaching: the coaching footer owns
			// the Notice slot and both footers are present.
			name:           "clamped with zero-match coaching",
			args:           `{"query":"foo(","context":9}`,
			wantNoticeKind: registry.NoticeZeroMatchCoach,
			wantFooters: []string{
				"context clamped to 3 (requested 9)",
				"no matches; query looks like a regex",
			},
		},
		{
			// Clamped + zero matches with no coaching: the clamp footer is the
			// only signal, so it must appear in Content.
			name:           "clamped with bare zero match",
			args:           `{"query":"zzzznotfound","context":-2}`,
			wantNoticeKind: registry.NoticeContextClamped,
			wantFooters:    []string{"context clamped to 0 (requested -2)"},
		},
		{
			// Clamped + capped: the capped footer owns the Notice slot and both
			// footers are present.
			name:           "clamped with capped results",
			args:           `{"query":"needle","context":7}`,
			wantNoticeKind: registry.NoticeCappedResults,
			wantFooters: []string{
				"context clamped to 3 (requested 7)",
				"result capped at 1 matches",
			},
			hardCap: 1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.hardCap > 0 {
				old := hardSearchMaxResults
				hardSearchMaxResults = tc.hardCap
				defer func() { hardSearchMaxResults = old }()
			}

			result, err := invokeTool(t, reg, "repo.search", tc.args)
			if err != nil {
				t.Fatalf("repo.search returned error: %v", err)
			}
			if result.Notice == nil || result.Notice.Kind != tc.wantNoticeKind {
				t.Fatalf("Notice = %+v, want kind %q", result.Notice, tc.wantNoticeKind)
			}
			for _, footer := range tc.wantFooters {
				if got := strings.Count(result.Content, footer); got != 1 {
					t.Fatalf("Content has %d occurrences of %q, want exactly 1:\n%s", got, footer, result.Content)
				}
			}
		})
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

// TestRepoSearchDisclosesSkippedSymlinks pins that searching a subtree made of
// symlinks — the shape a worktree's seeded .docs-archive has — reports that
// links were skipped rather than a bare "found 0 matches" the model would read
// as "the content does not exist".
func TestRepoSearchDisclosesSkippedSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on Windows")
	}
	root := t.TempDir()
	source := t.TempDir()
	writeFile(t, filepath.Join(source, "plans", "p.md"), "needle\n")

	// A real directory holding a per-entry symlink, exactly as seeded.
	archive := filepath.Join(root, ".docs-archive")
	if err := os.MkdirAll(archive, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(source, "plans"), filepath.Join(archive, "plans")); err != nil {
		t.Fatal(err)
	}

	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	result, err := invokeTool(t, reg, "repo.search", `{"query":"needle","path":".docs-archive"}`)
	if err != nil {
		t.Fatalf("repo.search: %v", err)
	}
	if !strings.Contains(result.Content, "symlinked path") {
		t.Fatalf("Content = %q, want it to disclose skipped symlinks", result.Content)
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
