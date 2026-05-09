package knowledge

import (
	"fmt"
	"strings"
)

// AutoDetect tries to infer scope from question.
func AutoDetect(question string) Scope {
	lower := strings.ToLower(question)
	
	backendTerms := []string{"api", "backend", "server", "database", "db", "sql", "postgres", "endpoint", "handler", "middleware"}
	frontendTerms := []string{"ui", "frontend", "react", "component", "css", "html", "javascript", "typescript", "dom", "browser"}
	docsTerms := []string{"readme", "doc", "documentation", "guide", "tutorial", "example", "howto"}
	testsTerms := []string{"test", "spec", "integration", "unit test", "e2e", "benchmark"}
	
	if containsAny(lower, backendTerms) {
		return ScopeBackend
	}
	if containsAny(lower, frontendTerms) {
		return ScopeFrontend
	}
	if containsAny(lower, docsTerms) {
		return ScopeDocs
	}
	if containsAny(lower, testsTerms) {
		return ScopeTests
	}
	
	return ScopeAll
}

// Validate checks if scope is valid.
func ValidateScope(s string) error {
	scope := Scope(s)
	if !scope.IsValid() {
		return fmt.Errorf("invalid scope: %s (valid: backend, frontend, docs, tests, all, auto)", s)
	}
	return nil
}

// ScopeFromString converts string to Scope with auto-detection support.
func ScopeFromString(s string, question string) Scope {
	if s == "" || s == "auto" {
		return AutoDetect(question)
	}
	return Scope(s)
}

func containsAny(s string, substrs []string) bool {
	for _, sub := range substrs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
