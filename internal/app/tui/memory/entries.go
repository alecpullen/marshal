package memory

import (
	"fmt"
	"strings"
	"time"

	"marshal/internal/db"
)

// RenderEntry loads a memory by id and formats it for the transcript: the
// title (first line of the memory content), a muted metadata line, then the
// remaining body.
func RenderEntry(database *db.DB, id int64) string {
	m, err := database.GetMemory(id)
	if err != nil {
		return fmt.Sprintf("Memory #%d\n%s", id, err.Error())
	}
	title, body := splitTitleBody(m.Content)
	if body != "" {
		return title + "\n" + formatMeta(m) + "\n" + body
	}
	return title + "\n" + formatMeta(m)
}

func formatMeta(m db.Memory) string {
	return fmt.Sprintf("%s · %s · %s", m.Kind, m.Confidence, formatAge(m.CreatedAt))
}

func formatAge(t time.Time) string {
	d := time.Since(t)
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
