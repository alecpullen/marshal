package native

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
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

// readPathBaseDescription is the path schema base text for the read
// tools; kept short — schema descriptions are prompt budget.
const readPathBaseDescription = "file path relative to the workspace; absolute paths that resolve inside an allowed root are accepted"

type fileReadArgs struct {
	Path      string `json:"path"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
}

func (t *toolSet) fileReadTool() registry.Tool {
	tool := registry.Tool{
		Name:        "file.read",
		Description: "Read a workspace file. For large files, use start_line and end_line (1-based, inclusive) to page through content instead of reading the whole file at once.",
		Schema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":` +
			t.pathDescription(readPathBaseDescription) +
			`},"start_line":{"type":"integer","description":"1-based first line to return"},"end_line":{"type":"integer","description":"1-based last line to return"}},"required":["path"],"additionalProperties":false}`),
		Risk: registry.RiskReadOnly,
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
		Schema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":` +
			t.pathDescription(readPathBaseDescription) +
			`},"page":{"type":"integer","description":"1-based page number"},"page_size":{"type":"integer","description":"lines per page (default 200, max 1000)"}},"required":["path","page"],"additionalProperties":false}`),
		Risk: registry.RiskReadOnly,
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
	path, err := resolveNamedRootRead(t.namedRoots, t.activeRoot(), t.effectiveAdditionalRoots(), requestedPath)
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
		Name: "file.write_patch",
		Description: "Apply one or more search/replace patch blocks to workspace files. " +
			"Each block edits one file; chain multiple blocks in a single call to edit several files at once. " +
			"The SEARCH block must match the current file content exactly. " +
			"To create a new file, use an empty SEARCH block. " +
			"Unified diff format (---/+++/@@ hunks) is also accepted and converted internally. " +
			"Always read before editing a file if you have not already done so this session. " +
			"Every SEARCH/REPLACE block must end with >>>>>>> REPLACE; unified diffs use standard ---/+++/@@ syntax.",
		Schema: json.RawMessage(`{"type":"object","properties":{"patch":{"type":"string","description":` +
			t.pathDescription("search/replace patch blocks; each block's `File:` header is a file path relative to the workspace") +
			`}},"required":["patch"],"additionalProperties":false}`),
		Risk: registry.RiskWorkspaceWrite,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		args, err := decodeArgs[fileWritePatchArgs](tool, call.Args)
		if err != nil {
			return registry.ToolResult{}, err
		}

		parsed, err := patch.ParseRepairing(args.Patch)
		if err != nil {
			return registry.ToolResult{}, fmt.Errorf("parse patch error: %w", err)
		}
		patches, patchRepairs := parsed.Patches, parsed.Repairs
		if len(patches) == 0 {
			return registry.ToolResult{}, fmt.Errorf("no valid patches found in proposal")
		}

		// Dry run first
		for _, fp := range patches {
			path, err := resolveNamedRoot(t.namedRoots, t.activeRoot(), t.effectiveAdditionalRoots(), fp.Path)
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
			} else {
				slog.Warn("file.write_patch with nil fileTracker; TOCTOU check skipped",
					"path", fp.Path)
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
		var symbols []registry.SymbolRef

		// Apply for real
		for _, fp := range patches {
			path, err := resolveNamedRoot(t.namedRoots, t.activeRoot(), t.effectiveAdditionalRoots(), fp.Path)
			if err != nil {
				return registry.ToolResult{}, err
			}
			data, err := os.ReadFile(path)
			var original string
			fileExists := err == nil
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
				Exists:  fileExists,
			})

			patched := patch.ApplyPatch(original, fp)
			if diff != "" {
				symbols = append(symbols, symbolsForEdit(ctx, fp.Path, patched, diff)...)
			}
			if strings.Contains(original, "\r\n") {
				// Normalize safely: collapse existing CRLF to LF first so a
				// naive LF->CRLF conversion does not turn CRLF into CRCRLF.
				patched = strings.ReplaceAll(patched, "\r\n", "\n")
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
			} else {
				slog.Warn("file.write_patch TOCTOU re-check skipped: nil fileTracker",
					"path", fp.Path)
			}

			if err := os.WriteFile(path, []byte(patched), mode); err != nil {
				return registry.ToolResult{}, fmt.Errorf("write file %s: %w", fp.Path, err)
			}

			if t.fileTracker != nil {
				_ = t.fileTracker.RecordWrite(path, time.Now())
				_ = t.fileTracker.RecordRead(path, time.Now())
			}
		}

		if ws := t.wsState(); ws != nil {
			ws.StoreBackup(backups)
		}

		var paths []string
		for _, fp := range patches {
			paths = append(paths, fp.Path)
		}

		content := strings.Join(diffs, "\n\n")
		// Surface healed format mistakes so the model corrects its next
		// proposal instead of relying on the repair. The patch applied, so
		// this is a notice appended to a success, not an error.
		if len(patchRepairs) > 0 {
			content += "\n\nNote — the proposal's format was repaired before applying:\n- " +
				strings.Join(patchRepairs, "\n- ") +
				"\nClose every REPLACE block with \">>>>>>> REPLACE\"."
		}
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
			Symbols:      symbols,
		}, nil
	}
	return tool
}

type fileWriteArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// fileWriteTool creates or overwrites a whole file with the exact content
// provided. It shares file.write_patch's plumbing: root resolution, the
// stale-file guard, backups, diff generation, diagnostics, and changed-files
// tracking. New files skip the stale/read guard (no on-disk version to be
// stale against); existing files must have been read this session.
func (t *toolSet) fileWriteTool() registry.Tool {
	tool := registry.Tool{
		Name: "file.write",
		Description: "Create a new file or overwrite an existing file with the exact content provided. " +
			"Prefer this over file.write_patch when writing a whole new file or replacing most of a file's content; " +
			"use file.write_patch for targeted edits. " +
			"Never write files via shell.run redirection or heredocs — those bypass diff review and rollback. " +
			"Always read before overwriting an existing file.",
		Schema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":` +
			t.pathDescription("file path relative to the workspace") +
			`},"content":{"type":"string","description":"exact file content to write"}},"required":["path","content"],"additionalProperties":false}`),
		Risk: registry.RiskWorkspaceWrite,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		args, err := decodeArgs[fileWriteArgs](tool, call.Args)
		if err != nil {
			return registry.ToolResult{}, err
		}

		path, err := resolveNamedRoot(t.namedRoots, t.activeRoot(), t.effectiveAdditionalRoots(), args.Path)
		if err != nil {
			return registry.ToolResult{}, err
		}

		// Read the current on-disk state (if any) and enforce the stale-file
		// contract for existing files.
		data, readErr := os.ReadFile(path)
		var original string
		exists := readErr == nil
		if readErr != nil && !os.IsNotExist(readErr) {
			return registry.ToolResult{}, fmt.Errorf("read file %s: %w", args.Path, readErr)
		}
		if exists {
			original = string(data)
			if t.fileTracker == nil {
				slog.Warn("file.write on existing file with nil fileTracker; TOCTOU check skipped",
					"path", args.Path)
				return registry.ToolResult{}, fmt.Errorf(
					"file %s already exists; file.write requires a tracker-backed session to overwrite an existing file", args.Path)
			}
			lastRead, hasRead, lrErr := t.fileTracker.LastReadTime(path)
			if lrErr != nil {
				return registry.ToolResult{}, fmt.Errorf(
					"cannot verify read state for %s: %w; re-read it before editing", args.Path, lrErr)
			}
			info, statErr := os.Stat(path)
			if statErr != nil {
				return registry.ToolResult{}, fmt.Errorf("stat %s: %w", args.Path, statErr)
			}
			if hasRead && info.ModTime().After(lastRead) {
				return registry.ToolResult{}, changedOnDiskError(path, patch.FilePatch{Path: args.Path, Chunks: []patch.PatchChunk{{Search: original}}})
			}
			if !hasRead {
				return registry.ToolResult{}, fmt.Errorf(
					"file %s was never read this session; read it before editing", args.Path)
			}
		}

		info, err := os.Stat(path)
		var mode os.FileMode = 0644
		if err == nil {
			mode = info.Mode()
		}

		// Synthesize a whole-file patch so the in-session diff looks identical
		// to a patch write: empty SEARCH for a new file, whole-file SEARCH
		// otherwise.
		fp := patch.FilePatch{Path: args.Path}
		if exists {
			fp.Chunks = []patch.PatchChunk{{Search: original, Replace: args.Content}}
		} else {
			fp.Chunks = []patch.PatchChunk{{Search: "", Replace: args.Content}}
		}

		content := args.Content
		if strings.Contains(original, "\r\n") {
			// Normalize safely: collapse existing CRLF to LF first so a
			// naive LF->CRLF conversion does not turn CRLF into CRCRLF.
			content = strings.ReplaceAll(content, "\r\n", "\n")
			content = strings.ReplaceAll(content, "\n", "\r\n")
		}

		// Generate the diff using the content that will actually be
		// written, so the displayed diff matches the on-disk bytes. The
		// whole-file patch is synthesized from the normalized content.
		writeFP := patch.FilePatch{Path: args.Path}
		if exists {
			writeFP.Chunks = []patch.PatchChunk{{Search: original, Replace: content}}
		} else {
			writeFP.Chunks = []patch.PatchChunk{{Search: "", Replace: content}}
		}
		var diff string
		if d, dErr := patch.GenerateDiff(args.Path, original, writeFP); dErr == nil {
			diff = d
		}

		// TOCTOU re-check for existing files: verify the file hasn't changed
		// between the read above and this write.
		if t.fileTracker != nil && exists {
			lastRead, hasRead, lrErr := t.fileTracker.LastReadTime(path)
			if lrErr == nil && hasRead {
				writeStat, statErr := os.Stat(path)
				if statErr == nil && writeStat.ModTime().After(lastRead) {
					return registry.ToolResult{}, changedOnDiskError(path, fp)
				}
			}
		}

		if err := os.WriteFile(path, []byte(content), mode); err != nil {
			return registry.ToolResult{}, fmt.Errorf("write file %s: %w", args.Path, err)
		}

		if t.fileTracker != nil {
			_ = t.fileTracker.RecordWrite(path, time.Now())
			_ = t.fileTracker.RecordRead(path, time.Now())
		}

		if ws := t.wsState(); ws != nil {
			ws.StoreBackup([]session.BackupFile{{
				Path:    args.Path,
				Content: original,
				Mode:    mode,
				Exists:  exists,
			}})
		}

		result := registry.ToolResult{
			Summary:      fmt.Sprintf("Wrote %s", args.Path),
			Content:      diff,
			FilesChanged: []string{args.Path},
			Symbols:      symbolsForEdit(ctx, args.Path, content, diff),
		}
		if t.diagnostics != nil {
			diag, _ := t.diagnostics.Check([]string{args.Path}, languageOf([]string{args.Path}))
			if diag != "" {
				result.Content += "\n\n" + diag
			}
		}
		return result, nil
	}
	return tool
}
