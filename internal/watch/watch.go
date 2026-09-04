package watch

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"marshal/internal/pubsub"
	"marshal/internal/tools/native"
)

// ErrManagerClosed is returned by Start when the manager has been shut down.
var ErrManagerClosed = errors.New("watch manager is closed")

// Limits (v1 constants, not config).
const (
	// MaxWatches is the maximum number of concurrently registered watches.
	MaxWatches = 10
	// MinInterval is the floor for poll intervals. Below-floor intervals are
	// clamped with a note, not rejected.
	MinInterval = 2 * time.Second
	// MaxConsecutiveErrors is the consecutive-error budget. Exceeding it
	// auto-stops the watch and enqueues an error report.
	MaxConsecutiveErrors = 3
	// SampleTailCap is the byte cap for stored sample tails.
	SampleTailCap = 2048
)

// Kind identifies the source type.
type Kind string

const (
	KindCommand Kind = "command"
	KindJob     Kind = "job"
	KindFile    Kind = "file"
)

// Mode describes how a watch behaves once its condition trips.
type Mode string

const (
	// ModeOnce is the default: fire once, auto-remove, enqueue one report.
	ModeOnce Mode = "once"
	// ModeRepeat fires on every trigger until stopped.
	ModeRepeat Mode = "repeat"
)

// State is the lifecycle state of a watch.
type State string

const (
	StateWatching State = "watching"
	StateFired    State = "fired"
	StateStopped  State = "stopped"
	StateError    State = "error"
)

// Spec is the registration payload (decoded from watch.start args).
type Spec struct {
	Name      string
	Kind      Kind
	Command   string // KindCommand
	JobID     string // KindJob
	Path      string // KindFile — file path or glob
	Condition string // e.g. "change" (default), "exit_code 0", "regex ...", "json <path> <op> <value>"
	Mode      Mode
	Notify    *bool         // repeat-mode only; default true
	Interval  time.Duration // command/file; clamped to floor
	Owner     string        // "" (parent) or subagent tag
}

// Info is the snapshot returned by List/Status.
type Info struct {
	ID          string
	Name        string
	Kind        Kind
	State       State
	Condition   string
	Mode        Mode
	Interval    time.Duration
	Owner       string
	FireCount   int
	LastSample  string // tail-capped
	LastError   string
	CreatedAt   time.Time
	LastFiredAt time.Time
}

// Report is the payload enqueued to the session queue when a watch fires.
// Task 3 wires OnFire to the session's WatchReports queue.
type Report struct {
	WatchID   string
	Name      string
	Kind      Kind
	Condition string
	Mode      Mode
	Interval  time.Duration
	Sample    string // tail-capped
	Fired     int
	Owner     string
	Removed   bool // once-mode auto-removal
}

// Event is the pubsub payload for the TUI lane. Task 5 wires OnEvent to the
// broker.
type Event struct {
	WatchID string
	Name    string
	Kind    Kind
	State   State
	Sample  string // tail-capped
}

// Deps carries the injected seams the Manager's sources consume. Task 1 ships
// a stub sampler that returns a parse-level error; task 2 implements real
// samplers on top of these seams.
type Deps struct {
	// RunSample runs a command source sample. Production wires the sandbox
	// runner; tests inject a fake. dir is the working directory for the
	// command (captured by the caller's closure).
	RunSample func(ctx context.Context, command string, dir string) (stdout string, exitCode int, err error)
	// SubscribeJobs subscribes to the job event stream. It returns a receive
	// channel and a cleanup func. Task 2's job source consumes it.
	SubscribeJobs func(ctx context.Context) (<-chan pubsub.Event[native.JobEvent], func())
	// JobLookup resolves a job ID to its metadata. Used to validate job
	// watches at registration and to read terminal state.
	JobLookup func(id string) (native.JobInfo, bool)
	// OnFire enqueues a fired report. Task 3 wires it to the session queue.
	OnFire func(Report)
	// OnEvent publishes a watch event for the TUI lane. Task 5 wires the
	// broker.
	OnEvent func(Event)
}

// sampler produces a Sample for a watch on each evaluation. The Manager's
// loop calls Sample on each tick. Task 1 ships a stub sampler that returns a
// parse-level error; task 2 replaces it with real source samplers. Tests
// drive the loop by injecting a fake sampler.
type sampler interface {
	Sample(ctx context.Context, w *watch) (Sample, error)
}

// stubSampler is the task-1 placeholder. It returns a parse-level error so
// the loop runs but no real sampling occurs. Task 2 replaces it with real
// source samplers.
type stubSampler struct{}

