package tui

import (
	"testing"
	"time"

	"marshal/internal/acpwire"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
)

// registerRunningSubagent registers a running background child on the test
// model's session. The box only tracks views with a live Child state;
// pipeline/SDD cards (Child == nil) are already pinned by the progress row.
func registerRunningSubagent(t *testing.T, m *Model, label string) {
	t.Helper()
	child := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	stateOf(m).RegisterSubagent(label, child)
}

// workT0 is the fixed clock the work-card fixtures are built against.
var workT0 = time.Unix(10_000, 0)

// workRunningCard is a running command card with one activity line, so a
// collapsed card is exactly two rows.
func workRunningCard(key string, started time.Time) acpwire.WorkItem {
	return acpwire.WorkItem{
		TaskKey:   key,
		Kind:      "command",
		Label:     key,
		SourceID:  key,
		State:     "running",
		StartedAt: acpwire.Millis(started),
		Activity:  "working",
	}
}

func workSnapOf(items ...acpwire.WorkItem) acpwire.WorkSnapshot {
	return acpwire.WorkSnapshot{Version: 1, Items: items}
}
