// Package bridge provides adapters to map Marshal's internal types to the
// swarm protocol types (SwarmEvent, ContextRef, etc.).
// This enables gradual migration from Marshal's event system to the swarm event system.
package bridge

import (
	"encoding/json"

	"github.com/alecpullen/marshal/internal/events"
	"github.com/alecpullen/marshal/internal/pipeline"
	"github.com/alecpullen/marshal/internal/session"
	"github.com/alecpullen/marshal/pkg/protocol"
)

// MarshalBridge adapts Marshal's internal types to SwarmEvents.
type MarshalBridge struct {
	bus       *events.Bus
	sessionID string
}

// NewMarshalBridge creates a new bridge for a session.
func NewMarshalBridge(bus *events.Bus, sessionID string) *MarshalBridge {
	return &MarshalBridge{
		bus:       bus,
		sessionID: sessionID,
	}
}

// SessionToSwarm creates a SwarmEvent from Marshal's Session.
func (b *MarshalBridge) SessionToSwarm(sess *session.Session) protocol.SwarmEvent {
	// Sessions are generally not published as events, but we can create one
	// for session lifecycle events.
	event := protocol.NewEvent(protocol.EventTaskSpawned, b.sessionID)
	payload := map[string]interface{}{
		"session_id":       sess.ID,
		"target_branch":    sess.TargetBranch,
		"staging_branch":   sess.StagingBranch,
		"target_start_sha": sess.TargetStartSHA,
	}
	event, _ = event.WithPayload(payload)
	return event
}

// TaskToSwarm converts a Marshal Task to SwarmEvents.
// Returns multiple events: spawn, progress (if running), completion/failure.
func (b *MarshalBridge) TaskToSwarm(task *session.Task) []protocol.SwarmEvent {
	var events []protocol.SwarmEvent

	// Task spawned event
	spawnEvent := protocol.NewEvent(protocol.EventTaskSpawned, b.sessionID)
	spawnPayload := protocol.MarshalTaskPayload{
		TaskID: task.ID,
		Prompt: task.Prompt,
		Status: string(task.Status),
	}
	spawnEvent, _ = spawnEvent.WithPayload(spawnPayload)
	events = append(events, spawnEvent)

	// Status-specific events
	switch task.Status {
	case session.StatusPassed:
		completedEvent := protocol.NewEvent(protocol.EventTaskCompleted, b.sessionID)
		completedPayload := protocol.TaskCompletedPayload{
			TaskID:   task.ID,
			Duration: taskDuration(task),
		}
		completedEvent, _ = completedEvent.WithPayload(completedPayload)
		events = append(events, completedEvent)

	case session.StatusFailed:
		failedEvent := protocol.NewEvent(protocol.EventTaskFailed, b.sessionID)
		failedPayload := protocol.TaskFailedPayload{
			TaskID: task.ID,
			Error:  getTaskSummary(task),
		}
		failedEvent, _ = failedEvent.WithPayload(failedPayload)
		events = append(events, failedEvent)

	case session.StatusRunning:
		progressEvent := protocol.NewEvent(protocol.EventTaskProgress, b.sessionID)
		progressPayload := protocol.TaskProgressPayload{
			TaskID:  task.ID,
			Status:  "running",
			Message: "Task is being executed",
		}
		progressEvent, _ = progressEvent.WithPayload(progressPayload)
		events = append(events, progressEvent)
	}

	return events
}

// RoundToSwarm converts a Marshal Round to a SwarmEvent.
func (b *MarshalBridge) RoundToSwarm(round *session.Round) protocol.SwarmEvent {
	event := protocol.NewEvent(protocol.EventModelCall, b.sessionID)

	var verdict string
	if round.VerdictJSON != nil {
		var v map[string]interface{}
		if err := json.Unmarshal([]byte(*round.VerdictJSON), &v); err == nil {
			if vstr, ok := v["verdict"].(string); ok {
				verdict = vstr
			}
		}
	}

	payload := protocol.MarshalRoundPayload{
		RoundID:          round.Round,
		TaskID:           round.TaskID,
		Role:             round.Role,
		Model:            round.Model,
		PromptTokens:     round.PromptTokens,
		CompletionTokens: round.CompletionTokens,
		Verdict:          verdict,
		ContentPreview:   truncate(round.Content, 200),
	}

	event, _ = event.WithPayload(payload)
	return event
}