func (stubSampler) Sample(ctx context.Context, w *watch) (Sample, error) {
	return Sample{}, fmt.Errorf("watch source %q not implemented until task 2", w.kind)
}

// Manager tracks registered watches, enforces caps, and runs one goroutine
// per watch. It is shaped after internal/tools/native.JobManager.
type Manager struct {
	mu            sync.Mutex
	watches       map[string]*watch
	nextID        int
	closed        bool
	wg            sync.WaitGroup
	managerCtx    context.Context
	managerCancel context.CancelFunc
	deps          Deps
	sampler       sampler
}

// watch is the internal per-watch state.
type watch struct {
	mu                sync.Mutex
	id                string
	name              string
	kind              Kind
	command           string
	jobID             string
	path              string
	cond              condition
	condRaw           string
	mode              Mode
	notify            bool
	interval          time.Duration
	owner             string
	state             State
	fireCount         int
	lastSample        string
	lastError         string
	prev              Sample
	hasPrev           bool
	createdAt         time.Time
	lastFiredAt       time.Time
	consecutiveErrors int
	ctx               context.Context
	cancel            context.CancelFunc
}

// NewManager creates a watch manager. The manager derives its lifetime from
// ctx: when the parent context is cancelled all watch goroutines are
// cancelled. deps carries the injected seams; a zero Deps is allowed and
// yields a manager whose sources are stubbed (task 1).
func NewManager(ctx context.Context, deps Deps) *Manager {
	managerCtx, managerCancel := context.WithCancel(ctx)
	return &Manager{
		watches:       make(map[string]*watch),
		managerCtx:    managerCtx,
		managerCancel: managerCancel,
		deps:          deps,
		sampler:       stubSampler{},
	}
}

// setSampler replaces the source sampler. Test-only; production uses the
// stub until task 2 wires real samplers.
func (m *Manager) setSampler(s sampler) {
	m.mu.Lock()
	m.sampler = s
	m.mu.Unlock()
}

// getWatch returns the internal watch for a given ID. Test-only.
func (m *Manager) getWatch(id string) *watch {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.watches[id]
}

// Start registers a watch and returns its ID, an optional note (e.g. an
// interval clamp), and an error. It rejects a closed manager, an unknown
// kind, an unparseable condition, a job watch with an unknown job ID, and a
// watch when the cap is reached.
func (m *Manager) Start(spec Spec) (string, string, error) {
	switch spec.Kind {
	case KindCommand, KindJob, KindFile:
	default:
		return "", "", fmt.Errorf("unknown watch kind %q", spec.Kind)
	}

	// Parse the condition. Job watches have an implicit terminal-state
	// condition and reject a user-supplied one.
	var cond condition
	if spec.Kind == KindJob {
		if strings.TrimSpace(spec.Condition) != "" {
			return "", "", fmt.Errorf("job watch %q: user-supplied condition not allowed (job source has an implicit terminal-state condition)", spec.Name)
		}
	} else {
		var err error
		cond, err = parseCondition(spec.Condition)
		if err != nil {
			return "", "", err
		}
	}

	// Validate the job ID for job watches.
	if spec.Kind == KindJob {
		if m.deps.JobLookup == nil {
			return "", "", fmt.Errorf("job watch %q: job lookup not configured", spec.Name)
		}
		if _, ok := m.deps.JobLookup(spec.JobID); !ok {
			return "", "", fmt.Errorf("job watch %q: unknown job ID %q", spec.Name, spec.JobID)
		}
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return "", "", ErrManagerClosed
	}
	if err := m.managerCtx.Err(); err != nil {
		m.mu.Unlock()
		return "", "", err
	}

	// Enforce the cap.
	if len(m.watches) >= MaxWatches {
		m.mu.Unlock()
		return "", "", m.capError()
	}

	// Clamp the interval below the floor with a note.
	note := ""
	interval := spec.Interval
	if interval < MinInterval {
		interval = MinInterval
		note = fmt.Sprintf("interval %s below floor; clamped to %s", spec.Interval, MinInterval)
	}

	// Defaults.
	mode := spec.Mode
	if mode == "" {
		mode = ModeOnce
	}
	notify := true
	if spec.Notify != nil {
		notify = *spec.Notify
	}

	// Dedup the name.
	name := m.dedupName(spec.Name)

	// Assign the ID.
	m.nextID++
	id := fmt.Sprintf("w%d", m.nextID)

	watchCtx, watchCancel := context.WithCancel(m.managerCtx)
	w := &watch{
		id:        id,
		name:      name,
		kind:      spec.Kind,
		command:   spec.Command,
		jobID:     spec.JobID,
		path:      spec.Path,
		cond:      cond,
		condRaw:   spec.Condition,
		mode:      mode,
		notify:    notify,
		interval:  interval,
		owner:     spec.Owner,
		state:     StateWatching,
		createdAt: time.Now(),
		ctx:       watchCtx,
		cancel:    watchCancel,
	}
	m.watches[id] = w
	// Increment wg under the lock so Close either sees the counted goroutine
	// or rejects the Start outright (mirrors JobManager.Start).
	m.wg.Add(1)
	m.mu.Unlock()

	go m.runWatch(w)
	return id, note, nil
}

