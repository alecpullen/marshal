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
	// TaskMerged is called after a PASS squash-merge into the staging branch.
	TaskMerged(taskID, stagingSHA string)
	// TaskFailed is called when all rounds are exhausted.
	TaskFailed(taskID, lastIssue string)
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
	fmt.Printf("[task %s] merged to staging (%s)\n", taskID, stagingSHA[:8])
}

func (StdoutSink) TaskFailed(taskID, lastIssue string) {
	fmt.Printf("[task %s] failed after all rounds: %s\n", taskID, lastIssue)
}

// DiscardSink drops all events. Useful in tests that don't care about output.
type DiscardSink struct{}

func (DiscardSink) Token(string)                              {}
func (DiscardSink) RoundStart(string, int, int)               {}
func (DiscardSink) LintErrors(string, string)                 {}
func (DiscardSink) VerdictBadge(string, string, string)       {}
func (DiscardSink) TaskMerged(string, string)                 {}
func (DiscardSink) TaskFailed(string, string)                 {}
