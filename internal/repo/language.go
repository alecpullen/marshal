package repo

import (
	"path/filepath"
	"strings"
)

// DetectLanguage returns a language identifier for the given path based on
// its basename or extension. It returns an empty string when no mapping exists.
func DetectLanguage(path string) string {
	base := filepath.Base(path)
	if lang, ok := specialLanguages[strings.ToLower(base)]; ok {
		return lang
	}
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(path), "."))
	if lang, ok := extensionLanguages[ext]; ok {
		return lang
	}
	return ""
}

var specialLanguages = map[string]string{
	"dockerfile": "dockerfile",
	"makefile":   "makefile",
	"go.mod":     "go-module",
	"go.sum":     "go-sum",
}

var extensionLanguages = map[string]string{
	"go": "go", "js": "javascript", "mjs": "javascript", "cjs": "javascript",
	"ts":  "typescript",
	"jsx": "javascript", "tsx": "typescript", "py": "python",
	"rs": "rust", "java": "java", "kt": "kotlin",
	"cpp": "cpp", "c": "c", "h": "c", "hpp": "cpp",
	"rb": "ruby", "php": "php", "sh": "shell",
	"md": "markdown", "json": "json", "yaml": "yaml",
	"yml": "yaml", "toml": "toml", "html": "html",
	"css": "css", "scss": "scss", "sql": "sql",
	"cs": "csharp", "fs": "fsharp", "vb": "visualbasic",
	"vue": "vue", "svelte": "svelte", "astro": "astro",
	"scala": "scala", "sbt": "scala",
	"clj": "clojure", "cljs": "clojurescript", "cljc": "clojure",
	"ex": "elixir", "exs": "elixir",
	"erl": "erlang", "hrl": "erlang",
	"hs": "haskell", "lhs": "haskell",
	"swift": "swift", "m": "objc", "mm": "objcpp",
	"dart": "dart", "lua": "lua", "r": "r", "jl": "julia",
	"nim": "nim", "zig": "zig", "v": "v",
	"pl": "perl", "pm": "perl", "t": "perl",
	"groovy": "groovy", "gradle": "groovy",
	"asm": "assembly", "s": "assembly",
	"proto": "protobuf",
}
