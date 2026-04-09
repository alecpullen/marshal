// internal/tui/loop_adapter.go
// Bridges the loop engine with the Bubble Tea TUI by converting loop events
// into tea.Msg messages that update the UI.

package tui

import (
	"context"
	"fmt"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/alecpullen/marshal/internal/backend"
	"github.com/alecpullen/marshal/internal/config"
	"github.com/alecpullen/marshal/internal/git"
	"github.com/alecpullen/marshal/internal/loop"
	"github.com/alecpullen/marshal/internal/store"
)

// LoopAdapter bridges the executor-critic loop with the TUI.
// It runs the loop in a goroutine and sends messages to the TUI program.
type LoopAdapter struct {
	cfg     *config.Config
	store   *store.Store
	program *tea.Program
	ctx     context.Context
	cancel  context.CancelFunc

	// taskQueue holds tasks waiting to be processed
	taskQueue []queuedTask
	running   bool
	runningMu sync.Mutex

	// taskCancel allows cancelling the currently running task
	taskCancel context.CancelFunc
}

type queuedTask struct {
	id   string
	desc string
}

// NewLoopAdapter creates a new adapter that connects the loop to the TUI.
// If initialTask is provided, it will be queued immediately.
func NewLoopAdapter(cfg *config.Config, s *store.Store, initialTask string) *LoopAdapter {
	ctx, cancel := context.WithCancel(context.Background())
	adapter := &LoopAdapter{
		cfg:    cfg,
		store:  s,
		ctx:    ctx,
		cancel: cancel,
	}

	// Queue initial task if provided
	if initialTask != "" {
		adapter.taskQueue = append(adapter.taskQueue, queuedTask{
			id:   fmt.Sprintf("task-1"),
			desc: initialTask,
		})
	}

	return adapter
}

