package memory

import (
	"fmt"
	"strings"
	"time"

	"marshal/internal/acpwire"
)

// RenderEntry formats a listed memory for the transcript: the title (first
// line of the memory content), a muted metadata line, then the remaining body.
func RenderEntry(e acpwire.MemoryEntry) string {
	title, body := splitTitleBody(e.Content)
	if body != "" {
		return title + "\n" + formatMeta(e) + "\n" + body
	}
	return title + "\n" + formatMeta(e)
}

func formatMeta(m acpwire.MemoryEntry) string {
	return fmt.Sprintf("%s · %s · %s", m.Kind, m.Confidence, formatAge(acpwire.Time(m.CreatedAt)))
}

func formatAge(t time.Time) string {
	d := time.Since(t)
	if d < 0 {
		// Clock skew or a future timestamp: show "just now" rather than a
		// negative duration.
		return "just now"
	}
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

func memoryTitle(content string) string {
	title, _ := splitTitleBody(content)
	return title
}

func splitTitleBody(content string) (string, string) {
	content = strings.TrimSpace(content)
	idx := strings.Index(content, "\n")
	if idx < 0 {
		return content, ""
	}
	return strings.TrimSpace(content[:idx]), strings.TrimSpace(content[idx+1:])
}
