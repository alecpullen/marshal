// Package subagentwork owns the durable allocation and observation of
// subagent workspaces: which checkout a child was given, what commit it
// started at, which commit it produced, and whether that workspace is still
// the one the record owns.
//
// The package is deliberately transport- and runtime-independent. It imports
// no session state, agent loop, native tool set, database adapter, transport
// or UI package — only internal/worktree, for the git seam and the setup plan
// a caller executes. That keeps the DB adapter (package db), the native tool
// registry and the app wiring free of cycles, and lets the same service be
// driven by a test with a fake Store.
package subagentwork

import (
	"errors"

	"marshal/internal/worktree"
)

// Mode is how a dispatched child is bound to files. Isolated children get an
// owned worktree and commit their own work; shared children work directly in
// the captured parent checkout, and the parent commits.
type Mode string

const (
	// ModeIsolated gives the child a worktree created from an exact commit.
	ModeIsolated Mode = "isolated"
	// ModeShared binds the child to the captured parent root, without git
	// mutation. It is never a fallback for a refused isolated dispatch.
	ModeShared Mode = "shared"
)

// RecordVersion is the only record schema this package writes. A stored
// version other than this one is an explicit error, never a blank record.
const RecordVersion = 1

// Execution outcomes. They describe what the child's run did and are kept
// independently of any integration result: a done child that has not been
// integrated is still done, and a failed child stays failed even when it left
// commits behind.
const (
	OutcomePreparing   = "preparing"
	OutcomePrepared    = "prepared"
	OutcomeRunning     = "running"
	OutcomeDone        = "done"
	OutcomeFailed      = "failed"
	OutcomeStopped     = "stopped"
	OutcomePartial     = "partial"
	OutcomeInterrupted = "interrupted"
	OutcomeUnknown     = "unknown"
)

// Integration kinds and states. Integration is a separate axis from the
// execution outcome; P3 owns the operations that write these.
const (
	KindIntegrate = "integrate"
	KindDiscard   = "discard"
)

const (
	IntegrationIntent     = "intent"
	IntegrationIntegrated = "integrated"
	IntegrationNoChanges  = "no_changes"
	IntegrationConflict   = "conflict"
	IntegrationRefused    = "refused"
	IntegrationUnknown    = "unknown"
	IntegrationDiscarded  = "discarded"
)

// Cleanup states for the owned artifacts a record retains.
const (
	CleanupRetained          = "retained"
	CleanupComplete          = "complete"
	CleanupIntegratedPending = "integrated_cleanup_pending"
	CleanupUnknown           = "unknown"
)

// ErrStaleRecord reports that a compare-and-swap lost its revision race. The
// caller must re-read the record instead of overwriting the newer state.
var ErrStaleRecord = errors.New("subagentwork: stale workspace record revision")

// ErrRecordNotFound reports that no record exists for that owner and key, or
// for that owner and invocation. Owner scoping is part of the contract: one
// session's records are never visible to another.
var ErrRecordNotFound = errors.New("subagentwork: workspace record not found")

// Binding is where a child actually works: the owned worktree in isolated
// mode, the captured parent checkout in shared mode.
//
// Repository is the canonical absolute git common directory, not a branch
// name. Every worktree of one repository reports the same value, so a copied
// or re-initialised directory cannot pass as the recorded one.
type Binding struct {
	Mode        Mode   `json:"mode"`
	Root        string `json:"root"`
	ProjectRoot string `json:"project_root"`
	Repository  string `json:"repository"`
	Branch      string `json:"branch"`
	BaseSHA     string `json:"base_sha"`
}

// Target is the parent checkout an isolated candidate would be integrated
// into, captured at allocation. An explicit base commit changes Binding, never
// Target.
type Target struct {
	Root       string `json:"root"`
	Repository string `json:"repository"`
	Branch     string `json:"branch"`
	BaseSHA    string `json:"base_sha"`
}

// Verification is one command's outcome as the child reported it. ExitCode is
// nil when the caller reported no exit status.
type Verification struct {
	Command  string `json:"command"`
	Outcome  string `json:"outcome"`
	ExitCode *int   `json:"exit_code,omitempty"`
}

// Integration is the durable journal of one parent integration or discard
// attempt. P3 writes it; P2 only carries it through the record.
//
// The journal is ordered so a restart can always tell what happened: an accepted
// mutation is journalled as intent — with the exact source and the target commit
// it is about to sit on — before git runs, then as the result it proved
// (integrated, no_changes or conflict). OperationID is also written into the
// merge commit's message, so the merge in the target's history and the journal
// entry name each other. A refusal is journalled too: it is evidence that
// nothing was mutated, not the absence of a decision.
type Integration struct {
	OperationID    string `json:"operation_id"`
	Kind           string `json:"kind"`
	State          string `json:"state"`
	ExpectedSource string `json:"expected_source"`
	BeforeTarget   string `json:"before_target"`
	ResultCommit   string `json:"result_commit"`
	SnapshotHash   string `json:"snapshot_hash"`
	Reason         string `json:"reason"`
}

