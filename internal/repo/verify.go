package repo

import (
	"os"
	"path/filepath"
)

// Manifest is a well-known project manifest detected in a repo.
type Manifest struct {
	Name     string
	Language string
}

// manifestOrder lists well-known manifests in detection-priority order.
var manifestOrder = []Manifest{
	{"go.mod", "go"},
	{"go.work", "go"},
	{"Cargo.toml", "rust"},
	{"pom.xml", "java"},
	{"pyproject.toml", "python"},
	{"package.json", "node"},
}

// DetectManifest returns the first well-known manifest found in repoRoot's
// top level, in priority order.
func DetectManifest(repoRoot string) (Manifest, bool) {
	for _, m := range manifestOrder {
		if _, err := os.Stat(filepath.Join(repoRoot, m.Name)); err == nil {
			return m, true
		}
	}
	return Manifest{}, false
}

// sourceLanguages is the set of languages considered source (as opposed to
// markdown, json, yaml, etc.) when computing the dominant language.
var sourceLanguages = map[string]bool{
	"go": true, "javascript": true, "typescript": true, "python": true,
	"rust": true, "java": true, "kotlin": true, "c": true, "cpp": true,
	"csharp": true, "ruby": true, "php": true, "swift": true, "dart": true,
}

// DominantSourceLanguage walks repoRoot (skipping common vendor dirs) and
// returns the most common source language by extension, or "" when none.
func DominantSourceLanguage(repoRoot string) string {
	counts := map[string]int{}
	filepath.WalkDir(repoRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor", ".marshal", "dist", "build":
				return filepath.SkipDir
			}
			return nil
		}
		if lang := DetectLanguage(path); sourceLanguages[lang] {
			counts[lang]++
		}
		return nil
	})
	best, n := "", 0
	for lang, c := range counts {
		if c > n {
			best, n = lang, c
		}
	}
	return best
}