// PipelineTaskToSwarm converts a PipelineTask to a TaskSpec.
func (b *MarshalBridge) PipelineTaskToSwarm(pt *pipeline.PipelineTask) *pipeline.TaskSpec {
	return pipeline.FromPipelineTask(pt)
}

// TaskSpecToSwarmEvents generates SwarmEvents from a TaskSpec.
func (b *MarshalBridge) TaskSpecToSwarmEvents(spec *pipeline.TaskSpec) []protocol.SwarmEvent {
	var events []protocol.SwarmEvent

	// Spawn event
	spawnEvent := protocol.NewEvent(protocol.EventTaskSpawned, b.sessionID)
	spawnPayload := protocol.TaskSpawnedPayload{
		TaskID:       spec.ID,
		Role:         spec.Role,
		Goal:         spec.Goal,
		ParentIDs:    spec.DependsOn,
		OutputSchema: spec.OutputSchema,
	}
	spawnEvent, _ = spawnEvent.WithPayload(spawnPayload)
	events = append(events, spawnEvent)

	// Current status event
	switch spec.Status {
	case pipeline.TaskRunning:
		progressEvent := protocol.NewEvent(protocol.EventTaskProgress, b.sessionID)
		progressPayload := protocol.TaskProgressPayload{
			TaskID: spec.ID,
			Status: "running",
		}
		progressEvent, _ = progressEvent.WithPayload(progressPayload)
		events = append(events, progressEvent)
	}

	return events
}

// PublishTask publishes Marshal Task events to the bus.
func (b *MarshalBridge) PublishTask(task *session.Task) {
	events := b.TaskToSwarm(task)
	for _, e := range events {
		b.bus.Publish(e)
	}
}

// PublishRound publishes Marshal Round events to the bus.
func (b *MarshalBridge) PublishRound(round *session.Round) {
	event := b.RoundToSwarm(round)
	b.bus.Publish(event)
}

// PublishTaskSpec publishes TaskSpec events to the bus.
func (b *MarshalBridge) PublishTaskSpec(spec *pipeline.TaskSpec) {
	events := b.TaskSpecToSwarmEvents(spec)
	for _, e := range events {
		b.bus.Publish(e)
	}
}

// --- Publisher factory ---

// NewPublisher creates an events.Publisher for this session.
func (b *MarshalBridge) NewPublisher(agentID string) *events.Publisher {
	return events.NewPublisher(b.bus, b.sessionID, agentID)
}

// --- Helper functions ---

func taskDuration(task *session.Task) int64 {
	if task.EndedAt == nil || task.StartedAt.IsZero() {
		return 0
	}
	return task.EndedAt.Sub(task.StartedAt).Milliseconds()
}

