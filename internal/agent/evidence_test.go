package agent

import (
	"strings"
	"testing"

	"marshal/internal/app/session"
)

func TestFormatEvidenceReceiptIsBoundedAndUsesObservedFacts(t *testing.T) {
	exit := 4
	text := formatEvidenceReceipt(session.EvidenceRecord{
		Alias: "e1-9", SourceViewID: "audit@s2:9", ToolName: "shell.run",
		Denied: true, ExitCode: &exit, FilesChanged: []string{"one.go", "two.go"},
		Retention: "slice_truncated: output shortened",
	})
	for _, want := range []string{"e1-9", "audit@s2:9", "denied", "exit 4", "one.go, two.go", "retention:"} {
		if !strings.Contains(text, want) {
			t.Fatalf("receipt %q omitted %q", text, want)
		}
	}
	if len(formatEvidenceReceipt(session.EvidenceRecord{Alias: strings.Repeat("x", 1000)})) > 700 {
		t.Fatal("receipt exceeded the 700 byte cap")
	}
}
