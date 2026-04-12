package repomap

import (
	"os"
	"path/filepath"
	"strings"
)

// supportedExts maps file extensions to language names.
var supportedExts = map[string]string{
	".go":  "go",
	".py":  "python",
	".js":  "javascript",
	".mjs": "javascript",
	".cjs": "javascript",
	".ts":  "typescript",
	".tsx": "typescript",
}

// skipDirs are directory names that are always skipped when walking.
var skipDirs = map[string]bool{
	".git":         true,
	".marshal":     true,
	"node_modules": true,
	"vendor":       true,
	".venv":        true,
	"venv":         true,
	"__pycache__":  true,
	"dist":         true,
	"build":        true,
	".next":        true,
	".nuxt":        true,
}

// walkFiles returns relative paths (forward slashes) of all supported source
// files under root, respecting ig and skipDirs.  Files are returned in
// lexicographic order.
func walkFiles(root string, ig Ignorer) ([]string, error) {
	var files []string

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries
		}

		name := d.Name()

		if d.IsDir() {
			if skipDirs[name] || strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}

		ext := strings.ToLower(filepath.Ext(name))
		if supportedExts[ext] == "" {
			return nil
		}

		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)

		if ig != nil && ig.Match(rel) {
			return nil
		}

		files = append(files, rel)
		return nil
	})

	return files, err
}

// langFor returns the language name for a relative file path, or "" if
// unsupported.
func langFor(rel string) string {
	ext := strings.ToLower(filepath.Ext(rel))
	return supportedExts[ext]
}
