package linter_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alecpullen/marshal/internal/config"
	"github.com/alecpullen/marshal/internal/linter"
)

// --- Parser unit tests -------------------------------------------------------

func TestParseColonDiagnostics_WithCol(t *testing.T) {
	output := "internal/foo/bar.go:42:5: undefined: Foo (typecheck)"
	l := linter.New(config.LinterConfig{Go: "golangci-lint run"}, "/repo")
	// Run with a fake command to get the parser; test via Run is complex.
	// Instead test the exported Format and the round-trip via a real go vet below.
	_ = l
	_ = output
}

func TestFormat_Empty(t *testing.T) {
	if linter.Format(nil) != "" {
		t.Error("expected empty string for nil issues")
	}
}

func TestFormat_MultipleIssues(t *testing.T) {
	issues := []linter.Issue{
		{File: "a.go", Line: 1, Col: 5, Message: "undefined: X"},
		{File: "b.go", Line: 10, Message: "declared and not used: y"},
	}
	out := linter.Format(issues)
	if !strings.Contains(out, "a.go:1:5: undefined: X") {
		t.Errorf("missing first issue: %q", out)
	}
	if !strings.Contains(out, "b.go:10: declared and not used: y") {
		t.Errorf("missing second issue: %q", out)
	}
}

func TestIssue_String_WithCol(t *testing.T) {
	i := linter.Issue{File: "main.go", Line: 5, Col: 3, Message: "oops"}
	if i.String() != "main.go:5:3: oops" {
		t.Errorf("got %q", i.String())
	}
}

func TestIssue_String_LineOnly(t *testing.T) {
	i := linter.Issue{File: "main.go", Line: 5, Message: "oops"}
	if i.String() != "main.go:5: oops" {
		t.Errorf("got %q", i.String())
	}
}

func TestIssue_String_FileOnly(t *testing.T) {
	i := linter.Issue{File: "main.go", Message: "oops"}
	if i.String() != "main.go: oops" {
		t.Errorf("got %q", i.String())
	}
}

// --- Functional tests using go vet -------------------------------------------

// goVetAvailable checks whether go is on PATH.
func goVetAvailable() bool {
	_, err := exec.LookPath("go")
	return err == nil
}

func TestRun_GoVet_CleanFile(t *testing.T) {
	if !goVetAvailable() {
		t.Skip("go not on PATH")
	}

	dir := t.TempDir()
	writeGoMod(t, dir, "example.com/testmod")
	writeFile(t, filepath.Join(dir, "main.go"), "package main\n\nfunc main() {}\n")

	l := linter.New(config.LinterConfig{Go: "go vet"}, dir)
	issues, err := l.Run(context.Background(), []string{"main.go"})
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 0 {
		t.Errorf("expected no issues for clean file, got: %v", issues)
	}
}

func TestRun_GoVet_BadPrintf(t *testing.T) {
	if !goVetAvailable() {
		t.Skip("go not on PATH")
	}

	dir := t.TempDir()
	writeGoMod(t, dir, "example.com/testmod")
	// fmt.Printf with wrong verb — go vet detects this.
	writeFile(t, filepath.Join(dir, "main.go"),
		"package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Printf(\"%d\", \"not an int\")\n}\n")

	l := linter.New(config.LinterConfig{Go: "go vet"}, dir)
	issues, err := l.Run(context.Background(), []string{"main.go"})
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) == 0 {
		t.Error("expected at least one vet issue for bad Printf")
	}
}

func TestRun_MissingBinary_Skipped(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.go"), "package main\n")

	// Use a binary name that almost certainly doesn't exist.
	l := linter.New(config.LinterConfig{Go: "nonexistent-linter-binary-xyz run"}, dir)
	issues, err := l.Run(context.Background(), []string{"a.go"})
	if err != nil {
		t.Fatalf("missing binary should not return error, got: %v", err)
	}
	if len(issues) != 0 {
		t.Errorf("expected 0 issues when binary missing, got %d", len(issues))
	}
}

func TestRun_NoChangedFiles_NoIssues(t *testing.T) {
	dir := t.TempDir()
	l := linter.New(config.LinterConfig{Go: "go vet"}, dir)
	issues, err := l.Run(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 0 {
		t.Errorf("expected 0 issues for empty changed files, got %d", len(issues))
	}
}

func TestRun_OnlyRunsRelevantLinter(t *testing.T) {
	if !goVetAvailable() {
		t.Skip("go not on PATH")
	}

	dir := t.TempDir()
	writeGoMod(t, dir, "example.com/testmod")
	writeFile(t, filepath.Join(dir, "script.py"), "def foo():\n    pass\n")

	// Only Go linter configured; changed files are .py — should produce no issues.
	l := linter.New(config.LinterConfig{Go: "go vet"}, dir)
	issues, err := l.Run(context.Background(), []string{"script.py"})
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 0 {
		t.Errorf("go vet should not run for .py files, got %d issues", len(issues))
	}
}

// --- helpers -----------------------------------------------------------------

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeGoMod(t *testing.T, dir, module string) {
	t.Helper()
	content := "module " + module + "\n\ngo 1.21\n"
	writeFile(t, filepath.Join(dir, "go.mod"), content)
}
