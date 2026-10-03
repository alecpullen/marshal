package tui

import (
	"strings"
	"testing"
	"time"

	"marshal/internal/app/session"
)

func TestContextRequestOpensTheLastRequestPanel(t *testing.T) {
	m := newTestModel(t)
	m.state.SetRequestInspection(session.RequestInspection{
		AttemptID: 1,
		At:        time.Unix(100, 0),
		Provider:  "ollama",
		Model:     "qwen3:32b",
		Messages:  []session.InspectionMessage{{Role: "user", Content: "fix the parser"}},
	})

	updated, _ := m.dispatchCommand("/context request")
	m = *updated.(*Model)

	view := stripANSI(m.viewString())
	for _, want := range []string{"Last request", "ollama/qwen3:32b", "dispatched", "1  user"} {
		if !strings.Contains(view, want) {
			t.Fatalf("panel should show %q:\n%s", want, view)
		}
	}
}
