package native

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"marshal/internal/pubsub"
	"marshal/internal/tools/registry"
)

// ErrJobManagerClosed is returned by Launch and Start when the manager has been
// shut down.
var ErrJobManagerClosed = errors.New("job manager is closed")

// ErrTurnStopped is returned by Launch when the owning turn's explicit stop was
// already accepted. A stopped turn cannot admit more commands, so the caller
// must not retry with the same origin.
var ErrTurnStopped = errors.New("turn has been stopped")

// maxStoppedTurns bounds the manager's memory of stopped turns. Turn ids are
// unique and a stopped turn cannot admit new commands anyway, so only the most
// recent decisions are retained.
const maxStoppedTurns = 64

// foregroundStopSettleTimeout bounds how long WaitForeground waits for an
// execution to settle after an explicit stop was accepted for it. It is a join
// timeout for the caller, never a process lifetime: the stop request already
// asked for termination, and the runner escalates from its own grace period.
var foregroundStopSettleTimeout = 10 * time.Second

// resourceLimitCauses is the vocabulary a sandbox backend uses to name a
// resource limit it enforced. Marshal reports a resource-limit outcome only
// when the backend names one: a bare exit code (137 or -9, say) never implies a
// limit, because an explicit kill and a runtime crash look identical.
var resourceLimitCauses = map[string]bool{
	"resource_limit": true,
	"memory_limit":   true,
	"cpu_limit":      true,
	"process_limit":  true,
	"oom":            true,
}

// JobEvent is the broker payload published by JobManager whenever the tracked
// execution snapshot changes: admission, published PID, attachment, state and
// settlement — including changes that leave the detached count equal. The TUI's
// pump converts it into a jobCountMsg to drive the status line without polling.
// The type lives next to the service that publishes it; the tui package imports
// this native type directly.
type JobEvent struct {
	// Count is the number of detached executions that are still running or
	// stopping. Attached foreground commands are not background work; they
	// belong to the tool call waiting for them.
	Count int
	Delta int
	// Jobs is a snapshot of every tracked job at publish time, both
	// attachments. The TUI's job lane needs per-job detail (command, status,
	// elapsed), which a bare count cannot supply. Published by value; consumers
	// must not mutate it.
	Jobs []JobInfo
}

// JobStatus describes the lifecycle state of a managed command.
type JobStatus string

const (
	StatusRunning   JobStatus = "running"
	StatusStopping  JobStatus = "stopping"
	StatusCompleted JobStatus = "completed"
	StatusFailed    JobStatus = "failed"
	// StatusTimedOut is historical: it stays readable for records written when
	// a wait expiry could terminate a command. Nothing in this service
	// produces it now, because elapsed wall-clock time never ends a process.
	StatusTimedOut JobStatus = "timed_out"
	StatusKilled   JobStatus = "killed"
)

// Terminal reports whether the status is a settled state. A stopping execution
// is not terminal: its process is still being terminated.
func (s JobStatus) Terminal() bool {
	switch s {
	case StatusCompleted, StatusFailed, StatusTimedOut, StatusKilled:
		return true
	}
	return false
}

// Attachment records how the launching caller relates to an execution:
// foreground while it waits for the outcome, background once it stops waiting.
// Attachment is not execution state — a command survives its attachment
// changing — and a wait timer changes only attachment.
type Attachment string

const (
	// AttachmentForeground means the caller that launched the command is still
	// waiting for its outcome.
	AttachmentForeground Attachment = "foreground"
	// AttachmentBackground means the caller stopped waiting and the command
	// continues as a detached job owned by the conversation service.
	AttachmentBackground Attachment = "background"
)

// JobOutcome classifies how an execution ended. It separates causes that share
// a lifecycle status: a natural non-zero exit and a startup failure are both
// "failed", a resource limit is only claimed on backend evidence, and a stop
// request that lost the race with natural completion never changes the outcome.
// It is empty while an execution is running and for historical records whose
// cause was never recorded.
type JobOutcome string

const (
	// OutcomeCompleted is a natural exit with code 0.
	OutcomeCompleted JobOutcome = "completed"
	// OutcomeNonZeroExit is a natural exit with a non-zero code, which is
	// preserved verbatim.
	OutcomeNonZeroExit JobOutcome = "nonzero_exit"
	// OutcomeStartupFailure means the runner never started a process.
	OutcomeStartupFailure JobOutcome = "startup_failure"
	// OutcomeStopped means an accepted explicit stop request terminated the
	// process.
	OutcomeStopped JobOutcome = "stopped"
	// OutcomeResourceLimit means the backend enforced and named a resource
	// limit that ended the command.
	OutcomeResourceLimit JobOutcome = "resource_limit"
	// OutcomeShutdown means the owning service cancelled the execution without
	// a stop request (conversation end or manager close).
	OutcomeShutdown JobOutcome = "shutdown"
)

// CommandOrigin identifies the conversation work a managed command belongs to.
// Everything here is captured before the execution's goroutine starts, so a
// later config reload cannot redirect or re-own an admitted command.
type CommandOrigin struct {
	// ConversationID is the owning conversation (session). It outlives the
	// turn that launched the command.
	ConversationID string
	// Actor identifies who requested the command: the parent agent, a named
	// subagent, or a pipeline worker.
	Actor string
	// TurnID is the turn whose explicit stop targets commands still attached
	// to it.
	TurnID string
	// StepID and ToolCallID identify the originating call, so a result can be
	// paired with the call that produced it instead of by timestamp order.
	StepID     string
	ToolCallID string
	// Generation is the runtime config generation captured at admission.
	Generation int
}

