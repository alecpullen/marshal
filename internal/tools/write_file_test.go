package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteFile_Basic(t *testing.T) {
	tmpDir := t.TempDir()
	content := "Hello, World!"

	input, _ := json.Marshal(WriteFileInput{Path: "test.txt", Content: content})
	result, err := WriteFile(tmpDir, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Error != "" {
		t.Errorf("unexpected error result: %s", result.Error)
	}
	if !result.Created {
		t.Error("expected Created=true for new file")
	}
	if result.Bytes != len(content) {
		t.Errorf("bytes mismatch: got %d, want %d", result.Bytes, len(content))
	}

	// Verify file content
	data, err := os.ReadFile(filepath.Join(tmpDir, "test.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != content {
		t.Errorf("content mismatch: got %q, want %q", string(data), content)
	}
}

func TestWriteFile_Append(t *testing.T) {
	tmpDir := t.TempDir()

	// First write
	input1, _ := json.Marshal(WriteFileInput{Path: "test.txt", Content: "Hello"})
	WriteFile(tmpDir, input1)

	// Append
	input2, _ := json.Marshal(WriteFileInput{Path: "test.txt", Content: " World", Append: true})
	result, err := WriteFile(tmpDir, input2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.Appended {
		t.Error("expected Appended=true")
	}

	// Verify content
	data, _ := os.ReadFile(filepath.Join(tmpDir, "test.txt"))
	if string(data) != "Hello World" {
		t.Errorf("append result: got %q, want %q", string(data), "Hello World")
	}
}

func TestWriteFile_NestedDirectory(t *testing.T) {
	tmpDir := t.TempDir()

	input, _ := json.Marshal(WriteFileInput{Path: "subdir/nested/file.txt", Content: "nested content"})
	result, err := WriteFile(tmpDir, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Error != "" {
		t.Errorf("unexpected error: %s", result.Error)
	}

	// Verify directory and file created
	_, err = os.Stat(filepath.Join(tmpDir, "subdir", "nested"))
	if err != nil {
		t.Errorf("nested directory not created: %v", err)
	}
}

func TestWriteFile_PathTraversal(t *testing.T) {
	tmpDir := t.TempDir()

	input, _ := json.Marshal(WriteFileInput{Path: "../escape.txt", Content: "bad"})
	result, err := WriteFile(tmpDir, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Error == "" {
		t.Error("expected error for path traversal")
	}

	// Verify file was NOT created outside temp dir
	_, err = os.Stat(filepath.Join(tmpDir, "..", "escape.txt"))
	if !os.IsNotExist(err) {
		t.Error("file was created outside repo - security violation!")
	}
}

func TestWriteFile_EmptyPath(t *testing.T) {
	tmpDir := t.TempDir()

	input, _ := json.Marshal(WriteFileInput{Path: "", Content: "test"})
	result, err := WriteFile(tmpDir, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Error != "path is required" {
		t.Errorf("expected 'path is required' error, got: %s", result.Error)
	}
}

func TestWriteFile_OverwriteExisting(t *testing.T) {
	tmpDir := t.TempDir()

	// Create initial file
	os.WriteFile(filepath.Join(tmpDir, "existing.txt"), []byte("old"), 0644)

	// Overwrite
	input, _ := json.Marshal(WriteFileInput{Path: "existing.txt", Content: "new"})
	result, err := WriteFile(tmpDir, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Created {
		t.Error("expected Created=false for overwrite")
	}

	// Verify content changed
	data, _ := os.ReadFile(filepath.Join(tmpDir, "existing.txt"))
	if string(data) != "new" {
		t.Errorf("overwrite failed: got %q, want %q", string(data), "new")
	}
}
