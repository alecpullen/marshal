package tools

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ReadFileAutoInput is the JSON schema input for the read_file_auto tool.
type ReadFileAutoInput struct {
	Path   string `json:"path"`           // Relative path within repository
	Offset int    `json:"offset,omitempty"` // 1-based starting line (default 1)
	Limit  int    `json:"limit,omitempty"`  // Max lines to return (default 1000, max 1000)
}

// ReadFileAutoResult is the JSON schema output for the read_file_auto tool.
type ReadFileAutoResult struct {
	Content    string `json:"content"`              // File content (paginated)
	Path       string `json:"path"`                 // Requested path
	TotalLines int    `json:"total_lines"`          // Total lines in file
	Offset     int    `json:"offset"`               // Actual offset used
	Limit      int    `json:"limit"`                // Actual limit used
	Truncated  bool   `json:"truncated"`            // True if content was truncated
	Error      string `json:"error,omitempty"`      // Error message if any
}

const (
	maxFileSize    = 1 * 1024 * 1024 // 1MB limit
	defaultLimit   = 1000
	maxLimit       = 1000
)

// ReadFileAuto reads a file with strict path validation preventing traversal attacks.
// It supports pagination via offset/limit and enforces a 1MB size limit.
func ReadFileAuto(repoRoot string, input json.RawMessage) (json.RawMessage, error) {
	var req ReadFileAutoInput
	if err := json.Unmarshal(input, &req); err != nil {
		result := ReadFileAutoResult{Error: fmt.Sprintf("invalid JSON input: %v", err)}
		return json.Marshal(result)
	}

	// Sanitize and validate path
	absPath, err := sanitizeAndValidatePath(repoRoot, req.Path)
	if err != nil {
		result := ReadFileAutoResult{
			Path:  req.Path,
			Error: err.Error(),
		}
		return json.Marshal(result)
	}

	// Check file existence and size
	info, err := os.Lstat(absPath)
	if err != nil {
		result := ReadFileAutoResult{
			Path:  req.Path,
			Error: fmt.Sprintf("file not accessible: %v", err),
		}
		return json.Marshal(result)
	}

	if info.IsDir() {
		result := ReadFileAutoResult{
			Path:  req.Path,
			Error: "path is a directory, not a file",
		}
		return json.Marshal(result)
	}

	// Check if it's a symlink (we need to resolve and validate target)
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(absPath)
		if err != nil {
			result := ReadFileAutoResult{
				Path:  req.Path,
				Error: fmt.Sprintf("cannot read symlink: %v", err),
			}
			return json.Marshal(result)
		}
		// Resolve relative to the symlink's directory
		linkDir := filepath.Dir(absPath)
		resolvedTarget := target
		if !filepath.IsAbs(target) {
			resolvedTarget = filepath.Clean(filepath.Join(linkDir, target))
		}
		
		// Validate the symlink target is within repo root
		if !isPathWithinRoot(resolvedTarget, repoRoot) {
			result := ReadFileAutoResult{
				Path:  req.Path,
				Error: "symlink points outside repository root",
			}
			return json.Marshal(result)
		}
		// Update absPath to the target for reading, but keep original for reporting
		info, err = os.Stat(resolvedTarget)
		if err != nil {
			result := ReadFileAutoResult{
				Path:  req.Path,
				Error: fmt.Sprintf("symlink target not accessible: %v", err),
			}
			return json.Marshal(result)
		}
		absPath = resolvedTarget
	}

	if info.Size() > maxFileSize {
		result := ReadFileAutoResult{
			Path:  req.Path,
			Error: fmt.Sprintf("file size %d exceeds maximum allowed %d bytes", info.Size(), maxFileSize),
		}
		return json.Marshal(result)
	}

	// Normalize pagination parameters
	offset := req.Offset
	if offset < 1 {
		offset = 1
	}
	limit := req.Limit
	if limit <= 0 {
		limit = defaultLimit
	}
	if limit > maxLimit {
		limit = maxLimit
	}

	// Read file with pagination
	content, totalLines, truncated, err := readFilePaginated(absPath, offset, limit)
	if err != nil {
		result := ReadFileAutoResult{
			Path:  req.Path,
			Error: fmt.Sprintf("read error: %v", err),
		}
		return json.Marshal(result)
	}

	result := ReadFileAutoResult{
		Content:    content,
		Path:       req.Path,
		TotalLines: totalLines,
		Offset:     offset,
		Limit:      limit,
		Truncated:  truncated,
	}
	return json.Marshal(result)
}

