// Package jsonstream implements a loop.Sink that emits NDJSON events for CI/headless mode.
package jsonstream

import (
	"encoding/json"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

// NDJSONSink implements loop.Sink and writes structured events as newline-delimited JSON.
// It is safe for concurrent use.
type NDJSONSink struct {
	w      io.Writer
	mu     sync.Mutex
	enc    *json.Encoder
	sessID string
	prompt string

	// Accumulators for token accounting and response buffering.
	totalPromptTokens     int
	totalCompletionTokens int
	roundResponse         *strings.Builder
	lastVerdict           string
	lastSummary           string
	startTime             time.Time

	// Per-round timing (local-model metrics).
	roundStartTime     time.Time
	firstTokenReceived bool
	ttftMs             int64 // time-to-first-token in milliseconds
}

// NewSink creates an NDJSONSink that writes to w (typically os.Stdout).
func NewSink(w io.Writer, sessionID, prompt string) *NDJSONSink {
	if w == nil {
		w = os.Stdout
	}
	s := &NDJSONSink{
		w:             w,
		enc:           json.NewEncoder(w),
		sessID:        sessionID,
		prompt:        prompt,
		roundResponse: &strings.Builder{},
		startTime:     time.Now(),
	}
	s.emit(eventSessionStart(sessionID, prompt))
	return s
}

// Token accumulates streaming content for the current round.
// Records time-to-first-token on the first chunk.
func (s *NDJSONSink) Token(chunk string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.firstTokenReceived && chunk != "" {
		s.ttftMs = time.Since(s.roundStartTime).Milliseconds()
		s.firstTokenReceived = true
	}
	s.roundResponse.WriteString(chunk)
}

// RoundStart emits the round_start event and resets per-round timing.
func (s *NDJSONSink) RoundStart(taskID string, round, maxRounds int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.roundStartTime = time.Now()
	s.firstTokenReceived = false
	s.ttftMs = 0
	s.emit(eventRoundStart(taskID, round, maxRounds))
}

// LintErrors is emitted as part of round data but not a separate event.
func (s *NDJSONSink) LintErrors(taskID, summary string) {
	// In NDJSON mode, lint errors are part of the round context;
	// we don't emit a separate event but they would be in the round_end response.
}

// FileEditStart emits the file_edit_start event.
func (s *NDJSONSink) FileEditStart(taskID, path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.emit(eventFileEditStart(taskID, path))
}

// FileEditDone emits the file_edit_done event.
func (s *NDJSONSink) FileEditDone(taskID, path string, added, deleted int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.emit(eventFileEditDone(taskID, path, added, deleted))
}

// FileEditFailed emits the file_edit_failed event.
func (s *NDJSONSink) FileEditFailed(taskID, path, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.emit(eventFileEditFailed(taskID, path, reason))
}

func (s *NDJSONSink) PermissionRequest(taskID, toolName, path, preview string, response chan<- bool) {
	// In NDJSON mode, auto-approve (no interactive prompt)
	response <- true
}

func (s *NDJSONSink) ToolOperation(taskID, toolName, path, status, summary string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.emit(eventToolOperation(taskID, toolName, path, status, summary))
}

func (s *NDJSONSink) ToolOperationDetail(taskID, path, content string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.emit(eventToolOperationDetail(taskID, path, content))
}

func (s *NDJSONSink) ThinkBlock(taskID, content string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.emit(eventThinkBlock(taskID, content))
}

func (s *NDJSONSink) ProposalsReady(taskID string, files []string, summary string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.emit(eventProposalsReady(taskID, files, summary))
}

func (s *NDJSONSink) ProposalsApplied(taskID string, files []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.emit(eventProposalsApplied(taskID, files))
}

func (s *NDJSONSink) ProposalsDiscarded(taskID, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.emit(eventProposalsDiscarded(taskID, reason))
}

// VerdictBadge stores the verdict for inclusion in round_end; does not emit immediately.
func (s *NDJSONSink) VerdictBadge(taskID string, verdict, summary string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastVerdict = verdict
	s.lastSummary = summary
}

// RoundEnd emits the round_end event with accumulated response, token counts, and timing metrics.
func (s *NDJSONSink) RoundEnd(taskID string, round, promptTokens, completionTokens int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.totalPromptTokens += promptTokens
	s.totalCompletionTokens += completionTokens

	durationMs := time.Since(s.roundStartTime).Milliseconds()

	// Calculate throughput (tokens per second) from completion tokens and duration.
	var tokensPerSec float64
	if durationMs > 0 && completionTokens > 0 {
		tokensPerSec = float64(completionTokens) / (float64(durationMs) / 1000.0)
	}

	s.emit(eventRoundEnd(taskID, round, s.roundResponse.String(), s.lastVerdict, s.lastSummary,
		promptTokens, completionTokens, s.ttftMs, tokensPerSec))

	// Reset for next round.
	s.roundResponse.Reset()
	s.lastVerdict = ""
	s.lastSummary = ""
	s.firstTokenReceived = false
	s.ttftMs = 0
}

// TaskMerged emits the merged event.
func (s *NDJSONSink) TaskMerged(taskID, stagingSHA string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.emit(eventMerged(taskID, stagingSHA))
}

// TaskFailed emits the session_end event with failure status and exits.
func (s *NDJSONSink) TaskFailed(taskID, lastIssue string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	duration := time.Since(s.startTime).Milliseconds()
	s.emit(eventSessionEnd(s.sessID, "FAIL", s.totalPromptTokens, s.totalCompletionTokens, duration, lastIssue))
}

// SessionSuccess emits the session_end event with success status.
// This is called by the caller after a successful run, since TaskFailed handles failures.
func (s *NDJSONSink) SessionSuccess(stagingSHA string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	duration := time.Since(s.startTime).Milliseconds()
	s.emit(eventSessionEnd(s.sessID, "PASS", s.totalPromptTokens, s.totalCompletionTokens, duration, ""))
}

func (s *NDJSONSink) emit(v any) {
	_ = s.enc.Encode(v)
}

// Event types for NDJSON output.

type event struct {
	Event string `json:"event"`
	TS    string `json:"ts"`
}

type sessionStartEvent struct {
	event
	SessionID string `json:"session_id"`
	Prompt    string `json:"prompt"`
}

type roundStartEvent struct {
	event
	TaskID    string `json:"task_id"`
	Round     int    `json:"round"`
	MaxRounds int    `json:"max_rounds"`
}

type roundEndEvent struct {
	event
	TaskID           string `json:"task_id"`
	Round            int    `json:"round"`
	Response         string `json:"response"`
	Verdict          string `json:"verdict,omitempty"`
	Summary          string `json:"summary,omitempty"`
	PromptTokens     int    `json:"prompt_tokens"`
	CompletionTokens int    `json:"completion_tokens"`
	// Timing fields for local-model benchmarking
	TimeToFirstTokenMs int64   `json:"ttft_ms,omitempty"`        // time-to-first-token
	TokensPerSec       float64 `json:"tokens_per_sec,omitempty"` // throughput estimate
}

type verdictEvent struct {
	event
	TaskID  string `json:"task_id"`
	Verdict string `json:"verdict"`
	Summary string `json:"summary"`
}

type mergedEvent struct {
	event
	TaskID     string `json:"task_id"`
	StagingSHA string `json:"staging_sha"`
}

type sessionEndEvent struct {
	event
	SessionID        string `json:"session_id"`
	Verdict          string `json:"verdict"`
	PromptTokens     int    `json:"prompt_tokens"`
	CompletionTokens int    `json:"completion_tokens"`
	TotalTokens      int    `json:"total_tokens"`
	DurationMS       int64  `json:"duration_ms"`
	Error            string `json:"error,omitempty"`
}

type fileEditStartEvent struct {
	event
	TaskID string `json:"task_id"`
	Path   string `json:"path"`
}

type fileEditDoneEvent struct {
	event
	TaskID  string `json:"task_id"`
	Path    string `json:"path"`
	Added   int    `json:"added"`
	Deleted int    `json:"deleted"`
}

type fileEditFailedEvent struct {
	event
	TaskID string `json:"task_id"`
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

func newEvent(name string) event {
	return event{
		Event: name,
		TS:    time.Now().UTC().Format(time.RFC3339),
	}
}

func eventSessionStart(sessID, prompt string) sessionStartEvent {
	return sessionStartEvent{
		event:     newEvent("session_start"),
		SessionID: sessID,
		Prompt:    prompt,
	}
}

func eventRoundStart(taskID string, round, maxRounds int) roundStartEvent {
	return roundStartEvent{
		event:     newEvent("round_start"),
		TaskID:    taskID,
		Round:     round,
		MaxRounds: maxRounds,
	}
}

func eventRoundEnd(taskID string, round int, response, verdict, summary string, pToks, cToks int, ttftMs int64, tokensPerSec float64) roundEndEvent {
	return roundEndEvent{
		event:              newEvent("round_end"),
		TaskID:             taskID,
		Round:              round,
		Response:           response,
		Verdict:            verdict,
		Summary:            summary,
		PromptTokens:       pToks,
		CompletionTokens:   cToks,
		TimeToFirstTokenMs: ttftMs,
		TokensPerSec:       tokensPerSec,
	}
}

func eventVerdict(taskID, verdict, summary string) verdictEvent {
	return verdictEvent{
		event:   newEvent("verdict"),
		TaskID:  taskID,
		Verdict: verdict,
		Summary: summary,
	}
}

func eventMerged(taskID, stagingSHA string) mergedEvent {
	return mergedEvent{
		event:      newEvent("merged"),
		TaskID:     taskID,
		StagingSHA: stagingSHA,
	}
}

func eventSessionEnd(sessID, verdict string, pToks, cToks int, duration int64, err string) sessionEndEvent {
	return sessionEndEvent{
		event:            newEvent("session_end"),
		SessionID:        sessID,
		Verdict:          verdict,
		PromptTokens:     pToks,
		CompletionTokens: cToks,
		TotalTokens:      pToks + cToks,
		DurationMS:       duration,
		Error:            err,
	}
}

func eventFileEditStart(taskID, path string) fileEditStartEvent {
	return fileEditStartEvent{
		event:  newEvent("file_edit_start"),
		TaskID: taskID,
		Path:   path,
	}
}

func eventFileEditDone(taskID, path string, added, deleted int) fileEditDoneEvent {
	return fileEditDoneEvent{
		event:   newEvent("file_edit_done"),
		TaskID:  taskID,
		Path:    path,
		Added:   added,
		Deleted: deleted,
	}
}

func eventFileEditFailed(taskID, path, reason string) fileEditFailedEvent {
	return fileEditFailedEvent{
		event:  newEvent("file_edit_failed"),
		TaskID: taskID,
		Path:   path,
		Reason: reason,
	}
}

type toolOperationEvent struct {
	event
	TaskID   string `json:"task_id"`
	ToolName string `json:"tool_name"`
	Path     string `json:"path"`
	Status   string `json:"status"`
	Summary  string `json:"summary,omitempty"`
}

type toolOperationDetailEvent struct {
	event
	TaskID  string `json:"task_id"`
	Path    string `json:"path"`
	Content string `json:"content,omitempty"`
}

func eventToolOperation(taskID, toolName, path, status, summary string) toolOperationEvent {
	return toolOperationEvent{
		event:    newEvent("tool_operation"),
		TaskID:   taskID,
		ToolName: toolName,
		Path:     path,
		Status:   status,
		Summary:  summary,
	}
}

func eventToolOperationDetail(taskID, path, content string) toolOperationDetailEvent {
	return toolOperationDetailEvent{
		event:   newEvent("tool_operation_detail"),
		TaskID:  taskID,
		Path:    path,
		Content: content,
	}
}

type thinkBlockEvent struct {
	event
	TaskID  string `json:"task_id"`
	Content string `json:"content"`
}

func eventThinkBlock(taskID, content string) thinkBlockEvent {
	return thinkBlockEvent{
		event:   newEvent("think_block"),
		TaskID:  taskID,
		Content: content,
	}
}

type proposalsReadyEvent struct {
	event
	TaskID  string   `json:"task_id"`
	Files   []string `json:"files"`
	Summary string   `json:"summary"`
}

type proposalsAppliedEvent struct {
	event
	TaskID string   `json:"task_id"`
	Files  []string `json:"files"`
}

type proposalsDiscardedEvent struct {
	event
	TaskID string `json:"task_id"`
	Reason string `json:"reason"`
}

func eventProposalsReady(taskID string, files []string, summary string) proposalsReadyEvent {
	return proposalsReadyEvent{
		event:   newEvent("proposals_ready"),
		TaskID:  taskID,
		Files:   files,
		Summary: summary,
	}
}

func eventProposalsApplied(taskID string, files []string) proposalsAppliedEvent {
	return proposalsAppliedEvent{
		event:  newEvent("proposals_applied"),
		TaskID: taskID,
		Files:  files,
	}
}

func eventProposalsDiscarded(taskID, reason string) proposalsDiscardedEvent {
	return proposalsDiscardedEvent{
		event:  newEvent("proposals_discarded"),
		TaskID: taskID,
		Reason: reason,
	}
}
