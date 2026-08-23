package agent

import (
	"fmt"
	"testing"
)

func TestUnverifiedMutation_NoHistory(t *testing.T) {
	tr := newProgressTracker()
	if _, fire := tr.unverifiedMutation(); fire {
		t.Fatal("empty history must not fire the gate")
	}
}

func TestUnverifiedMutation_MutationWithoutVerification(t *testing.T) {
	tr := newProgressTracker()
	tr.record("file.read", `{"path":"a.go"}`, hashToolResult("x"), true)
	tr.record("file.write_patch", `{"patch":"p"}`, hashToolResult("applied"), true)
	last, fire := tr.unverifiedMutation()
	if !fire {
		t.Fatal("patch without verification must fire the gate")
	}
	if last.name != "file.write_patch" {
		t.Fatalf("last mutation = %q, want file.write_patch", last.name)
	}
}

func TestUnverifiedMutation_VerificationAfterMutation(t *testing.T) {
	tr := newProgressTracker()
	tr.record("file.write_patch", `{"patch":"p"}`, hashToolResult("applied"), true)
	tr.record("test.run", `{"command":"go test ./..."}`, hashToolResult("ok"), true)
	if _, fire := tr.unverifiedMutation(); fire {
		t.Fatal("verification after the mutation must satisfy the gate")
	}
}

func TestUnverifiedMutation_VerificationBeforeMutationDoesNotCount(t *testing.T) {
	tr := newProgressTracker()
	tr.record("test.run", `{"command":"go test ./..."}`, hashToolResult("ok"), true)
	tr.record("file.write_patch", `{"patch":"p"}`, hashToolResult("applied"), true)
	if _, fire := tr.unverifiedMutation(); !fire {
		t.Fatal("verification before the last mutation must not satisfy the gate")
	}
}

func TestUnverifiedMutation_TestLikeShellCountsAsVerification(t *testing.T) {
	tr := newProgressTracker()
	tr.record("file.write_patch", `{"patch":"p"}`, hashToolResult("applied"), true)
	tr.record("shell.run", `{"command":"go test ./..."}`, hashToolResult("ok"), true)
	if _, fire := tr.unverifiedMutation(); fire {
		t.Fatal("shell.run 'go test' must count as verification")
	}
}

func TestUnverifiedMutation_NonTestShellIsMutation(t *testing.T) {
	tr := newProgressTracker()
	tr.record("shell.run", `{"command":"rm -rf build"}`, hashToolResult(""), true)
	if _, fire := tr.unverifiedMutation(); !fire {
		t.Fatal("non-test, non-housekeeping shell.run is a mutation and must fire the gate")
	}
}

func TestUnverifiedMutation_FailedCallsDoNotCount(t *testing.T) {
	tr := newProgressTracker()
	tr.record("file.write_patch", `{"patch":"p"}`, hashToolResult("err"), false)
	if _, fire := tr.unverifiedMutation(); fire {
		t.Fatal("failed mutation must not arm the gate")
	}
	// A red test is a FAILED call (test.run surfaces a non-zero exit as a
	// tool error), but it must still satisfy the gate: the model ran a
	// verification command, which is what the gate checks. Otherwise an
	// honest model that runs tests and reports "tests still fail" would be
	// penalized identically to one that never verified.
	tr.record("file.write_patch", `{"patch":"p"}`, hashToolResult("applied"), true)
	tr.record("test.run", `{"command":"go test ./..."}`, hashToolResult("exit 1"), false)
	if _, fire := tr.unverifiedMutation(); fire {
		t.Fatal("a failed verification run must still satisfy the gate")
	}
}

func TestUnverifiedMutation_FailedVerificationStillSatisfiesGate(t *testing.T) {
	tr := newProgressTracker()
	tr.record("file.write", `{"path":"a.go","content":"x"}`, hashToolResult("ok"), true)
	tr.record("shell.run", `{"command":"go test ./..."}`, hashToolResult("exit 2"), false)
	if _, fire := tr.unverifiedMutation(); fire {
		t.Fatal("a failing test-like shell.run must still satisfy the gate")
	}
}

func TestLooksLikeVerificationCommandMatchesCommandFieldOnly(t *testing.T) {
	// A verification keyword in the command field counts.
	if !looksLikeVerificationCommand(`{"command":"go test ./..."}`) {
		t.Fatal("'go test ./...' must count as verification")
	}
	// A verification keyword in an UNRELATED field must not count, or a
	// mutation could masquerade as a verification while doing real work.
	if looksLikeVerificationCommand(`{"command":"rm -rf build","note":"run pytest later"}`) {
		t.Fatal("a mutation whose only 'pytest' mention is in a non-command field must not count as verification")
	}
	// Empty or non-JSON args are not verification.
	if looksLikeVerificationCommand("") || looksLikeVerificationCommand("not json") {
		t.Fatal("empty or malformed args must not count as verification")
	}
}

