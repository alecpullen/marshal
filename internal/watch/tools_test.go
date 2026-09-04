package watch

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"marshal/internal/pubsub"
	"marshal/internal/tools/native"
	"marshal/internal/tools/registry"
)

// fakeRunSample returns a fixed stdout/exitCode for command samples.
func fakeRunSample(stdout string, exitCode int) func(context.Context, string, string) (string, int, error) {
	return func(context.Context, string, string) (string, int, error) {
		return stdout, exitCode, nil
	}
}

func newToolsManager(t *testing.T, deps Deps) *Manager {
	t.Helper()
	m := NewManager(context.Background(), deps)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = m.Close(ctx)
	})
	return m
}

func registerTools(t *testing.T, m *Manager) *registry.Registry {
	t.Helper()
	reg := registry.New()
	if err := RegisterTools(reg, m); err != nil {
		t.Fatalf("RegisterTools: %v", err)
	}
	return reg
}

func invoke(t *testing.T, reg *registry.Registry, name, args string) (registry.ToolResult, error) {
	t.Helper()
	tool, ok := reg.Lookup(name)
	if !ok {
		t.Fatalf("Lookup(%q) ok=false", name)
	}
	return tool.Handler(context.Background(), registry.ToolCall{Name: name, Args: json.RawMessage(args)})
}

func TestWatchToolsRiskClassification(t *testing.T) {
	m := newToolsManager(t, Deps{})
	reg := registerTools(t, m)

	want := map[string]registry.RiskLevel{
		"watch.start":  registry.RiskCommand,
		"watch.list":   registry.RiskReadOnly,
		"watch.status": registry.RiskReadOnly,
		"watch.stop":   registry.RiskCommand,
	}
	for name, risk := range want {
		tool, ok := reg.Lookup(name)
		if !ok {
			t.Fatalf("Lookup(%q) ok=false", name)
		}
		if tool.Risk != risk {
			t.Fatalf("%s risk = %q, want %q", name, tool.Risk, risk)
		}
		if tool.Handler == nil {
			t.Fatalf("%s Handler is nil", name)
		}
		if len(tool.Schema) == 0 {
			t.Fatalf("%s Schema is empty", name)
		}
	}
}

func TestWatchStartCommandHappyPath(t *testing.T) {
	m := newToolsManager(t, Deps{
		RunSample: fakeRunSample("hello", 0),
		DirFn:     func() string { return "/tmp" },
	})
	reg := registerTools(t, m)

	res, err := invoke(t, reg, "watch.start", `{"name":"mywatch","kind":"command","command":"echo hello","interval":"5s"}`)
	if err != nil {
		t.Fatalf("watch.start: %v", err)
	}
	if !strings.Contains(res.Content, "watch_id: w1") {
		t.Fatalf("content = %q, want watch_id: w1", res.Content)
	}
	if !strings.Contains(res.Content, "kind: command") {
		t.Fatalf("content = %q, want kind: command", res.Content)
	}

	// The watch is registered and listed.
	list, err := invoke(t, reg, "watch.list", `{}`)
	if err != nil {
		t.Fatalf("watch.list: %v", err)
	}
	if !strings.Contains(list.Content, "w1") || !strings.Contains(list.Content, "mywatch") {
		t.Fatalf("watch.list content = %q, want w1 and mywatch", list.Content)
	}
	if !strings.Contains(list.Content, "kind=command") || !strings.Contains(list.Content, "state=watching") {
		t.Fatalf("watch.list content = %q, want kind=command and state=watching", list.Content)
	}
}

func TestWatchStartValidationErrors(t *testing.T) {
	m := newToolsManager(t, Deps{})
	reg := registerTools(t, m)

	// Missing required name (schema validation).
	if _, err := invoke(t, reg, "watch.start", `{"kind":"command"}`); err == nil {
		t.Fatal("expected schema error for missing name")
	}
	// Missing required kind (schema validation).
	if _, err := invoke(t, reg, "watch.start", `{"name":"x"}`); err == nil {
		t.Fatal("expected schema error for missing kind")
	}
	// Unknown kind is rejected by the schema enum before the handler runs.
	if _, err := invoke(t, reg, "watch.start", `{"name":"x","kind":"bogus"}`); err == nil || !strings.Contains(err.Error(), "must be one of") {
		t.Fatalf("expected schema enum error for unknown kind, got %v", err)
	}
	// Unknown mode is rejected by the schema enum before the handler runs.
	if _, err := invoke(t, reg, "watch.start", `{"name":"x","kind":"command","mode":"bogus"}`); err == nil || !strings.Contains(err.Error(), "must be one of") {
		t.Fatalf("expected schema enum error for unknown mode, got %v", err)
	}
	// Invalid interval.
	if _, err := invoke(t, reg, "watch.start", `{"name":"x","kind":"command","interval":"not-a-duration"}`); err == nil || !strings.Contains(err.Error(), "invalid interval") {
		t.Fatalf("expected invalid interval error, got %v", err)
	}
	// Bad condition.
	if _, err := invoke(t, reg, "watch.start", `{"name":"x","kind":"command","condition":"bogus"}`); err == nil || !strings.Contains(err.Error(), "unknown condition type") {
		t.Fatalf("expected condition error, got %v", err)
	}
}

