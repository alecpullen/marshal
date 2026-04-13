package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestReadFile_Basic(t *testing.T) {
	// Create temp directory with a test file
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.txt")
	content := "line1\nline2\nline3"
	if err := os.WriteFile(testFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	input, _ := json.Marshal(ReadFileInput{Path: "test.txt"})
	result, err := ReadFile(tmpDir, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Error != "" {
		t.Errorf("unexpected error result: %s", result.Error)
	}
	if result.Content != content {
		t.Errorf("content mismatch: got %q, want %q", result.Content, content)
	}
	if result.Truncated {
		t.Error("unexpected truncation")
	}
}

func TestReadFile_WithOffsetAndLimit(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.txt")
	content := "line1\nline2\nline3\nline4\nline5"
	if err := os.WriteFile(testFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	input, _ := json.Marshal(ReadFileInput{Path: "test.txt", Offset: 1, Limit: 2})
	result, err := ReadFile(tmpDir, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := "line2\nline3"
	if result.Content != expected {
		t.Errorf("content mismatch: got %q, want %q", result.Content, expected)
	}
	if !result.Truncated {
		t.Error("expected truncation flag for partial read")
	}
}

func TestReadFile_PathTraversal(t *testing.T) {
	tmpDir := t.TempDir()

	input, _ := json.Marshal(ReadFileInput{Path: "../etc/passwd"})
	result, err := ReadFile(tmpDir, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Error != "path traversal not allowed" {
		t.Errorf("expected path traversal error, got: %s", result.Error)
	}
}

func TestReadFile_FileNotFound(t *testing.T) {
	tmpDir := t.TempDir()

	input, _ := json.Marshal(ReadFileInput{Path: "nonexistent.txt"})
	result, err := ReadFile(tmpDir, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Error != "file not found" {
		t.Errorf("expected file not found error, got: %s", result.Error)
	}
}

func TestReadFile_Directory(t *testing.T) {
	tmpDir := t.TempDir()

	input, _ := json.Marshal(ReadFileInput{Path: "."})
	result, err := ReadFile(tmpDir, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Error != "path is a directory, not a file" {
		t.Errorf("expected directory error, got: %s", result.Error)
	}
}

func TestReadFile_SizeLimit(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "large.bin")

	// Create file larger than MaxFileSize
	largeContent := make([]byte, MaxFileSize+1)
	if err := os.WriteFile(testFile, largeContent, 0644); err != nil {
		t.Fatal(err)
	}

	input, _ := json.Marshal(ReadFileInput{Path: "large.bin"})
	result, err := ReadFile(tmpDir, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Error == "" || !contains(result.Error, "file too large") {
		t.Errorf("expected size limit error, got: %s", result.Error)
	}
}

func TestReadFile_InvalidInput(t *testing.T) {
	tmpDir := t.TempDir()

	// Send invalid JSON
	result, err := ReadFile(tmpDir, []byte("not json"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Error == "" {
		t.Error("expected error for invalid input")
	}
}

func TestReadFile_EmptyPath(t *testing.T) {
	tmpDir := t.TempDir()

	input, _ := json.Marshal(ReadFileInput{Path: "   "})
	result, err := ReadFile(tmpDir, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Error != "path is required" {
		t.Errorf("expected path required error, got: %s", result.Error)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && (s[:len(substr)] == substr || s[len(s)-len(substr):] == substr || containsAt(s, substr, 1)))
}

func containsAt(s, substr string, start int) bool {
	for i := start; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
