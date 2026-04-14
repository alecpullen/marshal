// internal/tui/marshal_adapter.go
// Bridges the Marshal orchestrator with the Bubble Tea TUI.
// The Marshal commands agents; this adapter reflects those commands in the UI.
// Now supports agent-centric conversation model.

package tui

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/alecpullen/marshal/internal/agents/planner"
	"github.com/alecpullen/marshal/internal/config"
	"github.com/alecpullen/marshal/internal/conversation"
	"github.com/alecpullen/marshal/internal/git"
	"github.com/alecpullen/marshal/internal/loop"
	"github.com/alecpullen/marshal/internal/marshal"
	"github.com/alecpullen/marshal/internal/store"
)

// MarshalAdapter bridges the Marshal orchestrator with the TUI.
// It manages conversations and spawns tasks when the Marshal decides.
type MarshalAdapter struct {
	cfg     *config.Config
	store   *store.Store
	program *tea.Program
	ctx     context.Context
	cancel  context.CancelFunc

	// Conversation state
	conversations map[string]*conversation.Conversation
	currentConvID string
	convMu        sync.Mutex

	// Task execution state
	running    bool
	runningMu  sync.Mutex
	taskCancel context.CancelFunc

	// Dependencies loaded at runtime
	skills []loop.Skill

	// tuiSender is set by the TUI program to enable goroutines to send messages
	tuiSender func(msg tea.Msg)
}

// queuedTask represents a task to be executed
type queuedTask struct {
	id   string
	desc string
}

// NewMarshalAdapter creates a new adapter that connects the Marshal to the TUI.
// If initialTask is provided, it starts a conversation with that task.
func NewMarshalAdapter(cfg *config.Config, s *store.Store, initialTask string) *MarshalAdapter {
	ctx, cancel := context.WithCancel(context.Background())

	adapter := &MarshalAdapter{
		cfg:           cfg,
		store:         s,
		ctx:           ctx,
		cancel:        cancel,
		conversations: make(map[string]*conversation.Conversation),
	}

	// Create initial conversation if task provided
	if initialTask != "" {
		conv := conversation.New(conversation.GenerateID())
		adapter.conversations[conv.ID] = conv
		adapter.currentConvID = conv.ID
		// Store conversation in DB
		if s != nil {
			_ = s.CreateConversation(conv.ID)
		}
		// Queue the initial message for processing
		go func() {
			time.Sleep(100 * time.Millisecond) // Let Run() start first
			adapter.processConversationMessage(conv.ID, initialTask)
		}()
	}

	return adapter
}

// Run starts the adapter's event loop.
// It should be called in a goroutine.
func (a *MarshalAdapter) Run(p *tea.Program) {
	a.program = p
	a.tuiSender = func(msg tea.Msg) {
		if a.program != nil {
			a.program.Send(msg)
		}
	}

	// Load skills once at startup
	if skills, err := loop.LoadSkills(a.cfg.RepoRoot); err == nil {
		a.skills = skills
	}

	// Main adapter loop
	for {
		select {
		case <-a.ctx.Done():
			return
		default:
			// Check for pending tasks in conversations
			a.checkPendingTasks()
			time.Sleep(100 * time.Millisecond)
		}
	}
}

// Stop shuts down the adapter.
func (a *MarshalAdapter) Stop() {
	a.cancel()
}

// StartConversation creates a new conversation and sets it as current.
func (a *MarshalAdapter) StartConversation() string {
	a.convMu.Lock()
	defer a.convMu.Unlock()

	conv := conversation.New(conversation.GenerateID())
	a.conversations[conv.ID] = conv
	a.currentConvID = conv.ID

	// Store in DB
	if a.store != nil {
		_ = a.store.CreateConversation(conv.ID)
	}

	return conv.ID
}

// GetCurrentConversation returns the current conversation ID.
func (a *MarshalAdapter) GetCurrentConversation() string {
	a.convMu.Lock()
	defer a.convMu.Unlock()
	return a.currentConvID
}

// SendConversationMessage processes a user message in a conversation.
// This is the primary entry point for user input in agent-centric mode.
func (a *MarshalAdapter) SendConversationMessage(convID, msg string) {
	go a.processConversationMessage(convID, msg)
}

