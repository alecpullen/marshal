// internal/pipeline/ops_test.go
package pipeline

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
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

func TestApplyFileOpPreservesExecutableBit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "script.sh")

	// First write the file with execute permissions.
	op := &FileOp{
		Path:    "script.sh",
		Content: "#!/bin/sh\necho hi\n",
	}
	if err := applyFileOp(dir, op); err != nil {
		t.Fatalf("applyFileOp: %v", err)
	}

	// Manually set the executable bit (simulating a file that had +x).
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	// Now overwrite with Replace=true — the executable bit should be preserved.
	opReplace := &FileOp{
		Path:    "script.sh",
		Content: "#!/bin/sh\necho bye\n",
		Replace: true,
	}
	if err := applyFileOp(dir, opReplace); err != nil {
		t.Fatalf("applyFileOp replace: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	mode := info.Mode().Perm()
	if mode != 0o755 {
		t.Fatalf("file mode = %o, want 0o755 (executable bit stripped)", mode)
	}
}

func TestApplyPatchOpPreservesExecutableBit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "script.sh")

	// Write an executable file.
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho old\n"), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Apply a patch — the executable bit should be preserved.
	op := &PatchOp{
		File:    "script.sh",
		Search:  "echo old",
		Replace: "echo new",
	}
	if err := applyPatchOp(dir, op); err != nil {
		t.Fatalf("applyPatchOp: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	mode := info.Mode().Perm()
	if mode != 0o755 {
		t.Fatalf("file mode = %o, want 0o755 (executable bit stripped)", mode)
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
	if err := runRunOp(context.Background(), dir, Verifier{Runner: runner}, op); err != nil {
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
	err := runRunOp(context.Background(), dir, Verifier{Runner: runner}, op)
	if err == nil {
		t.Fatal("runRunOp should error on non-zero exit")
	}
}

func TestRunOpExpectedNonZeroExitSucceeds(t *testing.T) {
	dir := t.TempDir()
	runner := NewFakeCommandRunner()
	runner.SetError("grep -q pattern missing.txt", "", exitErr(1))

	op := &RunOp{
		Command:    []string{"grep", "-q", "pattern", "missing.txt"},
		Phase:      "verify",
		ExpectExit: 1,
		Status:     OpExecutable,
	}
	if err := runRunOp(context.Background(), dir, Verifier{Runner: runner}, op); err != nil {
		t.Fatalf("runRunOp should accept expected exit 1: %v", err)
	}
}

func TestRunOpUnexpectedZeroExitFails(t *testing.T) {
	dir := t.TempDir()
	runner := NewFakeCommandRunner()
	runner.SetOutput("grep -q pattern file.txt", "")

	op := &RunOp{
		Command:    []string{"grep", "-q", "pattern", "file.txt"},
		Phase:      "verify",
		ExpectExit: 1,
		Status:     OpExecutable,
	}
	if err := runRunOp(context.Background(), dir, Verifier{Runner: runner}, op); err == nil {
		t.Fatal("runRunOp should error when command succeeds but exit 1 was expected")
	}
}

func TestRunVerifyOpsExecutesVerifyPhase(t *testing.T) {
	dir := t.TempDir()
	runner := NewFakeCommandRunner()
	runner.SetOutput("go vet ./...", "")

	ops := []Operation{
		&RunOp{Command: []string{"go", "build", "./..."}, Phase: "prepare", Status: OpExecutable},
		&RunOp{Command: []string{"go", "vet", "./..."}, Phase: "verify", Status: OpExecutable},
	}
	res := runVerifyOps(context.Background(), dir, Verifier{Runner: runner}, ops)
	if !res.OK {
		t.Fatalf("runVerifyOps failed: %s", res.Output)
	}
	if len(runner.Calls) != 1 || runner.Calls[0] != "go vet ./..." {
		t.Errorf("calls = %v, want [go vet ./...]", runner.Calls)
	}
}

func TestRunVerifyOpsReturnsFirstFailure(t *testing.T) {
	dir := t.TempDir()
	runner := NewFakeCommandRunner()
	runner.SetError("go vet ./...", "vet failure", exitErr(1))

	ops := []Operation{
		&RunOp{Command: []string{"go", "vet", "./..."}, Phase: "verify", Status: OpExecutable},
	}
	res := runVerifyOps(context.Background(), dir, Verifier{Runner: runner}, ops)
	if res.OK {
		t.Fatal("runVerifyOps should report failure")
	}
	if res.FailedCommand != "go vet ./..." {
		t.Errorf("FailedCommand = %q, want go vet ./...", res.FailedCommand)
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
	if err := checkAssert(context.Background(), dir, Verifier{Runner: NewFakeCommandRunner()}, op); err != nil {
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
	err := checkAssert(context.Background(), dir, Verifier{Runner: NewFakeCommandRunner()}, op)
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
	if err := checkAssert(context.Background(), dir, Verifier{Runner: NewFakeCommandRunner()}, op); err != nil {
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
	err := checkAssert(context.Background(), dir, Verifier{Runner: NewFakeCommandRunner()}, op)
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
	if err := checkAssert(context.Background(), dir, Verifier{Runner: runner}, op); err != nil {
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
	err := checkAssert(context.Background(), dir, Verifier{Runner: runner}, op)
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

// symlinkOrSkip creates link -> target, skipping the test on platforms or
// filesystems where symlinks are unavailable.
func symlinkOrSkip(t *testing.T, target, link string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on Windows")
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
}

// relTo builds the relative spelling that reaches target from dir. Tests
// assert on file contents and existence, never on the absolute spelling.
func relTo(t *testing.T, dir, target string) string {
	t.Helper()
	rel, err := filepath.Rel(dir, target)
	if err != nil {
		t.Fatalf("filepath.Rel(%q, %q): %v", dir, target, err)
	}
	return rel
}

func TestPreflightOpsBlocksEscapingFilePath(t *testing.T) {
	dir := t.TempDir()
	op := &FileOp{Path: "../outside.txt", Content: "pwned", Replace: true}
	diags := preflightOps(dir, []Operation{op})
	if op.Status != OpBlocked {
		t.Fatalf("Status = %q, want %q", op.Status, OpBlocked)
	}
	requireErrorDiagnostic(t, diags, "../outside.txt")
}

func TestPreflightOpsBlocksEscapingPatchPath(t *testing.T) {
	dir := t.TempDir()
	op := &PatchOp{File: "../outside.txt", Search: "a", Replace: "b"}
	diags := preflightOps(dir, []Operation{op})
	if op.Status != OpBlocked {
		t.Fatalf("Status = %q, want %q", op.Status, OpBlocked)
	}
	requireErrorDiagnostic(t, diags, "../outside.txt")
}

func TestPreflightOpsBlocksAbsoluteFilePath(t *testing.T) {
	dir := t.TempDir()
	op := &FileOp{Path: "/etc/passwd", Content: "pwned", Replace: true}
	diags := preflightOps(dir, []Operation{op})
	if op.Status != OpBlocked {
		t.Fatalf("Status = %q, want %q", op.Status, OpBlocked)
	}
	requireErrorDiagnostic(t, diags, "/etc/passwd")
}

func TestPreflightOpsBlocksSymlinkEscapingFilePath(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "victim.txt"), []byte("secret\n"), 0o644); err != nil {
		t.Fatalf("write victim: %v", err)
	}
	symlinkOrSkip(t, outside, filepath.Join(dir, "link"))

	op := &FileOp{Path: "link/victim.txt", Content: "pwned", Replace: true}
	diags := preflightOps(dir, []Operation{op})
	if op.Status != OpBlocked {
		t.Fatalf("Status = %q, want %q", op.Status, OpBlocked)
	}
	requireErrorDiagnostic(t, diags, "link/victim.txt")
}

func TestPreflightOpsBlocksSymlinkEscapeToNewFile(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	symlinkOrSkip(t, outside, filepath.Join(dir, "link"))

	op := &FileOp{Path: "link/new.txt", Content: "pwned"}
	diags := preflightOps(dir, []Operation{op})
	if op.Status != OpBlocked {
		t.Fatalf("Status = %q, want %q", op.Status, OpBlocked)
	}
	requireErrorDiagnostic(t, diags, "link/new.txt")
}

func TestPreflightOpsAllowsSymlinkInsideWorktree(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatalf("mkdir real: %v", err)
	}
	symlinkOrSkip(t, real, filepath.Join(dir, "inner"))

	op := &FileOp{Path: "inner/new.txt", Content: "hello\n"}
	diags := preflightOps(dir, []Operation{op})
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if op.Status != OpExecutable {
		t.Fatalf("Status = %q, want %q", op.Status, OpExecutable)
	}
}

func TestPreflightOpsAllowsNormalizedWorkspaceRelativePath(t *testing.T) {
	dir := t.TempDir()
	op := &FileOp{Path: "pkg/../ok.txt", Content: "hello\n"}
	diags := preflightOps(dir, []Operation{op})
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if op.Status != OpExecutable {
		t.Fatalf("Status = %q, want %q", op.Status, OpExecutable)
	}
}

func TestApplyFileOpRejectsEscapingPath(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	victim := filepath.Join(outside, "victim.txt")
	if err := os.WriteFile(victim, []byte("original\n"), 0o644); err != nil {
		t.Fatalf("write victim: %v", err)
	}

	op := &FileOp{Path: relTo(t, dir, victim), Content: "pwned\n", Replace: true}
	if err := applyFileOp(dir, op); err == nil {
		t.Fatal("applyFileOp should reject a path outside the worktree")
	}
	got, err := os.ReadFile(victim)
	if err != nil {
		t.Fatalf("read victim: %v", err)
	}
	if string(got) != "original\n" {
		t.Fatalf("outside file was mutated: %q", got)
	}
}

func TestApplyFileOpRejectsAbsolutePath(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "created.txt")

	op := &FileOp{Path: target, Content: "pwned\n"}
	if err := applyFileOp(dir, op); err == nil {
		t.Fatal("applyFileOp should reject an absolute path")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("absolute target was created: stat err = %v", err)
	}
}

func TestApplyPatchOpRejectsEscapingPath(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	victim := filepath.Join(outside, "victim.txt")
	if err := os.WriteFile(victim, []byte("original\n"), 0o644); err != nil {
		t.Fatalf("write victim: %v", err)
	}

	op := &PatchOp{File: relTo(t, dir, victim), Search: "original", Replace: "pwned", Status: OpExecutable}
	if err := applyPatchOp(dir, op); err == nil {
		t.Fatal("applyPatchOp should reject a path outside the worktree")
	}
	got, err := os.ReadFile(victim)
	if err != nil {
		t.Fatalf("read victim: %v", err)
	}
	if string(got) != "original\n" {
		t.Fatalf("outside file was mutated: %q", got)
	}
}

func TestApplyFileOpRejectsSymlinkEscape(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	victim := filepath.Join(outside, "victim.txt")
	if err := os.WriteFile(victim, []byte("original\n"), 0o644); err != nil {
		t.Fatalf("write victim: %v", err)
	}
	symlinkOrSkip(t, outside, filepath.Join(dir, "link"))

	op := &FileOp{Path: "link/victim.txt", Content: "pwned\n", Replace: true}
	if err := applyFileOp(dir, op); err == nil {
		t.Fatal("applyFileOp should reject a symlink escape")
	}
	got, err := os.ReadFile(victim)
	if err != nil {
		t.Fatalf("read victim: %v", err)
	}
	if string(got) != "original\n" {
		t.Fatalf("outside file was mutated: %q", got)
	}
}

func TestApplyFileOpRejectsSymlinkFileEscape(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	victim := filepath.Join(outside, "victim.txt")
	if err := os.WriteFile(victim, []byte("original\n"), 0o644); err != nil {
		t.Fatalf("write victim: %v", err)
	}
	// The symlink is the final component, pointing at an existing file.
	symlinkOrSkip(t, victim, filepath.Join(dir, "filelink"))

	op := &FileOp{Path: "filelink", Content: "pwned\n", Replace: true}
	if err := applyFileOp(dir, op); err == nil {
		t.Fatal("applyFileOp should reject a symlink that points at an outside file")
	}
	got, err := os.ReadFile(victim)
	if err != nil {
		t.Fatalf("read victim: %v", err)
	}
	if string(got) != "original\n" {
		t.Fatalf("outside file was mutated: %q", got)
	}
}

func TestApplyPatchOpRejectsSymlinkEscape(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	victim := filepath.Join(outside, "victim.txt")
	if err := os.WriteFile(victim, []byte("original\n"), 0o644); err != nil {
		t.Fatalf("write victim: %v", err)
	}
	symlinkOrSkip(t, outside, filepath.Join(dir, "link"))

	op := &PatchOp{File: "link/victim.txt", Search: "original", Replace: "pwned", Status: OpExecutable}
	if err := applyPatchOp(dir, op); err == nil {
		t.Fatal("applyPatchOp should reject a symlink escape")
	}
	got, err := os.ReadFile(victim)
	if err != nil {
		t.Fatalf("read victim: %v", err)
	}
	if string(got) != "original\n" {
		t.Fatalf("outside file was mutated: %q", got)
	}
}

func TestApplyFileOpRejectsSymlinkEscapeToNewFile(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	symlinkOrSkip(t, outside, filepath.Join(dir, "link"))

	op := &FileOp{Path: "link/new.txt", Content: "pwned\n"}
	if err := applyFileOp(dir, op); err == nil {
		t.Fatal("applyFileOp should reject a symlink escape to a new file")
	}
	if _, err := os.Stat(filepath.Join(outside, "new.txt")); !os.IsNotExist(err) {
		t.Fatalf("outside file was created: stat err = %v", err)
	}
}

func TestApplyFileOpAllowsSymlinkInsideWorktree(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatalf("mkdir real: %v", err)
	}
	symlinkOrSkip(t, real, filepath.Join(dir, "inner"))

	op := &FileOp{Path: "inner/new.txt", Content: "hello\n"}
	if err := applyFileOp(dir, op); err != nil {
		t.Fatalf("applyFileOp: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(real, "new.txt"))
	if err != nil {
		t.Fatalf("read resolved target: %v", err)
	}
	if string(got) != "hello\n" {
		t.Fatalf("content = %q, want %q", got, "hello\n")
	}
}

func TestApplyFileOpAcceptsNormalizedWorkspaceRelativePath(t *testing.T) {
	dir := t.TempDir()

	op := &FileOp{Path: "pkg/../ok.txt", Content: "hello\n"}
	if err := applyFileOp(dir, op); err != nil {
		t.Fatalf("applyFileOp: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "ok.txt"))
	if err != nil {
		t.Fatalf("read ok.txt: %v", err)
	}
	if string(got) != "hello\n" {
		t.Fatalf("content = %q, want %q", got, "hello\n")
	}
}

func TestApplyFileOpAcceptsNestedPathWithMissingParents(t *testing.T) {
	dir := t.TempDir()

	op := &FileOp{Path: "a/b/c.txt", Content: "hello\n"}
	if err := applyFileOp(dir, op); err != nil {
		t.Fatalf("applyFileOp: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "a", "b", "c.txt"))
	if err != nil {
		t.Fatalf("read nested file: %v", err)
	}
	if string(got) != "hello\n" {
		t.Fatalf("content = %q, want %q", got, "hello\n")
	}
}

type exitErr int

func (e exitErr) Error() string { return fmt.Sprintf("exit status %d", int(e)) }
func (e exitErr) ExitCode() int { return int(e) }

// slowCommandRunner blocks until the context is cancelled or delay elapses.
type slowCommandRunner struct {
	delay time.Duration
}

func (s *slowCommandRunner) Run(ctx context.Context, dir string, argv []string) (string, error) {
	select {
	case <-time.After(s.delay):
		return "", nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func TestRunRunOpTimeoutEnforced(t *testing.T) {
	// A runner that blocks until the context is cancelled.
	runner := &slowCommandRunner{delay: 5 * time.Second}
	vr := Verifier{Runner: runner, Timeout: 50 * time.Millisecond}

	op := &RunOp{Command: []string{"sleep", "5"}}
	start := time.Now()
	err := runRunOp(context.Background(), t.TempDir(), vr, op)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("runRunOp should timeout, got nil error")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("runRunOp took %v, should have timed out after 50ms", elapsed)
	}
}