// JobInfo is a snapshot of a managed command's metadata.
type JobInfo struct {
	ID string
	// ExecutionID is the cross-resume identity of this execution: the display
	// ID alone repeats in every process (job-1, job-1 again after a restart),
	// so pairing a start with its completion — and its persisted record —
	// needs the owning conversation, the runtime generation and the origin
	// call as well. It is composed at admission and never changes, even when
	// the execution hands off from a foreground wait to the background.
	ExecutionID string
	PID         int
	Command     string
	// Dir is the working directory resolved at admission and captured for the
	// execution's life, so a later workspace change cannot redirect it. It is
	// empty in the snapshot Launch returns (the directory is resolved after
	// admission to keep the directory function outside the manager lock) and
	// populated for every later read.
	Dir    string
	Status JobStatus
	// Outcome is empty until the execution settles. See JobOutcome.
	Outcome JobOutcome
	// Attachment is foreground while the launching caller waits, background
	// once it has detached.
	Attachment Attachment
	// StartedAt is the admission time: when the manager accepted the command,
	// which is also when a foreground wait budget starts. It is earlier than
	// the process start, so process startup time counts as part of the wait.
	StartedAt time.Time
	// EndedAt is the settlement time; zero while the execution runs.
	EndedAt time.Time
	// Wait is the effective foreground wait the launching caller resolved,
	// recorded as evidence only. No wall-clock budget in this service ends a
	// process, so this never bounds execution.
	Wait time.Duration
	// ExitCode is the process exit code when one is meaningful: always for a
	// natural settlement (including a non-zero exit), and for a stopped
	// execution when the runner observed a non-zero code. Nil for a startup
	// failure.
	ExitCode        *int
	Sandbox         registry.SandboxMeta
	OutputTruncated bool
	// OutputTail is a bounded recent tail of the command's output, kept for
	// the exit evidence a completion carries. Empty while the execution runs.
	OutputTail string
	// Origin carries the conversation/actor/turn/step/tool-call identity and
	// config generation captured at admission.
	Origin CommandOrigin
	// StopCause and StopRequestedAt record an accepted explicit stop request,
	// separately from the terminal outcome: a request that arrived after the
	// process exited is evidence only and never relabels a natural completion.
	StopCause       string
	StopRequestedAt time.Time
}

// Execution returns the structured, persistable record of this execution: the
// identity, origin, attachment and outcome a transcript needs to pair a start
// with its completion across a resume. It is the one mapping from the manager's
// snapshot to the stored payload, so a field added here cannot be forgotten by
// a producer.
func (i JobInfo) Execution() registry.CommandExecution {
	ex := registry.CommandExecution{
		ExecutionID:     i.ExecutionID,
		DisplayID:       i.ID,
		ConversationID:  i.Origin.ConversationID,
		Actor:           i.Origin.Actor,
		TurnID:          i.Origin.TurnID,
		StepID:          i.Origin.StepID,
		ToolCallID:      i.Origin.ToolCallID,
		Cwd:             i.Dir,
		Generation:      i.Origin.Generation,
		PID:             i.PID,
		StartedAt:       i.StartedAt,
		EndedAt:         i.EndedAt,
		State:           string(i.Status),
		Outcome:         string(i.Outcome),
		Attachment:      executionAttachment(i.Attachment),
		StopCause:       i.StopCause,
		StopRequestedAt: i.StopRequestedAt,
		Truncated:       i.OutputTruncated,
	}
	if i.Wait > 0 {
		ex.Wait = i.Wait.String()
	}
	if i.ExitCode != nil {
		code := *i.ExitCode
		ex.ExitCode = &code
	}
	return ex
}

// executionAttachment maps the manager's attachment onto the stored payload's
// spelling of it.
func executionAttachment(a Attachment) registry.ExecutionAttachment {
	if a == AttachmentBackground {
		return registry.ExecutionAttachmentBackground
	}
	return registry.ExecutionAttachmentForeground
}

// ExecutionIdentity composes an execution's cross-resume identity from its
// display ID and the origin captured at admission. Display IDs restart in every
// process, so an identity that has to survive a resume must name the
// conversation, the runtime generation and the originating call as well.
func ExecutionIdentity(displayID string, origin CommandOrigin) string {
	parts := make([]string, 0, 6)
	parts = append(parts, "job="+displayID)
	if origin.ConversationID != "" {
		parts = append(parts, "conv="+origin.ConversationID)
	}
	if origin.Generation != 0 {
		parts = append(parts, "gen="+strconv.Itoa(origin.Generation))
	}
	if origin.TurnID != "" {
		parts = append(parts, "turn="+origin.TurnID)
	}
	if origin.StepID != "" {
		parts = append(parts, "step="+origin.StepID)
	}
	if origin.ToolCallID != "" {
		parts = append(parts, "call="+origin.ToolCallID)
	}
	return strings.Join(parts, "|")
}

// LaunchSpec is the immutable snapshot of an admitted command. Everything the
// execution needs is captured here before its goroutine starts, so a later
// workspace, config or reload change cannot redirect it.
type LaunchSpec struct {
	Command string
	// Dir is the resolved working directory. Empty means the manager's
	// configured directory, or its dir function's value at admission.
	Dir string
	// Runner overrides the manager's CommandRunner for this execution.
	Runner CommandRunner
	// MaxOutputBytes bounds the retained tail of each stream. Non-positive
	// uses the manager's configured limit.
	MaxOutputBytes int
	// Stdout and Stderr receive every byte of the matching stream while the
	// execution is attached to a foreground caller. Detaching stops the
	// forwarding without touching the retained tail or the process.
	Stdout io.Writer
	Stderr io.Writer
	// Origin is the captured conversation/actor/turn/step/tool-call identity and
	// config generation.
	Origin CommandOrigin
	// Detached requests background attachment from the start: Launch returns as
	// soon as the command is admitted and the caller does not wait for it.
	Detached bool
	// Wait is the effective foreground wait the launching caller resolved,
	// recorded for evidence only. The manager never uses it as an execution
	// deadline: no wall-clock budget ends a process.
	Wait time.Duration
}

// WaitEndReason explains why a foreground wait stopped waiting. It never
// describes why a process ended.
type WaitEndReason string

const (
	// WaitEndTerminal means the command settled during the wait.
	WaitEndTerminal WaitEndReason = "terminal"
	// WaitEndExpired means the caller's wait budget elapsed while the command
	// was still running.
	WaitEndExpired WaitEndReason = "expired"
	// WaitEndCanceled means the caller's wait context ended first.
	WaitEndCanceled WaitEndReason = "canceled"
)

