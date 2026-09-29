package tui

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"marshal/internal/app/clipboard"
)

// fakeClipboard is a clipboard.Writer that records every write verbatim. It
// reports availability through Available(), so the adapter's optional-interface
// probe is exercised. The zero value is available and succeeds.
type fakeClipboard struct {
	mu          sync.Mutex
	texts       []string
	err         error
	unavailable bool
	// block, when non-nil, makes Write wait until it is closed or ctx is done.
	block chan struct{}
}

func (f *fakeClipboard) Write(ctx context.Context, text string) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	f.mu.Lock()
	f.texts = append(f.texts, text)
	f.mu.Unlock()
	return f.err
}

func (f *fakeClipboard) Available() bool { return !f.unavailable }

func (f *fakeClipboard) writes() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.texts...)
}

// plainClipboard implements clipboard.Writer but not Available(), so the
// adapter must treat it as usable.
type plainClipboard struct {
	mu    sync.Mutex
	texts []string
	err   error
}

func (p *plainClipboard) Write(ctx context.Context, text string) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	p.mu.Lock()
	p.texts = append(p.texts, text)
	p.mu.Unlock()
	return p.err
}

func (p *plainClipboard) writes() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.texts...)
}

var (
	_ clipboard.Writer = (*fakeClipboard)(nil)
	_ clipboard.Writer = (*plainClipboard)(nil)
)

// copyWithin runs Copy in a goroutine and fails the test if it does not return
// within d, so a hung clipboard write cannot hang the whole package.
func copyWithin(t *testing.T, d time.Duration, a CopyAdapter, text string) CopyResult {
	t.Helper()
	done := make(chan CopyResult, 1)
	go func() { done <- a.Copy(context.Background(), text) }()
	select {
	case res := <-done:
		return res
	case <-time.After(d):
		t.Fatalf("Copy did not return within %v: the local write ignored its timeout", d)
		return CopyResult{}
	}
}

// TestCopyDeliversExactBytes pins the bytes handed to a local backend for the
// awkward payloads: Unicode, tabs, trailing newlines, CRLF, empty, long lines.
func TestCopyDeliversExactBytes(t *testing.T) {
	cases := []struct {
		name string
		text string
	}{
		{"empty", ""},
		{"unicode", "héllo wörld — 日本語 🎉"},
		{"tabs", "a\tb\t\tc"},
		{"trailing newline", "line\n"},
		{"trailing newlines", "line\n\n\n"},
		{"crlf", "a\r\nb\r\n"},
		{"long line", strings.Repeat("x", 5000)},
		{"code block", "func main() {\n\tfmt.Println(\"héllo\")\n}\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := &fakeClipboard{}
			a := CopyAdapter{Writer: w}

			res := a.Copy(context.Background(), tc.text)

			if res.Kind != CopyLocalDone {
				t.Fatalf("Kind = %v, want CopyLocalDone (err=%v)", res.Kind, res.Err)
			}
			if res.Bytes != len(tc.text) {
				t.Errorf("Bytes = %d, want %d", res.Bytes, len(tc.text))
			}
			if res.Cmd != nil {
				t.Errorf("Cmd = non-nil, want nil for a confirmed local copy")
			}
			got := w.writes()
			if len(got) != 1 {
				t.Fatalf("writer saw %d writes, want exactly 1", len(got))
			}
			if got[0] != tc.text {
				t.Errorf("writer got %q, want %q", got[0], tc.text)
			}
		})
	}
}

// TestCopyWriterWithoutAvailableCountsAsUsable covers the optional-interface
// probe: a Writer that does not implement Available() may still work, so it
// must be tried rather than skipped.
func TestCopyWriterWithoutAvailableCountsAsUsable(t *testing.T) {
	w := &plainClipboard{}
	a := CopyAdapter{Writer: w}

	res := a.Copy(context.Background(), "hello")

	if res.Kind != CopyLocalDone {
		t.Fatalf("Kind = %v, want CopyLocalDone (err=%v)", res.Kind, res.Err)
	}
	got := w.writes()
	if len(got) != 1 || got[0] != "hello" {
		t.Fatalf("writer saw %q, want exactly one write of %q", got, "hello")
	}
}

