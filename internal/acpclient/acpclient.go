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

import "marshal/internal/acpwire"

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
}
