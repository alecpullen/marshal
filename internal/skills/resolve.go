package skills

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// The four places a skill copy can come from. Discovery keeps the source with
// every copy: two stores holding the same file or directory name are different
// skills, and the declared name in the frontmatter is content, not identity.
const (
	SourceBuiltin = "builtin"
	SourceGlobal  = "global"
	SourceProject = "project"
	SourceShared  = "shared"
)

// Stores names the directories skills are discovered in. Built-ins always come
// from the binary. A missing directory is an empty source — including the
// shared root, which other agents install into and remove from at will.
type Stores struct {
	GlobalDir  string // <user config dir>/skills
	ProjectDir string // <repo>/.marshal/skills
	SharedDir  string // <home>/.agents/skills
}

// Candidate is one discovered copy of a skill.
type Candidate struct {
	Skill Skill
	// Source is one of the Source* constants.
	Source string
	// SourceName is the copy's name inside that source: the file name for a
	// flat skill, the child directory's name for a bundle.
	SourceName string
	// SourceInfo is the lstat snapshot of the store entry at discovery. Only
	// the managed stores (global and project) carry one — removal verifies it,
	// and the shared store is read-only through Marshal.
	SourceInfo os.FileInfo
}

// Resolution is every copy of one declared skill name, highest precedence
// first, plus the copy that won and whether the user preferred the shared one.
type Resolution struct {
	Name         string
	Candidates   []Candidate
	Winner       Candidate
	PreferShared bool
}

// Resolved is the outcome of resolving the stores: the index the runtime runs
// on, and the candidates behind every name in it.
type Resolved struct {
	Index *Index
	// ByName is keyed by the declared skill name, never by source name.
	ByName map[string]Resolution
	// Names lists the declared names in sorted order.
	Names []string
}

// ListSources returns every discovered copy across the four sources, one entry
// per copy: unmerged, in the default precedence order of its source.
func ListSources(stores Stores) ([]Candidate, error) {
	builtins, err := builtInCandidates()
	if err != nil {
		return nil, err
	}

	var out []Candidate
	for _, store := range []struct {
		dir, source string
	}{
		{stores.ProjectDir, SourceProject},
		{stores.GlobalDir, SourceGlobal},
	} {
		candidates, err := scanManagedStore(store.dir, store.source)
		if err != nil {
			return nil, fmt.Errorf("scan %s skills from %s: %w", store.source, store.dir, err)
		}
		out = append(out, candidates...)
	}
	out = append(out, builtins...)
	out = append(out, scanSharedStore(stores.SharedDir)...)
	return out, nil
}

// Resolve discovers every store and decides, per declared skill name, which
// copy the index holds.
//
// The default order is project, Marshal global, built-in, shared: an explicit
// install wins, and another tool's shared copy never silently replaces a
// built-in. preferShared names the declared skills whose built-in should give
// way to the shared copy; it swaps those two and nothing else, so a project or
// global copy keeps its priority over both.
func Resolve(stores Stores, preferShared []string) (*Resolved, error) {
	all, err := ListSources(stores)
	if err != nil {
		return nil, err
	}

	preferred := make(map[string]bool, len(preferShared))
	for _, name := range preferShared {
		if name = strings.TrimSpace(name); name != "" {
			preferred[name] = true
		}
	}

	grouped := make(map[string][]Candidate, len(all))
	for _, candidate := range all {
		name := candidate.Skill.Name
		grouped[name] = append(grouped[name], candidate)
	}

	resolved := &Resolved{
		Index:  NewIndex(),
		ByName: make(map[string]Resolution, len(grouped)),
		Names:  make([]string, 0, len(grouped)),
	}
	for name, candidates := range grouped {
		prefer := preferred[name]
		// Stable: copies of one name within one source keep the order the
		// store listed them in, so the same tree always resolves the same way.
		sort.SliceStable(candidates, func(i, j int) bool {
			return sourceRank(candidates[i].Source, prefer) < sourceRank(candidates[j].Source, prefer)
		})
		winner := candidates[0]
		resolved.ByName[name] = Resolution{
			Name:         name,
			Candidates:   candidates,
			Winner:       winner,
			PreferShared: prefer,
		}
		resolved.Index.Set(name, winner.Skill)
		resolved.Names = append(resolved.Names, name)
	}
	sort.Strings(resolved.Names)
	return resolved, nil
}

