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

	"marshal/internal/db"
	"marshal/internal/repo"
	"marshal/internal/tools/registry"
)

const defaultSearchMaxResults = 50

// hardSearchMaxResults is a var (not a const) so tests can lower it and
// exercise the capped-results notice without generating 200 matches.
var hardSearchMaxResults = 200

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
	Kind       string `json:"kind"`
}

func (t *toolSet) repoSearchTool() registry.Tool {
	tool := registry.Tool{
		Name:        "repo.search",
		Description: "Search workspace files for matching lines. Default mode=auto: bare words do a case-sensitive substring match, pattern-shaped queries are compiled as RE2 (choice echo in summary). Use kind=func|method|type|import to resolve via the symbol index instead of line-matching. context adds 0-3 surrounding lines; results cap at 200 with guidance.",
		Schema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"},"path":{"type":"string","description":` +
			t.pathDescription("directory or file to search, relative to the workspace") +
			`},"max_results":{"type":"integer"},"mode":{"type":"string","enum":["auto","substring","regex"]},"kind":{"type":"string","enum":["function","method","type","import"],"description":"Resolve via the symbol index instead of line-matching; when set, context and mode are rejected, while path, include, and max_results still filter results"},"include":{"type":"string"},"context":{"type":"integer"}},"required":["query"],"additionalProperties":false}`),
		Risk: registry.RiskReadOnly,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		args, err := decodeArgs[repoSearchArgs](tool, call.Args)
		if err != nil {
			return registry.ToolResult{}, err
		}

		// Kind queries resolve through the symbol index and reject the
		// line-matching knobs (context, non-auto mode) up front.
		if args.Kind != "" {
			if !validSymbolKinds[args.Kind] {
				return registry.ToolResult{}, fmt.Errorf("repo.search kind %q is not one of function, method, type, import", args.Kind)
			}
			if args.Context != 0 {
				return registry.ToolResult{}, fmt.Errorf("repo.search context does not apply when kind is set")
			}
			if args.Mode != "" && args.Mode != "auto" {
				return registry.ToolResult{}, fmt.Errorf("repo.search mode does not apply when kind is set")
			}
			if t.db == nil || t.projectID == 0 {
				return registry.ToolResult{}, errors.New("database not configured for repo.search kind queries")
			}
		}

		if args.Query == "" && args.Kind == "" {
			return registry.ToolResult{}, fmt.Errorf("repo.search query is required")
		}

		// Limit is needed by both the line-matching walk and the kind branch,
		// so compute it before either path runs.
		limit := args.MaxResults
		if limit <= 0 {
			limit = defaultSearchMaxResults
		}
		if limit > hardSearchMaxResults {
			limit = hardSearchMaxResults
		}

		if args.Kind != "" {
			// Validate the include glob up front, exactly as the line-search
			// path does, so a bad pattern is a clear error rather than a
			// silently empty result.
			if args.Include != "" {
				if _, err := path.Match(args.Include, ""); err != nil {
					return registry.ToolResult{}, fmt.Errorf("repo.search invalid include glob %q: %w", args.Include, err)
				}
			}

			// Resolve the path filter to a workspace-relative prefix (slash-
			// separated, like db.Symbol.FilePath). A path naming a file matches
			// that file exactly; a directory matches its whole subtree.
			// resolveReadToolPath rejects traversal and absolute paths, mirroring
			// the line-search path.
			pathPrefix := ""
			if args.Path != "" {
				resolved, err := t.resolveReadToolPath(args.Path)
				if err != nil {
					return registry.ToolResult{}, err
				}
				rel, err := workspaceRel(t.activeRoot(), resolved)
				if err != nil {
					// workspaceRel fails for two reasons: the active root cannot
					// be resolved at all (a genuine error worth surfacing), or the
					// resolved path lies outside it. The latter is benign and
					// expected: resolveReadToolPath accepts paths in any allowed
					// root (a linked worktree, a configured extra directory), so a
					// valid read path can sit outside the workspace root.
					if _, rootErr := filepath.EvalSymlinks(t.activeRoot()); rootErr != nil {
						return registry.ToolResult{}, err
					}
					// db.Symbol rows store workspace-relative paths (the scanner
					// and indexer both root at the active workspace), so no
					// indexed symbol can ever carry a path outside the root: the
					// filter can only match nothing. Report that honestly rather
					// than failing the call, which would be inconsistent with the
					// unfiltered kind query that succeeds against these roots.
					return registry.ToolResult{
						Summary: "No matching symbols (path outside the indexed workspace)",
						Content: fmt.Sprintf("path %q resolves outside the indexed workspace (e.g. a linked worktree or an additional root); the symbol index stores workspace-relative paths, so no indexed symbol can match it — retry without a path, or run the search from that root", args.Path),
					}, nil
				}
				// "." means the workspace root itself: no restriction.
				if rel != "." {
					pathPrefix = rel
				}
			}

			filtered := pathPrefix != "" || args.Include != ""

			// The DB applies its LIMIT before we can post-filter, so a filtered
			// query could come back short even though more matching symbols sit
			// beyond the cutoff. Deliberately over-fetch to the hard ceiling
			// while filtering, then trim to the caller's limit afterwards.
			fetchLimit := limit
			if filtered && hardSearchMaxResults > fetchLimit {
				fetchLimit = hardSearchMaxResults
			}

			symbols, err := t.db.FindSymbols(t.projectID, args.Query, args.Kind, fetchLimit)
			if err != nil {
				return registry.ToolResult{}, fmt.Errorf("find symbols: %w", err)
			}
			// Fewer rows than asked for means the DB had nothing more to give,
			// so post-filtering cannot have hidden anything.
			dbExhausted := len(symbols) < fetchLimit

			symbols = filterSymbols(symbols, pathPrefix, args.Include)
			// Capture the post-filter count before trimming. A trim drops rows
			// exactly as a truncated DB scan does, so completeness must account
			// for both: dbExhausted only proves the scan finished, not that the
			// returned result is whole.
			filteredCount := len(symbols)
			if filteredCount > limit {
				symbols = symbols[:limit]
			}

			if len(symbols) == 0 {
				// Keep the plain message byte-for-byte when no filter was given.
				if !filtered {
					return registry.ToolResult{
						Summary: "No matching symbols",
						Content: "No matching symbols — run repo.index first if the index may be missing",
					}, nil
				}
				// Say which filter applied, so a model can tell "no such symbol"
				// apart from "my filter excluded it".
				return registry.ToolResult{
					Summary: "No matching symbols (filtered)",
					Content: fmt.Sprintf("No matching symbols after applying %s — the symbol may exist outside the filtered scope, or run repo.index first if the index may be missing", describeSymbolFilters(pathPrefix, args.Include)),
				}, nil
			}

			rendered := t.renderSymbolSkeletons(symbols)
			summary := fmt.Sprintf("found %d symbols matching %s (kind: %s)", len(symbols), args.Query, args.Kind)
			content := rendered
			if filtered {
				summary += fmt.Sprintf(" (filtered by %s)", describeSymbolFilters(pathPrefix, args.Include))
				if !dbExhausted || filteredCount > limit {
					// Either the DB scan stopped at its LIMIT or the post-filter
					// trim dropped rows, so the result may be incomplete. Say so
					// instead of implying completeness.
					content += "\n(filter applied after the symbol lookup; more matching symbols may exist — narrow the query or the path/include filter)"
				}
			} else if !dbExhausted {
				// Unfiltered kind query: fetchLimit == limit and the DB returned
				// exactly that many rows, so its LIMIT may have cut the scan short.
				// Disclose the possible truncation instead of implying completeness
				// (the filtered branch above already discloses its own truncation).
				summary += " (capped)"
				content += fmt.Sprintf("\n(result capped at %d symbols; more matching symbols may exist — narrow the query or add a path/include filter)", limit)
			}
			return registry.ToolResult{
				Summary: summary,
				Content: limitOutput(content, t.maxOutputBytes),
			}, nil
		}

		var match lineMatcher
		actualMode := "substring"
		switch args.Mode {
		case "", "auto":
			// Auto: treat pattern-shaped queries as RE2 when they compile,
			// otherwise fall back to a literal substring match. A query that
			// looks like a regex but does not compile is a soft error: fall
			// back silently rather than failing the call.
			if re, err := regexp.Compile(args.Query); err == nil && looksLikeRegex(args.Query) {
				match = re.MatchString
				actualMode = "regex"
			} else {
				query := args.Query
				match = func(line string) bool { return strings.Contains(line, query) }
				actualMode = "substring"
			}
		case "substring":
			query := args.Query
			match = func(line string) bool { return strings.Contains(line, query) }
			actualMode = "substring"
		case "regex":
			re, err := regexp.Compile(args.Query)
			if err != nil {
				return registry.ToolResult{}, fmt.Errorf("repo.search invalid regex %q: %w", args.Query, err)
			}
			match = re.MatchString
			actualMode = "regex"
		default:
			return registry.ToolResult{}, fmt.Errorf("repo.search mode must be auto|substring|regex, got %q", args.Mode)
		}

		if args.Context < 0 || args.Context > 3 {
			return registry.ToolResult{}, fmt.Errorf("repo.search context must be between 0 and 3, got %d", args.Context)
		}
		if args.Include != "" {
			if _, err := path.Match(args.Include, ""); err != nil {
				return registry.ToolResult{}, fmt.Errorf("repo.search invalid include glob %q: %w", args.Include, err)
			}
		}

		start := t.activeRoot()
		if args.Path != "" {
			start, err = t.resolveReadToolPath(args.Path)
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
		// Echo the chosen mode only when the caller left it to auto; explicit
		// modes need no explanation.
		if args.Mode == "" || args.Mode == "auto" {
			summary += fmt.Sprintf(" (mode: %s)", actualMode)
		}

		var notice *registry.ToolNotice
		if len(matches) == 0 {
			// Zero matches: coach the caller toward the other mode when the
			// query shape suggests they picked the wrong one. A plain literal
			// zero-match gets no footer and no notice.
			footer := ""
			switch {
			case actualMode == "substring" && looksLikeRegex(args.Query):
				footer = `no matches; query looks like a regex — retry with mode:"regex"`
			case actualMode == "regex":
				footer = `no matches; regex matched nothing — check escaping or try mode:"substring"`
			}
			if footer != "" {
				if content == "" {
					content = footer
				} else {
					content += "\n" + footer
				}
				notice = &registry.ToolNotice{
					Kind: registry.NoticeZeroMatchCoach,
					Text: footer,
					Data: map[string]any{"query": args.Query, "mode": actualMode},
				}
			}
		} else if capped {
			footer := fmt.Sprintf("result capped at %d matches; narrow with path/include or a more specific query", limit)
			if content == "" {
				content = footer
			} else {
				content += "\n" + footer
			}
			notice = &registry.ToolNotice{
				Kind: registry.NoticeCappedResults,
				Text: footer,
				Data: map[string]any{"limit": limit},
			}
		}

		return registry.ToolResult{Summary: summary, Content: content, Notice: notice}, nil
	}
	return tool
}

