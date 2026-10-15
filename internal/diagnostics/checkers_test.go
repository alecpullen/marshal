package diagnostics

import (
	"context"
	"strings"
	"testing"
)

func TestGoCheckerFindsVetError(t *testing.T) {
	c := NewChecker(map[string]string{"go": "go vet {package}"})
	c.runner = func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
		if name != "go" {
			t.Fatalf("runner name = %q, want go", name)
		}
		if len(args) < 2 || args[0] != "vet" {
			t.Fatalf("runner args = %v, want leading 'vet'", args)
		}
		return []byte("./testdata/bad.go:5:2: declared and not used: x\n"), nil
	}
	out, err := c.Check("/repo", []string{"testdata/bad.go"}, "go")
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if !strings.Contains(out, "declared and not used") {
		t.Fatalf("expected vet error, got:\n%s", out)
	}
}

func TestMissingCheckerReturnsEmptyString(t *testing.T) {
	e := NewChecker(map[string]string{})
	out, err := e.Check("/repo", []string{"foo.rs"}, "rust")
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if out != "" {
		t.Fatalf("got %q, want empty string when no checker is configured", out)
	}
}

func TestConfiguredCheckerRunsCleanReportsNone(t *testing.T) {
	c := NewChecker(map[string]string{"go": "go vet {package}"})
	c.runner = func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
		return nil, nil
	}
	out, err := c.Check("/repo", []string{"x.go"}, "go")
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if out != "diagnostics: none" {
		t.Fatalf("got %q, want 'diagnostics: none' when configured checker finds nothing", out)
	}
}

// Review focus 1: the fallback must check the session's worktree copy.
func TestCheckerRunsInGivenDir(t *testing.T) {
	var gotDir string
	c := NewChecker(map[string]string{"go": "go vet {package}"})
	c.runner = func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
		gotDir = dir
		return nil, nil
	}
	if _, err := c.Check("/wt/feature", []string{"a/x.go"}, "go"); err != nil {
		t.Fatal(err)
	}
	if gotDir != "/wt/feature" {
		t.Fatalf("checker ran in %q, want the active root", gotDir)
	}
}

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
	c.runner = func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
		ran = append(ran, strings.Join(args, " "))
		return []byte("issue near " + args[len(args)-1]), nil
	}
	out, err := c.Check("/repo", []string{"a/x.go", "a/y.go", "b/z.go"}, "go")
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
	c.runner = func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
		runs++
		return nil, nil
	}
	if _, err := c.Check("/repo", []string{"a/x.go", "b/z.go"}, "go"); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if runs != 1 {
		t.Fatalf("runs = %d, want 1 (no {package} placeholder)", runs)
	}
}
