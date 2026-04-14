// Package tui implements the Bubbletea TUI for the chat command.
package tui

import (
	tea "github.com/charmbracelet/bubbletea"
)

// The following tea.Msg types are sent from background goroutines to the
// Bubbletea program via program.Send().

// TokenMsg carries a single streaming token from the executor.
type TokenMsg struct{ Content string }

// RoundStartMsg signals the beginning of a new executor round.
type RoundStartMsg struct {
	TaskID    string
	Round     int
	MaxRounds int
}

// VerdictMsg carries the critic verdict.
type VerdictMsg struct {
	TaskID  string
	Verdict string // "PASS" or "FAIL"
	Summary string
}

// TaskMergedMsg signals a successful task merge into staging.
type TaskMergedMsg struct {
	TaskID     string
	StagingSHA string
}

// TaskFailedMsg signals task exhaustion.
type TaskFailedMsg struct {
	TaskID    string
	LastIssue string
}

// RoundEndMsg signals the end of a round with token counts.
type RoundEndMsg struct {
	TaskID           string
	Round            int
	PromptTokens     int
	CompletionTokens int
}

// LintErrorsMsg is sent when the linter finds issues after applying edits.
type LintErrorsMsg struct {
	TaskID  string
	Summary string // multi-line list of lint issues
}

// TaskDoneMsg is sent when the engine goroutine finishes (pass, fail, or error).
type TaskDoneMsg struct{ Err error }

// FileEditStartMsg is sent when the engine begins writing a file.
type FileEditStartMsg struct {
	TaskID string
	Path   string
}

// FileEditDoneMsg is sent after a file is successfully written.
type FileEditDoneMsg struct {
	TaskID  string
	Path    string
	Added   int
	Deleted int
}

// FileEditFailedMsg is sent when a file edit fails (e.g., search/replace mismatch).
type FileEditFailedMsg struct {
	TaskID string
	Path   string
	Reason string
}

// PermissionRequestMsg is sent when the engine needs user confirmation before editing.
type PermissionRequestMsg struct {
	TaskID   string
	ToolName string // "write_file", "read_file", "run_command", "search_replace", etc.
	Path     string // file path being operated on (empty for run_command)
	Preview  string // brief preview of the change (e.g., "+5/-3 lines" or truncated content)
	// Response channel - sends true for "yes", false for "no"
	Response chan<- bool
}

// ToolOperationMsg is sent to show a compact tool operation indicator.
type ToolOperationMsg struct {
	TaskID   string
	ToolName string // "write_file", "read_file", "run_command"
	Path     string // file path or command being executed
	Status   string // "pending", "running", "done", "failed"
	Summary  string // brief summary (e.g., "+5/-3" for edits, or "1.2KB" for reads)
}

// ToolOperationDetailMsg carries the full diff/content for expandable view.
type ToolOperationDetailMsg struct {
	TaskID  string
	Path    string
	Content string
}

// ThinkBlockMsg carries a thinking/reasoning block from the executor.
// This enables the marshal model to provide real-time summaries.
type ThinkBlockMsg struct {
	TaskID  string
	Content string // The thinking content (may be partial during streaming)
	Done    bool   // True when this is the final chunk of the think block
}

// ProposalsReadyMsg signals that file changes have been proposed and are awaiting critic review.
type ProposalsReadyMsg struct {
	TaskID  string
	Files   []string // list of files with proposed changes
	Summary string   // summary of changes
}

// ProposalsAppliedMsg signals that proposals have been applied after critic approval.
type ProposalsAppliedMsg struct {
	TaskID string
	Files  []string // list of files that were applied
}

// ProposalsDiscardedMsg signals that proposals were rejected and discarded.
type ProposalsDiscardedMsg struct {
	TaskID string
	Reason string // reason for discard
}

// ShellResultMsg carries the output of a shell command run by a TUI handler.
type ShellResultMsg struct {
	Label  string // display label, e.g. "$ ls -la" or "/diff"
	Output string
	Err    error
}

// EditorResultMsg carries text from an external editor session.
type EditorResultMsg struct {
	Text string
	Err  error
}

