package contract

import "time"

// The coordinator contract: one host-local coordinated task (Marshal's own
// conversation), the thread it was opened from, and the read-only attention
// summary it may ask about.
//
// A task belongs to exactly one daemon. Host is the serving daemon's own
// identity, not something a client may choose: the same task ID on another
// host names a different task, and Studio routes carry the host they were read
// from so a link never resolves against the wrong one.
//
// Nothing here carries a credential, a path or a project root. A project is
// named by the opaque ID the daemon minted, which is what an access check is
// keyed by.

// CoordinatorThreadRef identifies one source a coordinated task points at: the
// project it belongs to, what kind of thing it is, and its own id.
type CoordinatorThreadRef struct {
	ProjectID string `json:"projectId"`
	// Kind is "agent" (a fleet thread), "intake" (an unconfirmed submission),
	// "pull" (a pull request) or "review" (a pull request ready to review).
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// CoordinatorTaskStatus is where a coordinated task stands. Only "open" is
// written by this phase; a stopped task arrives with the stop scope.
type CoordinatorTaskStatus string

// The task states.
const (
	CoordinatorTaskOpen    CoordinatorTaskStatus = "open"
	CoordinatorTaskStopped CoordinatorTaskStatus = "stopped"
)

// CoordinatorTask is one host-local coordinated task: a durable task ID, the
// coordinator conversation's session, an optional source thread, a title, a
// status and a revision.
type CoordinatorTask struct {
	ID string `json:"id"`
	// SessionID is the coordinator conversation's ACP session, minted by the
	// daemon and persisted here so the conversation outlives a browser.
	SessionID string                `json:"sessionId"`
	Title     string                `json:"title,omitempty"`
	Status    CoordinatorTaskStatus `json:"status"`
	Revision  uint64                `json:"revision"`
	Source    *CoordinatorThreadRef `json:"source,omitempty"`
	// Projects are the opaque project IDs this task may read. An empty list is
	// the host-local scope: every project registered on the serving daemon,
	// checked per read.
	Projects []string `json:"projects"`
	// Host is the serving daemon's identity (its socket path), so a client can
	// tell this task apart from one with the same ID on another host.
	Host      string    `json:"host,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// CoordinatorTaskList is GET /api/coordinator/tasks.
type CoordinatorTaskList struct {
	Tasks []CoordinatorTask `json:"tasks"`
	Host  string            `json:"host,omitempty"`
}

// CoordinatorCreateRequest is POST /api/coordinator/tasks: the first send of a
// coordinator destination. RequestID is the client's idempotency key, reused
// for retries of the same content.
type CoordinatorCreateRequest struct {
	RequestID string                `json:"requestId"`
	Title     string                `json:"title,omitempty"`
	Source    *CoordinatorThreadRef `json:"source,omitempty"`
	Projects  []string              `json:"projects,omitempty"`
}

// CoordinatorCreateResult is POST /api/coordinator/tasks's answer. Created is
// false when the request ID replayed an existing task, which is what tells a
// retry from a fresh destination.
type CoordinatorCreateResult struct {
	Task    CoordinatorTask `json:"task"`
	Created bool            `json:"created"`
}

// CoordinatorAttentionKind is what an attention entry is: an ask that needs a
// decision, or a review that is ready.
type CoordinatorAttentionKind string

// The attention entry kinds.
const (
	CoordinatorAttentionAsk    CoordinatorAttentionKind = "ask"
	CoordinatorAttentionReview CoordinatorAttentionKind = "review"
)

// CoordinatorAttentionEntry is one read-only thing Marshal found: the source it
// came from, what kind it is, the text to show, how long it has waited and
// where it opens.
type CoordinatorAttentionEntry struct {
	Source CoordinatorThreadRef     `json:"source"`
	Kind   CoordinatorAttentionKind `json:"kind"`
	// Text is display text, never markup.
	Text string `json:"text"`
	// AgeSeconds is how long the entry has waited, floored at zero.
	AgeSeconds int64 `json:"ageSeconds"`
	// Destination is the Studio path that opens the entry at its source. It is
	// absent when the source is not one this build can link to.
	Destination string `json:"destination,omitempty"`
}

// CoordinatorSourceState is how much one source contributed to a read. A
// source that is not fresh is never part of an all-clear.
type CoordinatorSourceState string

// The per-source freshness states.
const (
	// CoordinatorSourceFresh means the read reached the project and what it
	// holds was observed recently enough to act on.
	CoordinatorSourceFresh CoordinatorSourceState = "fresh"
	// CoordinatorSourceStale means the read reached the project but its
	// evidence is older than the freshness window: a quiet answer from stale
	// evidence is not an all-clear.
	CoordinatorSourceStale CoordinatorSourceState = "stale"
	// CoordinatorSourceUnavailable means the source could not be read at all.
	CoordinatorSourceUnavailable CoordinatorSourceState = "unavailable"
	// CoordinatorSourceUnauthorized means the read was refused: the task's
	// access to that project is revoked or was never granted.
	CoordinatorSourceUnauthorized CoordinatorSourceState = "unauthorized"
)

// CoordinatorSource is one project's contribution to an attention read, with
// the reason when it is not fresh.
type CoordinatorSource struct {
	ProjectID string                 `json:"projectId"`
	State     CoordinatorSourceState `json:"state"`
	Reason    string                 `json:"reason,omitempty"`
	// ObservedAt is the newest evidence this source contributed, in Unix
	// millis. Zero when it contributed none.
	ObservedAt int64 `json:"observedAt,omitempty"`
}

// CoordinatorAttention is the read-only attention summary for one task:
// entries to show plus per-source freshness. AllClear is true only when every
// source was read fresh and nothing needs a decision; a stale, unavailable or
// unauthorized source can never produce it.
type CoordinatorAttention struct {
	TaskID   string                      `json:"taskId"`
	Host     string                      `json:"host,omitempty"`
	Entries  []CoordinatorAttentionEntry `json:"entries"`
	Sources  []CoordinatorSource         `json:"sources"`
	AllClear bool                        `json:"allClear"`
	// Partial is true when some source was not fresh, so the caller knows the
	// answer covers less than the task's scope.
	Partial    bool      `json:"partial"`
	ObservedAt time.Time `json:"observedAt"`
}
