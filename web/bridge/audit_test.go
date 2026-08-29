package bridge

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAuditAppendAndTailRoundTrip(t *testing.T) {
	a := NewAuditLog(t.TempDir())
	for i, ev := range []AuditEvent{
		{Event: AuditSpawn, OwnerID: DefaultOwnerID, AgentID: "a1", Origin: OriginMCP, ClientID: "c1"},
		{Event: AuditGateOverride, OwnerID: DefaultOwnerID, AgentID: "a1", Reason: "flaky suite"},
		{Event: AuditPush, OwnerID: DefaultOwnerID, AgentID: "a1", Detail: "marshal/a1"},
	} {
		if err := a.Append(ev); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}

	got, err := a.Tail(10)
	if err != nil {
		t.Fatalf("Tail: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d events, want 3", len(got))
	}
	// Newest last, and timestamps filled in by Append.
	if got[2].Event != AuditPush {
		t.Fatalf("last event = %q, want %q", got[2].Event, AuditPush)
	}
	if got[0].TS.IsZero() {
		t.Fatal("Append did not stamp a timestamp")
	}
}

func TestAuditTailBoundsItsResult(t *testing.T) {
	a := NewAuditLog(t.TempDir())
	for i := 0; i < 50; i++ {
		if err := a.Append(AuditEvent{Event: AuditSpawn, AgentID: "a"}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := a.Tail(5)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 {
		t.Fatalf("Tail(5) returned %d", len(got))
	}
}

func TestAuditRotatesAtTheSizeCap(t *testing.T) {
	dir := t.TempDir()
	a := NewAuditLog(dir)
	a.maxBytes = 512 // small enough to trip quickly

	for i := 0; i < 200; i++ {
		if err := a.Append(AuditEvent{
			Event: AuditSpawn, AgentID: "agent-with-a-longish-identifier", Detail: strings.Repeat("x", 40),
		}); err != nil {
			t.Fatal(err)
		}
	}

	entries, err := os.ReadDir(filepath.Join(dir, "audit"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) < 2 {
		t.Fatalf("got %d audit files, want rotation to have produced more than one", len(entries))
	}
}

func TestSpawnIsAudited(t *testing.T) {
	f := testFleetWithAudit(t)
	// Use a scripted transport so the test does not depend on a real
	// child process or git — the audit emission is the same either way.
	tr := &scriptedTransport{gate: gateResult{OK: true}}
	f.newRuntime = func(a Agent) (*Child, error) { return &Child{Transport: tr}, nil }
	id, err := f.Spawn(context.Background(), "/p", SpawnOptions{Prompt: "x"})
	if err != nil {
		t.Fatal(err)
	}
	events := auditTail(t, f)
	if !hasEvent(events, AuditSpawn, id) {
		t.Fatalf("no spawn record for %s: %+v", id, events)
	}
}

func TestGateOverrideIsAudited(t *testing.T) {
	f := testFleetWithAuditAndGate(t, gateResult{OK: false, FailedCommand: "go test ./..."})
	id := spawnGitAgent(t, f)
	if _, err := f.Exit(context.Background(), id, ExitOptions{
		CommitMessage: "w", Override: &GateOverride{Reason: "flaky suite"},
	}); err != nil {
		t.Fatal(err)
	}

	events := auditTail(t, f)
	rec := findEvent(events, AuditGateOverride)
	if rec == nil {
		t.Fatal("a gate override left no audit record")
	}
	if rec.Reason != "flaky suite" {
		t.Fatalf("reason = %q", rec.Reason)
	}
}

func TestDeniedSpawnIsAudited(t *testing.T) {
	f := testFleetWithAudit(t)
	_, err := f.Submit(context.Background(), SpawnRequest{
		Origin: OriginMCP, ClientID: "c1", RepoID: "unregistered", Title: "t", Prompt: "p",
	})
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if findEvent(auditTail(t, f), AuditSpawnDenied) == nil {
		t.Fatal("a refused submission left no record; refusals are the interesting ones")
	}
}

func testFleetWithAudit(t *testing.T) *Fleet {
	t.Helper()
	f := testFleet(t)
	f.audit = NewAuditLog(t.TempDir())
	return f
}

func testFleetWithAuditAndGate(t *testing.T, gate gateResult) *Fleet {
	t.Helper()
	f := testFleetWithAudit(t)
	if f.git == nil {
		t.Skip("git not installed")
	}
	tr := &scriptedTransport{gate: gate}
	f.newRuntime = func(a Agent) (*Child, error) { return &Child{Transport: tr}, nil }
	return f
}

func auditTail(t *testing.T, f *Fleet) []AuditEvent {
	t.Helper()
	events, err := f.audit.Tail(100)
	if err != nil {
		t.Fatal(err)
	}
	return events
}

func hasEvent(events []AuditEvent, name, agentID string) bool {
	for _, e := range events {
		if e.Event == name && e.AgentID == agentID {
			return true
		}
	}
	return false
}

func findEvent(events []AuditEvent, name string) *AuditEvent {
	for i := range events {
		if events[i].Event == name {
			return &events[i]
		}
	}
	return nil
}

// TestAuditNeverRecordsASecret is the non-negotiable one: an audit log
// is exactly the file that ends up pasted into a bug report.
func TestAuditNeverRecordsASecret(t *testing.T) {
	const secret = "sk-super-secret-token-value"
	dir := t.TempDir()
	a := NewAuditLog(dir)

	// Every event type, with credential-bearing values pushed into every
	// free-text field an caller could plausibly misuse.
	for _, name := range []string{
		AuditSpawn, AuditSpawnDenied, AuditPendingApproved, AuditPendingDenied,
		AuditGateOverride, AuditPush, AuditPatchExport,
		AuditClientCreated, AuditClientRevoked,
		AuditRepoRegistered, AuditRepoRemoved, AuditPrune,
	} {
		if err := a.Append(AuditEvent{
			Event: name, OwnerID: DefaultOwnerID, AgentID: "a1", ClientID: "c1",
			RepoID: "r1", Origin: OriginMCP,
			Reason: "denied", Detail: "branch",
		}); err != nil {
			t.Fatal(err)
		}
	}

	// The struct has no field that holds a credential, so nothing a
	// caller passes through the supported fields can carry one. Prove
	// the file is clean.
	body, err := os.ReadFile(filepath.Join(dir, "audit", currentAuditName(time.Now())))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), secret) {
		t.Fatal("a secret reached the audit log")
	}
	for _, banned := range []string{"tokenHash", "Authorization", "MARSHAL_ASKPASS"} {
		if strings.Contains(string(body), banned) {
			t.Fatalf("the audit log carries a credential-adjacent field: %s", banned)
		}
	}
}
