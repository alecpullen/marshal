package repo

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetectManifestPriority(t *testing.T) {
	dir := t.TempDir()
	if _, ok := DetectManifest(dir); ok {
		t.Fatal("empty dir should have no manifest")
	}
	must := func(name string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must("go.mod")
	m, ok := DetectManifest(dir)
	if !ok || m.Name != "go.mod" || m.Language != "go" {
		t.Fatalf("got %+v ok=%v, want go.mod/go", m, ok)
	}
	must("package.json")
	m, _ = DetectManifest(dir) // go.mod wins by priority
	if m.Name != "go.mod" {
		t.Errorf("priority broken: got %s", m.Name)
	}
}

func TestDominantSourceLanguage(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"a.go", "b.go", "c.rs"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("// x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if got := DominantSourceLanguage(dir); got != "go" {
		t.Errorf("got %q, want go", got)
	}
	if got := DominantSourceLanguage(t.TempDir()); got != "" {
		t.Errorf("empty dir: got %q, want empty", got)
	}
}
