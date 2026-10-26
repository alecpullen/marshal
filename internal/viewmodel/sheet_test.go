package viewmodel

import (
	"encoding/json"
	"testing"
	"time"

	"marshal/internal/acpwire"
	"marshal/internal/tools/registry"
)

func toolEvents() []registry.AuditEvent {
	return []registry.AuditEvent{
		{ToolName: "file.read", Duration: 10 * time.Millisecond},
		{ToolName: "file.read", Duration: 20 * time.Millisecond},
		{ToolName: "shell.run", Duration: 4200 * time.Millisecond},
		{ToolName: "shell.run", Duration: 100 * time.Millisecond, Error: "exit 1"},
		{ToolName: "file.write_patch", Duration: 5 * time.Millisecond},
	}
}

func TestSheetToolsOfAggregates(t *testing.T) {
	got := SheetToolsOf(toolEvents())
	if len(got) != 3 {
		t.Fatalf("got %d tools, want 3: %+v", len(got), got)
	}
	// Descending by call count, so file.read and shell.run (2 each) lead.
	byName := map[string]acpwire.SheetTool{}
	for _, s := range got {
		byName[s.Name] = s
	}
	if byName["shell.run"].Calls != 2 {
		t.Errorf("shell.run calls = %d, want 2", byName["shell.run"].Calls)
	}
	if byName["shell.run"].Errors != 1 {
		t.Errorf("shell.run errors = %d, want 1", byName["shell.run"].Errors)
	}
	if byName["shell.run"].SlowestMs != 4200 {
		t.Errorf("shell.run slowest = %dms, want 4200ms", byName["shell.run"].SlowestMs)
	}
	if byName["file.read"].Errors != 0 {
		t.Errorf("file.read errors = %d, want 0", byName["file.read"].Errors)
	}
}

func TestSheetToolsOfSortsByCallsDescending(t *testing.T) {
	got := SheetToolsOf(toolEvents())
	for i := 1; i < len(got); i++ {
		if got[i-1].Calls < got[i].Calls {
			t.Errorf("not sorted descending by calls: %+v", got)
			break
		}
	}
}

func TestSheetToolsOfEmptyIsNil(t *testing.T) {
	if got := SheetToolsOf(nil); got != nil {
		t.Errorf("SheetToolsOf(nil) = %+v, want nil", got)
	}
}

func sheetAt(min int) time.Time {
	return time.Date(2026, 8, 25, 14, min, 0, 0, time.UTC)
}

func writeEvent(path string, min int) registry.AuditEvent {
	return registry.AuditEvent{
		ToolName:     "file.write_patch",
		FilesChanged: []string{path},
		Timestamp:    sheetAt(min),
	}
}

func readEvent(path string, min int) registry.AuditEvent {
	args, _ := json.Marshal(map[string]string{"path": path})
	return registry.AuditEvent{
		ToolName:  "file.read",
		Args:      args,
		Timestamp: sheetAt(min),
	}
}

func TestSheetFilesOfCountsEditsAndReads(t *testing.T) {
	stats := SheetFilesOf([]registry.AuditEvent{
		writeEvent("a.go", 1), writeEvent("a.go", 5), readEvent("a.go", 3),
		readEvent("b.go", 2),
	})
	byPath := map[string]acpwire.SheetFile{}
	for _, s := range stats {
		byPath[s.Path] = s
	}
	if got := byPath["a.go"]; got.Edits != 2 || got.Reads != 1 {
		t.Fatalf("a.go = %+v, want 2 edits / 1 read", got)
	}
	if got := byPath["b.go"]; got.Edits != 0 || got.Reads != 1 {
		t.Fatalf("b.go = %+v, want 0 edits / 1 read", got)
	}
}

// Most recently touched first — that is the ordering that answers "what is
// it working on right now".
func TestSheetFilesOfOrdersByRecency(t *testing.T) {
	stats := SheetFilesOf([]registry.AuditEvent{
		writeEvent("old.go", 1), writeEvent("new.go", 9), writeEvent("mid.go", 5),
	})
	want := []string{"new.go", "mid.go", "old.go"}
	for i, w := range want {
		if stats[i].Path != w {
			t.Fatalf("position %d = %q, want %q (order: %+v)", i, stats[i].Path, w, stats)
		}
	}
}

// Ties must break alphabetically or the rail reshuffles between renders,
// which is what SheetToolsOf does and why.
func TestSheetFilesOfTiesBreakAlphabetically(t *testing.T) {
	stats := SheetFilesOf([]registry.AuditEvent{writeEvent("b.go", 4), writeEvent("a.go", 4)})
	if stats[0].Path != "a.go" || stats[1].Path != "b.go" {
		t.Fatalf("ties must break alphabetically, got %+v", stats)
	}
}

func TestSheetFilesOfLastAtIsTheLatestTouch(t *testing.T) {
	stats := SheetFilesOf([]registry.AuditEvent{writeEvent("a.go", 1), readEvent("a.go", 7)})
	if len(stats) != 1 || stats[0].LastAt != acpwire.Millis(sheetAt(7)) {
		t.Fatalf("LastAt should be the most recent touch of either kind, got %+v", stats)
	}
}

// A multi-file patch counts once for each file it changed.
func TestSheetFilesOfMultiFilePatch(t *testing.T) {
	stats := SheetFilesOf([]registry.AuditEvent{{
		ToolName:     "file.write_patch",
		FilesChanged: []string{"a.go", "b.go"},
		Timestamp:    sheetAt(3),
	}})
	if len(stats) != 2 {
		t.Fatalf("want a stat per changed file, got %+v", stats)
	}
}

// Non-file tools contribute nothing: a shell command is not a working-set
// entry even though its args contain a path-shaped string.
func TestSheetFilesOfIgnoresNonFileTools(t *testing.T) {
	args, _ := json.Marshal(map[string]string{"command": "cat internal/app/main.go"})
	stats := SheetFilesOf([]registry.AuditEvent{{ToolName: "shell.run", Args: args, Timestamp: sheetAt(1)}})
	if len(stats) != 0 {
		t.Fatalf("shell.run must not create a working-set entry, got %+v", stats)
	}
}

// A failed call did not touch the file and must not appear.
func TestSheetFilesOfIgnoresFailedCalls(t *testing.T) {
	e := writeEvent("a.go", 1)
	e.Error = "permission denied"
	if stats := SheetFilesOf([]registry.AuditEvent{e}); len(stats) != 0 {
		t.Fatalf("failed calls must not count, got %+v", stats)
	}
}

func TestSheetFilesOfEmptyIsNil(t *testing.T) {
	if got := SheetFilesOf(nil); got != nil {
		t.Fatalf("want nil, got %+v", got)
	}
}

func TestSheetFilesOfMalformedArgsAreSkipped(t *testing.T) {
	stats := SheetFilesOf([]registry.AuditEvent{
		{ToolName: "file.read", Args: json.RawMessage(`{not json`), Timestamp: sheetAt(1)},
	})
	if len(stats) != 0 {
		t.Fatalf("malformed args must be skipped, got %+v", stats)
	}
}
