package watch

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"marshal/internal/progress"
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
	Notify    *bool // repeat-mode only; default true
	// Resume opts the watch into session auto-resume: a fire on an idle
	// session starts a new turn (gated by [watch] resume_enabled at wake
	// time). Start forces Notify on and drops Resume for owned
	// (subagent) watches.
	Resume   bool
	Interval time.Duration // command/file; clamped to floor
	Owner    string        // "" (parent) or subagent tag
}

// TerminalReason values pinned by the plan. A terminal watch's disposition is
// one of these; human diagnostics stay in Info.LastError.
const (
	terminalReasonCompleted = "completed"
	terminalReasonStopped   = "stopped"
	terminalReasonFailed    = "failed"
	terminalReasonShutdown  = "shutdown"
)

// WorkFacts is the source-membership projection embedded in Info and Event. It
// names the manager epoch, the lifecycle/membership revision the fact was read
// at, whether the registration is still active, and — once terminal — when it
// ended, why, and the canonical stable result revision.
type WorkFacts struct {
	Epoch          string
	Revision       uint64
	Active         bool
	EndedAt        time.Time
	TerminalReason string
	ResultRevision string
}

// Info is the snapshot returned by List/Status.
type Info struct {
	WorkFacts
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
	// Samples is the watch's recent sample history, oldest first, capped at
	// MaxSamples. It is a copy; callers may modify it freely.
	Samples []SamplePoint
}

// clone returns a deep copy of the Info, including its sample ring, so a
// caller never shares mutable state with the manager.
func (in Info) clone() Info {
	out := in
	out.Samples = append([]SamplePoint(nil), in.Samples...)
	return out
}

// MaxSamples is how many sample points each watch keeps.
const MaxSamples = 288

// SamplePoint is one evaluated sample: when, a numeric reading (see
// sampleValue), and whether the condition tripped on it.
type SamplePoint struct {
	At      time.Time
	Value   float64
	Tripped bool
}

// Report is the payload enqueued to the session queue when a watch fires.
// Task 3 wires OnFire to the session's WatchReports queue.
type Report struct {
	WatchID     string
	Name        string
	Kind        Kind
	Condition   string
	Mode        Mode
	Interval    time.Duration
	Sample      string // tail-capped
	FiredCount  int
	AutoRemoved bool
	IsError     bool
	Owner       string // "" parent, else subagent tag
	// Resume echoes the watch's resume opt-in: the push site forwards it
	// to the session queue so the turn-end residual drain can arm the
	// auto-resume latch.
	Resume bool
}

// Event is the pubsub payload for the TUI lane.
type Event struct {
	WorkFacts
	WatchID string
	Name    string
	Kind    Kind
	State   State
	Sample  string // tail-capped
	// Owner is the subagent tag ("" for parent watches). Informational:
	// owned watches never carry resume intent (Start drops it at
	// registration), so wake sites need no owner check of their own.
	Owner string
	// Mode is the watch mode; the TUI's idle wake builds the wrapper
	// goal from the event alone (the queue entry is not on this lane).
	Mode Mode
	// Resume is true only when this event corresponds to a pushed
	// report on a resume-opted watch: deliberate stops, deduped repeat
	// fires, and transient sample errors carry false.
	Resume bool
	// Info is a deep copy of the watch's snapshot when this event carries
	// a terminal membership change (once fire, stop, auto-stop, shutdown
	// close); nil otherwise. It is a copy, so a subscriber may mutate it.
	Info *Info
}

// Deps carries the injected seams the Manager's sources consume. Task 1 ships
// a stub sampler that returns a parse-level error; task 2 implements real
// samplers on top of these seams.
type Deps struct {
	// RunSample runs a command source sample. Production wires the sandbox
	// runner; tests inject a fake. dir is the working directory for the
	// command (captured by the caller's closure).
	RunSample func(ctx context.Context, command string, dir string) (stdout string, exitCode int, err error)
	// DirFn resolves the working directory for command source samples at
	// sample time (the session's active root), mirroring
	// JobManager.SetDirFunc. Until set, command samples run in "".
	DirFn func() string
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
	// OnTerminal hands a retained terminal fact to the durable-result sink
	// outside every manager/watch lock. It is optional: when nil, no
	// settling boundary is recorded and a removal is announced in memory
	// only. A failed attempt is recorded for RetryTerminalSaves.
	OnTerminal func(Info) error
	// ConversationID is the owning conversation identity, used with the
	// manager epoch and registration ID to derive a watch's canonical stable
	// result revision. Watch performs no runtime lookup; a standalone fixture
	// may leave it empty.
	ConversationID string
}

