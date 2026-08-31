package trust

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCanonicalizeResolvesSymlinks pins the /var vs /private/var class of
// bugs: the same directory reached through a symlink must produce the same
// trust key as its resolved form.
func TestCanonicalizeResolvesSymlinks(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if got, want := Canonicalize(link), Canonicalize(real); got != want {
		t.Errorf("Canonicalize(link) = %q, want %q", got, want)
	}
}

func TestCanonicalizeIsAbsolute(t *testing.T) {
	got := Canonicalize(".")
	if !filepath.IsAbs(got) {
		t.Errorf("Canonicalize(\".\") = %q, want absolute", got)
	}
}