// runWatch dispatches to the source-specific loop.
func (m *Manager) runWatch(w *watch) {
	defer m.wg.Done()
	if w.kind == KindJob {
		m.runJobWatch(w)
		return
	}
	m.runPollWatch(w)
}

// runPollWatch is the interval-poll loop for command/file sources.
func (m *Manager) runPollWatch(w *watch) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-m.managerCtx.Done():
			return
		case <-w.ctx.Done():
			return
		case <-ticker.C:
			m.sampleOnce(w)
		}
	}
}

// runJobWatch is the event-driven loop for job sources. Task 1 does not wire
// the job source: it samples once through the sampler (the stub returns a
// parse-level error) and then waits for shutdown. Task 2 replaces this with a
// real loop over SubscribeJobs.
func (m *Manager) runJobWatch(w *watch) {
	m.sampleOnce(w)
	select {
	case <-m.managerCtx.Done():
	case <-w.ctx.Done():
	}
}

// sampleOnce performs one evaluation: sample the source, record the result,
// and fire if the condition trips. It is the loop's core and is directly
// testable.
func (m *Manager) sampleOnce(w *watch) {
	sample, err := m.sampler.Sample(m.managerCtx, w)
	if err != nil {
		m.handleSampleError(w, err)
		return
	}
	w.mu.Lock()
	prev := w.prev
	hasPrev := w.hasPrev
	w.prev = sample
	w.hasPrev = true
	w.lastSample = tailCap(sample.Stdout)
	// A successful sample resets the consecutive-error budget and, if the
	// watch was in the error state, returns it to watching. Fired/stopped
	// watches are never resurrected.
	w.consecutiveErrors = 0
	if w.state == StateError {
		w.state = StateWatching
	}
	w.mu.Unlock()
	if w.cond == nil {
		return
	}
	// The change condition needs a baseline; skip the first sample so a
	// static source never fires on registration (design: repeat+change+
	// static source -> no fires).
	if _, isChange := w.cond.(changeCondition); isChange && !hasPrev {
		return
	}
	if w.cond.Eval(sample, prev) {
		m.fire(w, sample)
	}
}

// handleSampleError records a sampling error and auto-stops the watch once
// the consecutive-error budget is exceeded.
func (m *Manager) handleSampleError(w *watch, err error) {
	w.mu.Lock()
	w.consecutiveErrors++
	w.lastError = err.Error()
	w.state = StateError
	consecutive := w.consecutiveErrors
	w.mu.Unlock()
	m.publishEvent(w, StateError, "")

	if consecutive >= MaxConsecutiveErrors {
		reason := fmt.Sprintf("stopped after %d consecutive errors: %v", MaxConsecutiveErrors, err)
		m.autoStop(w, reason)
	}
}

// autoStop removes a watch and enqueues an error report.
func (m *Manager) autoStop(w *watch, reason string) {
	w.mu.Lock()
	if w.state == StateStopped || w.state == StateFired {
		w.mu.Unlock()
		return
	}
	w.state = StateStopped
	w.lastError = reason
	w.mu.Unlock()
	m.removeWatch(w.id)
	m.publishEvent(w, StateStopped, "")
	if m.deps.OnFire != nil {
		m.deps.OnFire(Report{
			WatchID:   w.id,
			Name:      w.name,
			Kind:      w.kind,
			Condition: w.condRaw,
			Mode:      w.mode,
			Interval:  w.interval,
			Owner:     w.owner,
			Removed:   true,
		})
	}
}

// fire records a fired state, enqueues a report, and (for once mode)
// auto-removes the watch.
func (m *Manager) fire(w *watch, sample Sample) {
	w.mu.Lock()
	w.fireCount++
	w.lastFiredAt = time.Now()
	w.lastSample = tailCap(sample.Stdout)
	removed := false
	if w.mode == ModeOnce {
		w.state = StateFired
		removed = true
	}
	notify := w.notify
	mode := w.mode
	w.mu.Unlock()

	m.publishEvent(w, StateFired, sample.Stdout)
	if removed {
		m.removeWatch(w.id)
	}
	if notify && m.deps.OnFire != nil {
		m.deps.OnFire(Report{
			WatchID:   w.id,
			Name:      w.name,
			Kind:      w.kind,
			Condition: w.condRaw,
			Mode:      mode,
			Interval:  w.interval,
			Sample:    tailCap(sample.Stdout),
			Fired:     1,
			Owner:     w.owner,
			Removed:   removed,
		})
	}
}

