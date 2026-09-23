package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPreviewPatchDiffSystemModeAbsolute pins the system-access branch of
// PreviewPatchDiff: an absolute patch path outside the workspace is rejected
// without the flag (existing containment behavior) and previewed with it.
func TestPreviewPatchDiffSystemModeAbsolute(t *testing.T) {
	workspaceRoot := t.TempDir()
	outsideDir := t.TempDir()

	target := filepath.Join(outsideDir, "report.json")
	if err := os.WriteFile(target, []byte("{\n  \"status\": \"ok\"\n}\n"), 0644); err != nil {
		t.Fatalf("write fixture file: %v", err)
	}

	patchText := "File: " + target + "\n<<<<<<< SEARCH\n  \"status\": \"ok\"\n=======\n  \"status\": \"patched\"\n>>>>>>> REPLACE\n"

	if _, err := PreviewPatchDiff(workspaceRoot, patchText, false); err == nil {
		t.Fatal("expected error for out-of-root absolute path without system access, got nil")
	}

	diff, err := PreviewPatchDiff(workspaceRoot, patchText, true)
	if err != nil {
		t.Fatalf("PreviewPatchDiff with system access returned error: %v", err)
	}
	if diff == "" {
		t.Fatal("expected non-empty diff with system access, got empty string")
	}
	if !strings.Contains(diff, `"status": "patched"`) {
		t.Fatalf("diff missing replacement content: %s", diff)
	}
}
