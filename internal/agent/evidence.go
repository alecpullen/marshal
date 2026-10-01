package agent

import (
	"fmt"
	"strings"

	"marshal/internal/app/session"
)

// formatEvidenceReceipt keeps model-facing receipt text separate from the
// registry result and the session's immutable audit facts.
func formatEvidenceReceipt(record session.EvidenceRecord) string {
	parts := []string{record.Alias, "source " + record.SourceViewID, record.ToolName}
	if record.Error != "" {
		parts = append(parts, "error recorded")
	}
	if record.Denied {
		parts = append(parts, "denied")
	}
	if record.ExitCode != nil && *record.ExitCode != 0 {
		parts = append(parts, fmt.Sprintf("exit %d", *record.ExitCode))
	}
	if record.Retention != "" {
		parts = append(parts, "retention: "+record.Retention)
	}
	if len(record.FilesChanged) > 0 {
		parts = append(parts, "files changed: "+strings.Join(record.FilesChanged, ", "))
	}
	text := "\n\nEvidence receipt: " + strings.Join(parts, " · ")
	if len(text) > 700 {
		text = text[:697] + "..."
	}
	return text
}