// TestCopyNoLocalBackendRequestsTerminal covers the two "no local method"
// shapes: a nil Writer and a Writer reporting !Available().
func TestCopyNoLocalBackendRequestsTerminal(t *testing.T) {
	t.Run("nil writer", func(t *testing.T) {
		a := CopyAdapter{}
		res := a.Copy(context.Background(), "hello")

		if res.Kind != CopyTermRequested {
			t.Fatalf("Kind = %v, want CopyTermRequested (err=%v)", res.Kind, res.Err)
		}
		if res.Cmd == nil {
			t.Fatal("Cmd = nil, want a terminal request command")
		}
		if msg := res.Cmd(); msg == nil {
			t.Error("Cmd produced a nil message")
		}
		if res.Bytes != 5 {
			t.Errorf("Bytes = %d, want 5", res.Bytes)
		}
	})

	t.Run("unavailable writer", func(t *testing.T) {
		w := &fakeClipboard{unavailable: true}
		a := CopyAdapter{Writer: w}
		res := a.Copy(context.Background(), "hello")

		if res.Kind != CopyTermRequested {
			t.Fatalf("Kind = %v, want CopyTermRequested (err=%v)", res.Kind, res.Err)
		}
		if res.Cmd == nil {
			t.Fatal("Cmd = nil, want a terminal request command")
		}
		if n := len(w.writes()); n != 0 {
			t.Errorf("unavailable writer saw %d writes, want 0", n)
		}
	})
}

// TestCopyRemoteSessionIgnoresLocalWriter: over SSH the local helper would
// write to the wrong machine's clipboard, so the terminal must be asked even
// though a local backend resolves.
func TestCopyRemoteSessionIgnoresLocalWriter(t *testing.T) {
	w := &fakeClipboard{}
	a := CopyAdapter{Writer: w, Remote: func() bool { return true }}

	res := a.Copy(context.Background(), "hello")

	if res.Kind != CopyTermRequested {
		t.Fatalf("Kind = %v, want CopyTermRequested (err=%v)", res.Kind, res.Err)
	}
	if res.Cmd == nil {
		t.Fatal("Cmd = nil, want a terminal request command")
	}
	if n := len(w.writes()); n != 0 {
		t.Errorf("remote session wrote to the local clipboard %d times, want 0", n)
	}
}

// TestCopyTooLargeWithoutLocalBackend: never truncate, always report the full
// byte count, and never claim success.
func TestCopyTooLargeWithoutLocalBackend(t *testing.T) {
	text := strings.Repeat("a", MaxOSC52Bytes+1)
	a := CopyAdapter{}

	res := a.Copy(context.Background(), text)

	if res.Kind != CopyFailure {
		t.Fatalf("Kind = %v, want CopyFailure (err=%v)", res.Kind, res.Err)
	}
	if !errors.Is(res.Err, ErrCopyTooLarge) {
		t.Errorf("Err = %v, want ErrCopyTooLarge", res.Err)
	}
	if res.Bytes != len(text) {
		t.Errorf("Bytes = %d, want the full length %d (no truncation)", res.Bytes, len(text))
	}
	if res.Cmd != nil {
		t.Errorf("Cmd = non-nil, want nil on failure")
	}
}

