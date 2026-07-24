package native

import (
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
)

func TestModeRequestApprovedRelaysChosenMode(t *testing.T) {
	root := t.TempDir()
	state := session.New(config.Config{}, root, time.Now(), session.Persistence{})
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}, SessionState: state}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	args := `{"mode":"edit"}`
	type result struct {
		res registry.ToolResult
		err error
	}
	done := make(chan result, 1)
	go func() {
		res, err := invokeTool(t, reg, "mode.request", args)
		done <- result{res, err}
	}()

	deadline := time.After(2 * time.Second)
	var pending *session.PendingToolCall
	for {
		pending = state.PendingApproval()
		if pending != nil {
			break
		}
		select {
		case <-deadline:
			t.Fatal("mode.request did not set a pending approval")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if pending.Name != "mode.request" {
		t.Fatalf("pending Name = %q, want mode.request", pending.Name)
	}
	if !strings.Contains(pending.Reason, "mode-elevation") {
		t.Fatalf("pending Reason = %q, should contain mode-elevation", pending.Reason)
	}

	pending.Respond(session.UserApprovalDecision{Approved: true, Edited: "copilot"})

	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("mode.request error: %v", r.err)
		}
		if !strings.Contains(r.res.Content, "copilot") {
			t.Fatalf("result content %q should mention copilot", r.res.Content)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("mode.request did not return after approval")
	}
}

func TestModeRequestDeniedStaysInDefault(t *testing.T) {
	root := t.TempDir()
	state := session.New(config.Config{}, root, time.Now(), session.Persistence{})
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}, SessionState: state}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	args := `{"mode":"edit"}`
	type result struct {
		res registry.ToolResult
		err error
	}
	done := make(chan result, 1)
	go func() {
		res, err := invokeTool(t, reg, "mode.request", args)
		done <- result{res, err}
	}()

	deadline := time.After(2 * time.Second)
	for {
		if p := state.PendingApproval(); p != nil {
			p.Respond(session.UserApprovalDecision{Approved: false})
			break
		}
		select {
		case <-deadline:
			t.Fatal("mode.request did not set a pending approval")
		case <-time.After(10 * time.Millisecond):
		}
	}

	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("mode.request error: %v", r.err)
		}
		if !strings.Contains(r.res.Content, "denied") {
			t.Fatalf("denied result content %q should mention denied", r.res.Content)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("mode.request did not return after denial")
	}
}
