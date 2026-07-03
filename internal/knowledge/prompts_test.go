package knowledge

import (
	"strings"
	"testing"
	"time"

	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
)

func TestBuildExtractionPromptIncludesTranscriptActivityAndFiles(t *testing.T) {
	messages := []session.Message{
		{Role: session.RoleUser, Content: "Fix the login bug", CreatedAt: time.Unix(100, 0)},
		{Role: session.RoleAssistant, Content: "Fixed it.", CreatedAt: time.Unix(101, 0)},
	}
	auditLog := []registry.AuditEvent{
		{ToolName: "file.write_patch", ResultSummary: "applied patch to internal/foo/bar.go"},
	}
	touchedFiles := map[string]string{
		"internal/foo/bar.go": "package foo\n\nfunc Bar() {}\n",
	}

	msg := BuildExtractionPrompt(messages, auditLog, touchedFiles)

	for _, want := range []string{
		"Fix the login bug",
		"Fixed it.",
		"file.write_patch -> applied patch to internal/foo/bar.go",
		"internal/foo/bar.go",
		"func Bar() {}",
	} {
		if !strings.Contains(msg.Content, want) {
			t.Fatalf("prompt missing %q:\n%s", want, msg.Content)
		}
	}
}

func TestBuildExtractionPromptHandlesEmptyInputs(t *testing.T) {
	msg := BuildExtractionPrompt(nil, nil, nil)

	for _, want := range []string{"(no messages)", "(no tool activity)", "(no files touched)"} {
		if !strings.Contains(msg.Content, want) {
			t.Fatalf("prompt missing %q:\n%s", want, msg.Content)
		}
	}
}
