package git

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"

	"github.com/gobwas/glob"
)

// Ignorer applies .marshalignore patterns to filter the editable file set.
// It mirrors .gitignore semantics: blank lines and # comments are skipped;
// each remaining line is treated as a gobwas/glob pattern with '/' as the
// path separator.
type Ignorer struct {
	patterns []glob.Glob
}

// LoadMarshalIgnore reads .marshalignore from repoRoot. If the file does not
// exist an empty (pass-through) Ignorer is returned without error.
func LoadMarshalIgnore(repoRoot string) (*Ignorer, error) {
	path := filepath.Join(repoRoot, ".marshalignore")
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return &Ignorer{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	ig := &Ignorer{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		g, err := glob.Compile(line, '/')
		if err != nil {
			// Skip invalid patterns rather than aborting.
			continue
		}
		ig.patterns = append(ig.patterns, g)
	}
	return ig, scanner.Err()
}

// Match reports whether relPath (relative to the repo root, using forward
// slashes) matches any .marshalignore pattern.
func (ig *Ignorer) Match(relPath string) bool {
	for _, g := range ig.patterns {
		if g.Match(relPath) {
			return true
		}
	}
	return false
}

// Filter returns only the paths from files that do NOT match any pattern.
func (ig *Ignorer) Filter(files []string) []string {
	if len(ig.patterns) == 0 {
		return files
	}
	out := make([]string, 0, len(files))
	for _, f := range files {
		if !ig.Match(f) {
			out = append(out, f)
		}
	}
	return out
}
