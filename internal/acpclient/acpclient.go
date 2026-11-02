// Package acpclient is the seam the TUI holds the agent side by (spec C2).
//
// The TUI reaches the agent side only through the two interfaces below: Host
// for the transport, Session for one conversation. Reads are synchronous and
// every call takes a context, so a caller never waits on a promise and an
// implementation never has to buffer for one.
//
// Each method's doc ends with an "ACP:" line naming the ACP method that
// carries it, or "ACP: none" when the method is a property of the transport.
// Methods carries the same mapping as data, so the seam can be checked against
// acp.Server without reading the docs.
//
// The TUI may import this package and acpwire, never inproc: the in-process
// adapter is an implementation, and the wire types are the only values that
// cross. The interfaces gain their groups' methods plan by plan (spec
// Migration order); a method added here needs a row in Methods and an ACP:
// line in its doc.
package acpclient

import (
	"context"

	"marshal/internal/acpwire"
)

// Host is the transport the TUI holds: what the agent side is, independent of
// any one session. It gains its groups' methods plan by plan (spec Migration
// order).
type Host interface {
	// SharedFilesystem reports whether a path the agent names is also a
	// path on this machine, so the TUI may open it in $EDITOR (C12).
	//
	// ACP: none (a property of the transport; true for the in-process
	// adapter and for P3's pipe).
	SharedFilesystem() bool
}

