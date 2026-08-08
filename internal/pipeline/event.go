package pipeline

// Event is one progress update. The controller emits events; the app layer
// maps them onto session state for the TUI. The pipeline package knows
// nothing about the TUI.
//
// Payload optionally carries the event's detail as plain data — the
// compiler error that failed a gate, the findings a reviewer raised, the
// commit a task produced. It is nil for events that are pure phase
// transitions. Consumers type-switch on it; an unrecognised or nil payload
// is not an error, it simply renders nothing.
type Event struct {
	TaskN        int
	TotalTasks   int
	FixRound     int
	MaxFixRounds int
	Phase        string
	Detail       string
	Payload      any
}

// VerifyFailedPayload carries a failed build/test gate. Output is the
// verifier's combined stdout+stderr, verbatim and untruncated — the UI
// decides how much to show.
type VerifyFailedPayload struct {
	Command   string
	Output    string
	Round     int
	MaxRounds int
}

// ReviewPayload carries a reviewer verdict. Clean is true when the review
// passed with no blocking findings; Findings holds every finding raised,
// blocking or minor.
type ReviewPayload struct {
	Findings []Finding
	Clean    bool
}

// CommitPayload carries a task's commit. SHA is the short form.
type CommitPayload struct {
	SHA     string
	Subject string
}

// RetryPayload carries a transient dispatch failure that is being retried.
type RetryPayload struct {
	Attempt     int
	MaxAttempts int
	Err         string
}

// ConcernPayload carries an implementer's DONE_WITH_CONCERNS note.
type ConcernPayload struct {
	Text string
}

// GateSkippedPayload carries a verification gate that did not run. This is
// never a pass: see VerifyResult.Skipped.
type GateSkippedPayload struct {
	Reason string
}

// ExecPayload carries the execution type of a completed task.
type ExecPayload struct {
	TaskN    int
	ExecType ExecType
}

// Observer receives progress events. A nil Observer is valid — the
// controller runs headless.
type Observer interface {
	Event(ev Event)
}
