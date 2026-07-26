package agent

import (
	"fmt"
	"strings"
	"unicode"

	"marshal/internal/tools/registry"
)

// sanitizeToolName converts a canonical, dot-separated native/MCP tool name
// (e.g. "file.read", "mcp.github.create_issue") into a name that satisfies
// the common OpenAI-compatible function-name constraint enforced by some
// providers (Kimi's kimi-for-coding, confirmed live): must start with a
// letter and contain only letters, digits, underscores, and dashes. Ollama
// Cloud tolerates dots; Kimi rejects the entire request if any tool name in
// the schema violates this, so the alias applies unconditionally rather
// than only when a strict provider is detected.
func sanitizeToolName(name string) string {
	var b strings.Builder
	for _, r := range name {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	out := b.String()
	if out == "" || !unicode.IsLetter(rune(out[0])) {
		out = "t_" + out
	}
	return out
}

// toolAliasMap builds a deterministic alias -> canonical name mapping for
// every tool in tools. Pass an already-sorted slice (registry.Registry.List
// sorts by name) so the mapping is stable across calls. A collision between
// two distinct canonical names sanitizing to the same alias is disambiguated
// with a numeric suffix on the later one in iteration order, so a name never
// silently becomes unreachable — this should not happen for any currently
// registered native tool name (see TestToolAliasMapIsInjectiveForCurrentNativeToolSet).
func toolAliasMap(tools []registry.Tool) map[string]string {
	aliasToCanonical := make(map[string]string, len(tools))
	used := make(map[string]bool, len(tools))
	for _, tool := range tools {
		alias := sanitizeToolName(tool.Name)
		candidate := alias
		for n := 2; used[candidate]; n++ {
			candidate = fmt.Sprintf("%s_%d", alias, n)
		}
		used[candidate] = true
		aliasToCanonical[candidate] = tool.Name
	}
	return aliasToCanonical
}

// toolNameToAlias inverts toolAliasMap into canonical name -> alias, for use
// when building outgoing tool schemas.
func toolNameToAlias(tools []registry.Tool) map[string]string {
	nameToAlias := make(map[string]string, len(tools))
	for alias, name := range toolAliasMap(tools) {
		nameToAlias[name] = alias
	}
	return nameToAlias
}
