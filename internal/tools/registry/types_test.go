package registry

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"marshal/internal/progress"
)

func TestSandboxMetaLimitsJSONIncludesOutputTruncated(t *testing.T) {
	m := SandboxMeta{OutputTruncated: true}
	j, err := m.LimitsJSON()
	if err != nil {
		t.Fatalf("LimitsJSON: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(j), &parsed); err != nil {
		t.Fatalf("LimitsJSON() = %q, cannot unmarshal: %v", j, err)
	}
	v, ok := parsed["output_truncated"]
	if !ok {
		t.Fatalf("LimitsJSON() = %q, missing output_truncated key", j)
	}
	if b, _ := v.(bool); !b {
		t.Fatalf("output_truncated = %v (%T), want true", v, v)
	}

	m2 := SandboxMeta{OutputTruncated: false}
	j2, err := m2.LimitsJSON()
	if err != nil {
		t.Fatalf("LimitsJSON: %v", err)
	}
	if strings.Contains(j2, "output_truncated") {
		t.Fatalf("LimitsJSON() for false = %q, should not contain output_truncated", j2)
	}
}

func TestSandboxMetaLimitsJSONReturnsValidJSON(t *testing.T) {
	// LimitsJSON must return a non-empty JSON string and a nil error
	// for valid meta. The marshal-error path is not exercisable through
	// the public API because all map-value types are primitives.
	m := SandboxMeta{Backend: "restricted"}
	s, err := m.LimitsJSON()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s == "" {
		t.Fatal("expected non-empty JSON")
	}
}

// executionWithProgress builds a fresh execution record carrying progress, so
// each ownership subtest starts from its own nested values.
func executionWithProgress() (CommandExecution, *progress.Snapshot, *progress.Config, *int) {
	total := 100.0
	snap := &progress.Snapshot{
		Frame: progress.Frame{
			Version:         progress.Version,
			Sequence:        7,
			PrimaryMetricID: "files",
			Phase:           &progress.Phase{ID: "compile", Label: "Compiling", Index: 2, Total: 4},
			Metrics: []progress.Metric{
				{ID: "files", Label: "Files", Unit: "files", Current: 42, Total: &total},
			},
			Detail: "building",
		},
		ObservedAt: time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC),
	}
	cfg := &progress.Config{Profile: "tests", Source: "go-test-json", Title: "go test"}
	code := 0
	ex := CommandExecution{
		Version:        CommandExecutionVersion,
		ExecutionID:    "exec-1",
		State:          "running",
		Outcome:        "",
		ExitCode:       &code,
		ProgressConfig: cfg,
		Progress:       snap,
	}
	return ex, snap, cfg, &code
}

