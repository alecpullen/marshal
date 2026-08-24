package sidepanel

import (
	"strings"
	"testing"

	"marshal/internal/tools/registry"
)

func wsData(events ...registry.AuditEvent) Data {
	return Data{Audit: events, Now: at(20)}
}

func TestWorkingSetIrrelevantWhenNothingTouched(t *testing.T) {
	if (WorkingSetSection{}).Relevant(Data{}) {
		t.Fatal("no file activity means the section must not render")
	}
	// A session that only ran shell commands has no working set either.
	if (WorkingSetSection{}).Relevant(wsData(registry.AuditEvent{ToolName: "shell.run", Timestamp: at(1)})) {
		t.Fatal("non-file tools must not make the section relevant")
	}
}

func TestWorkingSetListsMostRecentFirst(t *testing.T) {
	rows := WorkingSetSection{}.Render(wsData(
		writeEvent("old.go", 1), writeEvent("new.go", 9),
	), 40, 0)
	if len(rows) != 2 {
		t.Fatalf("want a row per file, got %d: %v", len(rows), rows)
	}
	if !strings.Contains(rows[0], "new.go") {
		t.Fatalf("most recent must lead, got %q", rows[0])
	}
}

// An edited file must be distinguishable from one only read — that is the
// distinction the section exists to make.
func TestWorkingSetDistinguishesEditsFromReads(t *testing.T) {
	rows := WorkingSetSection{}.Render(wsData(
		writeEvent("edited.go", 5), readEvent("readonly.go", 4),
	), 60, 0)
	edited, readonly := rows[0], rows[1]
	if !strings.Contains(edited, "edit") {
		t.Errorf("edited row should say so: %q", edited)
	}
	if strings.Contains(readonly, "edit") {
		t.Errorf("read-only row must not claim an edit: %q", readonly)
	}
}

func TestWorkingSetShowsEditCount(t *testing.T) {
	rows := WorkingSetSection{}.Render(wsData(
		writeEvent("a.go", 1), writeEvent("a.go", 2), writeEvent("a.go", 3),
	), 60, 0)
	if !strings.Contains(rows[0], "3 edits") {
		t.Fatalf("row should carry the edit count, got %q", rows[0])
	}
}

func TestWorkingSetClipsToMaxRows(t *testing.T) {
	var evs []registry.AuditEvent
	for i := 1; i <= 10; i++ {
		evs = append(evs, writeEvent(string(rune('a'+i))+".go", i))
	}
	if rows := (WorkingSetSection{}).Render(wsData(evs...), 40, 3); len(rows) != 3 {
		t.Fatalf("want 3 rows, got %d", len(rows))
	}
}

func TestWorkingSetTruncatesToWidth(t *testing.T) {
	long := strings.Repeat("d/", 60) + "file.go"
	for _, row := range (WorkingSetSection{}).Render(wsData(writeEvent(long, 3)), 24, 0) {
		if len([]rune(row)) > 24 {
			t.Fatalf("row exceeds width: %d runes", len([]rune(row)))
		}
	}
}

func TestWorkingSetOneLineSummarises(t *testing.T) {
	got := WorkingSetSection{}.OneLine(wsData(writeEvent("a.go", 1), readEvent("b.go", 2)), 40)
	if !strings.Contains(got, "2") {
		t.Fatalf("one-line form should carry the file count, got %q", got)
	}
}

func TestWorkingSetIDIsStable(t *testing.T) {
	// The ID is the tui.side_panel.hidden config key; changing it silently
	// un-hides the section for anyone who had hidden it.
	if got := (WorkingSetSection{}).ID(); got != "files" {
		t.Fatalf("ID = %q, want \"files\"", got)
	}
}
