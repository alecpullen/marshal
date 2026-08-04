package native

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"marshal/internal/app/session"
	"marshal/internal/tools/patch"
	"marshal/internal/tools/registry"
)

// maxPageableFileBytes is the largest file file.page will load into memory.
// It is intentionally larger than the per-tool output limit so the model can
// page through big files a screen at a time.
const maxPageableFileBytes = 10 * 1024 * 1024 // 10 MiB

type fileReadArgs struct {
	Path      string `json:"path"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
}

func (t *toolSet) fileReadTool() registry.Tool {
	tool := registry.Tool{
		Name:        "file.read",
		Description: "Read a workspace file. For large files, use start_line and end_line (1-based, inclusive) to page through content instead of reading the whole file at once.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"file path relative to the workspace"},"start_line":{"type":"integer","description":"1-based first line to return"},"end_line":{"type":"integer","description":"1-based last line to return"}},"required":["path"],"additionalProperties":false}`),
		Risk:        registry.RiskReadOnly,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		args, err := decodeArgs[fileReadArgs](tool, call.Args)
		if err != nil {
			return registry.ToolResult{}, err
		}
		if args.Path == "" {
			return registry.ToolResult{}, fmt.Errorf("file.read path is required")
		}
		if args.StartLine > 0 && args.EndLine > 0 && args.StartLine > args.EndLine {
			return registry.ToolResult{}, fmt.Errorf("file.read start_line must be <= end_line")
		}

		data, err := t.readWorkspaceFile(args.Path, int64(t.maxOutputBytes))
		if err != nil {
			return registry.ToolResult{}, err
		}

		content, start, end := selectLines(string(data), args.StartLine, args.EndLine)
		content = limitOutput(content, t.maxOutputBytes)

		return registry.ToolResult{
			Summary: fmt.Sprintf("read %s lines %d-%d", args.Path, start, end),
			Content: content,
		}, nil
	}
	return tool
}

func (t *toolSet) filePageTool() registry.Tool {
	type filePageArgs struct {
		Path     string `json:"path"`
		Page     int    `json:"page"`
		PageSize int    `json:"page_size"`
	}
	tool := registry.Tool{
		Name:        "file.page",
		Description: "Read a page of a workspace file by 1-based page number. Useful for iterating through large files without spilling tool output.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"file path relative to the workspace"},"page":{"type":"integer","description":"1-based page number"},"page_size":{"type":"integer","description":"lines per page (default 200, max 1000)"}},"required":["path","page"],"additionalProperties":false}`),
		Risk:        registry.RiskReadOnly,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		args, err := decodeArgs[filePageArgs](tool, call.Args)
		if err != nil {
			return registry.ToolResult{}, err
		}
		if args.Path == "" {
			return registry.ToolResult{}, fmt.Errorf("file.page path is required")
		}
		if args.Page < 1 {
			return registry.ToolResult{}, fmt.Errorf("file.page page must be >= 1")
		}
		pageSize := args.PageSize
		if pageSize <= 0 {
			pageSize = 200
		}
		const maxPageSize = 1000
		if pageSize > maxPageSize {
			pageSize = maxPageSize
		}

		data, err := t.readWorkspaceFile(args.Path, int64(maxPageableFileBytes))
		if err != nil {
			return registry.ToolResult{}, err
		}

		lines := strings.Split(string(data), "\n")
		if len(lines) > 0 && lines[len(lines)-1] == "" {
			lines = lines[:len(lines)-1]
		}
		totalLines := len(lines)
		startLine := (args.Page-1)*pageSize + 1
		if startLine > totalLines {
			return registry.ToolResult{}, fmt.Errorf("file.page page %d is past end of file (total lines %d)", args.Page, totalLines)
		}
		endLine := startLine + pageSize - 1
		if endLine > totalLines {
			endLine = totalLines
		}

		content := strings.Join(lines[startLine-1:endLine], "\n")
		if endLine == totalLines && strings.HasSuffix(string(data), "\n") {
			content += "\n"
		}
		content = limitOutput(content, t.maxOutputBytes)

		return registry.ToolResult{
			Summary: fmt.Sprintf("read %s page %d (lines %d-%d of %d)", args.Path, args.Page, startLine, endLine, totalLines),
			Content: content,
		}, nil
	}
	return tool
}

