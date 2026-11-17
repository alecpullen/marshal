package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"marshal/internal/daemon"
)

// fakeSpawnClient answers `daemon spawn` without a socket. got, when set,
// records the last request the command built, so a test asserts what the daemon
// would have been asked rather than what a rendering looked like.
type fakeSpawnClient struct {
	result daemon.SpawnResult
	err    error
	got    *daemon.SpawnRequest
	calls  *int
}

func (c fakeSpawnClient) Spawn(_ context.Context, req daemon.SpawnRequest) (daemon.SpawnResult, error) {
	if c.got != nil {
		*c.got = req
	}
	if c.calls != nil {
		*c.calls++
	}
	return c.result, c.err
}

// fakeSpawnSocket replaces the spawn seam for one test.
func fakeSpawnSocket(t *testing.T, c daemonSpawnClient) {
	t.Helper()
	old := daemonSpawnFor
	t.Cleanup(func() { daemonSpawnFor = old })
	daemonSpawnFor = func(string) daemonSpawnClient { return c }
}

// spawn runs the subcommand and returns its combined output.
func spawn(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	err := runDaemonSpawn(context.Background(), args, &out, &errOut)
	return out.String(), err
}

func TestDaemonSpawnSendsThePromptAndTheChosenStart(t *testing.T) {
	var got daemon.SpawnRequest
	fakeSpawnSocket(t, fakeSpawnClient{result: daemon.SpawnResult{AgentID: "a1"}, got: &got})

	project := t.TempDir()
	out, err := spawn(t, "--project", project, "--prompt", "fix the gate",
		"--mode", "act", "--auto", "--isolated", "--name", "gate fixer",
		"--branch", "fix/gate", "--base-ref", "origin/main", "--profile", "fast")
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if out != "spawned a1\n" {
		t.Fatalf("output = %q, want the agent id", out)
	}
	if got.Project != project {
		t.Errorf("project = %q, want %q", got.Project, project)
	}
	if got.Prompt != "fix the gate" || got.Name != "gate fixer" {
		t.Errorf("prompt = %q, name = %q", got.Prompt, got.Name)
	}
	if got.Mode != "act" {
		t.Errorf("mode = %q, want act", got.Mode)
	}
	if got.Auto == nil || !*got.Auto {
		t.Errorf("auto = %v, want true", got.Auto)
	}
	if got.Isolated == nil || !*got.Isolated {
		t.Errorf("isolated = %v, want true", got.Isolated)
	}
	if got.Branch != "fix/gate" || got.BaseRef != "origin/main" {
		t.Errorf("branch = %q, baseRef = %q", got.Branch, got.BaseRef)
	}
	var routing map[string]string
	if err := json.Unmarshal(got.Routing, &routing); err != nil {
		t.Fatalf("routing %q: %v", got.Routing, err)
	}
	if routing["profile"] != "fast" {
		t.Errorf("routing = %v, want the fast profile", routing)
	}
}

// TestDaemonSpawnLeavesUnaskedDefaultsAlone: Auto and Isolated are pointers on
// the wire so an absent flag lets the project decide. Sending an explicit false
// would silently override a project that starts isolated.
func TestDaemonSpawnLeavesUnaskedDefaultsAlone(t *testing.T) {
	var got daemon.SpawnRequest
	fakeSpawnSocket(t, fakeSpawnClient{result: daemon.SpawnResult{AgentID: "a1"}, got: &got})

	if _, err := spawn(t, "--project", t.TempDir()); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if got.Auto != nil {
		t.Errorf("auto = %v, want nil", *got.Auto)
	}
	if got.Isolated != nil {
		t.Errorf("isolated = %v, want nil", *got.Isolated)
	}
	if got.Mode != "" {
		t.Errorf("mode = %q, want empty for the project's default", got.Mode)
	}
	if len(got.Routing) != 0 {
		t.Errorf("routing = %q, want none", got.Routing)
	}
	if got.Prompt != "" {
		t.Errorf("prompt = %q, want none", got.Prompt)
	}
}

