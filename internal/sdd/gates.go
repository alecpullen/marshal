package sdd

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// AuditGate is the gate between implementer return and reviewer dispatch
// (spec §9). It reads the auditor's report, normalizes + validates it, and
// returns a typed decision:
//   - AUDIT_MISSING (block): no report exists; dispatch the auditor first
//   - AUDIT_MALFORMED (block): report exists but fails validation
//   - AUDIT_FAIL (block): auditor reported FAIL
//   - AUDIT_PASS (accept): auditor reported PASS; proceed to reviewer
//
// The caller logs one progress event using r.Event + r.KV.
func AuditGate(ws *Workspace, taskID string) GateResult {
	path := filepath.Join(ws.Dir(), "reports", taskID+"-audit.md")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return GateResult{Decision: DecisionBlock, Event: "AUDIT_MISSING", Reason: "no audit report; dispatch auditor first"}
	}
	if err != nil {
		return GateResult{Decision: DecisionBlock, Event: "AUDIT_MALFORMED", Reason: fmt.Sprintf("read audit report: %v", err)}
	}
	normalized := NormalizeReport(string(data))
	// Parse the normalized string directly (no re-read from disk).
	rep := parseReportFromString(taskID, normalized)
	if err := rep.Validate(); err != nil {
		return GateResult{Decision: DecisionBlock, Event: "AUDIT_MALFORMED", Reason: err.Error()}
	}
	switch rep.Status {
	case ReportPass:
		return GateResult{Decision: DecisionAccept, Event: "AUDIT_PASS", KV: []string{"task", taskID}}
	case ReportFail:
		return GateResult{Decision: DecisionBlock, Event: "AUDIT_FAIL", KV: []string{"task", taskID}}
	default:
		return GateResult{Decision: DecisionBlock, Event: "AUDIT_FAIL", Reason: fmt.Sprintf("unexpected audit status %q", rep.Status), KV: []string{"task", taskID}}
	}
}

// ReviewStateOpts controls the trivial-diff threshold for ReviewState.
type ReviewStateOpts struct {
	MaxLines int
	MaxFiles int
}

// DefaultReviewStateOpts is the threshold the orchestrator uses by default
// (≤ 20 lines, ≤ 2 files). P4 can override.
var DefaultReviewStateOpts = ReviewStateOpts{MaxLines: 20, MaxFiles: 2}

// ReviewState decides whether a task needs a reviewer dispatch (spec §9).
// It compares the task's current HEAD against its ReviewedHead:
//   - REVIEWED_HEAD empty (first pass): REVIEW_REQUIRED
//   - current == ReviewedHead: REVIEW_SKIP (already reviewed, no new commits)
//   - trivial diff (≤ MaxLines, ≤ MaxFiles, non-core): REVIEW_SKIP
//   - otherwise: REVIEW_REQUIRED
func ReviewState(git GitOps, dag *DAG, taskID string) GateResult {
	return reviewStateWithOpts(git, dag, taskID, DefaultReviewStateOpts)
}

func reviewStateWithOpts(git GitOps, dag *DAG, taskID string, opts ReviewStateOpts) GateResult {
	task, ok := dag.TaskByID(taskID)
	if !ok {
		return GateResult{Decision: DecisionBlock, Event: "REVIEW_STATE", Reason: "unknown task " + taskID}
	}
	if task.Branch == "" {
		return GateResult{Decision: DecisionBlock, Event: "REVIEW_STATE", Reason: "task has no branch"}
	}
	currentHead, err := git.RevParse(task.Branch)
	if err != nil {
		return GateResult{Decision: DecisionBlock, Event: "REVIEW_STATE", Reason: "rev-parse task branch: " + err.Error()}
	}
	if task.ReviewedHead == "" {
		return GateResult{Decision: DecisionAccept, Event: "REVIEW_REQUIRED", KV: []string{"task", taskID, "reason", "first_pass"}}
	}
	if currentHead == task.ReviewedHead {
		return GateResult{Decision: DecisionSkip, Event: "REVIEW_SKIP", KV: []string{"task", taskID, "reason", "already_reviewed"}}
	}
	diff, err := git.DiffStat(task.ReviewedHead, task.Branch)
	if err != nil {
		return GateResult{Decision: DecisionBlock, Event: "REVIEW_STATE", Reason: "diff-stat: " + err.Error()}
	}
	lines, files := countDiffStat(diff)
	if lines <= opts.MaxLines && files <= opts.MaxFiles {
		return GateResult{Decision: DecisionSkip, Event: "REVIEW_SKIP", KV: []string{"task", taskID, "reason", "trivial_diff", "lines", strconv.Itoa(lines), "files", strconv.Itoa(files)}}
	}
	return GateResult{Decision: DecisionAccept, Event: "REVIEW_REQUIRED", KV: []string{"task", taskID, "lines", strconv.Itoa(lines), "files", strconv.Itoa(files)}}
}

// countDiffStat parses `git diff --stat` output and returns the total line
// count and file count. Lines look like " path | N +++---"; N is the number
// after the pipe.
var diffStatLineCountRe = regexp.MustCompile(`\|\s+(\d+)\s`)

func countDiffStat(diff string) (lines, files int) {
	for _, line := range strings.Split(diff, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		files++
		if m := diffStatLineCountRe.FindStringSubmatch(line); m != nil {
			if n, err := strconv.Atoi(m[1]); err == nil {
				lines += n
			}
		}
	}
	return
}