// sampler produces a Sample for a watch on each evaluation. The Manager's
// loop calls Sample on each tick. Task 1 ships a stub sampler that returns a
// parse-level error; task 2 replaces it with real source samplers. Tests
// drive the loop by injecting a fake sampler.
type sampler interface {
	Sample(ctx context.Context, w *watch) (Sample, error)
}

// Manager tracks registered watches, enforces caps, and runs one goroutine
// per watch. It is shaped after internal/tools/native.JobManager.
type Manager struct {
	// epoch names this manager's lifetime. It is minted once per manager and
	// never changes, so a consumer can discard facts from a superseded source.
	epoch string

	mu            sync.Mutex
	watches       map[string]*watch
	nextID        int
	closed        bool
	wg            sync.WaitGroup
	managerCtx    context.Context
	managerCancel context.CancelFunc
	deps          Deps
	sampler       sampler
	// revision versions the lifecycle/membership state. It advances under the
	// manager lock on every membership change (admission, reparenting,
	// removal, and the settling/save-error boundary) so a consumer can tell a
	// newer projection from an older one.
	revision uint64
	// terminals retains the terminal facts of removed registrations, keyed by
	// registration ID, so a dropped removal event is repairable from Snapshot.
	terminals map[string]Info
	// settling names the registrations whose initial durable-result sink is in
	// flight. It is recorded before a registration leaves active membership and
	// cleared once the initial callback attempt has returned.
	settling map[string]struct{}
	// saveErrors records the bounded error of a failed durable-result attempt
	// by registration ID, so RetryTerminalSaves can offer it again.
	saveErrors map[string]string
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
	resume            bool
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
	firedThisInterval bool
	// Terminal facts, set under w.mu at the moment the watch leaves active
	// membership. endedAt is the removal time, terminalReason one of the
	// pinned reasons, resultRevision the canonical stable result revision.
	endedAt        time.Time
	terminalReason string
	resultRevision string
	// samples is the ring of the last MaxSamples evaluated samples.
	samples []SamplePoint

	// waiters are signaled (channel closed) whenever the watch reaches a
	// terminal state or fires. Guarded by w.mu; closed-and-cleared, never
	// reused.
	waiters []chan struct{}
	ctx     context.Context
	cancel  context.CancelFunc
}

