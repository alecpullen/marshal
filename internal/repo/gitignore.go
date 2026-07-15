package repo

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Gitignore struct {
	patterns []gitignorePattern
}

type gitignorePattern struct {
	anchored bool
	dirOnly  bool
	segments []string
}

func ParseGitignore(content string) (*Gitignore, error) {
	var patterns []gitignorePattern
	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		p, err := parseGitignorePattern(line)
		if err != nil {
			return nil, err
		}
		patterns = append(patterns, p)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return &Gitignore{patterns: patterns}, nil
}

func LoadGitignore(path string) (*Gitignore, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Gitignore{}, nil
		}
		return nil, err
	}
	return ParseGitignore(string(data))
}

func parseGitignorePattern(line string) (gitignorePattern, error) {
	p := gitignorePattern{}
	if strings.HasPrefix(line, "/") {
		p.anchored = true
		line = line[1:]
	}
	// Record whether the pattern is dir-only before removing
	// the trailing slash, so the slash-anchor check below
	// can see the full pattern.
	dirOnly := strings.HasSuffix(line, "/")
	// A slash anywhere in the pattern anchors it relative
	// to the .gitignore location. Check before stripping
	// the trailing / so that build/ is correctly anchored.
	if strings.Contains(line, "/") {
		p.anchored = true
	}
	if dirOnly {
		line = strings.TrimSuffix(line, "/")
	}
	p.dirOnly = dirOnly
	p.segments = strings.Split(line, "/")
	for _, seg := range p.segments {
		if _, err := filepath.Match(seg, ""); err != nil {
			return gitignorePattern{}, fmt.Errorf("invalid gitignore pattern %q: %w", line, err)
		}
	}
	return p, nil
}

func (g *Gitignore) Match(path string, isDir bool) bool {
	path = filepath.ToSlash(path)
	for _, p := range g.patterns {
		if p.dirOnly {
			pathSegments := strings.Split(path, "/")
			end := len(pathSegments)
			if !isDir {
				end = len(pathSegments) - 1
			}
			for i := 1; i <= end; i++ {
				prefix := strings.Join(pathSegments[:i], "/")
				if matchPattern(p, prefix) {
					return true
				}
			}
			continue
		}
		if matchPattern(p, path) {
			return true
		}
	}
	return false
}

func matchPattern(p gitignorePattern, path string) bool {
	pathSegments := strings.Split(path, "/")
	starts := []int{0}
	if !p.anchored {
		for i := range pathSegments {
			starts = append(starts, i)
		}
	}
	for _, start := range starts {
		if start+len(p.segments) > len(pathSegments) {
			continue
		}
		match := true
		for i, seg := range p.segments {
			if matched, _ := filepath.Match(seg, pathSegments[start+i]); !matched {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
