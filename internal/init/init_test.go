package initmgr

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInit_CreatesMarshalDirectory(t *testing.T) {
	// Create a temporary directory for testing
	tmpDir := t.TempDir()

	opts := Options{
		SkipGit:     true,
		SkipRepoMap: true,
		SkipConfig:  true,
		Force:       false,
	}

	result, err := Init(tmpDir, opts)
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	// Check that the .marshal directory was created
	marshalDir := filepath.Join(tmpDir, ".marshal")
	if _, err := os.Stat(marshalDir); os.IsNotExist(err) {
		t.Errorf(".marshal directory was not created")
	}

	// Check that the session database was created
	dbPath := filepath.Join(marshalDir, "sessions.db")
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Errorf("sessions.db was not created")
	}

	// Check result fields
	if result.RepoRoot != tmpDir {
		t.Errorf("expected RepoRoot to be %s, got %s", tmpDir, result.RepoRoot)
	}
	if result.MarshalDir != marshalDir {
		t.Errorf("expected MarshalDir to be %s, got %s", marshalDir, result.MarshalDir)
	}
	if result.SessionID == "" {
		t.Error("expected SessionID to be set")
	}
	if result.SessionDBPath != dbPath {
		t.Errorf("expected SessionDBPath to be %s, got %s", dbPath, result.SessionDBPath)
	}
}

func TestInit_CreatesSampleConfig(t *testing.T) {
	// Create a temporary directory for testing
	tmpDir := t.TempDir()

	opts := Options{
		SkipGit:     true,
		SkipRepoMap: true,
		SkipConfig:  false,
		Force:       false,
	}

	result, err := Init(tmpDir, opts)
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	// Check that the config file was created
	configPath := filepath.Join(tmpDir, "marshal.toml")
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		t.Errorf("marshal.toml was not created")
	}

	if !result.ConfigCreated {
		t.Error("expected ConfigCreated to be true")
	}
	if result.ConfigPath != configPath {
		t.Errorf("expected ConfigPath to be %s, got %s", configPath, result.ConfigPath)
	}
}

func TestInit_SkipsConfigIfExists(t *testing.T) {
	// Create a temporary directory for testing
	tmpDir := t.TempDir()

	// Create an existing config file
	configPath := filepath.Join(tmpDir, "marshal.toml")
	if err := os.WriteFile(configPath, []byte("existing config"), 0o644); err != nil {
		t.Fatalf("Failed to create existing config: %v", err)
	}

	opts := Options{
		SkipGit:     true,
		SkipRepoMap: true,
		SkipConfig:  false,
		Force:       false,
	}

	result, err := Init(tmpDir, opts)
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	// Check that the config was not overwritten
	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("Failed to read config: %v", err)
	}
	if string(content) != "existing config" {
		t.Error("existing config was overwritten")
	}

	if result.ConfigCreated {
		t.Error("expected ConfigCreated to be false when config exists")
	}
}

func TestInit_ForceOverwritesConfig(t *testing.T) {
	// Create a temporary directory for testing
	tmpDir := t.TempDir()

	// Create an existing config file
	configPath := filepath.Join(tmpDir, "marshal.toml")
	if err := os.WriteFile(configPath, []byte("existing config"), 0o644); err != nil {
		t.Fatalf("Failed to create existing config: %v", err)
	}

	opts := Options{
		SkipGit:     true,
		SkipRepoMap: true,
		SkipConfig:  false,
		Force:       true,
	}

	result, err := Init(tmpDir, opts)
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	// Check that the config was overwritten
	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("Failed to read config: %v", err)
	}
	if string(content) == "existing config" {
		t.Error("existing config was not overwritten with force=true")
	}

	if !result.ConfigCreated {
		t.Error("expected ConfigCreated to be true with force=true")
	}
}

func TestFileExists(t *testing.T) {
	// Create a temporary directory for testing
	tmpDir := t.TempDir()

	// Test with non-existent file
	if fileExists(filepath.Join(tmpDir, "nonexistent")) {
		t.Error("fileExists should return false for non-existent file")
	}

	// Test with existing file
	existingFile := filepath.Join(tmpDir, "existing")
	if err := os.WriteFile(existingFile, []byte("test"), 0o644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}
	if !fileExists(existingFile) {
		t.Error("fileExists should return true for existing file")
	}
}
