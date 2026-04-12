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

// LintErrorsMsg is sent when the linter finds issues after applying edits.
type LintErrorsMsg struct {
	TaskID  string
	Summary string // multi-line list of lint issues
}

// TaskDoneMsg is sent when the engine goroutine finishes (pass, fail, or error).
type TaskDoneMsg struct{ Err error }

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