// sourceRank orders the sources for one name, lowest precedence first. Only
// built-in and shared swap: the preference is a choice between Marshal's own
// copy and another tool's, never over the project's or the user's install.
func sourceRank(source string, preferShared bool) int {
	switch source {
	case SourceProject:
		return 0
	case SourceGlobal:
		return 1
	case SourceBuiltin:
		if preferShared {
			return 3
		}
		return 2
	case SourceShared:
		if preferShared {
			return 2
		}
		return 3
	default:
		return 4
	}
}

// scanManagedStore discovers a store Marshal can write to, so every copy
// carries the lstat snapshot a removal checks.
func scanManagedStore(dir, source string) ([]Candidate, error) {
	paths, err := scanSkillDir(dir)
	if err != nil {
		return nil, err
	}
	var out []Candidate
	for _, path := range paths {
		if candidate, ok := candidateFromPath(dir, path, source, true); ok {
			out = append(out, candidate)
		}
	}
	return out, nil
}

// scanSharedStore discovers the shared root. Other agents install bundles by
// linking them in, and a ReadDir entry for a symlink reports itself as a
// symlink rather than a directory, so this scan (unlike the managed stores)
// looks through each non-.md child for a SKILL.md. A missing or unreadable
// root, and a dangling link inside it, are empty rather than fatal: the shared
// root belongs to another tool, and Marshal has to keep working without it.
func scanSharedStore(dir string) []Candidate {
	if dir == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Default().Warn("skipping shared skills", "dir", dir, "error", err)
		}
		return nil
	}

	var out []Candidate
	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name())
		if !strings.HasSuffix(entry.Name(), ".md") {
			bundlePath := filepath.Join(path, BundleFileName)
			if _, err := os.Stat(bundlePath); err != nil {
				if _, dirErr := os.Stat(path); dirErr != nil {
					slog.Default().Warn("skipping unreadable shared skill entry", "path", path, "error", dirErr)
				}
				continue
			}
			path = bundlePath
		}
		if candidate, ok := candidateFromPath(dir, path, SourceShared, false); ok {
			out = append(out, candidate)
		}
	}
	return out
}

// candidateFromPath parses one discovered skill file. A file that is
// unreadable or invalid is skipped rather than failing the source: a broken
// shared copy must leave the built-in of the same name winning, and the other
// skills available.
func candidateFromPath(dir, path, source string, managed bool) (Candidate, bool) {
	skill, err := parseSkillFile(path)
	if err != nil {
		slog.Default().Warn("skipping invalid skill file", "path", path, "error", err)
		return Candidate{}, false
	}

	// Provenance comes from the filesystem, never from Skill.Name. An entry
	// that vanished between scan and stat is skipped the same way an
	// unreadable entry is: no identity is fabricated.
	candidate := Candidate{
		Skill:      skill,
		Source:     source,
		SourceName: removalSourceName(path),
	}
	if !managed {
		return candidate, true
	}
	info, err := os.Lstat(filepath.Join(dir, candidate.SourceName))
	if err != nil {
		return Candidate{}, false
	}
	candidate.SourceInfo = info
	return candidate, true
}

// builtInCandidates parses the embedded skills, in name order so discovery
// order is stable.
func builtInCandidates() ([]Candidate, error) {
	sources := builtInSources()
	names := make([]string, 0, len(sources))
	for name := range sources {
		names = append(names, name)
	}
	sort.Strings(names)

	out := make([]Candidate, 0, len(names))
	for _, name := range names {
		skill, err := Parse(string(sources[name]))
		if err != nil {
			return nil, fmt.Errorf("parse built-in skill %q: %w", name, err)
		}
		out = append(out, Candidate{Skill: skill, Source: SourceBuiltin, SourceName: name})
	}
	return out, nil
}
