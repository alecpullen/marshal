package watch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"marshal/internal/pubsub"
	"marshal/internal/tools/native"
)

// ---------------------------------------------------------------------------
// Command source tests
// ---------------------------------------------------------------------------

// scriptedRunSample is a controllable fake for Deps.RunSample.
type scriptedRunSample struct {
	mu      sync.Mutex
	samples []runSampleStep
	idx     int
	dirs    []string
}

type runSampleStep struct {
	stdout string
	exit   int
	err    error
}

func (s *scriptedRunSample) Run(ctx context.Context, command, dir string) (string, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dirs = append(s.dirs, dir)
	if s.idx < len(s.samples) {
		step := s.samples[s.idx]
		s.idx++
		return step.stdout, step.exit, step.err
	}
	// Exhausted: repeat the last scripted sample so a "static" source stays
	// static (no spurious change fires).
	if len(s.samples) > 0 {
		last := s.samples[len(s.samples)-1]
		return last.stdout, last.exit, last.err
	}
	return "", 0, nil
}

func (s *scriptedRunSample) dirsSeen() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.dirs...)
}

func newCommandManager(t *testing.T, rs *scriptedRunSample, deps Deps) *Manager {
	t.Helper()
	if deps.RunSample == nil {
		deps.RunSample = rs.Run
	}
	return newTestManager(t, deps)
}

func TestCommandSourceChangeFiresOncePerChange(t *testing.T) {
	var mu sync.Mutex
	var reports []Report
	rs := &scriptedRunSample{samples: []runSampleStep{
		{stdout: "a"}, {stdout: "b"}, {stdout: "b"},
	}}
	m := newCommandManager(t, rs, Deps{OnFire: func(r Report) {
		mu.Lock()
		reports = append(reports, r)
		mu.Unlock()
	}})
	id, _, err := m.Start(Spec{Name: "x", Kind: KindCommand, Condition: "change", Mode: ModeRepeat})
	if err != nil {
		t.Fatal(err)
	}
	w := m.getWatch(id)
	m.sampleOnce(w) // baseline "a"
	m.sampleOnce(w) // "b" -> fire
	m.sampleOnce(w) // "b" static -> no fire

	mu.Lock()
	n := len(reports)
	mu.Unlock()
	if n != 1 {
		t.Fatalf("reports = %d, want 1", n)
	}
}

func TestCommandSourceStaticRepeatNeverFires(t *testing.T) {
	var mu sync.Mutex
	var reports []Report
	rs := &scriptedRunSample{samples: []runSampleStep{{stdout: "same"}}}
	m := newCommandManager(t, rs, Deps{OnFire: func(r Report) {
		mu.Lock()
		reports = append(reports, r)
		mu.Unlock()
	}})
	id, _, err := m.Start(Spec{Name: "x", Kind: KindCommand, Condition: "change", Mode: ModeRepeat})
	if err != nil {
		t.Fatal(err)
	}
	w := m.getWatch(id)
	m.sampleOnce(w)
	m.sampleOnce(w)
	m.sampleOnce(w)

	mu.Lock()
	n := len(reports)
	mu.Unlock()
	if n != 0 {
		t.Fatalf("reports = %d, want 0", n)
	}
}

func TestCommandSourceExitCodeNonZeroIsData(t *testing.T) {
	var mu sync.Mutex
	var reports []Report
	rs := &scriptedRunSample{samples: []runSampleStep{{stdout: "boom", exit: 1}}}
	m := newCommandManager(t, rs, Deps{OnFire: func(r Report) {
		mu.Lock()
		reports = append(reports, r)
		mu.Unlock()
	}})
	id, _, err := m.Start(Spec{Name: "x", Kind: KindCommand, Condition: "exit_code 1"})
	if err != nil {
		t.Fatal(err)
	}
	w := m.getWatch(id)
	m.sampleOnce(w)

	mu.Lock()
	n := len(reports)
	mu.Unlock()
	if n != 1 {
		t.Fatalf("reports = %d, want 1 (non-zero exit is data)", n)
	}
	if reports[0].IsError {
		t.Error("non-zero exit should not produce an error report")
	}
	if reports[0].FiredCount != 1 {
		t.Fatalf("FiredCount = %d, want 1", reports[0].FiredCount)
	}
}