// readWorkspaceFile resolves and reads a regular workspace file up to maxBytes.
// It performs the same path validation, TOCTOU size check, and read tracking
// used by both file.read and file.page.
func (t *toolSet) readWorkspaceFile(requestedPath string, maxBytes int64) ([]byte, error) {
	path, err := resolveWorkspacePathMulti(t.activeRoot(), t.additionalRoots, requestedPath)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, t.enrichMissingFileError(requestedPath, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", requestedPath)
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", requestedPath, err)
	}
	defer f.Close()

	// Re-check size now that we have an open fd; closes the TOCTOU window
	// between os.Stat and the read. A symlink swap after open would change
	// the file backing the fd but the size is fixed at this point.
	info2, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat %s after open: %w", requestedPath, err)
	}
	if info2.Size() > maxBytes+1 {
		return nil, fmt.Errorf("%s is too large to read (%d bytes; limit %d)",
			requestedPath, info2.Size(), maxBytes)
	}

	cap := maxBytes + 1
	data, err := io.ReadAll(io.LimitReader(f, cap))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", requestedPath, err)
	}

	if t.fileTracker != nil {
		_ = t.fileTracker.RecordRead(path, time.Now())
	}
	return data, nil
}

// changedOnDiskError builds the "file changed on disk" error, embedding the
// current on-disk content's nearest-matching region for the patch's first
// chunk (mirroring patch.ValidatePatch's "search block not found" hint) so
// the model can retry the patch immediately instead of spending a separate
// tool-call round-trip re-reading the file first. Live testing showed this
// exact pattern recurring during multi-step edits.
func changedOnDiskError(path string, fp patch.FilePatch) error {
	base := fmt.Errorf("file %s changed on disk since last read; re-read it before editing", fp.Path)
	if len(fp.Chunks) == 0 {
		return base
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return base
	}
	region := patch.NearestRegion(string(data), fp.Chunks[0].Search, patch.NearestRegionWindowSize(fp.Chunks[0].Search))
	if region == "" {
		return base
	}
	return fmt.Errorf("%s\n\ncurrent content near the target:\n%s", base, region)
}

func (t *toolSet) enrichMissingFileError(requestedPath string, origErr error) error {
	baseErr := fmt.Errorf("stat %s: %w", requestedPath, origErr)
	if t.db == nil || t.projectID == 0 {
		return baseErr
	}

	basename := filepath.Base(requestedPath)
	paths, err := t.db.FilesMatchingBasename(t.projectID, basename, 5)
	if err != nil || len(paths) == 0 {
		return baseErr
	}

	var sb strings.Builder
	sb.WriteString(baseErr.Error())
	sb.WriteString("\n\nclosest indexed paths:\n")
	for _, p := range paths {
		sb.WriteString("  ")
		sb.WriteString(p)
		sb.WriteString("\n")
	}
	return fmt.Errorf("%s", sb.String())
}

