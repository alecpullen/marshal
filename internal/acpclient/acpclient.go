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
}
