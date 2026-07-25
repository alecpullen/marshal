package agent

import "testing"

// Regression tests for the mutating-tool-reset behavior in progressTracker:
// a FAILING file.write_patch/shell.run call must NOT reset repeat counts
// (only a SUCCESSFUL mutating call changes state and should reset them).
// Without this, identical futile tool calls never trip loop detection —
// see docs/superpowers/plans/2026-07-26-agent-loop-reliability-fixes.md.

func TestFailingPatchAccumulatesRepeatCountAndEscalates(t *testing.T) {
	tr := newProgressTracker()
	failHash := hashToolResult("patch validation failed: SEARCH block not found")
	args := `{"patch":"same-bad-patch"}`

	var lastReminder string
	for i := 1; i <= repeatHardStall; i++ {
		count := tr.record("file.write_patch", args, failHash, false)
		if count != i {
			t.Fatalf("iteration %d: count = %d, want %d (failures must accumulate)", i, count, i)
		}
		lastReminder = repeatReminder(count, "file.write_patch", args)
	}
	if lastReminder == "" {
		t.Fatal("expected a reminder once the repeat count reached repeatHardStall")
	}
	if got := tr.assess(); got != assessHardStall {
		t.Fatalf("assess() = %v, want assessHardStall after %d identical failures", got, repeatHardStall)
	}
}

func TestPatchTestDoomLoopEventuallyStalls(t *testing.T) {
	tr := newProgressTracker()
	patchFail := hashToolResult("parse patch error")
	testFail := hashToolResult("FAIL cartkit [build failed]")

	for i := 0; i < repeatHardStall; i++ {
		tr.record("file.write_patch", `{"patch":"p"}`, patchFail, false)
	}
	if got := tr.assess(); got != assessHardStall {
		t.Fatalf("assess() = %v, want assessHardStall after %d identical failing patches", got, repeatHardStall)
	}

	tr2 := newProgressTracker()
	for i := 0; i < repeatHardStall; i++ {
		tr2.record("shell.run", `{"command":"go test ./..."}`, testFail, false)
	}
	if got := tr2.assess(); got != assessHardStall {
		t.Fatalf("assess() = %v, want assessHardStall after %d identical failing shell.run calls", got, repeatHardStall)
	}
}

func TestSuccessfulMutatingCallStillResetsRepeatCounts(t *testing.T) {
	tr := newProgressTracker()
	h := hashToolResult("x")
	tr.record("file.read", `{"path":"a.go"}`, h, true)
	tr.record("file.read", `{"path":"a.go"}`, h, true)
	tr.record("file.write_patch", `{"patch":"p"}`, hashToolResult("applied"), true)
	if got := tr.record("file.read", `{"path":"a.go"}`, h, true); got != 1 {
		t.Fatalf("count after a SUCCESSFUL mutating call = %d, want 1 (state changed, re-read is fresh)", got)
	}
}

func TestReadOnlyRepeatIsDetectedAsControl(t *testing.T) {
	tr := newProgressTracker()
	h := hashToolResult("same content")
	var last int
	for i := 0; i < repeatHardStall; i++ {
		last = tr.record("file.read", `{"path":"a.go"}`, h, true)
	}
	if last != repeatHardStall {
		t.Fatalf("count = %d, want %d", last, repeatHardStall)
	}
	if got := tr.assess(); got != assessHardStall {
		t.Fatalf("assess() = %v, want assessHardStall", got)
	}
}
