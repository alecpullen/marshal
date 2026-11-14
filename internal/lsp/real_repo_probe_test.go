//go:build latencyprobe

package lsp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// This probe only walks metadata through the production discovery function.
// It does not construct a manager, launch a server, or open private state.
func TestRealRepoLanguageDiscovery(t *testing.T) {
	root := os.Getenv("MARSHAL_LATENCY_INSPECT_REPO")
	if root == "" {
		t.Skip("set MARSHAL_LATENCY_INSPECT_REPO for a metadata-only discovery probe")
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(map[string]any{"walk_limit": repoLanguageWalkLimit, "languages": DetectRepoLanguages(root, repoLanguageWalkLimit)})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("REAL_REPO_LANGUAGES %s", data)
}