func TestExecutionProgressRoundTripAndLegacy(t *testing.T) {
	ex, _, _, _ := executionWithProgress()
	ex.State = "completed"
	ex.Outcome = "nonzero_exit"
	code := 3
	ex.ExitCode = &code
	ex.ResultRevision = "rev-9"
	ex.SourceEpoch = "epoch-1"

	notice, err := CommandExecutionNotice(ex)
	if err != nil {
		t.Fatalf("CommandExecutionNotice: %v", err)
	}
	raw, err := json.Marshal(notice)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back ToolNotice
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	got, ok := back.Execution()
	if !ok {
		t.Fatal("Execution() ok = false, want true")
	}
	if got.State != "completed" || got.Outcome != "nonzero_exit" {
		t.Errorf("state/outcome = %q/%q, want completed/nonzero_exit", got.State, got.Outcome)
	}
	if got.ExitCode == nil || *got.ExitCode != 3 {
		t.Errorf("ExitCode = %v, want 3", got.ExitCode)
	}
	if got.ResultRevision != "rev-9" || got.SourceEpoch != "epoch-1" {
		t.Errorf("result/source = %q/%q, want rev-9/epoch-1", got.ResultRevision, got.SourceEpoch)
	}
	if got.ProgressConfig == nil || *got.ProgressConfig != (progress.Config{Profile: "tests", Source: "go-test-json", Title: "go test"}) {
		t.Errorf("ProgressConfig = %#v, want tests/go-test-json/go test", got.ProgressConfig)
	}
	if got.Progress == nil {
		t.Fatal("Progress = nil, want the round-tripped snapshot")
	}
	if got.Progress.Sequence != 7 || got.Progress.PrimaryMetricID != "files" || got.Progress.Detail != "building" {
		t.Errorf("Progress header = %#v, want sequence 7/files/building", got.Progress.Frame)
	}
	if got.Progress.Phase == nil || got.Progress.Phase.ID != "compile" || got.Progress.Phase.Index != 2 {
		t.Errorf("Progress.Phase = %#v, want compile/2", got.Progress.Phase)
	}
	if len(got.Progress.Metrics) != 1 || got.Progress.Metrics[0].Current != 42 || got.Progress.Metrics[0].Total == nil || *got.Progress.Metrics[0].Total != 100 {
		t.Errorf("Progress.Metrics = %#v, want files=42/100", got.Progress.Metrics)
	}
	if got.Progress.ObservedAt.IsZero() {
		t.Error("Progress.ObservedAt is zero, want the persisted observation time")
	}

	// Legacy: a version-0 record with no progress decodes unchanged, and no
	// zero exit code is manufactured for one that never had a code.
	legacy := func() CommandExecution {
		return CommandExecution{Version: 0, State: "completed", Outcome: "nonzero_exit", ExitCode: intPtr(1)}
	}
	for name, value := range map[string]any{
		"struct": legacy(),
		"map":    map[string]any{"version": float64(0), "state": "completed", "outcome": "nonzero_exit", "exit_code": float64(1)},
	} {
		got, ok := commandExecutionFromValue(value)
		if !ok {
			t.Fatalf("%s: ok = false, want true", name)
		}
		if got.Progress != nil || got.ProgressConfig != nil {
			t.Errorf("%s: progress = %#v/%#v, want nil", name, got.Progress, got.ProgressConfig)
		}
		if got.State != "completed" || got.Outcome != "nonzero_exit" || got.ExitCode == nil || *got.ExitCode != 1 {
			t.Errorf("%s: legacy state/outcome/exit mutated: %#v", name, got)
		}
	}
	noCode, ok := commandExecutionFromValue(map[string]any{"version": float64(1), "state": "failed", "outcome": "startup_failure"})
	if !ok {
		t.Fatal("no-exit record: ok = false, want true")
	}
	if noCode.ExitCode != nil {
		t.Errorf("ExitCode = %v, want nil (never manufactured)", *noCode.ExitCode)
	}

	// An unrecognised future progress version is omitted and diagnosed, but
	// the actual execution record it rode on stays readable.
	future := &ToolNotice{Kind: NoticeCommandExecution, Data: map[string]any{
		CommandExecutionKey: map[string]any{
			"version":      float64(CommandExecutionVersion),
			"execution_id": "exec-future",
			"state":        "completed",
			"outcome":      "nonzero_exit",
			"exit_code":    float64(3),
			"progress": map[string]any{
				"version":  float64(progress.Version + 1),
				"sequence": float64(9),
				"metrics":  []any{map[string]any{"id": "x", "unit": "items", "current": float64(1)}},
			},
		},
	}}
	gotFuture, ok := future.Execution()
	if !ok {
		t.Fatal("future progress: ok = false, want the record to survive")
	}
	if gotFuture.State != "completed" || gotFuture.ExitCode == nil || *gotFuture.ExitCode != 3 {
		t.Errorf("future progress changed the record: %#v", gotFuture)
	}
	if gotFuture.Progress == nil || !gotFuture.Progress.Partial || gotFuture.Progress.Warning != progressVersionDiagnostic {
		t.Fatalf("future progress = %#v, want a bounded partial diagnostic", gotFuture.Progress)
	}
	if len(gotFuture.Progress.Metrics) != 0 || gotFuture.Progress.Phase != nil {
		t.Errorf("future progress leaked measurements: %#v", gotFuture.Progress)
	}
}

func intPtr(v int) *int { return &v }