func TestUnverifiedMutation_DiagnosticsCheckCounts(t *testing.T) {
	tr := newProgressTracker()
	tr.record("file.write", `{"path":"a.go","content":"x"}`, hashToolResult("ok"), true)
	tr.record("diagnostics.check", `{}`, hashToolResult("clean"), true)
	if _, fire := tr.unverifiedMutation(); fire {
		t.Fatal("diagnostics.check must count as verification")
	}
}

func TestUnverifiedMutation_ReadsNeverFire(t *testing.T) {
	tr := newProgressTracker()
	tr.record("file.read", `{"path":"a.go"}`, hashToolResult("x"), true)
	tr.record("repo.search", `{"query":"foo"}`, hashToolResult("y"), true)
	if _, fire := tr.unverifiedMutation(); fire {
		t.Fatal("read-only history must not fire the gate")
	}
}

func TestLooksLikeHousekeepingCommand(t *testing.T) {
	housekeeping := []string{
		"git add .", "git commit -m msg", "git push origin main", "git fetch",
		"git pull --rebase", "git tag v1.2.3",
		"git status", "git log --oneline", "git show HEAD", "git diff --stat", "git branch -a",
		"mkdir scratch", "cp a.go b.go", "mv a.go sub/", "touch x", "chmod +x run.sh", "ln -s a b",
		"gofmt -w .", "go mod tidy", "go mod download",
		"npm install", "npm ci", "pnpm install", "pnpm add lodash",
		"yarn install", "yarn add react", "pip install requests",
	}
	for _, cmd := range housekeeping {
		if !looksLikeHousekeepingCommand(fmt.Sprintf(`{"command":%q}`, cmd)) {
			t.Errorf("%q must be housekeeping", cmd)
		}
	}
	// Working-tree-changing and verification commands are NOT housekeeping.
	notHousekeeping := []string{
		"rm -rf build", "git checkout main", "git switch feature", "git restore .",
		"go test ./...", "go generate ./...", "python codegen.py",
	}
	for _, cmd := range notHousekeeping {
		if looksLikeHousekeepingCommand(fmt.Sprintf(`{"command":%q}`, cmd)) {
			t.Errorf("%q must not be housekeeping", cmd)
		}
	}
	// Only the command field is matched; empty/malformed args are not housekeeping.
	if looksLikeHousekeepingCommand(`{"note":"git commit later"}`) {
		t.Error("a non-command field mentioning a housekeeping command must not count")
	}
	if looksLikeHousekeepingCommand("") || looksLikeHousekeepingCommand("not json") {
		t.Error("empty or malformed args must not be housekeeping")
	}
}

func TestUnverifiedMutation_HousekeepingDoesNotArmOrSatisfy(t *testing.T) {
	// Housekeeping alone never arms the gate.
	tr := newProgressTracker()
	tr.record("shell.run", `{"command":"mkdir scratch"}`, hashToolResult(""), true)
	if _, fire := tr.unverifiedMutation(); fire {
		t.Fatal("housekeeping shell.run must not arm the gate")
	}
	// Edit → verify → commit is clean: housekeeping after verification does
	// not re-arm.
	tr2 := newProgressTracker()
	tr2.record("file.write_patch", `{"patch":"p"}`, hashToolResult("applied"), true)
	tr2.record("test.run", `{"command":"go test ./..."}`, hashToolResult("ok"), true)
	tr2.record("shell.run", `{"command":"git commit -m done"}`, hashToolResult(""), true)
	if _, fire := tr2.unverifiedMutation(); fire {
		t.Fatal("git commit after a verification must not re-arm the gate")
	}
	// Housekeeping does not satisfy the gate for an earlier mutation.
	tr3 := newProgressTracker()
	tr3.record("file.write_patch", `{"patch":"p"}`, hashToolResult("applied"), true)
	tr3.record("shell.run", `{"command":"git commit -m done"}`, hashToolResult(""), true)
	if _, fire := tr3.unverifiedMutation(); !fire {
		t.Fatal("git commit is not a verification; the earlier mutation must still fire")
	}
}

func TestUnverifiedMutation_TreeChangingCommandsStillArm(t *testing.T) {
	for _, cmd := range []string{"rm -rf build", "git checkout main", "git switch feature", "git restore ."} {
		tr := newProgressTracker()
		tr.record("shell.run", fmt.Sprintf(`{"command":%q}`, cmd), hashToolResult(""), true)
		if _, fire := tr.unverifiedMutation(); !fire {
			t.Fatalf("%q changes working-tree content and must arm the gate", cmd)
		}
	}
}
