package agent

import (
	"fmt"
	"testing"
)

func TestCategorize(t *testing.T) {
	cases := map[string]toolCategory{
		"file.read":        catRead,
		"symbols.find":     catRead,
		"repo.search":      catSearch,
		"shell.run":        catShell,
		"file.write_patch": catPatch,
		"mystery.tool":     catOther,
	}
	for name, want := range cases {
		if got := categorize(name); got != want {
			t.Errorf("categorize(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestAssess(t *testing.T) {
	t.Run("empty is progressing", func(t *testing.T) {
		if got := newProgressTracker().assess(); got != assessProgressing {
			t.Fatalf("assess() = %v, want progressing", got)
		}
	})

	t.Run("exact repeat 3x is hard stall", func(t *testing.T) {
		tr := newProgressTracker()
		for i := 0; i < 3; i++ {
			tr.record("file.read", `{"path":"a.go"}`)
		}
		if got := tr.assess(); got != assessHardStall {
			t.Fatalf("assess() = %v, want hardStall", got)
		}
	})

	t.Run("sustained distinct reads are progressing", func(t *testing.T) {
		// Regression: the old readOnlyChurn heuristic nudged after 3 and
		// hard-stalled after 4 consecutive read/search calls even when every
		// call targeted a different file.
		tr := newProgressTracker()
		for i := 0; i < 8; i++ {
			tr.record("file.read", fmt.Sprintf(`{"path":"f%d.go"}`, i))
			if got := tr.assess(); got != assessProgressing {
				t.Fatalf("assess() after %d distinct reads = %v, want progressing", i+1, got)
			}
		}
	})

	t.Run("distinct mixed reads and searches are progressing", func(t *testing.T) {
		tr := newProgressTracker()
		tr.record("file.read", `{"path":"a.go"}`)
		tr.record("repo.search", `{"query":"foo"}`)
		tr.record("file.read", `{"path":"b.go"}`)
		tr.record("repo.search", `{"query":"bar"}`)
		if got := tr.assess(); got != assessProgressing {
			t.Fatalf("assess() = %v, want progressing", got)
		}
	})

	t.Run("three trailing duplicates is stalling", func(t *testing.T) {
		tr := newProgressTracker()
		tr.record("file.read", `{"path":"a.go"}`)
		tr.record("repo.search", `{"query":"foo"}`)
		tr.record("file.read", `{"path":"b.go"}`)
		// Revisit all three previously seen calls.
		tr.record("file.read", `{"path":"a.go"}`)
		tr.record("repo.search", `{"query":"foo"}`)
		tr.record("file.read", `{"path":"b.go"}`)
		if got := tr.assess(); got != assessStalling {
			t.Fatalf("assess() = %v, want stalling", got)
		}
	})

	t.Run("five trailing duplicates is hard stall", func(t *testing.T) {
		tr := newProgressTracker()
		tr.record("file.read", `{"path":"a.go"}`)
		tr.record("file.read", `{"path":"b.go"}`)
		tr.record("file.read", `{"path":"c.go"}`)
		tr.record("file.read", `{"path":"a.go"}`)
		tr.record("file.read", `{"path":"b.go"}`)
		tr.record("file.read", `{"path":"c.go"}`)
		tr.record("file.read", `{"path":"a.go"}`)
		tr.record("file.read", `{"path":"b.go"}`)
		if got := tr.assess(); got != assessHardStall {
			t.Fatalf("assess() = %v, want hardStall", got)
		}
	})

	t.Run("mutation resets novelty so re-reads are progress", func(t *testing.T) {
		// Re-reading a file after patching it is normal verification, not a
		// loop: the write invalidates earlier observations.
		tr := newProgressTracker()
		tr.record("file.read", `{"path":"a.go"}`)
		tr.record("file.write_patch", `{"patch":"..."}`)
		tr.record("file.read", `{"path":"a.go"}`)
		if got := tr.assess(); got != assessProgressing {
			t.Fatalf("assess() = %v, want progressing", got)
		}
	})

	t.Run("recent write is progressing", func(t *testing.T) {
		tr := newProgressTracker()
		tr.record("file.read", `{"path":"a.go"}`)
		tr.record("file.write_patch", `{"patch":"..."}`)
		tr.record("shell.run", `{"command":"go test ./..."}`)
		if got := tr.assess(); got != assessProgressing {
			t.Fatalf("assess() = %v, want progressing", got)
		}
	})

	t.Run("three consecutive idle turns is hard stall", func(t *testing.T) {
		tr := newProgressTracker()
		tr.recordIdle("stop")
		tr.recordIdle("stop")
		if got := tr.assess(); got != assessProgressing {
			t.Fatalf("assess() after 2 idle = %v, want progressing", got)
		}
		tr.recordIdle("stop")
		if got := tr.assess(); got != assessHardStall {
			t.Fatalf("assess() after 3 idle = %v, want hardStall", got)
		}
	})

	t.Run("idle interleaved with tool calls is not a stall", func(t *testing.T) {
		// Two idle turns separated by a tool call do not form a run of 3
		// consecutive idles, so the hard-stall path must not fire.
		tr := newProgressTracker()
		tr.recordIdle("stop")
		tr.recordIdle("stop")
		tr.record("file.read", `{"path":"a.go"}`)
		tr.recordIdle("stop")
		tr.recordIdle("stop")
		if got := tr.assess(); got != assessProgressing {
			t.Fatalf("assess() with interleaved tools = %v, want progressing", got)
		}
	})
}

func TestMutating(t *testing.T) {
	cases := map[toolCategory]bool{
		catShell:  true,
		catWrite:  true,
		catPatch:  true,
		catRead:   false,
		catSearch: false,
		catOther:  false,
	}
	for cat, want := range cases {
		if got := mutating(cat); got != want {
			t.Errorf("mutating(%q) = %v, want %v", cat, got, want)
		}
	}
}

func TestLastCall(t *testing.T) {
	tr := newProgressTracker()
	if _, _, ok := tr.lastCall(); ok {
		t.Fatal("lastCall() on empty tracker reported ok")
	}
	tr.record("file.read", `{"path":"a.go"}`)
	tr.record("repo.search", `{"query":"foo"}`)
	name, args, ok := tr.lastCall()
	if !ok || name != "repo.search" || args != `{"query":"foo"}` {
		t.Fatalf("lastCall() = %q, %q, %v; want repo.search / query foo / true", name, args, ok)
	}
}