// sanitizeAndValidatePath performs strict validation to prevent path traversal.
// It rejects absolute paths, paths with .. components that escape the root,
// null bytes, and ensures the final resolved path is within repoRoot.
func sanitizeAndValidatePath(repoRoot, inputPath string) (string, error) {
	// Reject empty paths
	if strings.TrimSpace(inputPath) == "" {
		return "", fmt.Errorf("path cannot be empty")
	}

	// Reject null bytes which could bypass security checks
	if strings.Contains(inputPath, "\x00") {
		return "", fmt.Errorf("path contains null bytes")
	}

	// Reject absolute paths
	if filepath.IsAbs(inputPath) {
		return "", fmt.Errorf("absolute paths are not allowed")
	}

	// Normalize to forward slashes for validation, then convert back
	normalized := filepath.ToSlash(inputPath)
	
	// Split path and validate each component manually
	// This prevents bypassing checks via .. sequences
	parts := strings.Split(normalized, "/")
	depth := 0
	for _, part := range parts {
		if part == "" || part == "." {
			continue // Skip empty (leading/trailing slashes) and current dir
		}
		if part == ".." {
			depth--
			if depth < 0 {
				return "", fmt.Errorf("path traversal detected: '..' escapes repository root")
			}
		} else {
			// Validate component doesn't contain path separators disguised as unicode
			// or other malicious patterns
			if strings.Contains(part, "\\") || strings.Contains(part, "/") {
				return "", fmt.Errorf("invalid path component: %q", part)
			}
			depth++
		}
	}

	// Reconstruct the path with OS-specific separators
	cleanInput := filepath.FromSlash(normalized)
	
	// Join with repo root and clean
	fullPath := filepath.Join(repoRoot, cleanInput)
	cleanPath := filepath.Clean(fullPath)
	
	// Verify no symlink traversal by checking each path component
	if err := validatePathComponents(cleanPath, repoRoot); err != nil {
		return "", err
	}

	// Final verification: ensure resolved path is within repo root
	// We compare the cleaned absolute paths with proper separators
	rootAbs, err := filepath.Abs(repoRoot)
	if err != nil {
		return "", fmt.Errorf("cannot resolve repo root: %w", err)
	}
	pathAbs, err := filepath.Abs(cleanPath)
	if err != nil {
		return "", fmt.Errorf("cannot resolve path: %w", err)
	}
	
	// Ensure both end with separator for prefix check to prevent
	// partial directory name matches (e.g., /repo vs /repo2)
	if !strings.HasPrefix(pathAbs+string(filepath.Separator), rootAbs+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes repository root boundary")
	}

	return cleanPath, nil
}

// validatePathComponents checks each directory component in the path
// to ensure no intermediate symlinks point outside the repository.
func validatePathComponents(fullPath, repoRoot string) error {
	// Get relative path from repo root
	relPath, err := filepath.Rel(repoRoot, fullPath)
	if err != nil {
		return fmt.Errorf("path not within repository: %w", err)
	}
	
	// If relative path starts with "..", it's outside the root
	if strings.HasPrefix(relPath, "..") {
		return fmt.Errorf("path escapes repository root")
	}

	// Walk each component and check for symlinks
	current := repoRoot
	components := strings.Split(relPath, string(filepath.Separator))
	
	for _, comp := range components {
		if comp == "" || comp == "." {
			continue
		}
		
		next := filepath.Join(current, comp)
		
		// Check if this specific path component is a symlink
		info, err := os.Lstat(next)
		if err != nil {
			if os.IsNotExist(err) {
				// File doesn't exist, which is OK for the final component (might be new file)
				// but intermediate directories must exist
				// Check if we're at the last component
				if current == fullPath || next == fullPath {
					return nil // Last component can be non-existent
				}
				return fmt.Errorf("intermediate path does not exist: %s", next)
			}
			return fmt.Errorf("cannot stat path component %s: %w", next, err)
		}
		
		// If it's a symlink, verify the target is within root
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(next)
			if err != nil {
				return fmt.Errorf("cannot read symlink %s: %w", next, err)
			}
			
			// Resolve relative symlink targets
			var resolvedTarget string
			if filepath.IsAbs(target) {
				resolvedTarget = filepath.Clean(target)
			} else {
				resolvedTarget = filepath.Clean(filepath.Join(current, target))
			}
			
			// Symlink must point within repo root
			if !isPathWithinRoot(resolvedTarget, repoRoot) {
				return fmt.Errorf("symlink at %s points outside repository root", next)
			}
		}
		
		current = next
	}
	
	return nil
}

// isPathWithinRoot checks if target is within repoRoot (inclusive).
// Both paths should be clean absolute paths.
func isPathWithinRoot(target, repoRoot string) bool {
	// Ensure both paths are absolute and clean
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return false
	}
	rootAbs, err := filepath.Abs(repoRoot)
	if err != nil {
		return false
	}
	
	targetAbs = filepath.Clean(targetAbs)
	rootAbs = filepath.Clean(rootAbs)
	
	// Check if target is exactly root, or is a subpath
	if targetAbs == rootAbs {
		return true
	}
	
	// Ensure root ends with separator for prefix matching
	if !strings.HasSuffix(rootAbs, string(filepath.Separator)) {
		rootAbs += string(filepath.Separator)
	}
	if !strings.HasSuffix(targetAbs, string(filepath.Separator)) {
		targetAbs += string(filepath.Separator)
	}
	
	return strings.HasPrefix(targetAbs, rootAbs)
}

// readFilePaginated reads a file and returns specific lines with pagination.
func readFilePaginated(path string, offset, limit int) (content string, totalLines int, truncated bool, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, false, err
	}
	defer f.Close()

	var buf bytes.Buffer
	scanner := bufio.NewScanner(f)
	lineNum := 0
	startLine := offset
	endLine := offset + limit - 1

	for scanner.Scan() {
		lineNum++
		if lineNum >= startLine && lineNum <= endLine {
			buf.Write(scanner.Bytes())
			buf.WriteByte('\n')
		}
	}

	if err := scanner.Err(); err != nil {
		return "", 0, false, err
	}

	// Determine if truncated (more lines exist beyond our read)
	truncated = lineNum > endLine

	// Remove trailing newline for cleaner output
	result := buf.String()
	if len(result) > 0 && result[len(result)-1] == '\n' {
		result = result[:len(result)-1]
	}

	return result, lineNum, truncated, nil
}