// ForegroundResult distinguishes a terminal command result from continuation
// metadata. Exactly one shape is populated: a terminal result carries the
// command's settled outcome, a continuation carries the handle of a command
// that is still running.
type ForegroundResult struct {
	// Terminal reports whether the command settled before the wait ended.
	Terminal bool
	// WaitEnd is why waiting stopped.
	WaitEnd WaitEndReason
	// Info is the execution snapshot at return time: terminal state for a
	// settled command, running or stopping state for a continuation.
	Info JobInfo
	// Result is the settled command result; zero-valued for a continuation.
	Result CommandResult
	// Output is a bounded recent tail of the command's combined output: the
	// settled output for a terminal result, the live tail otherwise.
	Output string
	// RunError is the runner's own error for a terminal result (a startup
	// failure, or a natural non-zero exit the runner reported as an error). It
	// is nil for a continuation: an expired wait is never a failed command.
	RunError error
}

// JobManager supervises every managed command — attached and detached — from
// admission through settlement, enforces the concurrency limit, and provides
// bounded output, status queries and explicit stop semantics. Commands run
// through the configured CommandRunner rather than the manager managing OS
// processes itself.
//
// # Lifetime and attachment
//
// Execution state (running, stopping, terminal) and attachment (foreground
// while a caller waits, background afterwards) are separate. Wait timers affect
// attachment only: no wall-clock timer in the manager terminates a process.
// Only an explicit stop request, the owning conversation's shutdown, or the
// command's own exit ends one. A caller that waits with a budget and stops
// waiting leaves the process running under the service's ownership.
//
// # Lock order
//
//	m.mu -> j.mu
//
// m.mu is the admission/detachment boundary and the single authoritative place
// where completion settlement, wait expiry, explicit target stop and turn stop
// are arbitrated, so a stop accepted while an execution is attached targets it
// and a detachment accepted first survives the stop. j.mu protects one job's
// snapshot only. j.mu is never held while acquiring m.mu, and no callback
// (OnChange, OnExit, the broker) is invoked with either lock held, so a
// callback may query the manager.
type JobManager struct {
	runner CommandRunner
	dir    string
	// dirFn, when set via SetDirFunc, resolves the working directory for
	// each execution at admission (the session's active root). Until set,
	// executions use dir.
	dirFn          func() string
	maxJobs        int
	retention      time.Duration
	maxOutputBytes int
	mu             sync.Mutex
	jobs           map[string]*job
	// active counts every admitted execution that has not settled, across both
	// attachments. Capacity is reserved against it: a stopping process keeps
	// its slot until it settles, and a foreground-to-background handoff
	// consumes no new slot.
	active int
	nextID int
	// turnStops remembers stopped turns so a stopped turn cannot admit more
	// commands. Bounded by maxStoppedTurns, most recent first.
	turnStops     map[string]string
	turnStopOrder []string
	onChange      func(int)
	onExit        func(JobInfo, time.Duration, string) error
	// settlements holds completions the exit sink has not acknowledged, keyed
	// by execution ID, so a failed durable write can be retried instead of
	// leaving a settled card that never comes back after a resume.
	settlements   map[string]settlement
	jobBroker     *pubsub.Broker[JobEvent]
	lastNotified  int
	closed        bool
	wg            sync.WaitGroup
	managerCtx    context.Context
	managerCancel context.CancelFunc
}

// settlement is one unacknowledged completion: everything the exit sink needs
// to try the durable write again.
type settlement struct {
	info   JobInfo
	ran    time.Duration
	output string
	err    error
}

// SettlementError reports a completion whose durable record the exit sink has
// still not accepted.
type SettlementError struct {
	Info JobInfo
	Err  error
}

type job struct {
	mu     sync.Mutex
	info   JobInfo
	cancel context.CancelFunc
	done   chan struct{}
	result CommandResult
	err    error
	// stdout and stderr retain a bounded recent tail of each stream. They are
	// handed to the runner through the sinks below, which also forward bytes to
	// an attached foreground observer.
	stdout *BoundedOutput
	stderr *BoundedOutput
	// stdoutSink and stderrSink are the writers passed to the runner.
	stdoutSink *jobSink
	stderrSink *jobSink
	// attached reports whether the execution is still attached to a foreground
	// caller. The sinks consult it without taking the job lock, so detachment
	// stops display forwarding promptly while the tail keeps filling.
	attached    atomic.Bool
	admittedAt  time.Time
	completedAt time.Time
	// processStarted is set from OnStart, and processExited as soon as the
	// runner returns. Together they mark the boundaries a stop request must
	// respect: a request accepted after the process exited is evidence only.
	processStarted bool
	processExited  bool
	stopIssued     bool
	stopPending    bool
	terminal       bool
}

// jobSink retains a bounded tail of one command stream and forwards bytes to
// the attached foreground observer while the execution is attached. It is the
// writer handed to the command runner, so display ownership follows attachment
// and never affects capture.
type jobSink struct {
	tail   *BoundedOutput
	stream io.Writer
	live   *atomic.Bool
}

func (s *jobSink) Write(p []byte) (int, error) {
	if s.stream != nil && s.live.Load() {
		// A failing display observer must not fail the command: the retained
		// tail is the durable record.
		_, _ = s.stream.Write(p)
	}
	return s.tail.Write(p)
}

// NewJobManager creates a job manager with the given runner and limits.
// The manager derives its lifetime from ctx: when the parent context is
// cancelled all admitted commands are cancelled. Non-positive maxJobs defaults
// to 25; non-positive retention defaults to 8h; non-positive maxOutputBytes
// defaults to 200000.
func NewJobManager(ctx context.Context, runner CommandRunner, dir string, maxJobs int, retention time.Duration, maxOutputBytes int) *JobManager {
	if maxJobs <= 0 {
		maxJobs = 25
	}
	if retention <= 0 {
		retention = 8 * time.Hour
	}
	if maxOutputBytes <= 0 {
		maxOutputBytes = defaultMaxOutputBytes
	}
	managerCtx, managerCancel := context.WithCancel(ctx)
	return &JobManager{
		runner:         runner,
		dir:            dir,
		maxJobs:        maxJobs,
		retention:      retention,
		maxOutputBytes: maxOutputBytes,
		jobs:           make(map[string]*job),
		turnStops:      make(map[string]string),
		managerCtx:     managerCtx,
		managerCancel:  managerCancel,
	}
}

// SetOnChange registers a callback that is invoked whenever the detached-job
// count changes, and on admission, PID and attachment changes that leave the
// count equal. It runs with no manager lock held, so it may query the manager;
// it must not launch or stop executions.
func (m *JobManager) SetOnChange(fn func(int)) {
	m.mu.Lock()
	m.onChange = fn
	m.mu.Unlock()
}

