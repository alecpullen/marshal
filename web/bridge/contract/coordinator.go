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

// CoordinatorAssignmentRelationship is what an assignment's target is: a worker
// the coordinated task created, or a thread that already existed and is linked.
type CoordinatorAssignmentRelationship string

// The relationship kinds.
const (
	// CoordinatorAssignmentWorker means the assignment created a worker: a
	// project agent of its own, started under the assignment's identity.
	CoordinatorAssignmentWorker CoordinatorAssignmentRelationship = "worker"
	// CoordinatorAssignmentThread means the assignment linked a thread that was
	// already running in the target project. A linked thread is observed by
	// default; its instruction reaches it only when a person explicitly directed
	// that thread, which the bridge verifies rather than the caller.
	CoordinatorAssignmentThread CoordinatorAssignmentRelationship = "thread"
)

// CoordinatorAssignmentState is where one assignment stands.
type CoordinatorAssignmentState string

// The assignment states.
const (
	// CoordinatorAssignmentDispatching means the intent and its dispatch
	// identity are durable and the dispatch has not resolved yet: the worker
	// has not acknowledged, or an unconfirmed submission is not yet stored. It
	// survives a restart, which is what lets the next dispatch resume the
	// assignment instead of starting a second worker.
	CoordinatorAssignmentDispatching CoordinatorAssignmentState = "dispatching"
	// CoordinatorAssignmentPending means the assignment is intake awaiting a
	// person's confirmation. It is not running, and nothing has been started.
	CoordinatorAssignmentPending CoordinatorAssignmentState = "pending"
	// CoordinatorAssignmentRunning means a worker acknowledged the assignment:
	// its session exists and its instruction reached it.
	CoordinatorAssignmentRunning CoordinatorAssignmentState = "running"
	// CoordinatorAssignmentLinked means an existing thread is linked. Nothing
	// was started; the thread is observed and, only under explicit user
	// direction, receives the assignment's instruction.
	CoordinatorAssignmentLinked CoordinatorAssignmentState = "linked"
	// CoordinatorAssignmentFailed means this target could not be dispatched.
	// Its siblings are unaffected.
	CoordinatorAssignmentFailed CoordinatorAssignmentState = "failed"
	// CoordinatorAssignmentExpired means an unconfirmed submission lapsed
	// before a person confirmed it.
	CoordinatorAssignmentExpired CoordinatorAssignmentState = "expired"
	// CoordinatorAssignmentStopped is where the task stop scope moves an
	// assignment. Nothing in this phase writes it.
	CoordinatorAssignmentStopped CoordinatorAssignmentState = "stopped"
)

// CoordinatorAssignmentFailure is the machine-readable reason an assignment
// failed. It is empty for every other state.
const (
	// CoordinatorFailureUnknownProject names a project ID this daemon did not
	// mint, or one outside the task's scope.
	CoordinatorFailureUnknownProject = "unknown_project"
	// CoordinatorFailureUnknownThread names a thread the target project does not
	// hold: a link never widens to another project's agent.
	CoordinatorFailureUnknownThread = "unknown_thread"
	// CoordinatorFailureUnauthorized means the task's effective access on the
	// target project does not hold the capability dispatching needs.
	CoordinatorFailureUnauthorized = "unauthorized"
	// CoordinatorFailureUnavailable means the dispatch could not be attempted:
	// the store, the access policy or the target project is unreadable.
	CoordinatorFailureUnavailable = "unavailable"
	// CoordinatorFailureUserDirectionRequired means the target asked to steer an
	// existing thread and no person's direction for that thread was verified.
	CoordinatorFailureUserDirectionRequired = "user_direction_required"
)

// CoordinatorWorkerRef names the worker an assignment created.
type CoordinatorWorkerRef struct {
	AgentID string `json:"agentId"`
	// SessionID is the worker's ACP session. It is empty until the worker
	// acknowledges, which is when its assignment becomes running.
	SessionID string `json:"sessionId,omitempty"`
}

// CoordinatorAssignment is one coordinated task's assignment to one target
// project: what it asked for, what that became, and how far it got.
//
// The assignment ID is the assignment's dispatch identity, not just a label:
// it is minted and persisted before any side effect and is the identity the
// pending submission, the spawn claim and the worker's own record all carry, so
// a retry, a confirmation, a reconnect and a daemon restart all resolve to one
// outcome.
type CoordinatorAssignment struct {
	ID     string `json:"id"`
	TaskID string `json:"taskId"`
	// ProjectID is the target project's canonical daemon ID, never a display
	// name or a path.
	ProjectID string `json:"projectId"`
	// Instruction is what the task asked this project to do, as the task wrote
	// it. It is display text in the caller's own words, never markup.
	Instruction  string                            `json:"instruction,omitempty"`
	Relationship CoordinatorAssignmentRelationship `json:"relationship"`
	State        CoordinatorAssignmentState        `json:"state"`
	// IntakeID is the pending submission an unconfirmed assignment awaits. It is
	// the assignment's own identity in the intake queue.
	IntakeID string `json:"intakeId,omitempty"`
	// Worker is the agent this assignment created, once one exists.
	Worker *CoordinatorWorkerRef `json:"worker,omitempty"`
	// Thread is the existing thread a linked assignment observes.
	Thread *CoordinatorThreadRef `json:"thread,omitempty"`
	// ResultRevision is the newest revision of this assignment's observed
	// result. Zero means nothing has been observed yet; it advances only when an
	// observation sees a different result, so a client can tell a quiet worker
	// from a stale record.
	ResultRevision uint64 `json:"resultRevision"`
	// Failure is the machine-readable reason when the state is failed.
	Failure string `json:"failure,omitempty"`
	// Reason explains a failed, expired or not-yet-resolved assignment in words
	// meant for a person.
	Reason    string    `json:"reason,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// CoordinatorAssignTarget is one target of a dispatch: the canonical project it
// names, what that project should do, and — optionally — an existing thread to
// link instead of starting a worker.
type CoordinatorAssignTarget struct {
	// ProjectID is the project's canonical daemon ID.
	ProjectID   string `json:"projectId"`
	Instruction string `json:"instruction,omitempty"`
	// ThreadID links an existing thread in the target project rather than
	// starting a worker. It must belong to that project.
	ThreadID string `json:"threadId,omitempty"`
	// Steer asks that the linked thread be given the instruction. It is admitted
	// only when a person's direction for that thread is verified; without one the
	// dispatch is refused before anything is recorded.
	Steer bool `json:"steer,omitempty"`
}

// CoordinatorAssignRequest is one task-scoped dispatch. RequestID is the
// caller's idempotency key: every retry of the same targets carries it, so a
// second press, a lost answer or a restart resolves to the assignments the first
// request recorded instead of dispatching again.
type CoordinatorAssignRequest struct {
	RequestID string                    `json:"requestId"`
	Targets   []CoordinatorAssignTarget `json:"targets"`
}

// CoordinatorAssignResult is one dispatch's answer: one entry per target, in
// request order. A target that failed carries its own failure, and its eligible
// siblings still progress.
type CoordinatorAssignResult struct {
	TaskID       string                  `json:"taskId"`
	TaskRevision uint64                  `json:"taskRevision,omitempty"`
	Assignments  []CoordinatorAssignment `json:"assignments"`
}

// CoordinatorAssignmentList is one task's assignments, as a read reports them.
type CoordinatorAssignmentList struct {
	TaskID      string                  `json:"taskId"`
	Host        string                  `json:"host,omitempty"`
	Assignments []CoordinatorAssignment `json:"assignments"`
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
