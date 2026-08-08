// internal/pipeline/ops_test.go
package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestApplyPatchOpSuccess(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "foo.go")
	os.WriteFile(path, []byte("package foo\n\nold line\n"), 0o644)

	op := &PatchOp{
		File:    "foo.go",
		Search:  "old line",
		Replace: "new line",
		Status:  OpExecutable,
	}
	if err := applyPatchOp(dir, op); err != nil {
		t.Fatalf("applyPatchOp: %v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "package foo\n\nnew line\n" {
		t.Errorf("file content = %q, want %q", got, "package foo\n\nnew line\n")
	}
}

func TestApplyPatchOpNoMatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "foo.go")
	os.WriteFile(path, []byte("package foo\n"), 0o644)

	op := &PatchOp{
		File:    "foo.go",
		Search:  "nonexistent",
		Replace: "whatever",
		Status:  OpExecutable,
	}
	err := applyPatchOp(dir, op)
	if err == nil {
		t.Fatal("applyPatchOp should error when search block not found")
	}
}

func TestApplyPatchOpAmbiguousMatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "foo.go")
	os.WriteFile(path, []byte("dup\nfoo\ndup\n"), 0o644)

	op := &PatchOp{
		File:    "foo.go",
		Search:  "dup",
		Replace: "unique",
		Status:  OpExecutable,
	}
	err := applyPatchOp(dir, op)
	if err == nil {
		t.Fatal("applyPatchOp should error on ambiguous match")
	}
}

func TestApplyFileOpCreateNew(t *testing.T) {
	dir := t.TempDir()
	op := &FileOp{
		Path:    "new.go",
		Content: "package new\n",
		Status:  OpExecutable,
	}
	if err := applyFileOp(dir, op); err != nil {
		t.Fatalf("applyFileOp: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "new.go"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "package new\n" {
		t.Errorf("content = %q, want %q", got, "package new\n")
	}
}

func TestApplyFileOpRefusesOverwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "exists.go")
	os.WriteFile(path, []byte("package exists\n"), 0o644)

	op := &FileOp{
		Path:    "exists.go",
		Content: "package new\n",
		Replace: false,
		Status:  OpExecutable,
	}
	err := applyFileOp(dir, op)
	if err == nil {
		t.Fatal("applyFileOp should refuse to overwrite without Replace=true")
	}
}

func TestApplyFileOpOverwriteWithReplace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "exists.go")
	os.WriteFile(path, []byte("package old\n"), 0o644)

	op := &FileOp{
		Path:    "exists.go",
		Content: "package new\n",
		Replace: true,
		Status:  OpExecutable,
	}
	if err := applyFileOp(dir, op); err != nil {
		t.Fatalf("applyFileOp with Replace: %v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "package new\n" {
		t.Errorf("content = %q, want %q", got, "package new\n")
	}
}

func TestRunOpPreparePhase(t *testing.T) {
	dir := t.TempDir()
	runner := NewFakeCommandRunner()
	runner.SetOutput("echo hello", "hello\n")

	op := &RunOp{
		Command:    []string{"echo", "hello"},
		Phase:      "prepare",
		ExpectExit: 0,
		Status:     OpExecutable,
	}
	if err := runRunOp(context.Background(), dir, runner, op); err != nil {
		t.Fatalf("runRunOp: %v", err)
	}
	if len(runner.Calls) != 1 {
		t.Errorf("calls = %d, want 1", len(runner.Calls))
	}
}

func TestRunOpNonZeroExitFails(t *testing.T) {
	dir := t.TempDir()
	runner := NewFakeCommandRunner()
	runner.SetError("go test ./...", "FAIL", exitErr(1))

	op := &RunOp{
		Command:    []string{"go", "test", "./..."},
		Phase:      "prepare",
		ExpectExit: 0,
		Status:     OpExecutable,
	}
	err := runRunOp(context.Background(), dir, runner, op)
	if err == nil {
		t.Fatal("runRunOp should error on non-zero exit")
	}
}