// SetOnExit registers a callback invoked once when a detached execution
// finishes, with its settled info, how long it ran and its bounded output tail.
// A foreground execution that settled while its caller was still waiting is not
// reported here: that result reaches the transcript as the call's normal tool
// audit, and reporting it twice would settle one execution twice.
//
// The callback returns whether the completion was made durable. A non-nil error
// means the sink could not store it, so the manager keeps the settlement pending
// and reports it through PendingSettlements until RetrySettlements succeeds;
// nothing may announce a completion that was never stored.
//
// It runs outside the manager's locks, so it may query the manager; it must not
// launch or stop executions.
func (m *JobManager) SetOnExit(fn func(info JobInfo, ran time.Duration, output string) error) {
	m.mu.Lock()
	m.onExit = fn
	m.mu.Unlock()
}

// SetBroker wires the F19 pub/sub broker so every change in the tracked
// execution snapshot publishes a JobEvent. Safe to call before any commands are
// launched. Replaces nil safely; a nil broker disables publishing only.
func (m *JobManager) SetBroker(b *pubsub.Broker[JobEvent]) {
	m.mu.Lock()
	m.jobBroker = b
	m.mu.Unlock()
}

// SetDirFunc makes executions resolve their working directory from fn at
// admission. Until set, the manager uses the dir passed to NewJobManager.
func (m *JobManager) SetDirFunc(fn func() string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dirFn = fn
}

// Launch admits a managed command and returns its execution snapshot. The
// command's process context derives from the conversation service, never from
// the caller's wait budget, so nothing the caller does to a wait can end the
// process: a non-positive waitBudget is not an execution deadline.
//
// It rejects a blank command, a runner-less manager, a cancelled call context,
// a closed manager, a turn whose explicit stop has already been accepted
// (ErrTurnStopped) and admission beyond the configured capacity, which counts
// attached and detached executions alike.
func (m *JobManager) Launch(ctx context.Context, spec LaunchSpec) (JobInfo, error) {
	command := strings.TrimSpace(spec.Command)
	if command == "" {
		return JobInfo{}, fmt.Errorf("command is required")
	}

	m.evictCompleted()

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return JobInfo{}, ErrJobManagerClosed
	}

	// Reject an already-cancelled call context or manager context.
	if err := ctx.Err(); err != nil {
		m.mu.Unlock()
		return JobInfo{}, err
	}
	if err := m.managerCtx.Err(); err != nil {
		m.mu.Unlock()
		return JobInfo{}, err
	}
	if turnID := strings.TrimSpace(spec.Origin.TurnID); turnID != "" {
		if _, stopped := m.turnStops[turnID]; stopped {
			m.mu.Unlock()
			return JobInfo{}, fmt.Errorf("%w: %s", ErrTurnStopped, turnID)
		}
	}
	if m.active >= m.maxJobs {
		max := m.maxJobs
		m.mu.Unlock()
		return JobInfo{}, fmt.Errorf("too many running commands (max %d)", max)
	}

	runner := spec.Runner
	if runner == nil {
		runner = m.runner
	}
	if runner == nil {
		m.mu.Unlock()
		return JobInfo{}, fmt.Errorf("no command runner configured")
	}

	limit := spec.MaxOutputBytes
	if limit <= 0 {
		limit = m.maxOutputBytes
	}
	limit = OutputLimit(limit)

	m.nextID++
	id := fmt.Sprintf("job-%d", m.nextID)

	// Derive the long-lived execution context from the conversation service
	// (not the tool-call context) so the command survives the tool-call
	// lifetime and every wait that ends early.
	jobCtx, jobCancel := context.WithCancel(m.managerCtx)

	jobDir := spec.Dir
	dirFn := m.dirFn
	defaultDir := m.dir

	now := time.Now()
	j := &job{
		info: JobInfo{
			ID:          id,
			ExecutionID: ExecutionIdentity(id, spec.Origin),
			Command:     command,
			Status:      StatusRunning,
			Attachment:  AttachmentForeground,
			StartedAt:   now,
			Wait:        spec.Wait,
			Origin:      spec.Origin,
		},
		cancel:     jobCancel,
		done:       make(chan struct{}),
		stdout:     NewBoundedOutputTail(limit, nil),
		stderr:     NewBoundedOutputTail(limit, nil),
		admittedAt: now,
	}
	if spec.Detached {
		j.info.Attachment = AttachmentBackground
	} else {
		j.attached.Store(true)
	}
	j.stdoutSink = &jobSink{tail: j.stdout, stream: spec.Stdout, live: &j.attached}
	j.stderrSink = &jobSink{tail: j.stderr, stream: spec.Stderr, live: &j.attached}

	m.jobs[id] = j
	m.active++
	// Increment wg under the lock so Shutdown either sees the counted
	// goroutine or rejects the Launch outright.
	m.wg.Add(1)
	m.mu.Unlock()

	// Resolve the working directory before the goroutine starts: a later
	// workspace or config change must not redirect an admitted command. dirFn
	// runs outside the manager lock so session state can never invert the lock
	// order here.
	if jobDir == "" {
		if dirFn != nil {
			jobDir = dirFn()
		} else {
			jobDir = defaultDir
		}
	}
	// Record the resolved directory on the execution: it is part of the
	// captured snapshot a completion reports, and the reason a later reload
	// cannot redirect an admitted command.
	j.mu.Lock()
	j.info.Dir = jobDir
	admitted := j.info
	j.mu.Unlock()

	// Publish admission before the goroutine starts, even when the detached
	// count is unchanged: the execution snapshot moved, and a client must never
	// receive a settled snapshot before the admission it settles.
	m.notifyChange()

	go m.runJob(j, jobCtx, jobDir, command, runner, limit)

	return admitted, nil
}

// Start launches command as a background job and returns its execution ID.
//
// Deprecated: timeout is retained for compiling callers only and is ignored —
// it used to be an execution deadline. Use Launch with a captured LaunchSpec
// and Detached set for new code. This wrapper is removed once callers migrate.
func (m *JobManager) Start(ctx context.Context, command string, timeout time.Duration) (string, error) {
	_ = timeout // A former duration must never end an execution.
	info, err := m.Launch(ctx, LaunchSpec{Command: command, Detached: true})
	if err != nil {
		return "", err
	}
	return info.ID, nil
}