func selectLines(content string, startLine int, endLine int) (string, int, int) {
	lines := strings.Split(content, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if startLine <= 0 {
		startLine = 1
	}
	if endLine <= 0 || endLine > len(lines) {
		endLine = len(lines)
	}
	if startLine > len(lines) {
		return "", startLine, endLine
	}
	selected := strings.Join(lines[startLine-1:endLine], "\n")
	if endLine == len(lines) && strings.HasSuffix(content, "\n") {
		selected += "\n"
	}
	return selected, startLine, endLine
}

type fileWritePatchArgs struct {
	Patch string `json:"patch"`
}

func (t *toolSet) fileWritePatchTool() registry.Tool {
	tool := registry.Tool{
		Name:        "file.write_patch",
		Description: "Apply a search/replace patch block format to files in the workspace.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"patch":{"type":"string"}},"required":["patch"],"additionalProperties":false}`),
		Risk:        registry.RiskWorkspaceWrite,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		args, err := decodeArgs[fileWritePatchArgs](tool, call.Args)
		if err != nil {
			return registry.ToolResult{}, err
		}

		patches, err := patch.Parse(args.Patch)
		if err != nil {
			return registry.ToolResult{}, fmt.Errorf("parse patch error: %w", err)
		}
		if len(patches) == 0 {
			return registry.ToolResult{}, fmt.Errorf("no valid patches found in proposal")
		}

		// Dry run first
		for _, fp := range patches {
			path, err := resolveWorkspacePathMulti(t.activeRoot(), t.additionalRoots, fp.Path)
			if err != nil {
				return registry.ToolResult{}, err
			}

			if t.fileTracker != nil {
				lastRead, hasRead, err := t.fileTracker.LastReadTime(path)
				if err != nil {
					return registry.ToolResult{}, fmt.Errorf(
						"cannot verify read state for %s: %w; re-read it before editing", fp.Path, err)
				}
				info, statErr := os.Stat(path)
				if statErr != nil {
					if os.IsNotExist(statErr) {
						// New file creation: no on-disk version to be stale against.
						continue
					}
					return registry.ToolResult{}, fmt.Errorf("stat %s: %w", fp.Path, statErr)
				}
				if hasRead && info.ModTime().After(lastRead) {
					return registry.ToolResult{}, changedOnDiskError(path, fp)
				}
				if !hasRead {
					return registry.ToolResult{}, fmt.Errorf(
						"file %s was never read this session; read it before editing", fp.Path)
				}
			}

			data, err := os.ReadFile(path)
			if err != nil {
				if os.IsNotExist(err) {
					// New file creation: no existing content to validate.
					continue
				}
				return registry.ToolResult{}, fmt.Errorf("read file %s: %w", fp.Path, err)
			}
			ok, err := patch.ValidatePatch(string(data), fp)
			if !ok || err != nil {
				return registry.ToolResult{}, fmt.Errorf("patch validation failed for %s: %v", fp.Path, err)
			}
		}

		// Generate diff to display in session
		var diffs []string
		var backups []session.BackupFile

		// Apply for real
		for _, fp := range patches {
			path, err := resolveWorkspacePathMulti(t.activeRoot(), t.additionalRoots, fp.Path)
			if err != nil {
				return registry.ToolResult{}, err
			}
			data, err := os.ReadFile(path)
			var original string
			if err != nil {
				if !os.IsNotExist(err) {
					return registry.ToolResult{}, err
				}
				// New file creation: verify SEARCH block is empty.
				for _, chunk := range fp.Chunks {
					if chunk.Search != "" {
						return registry.ToolResult{}, fmt.Errorf(
							"file %s does not exist but patch has a non-empty SEARCH block; use an empty SEARCH block to create a new file", fp.Path)
					}
				}
			} else {
				original = string(data)
			}

			info, err := os.Stat(path)
			var mode os.FileMode = 0644
			if err == nil {
				mode = info.Mode()
			}

			diff, err := patch.GenerateDiff(fp.Path, original, fp)
			if err == nil {
				diffs = append(diffs, diff)
			}

			backups = append(backups, session.BackupFile{
				Path:    fp.Path,
				Content: original,
				Mode:    mode,
			})

			patched := patch.ApplyPatch(original, fp)
			if strings.Contains(original, "\r\n") {
				patched = strings.ReplaceAll(patched, "\n", "\r\n")
			}

			// TOCTOU re-check — verify file hasn't been modified
			// between the validate loop and this write. This closes the window
			// between the fileTracker check in the validate loop and the actual
			// write. When fileTracker is nil (e.g. no session active) the check
			// is skipped, which is a known gap.
			if t.fileTracker != nil {
				lastRead, hasRead, lrErr := t.fileTracker.LastReadTime(path)
				if lrErr == nil && hasRead {
					writeStat, statErr := os.Stat(path)
					if statErr == nil && writeStat.ModTime().After(lastRead) {
						return registry.ToolResult{}, changedOnDiskError(path, fp)
					}
				}
			}

			if err := os.WriteFile(path, []byte(patched), mode); err != nil {
				return registry.ToolResult{}, fmt.Errorf("write file %s: %w", fp.Path, err)
			}

			if t.fileTracker != nil {
				_ = t.fileTracker.RecordWrite(path, time.Now())
				_ = t.fileTracker.RecordRead(path, time.Now())
			}
		}

		if t.sessionState != nil {
			t.sessionState.StoreBackup(backups)
		}

		var paths []string
		for _, fp := range patches {
			paths = append(paths, fp.Path)
		}

		content := strings.Join(diffs, "\n\n")
		if t.diagnostics != nil {
			diag, _ := t.diagnostics.Check(paths, languageOf(paths))
			if diag != "" {
				content += "\n\n" + diag
			}
		}

		return registry.ToolResult{
			Summary:      fmt.Sprintf("Applied patches to: %s", strings.Join(paths, ", ")),
			Content:      content,
			FilesChanged: append([]string(nil), paths...),
		}, nil
	}
	return tool
}