// NewManager creates a watch manager. The manager derives its lifetime from
// ctx: when the parent context is cancelled all watch goroutines are
// cancelled. deps carries the injected seams; a zero Deps is allowed and
// yields a manager whose sources are stubbed (task 1).
func NewManager(ctx context.Context, deps Deps) *Manager {
	managerCtx, managerCancel := context.WithCancel(ctx)
	return &Manager{
		epoch:         rand.Text(),
		watches:       make(map[string]*watch),
		terminals:     make(map[string]Info),
		settling:      make(map[string]struct{}),
		managerCtx:    managerCtx,
		managerCancel: managerCancel,
		deps:          deps,
		sampler:       sourceSampler{deps: deps},
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
	var notes []string
	interval := spec.Interval
	if interval < MinInterval {
		interval = MinInterval
		notes = append(notes, fmt.Sprintf("interval %s below floor; clamped to %s", spec.Interval, MinInterval))
	}

	// Defaults.
	mode := spec.Mode
	if mode == "" {
		mode = ModeOnce
	}
	if spec.Kind == KindJob && mode == ModeRepeat {
		return "", "", fmt.Errorf("job watch %q: repeat mode not allowed (job source is terminal-once)", spec.Name)
	}
	notify := true
	if spec.Notify != nil {
		notify = *spec.Notify
	}

	// Auto-resume rules (spec §1): a subagent watch never resumes —
	// dropping it here means re-parenting (TransferFromSubagent) can
	// never reintroduce intent; resume forces notify because a fire
	// nobody reports can't wake anyone.
	resume := spec.Resume
	if resume && spec.Owner != "" {
		resume = false
		notes = append(notes, "resume ignored: subagent watches never resume")
	}
	if resume && !notify {
		notes = append(notes, "resume=true forces notify=true")
	}
	if resume {
		notify = true
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
		resume:    resume,
		interval:  interval,
		owner:     spec.Owner,
		state:     StateWatching,
		createdAt: time.Now(),
		ctx:       watchCtx,
		cancel:    watchCancel,
	}
	m.watches[id] = w
	// Advance the membership revision under the lock so a consumer reading a
	// snapshot after this admission sees a newer projection than before it.
	m.revision++
	// Increment wg under the lock so Close either sees the counted goroutine
	// or rejects the Start outright (mirrors JobManager.Start).
	m.wg.Add(1)
	m.mu.Unlock()

	go m.runWatch(w)
	// Publish the initial StateWatching event so the TUI lane can synthesize
	// the row from registration (the manager owns the publish path; the lane
	// has no other way to learn about a watch before its first transition).
	m.publishEvent(w, StateWatching, "", spec.Owner, mode, false, nil)
	return id, strings.Join(notes, "; "), nil
}

// sampleOnce performs one evaluation: sample the source, record the result,
// and fire if the condition trips. It is the loop's core and is directly
// testable.
func (m *Manager) sampleOnce(w *watch) {
	// Reset the per-interval notification latch at the start of each tick.
	w.mu.Lock()
	w.firedThisInterval = false
	w.mu.Unlock()

	sample, err := m.sampler.Sample(m.managerCtx, w)
	if err != nil {
		m.handleSampleError(w, err)
		return
	}
	// Cap the sample before storing and before condition evaluation so
	// regex/json conditions see the same normalized tail the report shows.
	sample.Stdout = tailCap(sample.Stdout)
	w.mu.Lock()
	prev := w.prev
	hasPrev := w.hasPrev
	w.prev = sample
	w.hasPrev = true
	w.lastSample = sample.Stdout
	// A successful sample resets the consecutive-error budget and, if the
	// watch was in the error state, returns it to watching. Fired/stopped
	// watches are never resurrected.
	w.consecutiveErrors = 0
	if w.state == StateError {
		w.state = StateWatching
		// Release waiters on the recovery edge too: StateError is not a
		// WaitFire fast-path, so a waiter may be parked on an errored
		// watch. Callers re-scan after every wake — a recovered watch is
		// outstanding again and fresh waiters re-register on the healthy
		// watch.
		w.signalWaitersLocked()
	}
	w.mu.Unlock()
	tripped := false
	if w.cond != nil {
		// The change condition needs a baseline; the first sample never
		// trips, so a static source never fires on registration (design:
		// repeat+change+static source -> no fires).
		if _, isChange := w.cond.(changeCondition); !isChange || hasPrev {
			tripped = w.cond.Eval(sample, prev)
		}
	}
	w.recordSample(SamplePoint{At: time.Now(), Value: sampleValue(w.condRaw, sample, tripped), Tripped: tripped})
	if tripped {
		m.fire(w, sample)
	}
}

// recordSample appends p to the watch's history, dropping the oldest points
// beyond MaxSamples.
func (w *watch) recordSample(p SamplePoint) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.samples = append(w.samples, p)
	if n := len(w.samples); n > MaxSamples {
		w.samples = append(w.samples[:0], w.samples[n-MaxSamples:]...)
	}
}

// handleSampleError records a sampling error and auto-stops the watch once
// the consecutive-error budget is exceeded.
func (m *Manager) handleSampleError(w *watch, err error) {
	w.mu.Lock()
	w.consecutiveErrors++
	w.lastError = err.Error()
	w.state = StateError
	w.signalWaitersLocked()
	consecutive := w.consecutiveErrors
	owner, mode := w.owner, w.mode
	w.mu.Unlock()
	m.publishEvent(w, StateError, "", owner, mode, false, nil)

	if consecutive >= MaxConsecutiveErrors {
		reason := fmt.Sprintf("stopped after %d consecutive errors: %v", MaxConsecutiveErrors, err)
		m.autoStop(w, reason)
	}
}

