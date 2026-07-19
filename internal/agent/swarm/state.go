package swarm

import (
	"fmt"
	"strings"
	"sync"
)

// Finding is one observation an agent recorded in shared task state.
type Finding struct {
	Agent   string // role that produced it, e.g. "repo_scout"
	Area    string // focus area, e.g. "tests"
	Content string
}

// TestFailure is a structured test failure emitted by the tester role.
type TestFailure struct {
	File    string `json:"file"`
	Line    int    `json:"line"`
	Test    string `json:"test"`
	Message string `json:"message"`
}

// TaskState is the shared blackboard for one swarm run (docs/07, "Shared
// task state"). Agents never talk to each other directly: the orchestrator
// writes role outputs here and each role reads the whole state via Render.
// All methods are safe for concurrent use — parallel repo scouts write
// findings from separate goroutines.
type TaskState struct {
	mu           sync.Mutex
	goal         string
	plan         []string
	findings     []Finding
	testFailures []TestFailure
	patchNotes   []string
	finalSummary string
}

func NewTaskState(goal string) *TaskState {
	return &TaskState{goal: goal}
}

func (ts *TaskState) SetPlan(plan []string) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.plan = append([]string(nil), plan...)
}

func (ts *TaskState) Plan() []string {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return append([]string(nil), ts.plan...)
}

func (ts *TaskState) AddFinding(f Finding) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.findings = append(ts.findings, f)
}

func (ts *TaskState) Findings() []Finding {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return append([]Finding(nil), ts.findings...)
}

func (ts *TaskState) AddTestFailure(tf TestFailure) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.testFailures = append(ts.testFailures, tf)
}

func (ts *TaskState) AddPatchNote(note string) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.patchNotes = append(ts.patchNotes, note)
}

func (ts *TaskState) SetFinalSummary(s string) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.finalSummary = s
}

// Render produces the markdown block injected into every role prompt.
// Empty sections are omitted so early roles (planner) see a compact state.
func (ts *TaskState) Render() string {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	var b strings.Builder
	b.WriteString("## Shared task state\n\n")
	b.WriteString("Goal: " + ts.goal + "\n")
	if len(ts.plan) > 0 {
		b.WriteString("\nPlan:\n")
		for _, step := range ts.plan {
			b.WriteString("- " + step + "\n")
		}
	}
	if len(ts.findings) > 0 {
		b.WriteString("\nFindings:\n")
		for _, f := range ts.findings {
			b.WriteString(fmt.Sprintf("- [%s/%s] %s\n", f.Agent, f.Area, f.Content))
		}
	}
	if len(ts.testFailures) > 0 {
		b.WriteString("\nTest failures:\n")
		for _, tf := range ts.testFailures {
			fmt.Fprintf(&b, "- %s:%d [%s] %s\n", tf.File, tf.Line, tf.Test, tf.Message)
		}
	}
	if len(ts.patchNotes) > 0 {
		b.WriteString("\nChanges made:\n")
		for _, note := range ts.patchNotes {
			b.WriteString("- " + note + "\n")
		}
	}
	if ts.finalSummary != "" {
		b.WriteString("\nReview:\n" + ts.finalSummary + "\n")
	}
	return b.String()
}
