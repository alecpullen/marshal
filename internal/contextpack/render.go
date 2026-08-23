package contextpack

import (
	"fmt"
	"strings"
)

func Render(pack Pack) string {
	if pack.IsEmpty() {
		return ""
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Project context pack:\n")
	// The pack-level token estimate is operational metadata; it only earns
	// prompt space when it signals that sections were cut.
	if pack.TokenUsage.Truncated {
		fmt.Fprintf(&b, "Estimated tokens: %d/%d\n", pack.TokenUsage.EstimatedTokens, pack.TokenUsage.MaxTokens)
		fmt.Fprintf(&b, "Truncated: true\n")
	}

	for _, section := range pack.Sections {
		fmt.Fprintf(&b, "\n## %s\n", section.Title)
		if section.Source != "" {
			fmt.Fprintf(&b, "Source: %s\n", section.Source)
		}
		fmt.Fprintf(&b, "\n%s\n", section.Content)
	}

	return strings.TrimRight(b.String(), "\n")
}
