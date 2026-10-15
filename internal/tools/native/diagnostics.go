package native

import (
	"marshal/internal/diagnostics"
	"marshal/internal/repo"
)

// newDiagnosticsChecker creates a Checker from the configured per-language
// commands.
func newDiagnosticsChecker(opts Options) *diagnostics.Checker {
	return diagnostics.NewChecker(opts.Config.Diagnostics.Commands)
}

// languageOf returns the first detectable language among paths.
func languageOf(paths []string) string {
	for _, p := range paths {
		if lang := repo.DetectLanguage(p); lang != "" {
			return lang
		}
	}
	return ""
}
