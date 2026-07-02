package native

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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
			start, err = resolveWorkspacePath(t.root, args.Path)
			if err != nil {
				return registry.ToolResult{}, err
			}
		}

		matches, capped, err := t.searchFiles(ctx, start, args.Query, limit)
		if err != nil {
			return registry.ToolResult{}, err
		}

		content := limitOutput(strings.Join(matches, "\n"), t.maxOutputBytes)
		summary := fmt.Sprintf("found %d matches", len(matches))
		if capped {
			summary += " (capped)"
		}
		return registry.ToolResult{Summary: summary, Content: content}, nil
	}
	return tool
}

func (t *toolSet) searchFiles(ctx context.Context, start string, query string, limit int) ([]string, bool, error) {
	var files []string
	err := filepath.WalkDir(start, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			if repo.IsDefaultIgnoredDir(entry.Name()) && path != start {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type().IsRegular() {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return nil, false, err
	}

	sort.Strings(files)

	var matches []string
	capped := false
	for _, path := range files {
		fileMatches := t.searchFile(path, query, limit-len(matches))
		matches = append(matches, fileMatches...)
		if len(matches) >= limit {
			capped = true
			break
		}
	}

	return matches, capped, nil
}

func (t *toolSet) searchFile(path string, query string, remaining int) []string {
	if remaining <= 0 {
		return nil
	}

	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()

	rel, err := workspaceRel(t.root, path)
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
