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
	fmt.Fprintf(&b, "Estimated tokens: %d/%d\n", pack.TokenUsage.EstimatedTokens, pack.TokenUsage.MaxTokens)
	if pack.TokenUsage.Truncated {
		fmt.Fprintf(&b, "Truncated: true\n")
	}

	for _, section := range pack.Sections {
		fmt.Fprintf(&b, "\n## %s\n", section.Title)
		fmt.Fprintf(&b, "Kind: %s\n", section.Kind)
		if section.Source != "" {
			fmt.Fprintf(&b, "Source: %s\n", section.Source)
		}
		fmt.Fprintf(&b, "Estimated tokens: %d\n\n", section.EstimatedTokens)
		fmt.Fprintf(&b, "%s\n", section.Content)
	}

	return strings.TrimRight(b.String(), "\n")
}
