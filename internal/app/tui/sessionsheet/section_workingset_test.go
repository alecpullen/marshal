package sessionsheet

import (
	"strings"
	"testing"

	"marshal/internal/acpwire"
)

// wsData builds a sheet snapshot whose file rows are files, in the order the
// aggregate returns them.
func wsData(files ...acpwire.SheetFile) Data {
	return Data{Files: files}
}

func TestWorkingSetIrrelevantWhenNothingTouched(t *testing.T) {
	if (WorkingSetSection{}).Relevant(Data{}) {
		t.Fatal("no file activity means the section must not render")
	}
	// A session that only ran shell commands has no working set either: the
	// aggregate carries no file rows for it.
	if (WorkingSetSection{}).Relevant(wsData()) {
		t.Fatal("no file rows must not make the section relevant")
	}
}

func TestWorkingSetListsMostRecentFirst(t *testing.T) {
	rows := WorkingSetSection{}.Render(wsData(
		acpwire.SheetFile{Path: "new.go", Edits: 1, LastAt: 9000},
		acpwire.SheetFile{Path: "old.go", Edits: 1, LastAt: 1000},
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
		acpwire.SheetFile{Path: "edited.go", Edits: 1, LastAt: 5000},
		acpwire.SheetFile{Path: "readonly.go", Reads: 1, LastAt: 4000},
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
		acpwire.SheetFile{Path: "a.go", Edits: 3, LastAt: 3000},
	), 60, 0)
	if !strings.Contains(rows[0], "3 edits") {
		t.Fatalf("row should carry the edit count, got %q", rows[0])
	}
}

func TestWorkingSetClipsToMaxRows(t *testing.T) {
	var files []acpwire.SheetFile
	for i := 1; i <= 10; i++ {
		files = append(files, acpwire.SheetFile{Path: string(rune('a'+i)) + ".go", Edits: 1, LastAt: int64(i)})
	}
	if rows := (WorkingSetSection{}).Render(wsData(files...), 40, 3); len(rows) != 3 {
		t.Fatalf("want 3 rows, got %d", len(rows))
	}
}

func TestWorkingSetTruncatesToWidth(t *testing.T) {
	long := strings.Repeat("d/", 60) + "file.go"
	for _, row := range (WorkingSetSection{}).Render(wsData(acpwire.SheetFile{Path: long, Edits: 1}), 24, 0) {
		if len([]rune(row)) > 24 {
			t.Fatalf("row exceeds width: %d runes", len([]rune(row)))
		}
	}
}

func TestWorkingSetOneLineSummarises(t *testing.T) {
	got := WorkingSetSection{}.OneLine(wsData(
		acpwire.SheetFile{Path: "a.go", Edits: 1},
		acpwire.SheetFile{Path: "b.go", Reads: 1},
	), 40)
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