// Stop idempotently stops a watch. Unknown or already-terminal IDs return
// success with a "was already gone" note (mirrors job.kill conventions).
func (m *Manager) Stop(id string) (string, error) {
	m.mu.Lock()
	w, ok := m.watches[id]
	m.mu.Unlock()
	if !ok {
		return "was already gone", nil
	}
	w.mu.Lock()
	if w.state == StateStopped || w.state == StateFired {
		w.mu.Unlock()
		return "was already gone", nil
	}
	w.state = StateStopped
	w.mu.Unlock()
	m.removeWatch(id)
	m.publishEvent(w, StateStopped, "")
	return "", nil
}

// List returns a snapshot of all watches, sorted by CreatedAt then ID
// (stable, mirroring notifyChange's sort).
func (m *Manager) List() []Info {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Info, 0, len(m.watches))
	for _, w := range m.watches {
		out = append(out, w.snapshot())
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Status returns the snapshot for a single watch.
func (m *Manager) Status(id string) (Info, error) {
	m.mu.Lock()
	w, ok := m.watches[id]
	m.mu.Unlock()
	if !ok {
		return Info{}, fmt.Errorf("watch %q not found", id)
	}
	return w.snapshot(), nil
}

// Close cancels all watch goroutines and waits for them to finish, respecting
// the caller's context deadline. It is idempotent (mirrors
// JobManager.Shutdown).
func (m *Manager) Close(ctx context.Context) error {
	m.mu.Lock()
	if !m.closed {
		m.closed = true
		m.managerCancel()
	}
	m.mu.Unlock()

	done := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// publishEvent emits a watch event to the TUI lane via the injected OnEvent
// seam, if configured.
func (m *Manager) publishEvent(w *watch, state State, sample string) {
	if m.deps.OnEvent == nil {
		return
	}
	m.deps.OnEvent(Event{
		WatchID: w.id,
		Name:    w.name,
		Kind:    w.kind,
		State:   state,
		Sample:  tailCap(sample),
	})
}

// removeWatch deletes a watch from the map and cancels its goroutine.
func (m *Manager) removeWatch(id string) {
	m.mu.Lock()
	w, ok := m.watches[id]
	if ok {
		delete(m.watches, id)
	}
	m.mu.Unlock()
	if ok && w.cancel != nil {
		w.cancel()
	}
}

// dedupName returns a name that is not already taken, suffixing -2, -3, ...
// (mirrors subagent label dedup conventions).
func (m *Manager) dedupName(base string) string {
	if strings.TrimSpace(base) == "" {
		base = "watch"
	}
	name := base
	for i := 2; ; i++ {
		taken := false
		for _, w := range m.watches {
			if w.name == name {
				taken = true
				break
			}
		}
		if !taken {
			return name
		}
		name = fmt.Sprintf("%s-%d", base, i)
	}
}

// capError builds the error listing active watches when the cap is reached.
func (m *Manager) capError() error {
	ids := make([]string, 0, len(m.watches))
	names := make([]string, 0, len(m.watches))
	for _, w := range m.watches {
		ids = append(ids, w.id)
		names = append(names, w.name)
	}
	sort.Strings(ids)
	sort.Strings(names)
	return fmt.Errorf("watch cap reached (max %d); active: %s (%s)", MaxWatches, strings.Join(ids, ", "), strings.Join(names, ", "))
}

// snapshot returns a copy of the watch's public state.
func (w *watch) snapshot() Info {
	w.mu.Lock()
	defer w.mu.Unlock()
	return Info{
		ID:          w.id,
		Name:        w.name,
		Kind:        w.kind,
		State:       w.state,
		Condition:   w.condRaw,
		Mode:        w.mode,
		Interval:    w.interval,
		Owner:       w.owner,
		FireCount:   w.fireCount,
		LastSample:  w.lastSample,
		LastError:   w.lastError,
		CreatedAt:   w.createdAt,
		LastFiredAt: w.lastFiredAt,
	}
}

// tailCap truncates a string to the last SampleTailCap bytes.
func tailCap(s string) string {
	if len(s) <= SampleTailCap {
		return s
	}
	return s[len(s)-SampleTailCap:]
}