// autoStop retires a watch after the consecutive-error budget is exceeded,
// with the failed reason. It enqueues one error report; human diagnostics stay
// in LastError and the retained terminal Info.
func (m *Manager) autoStop(w *watch, reason string) {
	w.mu.Lock()
	if w.state == StateStopped || w.state == StateFired {
		w.mu.Unlock()
		return
	}
	w.lastError = reason
	owner, mode, resume := w.owner, w.mode, w.resume
	w.mu.Unlock()

	info, ok := m.finalizeTerminal(w, StateStopped, terminalReasonFailed)
	if !ok {
		return
	}
	m.publishEvent(w, StateStopped, "", owner, mode, resume, &info)
	m.deliverTerminal(info)
	if m.deps.OnFire != nil {
		m.deps.OnFire(Report{
			WatchID:     w.id,
			Name:        w.name,
			Kind:        w.kind,
			Condition:   w.condRaw,
			Mode:        mode,
			Interval:    w.interval,
			Owner:       owner,
			Resume:      resume,
			AutoRemoved: true,
			IsError:     true,
		})
	}
}

// fire records a fired state, enqueues a report, and (for once mode) retires
// the watch's membership with the completed reason. A repeat fire keeps the
// watch watching.
func (m *Manager) fire(w *watch, sample Sample) {
	w.mu.Lock()
	// State guard: a sample in flight when Stop (or auto-stop) is called must
	// not fire after the watch is stopped.
	if w.state == StateStopped || w.state == StateFired {
		w.mu.Unlock()
		return
	}
	w.fireCount++
	w.lastFiredAt = time.Now()
	w.lastSample = tailCap(sample.Stdout)
	alreadyNotified := w.firedThisInterval
	w.firedThisInterval = true
	removed := w.mode == ModeOnce
	notify := w.notify && !alreadyNotified
	mode := w.mode
	owner := w.owner
	resume := w.resume
	firedCount := w.fireCount
	if !removed {
		// Repeat mode stays watching; wake waiters for this fire. A once fire
		// wakes waiters inside finalizeTerminal so the terminal facts are
		// visible before any waiter reads them.
		w.signalWaitersLocked()
	}
	w.mu.Unlock()

	var info Info
	hasInfo := false
	if removed {
		var ok bool
		info, ok = m.finalizeTerminal(w, StateFired, terminalReasonCompleted)
		if !ok {
			// Lost a race with Stop/auto-stop, which already retired the watch.
			return
		}
		hasInfo = true
	}
	var infoRef *Info
	if hasInfo {
		infoRef = &info
	}
	m.publishEvent(w, StateFired, sample.Stdout, owner, mode, resume && notify, infoRef)
	if hasInfo {
		m.deliverTerminal(info)
	}
	if notify && m.deps.OnFire != nil {
		m.deps.OnFire(Report{
			WatchID:     w.id,
			Name:        w.name,
			Kind:        w.kind,
			Condition:   w.condRaw,
			Mode:        mode,
			Interval:    w.interval,
			Sample:      tailCap(sample.Stdout),
			FiredCount:  firedCount,
			AutoRemoved: removed,
			Owner:       owner,
			Resume:      resume,
		})
	}
}

// Stop idempotently stops a watch. Unknown or already-terminal IDs return
// success with a "was already gone" note (mirrors the idempotent-kill
// convention the retired job tools used).
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
	owner, mode := w.owner, w.mode
	w.mu.Unlock()

	info, ok := m.finalizeTerminal(w, StateStopped, terminalReasonStopped)
	if !ok {
		return "was already gone", nil
	}
	m.publishEvent(w, StateStopped, "", owner, mode, false, &info)
	m.deliverTerminal(info)
	return "", nil
}

// List returns a snapshot of all watches, sorted by CreatedAt then ID
// (stable, mirroring notifyChange's sort).
func (m *Manager) List() []Info {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Info, 0, len(m.watches))
	for _, w := range m.watches {
		// snapshotWith takes w.mu: the watch's sample ring is written by its
		// own goroutine under that lock, so reading it under m.mu alone races.
		out = append(out, w.snapshotWith(m.epoch, m.revision))
	}
	sort.Slice(out, func(i, j int) bool {
		return infoLess(out[i], out[j])
	})
	return out
}

// Status returns the snapshot for a single watch.
func (m *Manager) Status(id string) (Info, error) {
	m.mu.Lock()
	w, ok := m.watches[id]
	epoch, revision := m.epoch, m.revision
	m.mu.Unlock()
	if !ok {
		return Info{}, fmt.Errorf("watch %q not found", id)
	}
	return w.snapshotWith(epoch, revision), nil
}

