package bridge

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testFleetWithClient(t *testing.T, c MCPClient) *Fleet {
	t.Helper()
	f := testFleet(t)
	c.OwnerID = DefaultOwnerID
	if c.ID == "" {
		c.ID = "c1"
	}
	if err := f.ws.PutClient(c); err != nil {
		t.Fatal(err)
	}
	return f
}

func registerRepo(t *testing.T, f *Fleet, id string) {
	t.Helper()
	if err := f.ws.PutRepo(Repo{ID: id, URL: "file:///tmp/" + id, OwnerID: DefaultOwnerID}); err != nil {
		t.Fatal(err)
	}
}

// registerGitRepo registers a repo backed by a real bare git repo so a
// spawn against it can actually succeed. Skips when git is unavailable.
func registerGitRepo(t *testing.T, f *Fleet, id string) {
	t.Helper()
	if f.git == nil {
		t.Skip("git not installed")
	}
	if err := f.ws.PutRepo(Repo{ID: id, URL: newBareRepoFixture(t), Branch: "main", OwnerID: DefaultOwnerID}); err != nil {
		t.Fatal(err)
	}
}

func TestSubmitFromMCPRequiresARegisteredRepo(t *testing.T) {
	f := testFleet(t)
	_, err := f.Submit(context.Background(), SpawnRequest{
		Origin: OriginMCP, ClientID: "c1", RepoID: "not-registered", Title: "x", Prompt: "y",
	})
	if !errors.Is(err, ErrUnregisteredRepo) {
		t.Fatalf("got %v, want ErrUnregisteredRepo", err)
	}
}

func TestSubmitFromNonAutonomousClientLandsPending(t *testing.T) {
	f := testFleetWithClient(t, MCPClient{ID: "c1", OwnerID: DefaultOwnerID})
	registerRepo(t, f, "r1")

	res, err := f.Submit(context.Background(), SpawnRequest{
		Origin: OriginMCP, ClientID: "c1", RepoID: "r1", Title: "add a flag", Prompt: "do it",
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if res.Status != "pending" || res.PendingID == "" {
		t.Fatalf("got %+v, want a pending submission", res)
	}
	if len(f.Snapshot()) != 0 {
		t.Fatal("an unconfirmed submission started an agent")
	}
}

func TestSubmitFromAutonomousClientStartsImmediately(t *testing.T) {
	f := testFleetWithClient(t, MCPClient{ID: "c1", OwnerID: DefaultOwnerID, Autonomous: true})
	registerGitRepo(t, f, "r1")

	res, err := f.Submit(context.Background(), SpawnRequest{
		Origin: OriginMCP, ClientID: "c1", RepoID: "r1", Title: "x", Prompt: "y",
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if res.Status != "running" || res.AgentID == "" {
		t.Fatalf("got %+v, want a started agent", res)
	}
}

func TestSubmitRespectsAllowedRepos(t *testing.T) {
	f := testFleetWithClient(t, MCPClient{
		ID: "c1", OwnerID: DefaultOwnerID, Autonomous: true, AllowedRepos: []string{"other"},
	})
	registerRepo(t, f, "r1")

	_, err := f.Submit(context.Background(), SpawnRequest{
		Origin: OriginMCP, ClientID: "c1", RepoID: "r1", Title: "x", Prompt: "y",
	})
	if !errors.Is(err, ErrRepoNotAllowed) {
		t.Fatalf("got %v, want ErrRepoNotAllowed", err)
	}
}

func TestSubmitEnforcesTheDailyCap(t *testing.T) {
	f := testFleetWithClient(t, MCPClient{
		ID: "c1", OwnerID: DefaultOwnerID, Autonomous: true, MaxPerDay: 1,
	})
	registerGitRepo(t, f, "r1")
	req := SpawnRequest{Origin: OriginMCP, ClientID: "c1", RepoID: "r1", Title: "x", Prompt: "y"}

	if _, err := f.Submit(context.Background(), req); err != nil {
		t.Fatalf("first submit: %v", err)
	}
	if _, err := f.Submit(context.Background(), req); !errors.Is(err, ErrCapExceeded) {
		t.Fatalf("second submit = %v, want ErrCapExceeded", err)
	}
}

func TestSubmitRequiresATitle(t *testing.T) {
	f := testFleetWithClient(t, MCPClient{ID: "c1", OwnerID: DefaultOwnerID})
	registerRepo(t, f, "r1")
	if _, err := f.Submit(context.Background(), SpawnRequest{
		Origin: OriginMCP, ClientID: "c1", RepoID: "r1", Prompt: "y",
	}); err == nil {
		t.Fatal("accepted a submission with no title; the operator would confirm a blank row")
	}
}

func TestApproveWritesThePlanToABridgeChosenPath(t *testing.T) {
	f := testFleetWithClient(t, MCPClient{ID: "c1", OwnerID: DefaultOwnerID})
	registerGitRepo(t, f, "r1") // skips if no git
	res, err := f.Submit(context.Background(), SpawnRequest{
		Origin: OriginMCP, ClientID: "c1", RepoID: "r1", Title: "t",
		Plan: "## Global Constraints\n\n- none\n\n### Task 1: Do it\n\n- [ ] step\n",
	})
	if err != nil {
		t.Fatal(err)
	}

	agentID, err := f.Approve(context.Background(), res.PendingID)
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	a, _ := f.ws.Agent(agentID)
	planPath := planPathFor(a.Project, res.PendingID)
	body, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatalf("plan was not written: %v", err)
	}
	if !strings.Contains(string(body), "### Task 1: Do it") {
		t.Fatal("plan content was not preserved verbatim")
	}
}

// TestPlanPathIsNeverClientControlled is the path-traversal regression.
func TestPlanPathIsNeverClientControlled(t *testing.T) {
	// The intake surface exposes no path field at all, so the only way a
	// client could steer the write is through the pending id. Prove that
	// a hostile id cannot escape the workspace.
	for _, hostile := range []string{"../../etc/cron.d/x", "..", "/etc/passwd", "a/../../b"} {
		got := planPathFor("/srv/work/agent1", hostile)
		if !strings.HasPrefix(filepath.Clean(got), "/srv/work/agent1/") {
			t.Fatalf("planPathFor(%q) escaped the workspace: %q", hostile, got)
		}
	}
}

func TestDenyDiscardsWithoutSpawning(t *testing.T) {
	f := testFleetWithClient(t, MCPClient{ID: "c1", OwnerID: DefaultOwnerID})
	registerRepo(t, f, "r1")
	res, _ := f.Submit(context.Background(), SpawnRequest{
		Origin: OriginMCP, ClientID: "c1", RepoID: "r1", Title: "t", Prompt: "p",
	})

	if err := f.Deny(res.PendingID); err != nil {
		t.Fatalf("Deny: %v", err)
	}
	if len(f.ws.Pending()) != 0 {
		t.Fatal("Deny left the submission queued")
	}
	if len(f.Snapshot()) != 0 {
		t.Fatal("Deny started an agent")
	}
}

func TestApproveRefusesAnExpiredSubmission(t *testing.T) {
	f := testFleetWithClient(t, MCPClient{ID: "c1", OwnerID: DefaultOwnerID})
	registerRepo(t, f, "r1")
	if err := f.ws.PutPending(PendingSpawn{
		ID: "old", Origin: OriginMCP, ClientID: "c1", RepoID: "r1", Title: "t",
		Prompt: "p", ExpiresAt: time.Now().UTC().Add(-time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Approve(context.Background(), "old"); err == nil {
		t.Fatal("an expired submission was approved")
	}
}
