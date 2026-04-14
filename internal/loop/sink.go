package loop

import "fmt"

// Sink receives events from the Engine as a task runs. M3 uses StdoutSink;
// M4 replaces it with a bubbletea channel emitter.
type Sink interface {
	// Token is called for each streaming text chunk from the executor.
	Token(chunk string)
	// RoundStart is called at the beginning of each executor round.
	RoundStart(taskID string, round, maxRounds int)
	// LintErrors is called when the linter finds issues after applying edits.
	// summary is a human-readable multi-line list of the issues.
	LintErrors(taskID, summary string)
	// VerdictBadge is called once the critic verdict is parsed.
	VerdictBadge(taskID string, verdict, summary string)
	// RoundEnd is called at the end of each round with token counts.
	RoundEnd(taskID string, round, promptTokens, completionTokens int)
	// TaskMerged is called after a PASS squash-merge into the staging branch.
	TaskMerged(taskID, stagingSHA string)
	// TaskFailed is called when all rounds are exhausted.
	TaskFailed(taskID, lastIssue string)
	// FileEditStart is called when beginning to write a file.
	FileEditStart(taskID, path string)
	// FileEditDone is called after successfully writing a file.
	FileEditDone(taskID, path string, added, deleted int)
	// FileEditFailed is called when a file edit fails (e.g., search/replace mismatch).
	FileEditFailed(taskID, path, reason string)
	// PermissionRequest asks the user for confirmation before executing an operation.
	// The response is sent through the response channel (true = yes, false = no).
	PermissionRequest(taskID, toolName, path, preview string, response chan<- bool)
	// ToolOperation shows a compact tool operation indicator (e.g., "Editing (write_file) /path/to/file").
	ToolOperation(taskID, toolName, path, status, summary string)
	// ToolOperationDetail carries the full diff/content for expandable view.
	ToolOperationDetail(taskID, path, content string)
	// ThinkBlock is called when a thinking/reasoning block is detected in the executor output.
	// This allows the marshal model to provide real-time summaries of what the executor is doing.
	ThinkBlock(taskID, content string)
	// ThinkBlockDone is called when the thinking/reasoning block is complete.
	ThinkBlockDone(taskID string)
	// ProposalsReady is called when file changes have been proposed and are awaiting critic review.
	ProposalsReady(taskID string, files []string, summary string)
	// ProposalsApplied is called after proposals have been applied following critic approval.
	ProposalsApplied(taskID string, files []string)
	// ProposalsDiscarded is called when proposals are rejected and discarded.
	ProposalsDiscarded(taskID string, reason string)
}

// StdoutSink writes events to stdout with simple text formatting.
type StdoutSink struct{}

func (StdoutSink) Token(chunk string) {
	fmt.Print(chunk)
}

func (StdoutSink) RoundStart(taskID string, round, maxRounds int) {
	if round > 1 {
		fmt.Printf("\n[task %s] retrying (round %d/%d)\n", taskID, round, maxRounds)
	}
}

func (StdoutSink) LintErrors(taskID, summary string) {
	fmt.Printf("\n[task %s] lint errors:\n%s\n", taskID, summary)
}

func (StdoutSink) VerdictBadge(taskID, verdict, summary string) {
	switch verdict {
	case "PASS":
		fmt.Printf("\n✓ PASS  %s\n", summary)
	default:
		fmt.Printf("\n✗ FAIL  %s\n", summary)
	}
}

func (StdoutSink) TaskMerged(taskID, stagingSHA string) {
	if stagingSHA == "" {
		fmt.Printf("[task %s] task completed (git disabled)\n", taskID)
	} else if len(stagingSHA) >= 8 {
		fmt.Printf("[task %s] merged to staging (%s)\n", taskID, stagingSHA[:8])
	} else {
		fmt.Printf("[task %s] merged to staging (%s)\n", taskID, stagingSHA)
	}
}

func (StdoutSink) TaskFailed(taskID, lastIssue string) {
	fmt.Printf("[task %s] failed after all rounds: %s\n", taskID, lastIssue)
}

func (StdoutSink) RoundEnd(taskID string, round, promptTokens, completionTokens int) {
	// No-op for stdout sink; token counts are not displayed in human mode.
}

func (StdoutSink) FileEditStart(taskID, path string) {
	fmt.Printf("[task %s] editing %s...\n", taskID, path)
}

func (StdoutSink) FileEditDone(taskID, path string, added, deleted int) {
	fmt.Printf("[task %s] ✓ %s (+%d/-%d)\n", taskID, path, added, deleted)
}

func (StdoutSink) FileEditFailed(taskID, path, reason string) {
	fmt.Printf("[task %s] ✗ %s: %s\n", taskID, path, reason)
}

func (StdoutSink) PermissionRequest(taskID, toolName, path, preview string, response chan<- bool) {
	// In stdout mode, auto-approve (no interactive prompt)
	response <- true
}

func (StdoutSink) ToolOperation(taskID, toolName, path, status, summary string) {
	fmt.Printf("[task %s] %s (%s) %s: %s\n", taskID, status, toolName, path, summary)
}

func (StdoutSink) ToolOperationDetail(taskID, path, content string) {
	// In stdout mode, show full content
	fmt.Printf("[task %s] %s details:\n%s\n", taskID, path, content)
}

func (StdoutSink) ThinkBlock(taskID, content string) {
	// Silently ignore think blocks for stdout - they're internal reasoning
}

func (StdoutSink) ThinkBlockDone(taskID string) {
	// No-op for stdout
}

func (StdoutSink) ProposalsReady(taskID string, files []string, summary string) {
	fmt.Printf("[task %s] Proposed changes to %d files awaiting review\n", taskID, len(files))
}

func (StdoutSink) ProposalsApplied(taskID string, files []string) {
	fmt.Printf("[task %s] Applied %d files after review\n", taskID, len(files))
}

func (StdoutSink) ProposalsDiscarded(taskID string, reason string) {
	fmt.Printf("[task %s] Discarded proposals: %s\n", taskID, reason)
}

// DiscardSink drops all events. Useful in tests that don't care about output.
type DiscardSink struct{}

func (DiscardSink) Token(string)                          {}
func (DiscardSink) RoundStart(string, int, int)           {}
func (DiscardSink) LintErrors(string, string)             {}
func (DiscardSink) VerdictBadge(string, string, string)   {}
func (DiscardSink) RoundEnd(string, int, int, int)        {}
func (DiscardSink) TaskMerged(string, string)             {}
func (DiscardSink) TaskFailed(string, string)             {}
func (DiscardSink) FileEditStart(string, string)          {}
func (DiscardSink) FileEditDone(string, string, int, int) {}
func (DiscardSink) FileEditFailed(string, string, string) {}
func (DiscardSink) PermissionRequest(string, string, string, string, chan<- bool) {
	// Auto-approve in discard mode
}
func (DiscardSink) ToolOperation(string, string, string, string, string) {}
func (DiscardSink) ToolOperationDetail(string, string, string)           {}
func (DiscardSink) ThinkBlock(string, string)                            {}
func (DiscardSink) ThinkBlockDone(string)                                {}
func (DiscardSink) ProposalsReady(string, []string, string)              {}
func (DiscardSink) ProposalsApplied(string, []string)                    {}
func (DiscardSink) ProposalsDiscarded(string, string)                    {}
