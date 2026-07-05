package agent

import "testing"

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

	t.Run("three distinct reads is stalling", func(t *testing.T) {
		tr := newProgressTracker()
		tr.record("file.read", `{"path":"a.go"}`)
		tr.record("repo.search", `{"query":"foo"}`)
		tr.record("file.read", `{"path":"b.go"}`)
		if got := tr.assess(); got != assessStalling {
			t.Fatalf("assess() = %v, want stalling", got)
		}
	})

	t.Run("stall persists to hard stall", func(t *testing.T) {
		tr := newProgressTracker()
		tr.record("file.read", `{"path":"a.go"}`)
		tr.record("repo.search", `{"query":"foo"}`)
		tr.record("file.read", `{"path":"b.go"}`)
		tr.record("repo.search", `{"query":"bar"}`)
		if got := tr.assess(); got != assessHardStall {
			t.Fatalf("assess() = %v, want hardStall", got)
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
}