func TestWatchStartJobUnavailableWithoutBroker(t *testing.T) {
	// No SubscribeJobs wired -> job watches must be rejected with a clear error.
	m := newToolsManager(t, Deps{})
	reg := registerTools(t, m)

	_, err := invoke(t, reg, "watch.start", `{"name":"j","kind":"job","job_id":"job-1"}`)
	if err == nil || !strings.Contains(err.Error(), "job watching unavailable") {
		t.Fatalf("expected job watching unavailable error, got %v", err)
	}
}

func TestWatchStartJobWithBroker(t *testing.T) {
	// A broker wired makes job watches available; the manager validates the
	// job ID via JobLookup.
	m := newToolsManager(t, Deps{
		SubscribeJobs: func(ctx context.Context) (<-chan pubsub.Event[native.JobEvent], func()) {
			return nil, func() {}
		},
		JobLookup: func(id string) (native.JobInfo, bool) {
			return native.JobInfo{ID: id}, true
		},
	})
	reg := registerTools(t, m)

	res, err := invoke(t, reg, "watch.start", `{"name":"j","kind":"job","job_id":"job-1"}`)
	if err != nil {
		t.Fatalf("watch.start job: %v", err)
	}
	if !strings.Contains(res.Content, "watch_id: w1") {
		t.Fatalf("content = %q, want watch_id: w1", res.Content)
	}
}

func TestWatchStatusAndStop(t *testing.T) {
	m := newToolsManager(t, Deps{
		RunSample: fakeRunSample("hello", 0),
	})
	reg := registerTools(t, m)

	res, err := invoke(t, reg, "watch.start", `{"name":"s","kind":"command","command":"echo hi"}`)
	if err != nil {
		t.Fatalf("watch.start: %v", err)
	}
	id := strings.TrimPrefix(strings.Split(res.Content, "\n")[0], "watch_id: ")

	// Status.
	status, err := invoke(t, reg, "watch.status", `{"watch_id":"`+id+`"}`)
	if err != nil {
		t.Fatalf("watch.status: %v", err)
	}
	if !strings.Contains(status.Content, "state: watching") {
		t.Fatalf("status content = %q, want state: watching", status.Content)
	}

	// Stop.
	stop, err := invoke(t, reg, "watch.stop", `{"watch_id":"`+id+`"}`)
	if err != nil {
		t.Fatalf("watch.stop: %v", err)
	}
	if !strings.Contains(stop.Content, "stopped") {
		t.Fatalf("stop content = %q, want stopped", stop.Content)
	}

	// Idempotent stop of unknown ID succeeds with a note.
	stop2, err := invoke(t, reg, "watch.stop", `{"watch_id":"w999"}`)
	if err != nil {
		t.Fatalf("watch.stop unknown: %v", err)
	}
	if !strings.Contains(stop2.Content, "was already gone") {
		t.Fatalf("stop unknown content = %q, want was already gone", stop2.Content)
	}

	// Status of unknown ID errors.
	if _, err := invoke(t, reg, "watch.status", `{"watch_id":"w999"}`); err == nil {
		t.Fatal("expected watch.status error for unknown ID")
	}
}

func TestWatchListEmpty(t *testing.T) {
	m := newToolsManager(t, Deps{})
	reg := registerTools(t, m)

	res, err := invoke(t, reg, "watch.list", `{}`)
	if err != nil {
		t.Fatalf("watch.list: %v", err)
	}
	if !strings.Contains(res.Content, "no watches") {
		t.Fatalf("watch.list content = %q, want no watches", res.Content)
	}
}
