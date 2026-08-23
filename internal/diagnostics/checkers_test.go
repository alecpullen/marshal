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

type lspStubFunc func(lang, filePath string) (string, bool)

func (f lspStubFunc) Diagnostics(lang, filePath string) (string, bool) { return f(lang, filePath) }

func TestInferPackages(t *testing.T) {
	cases := []struct {
		name  string
		files []string
		want  []string
	}{
		{"empty falls back to all", nil, []string{"./..."}},
		{"single file", []string{"internal/agent/runner.go"}, []string{"./internal/agent"}},
		{"same dir deduped", []string{"a/x.go", "a/y.go"}, []string{"./a"}},
		{"distinct dirs order-preserving", []string{"b/z.go", "a/x.go", "b/w.go"}, []string{"./b", "./a"}},
	}
	for _, c := range cases {
		got := inferPackages(c.files)
		if len(got) != len(c.want) {
			t.Fatalf("%s: inferPackages = %v, want %v", c.name, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("%s: inferPackages = %v, want %v", c.name, got, c.want)
			}
		}
	}
}

func TestCheckRunsPerDistinctPackage(t *testing.T) {
	var ran []string
	c := NewChecker(map[string]string{"go": "govet {package}"})
	c.runner = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		ran = append(ran, strings.Join(args, " "))
		return []byte("issue near " + args[len(args)-1]), nil
	}
	out, err := c.Check([]string{"a/x.go", "a/y.go", "b/z.go"}, "go")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if len(ran) != 2 || ran[0] != "./a" || ran[1] != "./b" {
		t.Fatalf("checker runs = %v, want [./a ./b]", ran)
	}
	if !strings.Contains(out, "./a") || !strings.Contains(out, "./b") {
		t.Fatalf("output %q missing findings from both packages", out)
	}
}

func TestCheckPlaceholderFreeTemplateRunsOnce(t *testing.T) {
	runs := 0
	c := NewChecker(map[string]string{"go": "govet"})
	c.runner = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		runs++
		return nil, nil
	}
	if _, err := c.Check([]string{"a/x.go", "b/z.go"}, "go"); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if runs != 1 {
		t.Fatalf("runs = %d, want 1 (no {package} placeholder)", runs)
	}
}

func TestCheckLSPConsultsEveryFile(t *testing.T) {
	c := NewChecker(nil)
	var seen []string
	c.SetLSPSource(lspStubFunc(func(lang, filePath string) (string, bool) {
		seen = append(seen, filePath)
		return "diag for " + filePath, true
	}))
	out, err := c.Check([]string{"a/x.go", "b/z.go"}, "go")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if len(seen) != 2 || seen[0] != "a/x.go" || seen[1] != "b/z.go" {
		t.Fatalf("LSP consulted %v, want both files", seen)
	}
	if !strings.Contains(out, "diag for a/x.go") || !strings.Contains(out, "diag for b/z.go") {
		t.Fatalf("output %q missing per-file diagnostics", out)
	}
}

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