func TestCommandSourceErrorBudgetTrips(t *testing.T) {
	var mu sync.Mutex
	var reports []Report
	rs := &scriptedRunSample{samples: []runSampleStep{
		{err: errors.New("e1")}, {err: errors.New("e2")}, {err: errors.New("e3")},
	}}
	m := newCommandManager(t, rs, Deps{OnFire: func(r Report) {
		mu.Lock()
		reports = append(reports, r)
		mu.Unlock()
	}})
	id, _, err := m.Start(Spec{Name: "x", Kind: KindCommand})
	if err != nil {
		t.Fatal(err)
	}
	w := m.getWatch(id)
	m.sampleOnce(w)
	m.sampleOnce(w)
	m.sampleOnce(w)

	mu.Lock()
	n := len(reports)
	mu.Unlock()
	if n != 1 {
		t.Fatalf("error reports = %d, want 1", n)
	}
	if !reports[0].IsError {
		t.Error("error report should mark IsError")
	}
	if _, err := m.Status(id); err == nil {
		t.Error("auto-stopped watch should be removed")
	}
}

func TestCommandSourceErrorBudgetResetsOnSuccess(t *testing.T) {
	var mu sync.Mutex
	var reports []Report
	rs := &scriptedRunSample{samples: []runSampleStep{
		{err: errors.New("e1")}, {err: errors.New("e2")}, {stdout: "ok"},
		{err: errors.New("e3")}, {err: errors.New("e4")},
	}}
	m := newCommandManager(t, rs, Deps{OnFire: func(r Report) {
		mu.Lock()
		reports = append(reports, r)
		mu.Unlock()
	}})
	id, _, err := m.Start(Spec{Name: "x", Kind: KindCommand})
	if err != nil {
		t.Fatal(err)
	}
	w := m.getWatch(id)
	m.sampleOnce(w) // e1
	m.sampleOnce(w) // e2
	m.sampleOnce(w) // success -> resets
	m.sampleOnce(w) // e3
	m.sampleOnce(w) // e4

	mu.Lock()
	n := len(reports)
	mu.Unlock()
	if n != 0 {
		t.Fatalf("error reports = %d, want 0 (budget is consecutive)", n)
	}
	if _, err := m.Status(id); err != nil {
		t.Fatalf("watch should remain registered: %v", err)
	}
}

func TestCommandSourceDirFn(t *testing.T) {
	rs := &scriptedRunSample{samples: []runSampleStep{{stdout: "out"}}}
	m := newCommandManager(t, rs, Deps{DirFn: func() string { return "/tmp/foo" }})
	id, _, err := m.Start(Spec{Name: "x", Kind: KindCommand})
	if err != nil {
		t.Fatal(err)
	}
	w := m.getWatch(id)
	m.sampleOnce(w)
	dirs := rs.dirsSeen()
	if len(dirs) != 1 || dirs[0] != "/tmp/foo" {
		t.Fatalf("dirs = %v, want [/tmp/foo]", dirs)
	}
}

// ---------------------------------------------------------------------------
// File source tests
// ---------------------------------------------------------------------------

