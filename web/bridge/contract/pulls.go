package contract

import "encoding/json"

// PullOrigin is where a recorded pull request came from: an agent that
// shipped, a factory item, or one of the bridge's automations.
type PullOrigin string

// The pull request origins.
const (
	PullOriginAgent      PullOrigin = "agent"
	PullOriginFactory    PullOrigin = "factory"
	PullOriginAutomation PullOrigin = "automation"
)

// PullState is a pull request's derived state, which is what a client shows.
// It is derived from the evidence on the current head and is never a claim
// about a head that has moved.
type PullState string

// The pull request states. ready and failing describe the current head;
// in_progress means its checks have not settled; being_fixed means a worker
// was dispatched and is still on it; checks_unavailable is its own state,
// with the reason in PullRecord.Unavailable, and is never shown as passing.
const (
	PullReady             PullState = "ready"
	PullFailing           PullState = "failing"
	PullInProgress        PullState = "in_progress"
	PullBeingFixed        PullState = "being_fixed"
	PullChecksUnavailable PullState = "checks_unavailable"
)

// CheckState is one check's state on a pull request's head.
type CheckState string

// The check states.
const (
	CheckPassed  CheckState = "passed"
	CheckFailed  CheckState = "failed"
	CheckRunning CheckState = "running"
	CheckPending CheckState = "pending"
)

// PullCheck is one check on a pull request's head.
type PullCheck struct {
	Name  string     `json:"name"`
	State CheckState `json:"state"`
	URL   string     `json:"url,omitempty"`
	// LogTail is the tail of a failed check's log. Only the detail route
	// fills it, so an index stays small.
	LogTail string `json:"logTail,omitempty"`
}

// PullRecord is one pull request the bridge opened, with the evidence it holds
// for the head it last observed. Its id is <repoId>#<number>.
type PullRecord struct {
	ID          string      `json:"id"`
	RepoID      string      `json:"repoId"`
	ProjectRoot string      `json:"projectRoot"`
	Number      int         `json:"number"`
	URL         string      `json:"url"`
	Title       string      `json:"title"`
	HeadRef     string      `json:"headRef"`
	BaseRef     string      `json:"baseRef"`
	HeadSHA     string      `json:"headSha"`
	Draft       bool        `json:"draft"`
	Additions   int         `json:"additions"`
	Deletions   int         `json:"deletions"`
	Origin      PullOrigin  `json:"origin"`
	AgentID     string      `json:"agentId,omitempty"`
	FactoryItem string      `json:"factoryItemId,omitempty"`
	Automation  string      `json:"automation,omitempty"`
	State       PullState   `json:"state"`
	Checks      []PullCheck `json:"checks"`
	Reviews     string      `json:"reviews"`
	// Unavailable says why the checks cannot be read, when they cannot.
	// A record carrying it is never shown as passing.
	Unavailable string `json:"checksUnavailable,omitempty"`
	// Report is the engineer's narrated report, when the origin has one.
	// Only the detail route fills it.
	Report string `json:"report,omitempty"`
	// Fix is the dispatched fix, when a worker is on this pull request.
	Fix *PullFix `json:"fix,omitempty"`
	// ObservedAt is when the evidence above was last read, in Unix millis.
	// UpdatedAt is when the record itself last changed.
	ObservedAt int64 `json:"observedAt"`
	UpdatedAt  int64 `json:"updatedAt"`
}

// PullFix is one dispatched fix. It is bound to the head it was asked about:
// a worker that starts after the head has moved is cancelled rather than run
// against different code.
type PullFix struct {
	ID        string `json:"id"`
	Check     string `json:"check"`
	HeadSHA   string `json:"headSha"`
	Mode      string `json:"mode"` // "fix" | "investigate"
	AgentID   string `json:"agentId,omitempty"`
	StartedAt int64  `json:"startedAt"`
}

// The fix modes: fix a failing check, or investigate it and report.
const (
	PullFixModeFix         = "fix"
	PullFixModeInvestigate = "investigate"
)

// PullFixRequest is what POST /api/pulls/{id}/fix accepts.
type PullFixRequest struct {
	Check string `json:"check"`
	// Mode is "fix" or "investigate"; empty means fix.
	Mode    string          `json:"mode,omitempty"`
	Note    string          `json:"note,omitempty"`
	Routing json.RawMessage `json:"routing,omitempty"`
}

// PullDelta is the "pull" fleet delta's payload: the record that changed, or
// the id of one that went away.
type PullDelta struct {
	Pull    *PullRecord `json:"pull,omitempty"`
	Removed string      `json:"removed,omitempty"`
}

// PullAsk is the inbox item a fix that could not finish leaves behind: which
// pull request and check, what the worker tried, what it found, and what the
// user can do next. An investigate-only run always ends in one, carrying the
// finding.
type PullAsk struct {
	Pull  string `json:"pull"`
	Check string `json:"check"`
	// Outcome is what became of the fix: "not_reproduced", "gave_up",
	// "forbidden_change", "stopped", "head_moved" or "investigated".
	Outcome string `json:"outcome"`
	Tried   string `json:"tried,omitempty"`
	Finding string `json:"finding,omitempty"`
	Action  string `json:"action,omitempty"`
	At      int64  `json:"at,omitempty"`
}
