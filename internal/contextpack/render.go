package contextpack

import (
	"fmt"
	"strings"
)

// Render renders the whole pack in one block. Production code uses
// RenderStable and RenderVolatile so the volatile sections can sit at the
// end of the wire; Render is kept as the whole-pack view for builder tests.
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

// IsVolatile reports whether sections of kind k are rewritten within a
// turn: todo.write after every step and mid-turn semantic re-queries.
// Volatile sections render into the per-request working-state message at
// the end of the wire, not the context pack near its start: anything that
// changes near the start invalidates the provider prompt cache for
// everything after it. See docs/prompt-caching/design.md (D1).
func IsVolatile(k SectionKind) bool {
	switch k {
	case SectionTodos, SectionSemantic, SectionToolOutput:
		return true
	}
	return false
}

// RenderStable renders the sections that hold still within a turn, under
// the "Project context pack:" header. Empty when there are none.
//
// Stable sections have lower priority numbers than volatile ones, so the
// greedy budget pass admits them first and their content never depends on
// how large the volatile sections are.
func RenderStable(pack Pack) string {
	return renderPart(pack, "Project context pack:", false)
}

// volatileHeader frames the working-state tail. The tail is the last message
// of every request, so the header tells the model it is reference material
// and must not override the latest user or system message above it.
const volatileHeader = "Current working state (reference only, refreshed every request; act on the latest user or system message above):"

// RenderVolatile renders the turn-volatile sections under the working-state
// header. The pack's truncation note renders here rather than in the stable
// part: its token estimate moves whenever a volatile section does. Empty
// when there is nothing volatile and no truncation.
func RenderVolatile(pack Pack) string {
	return renderPart(pack, volatileHeader, true)
}

func renderPart(pack Pack, header string, volatile bool) string {
	var body strings.Builder
	for _, section := range pack.Sections {
		if IsVolatile(section.Kind) != volatile {
			continue
		}
		fmt.Fprintf(&body, "\n## %s\n", section.Title)
		if section.Source != "" {
			fmt.Fprintf(&body, "Source: %s\n", section.Source)
		}
		fmt.Fprintf(&body, "\n%s\n", section.Content)
	}
	note := volatile && pack.TokenUsage.Truncated
	if body.Len() == 0 && !note {
		return ""
	}
	var b strings.Builder
	b.WriteString(header + "\n")
	if note {
		fmt.Fprintf(&b, "Context pack truncated (~%d/%d tokens).\n", pack.TokenUsage.EstimatedTokens, pack.TokenUsage.MaxTokens)
	}
	b.WriteString(body.String())
	return strings.TrimRight(b.String(), "\n")
}
