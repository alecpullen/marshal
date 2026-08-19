package diagnostics

import (
	"context"
	"strings"
	"sync"
	"testing"
)

func TestGoCheckerFindsVetError(t *testing.T) {
	c := NewChecker(map[string]string{"go": "go vet {package}"})
	c.runner = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name != "go" {
			t.Fatalf("runner name = %q, want go", name)
		}
		if len(args) < 2 || args[0] != "vet" {
			t.Fatalf("runner args = %v, want leading 'vet'", args)
		}
		return []byte("./testdata/bad.go:5:2: declared and not used: x\n"), nil
	}
	out, err := c.Check([]string{"testdata/bad.go"}, "go")
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if !strings.Contains(out, "declared and not used") {
		t.Fatalf("expected vet error, got:\n%s", out)
	}
}

func TestMissingCheckerReturnsEmptyString(t *testing.T) {
	e := NewChecker(map[string]string{})
	out, err := e.Check([]string{"foo.rs"}, "rust")
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if out != "" {
		t.Fatalf("got %q, want empty string when no checker is configured", out)
	}
}

func TestConfiguredCheckerRunsCleanReportsNone(t *testing.T) {
	c := NewChecker(map[string]string{"go": "go vet {package}"})
	c.runner = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return nil, nil
	}
	out, err := c.Check([]string{"x.go"}, "go")
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if out != "diagnostics: none" {
		t.Fatalf("got %q, want 'diagnostics: none' when configured checker finds nothing", out)
	}
}

type fakeLSPSource struct {
	out string
	ok  bool
}

func (f fakeLSPSource) Diagnostics(string, string) (string, bool) { return f.out, f.ok }

func TestCheckerPrefersLSP(t *testing.T) {
	c := NewChecker(nil) // no command checkers configured
	c.SetLSPSource(fakeLSPSource{out: "a.go:1: oops", ok: true})
	out, err := c.Check([]string{"a.go"}, "go")
	if err != nil || out != "a.go:1: oops" {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestCheckerConcurrentWithSetLSPSource(t *testing.T) {
	c := NewChecker(map[string]string{"go": "go vet {package}"})
	c.runner = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return nil, nil
	}
	var wg sync.WaitGroup
	// Writer goroutine: set LSP source concurrently.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			c.SetLSPSource(fakeLSPSource{ok: false})
		}
	}()
	// Reader goroutine: call Check concurrently.
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = c.Check([]string{"x.go"}, "go")
		}()
	}
	wg.Wait()
}

func TestCheckerFallsBackWhenNoLSP(t *testing.T) {
	c := NewChecker(nil)
	c.SetLSPSource(fakeLSPSource{ok: false})
	out, _ := c.Check([]string{"a.rs"}, "rust")
	if out != "" { // no commands configured, no lsp → empty (caller renders "none")
		t.Fatalf("expected empty fallback, got %q", out)
	}
}