func TestFileSourceAppearChangeDisappear(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data.txt")
	var mu sync.Mutex
	var reports []Report
	m := newTestManager(t, Deps{OnFire: func(r Report) {
		mu.Lock()
		reports = append(reports, r)
		mu.Unlock()
	}})
	id, _, err := m.Start(Spec{Name: "f", Kind: KindFile, Path: path, Condition: "change", Mode: ModeRepeat})
	if err != nil {
		t.Fatal(err)
	}
	w := m.getWatch(id)

	// Baseline: file absent.
	m.sampleOnce(w)
	mu.Lock()
	n0 := len(reports)
	mu.Unlock()
	if n0 != 0 {
		t.Fatalf("baseline fired %d times, want 0", n0)
	}

	// Appear.
	if err := os.WriteFile(path, []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	m.sampleOnce(w)
	mu.Lock()
	n1 := len(reports)
	mu.Unlock()
	if n1 != 1 {
		t.Fatalf("appear fired %d times, want 1", n1)
	}

	// Change (size changes).
	if err := os.WriteFile(path, []byte("bb"), 0o644); err != nil {
		t.Fatal(err)
	}
	m.sampleOnce(w)
	mu.Lock()
	n2 := len(reports)
	mu.Unlock()
	if n2 != 2 {
		t.Fatalf("change fired %d times, want 2", n2)
	}

	// Disappear.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	m.sampleOnce(w)
	mu.Lock()
	n3 := len(reports)
	mu.Unlock()
	if n3 != 3 {
		t.Fatalf("disappear fired %d times, want 3", n3)
	}
}

func TestFileSourceGlobAppears(t *testing.T) {
	dir := t.TempDir()
	glob := filepath.Join(dir, "*.txt")
	var mu sync.Mutex
	var reports []Report
	m := newTestManager(t, Deps{OnFire: func(r Report) {
		mu.Lock()
		reports = append(reports, r)
		mu.Unlock()
	}})
	id, _, err := m.Start(Spec{Name: "g", Kind: KindFile, Path: glob, Condition: "change", Mode: ModeRepeat})
	if err != nil {
		t.Fatal(err)
	}
	w := m.getWatch(id)

	// Baseline: no match.
	m.sampleOnce(w)
	mu.Lock()
	n0 := len(reports)
	mu.Unlock()
	if n0 != 0 {
		t.Fatalf("baseline fired %d times, want 0", n0)
	}

	// A match appears.
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	m.sampleOnce(w)
	mu.Lock()
	n1 := len(reports)
	mu.Unlock()
	if n1 != 1 {
		t.Fatalf("glob appear fired %d times, want 1", n1)
	}
}

// ---------------------------------------------------------------------------
// Firing semantics
// ---------------------------------------------------------------------------

func TestOnceAutoRemoves(t *testing.T) {
	var mu sync.Mutex
	var reports []Report
	rs := &scriptedRunSample{samples: []runSampleStep{{stdout: "x", exit: 0}}}
	m := newCommandManager(t, rs, Deps{OnFire: func(r Report) {
		mu.Lock()
		reports = append(reports, r)
		mu.Unlock()
	}})
	id, _, err := m.Start(Spec{Name: "x", Kind: KindCommand, Condition: "exit_code 0"})
	if err != nil {
		t.Fatal(err)
	}
	w := m.getWatch(id)
	m.sampleOnce(w)

	mu.Lock()
	n := len(reports)
	mu.Unlock()
	if n != 1 {
		t.Fatalf("reports = %d, want 1", n)
	}
	if !reports[0].AutoRemoved {
		t.Error("once-mode report should mark AutoRemoved")
	}
	if reports[0].FiredCount != 1 {
		t.Fatalf("FiredCount = %d, want 1", reports[0].FiredCount)
	}
	if _, err := m.Status(id); err == nil {
		t.Error("once-mode watch should be removed after firing")
	}
}

func TestRepeatFiresThenStop(t *testing.T) {
	var mu sync.Mutex
	var reports []Report
	rs := &scriptedRunSample{samples: []runSampleStep{
		{stdout: "a", exit: 0}, {stdout: "b", exit: 0}, {stdout: "c", exit: 0},
	}}
	m := newCommandManager(t, rs, Deps{OnFire: func(r Report) {
		mu.Lock()
		reports = append(reports, r)
		mu.Unlock()
	}})
	id, _, err := m.Start(Spec{Name: "x", Kind: KindCommand, Condition: "exit_code 0", Mode: ModeRepeat})
	if err != nil {
		t.Fatal(err)
	}
	w := m.getWatch(id)
	m.sampleOnce(w)
	m.sampleOnce(w)
	m.sampleOnce(w)

	mu.Lock()
	n := len(reports)
	mu.Unlock()
	if n != 3 {
		t.Fatalf("reports = %d, want 3", n)
	}
	if _, err := m.Stop(id); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Status(id); err == nil {
		t.Error("stopped watch should be removed")
	}
}

func TestNotifyFalseSuppressesReports(t *testing.T) {
	var mu sync.Mutex
	var reports []Report
	notify := false
	rs := &scriptedRunSample{samples: []runSampleStep{{stdout: "x", exit: 0}}}
	m := newCommandManager(t, rs, Deps{OnFire: func(r Report) {
		mu.Lock()
		reports = append(reports, r)
		mu.Unlock()
	}})
	id, _, err := m.Start(Spec{Name: "x", Kind: KindCommand, Condition: "exit_code 0", Mode: ModeRepeat, Notify: &notify})
	if err != nil {
		t.Fatal(err)
	}
	w := m.getWatch(id)
	m.sampleOnce(w)

	mu.Lock()
	n := len(reports)
	mu.Unlock()
	if n != 0 {
		t.Fatalf("reports = %d, want 0 (notify=false)", n)
	}
	info, err := m.Status(id)
	if err != nil {
		t.Fatal(err)
	}
	if info.FireCount != 1 {
		t.Fatalf("FireCount = %d, want 1", info.FireCount)
	}
}

// ---------------------------------------------------------------------------
// Job source tests
// ---------------------------------------------------------------------------

// fakeJobRunner is a controllable native.CommandRunner for job source tests.
type fakeJobRunner struct {
	mu      sync.Mutex
	release chan struct{}
	result  native.CommandResult
	runErr  error
}

func (f *fakeJobRunner) Run(ctx context.Context, req native.CommandRequest) (native.CommandResult, error) {
	if req.OnStart != nil {
		req.OnStart(4242)
	}
	if f.release != nil {
		select {
		case <-f.release:
		case <-ctx.Done():
		}
	}
	return f.result, f.runErr
}

// newJobManager builds a native.JobManager with a fake runner and a broker.
func newJobManager(t *testing.T, runner *fakeJobRunner) (*native.JobManager, *pubsub.Broker[native.JobEvent]) {
	t.Helper()
	jm := native.NewJobManager(context.Background(), runner, t.TempDir(), 25, time.Hour, 100000)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = jm.Shutdown(ctx)
	})
	broker := pubsub.NewBroker[native.JobEvent]()
	t.Cleanup(broker.Close)
	jm.SetBroker(broker)
	return jm, broker
}