// TestCopyTooLargeWithLocalBackendUsesLocal: a local backend has no OSC 52 cap,
// so the full untruncated payload goes to it.
func TestCopyTooLargeWithLocalBackendUsesLocal(t *testing.T) {
	text := strings.Repeat("a", MaxOSC52Bytes+1)
	w := &fakeClipboard{}
	a := CopyAdapter{Writer: w}

	res := a.Copy(context.Background(), text)

	if res.Kind != CopyLocalDone {
		t.Fatalf("Kind = %v, want CopyLocalDone (err=%v)", res.Kind, res.Err)
	}
	if res.Bytes != len(text) {
		t.Errorf("Bytes = %d, want %d", res.Bytes, len(text))
	}
	got := w.writes()
	if len(got) != 1 {
		t.Fatalf("writer saw %d writes, want exactly 1", len(got))
	}
	if len(got[0]) != len(text) || got[0] != text {
		t.Errorf("writer received %d bytes, want the full untruncated %d bytes", len(got[0]), len(text))
	}
}

// TestCopyLocalErrorFallsBackToTerminal: a failed local write that still fits
// in OSC 52 asks the terminal, preserving the local error for reporting.
func TestCopyLocalErrorFallsBackToTerminal(t *testing.T) {
	writeErr := errors.New("clipboard: pbcopy: exit status 1")
	w := &fakeClipboard{err: writeErr}
	a := CopyAdapter{Writer: w}

	res := a.Copy(context.Background(), "hello")

	if res.Kind != CopyTermRequested {
		t.Fatalf("Kind = %v, want CopyTermRequested", res.Kind)
	}
	if res.Cmd == nil {
		t.Fatal("Cmd = nil, want a terminal request command")
	}
	if !errors.Is(res.Err, writeErr) {
		t.Errorf("Err = %v, want the local write error preserved", res.Err)
	}
	if res.Bytes != 5 {
		t.Errorf("Bytes = %d, want 5", res.Bytes)
	}
}

// TestCopyLocalErrorTooLargeIsFailure: a failed local write that does not fit
// in OSC 52 has no route left, so it is a failure wrapping the write error.
func TestCopyLocalErrorTooLargeIsFailure(t *testing.T) {
	writeErr := errors.New("clipboard: pbcopy: exit status 1")
	text := strings.Repeat("a", MaxOSC52Bytes+1)
	w := &fakeClipboard{err: writeErr}
	a := CopyAdapter{Writer: w}

	res := a.Copy(context.Background(), text)

	if res.Kind != CopyFailure {
		t.Fatalf("Kind = %v, want CopyFailure", res.Kind)
	}
	if !errors.Is(res.Err, writeErr) {
		t.Errorf("Err = %v, want the write error", res.Err)
	}
	if res.Bytes != len(text) {
		t.Errorf("Bytes = %d, want %d", res.Bytes, len(text))
	}
	if res.Cmd != nil {
		t.Errorf("Cmd = non-nil, want nil on failure")
	}
}

// TestCopyLocalWriteHonoursTimeout: a blocking backend must not hang Copy.
func TestCopyLocalWriteHonoursTimeout(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	w := &fakeClipboard{block: block}
	a := CopyAdapter{Writer: w, Timeout: 20 * time.Millisecond}

	res := copyWithin(t, 5*time.Second, a, "hello")

	if res.Kind != CopyTermRequested {
		t.Fatalf("Kind = %v, want CopyTermRequested after the local write timed out", res.Kind)
	}
	if res.Err == nil {
		t.Error("Err = nil, want the timed-out local write error")
	}
	if n := len(w.writes()); n != 0 {
		t.Errorf("writer saw %d writes, want 0", n)
	}
}

// TestCopyLocalWriteDefaultTimeout: a zero Timeout means 2s, not "no timeout"
// and not "cancel immediately".
func TestCopyLocalWriteDefaultTimeout(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	w := &fakeClipboard{block: block}
	a := CopyAdapter{Writer: w}

	start := time.Now()
	res := copyWithin(t, 5*time.Second, a, "hello")
	elapsed := time.Since(start)

	if elapsed < time.Second {
		t.Errorf("Copy returned after %v; a zero Timeout should default to ~2s", elapsed)
	}
	if res.Kind != CopyTermRequested {
		t.Fatalf("Kind = %v, want CopyTermRequested after the default timeout", res.Kind)
	}
	if res.Err == nil {
		t.Error("Err = nil, want the timed-out local write error")
	}
}

