package harness

import (
	"context"
	"strings"
	"testing"
	"time"
)

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
	if err := s.WaitFor(ctx, func(snap Snapshot) bool {
		return strings.Contains(string(snap.Content), "line1\r\n")
	}); err != nil {
		t.Fatalf("WaitFor: %v\noutput: %q", err, string(s.Output()))
	}
}
