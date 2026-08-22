// internal/pathutil/safe_test.go
package pathutil

import (
	"path/filepath"
	"testing"
)

func TestSafeWorkspacePath(t *testing.T) {
	tests := []struct {
		name    string
		root    string
		rel     string
		want    string
		wantErr bool
	}{
		{"simple relative", "/workspace", "file.go", filepath.Join("/workspace", "file.go"), false},
		{"nested relative", "/workspace", "dir/file.go", filepath.Join("/workspace", "dir/file.go"), false},
		{"empty path", "/workspace", "", "", true},
		{"absolute path rejected", "/workspace", "/etc/passwd", "", true},
		{"parent traversal rejected", "/workspace", "../etc/passwd", "", true},
		{"dot current dir", "/workspace", ".", "/workspace", false},
		{"dotdot at boundary", "/workspace", "..", "", true},
		{"nested parent traversal", "/workspace", "dir/../../../etc/passwd", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := SafeWorkspacePath(tt.root, tt.rel)
			if (err != nil) != tt.wantErr {
				t.Fatalf("SafeWorkspacePath(%q, %q) error = %v, wantErr %v", tt.root, tt.rel, err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Fatalf("SafeWorkspacePath(%q, %q) = %q, want %q", tt.root, tt.rel, got, tt.want)
			}
		})
	}
}
