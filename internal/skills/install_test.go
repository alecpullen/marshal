package skills

import "testing"

func TestLooksLikeGitURL(t *testing.T) {
	tests := []struct {
		source string
		want   bool
	}{
		{"github:owner/repo", true},
		{"git@github.com:owner/repo.git", true},
		{"https://github.com/owner/repo", true},
		{"https://github.com/owner/repo.git", true},
		{"http://example.com/repo", true},
		{"https://gitlab.com/owner/repo.git", true},
		{"./local/path/SKILL.md", false},
		{"/tmp/skill-bundle", false},
		{"SKILL.md", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.source, func(t *testing.T) {
			if got := looksLikeGitURL(tt.source); got != tt.want {
				t.Errorf("looksLikeGitURL(%q) = %v, want %v", tt.source, got, tt.want)
			}
		})
	}
}