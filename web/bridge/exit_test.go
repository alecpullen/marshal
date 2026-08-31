package bridge

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
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
func (t *scriptedTransport) Detach() error              { return nil }

// testFleetWithGate builds a fleet whose agent child scripts the given
// gate result for session/verify.
func testFleetWithGate(t *testing.T, gate gateResult) *Fleet {
	t.Helper()
	f := newTestFleetWithLimit(t, 4)
	if f.git == nil {
		t.Skip("git not installed")
	}
	tr := &scriptedTransport{gate: gate}
	f.newRuntime = func(a Agent) (*Child, error) { return &Child{Transport: tr}, nil }
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

// stubGitForForge creates a gitExecFunc that makes git operations work
// against a local bare repo fixture, while the repo URL stays as the
// github.com URL for parseOwnerRepo. The fixture is created once and
// reused.
func stubGitForForge(t *testing.T, f *Fleet) string {
	t.Helper()
	bare := newBareRepoFixture(t)

	f.git.exec = func(dir string, env []string, args ...string) ([]byte, error) {
		// args comes pre-prefixed with hardenedGitArgs: -c core.hooksPath=/dev/null -c protocol.ext.allow=never <real args...>
		// Skip the first 4 hardened args.
		realArgs := args[4:]

		switch realArgs[0] {
		case "clone":
			if len(realArgs) >= 4 && realArgs[1] == "--mirror" {
				// clone --mirror <url> <dir> → create a bare repo at <dir>
				dest := realArgs[3]
				if out, err := exec.Command("git", "clone", "--bare", bare, dest).CombinedOutput(); err != nil {
					return out, fmt.Errorf("stub clone --mirror: %w", err)
				}
				return nil, nil
			}
			// clone <mirror> <dir> → create a working repo
			src := realArgs[1]
			dest := realArgs[2]
			if out, err := exec.Command("git", "clone", src, dest).CombinedOutput(); err != nil {
				return out, fmt.Errorf("stub clone: %w", err)
			}
			return nil, nil
		case "symbolic-ref":
			// symbolic-ref --short HEAD → return main
			return []byte("main\n"), nil
		case "remote":
			// remote set-url origin <url> → no-op
			return nil, nil
		case "checkout":
			// checkout <ref> → no-op (the clone already has the right ref)
			return nil, nil
		case "fetch":
			// fetch --prune → no-op
			return nil, nil
		case "push":
			// push origin HEAD:refs/heads/<branch> → success
			return []byte(""), nil
		default:
			return nil, fmt.Errorf("stubGitForForge: unhandled git command: %v", realArgs)
		}
	}
	return bare
}

// testFleetWithForge builds a fleet whose agent child scripts a passing
// gate, and whose repo has a forge pointing at an httptest server that
// creates PR #7.
func testFleetWithForge(t *testing.T) (*Fleet, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"number":7,"html_url":"https://github.com/you/r/pull/7"}`))
	}))
	f := testFleetWithGate(t, gateResult{OK: true})
	return f, srv
}

// testFleetWithFailingForge returns a fleet whose forge returns 500.
func testFleetWithFailingForge(t *testing.T) (*Fleet, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	f := testFleetWithGate(t, gateResult{OK: true})
	return f, srv
}

// testFleetWithSSHRepo returns a fleet with a passing gate.
func testFleetWithSSHRepo(t *testing.T) *Fleet {
	t.Helper()
	return testFleetWithGate(t, gateResult{OK: true})
}

// spawnGitAgentWithPAT registers a repo with a forge and a PAT credential,
// stubs git to work offline, then spawns a git agent against it.
func spawnGitAgentWithPAT(t *testing.T, f *Fleet, srvURL string) string {
	t.Helper()
	stubGitForForge(t, f)

	repo := Repo{
		ID:      "r1",
		URL:     "https://github.com/you/r.git",
		Branch:  "main",
		CredRef: "pat1",
		OwnerID: DefaultOwnerID,
		Forge:   "github",
		APIBase: srvURL,
	}
	if err := f.ws.PutRepo(repo); err != nil {
		t.Fatal(err)
	}
	f.creds = NewCredentialStore([]Credential{{
		ID:      "pat1",
		Kind:    "pat",
		OwnerID: DefaultOwnerID,
		EnvVar:  "TEST_PAT",
	}})
	t.Setenv("TEST_PAT", "test-token")

	id, err := f.Spawn(context.Background(), "", SpawnOptions{RepoID: "r1", Prompt: "x"})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	return id
}

// registerPATRepo registers a repo with a forge and PAT credential
// without spawning an agent.
func registerPATRepo(t *testing.T, f *Fleet, repoID string) {
	t.Helper()
	repo := Repo{
		ID:      repoID,
		URL:     "https://github.com/you/r.git",
		Branch:  "main",
		CredRef: "pat1",
		OwnerID: DefaultOwnerID,
		Forge:   "github",
		APIBase: "",
	}
	if err := f.ws.PutRepo(repo); err != nil {
		t.Fatal(err)
	}
	f.creds = NewCredentialStore([]Credential{{
		ID:      "pat1",
		Kind:    "pat",
		OwnerID: DefaultOwnerID,
		EnvVar:  "TEST_PAT",
	}})
	t.Setenv("TEST_PAT", "test-token")
}

func TestExitCreatesARichPRWhenPossible(t *testing.T) {
	f, srv := testFleetWithForge(t)
	defer srv.Close()
	id := spawnGitAgentWithPAT(t, f, srv.URL)

	res, err := f.Exit(context.Background(), id, ExitOptions{CommitMessage: "work"})
	if err != nil {
		t.Fatalf("Exit: %v", err)
	}
	if !strings.Contains(res.PRUrl, "/pull/7") {
		t.Fatalf("PRUrl = %q, want the API-created PR", res.PRUrl)
	}
}

func TestExitFallsBackToExtractionForSSHRepos(t *testing.T) {
	f := testFleetWithSSHRepo(t)
	id := spawnGitAgent(t, f)

	res, err := f.Exit(context.Background(), id, ExitOptions{CommitMessage: "work"})
	if err != nil {
		t.Fatalf("Exit: %v", err)
	}
	// An ssh credential cannot call an API. This must degrade, not fail.
	if res.Branch == "" {
		t.Fatal("the push did not happen for an ssh repo")
	}
}

func TestExitKeepsThePushWhenTheAPIFails(t *testing.T) {
	f, srv := testFleetWithFailingForge(t)
	defer srv.Close()
	id := spawnGitAgentWithPAT(t, f, srv.URL)

	res, err := f.Exit(context.Background(), id, ExitOptions{CommitMessage: "work"})
	if err != nil {
		t.Fatalf("Exit returned an error, losing a successful push: %v", err)
	}
	a, _ := f.ws.Agent(id)
	if a.PushedAt.IsZero() {
		t.Fatal("the push was rolled back because the API call failed")
	}
	if res.Branch == "" {
		t.Fatal("the branch was not reported after an API failure")
	}
}

func TestPRBodyLinksTheIssueOnlyWhenIssueOriginated(t *testing.T) {
	withIssue := prBody(Agent{IssueNumber: 42}, &gateResult{OK: true})
	if !strings.Contains(withIssue, "Closes #42") {
		t.Errorf("issue link missing: %s", withIssue)
	}
	without := prBody(Agent{}, &gateResult{OK: true})
	if strings.Contains(without, "Closes #") {
		t.Errorf("a non-issue agent's PR claims to close something: %s", without)
	}
}

func TestPRBodyStatesAGateOverride(t *testing.T) {
	body := prBody(Agent{GateOverride: &GateOverride{
		Reason: "known-flaky suite", FailedCommand: "go test ./...",
	}}, &gateResult{OK: false, FailedCommand: "go test ./..."})

	if !strings.Contains(body, "known-flaky suite") {
		t.Fatal("the override reason is not in the PR body; a reviewer would not see it")
	}
}