func (m *JobManager) runJob(j *job, ctx context.Context, dir, command string, runner CommandRunner, limit int) {
	req := CommandRequest{
		Command:        command,
		Dir:            dir,
		MaxOutputBytes: limit,
		// Managed commands never derive an execution deadline from a caller's
		// wait budget, and they keep a bounded recent tail rather than a
		// prefix, so output recorded late is still recorded.
		RetainOutputTail: true,
		Stdout:           j.stdoutSink,
		Stderr:           j.stderrSink,
		OnStart: func(pid int) {
			// Publish the PID as soon as the process exists: a status query
			// between start and settlement must already show it.
			j.mu.Lock()
			j.info.PID = pid
			j.processStarted = true
			j.mu.Unlock()
			m.notifyChange()
		},
	}

	result, runErr := runner.Run(ctx, req)

	// Settlement. The authoritative decisions are made under the job lock with
	// process-exit evidence recorded first, so a stop request that arrives
	// after the process exited is recorded as a request without relabelling the
	// natural outcome.
	j.mu.Lock()
	j.processExited = true
	j.result = result
	j.err = runErr
	j.completedAt = time.Now()
	j.recordCapturedOutputLocked(result)
	j.info.Sandbox = result.Meta
	j.info.OutputTruncated = result.Meta.OutputTruncated || j.stdout.Truncated() || j.stderr.Truncated()
	status, outcome, exitCode := classifySettlement(j, ctx, result, runErr)
	j.info.Status = status
	j.info.Outcome = outcome
	j.info.ExitCode = exitCode
	j.info.EndedAt = j.completedAt
	// The settled snapshot carries the retained tail: it is the evidence a
	// completion reports, and the count event deliberately drops it (see
	// notifyChange) so a finished job never re-ships its output on every later
	// change.
	j.info.OutputTail = j.tailedOutputLocked()
	j.terminal = true
	info := j.info
	ran := j.completedAt.Sub(info.StartedAt)
	j.mu.Unlock()

	output := formatCommandOutput(j.stdout.String(), j.stderr.String())

	// A detached completion is the transcript's only record of it, so it goes to
	// the exit sink, which stores it durably before anything is published. A
	// foreground completion is not reported here: the waiting caller receives it
	// as its normal tool result, which the agent audits, and reporting it here
	// as well would settle one execution twice.
	if info.Attachment == AttachmentBackground {
		m.reportSettlement(info, ran, output)
	}

	m.mu.Lock()
	m.active--
	m.mu.Unlock()

	m.notifyChange()
	close(j.done)
	m.wg.Done()
}

// reportSettlement hands one detached completion to the exit sink and records
// whether the sink accepted it. A sink that reports an error leaves the
// settlement pending, so RetrySettlements can offer it again rather than a
// settled card never coming back after a resume; a successful report clears any
// earlier failure for the same execution.
//
// It runs with no manager lock held: the sink writes to the session's database,
// and holding the manager lock across that would stall every admission and
// status query behind one disk write.
func (m *JobManager) reportSettlement(info JobInfo, ran time.Duration, output string) {
	m.mu.Lock()
	onExit := m.onExit
	m.mu.Unlock()
	if onExit == nil {
		return
	}

	if err := onExit(info, ran, output); err != nil {
		m.mu.Lock()
		if m.settlements == nil {
			m.settlements = make(map[string]settlement)
		}
		m.settlements[info.ID] = settlement{info: info, ran: ran, output: output, err: err}
		m.mu.Unlock()
		return
	}
	m.mu.Lock()
	delete(m.settlements, info.ID)
	m.mu.Unlock()
}

// PendingSettlements reports the detached completions whose durable record the
// exit sink has not accepted, oldest first. A caller surfaces these instead of
// assuming an outcome is stored: nothing announced a completion that failed to
// persist.
func (m *JobManager) PendingSettlements() []SettlementError {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]SettlementError, 0, len(m.settlements))
	for _, s := range m.settlements {
		out = append(out, SettlementError{Info: s.info, Err: s.err})
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Info.StartedAt.Equal(out[j].Info.StartedAt) {
			return out[i].Info.StartedAt.Before(out[j].Info.StartedAt)
		}
		return out[i].Info.ID < out[j].Info.ID
	})
	return out
}

// RetrySettlements offers every pending completion to the exit sink again and
// returns the ones that still failed. It is a no-op when nothing is pending.
// The record for an execution is identified by its identity, so a retry updates
// it rather than producing a second completion.
func (m *JobManager) RetrySettlements() []SettlementError {
	m.mu.Lock()
	pending := make([]settlement, 0, len(m.settlements))
	for _, s := range m.settlements {
		pending = append(pending, s)
	}
	m.mu.Unlock()

	for _, s := range pending {
		m.reportSettlement(s.info, s.ran, s.output)
	}
	return m.PendingSettlements()
}

// recordCapturedOutputLocked folds a runner's captured stdout/stderr into the
// retained tails. Real runners stream through the sinks, so their captured text
// is already there and this is a no-op; a runner that only reports the captured
// result would otherwise leave the recorded output empty. The job lock must be
// held.
func (j *job) recordCapturedOutputLocked(result CommandResult) {
	if result.Stdout != "" && j.stdout.String() == "" {
		_, _ = j.stdout.Write([]byte(result.Stdout))
	}
	if result.Stderr != "" && j.stderr.String() == "" {
		_, _ = j.stderr.Write([]byte(result.Stderr))
	}
}

