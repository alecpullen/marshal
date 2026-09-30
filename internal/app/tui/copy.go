package tui

import (
	"context"
	"errors"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/clipboard"
)

// CopyKind classifies the outcome of one copy attempt. The distinction
// between a confirmed local write and a terminal request is the point:
// OSC 52 has no write acknowledgement, so the UI must never say "Copied"
// when it only asked the terminal to copy.
type CopyKind int

const (
	// CopyLocalDone means a local clipboard backend accepted the bytes.
	CopyLocalDone CopyKind = iota
	// CopyTermRequested means an OSC 52 request was emitted. It is a
	// request, not a completion: no terminal acknowledges it.
	CopyTermRequested
	// CopyFailure means no method accepted the bytes.
	CopyFailure
)

// String renders the kind for logs and test failures.
func (k CopyKind) String() string {
	switch k {
	case CopyLocalDone:
		return "local-done"
	case CopyTermRequested:
		return "term-requested"
	case CopyFailure:
		return "failure"
	default:
		return fmt.Sprintf("CopyKind(%d)", int(k))
	}
}

// CopyResult is the outcome of one copy attempt, plus the command the caller
// must run for a terminal request.
type CopyResult struct {
	Kind  CopyKind
	Bytes int
	Err   error
	Cmd   tea.Cmd
}

// MaxOSC52Bytes caps a terminal clipboard payload. Larger content must go
// through a local backend or the /export route; it is never truncated.
const MaxOSC52Bytes = 100 * 1024

// defaultCopyTimeout bounds a local clipboard write when CopyAdapter.Timeout
// is unset, so a hung helper cannot block the TUI.
const defaultCopyTimeout = 2 * time.Second

var (
	// ErrCopyTooLarge is returned when the payload exceeds MaxOSC52Bytes and
	// no local backend can take it.
	ErrCopyTooLarge = errors.New("copy: payload exceeds the OSC 52 limit and no local clipboard backend is available")
	// ErrNoClipboard is returned when no method is usable at all. It is also
	// the fallback cause rendered for a failure that recorded no error.
	ErrNoClipboard = errors.New("copy: no clipboard method available")
)

// CopyAdapter performs copy requests against a local backend or, when the
// session is remote or no local backend exists, the terminal via OSC 52.
type CopyAdapter struct {
	// Writer is the local clipboard backend. A nil Writer, or one reporting
	// !Available(), means no local method exists.
	Writer clipboard.Writer
	// Remote reports whether this session is remote (SSH), where the local
	// clipboard is the wrong destination. A nil Remote reports false.
	Remote func() bool
	// Timeout bounds a local write. Zero means 2s.
	Timeout time.Duration
}

// availableWriter is the optional interface a clipboard.Writer may implement
// to report whether a backend actually resolves. clipboard.LocalWriter
// implements it; the Writer interface itself does not, so a writer without it
// is assumed usable (it may still work).
type availableWriter interface{ Available() bool }

// local reports whether a local clipboard write is the right method: not
// remote, a writer is configured, and that writer reports itself available.
func (a CopyAdapter) local() bool {
	if a.Writer == nil {
		return false
	}
	if a.Remote != nil && a.Remote() {
		return false
	}
	if av, ok := a.Writer.(availableWriter); ok && !av.Available() {
		return false
	}
	return true
}

// writeLocal performs the bounded local write. The caller's ctx is respected:
// an already-cancelled ctx never reaches the backend.
func (a CopyAdapter) writeLocal(ctx context.Context, text string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	timeout := a.Timeout
	if timeout <= 0 {
		timeout = defaultCopyTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return a.Writer.Write(ctx, text)
}

// Copy writes text, choosing a method.
//
// A confirmed local write is reported as CopyLocalDone. Otherwise the terminal
// is asked via OSC 52 (CopyTermRequested) — a request, never a completion. A
// payload that exceeds MaxOSC52Bytes with no local backend is a CopyFailure;
// it is never truncated, and Bytes always reports the full len(text).
func (a CopyAdapter) Copy(ctx context.Context, text string) CopyResult {
	n := len(text)

	if a.local() {
		err := a.writeLocal(ctx, text)
		if err == nil {
			return CopyResult{Kind: CopyLocalDone, Bytes: n}
		}
		if n > MaxOSC52Bytes {
			// No route left: the local backend failed and OSC 52 cannot
			// carry this payload.
			return CopyResult{Kind: CopyFailure, Bytes: n, Err: err}
		}
		// The local write failed but the payload still fits in OSC 52, so
		// ask the terminal and keep the local error for reporting.
		return CopyResult{Kind: CopyTermRequested, Bytes: n, Err: err, Cmd: tea.SetClipboard(text)}
	}

	if n > MaxOSC52Bytes {
		return CopyResult{Kind: CopyFailure, Bytes: n, Err: ErrCopyTooLarge}
	}
	return CopyResult{Kind: CopyTermRequested, Bytes: n, Cmd: tea.SetClipboard(text)}
}

// Message renders a truthful, short feedback line for a result. It never
// claims a copy happened when only a terminal request was emitted.
func (r CopyResult) Message() string {
	switch r.Kind {
	case CopyLocalDone:
		return fmt.Sprintf("Copied %s to the clipboard.", copyByteLabel(r.Bytes))

	case CopyTermRequested:
		msg := fmt.Sprintf("Copy request sent to the terminal (%s via OSC 52; the terminal gives no acknowledgement).", copyByteLabel(r.Bytes))
		if r.Err != nil {
			msg += fmt.Sprintf(" Local clipboard failed: %v.", r.Err)
		}
		return msg

	default:
		err := r.Err
		if err == nil {
			err = ErrNoClipboard
		}
		return fmt.Sprintf("Copy failed (%s): %v. Use /export to save the transcript instead.", copyByteLabel(r.Bytes), err)
	}
}

// copyByteLabel renders a byte count exactly, with a human-readable size for
// large payloads. The exact count is always present so feedback never rounds
// away what was actually attempted.
func copyByteLabel(n int) string {
	if n == 1 {
		return "1 byte"
	}
	if n < 1024 {
		return fmt.Sprintf("%d bytes", n)
	}
	return fmt.Sprintf("%d bytes (%.1f KiB)", n, float64(n)/1024)
}
