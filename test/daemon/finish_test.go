package daemon_test

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"marshal/internal/fleetclient"
	"marshal/web/bridge"
	"marshal/web/bridge/contract"
)

// The one change both finish tests give their agent. The name is the test's
// own, so nothing else in the fixture writes it.
const (
	finishChangePath = "change.txt"
	finishChangeBody = "the agent's change\n"
)

// finishFixtureTurns is the fixture provider's script for one change: a write
// into the agent's working tree, then the reply that ends the turn.
func finishFixtureTurns(t *testing.T) []scriptedTurn {
	t.Helper()
	return []scriptedTurn{
		{ToolName: "file.write", ToolArgs: mustJSON(t, map[string]string{
			"path": finishChangePath, "content": finishChangeBody,
		})},
		{Text: "the change is in"},
	}
}

// TestTheFinishFlowOverridesAFailedGate is W7 against a real bridge: a git
// agent spawned from a bare repository gets one change, exits, and is stopped
// by the gate `go test ./...` fails on; the same exit with an override pushes
// the branch, and the agent's row records why.
func TestTheFinishFlowOverridesAFailedGate(t *testing.T) {
	const (
		commitMessage  = "add the change"
		overrideReason = "the seed's failing test predates this change"
	)

	// 1. A bare repository whose main holds a Go module with one failing test,
	// registered with the harness's workspace.
	bare := newBareGoRepo(t)

	fixture := newProviderFixture(t, finishFixtureTurns(t))
	fixture.useProvider(t)
	h := newDaemonHarness(t)
	ctx := callCtx(t)

	if err := h.ws.PutRepo(bridge.Repo{ID: "w7-repo", URL: bare, OwnerID: bridge.DefaultOwnerID}); err != nil {
		t.Fatalf("register the repo: %v", err)
	}

	// 2. A git agent spawns from the registered repository: the bridge clones
	// it into a checkout of its own, and the agent runs on a child of its own.
	agentID, err := h.fleet.Spawn(ctx, "", bridge.SpawnOptions{Name: "w7-agent", RepoID: "w7-repo"})
	if err != nil {
		t.Fatalf("spawn a git agent from %s: %v", bare, err)
	}
	a, ok := h.ws.Agent(agentID)
	if !ok || a.SessionID == "" {
		t.Fatalf("agent %s carries no session id", agentID)
	}
	if a.SourceKind != "git" || a.Project == h.root {
		t.Fatalf("agent %s is %+v, want a git-sourced agent in a checkout of its own", agentID, a)
	}

	// 3. One change through its session, so there is work to push.
	waitPrompt(t, promptAgent(t, h, ctx, a.SessionID, "make the change"))
	if got, err := os.ReadFile(filepath.Join(a.Project, finishChangePath)); err != nil || string(got) != finishChangeBody {
		t.Fatalf("the agent's checkout holds %q (%v), want %q", got, err, finishChangeBody)
	}

	client := fleetclient.New(fleetclient.Socket(h.socket))
	client.Start(ctx)
	defer client.Close()

	// 4. Exit with a message: the commit lands agent-side, the gate fails, and
	// nothing reaches the remote.
	blocked, err := client.Exit(ctx, agentID, contract.ExitRequest{CommitMessage: commitMessage})
	if err != nil {
		t.Fatalf("Exit: %v", err)
	}
	if blocked.Destination != "push" {
		t.Fatalf("the exit's destination is %q, want \"push\"", blocked.Destination)
	}
	if !blocked.Blocked {
		t.Fatalf("the exit is %+v, want it blocked by the failed gate", blocked)
	}
	if blocked.Verify == nil || blocked.Verify.OK || blocked.Verify.Skipped {
		t.Fatalf("the gate is %+v, want a failure rather than a pass or a skip", blocked.Verify)
	}
	if blocked.Verify.FailedCommand != "go test ./..." {
		t.Fatalf("the gate failed %q, want \"go test ./...\"", blocked.Verify.FailedCommand)
	}
	if branches := bareBranches(t, bare); len(branches) != 1 || branches[0] != "main" {
		t.Fatalf("the bare repository holds %v after a blocked exit, want only main", branches)
	}

	// 5. The same exit with a reason in place of the gate's answer: the branch
	// reaches the bare repository with the commit.
	shipped, err := client.Exit(ctx, agentID, contract.ExitRequest{
		CommitMessage: commitMessage,
		Override:      &contract.GateOverride{Reason: overrideReason},
	})
	if err != nil {
		t.Fatalf("Exit with an override: %v", err)
	}
	if shipped.Destination != "push" {
		t.Fatalf("the override's destination is %q, want \"push\"", shipped.Destination)
	}
	if shipped.Blocked {
		t.Fatalf("the overridden exit is %+v, want it shipped", shipped)
	}
	if shipped.Branch == "" || shipped.Branch == "main" {
		t.Fatalf("the exit pushed %q, want a branch of the agent's own", shipped.Branch)
	}
	branches := bareBranches(t, bare)
	if len(branches) != 2 || !hasString(branches, "main") || !hasString(branches, shipped.Branch) {
		t.Fatalf("the bare repository holds %v, want main and %q", branches, shipped.Branch)
	}
	if got := gitIn(t, "--git-dir", bare, "show", shipped.Branch+":"+finishChangePath); got != strings.TrimSpace(finishChangeBody) {
		t.Fatalf("the pushed branch holds %q, want the agent's change", got)
	}
	if got := gitIn(t, "--git-dir", bare, "log", "-1", "--format=%s", shipped.Branch); got != commitMessage {
		t.Fatalf("the pushed branch's commit is %q, want %q", got, commitMessage)
	}

	// 6. The agent's row records the override and what it overrode. The agents
	// listing is the authority on a row, and an exit moves no inbox group, so
	// the client above is not told to reread it: a client that attaches now
	// reads the row as the bridge holds it.
	reader := fleetclient.New(fleetclient.Socket(h.socket))
	reader.Start(ctx)
	defer reader.Close()
	waitFor(t, func() error {
		for _, row := range reader.State().Agents {
			if row.ID != agentID {
				continue
			}
			if row.GateOverride == nil {
				return errors.New("the agent's row records no gate override")
			}
			if row.GateOverride.Reason != overrideReason {
				return fmt.Errorf("the row's override reason is %q, want %q", row.GateOverride.Reason, overrideReason)
			}
			if row.GateOverride.FailedCommand != "go test ./..." {
				return fmt.Errorf("the row's override names %q, want \"go test ./...\"", row.GateOverride.FailedCommand)
			}
			return nil
		}
		return fmt.Errorf("agent %s is not among the client's rows", agentID)
	})
}