// TestCopyRespectsCancelledContext: an already-cancelled caller ctx must not
// reach the backend, and must never be reported as a confirmed local copy.
func TestCopyRespectsCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	w := &fakeClipboard{}
	a := CopyAdapter{Writer: w}

	res := a.Copy(ctx, "hello")

	if res.Kind == CopyLocalDone {
		t.Fatalf("Kind = CopyLocalDone, want no local success with a cancelled context")
	}
	if res.Err == nil {
		t.Error("Err = nil, want the cancellation error")
	}
	if n := len(w.writes()); n != 0 {
		t.Errorf("writer saw %d writes, want 0", n)
	}
	if (res.Kind == CopyTermRequested) != (res.Cmd != nil) {
		t.Errorf("Kind = %v with Cmd nil=%v; Cmd must be set exactly for CopyTermRequested", res.Kind, res.Cmd == nil)
	}
}

// TestCopyCmdNilUnlessTerminalRequest pins the Cmd contract across all kinds.
func TestCopyCmdNilUnlessTerminalRequest(t *testing.T) {
	writeErr := errors.New("clipboard: pbcopy: exit status 1")
	big := strings.Repeat("a", MaxOSC52Bytes+1)

	cases := []struct {
		name     string
		a        CopyAdapter
		text     string
		wantKind CopyKind
	}{
		{"local done", CopyAdapter{Writer: &fakeClipboard{}}, "hello", CopyLocalDone},
		{"terminal request", CopyAdapter{}, "hello", CopyTermRequested},
		{"failure", CopyAdapter{}, big, CopyFailure},
		{"local error too large", CopyAdapter{Writer: &fakeClipboard{err: writeErr}}, big, CopyFailure},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := tc.a.Copy(context.Background(), tc.text)
			if res.Kind != tc.wantKind {
				t.Fatalf("Kind = %v, want %v (err=%v)", res.Kind, tc.wantKind, res.Err)
			}
			if tc.wantKind == CopyTermRequested {
				if res.Cmd == nil {
					t.Error("Cmd = nil, want a terminal request command")
				}
				return
			}
			if res.Cmd != nil {
				t.Error("Cmd = non-nil, want nil for a non-terminal result")
			}
		})
	}
}

// TestCopyBytesAlwaysFullLength: Bytes is always len(text) in bytes, never a
// rune count and never a truncated length.
func TestCopyBytesAlwaysFullLength(t *testing.T) {
	multi := "héllo wörld — 日本語 🎉"
	if utf8.RuneCountInString(multi) == len(multi) {
		t.Fatalf("fixture %q is not multi-byte", multi)
	}

	cases := []struct {
		name string
		a    CopyAdapter
		text string
	}{
		{"local", CopyAdapter{Writer: &fakeClipboard{}}, multi},
		{"terminal", CopyAdapter{}, multi},
		{"remote", CopyAdapter{Writer: &fakeClipboard{}, Remote: func() bool { return true }}, multi},
		{"local error", CopyAdapter{Writer: &fakeClipboard{err: errors.New("boom")}}, multi},
		{"too large", CopyAdapter{}, strings.Repeat("é", MaxOSC52Bytes)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := tc.a.Copy(context.Background(), tc.text)
			if res.Bytes != len(tc.text) {
				t.Errorf("Bytes = %d, want len(text) = %d", res.Bytes, len(tc.text))
			}
		})
	}
}

