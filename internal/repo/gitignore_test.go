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
