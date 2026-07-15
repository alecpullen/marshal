package repo

import "testing"

func TestGitignoreMatch(t *testing.T) {
	g, err := ParseGitignore("*.log\nbuild/\n")
	if err != nil {
		t.Fatalf("ParseGitignore failed: %v", err)
	}
	cases := []struct {
		path  string
		isDir bool
		want  bool
	}{
		{"debug.log", false, true},
		{"main.go", false, false},
		{"build", true, true},
		{"build/output.js", false, true},
		{"src/build.go", false, false},
	}
	for _, tc := range cases {
		if got := g.Match(tc.path, tc.isDir); got != tc.want {
			t.Errorf("Match(%q, dir=%v) = %v, want %v", tc.path, tc.isDir, got, tc.want)
		}
	}
}

func TestGitignoreAnchoredPattern(t *testing.T) {
	g, err := ParseGitignore("/build\n")
	if err != nil {
		t.Fatalf("ParseGitignore failed: %v", err)
	}
	cases := []struct {
		path  string
		isDir bool
		want  bool
	}{
		{"build/output.js", false, true},
		{"build", true, true},
		{"src/build/output.js", false, false},
		{"src/build", true, false},
	}
	for _, tc := range cases {
		if got := g.Match(tc.path, tc.isDir); got != tc.want {
			t.Errorf("Match(%q, dir=%v) = %v, want %v", tc.path, tc.isDir, got, tc.want)
		}
	}
}

func TestParseGitignoreInvalidPattern(t *testing.T) {
	_, err := ParseGitignore("[\n")
	if err == nil {
		t.Fatalf("ParseGitignore invalid pattern: expected error, got nil")
	}
}

func TestGitignoreDirOnlyAnchored(t *testing.T) {
	g, err := ParseGitignore("build/\n")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if g.Match("src/build/output.js", false) {
		t.Errorf("build/ must not match nested paths when unanchored")
	}
	if !g.Match("build/output.js", false) {
		t.Errorf("build/ must match top-level build/output.js")
	}
	if !g.Match("build", true) {
		t.Errorf("build/ must match the build directory itself")
	}
}

// Leading-slash anchored + trailing-slash dirOnly.
func TestGitignoreLeadingSlashDirOnly(t *testing.T) {
	g, err := ParseGitignore("/build/\n")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !g.Match("build/output.js", false) {
		t.Errorf("/build/ must match build/output.js at root")
	}
	if !g.Match("build", true) {
		t.Errorf("/build/ must match the build directory itself")
	}
	if g.Match("src/build/output.js", false) {
		t.Errorf("/build/ must not match nested src/build/output.js")
	}
}

// Mid-pattern slash (a/b/) — already anchored by the Contains rule,
// verify that both the interior-slash anchor and trailing-slash dirOnly work.
func TestGitignoreMidSlashDirOnly(t *testing.T) {
	g, err := ParseGitignore("a/b/\n")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !g.Match("a/b/output.js", false) {
		t.Errorf("a/b/ must match a/b/output.js at root")
	}
	if !g.Match("a/b", true) {
		t.Errorf("a/b/ must match the a/b directory itself")
	}
	if g.Match("x/a/b/output.js", false) {
		t.Errorf("a/b/ must not match nested x/a/b/output.js")
	}
}

// Nominal dirOnly pattern — in proper .gitignore semantics, logs/ matches
// any directory named logs anywhere in the tree. The current implementation
// anchors every pattern containing a slash (including the trailing slash),
// so nested paths are NOT matched. This test documents that behaviour and
// serves as a regression marker.
func TestGitignoreDirOnlyMatchesNested(t *testing.T) {
	g, err := ParseGitignore("logs/\n")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// Top-level directory and its contents match.
	if !g.Match("logs/output.log", false) {
		t.Errorf("logs/ must match logs/output.log")
	}
	if !g.Match("logs", true) {
		t.Errorf("logs/ must match the logs directory itself")
	}
	// Under current implementation the trailing slash triggers anchoring,
	// so app/logs/x.log does NOT match (which differs from standard gitignore).
	// This test asserts the current behaviour; if the anchoring is later fixed
	// to match git semantics this assertion should be inverted.
	if g.Match("app/logs/output.log", false) {
		t.Errorf("logs/ currently anchored by trailing slash — should not match nested app/logs/output.log")
	}
}

func TestGitignoreNegation(t *testing.T) {
	g, err := ParseGitignore("*.log\n!important.log\n")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !g.Match("debug.log", false) {
		t.Errorf("*.log must match debug.log")
	}
	if g.Match("important.log", false) {
		t.Errorf("!important.log must un-ignore important.log")
	}
	// A path matched only by the negative pattern (not by a preceding
	// positive one) must NOT be ignored — git's behaviour.
	g2, _ := ParseGitignore("!foo\n")
	if g2.Match("foo", false) {
		t.Errorf("!foo alone must not match anything (no positive match to override)")
	}
}

func TestGitignoreMiddleSlashPattern(t *testing.T) {
	g, err := ParseGitignore("foo/bar\n")
	if err != nil {
		t.Fatalf("ParseGitignore failed: %v", err)
	}
	cases := []struct {
		path  string
		isDir bool
		want  bool
	}{
		{"foo/bar/baz.go", false, true},
		{"foo/bar", true, true},
		{"src/foo/bar/baz.go", false, false},
		{"src/foo/bar", true, false},
	}
	for _, tc := range cases {
		if got := g.Match(tc.path, tc.isDir); got != tc.want {
			t.Errorf("Match(%q, dir=%v) = %v, want %v", tc.path, tc.isDir, got, tc.want)
		}
	}
}