// snapshotFor returns a watch's public snapshot stamped with the manager's
// current epoch and membership revision.
func (m *Manager) snapshotFor(w *watch) Info {
	m.mu.Lock()
	epoch, revision := m.epoch, m.revision
	m.mu.Unlock()
	return w.snapshotWith(epoch, revision)
}

// WaitFire blocks until the watch with the given ID fires, stops, errors, or
// ctx ends, and returns its final snapshot. A watch that is already terminal
// returns immediately. An unknown ID (including a once-mode watch that fired
// earlier and was auto-removed) returns an error naming that fact —
// watch.status remains the non-blocking peek.
func (m *Manager) WaitFire(ctx context.Context, id string) (Info, error) {
	m.mu.Lock()
	w, ok := m.watches[id]
	m.mu.Unlock()
	if !ok {
		return Info{}, fmt.Errorf("watch %q not found (already fired or stopped)", id)
	}

	w.mu.Lock()
	switch w.state {
	case StateFired, StateStopped:
		w.mu.Unlock()
		return m.snapshotFor(w), nil
	}
	ch := make(chan struct{})
	w.waiters = append(w.waiters, ch)
	w.mu.Unlock()

	select {
	case <-ch:
		return m.snapshotFor(w), nil
	case <-w.ctx.Done():
		// removeWatch cancelled the goroutine (Stop/auto-stop/close raced
		// the registration); the final snapshot still reads correctly —
		// the watch struct outlives the map.
		return m.snapshotFor(w), nil
	case <-ctx.Done():
		w.mu.Lock()
		for i, waiter := range w.waiters {
			if waiter == ch {
				w.waiters = append(w.waiters[:i], w.waiters[i+1:]...)
				break
			}
		}
		w.mu.Unlock()
		return Info{}, ctx.Err()
	}
}