func TestExecutionProgressNoticeOwnsNestedSnapshots(t *testing.T) {
	// assertOriginal checks the nested values a decoded record must still hold
	// after the caller (or the notice) mutated its own copy.
	assertOriginal := func(t *testing.T, got CommandExecution) {
		t.Helper()
		if got.ExitCode == nil || *got.ExitCode != 0 {
			t.Errorf("ExitCode = %v, want 0", got.ExitCode)
		}
		if got.ProgressConfig == nil || got.ProgressConfig.Title != "go test" {
			t.Errorf("ProgressConfig = %#v, want title %q", got.ProgressConfig, "go test")
		}
		if got.Progress == nil || got.Progress.Phase == nil {
			t.Fatalf("Progress = %#v, want a snapshot with a phase", got.Progress)
		}
		if got.Progress.Phase.ID != "compile" {
			t.Errorf("Progress.Phase.ID = %q, want compile", got.Progress.Phase.ID)
		}
		if len(got.Progress.Metrics) != 1 || got.Progress.Metrics[0].Current != 42 {
			t.Errorf("Progress.Metrics = %#v, want one metric with current 42", got.Progress.Metrics)
		}
	}

	t.Run("struct", func(t *testing.T) {
		ex, snap, cfg, code := executionWithProgress()
		notice := NewCommandExecutionNotice(ex)
		// Mutating the caller's own nested values must not reach the stored payload.
		*snap.Phase = progress.Phase{ID: "mutated"}
		snap.Metrics[0].Current = 999
		*cfg = progress.Config{Title: "mutated"}
		*code = 99
		got, ok := notice.Execution()
		if !ok {
			t.Fatal("Execution() ok = false")
		}
		assertOriginal(t, got)
	})

	t.Run("pointer", func(t *testing.T) {
		ex, snap, cfg, code := executionWithProgress()
		got, ok := commandExecutionFromValue(&ex)
		if !ok {
			t.Fatal("commandExecutionFromValue(pointer) ok = false")
		}
		// The caller mutates its own nested values after the decode.
		snap.Metrics[0].Current = 999
		cfg.Title = "mutated"
		*code = 99
		assertOriginal(t, got)

		// And the decoded copy cannot reach back into the caller's value.
		got.Progress.Metrics[0].Current = 777
		if snap.Metrics[0].Current != 999 {
			t.Errorf("decoded copy aliased caller metrics: %v", snap.Metrics[0].Current)
		}
	})

	t.Run("map", func(t *testing.T) {
		value := map[string]any{
			"version":      float64(CommandExecutionVersion),
			"execution_id": "exec-map",
			"exit_code":    float64(0),
			"progress_config": map[string]any{
				"profile": "tests", "source": "go-test-json", "title": "go test",
			},
			"progress": map[string]any{
				"version":         float64(progress.Version),
				"sequence":        float64(7),
				"primaryMetricId": "files",
				"phase":           map[string]any{"id": "compile", "label": "Compiling", "index": float64(2), "total": float64(4)},
				"metrics":         []any{map[string]any{"id": "files", "label": "Files", "unit": "files", "current": float64(42)}},
			},
		}
		got, ok := commandExecutionFromValue(value)
		if !ok {
			t.Fatal("commandExecutionFromValue(map) ok = false")
		}
		// Each decode rebuilds from the map, so a mutated result cannot leak
		// into the next one.
		got.Progress.Metrics[0].Current = 999
		got.Progress.Phase.ID = "mutated"
		again, ok := commandExecutionFromValue(value)
		if !ok {
			t.Fatal("second decode ok = false")
		}
		assertOriginal(t, again)
	})

	t.Run("json", func(t *testing.T) {
		ex, _, _, _ := executionWithProgress()
		notice := NewCommandExecutionNotice(ex)
		// Mutating the caller after the notice is built must not reach it.
		ex.Progress.Metrics[0].Current = 999
		raw, err := json.Marshal(notice)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var back ToolNotice
		if err := json.Unmarshal(raw, &back); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		got, ok := back.Execution()
		if !ok {
			t.Fatal("Execution() ok = false")
		}
		assertOriginal(t, got)
		got.Progress.Phase.ID = "mutated"
		again, _ := back.Execution()
		assertOriginal(t, again)
	})
}
