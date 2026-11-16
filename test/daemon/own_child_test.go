package daemon_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"marshal/internal/acpwire"
	"marshal/web/bridge"
)

// ownChildRepoID is the repository the fleet registers for the agent whose
// session runs on a child of its own.
const ownChildRepoID = "own-child-repo"

// TestAnOwnChildsAgentOpensAsAViewer is P5 F5's own child, end to end against a
// real bridge: a git-sourced agent cannot share the project's child (P4 D10
// decision 2), so it runs on a child of its own, that child serves viewers on
// the socket the bridge keeps for it (decision 1), and a client attaches to the
// agent's session through the daemon's agent attach and reads the transcript
// the child wrote.
func TestAnOwnChildsAgentOpensAsAViewer(t *testing.T) {
	const prompt = "say something on your own child"
	const reply = "the reply from the agent's own child"

	fixture := newProviderFixture(t, []scriptedTurn{{Text: reply}})
	fixture.useProvider(t)
	h := newDaemonHarness(t)
	ctx := callCtx(t)

	// 1. A git-sourced spawn: its tree is a checkout of a local bare
	// repository, not the project, so it cannot run on the project's child.
	bare := bareRepoForOwnChild(t)
	if err := h.ws.PutRepo(bridge.Repo{ID: ownChildRepoID, URL: bare, OwnerID: bridge.DefaultOwnerID}); err != nil {
		t.Fatalf("register the repository: %v", err)
	}
	agentID, err := h.fleet.Spawn(ctx, h.root, bridge.SpawnOptions{
		Name: "own child agent", RepoID: ownChildRepoID,
	})
	if err != nil {
		t.Fatalf("spawn a git-sourced agent: %v", err)
	}
	agent, ok := h.ws.Agent(agentID)
	if !ok || agent.SessionID == "" {
		t.Fatalf("agent %s carries no session id", agentID)
	}
	if agent.SourceKind != "git" {
		t.Fatalf("the agent's source kind is %q, want a git checkout", agent.SourceKind)
	}
	if agent.Project == h.root {
		t.Fatalf("the agent works on the project itself (%q), so it would share the project's child", agent.Project)
	}
	agentSID := agent.SessionID

	// 2. Nothing was attached to the project, so the daemon lists no project
	// child: the agent's session is not on one.
	info, err := h.client.Info(ctx)
	if err != nil {
		t.Fatalf("daemon info: %v", err)
	}
	if len(info.Children) != 0 {
		t.Fatalf("the daemon lists %d project children; an agent on a child of its own has none", len(info.Children))
	}

	// 3. That child serves its viewers on a socket of its own, under the state
	// directory's v/ (decision 1).
	waitFor(t, func() error {
		sockets, err := filepath.Glob(filepath.Join(h.stateDir, "v", "*.sock"))
		if err != nil {
			return err
		}
		if len(sockets) != 1 {
			return fmt.Errorf("the state directory holds %d viewer sockets, want the own child's one", len(sockets))
		}
		return nil
	})

	// 4. One turn through the child, on the fleet's own registry: the fixture
	// serves the scripted reply, so the transcript holds it.
	waitPrompt(t, promptAgent(t, h, ctx, agentSID, prompt))
	if got := fixture.Completions(); got < 1 {
		t.Fatalf("the agent's turn did not reach the model (%d completions)", got)
	}

	// 5. The daemon's agent attach opens it: it dials the own child's viewer
	// socket, and the transcript is readable through the relay.
	a := attachAgent(t, h, agentID)
	defer a.close()
	if got := a.conn.AgentID; got != agentID {
		t.Fatalf("the attach named agent %q, want %q", got, agentID)
	}
	if got := a.conn.SessionID; got != agentSID {
		t.Fatalf("the attach named session %q, want the agent's %q", got, agentSID)
	}
	assertTranscript(t, a, prompt, reply)

	// 6. Closing the viewer leaves the agent listed and its session open: a
	// second attach lands on the same session and reads the same transcript.
	a.close()
	if _, ok := agentStatus(h, agentID); !ok {
		t.Fatalf("the fleet no longer lists agent %s after its viewer left", agentID)
	}
	again := attachAgent(t, h, agentID)
	defer again.close()
	if got := again.conn.SessionID; got != agentSID {
		t.Fatalf("the second attach named session %q, want the agent's still-open %q", got, agentSID)
	}
	assertTranscript(t, again, prompt, reply)
}

// assertTranscript fails unless the attachment's session carries both the
// prompt the agent was given and the reply the fixture served.
func assertTranscript(t *testing.T, a *attachment, prompt, reply string) {
	t.Helper()
	page, err := a.session.Messages(callCtx(t), acpwire.MessageQuery{})
	if err != nil {
		t.Fatalf("messages through the attachment: %v", err)
	}
	for _, want := range []string{prompt, reply} {
		if !carries(page, want) {
			t.Fatalf("the transcript the attachment read does not carry %q: %+v", want, page.Messages)
		}
	}
}

// bareRepoForOwnChild creates a bare repository with one commit and returns its
// path: the local "remote" a git-sourced spawn clones from. Everything is
// offline — no network, no git host — and it follows web/bridge's own
// newBareRepoFixture.
func bareRepoForOwnChild(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	root := t.TempDir()
	bare := filepath.Join(root, "origin.git")
	seed := filepath.Join(root, "seed")

	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}

	git("", "init", "--bare", "--initial-branch=main", bare)
	git("", "init", "--initial-branch=main", seed)
	git(seed, "config", "user.email", "test@example.com")
	git(seed, "config", "user.name", "Test")

	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatalf("write the seed's README: %v", err)
	}
	git(seed, "add", "README.md")
	git(seed, "commit", "-m", "seed")
	git(seed, "remote", "add", "origin", bare)
	git(seed, "push", "origin", "main")
	return bare
}
