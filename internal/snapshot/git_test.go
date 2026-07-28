package snapshot

import (
	"testing"
	"time"
)

// A non-matching unanchored glob against a nested path must terminate. The
// suffix scan used to feed a slice-relative index back into an absolute
// cursor, so indices cycled and the walk in largeFileExcludes spun forever
// on the first path three levels deep.
func TestMatchIgnorePatternTerminatesOnNestedPath(t *testing.T) {
	done := make(chan bool, 1)
	go func() {
		done <- matchIgnorePattern("*.test", "internal/app/tui/model.go")
	}()

	select {
	case got := <-done:
		if got {
			t.Fatalf("matchIgnorePattern(*.test, internal/app/tui/model.go) = true, want false")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("matchIgnorePattern did not terminate")
	}
}

func TestMatchIgnorePatternSuffixGlob(t *testing.T) {
	tests := []struct {
		pattern string
		rel     string
		want    bool
	}{
		{"*.test", "internal/app/tui/model.go", false},
		{"*.test", "internal/app/tui/model.test", true},
		{"*.out", "a/b/c/d/e/cover.out", true},
		{"*.out", "a/b/c/d/e/cover.txt", false},
		{"*.test", "model.test", true},
		{"*.test", "model.go", false},
		{"/marshal", "marshal", true},
		{"/bin/", "bin/tool", true},
		{".marshal/", "internal/app", false},
	}

	for _, tt := range tests {
		if got := matchIgnorePattern(tt.pattern, tt.rel); got != tt.want {
			t.Errorf("matchIgnorePattern(%q, %q) = %v, want %v", tt.pattern, tt.rel, got, tt.want)
		}
	}
}
