package sidepanel

import (
	"encoding/json"
	"testing"
	"time"

	"marshal/internal/tools/registry"
)

func at(min int) time.Time {
	return time.Date(2026, 8, 25, 14, min, 0, 0, time.UTC)
}

func writeEvent(path string, min int) registry.AuditEvent {
	return registry.AuditEvent{
		ToolName:     "file.write_patch",
		FilesChanged: []string{path},
		Timestamp:    at(min),
	}
}

func readEvent(path string, min int) registry.AuditEvent {
	args, _ := json.Marshal(map[string]string{"path": path})
	return registry.AuditEvent{
		ToolName:  "file.read",
		Args:      args,
		Timestamp: at(min),
	}
}

func TestFileStatsCountsEditsAndReads(t *testing.T) {
	stats := FileStats([]registry.AuditEvent{
		writeEvent("a.go", 1), writeEvent("a.go", 5), readEvent("a.go", 3),
		readEvent("b.go", 2),
	})
	byPath := map[string]FileStat{}
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
func TestFileStatsOrdersByRecency(t *testing.T) {
	stats := FileStats([]registry.AuditEvent{
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
// which is what ToolStats does and why.
func TestFileStatsTiesBreakAlphabetically(t *testing.T) {
	stats := FileStats([]registry.AuditEvent{writeEvent("b.go", 4), writeEvent("a.go", 4)})
	if stats[0].Path != "a.go" || stats[1].Path != "b.go" {
		t.Fatalf("ties must break alphabetically, got %+v", stats)
	}
}

func TestFileStatsLastIsTheLatestTouch(t *testing.T) {
	stats := FileStats([]registry.AuditEvent{writeEvent("a.go", 1), readEvent("a.go", 7)})
	if len(stats) != 1 || !stats[0].Last.Equal(at(7)) {
		t.Fatalf("Last should be the most recent touch of either kind, got %+v", stats)
	}
}

// A multi-file patch counts once for each file it changed.
func TestFileStatsMultiFilePatch(t *testing.T) {
	stats := FileStats([]registry.AuditEvent{{
		ToolName:     "file.write_patch",
		FilesChanged: []string{"a.go", "b.go"},
		Timestamp:    at(3),
	}})
	if len(stats) != 2 {
		t.Fatalf("want a stat per changed file, got %+v", stats)
	}
}

// Non-file tools contribute nothing: a shell command is not a working-set
// entry even though its args contain a path-shaped string.
func TestFileStatsIgnoresNonFileTools(t *testing.T) {
	args, _ := json.Marshal(map[string]string{"command": "cat internal/app/main.go"})
	stats := FileStats([]registry.AuditEvent{{ToolName: "shell.run", Args: args, Timestamp: at(1)}})
	if len(stats) != 0 {
		t.Fatalf("shell.run must not create a working-set entry, got %+v", stats)
	}
}

// A failed call did not touch the file and must not appear.
func TestFileStatsIgnoresFailedCalls(t *testing.T) {
	e := writeEvent("a.go", 1)
	e.Error = "permission denied"
	if stats := FileStats([]registry.AuditEvent{e}); len(stats) != 0 {
		t.Fatalf("failed calls must not count, got %+v", stats)
	}
}

func TestFileStatsEmptyIsNil(t *testing.T) {
	if got := FileStats(nil); got != nil {
		t.Fatalf("want nil, got %+v", got)
	}
}

func TestFileStatsMalformedArgsAreSkipped(t *testing.T) {
	stats := FileStats([]registry.AuditEvent{
		{ToolName: "file.read", Args: json.RawMessage(`{not json`), Timestamp: at(1)},
	})
	if len(stats) != 0 {
		t.Fatalf("malformed args must be skipped, got %+v", stats)
	}
}
