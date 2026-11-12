package bridge

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitText(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func factoryGitFixture(t *testing.T) (*factoryGit, FactoryItem, FactoryWorker, string, string) {
	t.Helper()
	bare := newBareRepoFixture(t)
	work := filepath.Join(t.TempDir(), "checkout")
	gitText(t, "", "clone", bare, work)
	gitText(t, work, "config", "user.email", "test@example.com")
	gitText(t, work, "config", "user.name", "Test")
	base := gitText(t, work, "rev-parse", "HEAD")
	ws := NewWorkspace(filepath.Join(t.TempDir(), "fleet.json"))
	if err := ws.PutRepo(Repo{ID: "repo", URL: bare, Branch: "main", OwnerID: DefaultOwnerID}); err != nil {
		t.Fatal(err)
	}
	worker := FactoryWorker{AgentID: "agent-1", SessionID: "s-1", Role: "implementer", SpawnEffectID: "spawn-1"}
	a := Agent{ID: worker.AgentID, SessionID: worker.SessionID, Project: work, SourceKind: "git", SourceRef: "repo", FactoryItemID: "item-1", FactoryRole: worker.Role, FactorySpawnEffectID: worker.SpawnEffectID}
	if err := ws.PutAgent(a); err != nil {
		t.Fatal(err)
	}
	f := NewFleet(ws, "", nil, t.TempDir(), Limits{}, "", nil, "")
	tr := &captureTransport{onRequest: func(method string) error {
		if method == "session/commit" {
			gitText(t, work, "add", "--", "docs/marshal-factory/item-1/plan.md", "docs/marshal-factory/item-1/spec.md")
			gitText(t, work, "commit", "-m", "seed")
		}
		return nil
	}}
	c := &Child{Transport: tr}
	r := NewRegistry(c)
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Stop)
	r.track(worker.SessionID, AgentPath(work))
	rt := &agentRuntime{id: worker.AgentID, root: work, sessionID: worker.SessionID, child: c, reg: r, sourceKind: "git"}
	f.runtimes[worker.AgentID] = rt
	f.sessionAgent[worker.SessionID] = worker.AgentID
	item := FactoryItem{ID: "item-1", RepoID: "repo", Branch: "marshal/factory/item-1", BaseSHA: base, Approvals: []FactoryApproval{{Plan: "line 1\n", Spec: "spec\n"}}}
	return newFactoryGit(f), item, worker, work, bare
}

func TestFactorySeedAndPublish(t *testing.T) {
	ctx := context.Background()
	t.Run("seed exact bytes and publish without force", func(t *testing.T) {
		g, item, worker, work, bare := factoryGitFixture(t)
		marker := filepath.Join(t.TempDir(), "should-not-run")
		item.Approvals[0].Plan += "$(touch " + marker + ")\n"
		head, tree, err := g.Seed(ctx, item, worker)
		if err != nil {
			t.Fatal(err)
		}
		if head == item.BaseSHA || tree == "" {
			t.Fatalf("head=%q tree=%q", head, tree)
		}
		plan, err := os.ReadFile(filepath.Join(work, "docs/marshal-factory/item-1/plan.md"))
		if err != nil {
			t.Fatal(err)
		}
		if string(plan) != item.Approvals[0].Plan {
			t.Fatalf("plan bytes changed: %q", plan)
		}
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatalf("plan text executed as shell command: %v", err)
		}
		ph, pt, err := g.Publish(ctx, item, worker, "")
		if err != nil {
			t.Fatal(err)
		}
		if ph != head || pt != tree || gitText(t, bare, "rev-parse", "refs/heads/"+item.Branch) != head {
			t.Fatal("published head mismatch")
		}
		if _, _, err := g.Publish(ctx, item, worker, ""); err != nil {
			t.Fatalf("uncertain push reconciliation: %v", err)
		}
	})
	t.Run("dirty initial work rejected", func(t *testing.T) {
		g, item, worker, work, _ := factoryGitFixture(t)
		if err := os.WriteFile(filepath.Join(work, "extra"), []byte("user"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, _, err := g.Seed(ctx, item, worker); err == nil {
			t.Fatal("dirty checkout seeded")
		}
	})
	t.Run("unsafe ID rejected", func(t *testing.T) {
		g, item, worker, _, _ := factoryGitFixture(t)
		item.ID = "../escape"
		item.Branch = "marshal/factory/../escape"
		if _, _, err := g.Seed(ctx, item, worker); err == nil {
			t.Fatal("unsafe ID seeded")
		}
	})
	t.Run("unexpected remote rejected", func(t *testing.T) {
		g, item, worker, _, bare := factoryGitFixture(t)
		if _, _, err := g.Seed(ctx, item, worker); err != nil {
			t.Fatal(err)
		}
		gitText(t, bare, "update-ref", "refs/heads/"+item.Branch, item.BaseSHA)
		if _, _, err := g.Publish(ctx, item, worker, ""); err == nil {
			t.Fatal("unexpected branch published")
		}
	})
}
