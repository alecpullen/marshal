package acp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"marshal/internal/acpclient"
	"marshal/internal/acpwire"
	"marshal/internal/interaction"
)

// createReplaceTestSession starts one routed session in work and returns its
// id, closing it when the test ends.
func createReplaceTestSession(t *testing.T, m *SessionManager, work string) string {
	t.Helper()
	res, err := m.Create(context.Background(), json.RawMessage(`{"cwd":`+jsonString(work)+`,"mcpServers":[]}`))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	id := res.(SessionResponse).SessionID
	t.Cleanup(func() { _ = m.Close(context.Background(), id) })
	return id
}

// TestAReplacingNewSessionKeepsTheRuntime pins decision 5: session/new with
// replaces builds the new session inside the same runtime, files the runtime
// under the new id, and leaves the replaced session closed.
func TestAReplacingNewSessionKeepsTheRuntime(t *testing.T) {
	m, work := newRoutingSessionManager(t)
	m.SetTurnCanceller(noopCancel())
	oldID := createReplaceTestSession(t, m, work)

	rt, ok := m.Get(oldID)
	if !ok {
		t.Fatalf("Get(%s) ok=false", oldID)
	}
	oldService, ok := m.Service(oldID)
	if !ok {
		t.Fatalf("Service(%s) ok=false", oldID)
	}

	res, err := m.Create(context.Background(), json.RawMessage(
		`{"replaces":`+jsonString(oldID)+`,"name":"next"}`))
	if err != nil {
		t.Fatalf("replacement: %v", err)
	}
	newID := res.(SessionResponse).SessionID
	if newID == "" || newID == oldID {
		t.Fatalf("the replacement named %q, want a new id", newID)
	}

	got, ok := m.Get(newID)
	if !ok || got != rt {
		t.Errorf("Get(%s) = (%v, %v), want the same runtime under the new id", newID, got, ok)
	}
	if _, ok := m.Get(oldID); ok {
		t.Errorf("Get(%s) still resolves after the replacement", oldID)
	}
	if rt.SessionID != newID {
		t.Errorf("the runtime's SessionID = %q, want %q", rt.SessionID, newID)
	}
	if title := rt.State.Title(); title != "next" {
		t.Errorf("the new state's title = %q, want next", title)
	}
	next, ok := m.Service(newID)
	if !ok || next == nil {
		t.Fatalf("Service(%s) ok=false after the replacement", newID)
	}
	if next.ID() != newID {
		t.Errorf("Service(%s).ID() = %q, want the new session", newID, next.ID())
	}
	if err := oldService.Stop(context.Background(), "stop"); !acpclient.IsCode(err, acpwire.CodeSessionClosed) {
		t.Errorf("the replaced session's Stop = %v, want code %q", err, acpwire.CodeSessionClosed)
	}

	_ = m.Close(context.Background(), newID)
}

// TestAFailedReplacementKeepsTheSession pins R7: a replacement that cannot
// build the new session changes nothing.
func TestAFailedReplacementKeepsTheSession(t *testing.T) {
	m, work := newRoutingSessionManager(t)
	m.SetTurnCanceller(noopCancel())
	oldID := createReplaceTestSession(t, m, work)

	rt, ok := m.Get(oldID)
	if !ok {
		t.Fatalf("Get(%s) ok=false", oldID)
	}
	before, ok := m.Service(oldID)
	if !ok {
		t.Fatalf("Service(%s) ok=false", oldID)
	}
	// The runtime's replacement validates the interaction defaults, so an
	// unknown mode makes it fail after the call was accepted.
	rt.Config.Agent.Mode = interaction.Mode("bogus")

	_, err := m.Create(context.Background(), json.RawMessage(
		`{"replaces":`+jsonString(oldID)+`,"name":"next"}`))
	if err == nil || !strings.Contains(err.Error(), "invalid interaction defaults") {
		t.Fatalf("the failed replacement = %v, want the runtime's interaction refusal", err)
	}

	got, ok := m.Get(oldID)
	if !ok || got != rt {
		t.Errorf("Get(%s) = (%v, %v) after the failure, want the runtime unchanged", oldID, got, ok)
	}
	if rt.SessionID != oldID {
		t.Errorf("the runtime's SessionID = %q after the failure, want %q", rt.SessionID, oldID)
	}
	after, ok := m.Service(oldID)
	if !ok || after != before {
		t.Errorf("Service(%s) = (%v, %v) after the failure, want the same session", oldID, after, ok)
	}
}

// TestAReplacementOfAnUnknownSessionIsRefused pins that a replaces naming no
// session is the unknown-session error, and starts nothing.
func TestAReplacementOfAnUnknownSessionIsRefused(t *testing.T) {
	m, work := newRoutingSessionManager(t)
	m.SetTurnCanceller(noopCancel())
	id := createReplaceTestSession(t, m, work)

	_, err := m.Create(context.Background(), json.RawMessage(`{"replaces":"nope","name":"next"}`))
	if err == nil || !strings.Contains(err.Error(), "unknown session") {
		t.Fatalf("the replacement = %v, want the unknown-session refusal", err)
	}
	if all := m.All(); len(all) != 1 {
		t.Errorf("All() lists %d runtimes, want only the one the test started", len(all))
	}
	if _, ok := m.Get(id); !ok {
		t.Errorf("Get(%s) ok=false after the refused replacement", id)
	}
}

// TestANewSessionWithoutReplacesStartsARuntime pins R9: session/new without
// replaces is today's, for every client.
func TestANewSessionWithoutReplacesStartsARuntime(t *testing.T) {
	m, work := newRoutingSessionManager(t)
	m.SetTurnCanceller(noopCancel())
	first := createReplaceTestSession(t, m, work)
	second := createReplaceTestSession(t, m, work)

	if first == second {
		t.Fatalf("the two session/new calls answered the same id %q", first)
	}
	one, ok := m.Get(first)
	if !ok {
		t.Fatalf("Get(%s) ok=false", first)
	}
	two, ok := m.Get(second)
	if !ok {
		t.Fatalf("Get(%s) ok=false", second)
	}
	if one == two {
		t.Error("the two session/new calls shared one runtime, want one each")
	}
}
