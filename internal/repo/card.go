package repo

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"marshal/internal/db"
)

// RenderRepoCard renders a short project summary: name, total files, language
// distribution, and top-level directories.
func RenderRepoCard(projectName string, files []db.FileIndex) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Project: %s\n", projectName)
	fmt.Fprintf(&b, "Files: %d\n", len(files))

	langCounts := map[string]int{}
	rootDirs := map[string]bool{}
	for _, f := range files {
		if f.Language != "" {
			langCounts[f.Language]++
		}
		parts := strings.Split(filepath.ToSlash(f.Path), "/")
		if len(parts) > 1 {
			rootDirs[parts[0]] = true
		}
	}

	if len(langCounts) > 0 {
		b.WriteString("\nLanguages:\n")
		langs := make([]string, 0, len(langCounts))
		for lang := range langCounts {
			langs = append(langs, lang)
		}
		sort.Strings(langs)
		for _, lang := range langs {
			fmt.Fprintf(&b, "  %s: %d\n", lang, langCounts[lang])
		}
	}

	if len(rootDirs) > 0 {
		b.WriteString("\nTop-level directories:\n")
		dirs := make([]string, 0, len(rootDirs))
		for d := range rootDirs {
			dirs = append(dirs, d)
		}
		sort.Strings(dirs)
		for _, d := range dirs {
			fmt.Fprintf(&b, "  %s/\n", d)
		}
	}

	return b.String()
}
