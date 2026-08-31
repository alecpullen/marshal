package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSaveProjectConfigDoesNotBakeUserValues pins the layer-aware save: a
// user-layer provider must not appear in the project file when the project
// layer did not set it.
func TestSaveProjectConfigDoesNotBakeUserValues(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".marshal", "config.toml")

	user := Default()
	user.Providers = map[string]ProviderConfig{
		"ollama": {Type: "ollama", BaseURL: "http://127.0.0.1:11434"},
	}
	merged := user                                  // project layer contributes nothing
	merged.Commands.Test = "go test ./internal/..." // differs from the user layer

	layers := Layers{User: user, Merged: merged}
	if err := SaveProjectConfig(path, merged, layers); err != nil {
		t.Fatalf("save: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.Contains(string(data), "[providers") {
		t.Errorf("user-layer provider leaked into project file:\n%s", data)
	}
	if !strings.Contains(string(data), "[commands]") {
		t.Errorf("project-layer commands section missing:\n%s", data)
	}
}

// TestSaveProjectConfigKeepsProjectValues verifies a value the project
// layer genuinely sets (differs from the user layer) is still persisted.
func TestSaveProjectConfigKeepsProjectValues(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".marshal", "config.toml")

	user := Default()
	merged := Default()
	merged.Commands.Test = "go test ./... -count=1"

	if err := SaveProjectConfig(path, merged, Layers{User: user, Merged: merged}); err != nil {
		t.Fatalf("save: %v", err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "go test ./... -count=1") {
		t.Errorf("project-layer value dropped:\n%s", data)
	}
}