func TestJobSourceAlreadyFinishedFiresImmediately(t *testing.T) {
	runner := &fakeJobRunner{result: native.CommandResult{ExitCode: 0}}
	jm, broker := newJobManager(t, runner)

	// Start a job that completes immediately.
	jobID, err := jm.Start(context.Background(), "echo done", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	// Wait for it to complete.
	waitJobStatus(t, jm, jobID, native.StatusRunning, 2*time.Second)

	var mu sync.Mutex
	var reports []Report
	m := newTestManager(t, Deps{
		JobLookup: func(id string) (native.JobInfo, bool) {
			info, _, err := jm.Output(id, 0)
			if err != nil {
				return native.JobInfo{}, false
			}
			return info, true
		},
		SubscribeJobs: func(ctx context.Context) (<-chan pubsub.Event[native.JobEvent], func()) {
			subCtx, cancel := context.WithCancel(ctx)
			return broker.Subscribe(subCtx), cancel
		},
		OnFire: func(r Report) {
			mu.Lock()
			reports = append(reports, r)
			mu.Unlock()
		},
	})
	if _, _, err := m.Start(Spec{Name: "job", Kind: KindJob, JobID: jobID}); err != nil {
		t.Fatal(err)
	}

	// The synchronous initial check fires immediately.
	deadline := time.After(2 * time.Second)
	for {
		mu.Lock()
		n := len(reports)
		mu.Unlock()
		if n >= 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("already-finished job did not fire within 2s")
		case <-time.After(10 * time.Millisecond):
		}
	}
	mu.Lock()
	if !strings.Contains(reports[0].Sample, "status=completed") {
		t.Fatalf("sample = %q, want status=completed", reports[0].Sample)
	}
	mu.Unlock()
}

func TestJobSourceRunningFiresOnCompletion(t *testing.T) {
	release := make(chan struct{})
	runner := &fakeJobRunner{release: release, result: native.CommandResult{ExitCode: 0}}
	jm, broker := newJobManager(t, runner)

	// Start a job that blocks.
	jobID, err := jm.Start(context.Background(), "sleep 30", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}

	subscribed := make(chan struct{})
	var mu sync.Mutex
	var reports []Report
	m := newTestManager(t, Deps{
		JobLookup: func(id string) (native.JobInfo, bool) {
			info, _, err := jm.Output(id, 0)
			if err != nil {
				return native.JobInfo{}, false
			}
			return info, true
		},
		SubscribeJobs: func(ctx context.Context) (<-chan pubsub.Event[native.JobEvent], func()) {
			subCtx, cancel := context.WithCancel(ctx)
			ch := broker.Subscribe(subCtx)
			close(subscribed)
			return ch, cancel
		},
		OnFire: func(r Report) {
			mu.Lock()
			reports = append(reports, r)
			mu.Unlock()
		},
	})
	if _, _, err := m.Start(Spec{Name: "job", Kind: KindJob, JobID: jobID}); err != nil {
		t.Fatal(err)
	}

	// Wait for the watch to subscribe, then release the runner.
	select {
	case <-subscribed:
	case <-time.After(2 * time.Second):
		t.Fatal("watch did not subscribe within 2s")
	}
	close(release)

	// The job completes -> event -> fire.
	deadline := time.After(2 * time.Second)
	for {
		mu.Lock()
		n := len(reports)
		mu.Unlock()
		if n >= 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("running job did not fire on completion within 2s")
		case <-time.After(10 * time.Millisecond):
		}
	}
	mu.Lock()
	if !strings.Contains(reports[0].Sample, "status=completed") {
		t.Fatalf("sample = %q, want status=completed", reports[0].Sample)
	}
	mu.Unlock()
}

// waitJobStatus polls jm.Output until the job's status is no longer want.
func waitJobStatus(t *testing.T, jm *native.JobManager, id string, want native.JobStatus, timeout time.Duration) {
	t.Helper()
	deadline := time.After(timeout)
	for {
		info, _, err := jm.Output(id, 0)
		if err != nil {
			t.Fatalf("Output(%q): %v", id, err)
		}
		if info.Status != want {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for status change from %q", want)
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
}