// classifySettlement decides a settled execution's lifecycle status, outcome
// and recorded exit code. The order matters:
//
//  1. A stop request accepted while the process was alive owns the outcome: the
//     command was terminated on request, however it finally exited.
//  2. A runner that never started a process failed to start — there is no exit
//     code to preserve, and this is not an exit status.
//  3. A resource limit is recorded only when the backend names one.
//  4. Otherwise the outcome is natural: completed, a natural non-zero exit with
//     its actual code, or a service-cancelled execution reported as shutdown.
//
// It runs with the job lock held and touches no other lock.
func classifySettlement(j *job, ctx context.Context, result CommandResult, runErr error) (JobStatus, JobOutcome, *int) {
	if j.stopPending {
		// Only a non-zero code is meaningful for a stopped process: a zero from
		// the runner means there was no process-level evidence of how it died.
		var code *int
		if result.ExitCode != 0 {
			c := result.ExitCode
			code = &c
		}
		return StatusKilled, OutcomeStopped, code
	}
	if runErr != nil && !j.processStarted {
		return StatusFailed, OutcomeStartupFailure, nil
	}
	if resourceLimitCauses[result.Meta.KilledReason] {
		code := result.ExitCode
		return StatusFailed, OutcomeResourceLimit, &code
	}
	code := result.ExitCode
	switch {
	case runErr == nil:
		return StatusCompleted, OutcomeCompleted, &code
	case ctx.Err() != nil:
		return StatusFailed, OutcomeShutdown, &code
	default:
		return StatusFailed, OutcomeNonZeroExit, &code
	}
}

// WaitForeground waits for an already-admitted execution to settle, up to
// waitBudget measured from admission, so process startup time is part of the
// caller's wait rather than extra.
//
// The budget is a waiting deadline only. Ending the wait never cancels the
// process: the execution is promoted to background attachment and stays owned
// by the service. A non-positive budget therefore means "do not wait", not "no
// time is allowed".
//
// When a stop request was accepted while the execution was still attached, the
// caller has asked for termination, so the wait is not reported as a
// continuation: WaitForeground waits (bounded by foregroundStopSettleTimeout)
// for the process to settle and reports its actual outcome.
func (m *JobManager) WaitForeground(ctx context.Context, executionID string, waitBudget time.Duration) (ForegroundResult, error) {
	m.evictCompleted()

	j, err := m.lookup(executionID)
	if err != nil {
		return ForegroundResult{}, err
	}
	if res, ok := m.settledResult(j); ok {
		return res, nil
	}

	j.mu.Lock()
	admittedAt := j.admittedAt
	done := j.done
	j.mu.Unlock()

	if waitBudget <= 0 {
		return m.endForegroundWait(j, WaitEndExpired), nil
	}
	remaining := time.Until(admittedAt.Add(waitBudget))
	if remaining <= 0 {
		return m.endForegroundWait(j, WaitEndExpired), nil
	}
	timer := time.NewTimer(remaining)
	defer timer.Stop()

	select {
	case <-done:
		if res, ok := m.settledResult(j); ok {
			return res, nil
		}
		return m.endForegroundWait(j, WaitEndTerminal), nil
	case <-timer.C:
		return m.endForegroundWait(j, WaitEndExpired), nil
	case <-ctx.Done():
		return m.endForegroundWait(j, WaitEndCanceled), nil
	}
}

// endForegroundWait arbitrates the end of a foreground wait at the manager's
// single authoritative boundary: the completion, stop and attachment decisions
// are read and taken together under m.mu -> j.mu. That gives the
// handoff-versus-stop race exactly one winner — a stop accepted first keeps the
// command attached and its outcome is awaited, while a detachment accepted
// first is never undone by a later stop — and it means a caller never receives
// "still running" metadata for a command that has already settled.
func (m *JobManager) endForegroundWait(j *job, waitEnd WaitEndReason) ForegroundResult {
	switch m.arbitrateWait(j) {
	case waitSettled:
		return m.terminalResult(j)
	case waitStopping:
		return m.awaitSettlement(j)
	case waitPromoted:
		m.notifyChange()
	}
	return m.continuation(j, waitEnd)
}

// waitDecision is the outcome of arbitrating one foreground wait.
type waitDecision int

const (
	// waitContinue means the command is still running and is already detached.
	waitContinue waitDecision = iota
	// waitPromoted means the command is still running and this wait's end
	// detached it.
	waitPromoted
	// waitSettled means the command has already settled.
	waitSettled
	// waitStopping means an explicit stop was accepted for a still-running
	// command, so the caller waits for its actual outcome.
	waitStopping
)

// arbitrateWait takes the wait-expiry decision under m.mu and j.mu together.
func (m *JobManager) arbitrateWait(j *job) waitDecision {
	m.mu.Lock()
	defer m.mu.Unlock()

	j.mu.Lock()
	defer j.mu.Unlock()
	if j.info.Status.Terminal() {
		return waitSettled
	}
	if j.stopPending {
		return waitStopping
	}
	if j.info.Attachment == AttachmentForeground {
		j.info.Attachment = AttachmentBackground
		j.attached.Store(false)
		return waitPromoted
	}
	return waitContinue
}

// awaitSettlement waits for a stopped execution to settle, bounded by
// foregroundStopSettleTimeout. A join timeout returns the continuation as-is:
// the stop request still stands and the execution stays service-owned.
func (m *JobManager) awaitSettlement(j *job) ForegroundResult {
	j.mu.Lock()
	done := j.done
	j.mu.Unlock()

	timer := time.NewTimer(foregroundStopSettleTimeout)
	defer timer.Stop()
	select {
	case <-done:
		if res, ok := m.settledResult(j); ok {
			return res
		}
	case <-timer.C:
	}
	return m.continuation(j, WaitEndTerminal)
}

// settledResult returns the terminal outcome of a settled execution.
func (m *JobManager) settledResult(j *job) (ForegroundResult, bool) {
	j.mu.Lock()
	if !j.info.Status.Terminal() {
		j.mu.Unlock()
		return ForegroundResult{}, false
	}
	res := foregroundResultLocked(j)
	j.mu.Unlock()
	return res, true
}

// terminalResult renders a settled execution's outcome. It is only called on an
// execution whose terminal state was already observed, and it re-reads the
// snapshot so the returned result is the one that settled.
func (m *JobManager) terminalResult(j *job) ForegroundResult {
	j.mu.Lock()
	defer j.mu.Unlock()
	return foregroundResultLocked(j)
}

// foregroundResultLocked builds the terminal result from a settled snapshot. The
// job lock must be held.
func foregroundResultLocked(j *job) ForegroundResult {
	return ForegroundResult{
		Terminal: true,
		WaitEnd:  WaitEndTerminal,
		Info:     j.info,
		Result:   j.result,
		Output:   j.tailedOutputLocked(),
		RunError: j.err,
	}
}

