package diagnostics

import (
	"context"
	"strings"
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

func TestMissingCheckerReturnsNone(t *testing.T) {
	e := NewChecker(map[string]string{})
	out, err := e.Check([]string{"foo.rs"}, "rust")
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if out != "diagnostics: none" {
		t.Fatalf("got %q", out)
	}
}
