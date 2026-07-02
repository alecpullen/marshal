package repo

import "testing"

func TestDetectLanguage(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"main.go", "go"},
		{"app.js", "javascript"},
		{"app.ts", "typescript"},
		{"main.py", "python"},
		{"README.md", "markdown"},
		{"Dockerfile", "dockerfile"},
		{"Makefile", "makefile"},
		{"file.unknown", ""},
		{"noext", ""},
	}
	for _, tc := range cases {
		if got := DetectLanguage(tc.path); got != tc.want {
			t.Errorf("DetectLanguage(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}
