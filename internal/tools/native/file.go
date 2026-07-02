package native

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

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

		path, err := resolveWorkspacePath(t.root, args.Path)
		if err != nil {
			return registry.ToolResult{}, err
		}
		info, err := os.Stat(path)
		if err != nil {
			return registry.ToolResult{}, fmt.Errorf("stat %s: %w", args.Path, err)
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
		return registry.ToolResult{
			Summary: fmt.Sprintf("read %s lines %d-%d", args.Path, start, end),
			Content: content,
		}, nil
	}
	return tool
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
			path, err := resolveWorkspacePath(t.root, fp.Path)
			if err != nil {
				return registry.ToolResult{}, err
			}
			data, err := os.ReadFile(path)
			if err != nil {
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
			path, err := resolveWorkspacePath(t.root, fp.Path)
			if err != nil {
				return registry.ToolResult{}, err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return registry.ToolResult{}, err
			}
			original := string(data)

			diff, err := patch.GenerateDiff(fp.Path, original, fp)
			if err == nil {
				diffs = append(diffs, diff)
			}

			backups = append(backups, session.BackupFile{
				Path:    fp.Path,
				Content: original,
			})

			patched := patch.ApplyPatch(original, fp)
			if err := os.WriteFile(path, []byte(patched), 0644); err != nil {
				return registry.ToolResult{}, fmt.Errorf("write file %s: %w", fp.Path, err)
			}
		}

		// Note: The caller orchestrator / loop is responsible for calling
		// state.StoreBackup(backups) when tool execution is approved.
		// For unit test purposes, we'll return summary list of modified files.
		var paths []string
		for _, fp := range patches {
			paths = append(paths, fp.Path)
		}

		return registry.ToolResult{
			Summary: fmt.Sprintf("Applied patches to: %s", strings.Join(paths, ", ")),
			Content: strings.Join(diffs, "\n\n"),
		}, nil
	}
	return tool
}

