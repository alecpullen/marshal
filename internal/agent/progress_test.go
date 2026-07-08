package agent

import (
	"strings"
	"testing"
)

func TestRecordReturnsRepeatCountForIdenticalSignature(t *testing.T) {
	tr := newProgressTracker()
	h := hashToolResult("same output")
	if got := tr.record("file.read", `{"path":"a.go"}`, h); got != 1 {
		t.Fatalf("first record count = %d, want 1", got)
	}
	if got := tr.record("file.read", `{"path":"a.go"}`, h); got != 2 {
		t.Fatalf("second record count = %d, want 2", got)
	}
	if got := tr.record("file.read", `{"path":"a.go"}`, h); got != 3 {
		t.Fatalf("third record count = %d, want 3", got)
	}
}

func TestDifferentOutputIsNotARepeat(t *testing.T) {
	tr := newProgressTracker()
	tr.record("shell.run", `{"command":"go test"}`, hashToolResult("FAIL: TestX"))
	got := tr.record("shell.run", `{"command":"go test"}`, hashToolResult("ok"))
	if got != 1 {
		t.Fatalf("same call with different output counted as repeat: count = %d, want 1", got)
	}
}

func TestMutatingCallResetsRepeatCounts(t *testing.T) {
	tr := newProgressTracker()
	h := hashToolResult("x")
	tr.record("file.read", `{"path":"a.go"}`, h)
	tr.record("file.read", `{"path":"a.go"}`, h)
	tr.record("file.write_patch", `{"patch":"p"}`, hashToolResult("applied"))
	if got := tr.record("file.read", `{"path":"a.go"}`, h); got != 1 {
		t.Fatalf("count after mutating call = %d, want 1 (state changed, re-read is fresh)", got)
	}
}

func TestAssessHardStallOnlyAtThreshold(t *testing.T) {
	tr := newProgressTracker()
	h := hashToolResult("out")
	for i := 0; i < repeatHardStall-1; i++ {
		tr.record("repo.search", `{"query":"q"}`, h)
		if a := tr.assess(); a != assessProgressing {
			t.Fatalf("assess after %d repeats = %v, want assessProgressing", i+1, a)
		}
	}
	tr.record("repo.search", `{"query":"q"}`, h)
	if a := tr.assess(); a != assessHardStall {
		t.Fatalf("assess at %d repeats = %v, want assessHardStall", repeatHardStall, a)
	}
}

func TestResetCountsClearsStreakButKeepsIdleHistory(t *testing.T) {
	tr := newProgressTracker()
	h := hashToolResult("out")
	for i := 0; i < repeatHardStall; i++ {
		tr.record("repo.search", `{"query":"q"}`, h)
	}
	tr.resetCounts()
	if a := tr.assess(); a != assessProgressing {
		t.Fatalf("assess after resetCounts = %v, want assessProgressing", a)
	}
	if got := tr.record("repo.search", `{"query":"q"}`, h); got != 1 {
		t.Fatalf("count after resetCounts = %d, want 1", got)
	}
}

func TestConsecutiveIdleStillHardStalls(t *testing.T) {
	tr := newProgressTracker()
	tr.recordIdle("empty")
	tr.recordIdle("empty")
	if a := tr.assess(); a == assessHardStall {
		t.Fatal("2 idles should not hard stall")
	}
	tr.recordIdle("empty")
	if a := tr.assess(); a != assessHardStall {
		t.Fatalf("assess after 3 idles = %v, want assessHardStall", a)
	}
}

func TestToolCallBreaksIdleRun(t *testing.T) {
	tr := newProgressTracker()
	tr.recordIdle("empty")
	tr.recordIdle("empty")
	tr.record("file.read", `{"path":"a.go"}`, hashToolResult("x"))
	tr.recordIdle("empty")
	if a := tr.assess(); a == assessHardStall {
		t.Fatal("idle run interrupted by a tool call must not hard stall")
	}
}

func TestRepeatReminderLadder(t *testing.T) {
	if got := repeatReminder(1, "file.read", "{}"); got != "" {
		t.Fatalf("reminder at count 1 = %q, want empty", got)
	}
	if got := repeatReminder(2, "file.read", "{}"); got != "" {
		t.Fatalf("reminder at count 2 = %q, want empty", got)
	}
	gentle := repeatReminder(3, "file.read", "{}")
	if !strings.Contains(gentle, "repeating the exact same tool call") {
		t.Fatalf("gentle reminder missing expected text: %q", gentle)
	}
	strong := repeatReminder(5, "file.read", `{"path":"a.go"}`)
	if !strings.Contains(strong, "repeated_times: 5") || !strings.Contains(strong, `{"path":"a.go"}`) {
		t.Fatalf("strong reminder missing count/args: %q", strong)
	}
	stop := repeatReminder(8, "file.read", "{}")
	if !strings.Contains(stop, "Stop all tool calls") {
		t.Fatalf("stop reminder missing expected text: %q", stop)
	}
	if got := repeatReminder(9, "file.read", "{}"); !strings.Contains(got, "Stop all tool calls") {
		t.Fatalf("counts above 8 keep the stop reminder, got %q", got)
	}
}

func TestLastCallReportsMostRecent(t *testing.T) {
	tr := newProgressTracker()
	if _, _, ok := tr.lastCall(); ok {
		t.Fatal("lastCall on empty tracker reported ok")
	}
	tr.record("repo.search", `{"query":"q"}`, hashToolResult("x"))
	name, args, ok := tr.lastCall()
	if !ok || name != "repo.search" || args != `{"query":"q"}` {
		t.Fatalf("lastCall = (%q, %q, %v)", name, args, ok)
	}
}
