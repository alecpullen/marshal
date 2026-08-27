package bridge

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
)

func TestExitDestinationFollowsTheSource(t *testing.T) {
	cases := []struct {
		name string
		a    Agent
		want string
	}{
		{"local", Agent{SourceKind: "local"}, "merge"},
		{"git writable", Agent{SourceKind: "git"}, "push"},
		{"git read-only", Agent{SourceKind: "git", ReadOnly: true}, "patch"},
	}
	for _, c := range cases {
		if got := exitDestination(c.a); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestExitBlocksOnGateFailure(t *testing.T) {
	f := testFleetWithGate(t, gateResult{OK: false, FailedCommand: "go test ./..."})
	id := spawnGitAgent(t, f)

	res, err := f.Exit(context.Background(), id, ExitOptions{CommitMessage: "work"})
	if err != nil {
		t.Fatalf("Exit: %v", err)
	}
	if !res.Blocked {
		t.Fatal("a failing gate did not block the push")
	}
	a, _ := f.ws.Agent(id)
	if !a.PushedAt.IsZero() {
		t.Fatal("the agent was pushed despite a failing gate")
	}
}

func TestExitBlocksOnSkippedGate(t *testing.T) {
	f := testFleetWithGate(t, gateResult{Skipped: true})
	id := spawnGitAgent(t, f)

	res, err := f.Exit(context.Background(), id, ExitOptions{CommitMessage: "work"})
	if err != nil {
		t.Fatalf("Exit: %v", err)
	}
	if !res.Blocked {
		t.Fatal("a skipped gate must block; it has proved nothing")
	}
}

func TestExitOverrideRecordsTheReason(t *testing.T) {
	f := testFleetWithGate(t, gateResult{OK: false, FailedCommand: "go test ./..."})
	id := spawnGitAgent(t, f)

	_, err := f.Exit(context.Background(), id, ExitOptions{
		CommitMessage: "work",
		Override:      &GateOverride{Reason: "known-flaky suite"},
	})
	if err != nil {
		t.Fatalf("Exit with override: %v", err)
	}
	a, _ := f.ws.Agent(id)
	if a.GateOverride == nil {
		t.Fatal("the override was not recorded")
	}
	if a.GateOverride.Reason != "known-flaky suite" {
		t.Fatalf("Reason = %q", a.GateOverride.Reason)
	}
	if a.GateOverride.FailedCommand != "go test ./..." {
		t.Fatal("the override did not capture which command failed")
	}
	if a.GateOverride.At.IsZero() || a.GateOverride.By == "" {
		t.Fatal("the override is missing its timestamp or owner")
	}
}

func TestExitRejectsAnOverrideWithNoReason(t *testing.T) {
	f := testFleetWithGate(t, gateResult{OK: false})
	id := spawnGitAgent(t, f)

	if _, err := f.Exit(context.Background(), id, ExitOptions{
		CommitMessage: "work", Override: &GateOverride{Reason: "  "},
	}); err == nil {
		t.Fatal("an override with a blank reason was accepted; the record would be worthless")
	}
}

// scriptedTransport is a fake agentTransport that answers session/commit
// and session/verify with scripted results, so Exit's gate path can be
// exercised without a real container.
type scriptedTransport struct {
	gate gateResult
}

func (t *scriptedTransport) Open() (io.WriteCloser, io.ReadCloser, io.ReadCloser, error) {
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	go t.serve(stdinR, stdoutW)
	return stdinW, stdoutR, io.NopCloser(strings.NewReader("")), nil
}

func (t *scriptedTransport) serve(r io.Reader, w io.WriteCloser) {
	defer w.Close()
	sc := bufio.NewScanner(r)
	enc := json.NewEncoder(w)
	for sc.Scan() {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			continue
		}
		var result any
		switch req.Method {
		case "session/new":
			result = map[string]any{"sessionId": "s-1"}
		case "session/commit":
			result = map[string]any{"commit": "abc", "clean": false}
		case "session/verify":
			result = t.gate
		default:
			result = map[string]any{}
		}
		_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}
}

func (t *scriptedTransport) Wait() error                { return nil }
func (t *scriptedTransport) Signal(sig os.Signal) error { return nil }
func (t *scriptedTransport) Kill() error                { return nil }

// testFleetWithGate builds a fleet whose agent child scripts the given
// gate result for session/verify.
func testFleetWithGate(t *testing.T, gate gateResult) *Fleet {
	t.Helper()
	f := newTestFleetWithLimit(t, 4)
	if f.git == nil {
		t.Skip("git not installed")
	}
	tr := &scriptedTransport{gate: gate}
	f.newRuntime = func(a Agent) *Child { return &Child{Transport: tr} }
	return f
}

// spawnGitAgent registers a bare repo fixture and spawns a writable
// git-sourced agent against it, returning the agent id.
func spawnGitAgent(t *testing.T, f *Fleet) string {
	t.Helper()
	if err := f.ws.PutRepo(Repo{ID: "r1", URL: newBareRepoFixture(t),
		Branch: "main", OwnerID: DefaultOwnerID}); err != nil {
		t.Fatal(err)
	}
	id, err := f.Spawn(context.Background(), "", SpawnOptions{RepoID: "r1", Prompt: "x"})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	return id
}
