package prompts

import "strings"

// Assemble combines a base system prompt with optional security and skill layers.
// Layer order: base → security → extra (skill additions).
// Empty layers are skipped. All non-empty layers are joined with blank-line separators.
func Assemble(base, extra string) string {
	return AssembleWithSecurity(base, Security, extra)
}

// AssembleWithSecurity combines layers with explicit security content.
// This allows tests to inject custom security text or skip it with empty string.
func AssembleWithSecurity(base, security, extra string) string {
	var parts []string

	base = strings.TrimSpace(base)
	if base != "" {
		parts = append(parts, base)
	}

	security = strings.TrimSpace(security)
	if security != "" {
		parts = append(parts, security)
	}

	extra = strings.TrimSpace(extra)
	if extra != "" {
		parts = append(parts, extra)
	}

	return strings.Join(parts, "\n\n")
}
