package permissions

import (
	"testing"

	"marshal/internal/app/session"
)

func TestPatternForApprovalShellRun(t *testing.T) {
	tc := &session.PendingToolCall{Name: "shell.run", Command: "git status"}
	if got := PatternForApproval(tc); got != "git status" {
		t.Fatalf("expected full command, got %q", got)
	}
}