// processConversationMessage runs Marshal.ProcessMessage and sends response to UI.
func (a *MarshalAdapter) processConversationMessage(convID, msg string) {
	a.convMu.Lock()
	conv, exists := a.conversations[convID]
	a.convMu.Unlock()

	if !exists {
		a.send(LogLineMsg{Line: LogLine{Kind: lineError, Content: "conversation not found: " + convID}})
		return
	}

	// Create git layer
	gitImpl, err := git.New(a.cfg.RepoRoot)
	if err != nil {
		a.send(LogLineMsg{Line: LogLine{Kind: lineError, Content: "git init failed: " + err.Error()}})
		return
	}

	// Create Marshal
	m := marshal.New(a.cfg, gitImpl, a.store, a.skills)

	// Process the message
	ctx := context.Background()
	response, err := m.ProcessMessage(ctx, conv, msg)

	if err != nil {
		a.send(LogLineMsg{Line: LogLine{Kind: lineError, Content: "marshal error: " + err.Error()}})
		return
	}

	// Send response to UI
	a.send(MarshalResponseMsg{Response: response})

	// Store messages in DB
	if a.store != nil {
		intent := response.Intent.String()
		var taskID string
		if response.ExecuteTask != nil {
			taskID = *response.ExecuteTask
		}
		_, _ = a.store.AddMessage(convID, "user", msg, intent, taskID)
		_, _ = a.store.AddMessage(convID, "marshal", response.Content, response.Type.String(), taskID)
	}

	// Handle immediate task execution
	if response.ExecuteTask != nil {
		a.SubmitTask(conv.ID+"-task-1", *response.ExecuteTask)
	}
}

// checkPendingTasks looks for conversations with pending tasks and executes them.
func (a *MarshalAdapter) checkPendingTasks() {
	a.convMu.Lock()
	defer a.convMu.Unlock()

	for _, conv := range a.conversations {
		if len(conv.PendingTasks) > 0 && conv.CanAutoExecute() {
			// Execute pending tasks
			for _, task := range conv.PendingTasks {
				a.SubmitTask(task.ID, task.Description)
			}
			conv.PendingTasks = nil
		}
	}
}

// SubmitTask adds a task to be executed.
// Tasks from conversations are submitted here when Marshal decides to execute.
func (a *MarshalAdapter) SubmitTask(id, description string) {
	a.runningMu.Lock()
	defer a.runningMu.Unlock()

	if !a.running {
		go a.runTask(queuedTask{id: id, desc: description})
	}
}

// CancelTask cancels the currently running task, if any.
func (a *MarshalAdapter) CancelTask() bool {
	a.runningMu.Lock()
	defer a.runningMu.Unlock()

	if a.taskCancel != nil {
		a.taskCancel()
		return true
	}
	return false
}

// IsRunning returns true if a task is currently executing.
func (a *MarshalAdapter) IsRunning() bool {
	a.runningMu.Lock()
	defer a.runningMu.Unlock()
	return a.running
}

// runTask executes a single task through the Marshal.
func (a *MarshalAdapter) runTask(task queuedTask) {
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

	// Create git layer
	gitImpl, err := git.New(a.cfg.RepoRoot)
	if err != nil {
		a.send(LogLineMsg{Line: LogLine{Kind: lineError, Content: "git init failed: " + err.Error()}})
		a.send(TaskCompleteMsg{ID: task.id, Passed: false})
		return
	}

	// Create Marshal
	m := marshal.New(a.cfg, gitImpl, a.store, a.skills)

	// Send Marshal planning message
	a.send(LogLineMsg{Line: LogLine{Kind: lineMarshal, Content: "analyzing task..."}})
	a.send(LogLineMsg{Line: LogLine{Kind: lineMarshal, Content: fmt.Sprintf("task: %s", truncate(task.desc, 50))}})

	// Streaming callbacks — split on newlines so a full non-streaming response
	// doesn't arrive as one truncated line.
	onExecutorChunk := func(content string) {
		for _, line := range strings.Split(content, "\n") {
			if strings.TrimSpace(line) != "" {
				a.send(LogLineMsg{Line: LogLine{Kind: lineExec, Content: line}})
			}
		}
	}
	onCriticChunk := func(content string) {
		for _, line := range strings.Split(content, "\n") {
			if strings.TrimSpace(line) != "" {
				a.send(LogLineMsg{Line: LogLine{Kind: lineCritic, Content: line}})
			}
		}
	}
	onThinkBlock := func(content string) {
		a.send(ThinkBlockMsg{Content: content})
	}

	// Execute through Marshal with streaming
	result, err := m.ExecuteTaskStreaming(taskCtx, task.desc, onExecutorChunk, onCriticChunk, onThinkBlock)

	// Determine final status
	passed := err == nil && result.Status == "SUCCESS"
	var sha string
	if passed {
		sha = result.SHA
	}

	// Send completion message
	a.send(TaskCompleteMsg{ID: task.id, Passed: passed, SHA: sha})
}

// send sends a message to the TUI program.
func (a *MarshalAdapter) send(msg tea.Msg) {
	if a.program != nil {
		a.program.Send(msg)
	}
}

// SetTUISender sets the function to send messages to the TUI program.
// This must be called by the TUI program after creating the adapter.
func (a *MarshalAdapter) SetTUISender(sender func(msg tea.Msg)) {
	a.tuiSender = sender
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

// truncate returns a string truncated to max length with ellipsis.
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}

// Backward compatibility: LoopAdapter is an alias for MarshalAdapter
type LoopAdapter = MarshalAdapter

// NewLoopAdapter is an alias for NewMarshalAdapter
var NewLoopAdapter = NewMarshalAdapter

// TaskRunner handles parallel task execution (placeholder for future)
type TaskRunner struct {
	planner *planner.Planner
	tasks   []queuedTask
}
