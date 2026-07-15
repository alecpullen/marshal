package native

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"marshal/internal/pubsub"
	"marshal/internal/tools/registry"
)

// ErrJobManagerClosed is returned by Start when the manager has been shut down.
var ErrJobManagerClosed = errors.New("job manager is closed")

// JobEvent is the broker payload published by JobManager whenever the
// running-job count changes. The TUI's pump converts it into a jobCountMsg
// to drive the status line without polling. Per F19 R4 the type lives next
// to the service that publishes it; the tui package imports this native
// type directly.
type JobEvent struct {
	Count int
	Delta int
}

// JobStatus describes the lifecycle state of a background shell job.
type JobStatus string

const (
	StatusRunning   JobStatus = "running"
	StatusCompleted JobStatus = "completed"
	StatusFailed    JobStatus = "failed"
	StatusTimedOut  JobStatus = "timed_out"
	StatusKilled    JobStatus = "killed"
)

// JobInfo is a snapshot of a background job's metadata.
type JobInfo struct {
	ID              string
	PID             int
	Command         string
	Status          JobStatus
	StartedAt       time.Time
	ExitCode        *int
	Sandbox         registry.SandboxMeta
	OutputTruncated bool
}

// JobManager tracks background shell jobs, enforces a maximum concurrency
// limit, and provides buffered output, status queries, and kill semantics.
// Jobs are run through the configured CommandRunner rather than directly
// managing OS processes.
type JobManager struct {
	runner         CommandRunner
	dir            string
	maxJobs        int
	retention      time.Duration
	maxOutputBytes int
	mu             sync.Mutex
	jobs           map[string]*job
	nextID         int
	onChange       func(int)
	jobBroker      *pubsub.Broker[JobEvent]
	lastNotified   int
	closed         bool
	wg             sync.WaitGroup
	managerCtx     context.Context
	managerCancel  context.CancelFunc
}

type job struct {
	mu            sync.Mutex
	info          JobInfo
	cancel        context.CancelFunc
	done          chan struct{}
	killRequested bool
	err           error
	stdout        *safeBuffer
	stderr        *safeBuffer
	completedAt   time.Time
}

// safeBuffer wraps a bytes.Buffer with a mutex so it can be read by
// JobManager.Output while the goroutine is still writing.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *safeBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *safeBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// NewJobManager creates a job manager with the given runner and limits.
// The manager derives its lifetime from ctx: when the parent context is
// cancelled all running jobs are cancelled. Non-positive maxJobs defaults
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
		managerCtx:     managerCtx,
		managerCancel:  managerCancel,
	}
}

// SetOnChange registers a callback that is invoked whenever the number of
// running jobs changes. The callback must not call back into the JobManager.
func (m *JobManager) SetOnChange(fn func(int)) {
	m.mu.Lock()
	m.onChange = fn
	m.mu.Unlock()
}

// SetBroker wires the F19 pub/sub broker so every change in the running-job
// count publishes a JobEvent. Safe to call before any jobs are started.
// Replaces nil safely; a nil broker disables publishing only.
func (m *JobManager) SetBroker(b *pubsub.Broker[JobEvent]) {
	m.mu.Lock()
	m.jobBroker = b
	m.mu.Unlock()
}

// Start launches command as a background job and returns its job ID.
// It rejects blank commands and a closed manager. The command is run
// through the configured CommandRunner with the manager's output limit.
func (m *JobManager) Start(ctx context.Context, command string, timeout time.Duration) (string, error) {
	if strings.TrimSpace(command) == "" {
		return "", fmt.Errorf("command is required")
	}

	m.evictCompleted()

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return "", ErrJobManagerClosed
	}

	// Count only running jobs against the concurrency limit.
	running := 0
	for _, j := range m.jobs {
		j.mu.Lock()
		if j.info.Status == StatusRunning {
			running++
		}
		j.mu.Unlock()
	}
	if running >= m.maxJobs {
		m.mu.Unlock()
		return "", fmt.Errorf("too many background jobs (max %d)", m.maxJobs)
	}

	// Reject an already-cancelled call context.
	if err := ctx.Err(); err != nil {
		m.mu.Unlock()
		return "", err
	}

	m.nextID++
	id := fmt.Sprintf("job-%d", m.nextID)

	// Derive the long-lived job context from the manager context (not the
	// tool-call context) so the job survives the tool-call lifetime.
	var jobCtx context.Context
	var jobCancel context.CancelFunc
	if timeout > 0 {
		jobCtx, jobCancel = context.WithTimeout(m.managerCtx, timeout)
	} else {
		jobCtx, jobCancel = context.WithCancel(m.managerCtx)
	}

	// Allocate bounded output buffers.
	stdout := &safeBuffer{}
	stderr := &safeBuffer{}

	j := &job{
		info: JobInfo{
			ID:        id,
			Command:   command,
			Status:    StatusRunning,
			StartedAt: time.Now(),
		},
		cancel: jobCancel,
		done:   make(chan struct{}),
		stdout: stdout,
		stderr: stderr,
	}
	m.jobs[id] = j
	// Increment wg under the lock so Shutdown either sees the counted
	// goroutine or rejects the Start outright.
	m.wg.Add(1)
	m.mu.Unlock()

	go m.runJob(j, jobCtx, command)

	m.notifyChange()
	return id, nil
}