// looksLikeRegex reports whether q contains any RE2 metacharacter. Auto mode
// uses it to guess whether the caller meant a pattern or a literal string.
func looksLikeRegex(q string) bool {
	return strings.ContainsAny(q, `[]{}()\\^$|*+?.`)
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

// filterSymbols keeps only the symbol rows whose workspace-relative FilePath
// lies inside pathPrefix (a directory subtree, or the exact file when path
// named a file) and matches the include glob. An empty value disables the
// corresponding filter, and a filter that matches nothing yields an empty
// slice rather than an error.
func filterSymbols(symbols []db.Symbol, pathPrefix, include string) []db.Symbol {
	if pathPrefix == "" && include == "" {
		return symbols
	}
	kept := make([]db.Symbol, 0, len(symbols))
	for _, s := range symbols {
		if pathPrefix != "" && s.FilePath != pathPrefix && !strings.HasPrefix(s.FilePath, pathPrefix+"/") {
			continue
		}
		if include != "" {
			// The glob is validated before this point; a bad pattern here
			// degrades to a skip, matching searchFile's call-site behavior.
			if ok, err := matchInclude(include, s.FilePath); err != nil || !ok {
				continue
			}
		}
		kept = append(kept, s)
	}
	return kept
}

// describeSymbolFilters renders the active kind-query filters for summaries
// and no-match messages, e.g. `path "internal/" and include "*.go"`.
func describeSymbolFilters(pathPrefix, include string) string {
	var parts []string
	if pathPrefix != "" {
		parts = append(parts, fmt.Sprintf("path %q", pathPrefix))
	}
	if include != "" {
		parts = append(parts, fmt.Sprintf("include %q", include))
	}
	return strings.Join(parts, " and ")
}

func (t *toolSet) searchFiles(ctx context.Context, start string, match lineMatcher, include string, ctxLines int, limit int) ([]string, bool, []error, error) {
	var walkErrs []error
	var matches []string
	matchCount := 0
	capped := false

	// Anchor gitignore rules at the workspace root when the search is scoped
	// to a subdirectory, so root-level patterns like "*.log" still apply.
	workspaceRoot := t.activeRoot()
	if resolved, err := filepath.EvalSymlinks(workspaceRoot); err == nil {
		workspaceRoot = resolved
	}
	gitignoreRoot := start
	if rel, err := filepath.Rel(workspaceRoot, start); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		gitignoreRoot = workspaceRoot
	}

	// Load the root .gitignore that anchors the stack for this walk.
	rootGitignore, rootGiErr := repo.LoadGitignore(filepath.Join(gitignoreRoot, ".gitignore"))
	if rootGiErr != nil {
		walkErrs = append(walkErrs, fmt.Errorf("gitignore %s: %w", filepath.Join(gitignoreRoot, ".gitignore"), rootGiErr))
	}
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

		// Path relative to the gitignore root for stack operations.
		giRel, giRelErr := filepath.Rel(gitignoreRoot, path)
		if giRelErr != nil {
			return nil
		}
		giRel = filepath.ToSlash(giRel)

		// Never search inside .gitignore files themselves, matching scanner
		// semantics. Skip the file before applying include/gitignore checks.
		if filepath.Base(rel) == ".gitignore" {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		// Check gitignore
		if stack != nil && stack.Match(giRel, entry.IsDir()) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if entry.IsDir() {
			if repo.IsDefaultIgnoredDir(entry.Name()) && path != start {
				return filepath.SkipDir
			}
			// Push per-directory .gitignore, using the gitignore-relative path.
			stack.PopTo(giRel)
			giPath := filepath.Join(gitignoreRoot, giRel, ".gitignore")
			if gi, giErr := repo.LoadGitignore(giPath); giErr != nil {
				walkErrs = append(walkErrs, fmt.Errorf("gitignore %s: %w", giPath, giErr))
			} else if gi.Patterns() > 0 {
				stack.Push(giRel, gi)
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
		// System access widens reads past the workspace root, so a file
		// outside it is searched rather than skipped. Display it by its
		// absolute path so matches remain attributable. Without the flag
		// this layered defense is unchanged.
		if !t.systemAccess() {
			return nil, 0
		}
		rel = filepath.ToSlash(resolvedPath)
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

// symbolBodyIndentMax bounds how many extra indentation columns still count
// as "one level deeper" than the header line. It matches the common 4-space
// Go/gofmt indent; tab-indented files are handled separately below.
const symbolBodyIndentMax = 4

// leadingWhitespace returns the run of leading space and tab characters in s.
func leadingWhitespace(s string) string {
	for i, r := range s {
		if r != ' ' && r != '\t' {
			return s[:i]
		}
	}
	return s
}

// readSymbolFileLines loads the workspace file for a symbol and returns its
// lines, or nil when the file is missing, unreadable, or over the pageable
// byte budget. It never fails the enclosing call: a missing body degrades to
// a header-only skeleton plus a file.read hint.
func (t *toolSet) readSymbolFileLines(rel string) []string {
	data, err := t.readWorkspaceFile(rel, int64(maxPageableFileBytes))
	if err != nil {
		return nil
	}
	return strings.Split(string(data), "\n")
}

// renderSymbolSkeletons formats symbol rows as skeleton headers: one line
// per symbol "path:start-end  signature", followed by the body's top-level
// declaration lines (for types/imports) when the symbol spans multiple
// lines, then a "+N more lines — file.read path:start-end" marker.
func (t *toolSet) renderSymbolSkeletons(symbols []db.Symbol) string {
	var b strings.Builder
	// Cache per-file line slices so a multi-symbol file is read once. A nil
	// entry records an unreadable/oversized file. ok distinguishes "not yet
	// loaded" from "loaded but nil".
	fileLines := make(map[string][]string)
	loaded := make(map[string]bool)

	for _, s := range symbols {
		fmt.Fprintf(&b, "%s:%d-%d  %s\n", s.FilePath, s.LineStart, s.LineEnd, s.Signature)

		// Lines belonging to the symbol, 1-based inclusive. Total body lines
		// beyond the header line is LineEnd-LineStart (0 for one-line symbols).
		bodyBeyondHeader := s.LineEnd - s.LineStart
		if bodyBeyondHeader <= 0 {
			continue
		}

		if !loaded[s.FilePath] {
			fileLines[s.FilePath] = t.readSymbolFileLines(s.FilePath)
			loaded[s.FilePath] = true
		}
		lines := fileLines[s.FilePath]

		shown := 0
		if lines != nil && s.LineStart >= 1 && s.LineEnd <= len(lines) {
			// Only type symbols get a body peek (a struct/interface shape
			// glimpse). function/method/import stay header-only, so shown
			// remains 0 for them.
			if s.Kind == "type" {
				headerLine := lines[s.LineStart-1]
				headerIndent := leadingWhitespace(headerLine)
				// Direct declaration lines: non-empty lines exactly one
				// indentation level deeper than the header. This is a
				// pragmatic struct-shape peek, not AST rendering; nested
				// members (deeper indent) and blank lines are skipped.
				for _, ln := range lines[s.LineStart:s.LineEnd] {
					trimmed := strings.TrimSpace(ln)
					if trimmed == "" {
						continue
					}
					indent := leadingWhitespace(ln)
					if !strings.HasPrefix(indent, headerIndent) {
						continue
					}
					extra := indent[len(headerIndent):]
					oneLevel := extra == "\t" || (len(extra) > 0 && len(extra) <= symbolBodyIndentMax && strings.Trim(extra, " ") == "")
					if !oneLevel {
						continue
					}
					fmt.Fprintf(&b, "  %s\n", trimmed)
					shown++
				}
			}
		}

		if unshown := bodyBeyondHeader - shown; unshown > 0 {
			fmt.Fprintf(&b, "  +%d more lines — file.read %s:%d-%d\n", unshown, s.FilePath, s.LineStart, s.LineEnd)
		}
	}
	return b.String()
}
