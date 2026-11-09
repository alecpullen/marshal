// Package sessiontest holds session fixtures that more than one package's
// tests share. Only tests import it.
package sessiontest

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
)

// LargeTurns and LargeCalls shape LargeSession: each turn is a prompt, one
// step of LargeCalls shell.run calls with 1 KB of output, and a final reply.
const (
	LargeTurns = 200
	LargeCalls = 10
)

// LargeSession builds the 2,000-call session P3's budgets measure (R10).
func LargeSession(tb testing.TB) *session.State {
	tb.Helper()
	st := session.New(config.Default(), tb.TempDir(), time.Now(), session.Persistence{})
	for turn := 0; turn < LargeTurns; turn++ {
		st.AddMessage(session.RoleUser, fmt.Sprintf("turn %d", turn), session.ContentTypePlain)
		step := st.BeginStep(session.Actor{})
		for call := 0; call < LargeCalls; call++ {
			id := fmt.Sprintf("t%03d-c%02d", turn, call)
			st.LogToolCall(registry.AuditEvent{
				ToolName:      "shell.run",
				StepID:        step,
				ToolCallID:    id,
				Args:          json.RawMessage(fmt.Sprintf(`{"command":"echo %s"}`, id)),
				ResultContent: strings.Repeat("x", 1024),
			})
		}
		st.EndStep(step)
		st.AddMessageFinal(session.RoleAssistant, "done", session.ContentTypePlain)
	}
	return st
}
