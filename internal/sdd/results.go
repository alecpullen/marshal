package sdd

// GateDecision is the closed set of outcomes a gate can return.
type GateDecision string

const (
	DecisionAccept GateDecision = "accept"
	DecisionBlock  GateDecision = "block"
	DecisionSkip   GateDecision = "skip"
	DecisionReject GateDecision = "reject"
)

// GateResult is the typed result of audit-gate, review-state, and
// review-guard. The caller (controller/orchestrator) logs one progress event
// using Event + KV; the gate itself does not call Progress.Append
// (single-responsibility: the caller decides whether to log).
type GateResult struct {
	Decision GateDecision
	Event    string // progress event name (e.g. "AUDIT_PASS", "REVIEW_SKIP")
	Reason   string
	KV       []string // flat key/value pairs for Progress.Append
}

// GuardResult is the typed result of branch-base-guard, edit-guard, and the
// allowed-files check. Pass is the boolean verdict; Files carries the
// offending paths when Pass is false.
type GuardResult struct {
	Pass   bool
	Event  string
	Reason string
	Files  []string
}

// VerifyResult is the typed result of verify-task. Output is the combined
// build/lint/test stdout+stderr (truncated by the caller if needed).
// Violations holds allowed-files violations (empty if Pass).
type VerifyResult struct {
	Pass       bool
	Event      string // "VERIFY_PASS" | "VERIFY_FAIL"
	Output     string
	Violations []string
}

// HealthAlert is what health-loop detection returns when it sees a problem.
// Evidence is the slice of progress.md lines that triggered the alert.
type HealthAlert struct {
	Kind     string // "LOOP" | "STAGNATION" | "MODEL_DEGRADATION"
	Detail   string
	Evidence []string
}

// MergeResult is the typed result of Merge. The caller logs one MERGED or
// BLOCKED progress event using Event + Reason.
type MergeResult struct {
	Merged bool
	TaskID string
	Event  string // "MERGED" | "BLOCKED"
	Reason string
}