// TestCopyResultMessage pins the truthful feedback line for each kind.
func TestCopyResultMessage(t *testing.T) {
	t.Run("local done", func(t *testing.T) {
		msg := CopyResult{Kind: CopyLocalDone, Bytes: 5}.Message()
		if !strings.Contains(msg, "Copied") {
			t.Errorf("message %q does not state the text was copied", msg)
		}
		if !strings.Contains(msg, "5") {
			t.Errorf("message %q does not report the byte count", msg)
		}
		if strings.Contains(strings.ToLower(msg), "request") {
			t.Errorf("message %q talks about a request despite a confirmed local copy", msg)
		}
	})

	t.Run("terminal request", func(t *testing.T) {
		msg := CopyResult{Kind: CopyTermRequested, Bytes: 5}.Message()
		if !strings.Contains(msg, "request") {
			t.Errorf("message %q does not say a request was made", msg)
		}
		if strings.Contains(msg, "Copied") || strings.Contains(msg, "copied") {
			t.Errorf("message %q falsely claims a copy", msg)
		}
		if !strings.Contains(strings.ToLower(msg), "osc 52") {
			t.Errorf("message %q does not name OSC 52", msg)
		}
		if !strings.Contains(strings.ToLower(msg), "acknowledg") {
			t.Errorf("message %q does not say OSC 52 has no acknowledgement", msg)
		}
		if !strings.Contains(msg, "5") {
			t.Errorf("message %q does not report the byte count", msg)
		}
	})

	t.Run("failure too large", func(t *testing.T) {
		res := CopyResult{Kind: CopyFailure, Bytes: MaxOSC52Bytes + 1, Err: ErrCopyTooLarge}
		msg := res.Message()
		if !strings.Contains(msg, strconv.Itoa(res.Bytes)) {
			t.Errorf("message %q does not report the exact byte count %d", msg, res.Bytes)
		}
		if !strings.Contains(msg, ErrCopyTooLarge.Error()) {
			t.Errorf("message %q does not include the underlying error text", msg)
		}
		if !strings.Contains(msg, "/export") {
			t.Errorf("message %q does not name /export as the actionable route", msg)
		}
		if strings.Contains(msg, "Copied") || strings.Contains(msg, "copied") {
			t.Errorf("message %q falsely claims a copy", msg)
		}
	})

	t.Run("failure with write error", func(t *testing.T) {
		writeErr := errors.New("clipboard: pbcopy: exit status 1")
		res := CopyResult{Kind: CopyFailure, Bytes: 12, Err: writeErr}
		msg := res.Message()
		if !strings.Contains(msg, writeErr.Error()) {
			t.Errorf("message %q does not include the write error text", msg)
		}
		if !strings.Contains(msg, "12") {
			t.Errorf("message %q does not report the byte count", msg)
		}
		if !strings.Contains(msg, "/export") {
			t.Errorf("message %q does not name /export as the actionable route", msg)
		}
	})
}

// TestCopyZeroValueAdapter: the zero value must not panic and must never
// report a false success.
func TestCopyZeroValueAdapter(t *testing.T) {
	var a CopyAdapter

	res := a.Copy(context.Background(), "hello")
	if res.Kind == CopyLocalDone {
		t.Fatalf("zero-value adapter reported a confirmed local copy")
	}
	if res.Kind != CopyTermRequested {
		t.Errorf("Kind = %v, want CopyTermRequested", res.Kind)
	}
	if res.Cmd == nil {
		t.Error("Cmd = nil, want a terminal request command")
	}
	if res.Bytes != 5 {
		t.Errorf("Bytes = %d, want 5", res.Bytes)
	}

	big := strings.Repeat("a", MaxOSC52Bytes+1)
	res = a.Copy(context.Background(), big)
	if res.Kind != CopyFailure {
		t.Errorf("Kind = %v, want CopyFailure for an oversized payload", res.Kind)
	}
	if !errors.Is(res.Err, ErrCopyTooLarge) {
		t.Errorf("Err = %v, want ErrCopyTooLarge", res.Err)
	}
	if res.Cmd != nil {
		t.Error("Cmd = non-nil, want nil on failure")
	}

	// A nil ctx must not panic either.
	res = a.Copy(nil, "hello")
	if res.Kind == CopyLocalDone {
		t.Errorf("nil ctx produced a confirmed local copy")
	}
}