func TestDaemonSpawnDefaultsToTheWorkingDirectoryRoot(t *testing.T) {
	var got daemon.SpawnRequest
	fakeSpawnSocket(t, fakeSpawnClient{result: daemon.SpawnResult{AgentID: "a1"}, got: &got})

	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "internal", "agent")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(sub)

	if _, err := spawn(t); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	// The daemon serves projects, and a project is anchored at its git root,
	// so a spawn from a subdirectory lands in the same project.
	if got.Project != root {
		t.Errorf("project = %q, want the repository root %q", got.Project, root)
	}
}

func TestDaemonSpawnJSONIsTheSpawnResult(t *testing.T) {
	fakeSpawnSocket(t, fakeSpawnClient{result: daemon.SpawnResult{AgentID: "a1", Warning: "no routing"}})

	out, err := spawn(t, "--project", t.TempDir(), "--json")
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	var res daemon.SpawnResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("output %q is not one JSON document: %v", out, err)
	}
	if res.AgentID != "a1" || res.Warning != "no routing" {
		t.Errorf("result = %+v", res)
	}
}

// TestDaemonSpawnReportsAWarning: a warning means the agent exists, so the id is
// still printed; the warning says what about its start was not fatal.
func TestDaemonSpawnReportsAWarning(t *testing.T) {
	fakeSpawnSocket(t, fakeSpawnClient{result: daemon.SpawnResult{AgentID: "a1", Warning: "workspace not built"}})

	out, err := spawn(t, "--project", t.TempDir())
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if want := "spawned a1\nwarning: workspace not built\n"; out != want {
		t.Fatalf("output = %q, want %q", out, want)
	}
}

// TestDaemonSpawnRefusesAnUnknownModeWithoutCallingTheDaemon: a typo fails before
// a socket is opened, so nothing is left half-started.
func TestDaemonSpawnRefusesAnUnknownModeWithoutCallingTheDaemon(t *testing.T) {
	calls := 0
	fakeSpawnSocket(t, fakeSpawnClient{calls: &calls})

	_, err := spawn(t, "--project", t.TempDir(), "--mode", "auto")
	if err == nil || !strings.Contains(err.Error(), "act or ask") {
		t.Fatalf("err = %v, want a refused mode", err)
	}
	if calls != 0 {
		t.Fatalf("the daemon was called %d times, want 0", calls)
	}
}

func TestDaemonSpawnRefusesAnExtraArgument(t *testing.T) {
	fakeSpawnSocket(t, fakeSpawnClient{})

	_, err := spawn(t, "--project", t.TempDir(), "extra")
	if err == nil || !strings.Contains(err.Error(), "unexpected argument") {
		t.Fatalf("err = %v, want a refusal naming the argument", err)
	}
}

// TestDaemonSpawnReportsTheDaemonsRefusal: a refusal is the daemon's own
// sentence, not a transport failure.
func TestDaemonSpawnReportsTheDaemonsRefusal(t *testing.T) {
	fakeSpawnSocket(t, fakeSpawnClient{err: errors.New("daemon answered 429 Too Many Requests: Budget reached (daily)")})

	_, err := spawn(t, "--project", t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "Budget reached") {
		t.Fatalf("err = %v, want the daemon's refusal", err)
	}
	if strings.Contains(err.Error(), "not answering") {
		t.Fatalf("err = %v, want a refusal rather than an unreachable daemon", err)
	}
}

// TestDaemonSpawnSaysWhenNothingAnswers: a dial failure is reported as an
// unreachable daemon on its socket, so the reader knows to look at the service
// rather than at their arguments.
func TestDaemonSpawnSaysWhenNothingAnswers(t *testing.T) {
	fakeSpawnSocket(t, fakeSpawnClient{err: &url.Error{Op: "Post", URL: "http://daemon/api/agents", Err: errors.New("dial unix: no such file")}})

	_, err := spawn(t, "--project", t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "not answering") {
		t.Fatalf("err = %v, want a note that the daemon is not answering", err)
	}
}