// continuation snapshots a still-running execution for the caller.
func (m *JobManager) continuation(j *job, waitEnd WaitEndReason) ForegroundResult {
	j.mu.Lock()
	info := j.info
	output := j.tailedOutputLocked()
	j.mu.Unlock()
	return ForegroundResult{WaitEnd: waitEnd, Info: info, Output: output}
}

// tailedOutputLocked renders the combined recent tail, with the truncation
// marker when capture was capped. The job lock must be held.
func (j *job) tailedOutputLocked() string {
	output := formatCommandOutput(j.stdout.String(), j.stderr.String())
	if j.info.OutputTruncated {
		output += truncationMarker
	}
	return output
}

// RequestStop requests termination of one execution and records the request's
// cause and time. Cancellation is a request until execution settles: the
// execution enters the stopping state and keeps its capacity slot until its
// process is gone.
//
// Repeated requests are harmless. A request for a settled execution is a no-op
// that records nothing, and a request that arrives after the process already
// exited is recorded as evidence without relabelling the natural outcome.
func (m *JobManager) RequestStop(executionID, cause string) error {
	m.evictCompleted()

	m.mu.Lock()
	j, ok := m.jobs[executionID]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("job %q not found", executionID)
	}
	cancel, changed := m.requestStopLocked(j, cause, time.Now())
	m.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if changed {
		m.notifyChange()
	}
	return nil
}

// StopAttached requests termination of every execution still attached to turnID
// at the moment the request is accepted, and records the turn as stopped, so it
// cannot admit more commands. Executions that detached first — and executions
// that already settled — are untouched: a background job survives its turn.
//
// The decision is taken at the manager's admission/detachment boundary, so the
// handoff-versus-stop race has exactly one winner. Every targeted execution is
// in the stopping state before StopAttached returns; termination settles
// asynchronously and is observable through Wait or Output.
func (m *JobManager) StopAttached(turnID, cause string) ([]string, error) {
	turnID = strings.TrimSpace(turnID)
	if turnID == "" {
		return nil, fmt.Errorf("turn id is required")
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, ErrJobManagerClosed
	}
	m.rememberStoppedTurnLocked(turnID, cause)

	now := time.Now()
	var ids []string
	var cancels []context.CancelFunc
	for id, j := range m.jobs {
		j.mu.Lock()
		attached := !j.terminal && j.info.Attachment == AttachmentForeground && j.info.Origin.TurnID == turnID
		j.mu.Unlock()
		if !attached {
			continue
		}
		cancel, changed := m.requestStopLocked(j, cause, now)
		if !changed {
			continue
		}
		ids = append(ids, id)
		if cancel != nil {
			cancels = append(cancels, cancel)
		}
	}
	m.mu.Unlock()

	for _, cancel := range cancels {
		cancel()
	}
	if len(ids) > 0 {
		m.notifyChange()
	}
	sort.Strings(ids)
	return ids, nil
}

// requestStopLocked accepts one stop request while m.mu is held, so it is
// serialized against admission, detachment, turn stops and settlement. It
// returns the cancel function to invoke once m.mu is released (nil when nothing
// was cancelled) and whether the execution's state changed.
func (m *JobManager) requestStopLocked(j *job, cause string, at time.Time) (context.CancelFunc, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()

	if j.terminal {
		return nil, false
	}
	if j.info.StopCause == "" {
		j.info.StopCause = cause
		j.info.StopRequestedAt = at
	}
	if j.processExited || j.stopIssued {
		// Evidence only: the process already exited, so a late request cannot
		// relabel its outcome, and the first request already cancelled it.
		return nil, false
	}
	j.stopIssued = true
	j.stopPending = true
	j.info.Status = StatusStopping
	return j.cancel, true
}

// rememberStoppedTurnLocked records turnID as stopped, along with the cause, so
// a later admission attempt is rejected with the same reason. The manager lock
// must be held.
func (m *JobManager) rememberStoppedTurnLocked(turnID, cause string) {
	if m.turnStops == nil {
		m.turnStops = make(map[string]string)
	}
	if _, seen := m.turnStops[turnID]; !seen {
		m.turnStopOrder = append(m.turnStopOrder, turnID)
	}
	m.turnStops[turnID] = cause
	for len(m.turnStopOrder) > maxStoppedTurns {
		oldest := m.turnStopOrder[0]
		m.turnStopOrder = m.turnStopOrder[1:]
		delete(m.turnStops, oldest)
	}
}

// Output returns the current metadata and buffered stdout/stderr for an
// execution. tailLines, when positive, limits the returned output to the last N
// lines. The truncation marker is appended when the output exceeded the limit.
func (m *JobManager) Output(id string, tailLines int) (JobInfo, string, error) {
	m.evictCompleted()

	j, err := m.lookup(id)
	if err != nil {
		return JobInfo{}, "", err
	}

	j.mu.Lock()
	info := j.info
	stdoutStr := j.stdout.String()
	stderrStr := j.stderr.String()
	truncated := j.info.OutputTruncated
	j.mu.Unlock()

	stdoutTail := tailString(stdoutStr, tailLines)
	stderrTail := tailString(stderrStr, tailLines)
	combined := formatCommandOutput(stdoutTail, stderrTail)
	if truncated {
		combined += truncationMarker
	}
	return info, combined, nil
}

// Kill terminates a running execution and waits for its process to settle.
// Repeated kills are harmless; a settled execution is left untouched.
func (m *JobManager) Kill(id string) error {
	m.evictCompleted()

	if err := m.RequestStop(id, "killed"); err != nil {
		return err
	}
	j, err := m.lookup(id)
	if err != nil {
		return err
	}
	j.mu.Lock()
	done := j.done
	j.mu.Unlock()

	<-done // Wait for the runner goroutine to finish.
	m.notifyChange()
	return nil
}

// List returns a snapshot of all tracked executions, both attachments,
// including settled ones still inside the retention window.
func (m *JobManager) List() []JobInfo {
	m.evictCompleted()

	m.mu.Lock()
	defer m.mu.Unlock()

	out := make([]JobInfo, 0, len(m.jobs))
	for _, j := range m.jobs {
		j.mu.Lock()
		out = append(out, j.info)
		j.mu.Unlock()
	}
	return out
}

