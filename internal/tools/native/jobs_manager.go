package native

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"marshal/internal/pubsub"
)

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
	ID        string
	PID       int
	Command   string
	Status    JobStatus
	StartedAt time.Time
	ExitCode  *int
}

// ProcessRunner is the execution backend used by JobManager. It must be able
// to run a command to completion and to start a detached process whose
// lifecycle is managed separately.
type ProcessRunner interface {
	CommandRunner
	Start(req CommandRequest) (*runningCmd, error)
}

// JobManager tracks background shell jobs, enforces a maximum concurrency
// limit, and provides buffered output, status queries, and kill semantics.
type JobManager struct {
	runner       ProcessRunner
	dir          string
	maxJobs      int
	retention    time.Duration
	mu           sync.Mutex
	jobs         map[string]*job
	nextID       int
	onChange     func(int)
	jobBroker    *pubsub.Broker[JobEvent]
	lastNotified int
}

type job struct {
	mu          sync.Mutex
	info        JobInfo
	cmd         *runningCmd
	cancel      context.CancelFunc
	done        chan struct{}
	completedAt time.Time
}

// runningCmd is defined in the platform-specific files (jobs_unix.go,
// jobs_windows.go). It wraps an os/exec.Cmd together with the stdout/stderr
// buffers wired to that process.

// safeBuffer wraps a bytes.Buffer with a mutex so it can be read by
// JobManager.Output while the process is still writing.
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
// Non-positive maxJobs defaults to 25; non-positive retention defaults to 8h.
func NewJobManager(runner ProcessRunner, dir string, maxJobs int, retention time.Duration) *JobManager {
	if maxJobs <= 0 {
		maxJobs = 25
	}
	if retention <= 0 {
		retention = 8 * time.Hour
	}
	return &JobManager{
		runner:    runner,
		dir:       dir,
		maxJobs:   maxJobs,
		retention: retention,
		jobs:      make(map[string]*job),
	}
}

// SetOnChange registers a callback that is invoked whenever the number of
// running jobs changes. The callback must not call back into the JobManager.
// Retained alongside the broker publish in SetBroker until the second
// consumer (F16 steering) lands; then the callback is removed.
func (m *JobManager) SetOnChange(fn func(int)) {
	m.mu.Lock()
	m.onChange = fn
	m.mu.Unlock()
}

// SetBroker wires the F19 pub/sub broker so every change in the running-job
// count publishes a JobEvent. Subscribers (currently the TUI) get the
// notification without polling. Safe to call before any jobs are started.
// Replaces nil safely; a nil broker disables publishing only.
func (m *JobManager) SetBroker(b *pubsub.Broker[JobEvent]) {
	m.mu.Lock()
	m.jobBroker = b
	m.mu.Unlock()
}

// Start launches command as a background job and returns its job ID.
func (m *JobManager) Start(ctx context.Context, command string, timeout time.Duration) (string, error) {
	m.evictCompleted()

	m.mu.Lock()
	if len(m.jobs) >= m.maxJobs {
		m.mu.Unlock()
		return "", fmt.Errorf("too many background jobs (max %d)", m.maxJobs)
	}
	m.nextID++
	id := fmt.Sprintf("job-%d", m.nextID)
	j := &job{
		info: JobInfo{ID: id, Command: command, Status: StatusRunning, StartedAt: time.Now()},
		done: make(chan struct{}),
	}
	m.jobs[id] = j
	m.mu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, timeout)
	j.cancel = cancel

	req := CommandRequest{Command: command, Dir: m.dir, Timeout: timeout}
	rc, err := m.runner.Start(req)
	if err != nil {
		j.mu.Lock()
		j.info.Status = StatusFailed
		j.mu.Unlock()
		m.mu.Lock()
		delete(m.jobs, id)
		m.mu.Unlock()
		m.notifyChange()
		return "", err
	}

	j.mu.Lock()
	j.cmd = rc
	if rc.cmd.Process != nil {
		j.info.PID = rc.cmd.Process.Pid
	}
	j.mu.Unlock()

	go m.wait(j, ctx, rc)
	m.notifyChange()
	return id, nil
}

func (m *JobManager) wait(j *job, ctx context.Context, rc *runningCmd) {
	defer close(j.done)

	done := make(chan error, 1)
	go func() { done <- rc.cmd.Wait() }()

	select {
	case <-ctx.Done():
		killProcessTree(rc)
		<-done
		j.mu.Lock()
		if j.info.Status == StatusRunning {
			j.info.Status = StatusTimedOut
		}
		j.completedAt = time.Now()
		j.mu.Unlock()
	case err := <-done:
		j.mu.Lock()
		if j.info.Status == StatusKilled {
			// keep killed
		} else if err != nil {
			j.info.Status = StatusFailed
		} else {
			j.info.Status = StatusCompleted
			if rc.cmd.ProcessState != nil {
				code := rc.cmd.ProcessState.ExitCode()
				j.info.ExitCode = &code
			}
		}
		j.completedAt = time.Now()
		j.mu.Unlock()
	}
	m.notifyChange()
}

// Output returns the current metadata and buffered stdout/stderr for a job.
// tailLines, when positive, limits the returned output to the last N lines.
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
	j.mu.Unlock()

	combined := j.cmd.stdout.String() + j.cmd.stderr.String()
	if tailLines > 0 {
		combined = tailString(combined, tailLines)
	}
	return info, combined, nil
}

// Kill terminates a running background job.
func (m *JobManager) Kill(id string) error {
	m.evictCompleted()

	m.mu.Lock()
	j, ok := m.jobs[id]
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("job %q not found", id)
	}

	j.mu.Lock()
	j.info.Status = StatusKilled
	j.cancel()
	cmd := j.cmd
	j.mu.Unlock()

	killProcessTree(cmd)
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

// evictCompleted removes terminal jobs whose completedAt timestamp is older
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

// Shutdown cancels and kills every tracked job. It does not wait for them to
// exit.
func (m *JobManager) Shutdown() {
	m.mu.Lock()
	jobs := make([]*job, 0, len(m.jobs))
	for _, j := range m.jobs {
		jobs = append(jobs, j)
	}
	m.mu.Unlock()
	for _, j := range jobs {
		j.mu.Lock()
		j.cancel()
		cmd := j.cmd
		j.mu.Unlock()
		killProcessTree(cmd)
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
