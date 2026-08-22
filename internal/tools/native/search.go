package native

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"marshal/internal/repo"
	"marshal/internal/tools/registry"
)

const defaultSearchMaxResults = 50
const hardSearchMaxResults = 200

// lineMatcher reports whether a single line of text is a search hit. It is
// either a substring check or a compiled RE2 regexp, built by repoSearchTool.
type lineMatcher func(line string) bool

type repoSearchArgs struct {
	Query      string `json:"query"`
	Path       string `json:"path"`
	MaxResults int    `json:"max_results"`
	Mode       string `json:"mode"`
	Include    string `json:"include"`
	Context    int    `json:"context"`
}

func (t *toolSet) repoSearchTool() registry.Tool {
	tool := registry.Tool{
		Name:        "repo.search",
		Description: "Search workspace files for matching lines. Default is a case-sensitive substring match; set mode=regex for an RE2 regular expression. Optional include glob filters files (e.g. *.go), context adds 0-3 surrounding lines per match.",
		Schema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"},"path":{"type":"string","description":` +
			t.pathDescription("directory or file to search, relative to the workspace") +
			`},"max_results":{"type":"integer"},"mode":{"type":"string","enum":["substring","regex"]},"include":{"type":"string"},"context":{"type":"integer"}},"required":["query"],"additionalProperties":false}`),
		Risk: registry.RiskReadOnly,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		args, err := decodeArgs[repoSearchArgs](tool, call.Args)
		if err != nil {
			return registry.ToolResult{}, err
		}
		if args.Query == "" {
			return registry.ToolResult{}, fmt.Errorf("repo.search query is required")
		}

		var match lineMatcher
		switch args.Mode {
		case "", "substring":
			query := args.Query
			match = func(line string) bool { return strings.Contains(line, query) }
		case "regex":
			re, err := regexp.Compile(args.Query)
			if err != nil {
				return registry.ToolResult{}, fmt.Errorf("repo.search invalid regex %q: %w", args.Query, err)
			}
			match = re.MatchString
		default:
			return registry.ToolResult{}, fmt.Errorf("repo.search mode must be substring|regex, got %q", args.Mode)
		}

		if args.Context < 0 || args.Context > 3 {
			return registry.ToolResult{}, fmt.Errorf("repo.search context must be between 0 and 3, got %d", args.Context)
		}
		if args.Include != "" {
			if _, err := path.Match(args.Include, ""); err != nil {
				return registry.ToolResult{}, fmt.Errorf("repo.search invalid include glob %q: %w", args.Include, err)
			}
		}

		limit := args.MaxResults
		if limit <= 0 {
			limit = defaultSearchMaxResults
		}
		if limit > hardSearchMaxResults {
			limit = hardSearchMaxResults
		}

		start := t.activeRoot()
		if args.Path != "" {
			start, err = resolveNamedRoot(t.namedRoots, t.activeRoot(), t.additionalRoots, args.Path)
			if err != nil {
				return registry.ToolResult{}, err
			}
		}

		matches, capped, walkErrs, err := t.searchFiles(ctx, start, match, args.Include, args.Context, limit)
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

// matchInclude reports whether rel (a slash-separated workspace-relative
// path) matches the include glob. The pattern is tried against both the
// full relative path and the base name, so "*.go" and "internal/*.go" both
// behave the way grep --include users expect.
func matchInclude(pattern, rel string) (bool, error) {
	if ok, err := path.Match(pattern, rel); ok || err != nil {
		return ok, err
	}
	return path.Match(pattern, path.Base(rel))
}

func (t *toolSet) searchFiles(ctx context.Context, start string, match lineMatcher, include string, ctxLines int, limit int) ([]string, bool, []error, error) {
	var walkErrs []error
	var matches []string
	matchCount := 0
	capped := false

	// Load root .gitignore for the search start directory.
	rootGitignore, _ := repo.LoadGitignore(filepath.Join(start, ".gitignore"))
	stack := repo.NewGitignoreStack(rootGitignore)

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
		if matchCount >= limit {
			return filepath.SkipAll
		}
		// Skip all symlinks — WalkDir does not follow directory symlinks on
		// most platforms, but this explicit check is an extra
		// defense so we never accidentally descend into or read a symlink.
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if path == start {
			return nil
		}

		rel, relErr := filepath.Rel(start, path)
		if relErr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)

		// Check gitignore
		if stack != nil && stack.Match(rel, entry.IsDir()) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if entry.IsDir() {
			if repo.IsDefaultIgnoredDir(entry.Name()) && path != start {
				return filepath.SkipDir
			}
			// Push per-directory .gitignore
			if rel != "." {
				stack.PopTo(rel)
				giPath := filepath.Join(start, rel, ".gitignore")
				if gi, err := repo.LoadGitignore(giPath); err == nil && gi.Patterns() > 0 {
					stack.Push(rel, gi)
				}
			}
			return nil
		}
		if entry.Type().IsRegular() {
			fileLines, n := t.searchFile(path, match, include, ctxLines, limit-matchCount)
			matches = append(matches, fileLines...)
			matchCount += n
			if matchCount >= limit {
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

// contextLine is a remembered pre-match line, kept so it can be emitted as
// leading context if a later match lands within ctxLines of it.
type contextLine struct {
	no   int
	text string
}

// searchFile returns the formatted lines to emit (matches as
// "rel:line:text", context as "rel-line-text") and the number of actual
// matches, which is what counts against remaining.
func (t *toolSet) searchFile(path string, match lineMatcher, include string, ctxLines int, remaining int) ([]string, int) {
	if remaining <= 0 {
		return nil, 0
	}

	// Skip files that exceed the configurable size cap for searching.
	if t.maxSearchableFileBytes > 0 {
		if info, err := os.Stat(path); err == nil && info.Size() > t.maxSearchableFileBytes {
			return nil, 0
		}
	}

	file, err := os.Open(path)
	if err != nil {
		return nil, 0
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
		return nil, 0
	}
	rel, err := workspaceRel(t.activeRoot(), resolvedPath)
	if err != nil {
		return nil, 0
	}

	if include != "" {
		ok, err := matchInclude(include, rel)
		if err != nil || !ok {
			return nil, 0
		}
	}

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	var lines []string
	matchCount := 0
	lineNo := 0
	after := 0       // trailing context lines still owed to the previous match
	emittedUpTo := 0 // highest line number already emitted (match or context)
	var prev []contextLine
	for scanner.Scan() {
		lineNo++
		line := scanner.Text()
		if match(line) {
			for _, p := range prev {
				if p.no > emittedUpTo {
					lines = append(lines, fmt.Sprintf("%s-%d-%s", rel, p.no, p.text))
				}
			}
			lines = append(lines, fmt.Sprintf("%s:%d:%s", rel, lineNo, line))
			emittedUpTo = lineNo
			matchCount++
			after = ctxLines
			if matchCount >= remaining {
				return lines, matchCount
			}
		} else if after > 0 {
			lines = append(lines, fmt.Sprintf("%s-%d-%s", rel, lineNo, line))
			emittedUpTo = lineNo
			after--
		}
		if ctxLines > 0 {
			prev = append(prev, contextLine{no: lineNo, text: line})
			if len(prev) > ctxLines {
				prev = prev[1:]
			}
		}
	}

	return lines, matchCount
}
