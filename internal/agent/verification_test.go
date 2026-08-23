package agent

import "testing"

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
	tr.record("shell.run", `{"command":"mkdir scratch"}`, hashToolResult(""), true)
	if _, fire := tr.unverifiedMutation(); !fire {
		t.Fatal("non-test shell.run is a mutation and must fire the gate")
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
