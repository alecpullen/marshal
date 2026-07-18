package report

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestReporterWritesArtifacts(t *testing.T) {
	r := New()
	r.Record("turn_started", map[string]any{"scenario": "help_open_close"})
	r.Record("key_sent", map[string]any{"key": "?"})
	r.Record("task_done", map[string]any{"scenario": "help_open_close", "success": true, "duration_ms": 1200})

	dir := t.TempDir()
	if err := r.WriteReport(dir); err != nil {
		t.Fatalf("WriteReport: %v", err)
	}

	for _, name := range []string{"usability-report.json", "usability-benchmark.json", "friction-log.md"} {
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("missing artifact %s: %v", name, err)
		}
	}

	data, err := os.ReadFile(filepath.Join(dir, "usability-report.json"))
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	var report map[string]any
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatalf("unmarshal report: %v", err)
	}
	events, ok := report["events"].([]any)
	if !ok || len(events) != 3 {
		t.Fatalf("expected 3 events, got %v", report["events"])
	}
}