// MapRefreshedMsg signals that the repo map has been rebuilt.
type MapRefreshedMsg struct {
	Map string
	Err error
}

// MarshalGateMsg is sent after the Marshal model has classified a prompt.
//
//   - Action "proceed"  → forward to executor-critic loop
//   - Action "chat"     → display Text as a conversational reply; no loop
//   - Action "clarify"  → display Text as a clarifying question; unlock input
type MarshalGateMsg struct {
	Action string // "proceed" | "chat" | "clarify"
	Text   string // empty for "proceed"
	Prompt string // original user prompt, echoed so Update can forward it
	Err    error
}

// ChanSink implements loop.Sink by sending tea.Msgs to a Bubbletea program.
// Safe to call from any goroutine.
type ChanSink struct {
	prog *tea.Program
}

// NewChanSink creates a ChanSink that dispatches to prog.
func NewChanSink(prog *tea.Program) *ChanSink {
	return &ChanSink{prog: prog}
}

func (s *ChanSink) Token(chunk string) { s.prog.Send(TokenMsg{Content: chunk}) }
func (s *ChanSink) RoundStart(id string, round, max int) {
	s.prog.Send(RoundStartMsg{TaskID: id, Round: round, MaxRounds: max})
}
func (s *ChanSink) VerdictBadge(id, verdict, summary string) {
	s.prog.Send(VerdictMsg{TaskID: id, Verdict: verdict, Summary: summary})
}
func (s *ChanSink) TaskMerged(id, sha string) {
	s.prog.Send(TaskMergedMsg{TaskID: id, StagingSHA: sha})
}
func (s *ChanSink) LintErrors(id, summary string) {
	s.prog.Send(LintErrorsMsg{TaskID: id, Summary: summary})
}
func (s *ChanSink) TaskFailed(id, issue string) {
	s.prog.Send(TaskFailedMsg{TaskID: id, LastIssue: issue})
}
func (s *ChanSink) RoundEnd(id string, round, promptTokens, completionTokens int) {
	s.prog.Send(RoundEndMsg{TaskID: id, Round: round, PromptTokens: promptTokens, CompletionTokens: completionTokens})
}

func (s *ChanSink) FileEditStart(id, path string) {
	s.prog.Send(FileEditStartMsg{TaskID: id, Path: path})
}

func (s *ChanSink) FileEditDone(id, path string, added, deleted int) {
	s.prog.Send(FileEditDoneMsg{TaskID: id, Path: path, Added: added, Deleted: deleted})
}

func (s *ChanSink) FileEditFailed(id, path, reason string) {
	s.prog.Send(FileEditFailedMsg{TaskID: id, Path: path, Reason: reason})
}

func (s *ChanSink) PermissionRequest(id, toolName, path, preview string, response chan<- bool) {
	s.prog.Send(PermissionRequestMsg{TaskID: id, ToolName: toolName, Path: path, Preview: preview, Response: response})
}

func (s *ChanSink) ToolOperation(id, toolName, path, status, summary string) {
	s.prog.Send(ToolOperationMsg{TaskID: id, ToolName: toolName, Path: path, Status: status, Summary: summary})
}

func (s *ChanSink) ToolOperationDetail(id, path, content string) {
	s.prog.Send(ToolOperationDetailMsg{TaskID: id, Path: path, Content: content})
}

func (s *ChanSink) ThinkBlock(id, content string) {
	s.prog.Send(ThinkBlockMsg{TaskID: id, Content: content})
}

func (s *ChanSink) ProposalsReady(id string, files []string, summary string) {
	s.prog.Send(ProposalsReadyMsg{TaskID: id, Files: files, Summary: summary})
}

func (s *ChanSink) ProposalsApplied(id string, files []string) {
	s.prog.Send(ProposalsAppliedMsg{TaskID: id, Files: files})
}

func (s *ChanSink) ProposalsDiscarded(id string, reason string) {
	s.prog.Send(ProposalsDiscardedMsg{TaskID: id, Reason: reason})
}
