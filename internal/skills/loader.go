package skills

import (
	"os"
	"path/filepath"
	"strings"
)

// The only scopes discovery reports and the only scopes a removal may target.
const (
	scopeGlobal  = "global"
	scopeProject = "project"
)

// ScopedSkill pairs a skill with the scope it was loaded from.
type ScopedSkill struct {
	Skill      Skill
	Scope      string      // "global" or "project"
	SourceName string      // direct child of the corresponding store
	SourceInfo os.FileInfo // Lstat snapshot of that child at discovery
}

// removalSourceName returns the direct child of the store that a discovered
// skill path lives under: the file itself for a top-level .md skill, or the
// containing directory for a bundle's SKILL.md.
func removalSourceName(path string) string {
	if filepath.Base(path) == BundleFileName {
		return filepath.Base(filepath.Dir(path))
	}
	return filepath.Base(path)
}

// scanSkillDir walks a directory and returns the paths of all skill files:
// top-level .md files and SKILL.md files inside subdirectories. Returns an
// empty slice (not an error) if the directory does not exist.
func scanSkillDir(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var paths []string
	for _, entry := range entries {
		if entry.IsDir() {
			bundlePath := filepath.Join(dir, entry.Name(), BundleFileName)
			if _, err := os.Stat(bundlePath); err != nil {
				continue
			}
			paths = append(paths, bundlePath)
			continue
		}
		if !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		paths = append(paths, filepath.Join(dir, entry.Name()))
	}
	return paths, nil
}

// ListScopes returns skills from both scopes without merging; the caller can
// decide how to handle name collisions. It is the two-store form of the
// resolver's own scan, so a listing and the resolution agree on what a store
// holds and on what is skipped — including the warning for a copy that could
// not be stat'ed.
func ListScopes(globalDir, projectDir string) ([]ScopedSkill, error) {
	var out []ScopedSkill
	for _, pair := range []struct{ dir, scope string }{
		{globalDir, scopeGlobal},
		{projectDir, scopeProject},
	} {
		candidates, err := scanManagedStore(pair.dir, pair.scope)
		if err != nil {
			return nil, err
		}
		for _, candidate := range candidates {
			out = append(out, ScopedSkill{
				Skill:      candidate.Skill,
				Scope:      candidate.Source,
				SourceName: candidate.SourceName,
				SourceInfo: candidate.SourceInfo,
			})
		}
	}
	return out, nil
}

func parseSkillFile(path string) (Skill, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Skill{}, err
	}
	return Parse(string(raw))
}

// LoadSkills resolves the built-in, Marshal global and project stores. It is
// the two-store form of Resolve, kept for callers that know nothing about the
// shared root; the winner per name is the same as Resolve's default order.
func LoadSkills(globalDir, projectDir string) (*Index, error) {
	resolved, err := Resolve(Stores{GlobalDir: globalDir, ProjectDir: projectDir}, nil)
	if err != nil {
		return nil, err
	}
	return resolved.Index, nil
}