// ActiveExecutions returns snapshots of every admitted execution that has not
// settled, across both attachments, oldest first. This is the authoritative
// work snapshot: JobEvent.Count and Outstanding report detached work only.
func (m *JobManager) ActiveExecutions() []JobInfo {
	m.evictCompleted()

	m.mu.Lock()
	out := make([]JobInfo, 0, len(m.jobs))
	for _, j := range m.jobs {
		j.mu.Lock()
		if !j.info.Status.Terminal() {
			out = append(out, j.info)
		}
		j.mu.Unlock()
	}
	m.mu.Unlock()
	sortJobs(out)
	return out
}

// ActiveCount returns the number of admitted executions that have not settled,
// across both attachments. Capacity is reserved against it: a stopping process
// keeps its slot until it settles, and a foreground-to-background handoff
// consumes no new slot.
func (m *JobManager) ActiveCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.active
}

// RunningCount returns the number of detached executions that are still running
// or stopping. Attached foreground commands are not background work: they
// belong to the tool call waiting for them, so they are excluded here and
// included in ActiveCount.
func (m *JobManager) RunningCount() int {
	m.evictCompleted()

	m.mu.Lock()
	defer m.mu.Unlock()
	count := 0
	for _, j := range m.jobs {
		j.mu.Lock()
		if !j.info.Status.Terminal() && j.info.Attachment == AttachmentBackground {
			count++
		}
		j.mu.Unlock()
	}
	return count
}

// Outstanding returns snapshots of every detached execution that is still
// running or stopping, sorted oldest-first (the same ordering notifyChange
// uses). Settled executions remain in the map for retention but are not
// outstanding. It backs the agent.await tool's pending scan.
func (m *JobManager) Outstanding() []JobInfo {
	m.evictCompleted()

	m.mu.Lock()
	out := make([]JobInfo, 0, len(m.jobs))
	for _, j := range m.jobs {
		j.mu.Lock()
		if !j.info.Status.Terminal() && j.info.Attachment == AttachmentBackground {
			out = append(out, j.info)
		}
		j.mu.Unlock()
	}
	m.mu.Unlock()
	sortJobs(out)
	return out
}

// Wait blocks until the execution with the given ID reaches a terminal state
// (or ctx ends) and returns its final snapshot. A settled execution returns
// immediately. Unknown IDs return an error; settled executions are swept after
// the configured retention, so "unknown" also covers long-finished commands.
//
// Wait is non-destructive: an ending context ends the caller's wait only. The
// process keeps running and stays owned by the service.
func (m *JobManager) Wait(ctx context.Context, id string) (JobInfo, error) {
	m.evictCompleted()

	j, err := m.lookup(id)
	if err != nil {
		return JobInfo{}, err
	}

	j.mu.Lock()
	info := j.info
	done := j.done
	j.mu.Unlock()
	if info.Status.Terminal() || done == nil {
		return info, nil
	}

	select {
	case <-done:
		// runJob stores the final info under j.mu before closing done, so
		// the snapshot is now immutable. The job pointer outlives the map.
		j.mu.Lock()
		info = j.info
		j.mu.Unlock()
		return info, nil
	case <-ctx.Done():
		return JobInfo{}, ctx.Err()
	}
}

// lookup resolves an execution by ID, reporting retention-aware errors.
func (m *JobManager) lookup(id string) (*job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok {
		return nil, fmt.Errorf("unknown job %q (finished jobs are swept after retention)", id)
	}
	return j, nil
}

// evictCompleted removes settled executions whose completion timestamp is older
// than the configured retention duration. Active executions are never evicted,
// however small the current capacity is.
func (m *JobManager) evictCompleted() {
	if m.retention <= 0 {
		return
	}
	cutoff := time.Now().Add(-m.retention)
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, j := range m.jobs {
		j.mu.Lock()
		completedAt := j.completedAt
		j.mu.Unlock()
		if !completedAt.IsZero() && completedAt.Before(cutoff) {
			delete(m.jobs, id)
		}
	}
}

// Shutdown closes the manager (rejecting new launches), cancels all admitted
// commands, and waits for every goroutine (including settled executions) to
// finish. It respects the caller's context deadline: if Shutdown's context is
// cancelled before all goroutines finish it returns the context error.
//
// Shutdown is idempotent — safe to call multiple times. Concurrent calls
// are safe; later calls block on the same wg and return the same outcome.
// After the first call returns, the manager is permanently closed.
func (m *JobManager) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	m.closed = true
	// Cancel the manager context so all execution contexts are cancelled.
	m.managerCancel()
	m.mu.Unlock()

	// Wait for all goroutines to finish, respecting the caller's deadline.
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

// notifyChange reports the current detached-execution count to the OnChange
// callback and publishes a JobEvent snapshot. It is called on admission, PID,
// attachment and state changes — including changes that leave the count equal —
// so a client always sees the execution snapshot move. Callbacks run outside
// the manager locks.
func (m *JobManager) notifyChange() {
	m.mu.Lock()
	count := 0
	snapshot := make([]JobInfo, 0, len(m.jobs))
	for _, j := range m.jobs {
		j.mu.Lock()
		if !j.info.Status.Terminal() && j.info.Attachment == AttachmentBackground {
			count++
		}
		info := j.info
		// A settled job keeps its retained tail for the exit evidence, but the
		// change event does not re-ship it: the card that settles from it is
		// built from the completion record, and a session that launches many
		// jobs would otherwise pay for every finished job's output on every
		// later count change.
		info.OutputTail = ""
		snapshot = append(snapshot, info)
		j.mu.Unlock()
	}
	sortJobs(snapshot)
	delta := count - m.lastNotified
	m.lastNotified = count
	onChange := m.onChange
	broker := m.jobBroker
	m.mu.Unlock()
	if onChange != nil {
		onChange(count)
	}
	if broker != nil {
		broker.Publish("jobs", JobEvent{Count: count, Delta: delta, Jobs: snapshot})
	}
}

// sortJobs orders snapshots oldest-first, ID as tie-break, so a lane built from
// a map has a stable order.
func sortJobs(jobs []JobInfo) {
	sort.Slice(jobs, func(i, j int) bool {
		if !jobs[i].StartedAt.Equal(jobs[j].StartedAt) {
			return jobs[i].StartedAt.Before(jobs[j].StartedAt)
		}
		return jobs[i].ID < jobs[j].ID
	})
}

func tailString(s string, n int) string {
	if n <= 0 {
		return s
	}
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}