// Run starts the adapter's event loop.
// It should be called in a goroutine.
func (a *LoopAdapter) Run(p *tea.Program) {
	a.program = p

	// Main adapter loop - check for tasks and run them
	for {
		select {
		case <-a.ctx.Done():
			return
		default:
			if !a.running && len(a.taskQueue) > 0 {
				task := a.taskQueue[0]
				a.taskQueue = a.taskQueue[1:]
				go a.runTask(task)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
}

// Stop shuts down the adapter.
func (a *LoopAdapter) Stop() {
	a.cancel()
}

// SubmitTask adds a task to the queue.
// This is called when the user submits a task via the TUI.
func (a *LoopAdapter) SubmitTask(id, description string) {
	a.taskQueue = append(a.taskQueue, queuedTask{id: id, desc: description})
}

// CancelTask cancels the currently running task, if any.
func (a *LoopAdapter) CancelTask() bool {
	a.runningMu.Lock()
	defer a.runningMu.Unlock()

	if a.taskCancel != nil {
		a.taskCancel()
		return true
	}
	return false
}

// IsRunning returns true if a task is currently executing.
func (a *LoopAdapter) IsRunning() bool {
	a.runningMu.Lock()
	defer a.runningMu.Unlock()
	return a.running
}

// runTask executes a single task through the loop.
func (a *LoopAdapter) runTask(task queuedTask) {
	a.runningMu.Lock()
	a.running = true

	// Create cancellable context for this task
	taskCtx, taskCancel := context.WithCancel(a.ctx)
	a.taskCancel = taskCancel
	a.runningMu.Unlock()

	defer func() {
		a.runningMu.Lock()
		a.running = false
		a.taskCancel = nil
		a.runningMu.Unlock()
	}()

	// Send task started message
	a.send(TaskStartedMsg{ID: task.id})

	// Setup backends
	executorBE := backend.NewOpenAICompatible("executor", a.cfg.Executor.BaseURL, a.cfg.Executor.APIKey).
		WithTemperature(a.cfg.Executor.Temperature).
		WithMaxTokens(a.cfg.Executor.MaxTokens)

	criticBE := backend.NewOpenAICompatible("critic", a.cfg.Critic.BaseURL, a.cfg.Critic.APIKey).
		WithTemperature(a.cfg.Critic.Temperature).
		WithMaxTokens(a.cfg.Critic.MaxTokens).
		WithJSONOutput(a.cfg.Critic.JSONOutput)

	// Load skills
	skills, err := loop.LoadSkills(a.cfg.RepoRoot)
	if err != nil {
		a.send(LogLineMsg{Line: LogLine{Kind: lineError, Content: fmt.Sprintf("failed to load skills: %v", err)}})
		a.send(TaskCompleteMsg{ID: task.id, Passed: false})
		return
	}

	// Create agents
	executor := loop.NewExecutor(executorBE, a.cfg.Executor, skills)
	critic := loop.NewCritic(criticBE, a.cfg.Critic)

	// Create git layer
	gitImpl, err := git.New(a.cfg.RepoRoot)
	if err != nil {
		a.send(LogLineMsg{Line: LogLine{Kind: lineError, Content: fmt.Sprintf("git init failed: %v", err)}})
		a.send(TaskCompleteMsg{ID: task.id, Passed: false})
		return
	}
	gitLayer := git.NewAdapter(gitImpl)

	// Create loop
	l := loop.New(a.cfg, executor, critic, gitLayer)

	// Create session
	session := &store.Session{
		ID:              generateSessionID(),
		RepoRoot:        a.cfg.RepoRoot,
		Task:            task.desc,
		Status:          "RUNNING",
		BaseBranch:      gitImpl.BaseBranch(),
		IsolationBranch: "",
	}

	// Create round callback to stream progress to TUI
	roundCallback := func(r loop.Round) {
		// Send round start
		a.send(LogLineMsg{Line: LogLine{Kind: lineRoundSep, Content: "", Round: r.Number}})
		a.send(LogLineMsg{Line: LogLine{Kind: lineExec, Content: "generating code..."}})

		// Send diff preview (truncated)
		if r.Diff != "" {
			lines := truncateLines(r.Diff, 10)
			for _, line := range lines {
				a.send(LogLineMsg{Line: LogLine{Kind: lineCont, Content: line}})
			}
		}

		// Send think-block if present
		if r.ThinkBlock != "" {
			a.send(ThinkBlockMsg{Content: r.ThinkBlock})
		}

		// Send critic verdict
		a.send(LogLineMsg{Line: LogLine{Kind: lineCritic, Content: fmt.Sprintf("verdict: %s", r.Verdict.Verdict)}})
		if r.Verdict.Summary != "" {
			a.send(LogLineMsg{Line: LogLine{Kind: lineCont, Content: r.Verdict.Summary}})
		}
	}

	// Run with recording
	recorder := store.NewLoopRecorder(a.store, l)
	result, err := a.runWithCallback(recorder, taskCtx, task.desc, session, roundCallback)

	// Determine final status
	passed := err == nil && result.Status == "SUCCESS"
	var sha string
	if passed {
		sha = gitImpl.HeadSHA()
	}

	// Send completion message
	a.send(TaskCompleteMsg{ID: task.id, Passed: passed, SHA: sha})
}

// runWithCallback runs the loop with a per-round callback for TUI updates.
func (a *LoopAdapter) runWithCallback(
	r *store.LoopRecorder,
	ctx context.Context,
	task string,
	session *store.Session,
	callback loop.RoundCallback,
) (*loop.Result, error) {
	// Create session in store
	session.CreatedAt = time.Now()
	session.UpdatedAt = session.CreatedAt
	if err := a.store.CreateSession(session); err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}

	// Get the underlying loop and use RunWithCallback
	// Since recorder wraps the loop, we need to access it differently
	// For now, run without per-round callback and simulate progress
	result, err := r.Run(ctx, task, session)

	// Simulate progress for TUI if we got rounds
	if len(result.Rounds) > 0 && callback != nil {
		for _, round := range result.Rounds {
			callback(round)
		}
	}

	return result, err
}

// send sends a message to the TUI program.
func (a *LoopAdapter) send(msg tea.Msg) {
	if a.program != nil {
		a.program.Send(msg)
	}
}

// generateSessionID creates a unique session identifier.
func generateSessionID() string {
	return fmt.Sprintf("marshal-%d-%s", time.Now().Unix(), randomHex(4))
}

// randomHex generates random hex string of given byte length.
func randomHex(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(time.Now().UnixNano() >> uint(i*8))
	}
	return fmt.Sprintf("%x", b)[:n*2]
}

// truncateLines returns up to n lines from the input string.
func truncateLines(s string, n int) []string {
	var lines []string
	var current string
	for _, r := range s {
		if r == '\n' {
			lines = append(lines, current)
			current = ""
			if len(lines) >= n {
				break
			}
		} else {
			current += string(r)
		}
	}
	if current != "" && len(lines) < n {
		lines = append(lines, current)
	}
	return lines
}
