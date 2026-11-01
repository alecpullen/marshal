package harness

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// writeHelper writes an executable POSIX shell helper to dir and returns its
// path. The helpers below are the test-owned subprocesses the session drives.
func writeHelper(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "helper.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatalf("write helper: %v", err)
	}
	return path
}

func TestSessionEcho(t *testing.T) {
	cfg := Config{
		BinaryPath: "cat", // use cat as a simple echo-like process
		Width:      80,
		Height:     24,
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	if err := s.Send("hello\n"); err != nil {
		t.Fatalf("Send: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := s.WaitFor(ctx, func(snap Snapshot) bool {
		return strings.Contains(string(snap.Content), "hello")
	}); err != nil {
		t.Fatalf("WaitFor: %v\noutput: %q", err, string(s.Output()))
	}
}

func TestSessionSendKeyEnter(t *testing.T) {
	cfg := Config{BinaryPath: "cat", Width: 80, Height: 24}
	s, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	if err := s.Send("line1"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if err := s.SendKey("enter"); err != nil {
		t.Fatalf("SendKey: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	// The viewport shows the visible text only: no CR, no escape bytes.
	if err := s.WaitFor(ctx, func(snap Snapshot) bool {
		return strings.Contains(strings.Join(snap.Lines, "\n"), "line1")
	}); err != nil {
		t.Fatalf("WaitFor: %v\nscreen: %q\noutput: %q", err, string(s.Snapshot().Content), string(s.Output()))
	}

	// The raw diagnostic tail still holds the CRLF the terminal echoed; the
	// viewport deliberately does not.
	if out := string(s.Output()); !strings.Contains(out, "line1\r\n") {
		t.Fatalf("Output = %q, want the raw CRLF echo", out)
	}
}

func TestSessionSnapshotIsCurrent(t *testing.T) {
	// The helper paints a marker, waits for a line of input, then erases the
	// marker line and writes its replacement: the second snapshot describes
	// the current screen, while the raw tail still holds the marker.
	bin := writeHelper(t, `
printf 'MARKER-ABC\r\nready'
dd bs=1 count=1 of=/dev/null 2>/dev/null
printf '\033[1;1H\033[2K\033[3;1Helapsed'
`)
	s, err := New(Config{BinaryPath: bin, Width: 40, Height: 6})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.WaitFor(ctx, func(snap Snapshot) bool {
		return strings.Contains(string(snap.Content), "MARKER-ABC")
	}); err != nil {
		t.Fatalf("WaitFor marker: %v\nscreen: %q\noutput: %q", err, string(s.Snapshot().Content), string(s.Output()))
	}
	if out := string(s.Output()); !strings.Contains(out, "MARKER-ABC") {
		t.Fatalf("Output = %q, want the raw marker before the redraw", out)
	}

	// The helper reads in canonical mode, so the acknowledgement is a line.
	if err := s.Send("x"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if err := s.SendKey("enter"); err != nil {
		t.Fatalf("SendKey: %v", err)
	}

	redraw, redrawCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer redrawCancel()
	if err := s.WaitFor(redraw, func(snap Snapshot) bool {
		content := string(snap.Content)
		return !strings.Contains(content, "MARKER-ABC") && strings.Contains(content, "elapsed")
	}); err != nil {
		t.Fatalf("WaitFor redraw: %v\nscreen: %q\noutput: %q", err, string(s.Snapshot().Content), string(s.Output()))
	}

	// The erased marker is gone from the screen but still in the raw tail:
	// the snapshot is the current viewport, not a replay of the output.
	if out := string(s.Output()); !strings.Contains(out, "MARKER-ABC") {
		t.Fatalf("Output = %q, want the raw marker retained after the redraw", out)
	}
}

func TestSessionRawOutputBounded(t *testing.T) {
	// The helper floods the PTY with more than the 1 MiB cap and ends with a
	// distinctive marker: the diagnostic tail keeps the ending and drops the
	// start, and the viewport stays at the session size.
	bin := writeHelper(t, `
printf 'START-MARKER'
head -c 1200000 /dev/zero | tr '\0' 'x'
printf '\r\nEND-VISIBLE'
sleep 5
`)
	s, err := New(Config{BinaryPath: bin, Width: 80, Height: 24})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := s.WaitFor(ctx, func(snap Snapshot) bool {
		return strings.Contains(string(snap.Content), "END-VISIBLE")
	}); err != nil {
		t.Fatalf("WaitFor ending: %v\noutput: %q", err, string(s.Output()))
	}

	out := s.Output()
	if len(out) > rawOutputCap {
		t.Fatalf("Output = %d bytes, want at most %d", len(out), rawOutputCap)
	}
	if strings.Contains(string(out), "START-MARKER") {
		t.Fatalf("Output still holds the head of the stream: the raw tail is not bounded")
	}
	if !strings.Contains(string(out), "END-VISIBLE") {
		t.Fatalf("Output = %d bytes, want the distinctive ending in the tail", len(out))
	}

	snap := s.Snapshot()
	if snap.Width != 80 || snap.Height != 24 {
		t.Fatalf("snapshot size = %dx%d, want 80x24", snap.Width, snap.Height)
	}
	if len(snap.Lines) != 24 {
		t.Fatalf("Lines = %d, want 24", len(snap.Lines))
	}
	for i, line := range snap.Lines {
		if width := len([]rune(line)); width > 80 {
			t.Fatalf("Lines[%d] = %q is %d cells wide, want at most 80", i, line, width)
		}
	}
	if snap.Err != nil {
		t.Fatalf("snapshot error = %v, want a clean decode", snap.Err)
	}
}

// shellQuote single-quotes s for safe interpolation into a POSIX shell script.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// TestSessionPasteFraming asserts that Paste writes exactly the bracketed-paste
// start marker, the payload unchanged, and the end marker, with no Enter. The
// helper reads in raw mode and reports the received bytes as hex in a file so
// the test can compare exact bytes rather than a decoded screen.
func TestSessionPasteFraming(t *testing.T) {
	const (
		start = "\x1b[200~"
		end   = "\x1b[201~"
	)
	cases := []struct {
		name    string
		payload string
	}{
		{"Empty", ""},
		{"Unicode", "héllo wörld ☃"},
		{"Multiline", "line one\nline two"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			framed := start + tc.payload + end
			out := filepath.Join(t.TempDir(), "received.hex")
			bin := writeHelper(t, fmt.Sprintf(`
stty raw -echo
printf 'READY\r\n'
dd bs=1 count=%d 2>/dev/null | od -An -v -tx1 | tr -d ' \n' > %s
printf 'DONE\r\n'
sleep 10
`, len(framed), shellQuote(out)))

			s, err := New(Config{BinaryPath: bin, Width: 80, Height: 24})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			defer s.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := s.WaitFor(ctx, func(snap Snapshot) bool {
				return strings.Contains(string(snap.Content), "READY")
			}); err != nil {
				t.Fatalf("WaitFor READY: %v\noutput: %q", err, string(s.Output()))
			}

			if err := s.Paste(tc.payload); err != nil {
				t.Fatalf("Paste: %v", err)
			}

			doneCtx, doneCancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer doneCancel()
			if err := s.WaitFor(doneCtx, func(snap Snapshot) bool {
				return strings.Contains(string(snap.Content), "DONE")
			}); err != nil {
				t.Fatalf("WaitFor DONE: %v\nscreen: %q\noutput: %q", err, string(s.Snapshot().Content), string(s.Output()))
			}

			data, err := os.ReadFile(out)
			if err != nil {
				t.Fatalf("read helper report: %v", err)
			}
			received, err := hex.DecodeString(strings.TrimSpace(string(data)))
			if err != nil {
				t.Fatalf("decode helper report %q: %v", string(data), err)
			}
			if !bytes.Equal(received, []byte(framed)) {
				t.Fatalf("received %q, want %q", received, framed)
			}
			// The framing carries the three regions in order.
			if !bytes.HasPrefix(received, []byte(start)) {
				t.Fatalf("received %q, want the bracketed-paste start %q", received, start)
			}
			if !bytes.HasSuffix(received, []byte(end)) {
				t.Fatalf("received %q, want the bracketed-paste end %q", received, end)
			}
			if body := received[len(start) : len(received)-len(end)]; !bytes.Equal(body, []byte(tc.payload)) {
				t.Fatalf("payload = %q, want %q unchanged between the framing", body, tc.payload)
			}
			// Paste never submits Enter, so the framing carries no CR.
			if bytes.Contains(received, []byte{'\r'}) {
				t.Fatalf("paste submitted an Enter: %q", received)
			}
		})
	}
}

// sizeObserverHelper reports the PTY window size each time a line arrives. It
// echoes nothing, so the only output is its own reports.
const sizeObserverHelper = `
stty -echo
printf 'INIT\r\n'
while read -r line; do
  printf 'SIZE:%s\r\n' "$(stty size)"
done
`

// TestSessionResize checks that a successful Resize updates the PTY window and
// the viewport model together: the helper's `stty size` and Snapshot agree at
// 80x24 and then at 100x30. A bounded race section drives resizes against
// concurrent snapshots and reads.
func TestSessionResize(t *testing.T) {
	bin := writeHelper(t, sizeObserverHelper)
	s, err := New(Config{BinaryPath: bin, Width: 80, Height: 24})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	waitContains := func(want string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := s.WaitFor(ctx, func(snap Snapshot) bool {
			return strings.Contains(string(snap.Content), want)
		}); err != nil {
			t.Fatalf("WaitFor %q: %v\nscreen: %q\noutput: %q", want, err, string(s.Snapshot().Content), string(s.Output()))
		}
	}

	waitContains("INIT")
	if snap := s.Snapshot(); snap.Width != 80 || snap.Height != 24 {
		t.Fatalf("snapshot = %dx%d, want 80x24 at start", snap.Width, snap.Height)
	}

	// Trigger the helper's initial observation and confirm it matches Snapshot.
	if err := s.SendKey("enter"); err != nil {
		t.Fatalf("SendKey enter: %v", err)
	}
	waitContains("SIZE:24 80")
	if snap := s.Snapshot(); snap.Width != 80 || snap.Height != 24 {
		t.Fatalf("snapshot = %dx%d, want 80x24 agreeing with the PTY", snap.Width, snap.Height)
	}

	if err := s.Resize(100, 30); err != nil {
		t.Fatalf("Resize: %v", err)
	}
	if err := s.SendKey("enter"); err != nil {
		t.Fatalf("SendKey enter: %v", err)
	}
	waitContains("SIZE:30 100")
	snap := s.Snapshot()
	if snap.Width != 100 || snap.Height != 30 {
		t.Fatalf("snapshot = %dx%d, want 100x30 agreeing with the PTY", snap.Width, snap.Height)
	}
	if len(snap.Lines) != 30 {
		t.Fatalf("Lines = %d, want 30", len(snap.Lines))
	}
	for i, line := range snap.Lines {
		if width := len([]rune(line)); width > 100 {
			t.Fatalf("Lines[%d] = %q is %d cells wide, want at most 100", i, line, width)
		}
	}

	// Resize races with concurrent snapshots and the read loop. Every snapshot
	// must describe one consistent viewport; the bounded context ends the
	// snapshotter without a timing sleep.
	raceCtx, raceCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer raceCancel()
	done := make(chan struct{})
	var wg sync.WaitGroup
	var raceErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-done:
				return
			case <-raceCtx.Done():
				return
			default:
			}
			snap := s.Snapshot()
			if len(snap.Lines) != snap.Height {
				raceErr = fmt.Errorf("snapshot has %d lines, want %d", len(snap.Lines), snap.Height)
				return
			}
			for _, line := range snap.Lines {
				if w := len([]rune(line)); w > snap.Width {
					raceErr = fmt.Errorf("snapshot line is %d cells wide, want at most %d", w, snap.Width)
					return
				}
			}
		}
	}()
	for i := 0; i < 25; i++ {
		w, h := 90, 20
		if i%2 == 0 {
			w, h = 110, 35
		}
		if err := s.Resize(w, h); err != nil {
			close(done)
			wg.Wait()
			t.Fatalf("Resize(%d,%d): %v", w, h, err)
		}
		// A read trigger keeps the read loop feeding the model while the next
		// resize and snapshot run.
		if err := s.SendKey("enter"); err != nil {
			close(done)
			wg.Wait()
			t.Fatalf("SendKey enter: %v", err)
		}
	}
	close(done)
	wg.Wait()
	if raceErr != nil {
		t.Fatal(raceErr)
	}

	// Leave the session at a deterministic size.
	if err := s.Resize(100, 30); err != nil {
		t.Fatalf("final Resize: %v", err)
	}
	if snap := s.Snapshot(); snap.Width != 100 || snap.Height != 30 {
		t.Fatalf("final snapshot = %dx%d, want 100x30", snap.Width, snap.Height)
	}
}

// TestSessionResizeInvalid checks that out-of-range dimensions are rejected
// before the ioctl and leave both the PTY and the model unchanged.
func TestSessionResizeInvalid(t *testing.T) {
	bin := writeHelper(t, sizeObserverHelper)
	s, err := New(Config{BinaryPath: bin, Width: 80, Height: 24})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.WaitFor(ctx, func(snap Snapshot) bool {
		return strings.Contains(string(snap.Content), "INIT")
	}); err != nil {
		t.Fatalf("WaitFor INIT: %v\noutput: %q", err, string(s.Output()))
	}

	invalids := []struct{ width, height int }{
		{0, 24},
		{80, 0},
		{-1, 24},
		{80, -1},
		{65536, 24},
		{80, 65536},
		{70000, 70000},
	}
	for _, c := range invalids {
		if err := s.Resize(c.width, c.height); err == nil {
			t.Fatalf("Resize(%d,%d) = nil error, want an out-of-range error", c.width, c.height)
		}
		snap := s.Snapshot()
		if snap.Width != 80 || snap.Height != 24 {
			t.Fatalf("after Resize(%d,%d) snapshot = %dx%d, want an unchanged 80x24", c.width, c.height, snap.Width, snap.Height)
		}
	}

	// The PTY itself must still report 80x24 after the rejected resizes.
	if err := s.SendKey("enter"); err != nil {
		t.Fatalf("SendKey enter: %v", err)
	}
	sizeCtx, sizeCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer sizeCancel()
	if err := s.WaitFor(sizeCtx, func(snap Snapshot) bool {
		return strings.Contains(string(snap.Content), "SIZE:24 80")
	}); err != nil {
		t.Fatalf("WaitFor PTY size after invalid resizes: %v\nscreen: %q\noutput: %q", err, string(s.Snapshot().Content), string(s.Output()))
	}
	if snap := s.Snapshot(); snap.Width != 80 || snap.Height != 24 {
		t.Fatalf("snapshot = %dx%d after invalid resizes, want an unchanged 80x24", snap.Width, snap.Height)
	}
}

// waitForMarker waits until want appears in the viewport, failing with the
// screen and the raw tail when it never does.
func waitForMarker(t *testing.T, s *Session, want string, timeout time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := s.WaitFor(ctx, func(snap Snapshot) bool {
		return strings.Contains(string(snap.Content), want)
	}); err != nil {
		t.Fatalf("WaitFor %q: %v\nscreen: %q\noutput: %q", want, err, string(s.Snapshot().Content), string(s.Output()))
	}
}

// runWithin runs fn on its own goroutine and fails unless it returns within
// timeout. It is the bounded-deadline assertion: a teardown that hangs shows up
// as a test failure rather than a suite timeout.
func runWithin(t *testing.T, timeout time.Duration, what string, fn func() error) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- fn() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
	case <-time.After(timeout):
		t.Fatalf("%s did not return within %s", what, timeout)
	}
}