// Session is one conversation the TUI drives. It gains its groups' methods
// plan by plan (spec Migration order).
type Session interface {
	// ID is the session's ID.
	//
	// ACP: session/new (exists). Its result carries the ID, and every
	// session/* call sends it back.
	ID() string

	// Status is the session's current status (C5). It is read without
	// blocking: the in-process adapter builds it from the state on each call,
	// and P3 serves it from a mirror.
	//
	// ACP: session/status (new in P3). A session_status update refreshes it.
	Status() acpwire.SessionStatus

	// Changed fires after anything Status returns may have changed. It is a
	// coalescing signal with a buffer of 1: a reader re-reads Status and never
	// infers what changed. It is closed when a swap replaces the session.
	//
	// ACP: none (the client fires it; P3's mirror fires it after applying a
	// session/update notification).
	Changed() <-chan struct{}

	// SendNow submits one Send now request: its text goes to the running
	// turn at the next safe boundary, interrupting the operation in flight.
	// The receipt is in Status before SendNow returns.
	//
	// ACP: session/steer (exists), with delivery "now".
	SendNow(ctx context.Context, req acpwire.SendNowRequest) (acpwire.SteeringReceipt, error)

	// Sheet is what the session sheet's sections read (C5): the session's
	// own sheet when subagentID is 0, or the sheet of the subagent with that
	// ID anywhere in the session's tree. An ID that names no child session
	// reads as 0, and the result's SubagentID says which sheet was read.
	// Sheet does not block: its database and git values are the ones the
	// last RefreshSheet read.
	//
	// ACP: session/sheet (new in P3). P3 serves it from a mirror that
	// RefreshSheet fills.
	Sheet(subagentID int64) acpwire.SessionSheet

	// RefreshSheet re-reads the sheet values req names from the database and
	// git, then fires Changed. It blocks on those reads.
	//
	// ACP: session/sheet (new in P3), sent with the refresh flags.
	RefreshSheet(ctx context.Context, req acpwire.SheetRefresh) error

	// Stack is the transcript the TUI renders (C6): the session's own when
	// subagentID is 0, or that of the subagent with that ID anywhere in the
	// session's tree. Roots are the turns in order. Nodes hold every node of
	// the tree, then the band node, which is not in Roots. An ID that names no
	// child the session keeps reads as an empty snapshot with that SubagentID.
	// Stack does not block.
	//
	// busy says a turn is running, which decides the live beat and the turn's
	// receipt. Until plan 5 moves turns behind Prompt the TUI owns turns, so it
	// passes its own flag, and the tree is built with it whatever subagentID
	// names. P3 drops the parameter: session/stack builds a session's tree as
	// busy while its turn runs, and a subagent's while the subagent runs.
	//
	// ACP: session/stack (exists). A stack_patch update refreshes it.
	Stack(subagentID int64, busy bool) acpwire.StackSnapshot

	// StackNode is one transcript node's wire form with the detail the
	// projection caps or leaves out (C6): a call's whole arguments, output,
	// diff and summary and its step; a beat's narration, reasoning and step;
	// a completion's whole output and report. subagentID 0 reads the
	// session's transcript, and any other ID the transcript of the subagent
	// with that ID anywhere in the session's tree. It fails for an ID that
	// names no child, and for a node the transcript does not hold.
	//
	// ACP: session/stack_node (exists).
	StackNode(ctx context.Context, nodeID string, subagentID int64) (acpwire.StackNodeResult, error)

	// Callers lists what references the symbol a call row changed (C6). It
	// returns nil and no error when the row changed no resolved symbol, when
	// no language server is wired or ready, and when the node is gone. A
	// caller asks once per row and keeps the answer, a nil one included.
	//
	// ACP: session/callers (new in P3).
	Callers(ctx context.Context, nodeID string) ([]string, error)

	// Pending lists every open request a person can answer (C7): the
	// session's own, and the approvals of its running subagents. Pending does
	// not block and settles nothing, so it is safe to read while rendering.
	// Changed does not fire when a request opens or settles: the TUI reads
	// Pending on every update and tick.
	//
	// ACP: session/request_permission (new in P3). The agent already sends it
	// and session/request_question, and P3 adds session/request_secret. P3's
	// mirror keeps each request until it is answered or an update settles it.
	Pending() acpwire.PendingRequests

	// AnswerApproval answers the approval with that key. A session or always
	// answer to a Forced approval is a deny. always saves an allow rule to the
	// user config and applies it to the session's config, or adds a session
	// rule when the save fails; session adds a session rule. Both rules land
	// on the session, whichever subagent asked. A key that names no open
	// approval returns CodeRequestSettled.
	//
	// ACP: session/request_permission (new in P3): the
	// answer is that request's result, extended with the decision.
	AnswerApproval(ctx context.Context, key string, answer acpwire.ApprovalAnswer) error

	// AnswerQuestion answers the question with that key, one answer per
	// question in order. A key that names no open question returns
	// CodeRequestSettled.
	//
	// ACP: session/request_question (new in P3).
	AnswerQuestion(ctx context.Context, key string, answers []acpwire.Answer) error

	// AnswerSkillGate answers the skill gate with that key, and records an
	// allow_skill or allow_all choice for the rest of the session. A key that
	// names no open gate returns CodeRequestSettled.
	//
	// ACP: session/request_permission (new in P3), whose
	// request carries the skill-gate kind.
	AnswerSkillGate(ctx context.Context, key string, choice acpwire.SkillGateChoice) error

	// AnswerSudo answers the sudo prompt with that key. The password is
	// handed to the handler as the same slice, and the handler zeroes it; it
	// is never logged, nor returned by any read. A key that names no open
	// prompt returns CodeRequestSettled.
	//
	// ACP: session/request_secret (new in P3).
	AnswerSudo(ctx context.Context, key string, answer acpwire.SudoAnswer) error

	// ReconcileAccess settles the typed access request that no longer asks
	// for anything, then returns the one the access panel should present, if
	// any. The TUI calls it once per update, so a request surfaces and leaves
	// without a keypress. An implementation that cannot answer access
	// requests settles each one as failed and reports none.
	//
	// ACP: none (in P3 the agent settles such a request itself, and this
	// reads the request from the mirror).
	ReconcileAccess(ctx context.Context) (acpwire.AccessRequest, bool)

	// AnswerAccess answers the access request the choice names. The result
	// says how it settled; refresh_required means the tuple moved while the
	// person decided, and the request stays open.
	//
	// ACP: session/request_permission (new in P3), with
	// the typed access choice.
	AnswerAccess(ctx context.Context, choice acpwire.AccessChoice) (acpwire.AccessResult, error)

	// Rollback restores the last backup and logs the rollback as an audit
	// row in the transcript, failed or not. It returns the restore's error.
	//
	// ACP: session/rollback (new in P3).
	Rollback(ctx context.Context) error
}