// Record is one owner's durable task record. The whole record is versioned;
// Revision is the optimistic-concurrency token CompareAndSwap checks.
type Record struct {
	Version int `json:"version"`

	OwnerSessionID string `json:"owner_session_id"`
	TaskKey        string `json:"task_key"`
	InvocationID   string `json:"invocation_id"`

	Binding Binding `json:"binding"`
	Target  Target  `json:"target"`
	// SetupPlan is the trusted setup hooks captured at allocation, so a
	// resumed dispatch runs exactly what the record says and not whatever
	// the configuration says now.
	SetupPlan worktree.SetupPlan `json:"setup_plan"`
	// SetupWarnings holds the non-fatal problems seeding reported when the
	// worktree was created. They never fail a dispatch, and they are kept so
	// a parent reviewer can see that a checkout was assembled imperfectly.
	SetupWarnings []string `json:"setup_warnings,omitempty"`

	Revision uint64 `json:"revision"`

	Outcome      string   `json:"outcome"`
	Candidate    string   `json:"candidate"`
	SourceError  string   `json:"source_error"`
	Commits      []string `json:"commits,omitempty"`
	ChangedPaths []string `json:"changed_paths,omitempty"`
	// Dirty is nil when the state is unknown. Unknown is never recorded as
	// clean.
	Dirty           *bool          `json:"dirty,omitempty"`
	ReportMessageID int64          `json:"report_message_id,omitempty"`
	Verification    []Verification `json:"verification,omitempty"`
	Integration     *Integration   `json:"integration,omitempty"`
	CleanupState    string         `json:"cleanup_state,omitempty"`
}

// PrepareRequest asks the service to allocate (or re-observe) one workspace.
// The service never infers task keys or workspace support; the caller derives
// both from trusted execution facts.
type PrepareRequest struct {
	OwnerSessionID string
	TaskKey        string
	InvocationID   string
	// ProjectRoot is the project the worktree directory lives under.
	ProjectRoot string
	// ParentRoot is the parent's active checkout: the git and seeding source,
	// and the integration target captured on the record. It may itself be a
	// worktree, so it is not assumed to equal ProjectRoot.
	ParentRoot string
	// BaseCommit is an exact existing commit object id. Empty means the
	// captured parent HEAD.
	BaseCommit string
	// Mode is ModeIsolated when empty, which is the omitted-argument default.
	Mode Mode
	// Setup is the seeding and setup-hook configuration for a fresh worktree.
	Setup worktree.WorktreeSetup
	// IsolatedSupported is the trusted backend capability: a backend that
	// cannot host a linked git worktree must refuse isolated dispatch before
	// anything is allocated.
	IsolatedSupported bool
}

// Completion is what a dispatch reports when its child's run ends. Outcome is
// required; ReportMessageID references the durable report message instead of
// duplicating its body.
type Completion struct {
	Outcome         string
	ReportMessageID int64
	Verification    []Verification
}

// clone returns a deep copy, so a caller can never mutate stored state and an
// observer can never mutate the record it was handed.
func (r Record) clone() Record {
	out := r
	out.Commits = cloneStrings(r.Commits)
	out.ChangedPaths = cloneStrings(r.ChangedPaths)
	out.SetupWarnings = cloneStrings(r.SetupWarnings)
	// Copy the plan's hooks without naming their config type: a full slice
	// expression gives a fresh backing array of the same element type.
	out.SetupPlan.Hooks = append(r.SetupPlan.Hooks[:0:0], r.SetupPlan.Hooks...)
	if r.Dirty != nil {
		dirty := *r.Dirty
		out.Dirty = &dirty
	}
	if r.Integration != nil {
		integration := *r.Integration
		out.Integration = &integration
	}
	out.Verification = cloneVerification(r.Verification)
	return out
}

func cloneStrings(in []string) []string {
	if in == nil {
		return nil
	}
	return append([]string(nil), in...)
}

func cloneVerification(in []Verification) []Verification {
	if in == nil {
		return nil
	}
	out := make([]Verification, len(in))
	for i, v := range in {
		out[i] = v
		if v.ExitCode != nil {
			code := *v.ExitCode
			out[i].ExitCode = &code
		}
	}
	return out
}

// validOutcome reports whether s is one of the recorded execution outcomes.
func validOutcome(s string) bool {
	switch s {
	case OutcomePreparing, OutcomePrepared, OutcomeRunning, OutcomeDone,
		OutcomeFailed, OutcomeStopped, OutcomePartial, OutcomeInterrupted,
		OutcomeUnknown:
		return true
	}
	return false
}

// validIntegrationState reports whether s is one of the journalled integration
// states. A journal write of anything else is refused rather than stored: a
// record that names a state this build cannot interpret is unreadable evidence.
func validIntegrationState(s string) bool {
	switch s {
	case IntegrationIntent, IntegrationIntegrated, IntegrationNoChanges,
		IntegrationConflict, IntegrationRefused, IntegrationUnknown,
		IntegrationDiscarded:
		return true
	}
	return false
}

// validCleanupState reports whether s is one of the cleanup states.
func validCleanupState(s string) bool {
	switch s {
	case CleanupRetained, CleanupComplete, CleanupIntegratedPending, CleanupUnknown:
		return true
	}
	return false
}
