package harness

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"os"
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