// TestALocalAgentsMergeLandsOnItsTarget is the other destination: an isolated
// local agent's work merges into the project's target branch through the
// client.
func TestALocalAgentsMergeLandsOnItsTarget(t *testing.T) {
	const commitMessage = "merge the change"

	fixture := newProviderFixture(t, finishFixtureTurns(t))
	fixture.useProvider(t)
	h := newDaemonHarness(t)
	ctx := callCtx(t)

	// The merge commit is git's own, not the session's commit path (which
	// supplies an identity when the repository has none), so the project needs
	// one.
	gitIn(t, "-C", h.root, "config", "user.email", "test@example.com")
	gitIn(t, "-C", h.root, "config", "user.name", "Test")

	client := fleetclient.New(fleetclient.Socket(h.socket))
	client.Start(ctx)
	defer client.Close()

	// 1. An isolated local agent: a worktree of its own, so there is a branch
	// to merge back.
	agentID, err := h.fleet.Spawn(ctx, h.root, bridge.SpawnOptions{Name: "merge-agent", Isolated: true})
	if err != nil {
		t.Fatalf("spawn an isolated local agent: %v", err)
	}
	a, ok := h.ws.Agent(agentID)
	if !ok || a.SessionID == "" {
		t.Fatalf("agent %s carries no session id", agentID)
	}
	if !a.Isolated || a.Branch == "" || a.TargetBranch == "" {
		t.Fatalf("agent %s is %+v, want an isolated branch with a merge target", agentID, a)
	}
	branch, target := a.Branch, a.TargetBranch

	// 2. One change through its session.
	waitPrompt(t, promptAgent(t, h, ctx, a.SessionID, "make the change"))

	// 3. The merge through the client commits the worktree and lands it on the
	// project's target branch.
	res, err := client.Merge(ctx, agentID, contract.MergeRequest{CommitMessage: commitMessage})
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if !res.Merged {
		t.Fatalf("the merge was refused: %+v", res)
	}
	if res.Branch != branch || res.Target != target {
		t.Fatalf("the merge is %+v, want branch %q into target %q", res, branch, target)
	}
	if got := gitIn(t, "-C", h.root, "show", target+":"+finishChangePath); got != strings.TrimSpace(finishChangeBody) {
		t.Fatalf("the target branch holds %q, want the agent's change", got)
	}
}

// newBareGoRepo makes a bare repository whose main holds a Go module with one
// failing test, and returns the bare path: a local "remote", so every
// operation the test drives stays offline.
func newBareGoRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git is not installed: %v", err)
	}
	dir := t.TempDir()
	bare := filepath.Join(dir, "origin.git")
	seed := filepath.Join(dir, "seed")

	gitIn(t, "init", "--bare", "--initial-branch=main", bare)
	gitIn(t, "init", "--initial-branch=main", seed)
	gitIn(t, "-C", seed, "config", "user.email", "test@example.com")
	gitIn(t, "-C", seed, "config", "user.name", "Test")

	// seed.go is what makes `go build ./...` pass, so the gate that stops the
	// first exit is `go test ./...` and not a build failure.
	for name, body := range map[string]string{
		"go.mod":  "module example.com/seed\n\ngo 1.21\n",
		"seed.go": "package seed\n\n// Value is the package's one export.\nconst Value = 1\n",
		"seed_test.go": "package seed\n\nimport \"testing\"\n\n" +
			"// TestTheSuiteFails is the failing test the gate names.\n" +
			"func TestTheSuiteFails(t *testing.T) {\n\tt.Fatal(\"the seed's test fails on purpose\")\n}\n",
	} {
		if err := os.WriteFile(filepath.Join(seed, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	gitIn(t, "-C", seed, "add", "-A")
	gitIn(t, "-C", seed, "commit", "-m", "seed: a module with a failing test")
	gitIn(t, "-C", seed, "remote", "add", "origin", bare)
	gitIn(t, "-C", seed, "push", "origin", "main")
	return bare
}

// bareBranches lists the branches a bare repository holds.
func bareBranches(t *testing.T, bare string) []string {
	t.Helper()
	out := gitIn(t, "--git-dir", bare, "for-each-ref", "--format=%(refname:short)", "refs/heads")
	if out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

// gitIn runs git with args, in this process's working directory, and answers
// its trimmed output. A failing command fails the test: every git step here is
// the test's own arrangement.
func gitIn(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// hasString reports whether list holds want.
func hasString(list []string, want string) bool {
	for _, got := range list {
		if got == want {
			return true
		}
	}
	return false
}
