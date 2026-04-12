// Package linter runs configured lint tools against changed files and returns
// structured diagnostics that the engine feeds back to the executor.
package linter

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/alec/marshal/internal/config"
)

// Issue is a single lint diagnostic.
type Issue struct {
	File    string
	Line    int
	Col     int
	Message string
}

// String returns a compact human-readable representation.
func (i Issue) String() string {
	if i.Line > 0 && i.Col > 0 {
		return fmt.Sprintf("%s:%d:%d: %s", i.File, i.Line, i.Col, i.Message)
	}
	if i.Line > 0 {
		return fmt.Sprintf("%s:%d: %s", i.File, i.Line, i.Message)
	}
	return fmt.Sprintf("%s: %s", i.File, i.Message)
}

// Linter runs configured lint tools and returns diagnostics.
type Linter struct {
	cfg  config.LinterConfig
	root string
}

// New creates a Linter that runs linters from root.
func New(cfg config.LinterConfig, root string) *Linter {
	return &Linter{cfg: cfg, root: root}
}

// Run runs the appropriate linters for changedFiles (relative paths) and
// returns any issues found.  Binaries that are not installed are silently
// skipped so marshal works in environments that lack some tools.
func (l *Linter) Run(ctx context.Context, changedFiles []string) ([]Issue, error) {
	var all []Issue

	goFiles := filterByExts(changedFiles, ".go")
	pyFiles := filterByExts(changedFiles, ".py")
	jsFiles := filterByExts(changedFiles, ".js", ".mjs", ".cjs")
	tsFiles := filterByExts(changedFiles, ".ts", ".tsx")

	if len(goFiles) > 0 && l.cfg.Go != "" {
		// Go linters are always project-level; pass ./... automatically.
		if issues, err := l.run(ctx, l.cfg.Go, []string{"./..."}, parseColonDiagnostics); err == nil {
			all = append(all, l.relativise(issues)...)
		}
	}

	if len(pyFiles) > 0 && l.cfg.Python != "" {
		if issues, err := l.run(ctx, l.cfg.Python, pyFiles, parseColonDiagnostics); err == nil {
			all = append(all, issues...)
		}
	}

	if len(jsFiles) > 0 && l.cfg.JS != "" {
		if issues, err := l.run(ctx, l.cfg.JS, jsFiles, parseESLintOutput); err == nil {
			all = append(all, issues...)
		}
	}

	if len(tsFiles) > 0 && l.cfg.TS != "" {
		if issues, err := l.run(ctx, l.cfg.TS, tsFiles, parseESLintOutput); err == nil {
			all = append(all, issues...)
		}
	}

	return all, nil
}

// Format returns a compact multi-line string suitable for the executor prompt.
func Format(issues []Issue) string {
	if len(issues) == 0 {
		return ""
	}
	var sb strings.Builder
	for _, i := range issues {
		sb.WriteString(i.String())
		sb.WriteByte('\n')
	}
	return strings.TrimRight(sb.String(), "\n")
}

// --- internal helpers --------------------------------------------------------

func (l *Linter) run(ctx context.Context, cmdStr string, fileArgs []string, parser func(string) []Issue) ([]Issue, error) {
	parts := strings.Fields(cmdStr)
	if len(parts) == 0 {
		return nil, nil
	}

	args := append(parts[1:], fileArgs...)
	cmd := exec.CommandContext(ctx, parts[0], args...)
	cmd.Dir = l.root
	out, err := cmd.CombinedOutput()

	// Binary not installed — skip silently.
	if errors.Is(err, exec.ErrNotFound) {
		return nil, nil
	}
	// Non-zero exit is the normal signal that issues were found; not an error.

	return parser(string(out)), nil
}

// relativise strips l.root from absolute file paths so they are repo-relative.
func (l *Linter) relativise(issues []Issue) []Issue {
	for i, is := range issues {
		if filepath.IsAbs(is.File) {
			if rel, err := filepath.Rel(l.root, is.File); err == nil {
				issues[i].File = rel
			}
		}
	}
	return issues
}

// --- output parsers ----------------------------------------------------------

// colonRe matches lines like:
//
//	path/to/file.go:42:5: message text (linter-name)
//	./path/to/file.go:42: message text
var colonRe = regexp.MustCompile(`^([^:\s][^:]*):(\d+):(\d+):\s+(.+)$`)
var colonLineRe = regexp.MustCompile(`^([^:\s][^:]*):(\d+):\s+(.+)$`)

// parseColonDiagnostics handles the file:line:col: message format used by
// golangci-lint, go vet, go build, and flake8.
func parseColonDiagnostics(output string) []Issue {
	var issues []Issue
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := scanner.Text()
		if m := colonRe.FindStringSubmatch(line); m != nil {
			lineNum, _ := strconv.Atoi(m[2])
			col, _ := strconv.Atoi(m[3])
			issues = append(issues, Issue{
				File:    m[1],
				Line:    lineNum,
				Col:     col,
				Message: m[4],
			})
			continue
		}
		if m := colonLineRe.FindStringSubmatch(line); m != nil {
			lineNum, _ := strconv.Atoi(m[2])
			issues = append(issues, Issue{
				File:    m[1],
				Line:    lineNum,
				Message: m[3],
			})
		}
	}
	return issues
}

// parseESLintOutput handles ESLint's default multi-line format:
//
//	/path/to/file.js
//	  42:5  error  message  rule-name
//
// and the unix/compact format:
//
//	/path/to/file.js:42:5: Error - message (rule)
func parseESLintOutput(output string) []Issue {
	// First try colon format (unix/compact output).
	issues := parseColonDiagnostics(output)
	if len(issues) > 0 {
		return issues
	}

	// Fall back to multi-line format.
	var currentFile string
	lineDetailRe := regexp.MustCompile(`^\s+(\d+):(\d+)\s+(error|warning)\s+(.+?)\s+\S+$`)

	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "/") || (len(line) > 1 && line[1] == ':') {
			// Looks like an absolute path line.
			currentFile = strings.TrimSpace(line)
			continue
		}
		if m := lineDetailRe.FindStringSubmatch(line); m != nil && currentFile != "" {
			lineNum, _ := strconv.Atoi(m[1])
			col, _ := strconv.Atoi(m[2])
			issues = append(issues, Issue{
				File:    currentFile,
				Line:    lineNum,
				Col:     col,
				Message: m[4],
			})
		}
	}
	return issues
}

// filterByExts returns the elements of paths whose extension is in exts.
func filterByExts(paths []string, exts ...string) []string {
	extSet := make(map[string]bool, len(exts))
	for _, e := range exts {
		extSet[e] = true
	}
	var out []string
	for _, p := range paths {
		if extSet[filepath.Ext(p)] {
			out = append(out, p)
		}
	}
	return out
}
