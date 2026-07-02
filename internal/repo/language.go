package repo

import (
	"path/filepath"
	"strings"
)

func DetectLanguage(path string) string {
	base := filepath.Base(path)
	if lang, ok := specialLanguages[base]; ok {
		return lang
	}
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(path), "."))
	if lang, ok := extensionLanguages[ext]; ok {
		return lang
	}
	return ""
}

var specialLanguages = map[string]string{
	"Dockerfile": "dockerfile",
	"Makefile":   "makefile",
	"go.mod":     "go-module",
	"go.sum":     "go-sum",
}

var extensionLanguages = map[string]string{
	"go": "go", "js": "javascript", "ts": "typescript",
	"jsx": "javascript", "tsx": "typescript", "py": "python",
	"rs": "rust", "java": "java", "kt": "kotlin",
	"cpp": "cpp", "c": "c", "h": "c", "hpp": "cpp",
	"rb": "ruby", "php": "php", "sh": "shell",
	"md": "markdown", "json": "json", "yaml": "yaml",
	"yml": "yaml", "toml": "toml", "html": "html",
	"css": "css", "scss": "scss", "sql": "sql",
}