func (m *JobManager) runJob(j *job, ctx context.Context, command string) {
	// Prepare bounded observers that write into the job's safe buffers.
	limit := m.maxOutputBytes
	stdoutObs := NewBoundedOutput(OutputLimit(limit), j.stdout)
	stderrObs := NewBoundedOutput(OutputLimit(limit), j.stderr)

	var capturedPID int
	req := CommandRequest{
		Command:        command,
		Dir:            m.dir,
		MaxOutputBytes: limit,
		Stdout:         stdoutObs,
		Stderr:         stderrObs,
		OnStart: func(pid int) {
			capturedPID = pid
		},
	}

	result, runErr := m.runner.Run(ctx, req)

	// Store results. Release the job lock before calling notifyChange
	// to avoid a lock-ordering deadlock (List holds m.mu -> j.mu,
	// notifyChange acquires m.mu).
	j.mu.Lock()
	j.info.PID = capturedPID
	j.info.Sandbox = result.Meta
	j.info.OutputTruncated = result.Meta.OutputTruncated || stdoutObs.Truncated() || stderrObs.Truncated()
	j.err = runErr
	j.completedAt = time.Now()

	// Classify completion status.
	if j.killRequested {
		j.info.Status = StatusKilled
	} else if ctx.Err() == context.DeadlineExceeded {
		j.info.Status = StatusTimedOut
	} else if runErr != nil {
		j.info.Status = StatusFailed
	} else {
		j.info.Status = StatusCompleted
		j.info.ExitCode = &result.ExitCode
	}
	j.mu.Unlock()

	m.notifyChange()
	close(j.done)
	m.wg.Done()
}

// Output returns the current metadata and buffered stdout/stderr for a job.
// tailLines, when positive, limits the returned output to the last N lines.
// The truncation marker is appended when the job's output exceeded the limit.
func (m *JobManager) Output(id string, tailLines int) (JobInfo, string, error) {
	m.evictCompleted()

	m.mu.Lock()
	j, ok := m.jobs[id]
	m.mu.Unlock()
	if !ok {
		return JobInfo{}, "", fmt.Errorf("job %q not found", id)
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

// Kill terminates a running background job. It sets the kill-requested flag,
// cancels the job context (which causes runner.Run to return), and then waits
// for the runner goroutine to finish. Repeated kill of a terminal job is
// harmless.
func (m *JobManager) Kill(id string) error {
	m.evictCompleted()

	m.mu.Lock()
	j, ok := m.jobs[id]
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("job %q not found", id)
	}

	j.mu.Lock()
	if j.info.Status != StatusRunning {
		j.mu.Unlock()
		return nil // Already terminal.
	}
	j.killRequested = true
	cancel := j.cancel
	done := j.done
	j.mu.Unlock()

	cancel()
	<-done // Wait for the runner goroutine to finish.
	m.notifyChange()
	return nil
}

// List returns a snapshot of all tracked jobs.
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

// RunningCount returns the number of jobs currently in the running state.
func (m *JobManager) RunningCount() int {
	m.evictCompleted()

	m.mu.Lock()
	defer m.mu.Unlock()
	count := 0
	for _, j := range m.jobs {
		j.mu.Lock()
		if j.info.Status == StatusRunning {
			count++
		}
		j.mu.Unlock()
	}
	return count
}

// evictCompleted removes terminal jobs whose completion timestamp is older
// than the configured retention duration.
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

// Shutdown closes the manager (rejecting new Start calls), cancels all
// running jobs, and waits for every goroutine (including completed jobs)
// to finish. It respects the caller's context deadline: if Shutdown's
// context is cancelled before all goroutines finish it returns the
// context error.
//
// Shutdown is idempotent — safe to call multiple times. Concurrent calls
// are safe; later calls block on the same wg and return the same outcome.
// After the first call returns, the manager is permanently closed.
func (m *JobManager) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	m.closed = true
	// Cancel the manager context so all job contexts are cancelled.
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

func (m *JobManager) notifyChange() {
	m.mu.Lock()
	count := 0
	for _, j := range m.jobs {
		j.mu.Lock()
		if j.info.Status == StatusRunning {
			count++
		}
		j.mu.Unlock()
	}
	delta := count - m.lastNotified
	m.lastNotified = count
	onChange := m.onChange
	broker := m.jobBroker
	m.mu.Unlock()
	if onChange != nil {
		onChange(count)
	}
	if broker != nil {
		broker.Publish("jobs", JobEvent{Count: count, Delta: delta})
	}
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
