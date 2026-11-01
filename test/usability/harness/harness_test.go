package harness

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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