func TestAssertFileExistsPass(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "exists.go"), []byte("x"), 0o644)

	op := &AssertOp{
		Kind:   AssertFileExists,
		File:   "exists.go",
		Status: OpExecutable,
	}
	if err := checkAssert(context.Background(), dir, NewFakeCommandRunner(), op); err != nil {
		t.Fatalf("checkAssert: %v", err)
	}
}

func TestAssertFileExistsFail(t *testing.T) {
	dir := t.TempDir()
	op := &AssertOp{
		Kind:   AssertFileExists,
		File:   "missing.go",
		Status: OpExecutable,
	}
	err := checkAssert(context.Background(), dir, NewFakeCommandRunner(), op)
	if err == nil {
		t.Fatal("checkAssert should error for missing file")
	}
}

func TestAssertFileMatchesPass(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "foo.go"), []byte("package foo\n"), 0o644)

	op := &AssertOp{
		Kind:    AssertFileMatches,
		File:    "foo.go",
		Content: "package foo",
		Status:  OpExecutable,
	}
	if err := checkAssert(context.Background(), dir, NewFakeCommandRunner(), op); err != nil {
		t.Fatalf("checkAssert: %v", err)
	}
}

func TestAssertFileMatchesFail(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "foo.go"), []byte("package bar\n"), 0o644)

	op := &AssertOp{
		Kind:    AssertFileMatches,
		File:    "foo.go",
		Content: "package foo",
		Status:  OpExecutable,
	}
	err := checkAssert(context.Background(), dir, NewFakeCommandRunner(), op)
	if err == nil {
		t.Fatal("checkAssert should error when content does not match")
	}
}

func TestAssertTestPasses(t *testing.T) {
	dir := t.TempDir()
	runner := NewFakeCommandRunner()
	runner.SetOutput("go test ./...", "ok\n")

	op := &AssertOp{
		Kind:    AssertTestPasses,
		Command: []string{"go", "test", "./..."},
		Status:  OpExecutable,
	}
	if err := checkAssert(context.Background(), dir, runner, op); err != nil {
		t.Fatalf("checkAssert: %v", err)
	}
}

func TestAssertTestPassesFail(t *testing.T) {
	dir := t.TempDir()
	runner := NewFakeCommandRunner()
	runner.SetError("go test ./...", "FAIL", exitErr(1))

	op := &AssertOp{
		Kind:    AssertTestPasses,
		Command: []string{"go", "test", "./..."},
		Status:  OpExecutable,
	}
	err := checkAssert(context.Background(), dir, runner, op)
	if err == nil {
		t.Fatal("checkAssert should error when test fails")
	}
}

func TestPreflightOpsMarksExecutablePatch(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "foo.go"), []byte("package foo\n\nold line\n"), 0o644)

	op := &PatchOp{
		File:    "foo.go",
		Search:  "old line",
		Replace: "new line",
	}
	diags := preflightOps(dir, []Operation{op})
	if len(diags) != 0 {
		t.Errorf("unexpected diagnostics: %v", diags)
	}
	if op.Status != OpExecutable {
		t.Errorf("Status = %q, want %q", op.Status, OpExecutable)
	}
}

func TestPreflightOpsMarksBlockedPatch(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "foo.go"), []byte("package foo\n"), 0o644)

	op := &PatchOp{
		File:    "foo.go",
		Search:  "nonexistent",
		Replace: "whatever",
	}
	diags := preflightOps(dir, []Operation{op})
	if len(diags) == 0 {
		t.Error("expected diagnostics for blocked patch")
	}
	if op.Status != OpBlocked {
		t.Errorf("Status = %q, want %q", op.Status, OpBlocked)
	}
}

func TestPreflightOpsMarksBlockedFileOverwrite(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "exists.go"), []byte("x"), 0o644)

	op := &FileOp{
		Path:    "exists.go",
		Content: "y",
		Replace: false,
	}
	diags := preflightOps(dir, []Operation{op})
	if len(diags) == 0 {
		t.Error("expected diagnostics for blocked file overwrite")
	}
	if op.Status != OpBlocked {
		t.Errorf("Status = %q, want %q", op.Status, OpBlocked)
	}
}

type exitErr int

func (e exitErr) Error() string { return "exit status 1" }
func (exitErr) ExitCode() int   { return 1 }
