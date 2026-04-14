// Package tools implements the tool-use executor interface for Marshal.
// This is a manual baseline implementation for comparison with Marshal's
// autonomous code generation.
package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ReadFileResult is the structured result from the read_file tool.
type ReadFileResult struct {
	Content   string `json:"content"`
	Path      string `json:"path"`
	Lines     int    `json:"lines"` // Total lines read
	Size      int    `json:"size"`  // Size in bytes
	Truncated bool   `json:"truncated,omitempty"`
	Error     string `json:"error,omitempty"`
}

// ReadFileInput is the input schema for the read_file tool.
type ReadFileInput struct {
	Path string `json:"path"`
	// Offset is the starting line (0-indexed). Optional, defaults to 0.
	Offset int `json:"offset,omitempty"`
	// Limit is the maximum number of lines to read. Optional, defaults to unlimited.
	Limit int `json:"limit,omitempty"`
}

const (
	// MaxFileSize is the maximum file size we'll read in bytes (1MB).
	MaxFileSize = 1 << 20
	// MaxLines is the maximum number of lines to return when using offset/limit.
	MaxLines = 250
)

// ReadFile reads a file from the repository with safety checks.
// It validates the path, checks for directory traversal attacks, enforces
// size limits, and supports optional line-based pagination.
func ReadFile(repoRoot string, input json.RawMessage) (*ReadFileResult, error) {
	var params ReadFileInput
	if err := json.Unmarshal(input, &params); err != nil {
		return &ReadFileResult{
			Error: fmt.Sprintf("invalid input: %v", err),
		}, nil
	}

	// Validate path is provided
	if strings.TrimSpace(params.Path) == "" {
		return &ReadFileResult{
			Error: "path is required",
		}, nil
	}

	// Clean and resolve the path
	cleanPath := filepath.Clean(params.Path)

	// Prevent directory traversal attacks
	if strings.HasPrefix(cleanPath, "..") || strings.Contains(cleanPath, "../") {
		return &ReadFileResult{
			Path:  params.Path,
			Error: "path traversal not allowed",
		}, nil
	}

	// Ensure path stays within repo root
	absPath := filepath.Join(repoRoot, cleanPath)
	if !strings.HasPrefix(absPath, repoRoot) {
		return &ReadFileResult{
			Path:  params.Path,
			Error: "path must be within repository",
		}, nil
	}

	// Check if path exists and is a regular file
	info, err := os.Stat(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			return &ReadFileResult{
				Path:  params.Path,
				Error: "file not found",
			}, nil
		}
		return &ReadFileResult{
			Path:  params.Path,
			Error: fmt.Sprintf("cannot access file: %v", err),
		}, nil
	}

	if info.IsDir() {
		return &ReadFileResult{
			Path:  params.Path,
			Error: "path is a directory, not a file",
		}, nil
	}

	// Enforce size limit
	if info.Size() > MaxFileSize {
		return &ReadFileResult{
			Path:      params.Path,
			Error:     fmt.Sprintf("file too large (%d bytes > %d limit)", info.Size(), MaxFileSize),
			Truncated: false,
		}, nil
	}

	// Read the file
	content, err := os.ReadFile(absPath)
	if err != nil {
		return &ReadFileResult{
			Path:  params.Path,
			Error: fmt.Sprintf("failed to read file: %v", err),
		}, nil
	}

	contentStr := string(content)
	totalLines := strings.Count(contentStr, "\n")
	if !strings.HasSuffix(contentStr, "\n") && contentStr != "" {
		totalLines++ // Count last line if no trailing newline
	}

	// Handle line-based pagination if requested
	if params.Offset > 0 || params.Limit > 0 {
		lines := strings.Split(contentStr, "\n")

		// Validate offset
		if params.Offset >= len(lines) {
			return &ReadFileResult{
				Path:      params.Path,
				Error:     fmt.Sprintf("offset %d exceeds file length (%d lines)", params.Offset, len(lines)),
				Truncated: true,
			}, nil
		}

		// Apply limit
		limit := params.Limit
		if limit <= 0 || limit > MaxLines {
			limit = MaxLines
		}

		end := params.Offset + limit
		truncated := false
		if end > len(lines) {
			end = len(lines)
		} else {
			truncated = true
		}

		contentStr = strings.Join(lines[params.Offset:end], "\n")
		readLines := end - params.Offset
		size := len(contentStr)

		return &ReadFileResult{
			Path:      params.Path,
			Content:   contentStr,
			Lines:     readLines,
			Size:      size,
			Truncated: truncated,
		}, nil
	}

	// Return full content (already size-limited by MaxFileSize check)
	return &ReadFileResult{
		Path:    params.Path,
		Content: contentStr,
		Lines:   totalLines,
		Size:    len(content),
	}, nil
}