// TestSessionWaitGraceful observes a natural child exit: the helper waits for a
// test-owned input, writes a final marker and exits with status zero, so Wait
// returns nil without killing anything. Repeated Close and Wait neither wait nor
// reap a second time, and the final screen survives cleanup.
func TestSessionWaitGraceful(t *testing.T) {
	bin := writeHelper(t, `
printf 'READY\r\n'
dd bs=1 count=1 of=/dev/null 2>/dev/null
printf 'FINAL-MARKER\r\n'
exit 0
`)
	s, err := New(Config{BinaryPath: bin, Width: 40, Height: 8})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	waitForMarker(t, s, "READY", 10*time.Second)

	// The helper reads one canonical-mode line, so acknowledgement is a line.
	if err := s.Send("x"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if err := s.SendKey("enter"); err != nil {
		t.Fatalf("SendKey enter: %v", err)
	}
	waitForMarker(t, s, "FINAL-MARKER", 10*time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.Wait(ctx); err != nil {
		t.Fatalf("Wait = %v, want nil for a clean exit\noutput: %q", err, string(s.Output()))
	}

	// The child has already been reaped: a second Close and a later Wait must
	// return at once with the recorded result instead of waiting or reaping.
	runWithin(t, 15*time.Second, "repeated Close and Wait", func() error {
		if err := s.Close(); err != nil {
			return fmt.Errorf("first Close: %w", err)
		}
		if err := s.Close(); err != nil {
			return fmt.Errorf("second Close: %w", err)
		}
		waitCtx, waitCancel := context.WithTimeout(context.Background(), time.Second)
		defer waitCancel()
		if err := s.Wait(waitCtx); err != nil {
			return fmt.Errorf("Wait after Close: %w", err)
		}
		return nil
	})

	// Cleanup preserved the final screen and the raw tail.
	if snap := s.Snapshot(); !strings.Contains(string(snap.Content), "FINAL-MARKER") {
		t.Fatalf("final snapshot = %q, want the marker preserved after Close", string(snap.Content))
	}
	if out := string(s.Output()); !strings.Contains(out, "FINAL-MARKER") {
		t.Fatalf("Output = %q, want the marker preserved after Close", out)
	}
}

// TestSessionWaitTimeoutAndClose covers the other half of the contract: a child
// that refuses to exit makes Wait report its deadline without being touched, and
// Close then kills and reaps it inside its own deadline.
func TestSessionWaitTimeoutAndClose(t *testing.T) {
	bin := writeHelper(t, `
printf 'STICKY\r\n'
while :; do sleep 1; done
`)
	s, err := New(Config{BinaryPath: bin, Width: 40, Height: 8})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	waitForMarker(t, s, "STICKY", 10*time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	err = s.Wait(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Wait = %v, want context.DeadlineExceeded for a child that keeps running", err)
	}

	// A cancelled Wait leaves the child and its screen untouched: cleanup, not
	// Wait, owns the kill.
	if !strings.Contains(string(s.Snapshot().Content), "STICKY") {
		t.Fatalf("snapshot = %q, want the child's screen still readable after a cancelled Wait", string(s.Snapshot().Content))
	}

	// Close force-kills and reaps the stuck child within its bounded deadline.
	// The recorded result is the kill's exit status, not a teardown failure.
	runWithin(t, 15*time.Second, "Close of a stuck child", func() error {
		err := s.Close()
		if err == nil {
			return nil
		}
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			return fmt.Errorf("Close = %v, want nil or the killed child's exit status", err)
		}
		return nil
	})

	// The exit was recorded during teardown, so a later Wait returns it at once
	// rather than reporting its own deadline.
	finalCtx, finalCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer finalCancel()
	if err := s.Wait(finalCtx); err == nil {
		t.Fatalf("Wait after Close = nil, want the recorded exit status of the killed child")
	} else if errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Wait after Close reported its deadline instead of returning the recorded result")
	}
}

// TestSessionWaitConcurrentClose drives Wait and Close concurrently: every
// waiter observes the one recorded exit and Close tears down exactly once. It is
// meaningful under -race.
func TestSessionWaitConcurrentClose(t *testing.T) {
	bin := writeHelper(t, `
printf 'BUSY\r\n'
sleep 30
`)
	s, err := New(Config{BinaryPath: bin, Width: 40, Height: 8})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	waitForMarker(t, s, "BUSY", 10*time.Second)

	const waiters = 8
	errs := make(chan error, waiters+1)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < waiters; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			waitCtx, waitCancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer waitCancel()
			errs <- s.Wait(waitCtx)
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		errs <- s.Close()
	}()
	close(start)
	wg.Wait()
	close(errs)

	// The child never exits on its own, so every observer must report the kill's
	// exit status: a waiter that timed out would mean Close never completed the
	// exit, and a nil would mean the exit was never recorded.
	for err := range errs {
		if errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("a concurrent Wait reported its deadline: teardown never completed the exit")
			continue
		}
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Errorf("concurrent Wait/Close = %v, want the killed child's exit status", err)
		}
	}
}
