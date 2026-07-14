package native

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"marshal/internal/app/session"
	"marshal/internal/tools/patch"
	"marshal/internal/tools/registry"
)

type fileReadArgs struct {
	Path      string `json:"path"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
}

func (t *toolSet) fileReadTool() registry.Tool {
	tool := registry.Tool{
		Name:        "file.read",
		Description: "Read a workspace file, optionally limited to a 1-based line range.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"start_line":{"type":"integer"},"end_line":{"type":"integer"}},"required":["path"]}`),
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

		path, err := resolveWorkspacePathMulti(t.root, t.additionalRoots, args.Path)
		if err != nil {
			return registry.ToolResult{}, err
		}
		info, err := os.Stat(path)
		if err != nil {
			return registry.ToolResult{}, t.enrichMissingFileError(args.Path, err)
		}
		if !info.Mode().IsRegular() {
			return registry.ToolResult{}, fmt.Errorf("%s is not a regular file", args.Path)
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return registry.ToolResult{}, fmt.Errorf("read %s: %w", args.Path, err)
		}

		content, start, end := selectLines(string(data), args.StartLine, args.EndLine)
		content = limitOutput(content, t.maxOutputBytes)

		if t.fileTracker != nil {
			_ = t.fileTracker.RecordRead(path, time.Now())
		}

		return registry.ToolResult{
			Summary: fmt.Sprintf("read %s lines %d-%d", args.Path, start, end),
			Content: content,
		}, nil
	}
	return tool
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
		Schema:      json.RawMessage(`{"type":"object","properties":{"patch":{"type":"string"}},"required":["patch"]}`),
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
			path, err := resolveWorkspacePathMulti(t.root, t.additionalRoots, fp.Path)
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
						// Continue checking other patches but still need to validate below.
					} else {
						return registry.ToolResult{}, fmt.Errorf("stat %s: %w", fp.Path, statErr)
					}
				}
				if statErr == nil && hasRead && info.ModTime().After(lastRead) {
					return registry.ToolResult{}, fmt.Errorf(
						"file %s changed on disk since last read; re-read it before editing", fp.Path)
				}
				if statErr == nil && !hasRead {
					return registry.ToolResult{}, fmt.Errorf(
						"file %s was never read this session; read it before editing", fp.Path)
				}
			}

			// Read the file for validation; if it doesn't exist, use empty content.
			var content string
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				if !os.IsNotExist(readErr) {
					return registry.ToolResult{}, fmt.Errorf("read file %s: %w", fp.Path, readErr)
				}
				// New file: validate with empty content.
				content = ""
			} else {
				content = string(data)
			}
			ok, err := patch.ValidatePatch(content, fp)
			if !ok || err != nil {
				return registry.ToolResult{}, fmt.Errorf("patch validation failed for %s: %v", fp.Path, err)
			}
		}

		// Generate diff to display in session
		var diffs []string
		var backups []session.BackupFile

		// Apply for real
		for _, fp := range patches {
			path, err := resolveWorkspacePathMulti(t.root, t.additionalRoots, fp.Path)
			if err != nil {
				return registry.ToolResult{}, err
			}
			var original string
			info, statErr := os.Stat(path)
			var mode os.FileMode = 0o644
			newFile := false
			if statErr != nil {
				if !os.IsNotExist(statErr) {
					return registry.ToolResult{}, fmt.Errorf("stat %s: %w", fp.Path, statErr)
				}
				// New file: only allowed when every chunk has an empty Search.
				for _, c := range fp.Chunks {
					if c.Search != "" {
						return registry.ToolResult{}, fmt.Errorf(
							"file %s does not exist; non-empty search block is not allowed for new files", fp.Path)
					}
				}
				newFile = true
			} else {
				mode = info.Mode()
				data, readErr := os.ReadFile(path)
				if readErr != nil {
					return registry.ToolResult{}, fmt.Errorf("read file %s: %w", fp.Path, readErr)
				}
				original = string(data)
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
			if !newFile && strings.Contains(original, "\r\n") {
				patched = strings.ReplaceAll(patched, "\n", "\r\n")
			}

			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return registry.ToolResult{}, fmt.Errorf("mkdir for %s: %w", fp.Path, err)
			}
			if err := os.WriteFile(path, []byte(patched), mode); err != nil {
				return registry.ToolResult{}, fmt.Errorf("write file %s: %w", fp.Path, err)
			}

			if t.fileTracker != nil {
				_ = t.fileTracker.RecordWrite(path, time.Now())
				_ = t.fileTracker.RecordRead(path, time.Now())
			}
		}

		if state, ok := t.sessionState.(*session.State); ok && state != nil {
			state.StoreBackup(backups)
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