func getTaskSummary(task *session.Task) string {
	if task.Summary != nil {
		return *task.Summary
	}
	return "Task " + string(task.Status)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// --- Sink adapter ---

// SinkAdapter wraps an events.Publisher to implement Marshal's Sink interface.
// This allows the existing loop.Engine to emit SwarmEvents without modification.
type SinkAdapter struct {
	publisher  *events.Publisher
	agentID    string
	taskID     string
	onFileRead func(path string, content []byte) // Callback for tracking read files
}

// NewSinkAdapter creates a sink adapter.
func NewSinkAdapter(publisher *events.Publisher, agentID, taskID string) *SinkAdapter {
	return &SinkAdapter{
		publisher: publisher,
		agentID:   agentID,
		taskID:    taskID,
	}
}

// SetFileReadCallback sets a callback for tracking file reads.
func (s *SinkAdapter) SetFileReadCallback(cb func(path string, content []byte)) {
	s.onFileRead = cb
}

// Token emits a model token chunk (forwarded as EventModelChunk).
func (s *SinkAdapter) Token(token string) {
	event := protocol.NewEvent(protocol.EventModelChunk, s.publisher.SessionID())
	payload := protocol.ModelChunkPayload{
		CallID:  s.taskID,
		Content: token,
	}
	event, _ = event.WithPayload(payload)
	s.publisher.PublishRaw(event)
}

// RoundStart emits a task progress event.
func (s *SinkAdapter) RoundStart(taskID string, round, maxRounds int) {
	s.publisher.TaskProgress(taskID, "running",
		formatRoundMessage(round, maxRounds), float64(round)/float64(maxRounds))
}

// RoundEnd emits cost update after a round.
func (s *SinkAdapter) RoundEnd(taskID string, round, totalTokens int) {
	// Cost update is handled separately by the cost tracker
}

// VerdictBadge emits a task completion event for a verdict.
func (s *SinkAdapter) VerdictBadge(taskID, verdict, summary string) {
	if verdict == "PASS" {
		s.publisher.TaskCompleted(taskID, map[string]string{
			"verdict": verdict,
			"summary": summary,
		}, 0, protocol.CostInfo{})
	} else {
		s.publisher.TaskFailed(taskID, summary, "", verdict == "FAIL")
	}
}

// TaskMerged emits a task completed event.
func (s *SinkAdapter) TaskMerged(taskID, sha string) {
	s.publisher.TaskCompleted(taskID, map[string]string{
		"merged_sha": sha,
	}, 0, protocol.CostInfo{})
}

// TaskFailed emits a task failed event.
func (s *SinkAdapter) TaskFailed(taskID, issue string) {
	s.publisher.TaskFailed(taskID, issue, "", false)
}

// FileEditDone can emit an edit applied event.
func (s *SinkAdapter) FileEditDone(sessionID, path string, added, deleted int) {
	// Could emit EventEditApplied here if needed
}

// FileEditFailed can emit an edit rejected event.
func (s *SinkAdapter) FileEditFailed(sessionID, path, err string) {
	// Could emit EventEditRejected here if needed
}

// LintErrors emits as task progress (with lint issues).
func (s *SinkAdapter) LintErrors(taskID, summary string) {
	s.publisher.TaskProgress(taskID, "running", "Lint errors: "+summary, 0)
}

// ThinkBlock emits as task progress (thinking block captured).
func (s *SinkAdapter) ThinkBlock(sessionID, content string) {
	// This could be a special event type in the future
	s.publisher.TaskProgress(s.taskID, "thinking", "Thinking...", 0)
}

// ToolOperation emits as tool call event.
func (s *SinkAdapter) ToolOperation(sessionID, tool, target, status, details string) {
	// This is a simplified version - full implementation would track call IDs
}

// PermissionRequest is not emitted (user interaction).
func (s *SinkAdapter) PermissionRequest(sessionID, tool, path, preview string, response chan<- bool) {
	// User interactions are not emitted as events
}

// formatRoundMessage creates a human-readable round message.
func formatRoundMessage(round, max int) string {
	if max > 1 {
		return format("Round %d/%d", round, max)
	}
	return "Running"
}

// format is a simple formatting helper to avoid importing fmt for simple cases.
func format(template string, args ...interface{}) string {
	// Simple placeholder replacement
	result := template
	for i, arg := range args {
		placeholder := formatPlaceholder(i)
		result = replaceFirst(result, placeholder, toString(arg))
	}
	return result
}

func formatPlaceholder(i int) string {
	if i == 0 {
		return "%d"
	}
	return "%d"
}

func toString(v interface{}) string {
	switch val := v.(type) {
	case int:
		return itoa(val)
	case string:
		return val
	default:
		return ""
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [32]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

func replaceFirst(s, old, new string) string {
	for i := 0; i < len(s)-len(old)+1; i++ {
		if s[i:i+len(old)] == old {
			return s[:i] + new + s[i+len(old):]
		}
	}
	return s
}
