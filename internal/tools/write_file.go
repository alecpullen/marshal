// Package tools implements the tool-use executor interface for Marshal.
package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/alecpullen/marshal/internal/sandbox"
)

// WriteFileInput is the JSON input schema for the write_file tool.
type WriteFileInput struct {
	Path    string `json:"path"`              // Relative path within repository
	Content string `json:"content"`             // File content to write
	Append  bool   `json:"append,omitempty"`    // If true, append instead of overwrite
}

// WriteFileResult is the JSON output from the write_file tool.
type WriteFileResult struct {
	Path      string `json:"path"`                 // Written file path
	Bytes     int    `json:"bytes"`                // Bytes written
	Created   bool   `json:"created"`              // True if file was created (not existed)
	Appended  bool   `json:"appended,omitempty"`     // True if content was appended
	Error     string `json:"error,omitempty"`      // Error message if any
}

// WriteFile writes content to a file within the repository.
// It creates parent directories if needed and validates the path is within bounds.
func WriteFile(repoRoot string, input json.RawMessage) (*WriteFileResult, error) {
	var params WriteFileInput
	if err := json.Unmarshal(input, &params); err != nil {
		return &WriteFileResult{
			Error: fmt.Sprintf("invalid input: %v", err),
		}, nil
	}

	// Validate path
	if params.Path == "" {
		return &WriteFileResult{
			Error: "path is required",
		}, nil
	}

	// Sandbox validation
	sb := sandbox.New(sandbox.Config{RepoRoot: repoRoot})
	if err := sb.ValidateWritePath(params.Path); err != nil {
		return &WriteFileResult{
			Path:  params.Path,
			Error: err.Error(),
		}, nil
	}

	// Resolve to absolute path
	cleanPath := filepath.Clean(params.Path)
	absPath := filepath.Join(repoRoot, cleanPath)

	// Check if file exists for tracking created vs modified
	_, err := os.Stat(absPath)
	created := os.IsNotExist(err)

	// Ensure parent directory exists
	parent := filepath.Dir(absPath)
	if err := os.MkdirAll(parent, 0755); err != nil {
		return &WriteFileResult{
			Path:  params.Path,
			Error: fmt.Sprintf("cannot create directory: %v", err),
		}, nil
	}

	// Write or append content
	var writeErr error
	if params.Append {
		writeErr = appendToFile(absPath, params.Content)
	} else {
		writeErr = os.WriteFile(absPath, []byte(params.Content), 0644)
	}

	if writeErr != nil {
		return &WriteFileResult{
			Path:  params.Path,
			Error: fmt.Sprintf("write failed: %v", writeErr),
		}, nil
	}

	return &WriteFileResult{
		Path:     params.Path,
		Bytes:    len(params.Content),
		Created:  created,
		Appended: params.Append,
	}, nil
}

func appendToFile(path, content string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(content)
	return err
}