// Close cancels all watch goroutines and waits for them to finish, respecting
// the caller's context deadline. It is idempotent. After the source goroutines
// have joined it finalizes every remaining registration with the shutdown
// reason. A join that times out leaves its registrations active until a later
// successful join; no close-time report or resume is generated.
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
		m.finalizeRemainingShutdown()
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// finalizeRemainingShutdown retires every registration still active after the
// manager's source goroutines have joined, with the shutdown reason. It is a
// no-op when nothing is left, so a repeated successful close neither finalizes
// nor reports a second time.
func (m *Manager) finalizeRemainingShutdown() {
	m.mu.Lock()
	ids := make([]string, 0, len(m.watches))
	for id := range m.watches {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	sort.Strings(ids)

	for _, id := range ids {
		m.mu.Lock()
		w, ok := m.watches[id]
		m.mu.Unlock()
		if !ok {
			continue
		}
		w.mu.Lock()
		if w.state == StateStopped || w.state == StateFired {
			w.mu.Unlock()
			continue
		}
		owner, mode := w.owner, w.mode
		w.mu.Unlock()

		info, ok := m.finalizeTerminal(w, StateStopped, terminalReasonShutdown)
		if !ok {
			continue
		}
		m.publishEvent(w, StateStopped, "", owner, mode, false, &info)
		m.deliverTerminal(info)
	}
}

// publishEvent emits a watch event to the TUI lane via the injected OnEvent
// seam, if configured. owner/mode/resume are passed by the caller — the sites
// that hold w.mu extract them there (fire, autoStop, Stop, handleSampleError)
// and Start passes its spec-locals — so the event carries exact resume
// semantics: resume is true only when this transition also pushed a report.
// info, when non-nil, is deep-copied onto the event so a subscriber mutating
// it never reaches the manager's retained state.
func (m *Manager) publishEvent(w *watch, state State, sample string, owner string, mode Mode, resume bool, info *Info) {
	if m.deps.OnEvent == nil {
		return
	}
	m.mu.Lock()
	facts := w.workFacts(m.epoch, m.revision)
	m.mu.Unlock()
	var infoCopy *Info
	if info != nil {
		c := info.clone()
		infoCopy = &c
	}
	m.deps.OnEvent(Event{
		WorkFacts: facts,
		WatchID:   w.id,
		Name:      w.name,
		Kind:      w.kind,
		State:     state,
		Sample:    tailCap(sample),
		Owner:     owner,
		Mode:      mode,
		Resume:    resume,
		Info:      infoCopy,
	})
}

// finalizeTerminal performs a watch's terminal transition under the manager ->
// watch lock order: it sets the terminal state, records EndedAt, the pinned
// terminal reason and the derived canonical result revision, wakes any
// WaitFire waiter (so they observe the terminal facts), retains the terminal
// Info, records the registration as settling when a durable sink is
// configured, and removes the watch from active membership. It reports false
// when another path already finalized the watch.
//
// The caller publishes the removal event and calls OnTerminal after this
// returns, with no manager or watch lock held. reason is one of
// terminalReasonCompleted/Stopped/Failed/Shutdown.
func (m *Manager) finalizeTerminal(w *watch, state State, reason string) (Info, bool) {
	now := time.Now()
	m.mu.Lock()
	w.mu.Lock()
	if w.state == StateStopped || w.state == StateFired {
		w.mu.Unlock()
		m.mu.Unlock()
		return Info{}, false
	}
	w.state = state
	w.endedAt = now
	w.terminalReason = reason
	w.resultRevision = m.resultRevisionLocked(w, reason, now)
	w.signalWaitersLocked()
	m.revision++
	info := w.snapshotLockedWith(m.epoch, m.revision)
	m.terminals[w.id] = info
	if m.deps.OnTerminal != nil {
		// Record the settling marker before releasing active membership, so a
		// Snapshot that races the removal either sees the watch active or sees
		// it settling — never a terminal fact with no sink recorded.
		m.settling[w.id] = struct{}{}
	}
	delete(m.watches, w.id)
	w.mu.Unlock()
	m.mu.Unlock()

	if w.cancel != nil {
		w.cancel()
	}
	return info, true
}

// deliverTerminal hands one retained terminal fact to the durable-result sink
// outside every manager and watch lock. It clears the settling marker once the
// initial attempt has returned and records a failure for RetryTerminalSaves. A
// successful retry never re-fires a report or re-announces completion.
func (m *Manager) deliverTerminal(info Info) {
	if m.deps.OnTerminal == nil {
		return
	}
	err := m.deps.OnTerminal(info)
	m.mu.Lock()
	delete(m.settling, info.ID)
	if err != nil {
		if m.saveErrors == nil {
			m.saveErrors = make(map[string]string)
		}
		m.saveErrors[info.ID] = err.Error()
	} else {
		delete(m.saveErrors, info.ID)
	}
	m.revision++
	m.mu.Unlock()
}

// resultRevisionLocked derives a watch's canonical stable result revision from
// the owning conversation, the manager epoch, the registration ID, the
// terminal state and reason, and the end time. Called with m.mu and w.mu held.
func (m *Manager) resultRevisionLocked(w *watch, reason string, endedAt time.Time) string {
	key := progress.WorkKey(m.deps.ConversationID, m.epoch, "watch", w.id)
	return progress.ResultRevision(key, string(w.state), reason, nil, endedAt)
}

// Snapshot is an immutable projection of the watch manager at one moment: the
// manager epoch, the lifecycle/membership revision, every active registration,
// the retained terminal facts of removed registrations, the registrations whose
// initial durable-result sink is still in flight, and any bounded save errors a
// failed attempt recorded. Every Info is a deep copy, so a consumer never
// shares mutable state with the manager or an observer. A broker event is only
// a wake: this is where a collector reads the current state rather than applying
// a possibly late event copy.
type Snapshot struct {
	Epoch      string
	Revision   uint64
	Active     []Info
	Terminal   []Info
	Settling   []string
	SaveErrors map[string]string
}

// Snapshot returns an immutable projection of every active registration plus
// the retained terminal facts of removed ones. The epoch and membership
// revision are read under the manager lock; each Info is copied under the
// manager -> watch lock order. A removed registration whose durable-result sink
// is still in flight is named in Settling; a failed attempt is reported in
// SaveErrors by registration identity.
func (m *Manager) Snapshot() Snapshot {
	m.mu.Lock()
	snap := Snapshot{
		Epoch:    m.epoch,
		Revision: m.revision,
		Active:   make([]Info, 0, len(m.watches)),
	}
	for _, w := range m.watches {
		// snapshotWith takes w.mu, matching the manager -> watch lock order
		// used by finalizeTerminal and TransferFromSubagent.
		snap.Active = append(snap.Active, w.snapshotWith(m.epoch, m.revision))
	}
	for _, info := range m.terminals {
		snap.Terminal = append(snap.Terminal, info.clone())
	}
	for id := range m.settling {
		snap.Settling = append(snap.Settling, id)
	}
	if len(m.saveErrors) > 0 {
		snap.SaveErrors = make(map[string]string, len(m.saveErrors))
		for k, v := range m.saveErrors {
			snap.SaveErrors[k] = v
		}
	}
	m.mu.Unlock()

	sort.Slice(snap.Active, func(i, j int) bool { return infoLess(snap.Active[i], snap.Active[j]) })
	sort.Slice(snap.Terminal, func(i, j int) bool {
		if !snap.Terminal[i].EndedAt.Equal(snap.Terminal[j].EndedAt) {
			return snap.Terminal[i].EndedAt.Before(snap.Terminal[j].EndedAt)
		}
		return snap.Terminal[i].ID < snap.Terminal[j].ID
	})
	sort.Strings(snap.Settling)
	return snap
}

// RetryTerminalSaves re-offers every retained terminal fact whose durable-result
// sink failed. It returns an error naming the saves that still failed, or nil
// when nothing is pending. A successful retry clears the recorded save error;
// it never re-fires a report or re-announces a completion.
func (m *Manager) RetryTerminalSaves() error {
	m.mu.Lock()
	ids := make([]string, 0, len(m.saveErrors))
	for id := range m.saveErrors {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	if len(ids) == 0 {
		return nil
	}
	sort.Strings(ids)

	var failed []string
	for _, id := range ids {
		m.mu.Lock()
		info, ok := m.terminals[id]
		if !ok {
			delete(m.saveErrors, id)
			m.mu.Unlock()
			continue
		}
		m.settling[id] = struct{}{}
		onTerminal := m.deps.OnTerminal
		m.mu.Unlock()

		var err error
		if onTerminal != nil {
			err = onTerminal(info)
		}

		m.mu.Lock()
		delete(m.settling, id)
		if err != nil {
			m.saveErrors[id] = err.Error()
			failed = append(failed, fmt.Sprintf("%s: %v", id, err))
		} else {
			delete(m.saveErrors, id)
		}
		m.revision++
		m.mu.Unlock()
	}
	if len(failed) > 0 {
		return fmt.Errorf("watch terminal save: %s", strings.Join(failed, "; "))
	}
	return nil
}

// infoLess orders info snapshots oldest-first, ID as tie-break, so a lane built
// from a map has a stable order.
func infoLess(a, b Info) bool {
	if !a.CreatedAt.Equal(b.CreatedAt) {
		return a.CreatedAt.Before(b.CreatedAt)
	}
	return a.ID < b.ID
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

// signalWaitersLocked wakes every WaitFire waiter. Called with w.mu held.
func (w *watch) signalWaitersLocked() {
	for _, ch := range w.waiters {
		close(ch)
	}
	w.waiters = nil
}

// workFacts returns the watch's membership projection stamped with the given
// manager epoch and revision.
func (w *watch) workFacts(epoch string, revision uint64) WorkFacts {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.workFactsLocked(epoch, revision)
}

func (w *watch) workFactsLocked(epoch string, revision uint64) WorkFacts {
	return WorkFacts{
		Epoch:          epoch,
		Revision:       revision,
		Active:         w.state == StateWatching || w.state == StateError,
		EndedAt:        w.endedAt,
		TerminalReason: w.terminalReason,
		ResultRevision: w.resultRevision,
	}
}

// snapshot returns a copy of the watch's public state. It is used by tests
// that hold a *watch directly; the manager stamps epoch and revision where it
// reads a snapshot.
func (w *watch) snapshot() Info {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.snapshotLockedWith("", 0)
}

// snapshotWith returns a copy of the watch's public state stamped with the
// manager's epoch and revision.
func (w *watch) snapshotWith(epoch string, revision uint64) Info {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.snapshotLockedWith(epoch, revision)
}

// snapshotLockedWith builds the public Info. The watch lock must be held.
func (w *watch) snapshotLockedWith(epoch string, revision uint64) Info {
	return Info{
		WorkFacts:   w.workFactsLocked(epoch, revision),
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
		Samples:     append([]SamplePoint(nil), w.samples...),
	}
}

// tailCap truncates a string to the last SampleTailCap bytes.
func tailCap(s string) string {
	if len(s) <= SampleTailCap {
		return s
	}
	return s[len(s)-SampleTailCap:]
}
