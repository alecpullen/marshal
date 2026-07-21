package native

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"marshal/internal/repo"
	"marshal/internal/tools/registry"
)

const defaultSearchMaxResults = 50
const hardSearchMaxResults = 200

type repoSearchArgs struct {
	Query      string `json:"query"`
	Path       string `json:"path"`
	MaxResults int    `json:"max_results"`
}

func (t *toolSet) repoSearchTool() registry.Tool {
	tool := registry.Tool{
		Name:        "repo.search",
		Description: "Search workspace files for a case-sensitive substring.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"},"path":{"type":"string"},"max_results":{"type":"integer"}},"required":["query"]}`),
		Risk:        registry.RiskReadOnly,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		args, err := decodeArgs[repoSearchArgs](tool, call.Args)
		if err != nil {
			return registry.ToolResult{}, err
		}
		if args.Query == "" {
			return registry.ToolResult{}, fmt.Errorf("repo.search query is required")
		}

		limit := args.MaxResults
		if limit <= 0 {
			limit = defaultSearchMaxResults
		}
		if limit > hardSearchMaxResults {
			limit = hardSearchMaxResults
		}

		start := t.root
		if args.Path != "" {
			start, err = resolveWorkspacePathMulti(t.root, t.additionalRoots, args.Path)
			if err != nil {
				return registry.ToolResult{}, err
			}
		}

		matches, capped, walkErrs, err := t.searchFiles(ctx, start, args.Query, limit)
		if err != nil {
			return registry.ToolResult{}, err
		}

		content := limitOutput(strings.Join(matches, "\n"), t.maxOutputBytes)
		summary := fmt.Sprintf("found %d matches", len(matches))
		if capped {
			summary += " (capped)"
		}
		if len(walkErrs) > 0 {
			summary += fmt.Sprintf(", %d walk errors", len(walkErrs))
		}
		return registry.ToolResult{Summary: summary, Content: content}, nil
	}
	return tool
}

func (t *toolSet) searchFiles(ctx context.Context, start string, query string, limit int) ([]string, bool, []error, error) {
	var walkErrs []error
	var matches []string
	capped := false

	err := filepath.WalkDir(start, func(path string, entry fs.DirEntry, walkErr error) error {
		// Collect walk errors instead of swallowing them.
		if walkErr != nil {
			walkErrs = append(walkErrs, fmt.Errorf("%s: %w", path, walkErr))
			if entry != nil && entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		// Short-circuit once the cap is reached.
		if len(matches) >= limit {
			return filepath.SkipAll
		}
		// Skip all symlinks — WalkDir does not follow directory symlinks on
		// most platforms, but this explicit check acts as a belt-and-suspenders
		// defense so we never accidentally descend into or read a symlink.
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if entry.IsDir() {
			if repo.IsDefaultIgnoredDir(entry.Name()) && path != start {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type().IsRegular() {
			fileMatches := t.searchFile(path, query, limit-len(matches))
			matches = append(matches, fileMatches...)
			if len(matches) >= limit {
				capped = true
			}
		}
		return nil
	})
	if errors.Is(err, filepath.SkipAll) {
		err = nil
	}
	if err != nil {
		return nil, false, walkErrs, err
	}

	return matches, capped, walkErrs, nil
}

func (t *toolSet) searchFile(path string, query string, remaining int) []string {
	if remaining <= 0 {
		return nil
	}

	// Skip files that exceed the configurable size cap for searching.
	if t.maxSearchableFileBytes > 0 {
		if info, err := os.Stat(path); err == nil && info.Size() > t.maxSearchableFileBytes {
			return nil
		}
	}

	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()

	// Re-verify the file is under the workspace root (F-SEC-123).
	// This is a layered defense: the walk already skips symlinks, but
	// workspaceRel provides a second check against any path that might
	// have escaped the root (e.g. on platforms where WalkDir follows
	// directory symlinks, or for any other unforeseen traversal path).
	// Resolve the path through symlinks first so the rel computation
	// matches the resolved root that workspaceRel uses.
	resolvedPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil
	}
	rel, err := workspaceRel(t.root, resolvedPath)
	if err != nil {
		return nil
	}

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	var matches []string
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := scanner.Text()
		if strings.Contains(line, query) {
			matches = append(matches, fmt.Sprintf("%s:%d:%s", rel, lineNo, line))
			if len(matches) >= remaining {
				return matches
			}
		}
	}

	return matches
}
