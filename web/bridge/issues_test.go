package bridge

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// testFleetWithIssueForge builds a fleet whose forge server returns an
// issue when GET /repos/.../issues/42 is called.
func testFleetWithIssueForge(t *testing.T) (*Fleet, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/issues/42") && r.Method == "GET" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"number":42,"title":"fix it","body":"please fix","html_url":"https://github.com/you/r/issues/42","labels":[{"name":"bug"}]}`))
			return
		}
		if strings.Contains(r.URL.Path, "/issues") && r.Method == "GET" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`[{"number":42,"title":"fix it","body":"please","html_url":"https://github.com/you/r/issues/42","labels":[{"name":"marshal"}]}]`))
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"number":7,"html_url":"https://github.com/you/r/pull/7"}`))
	}))
	f := testFleetWithGate(t, gateResult{OK: true})
	return f, srv
}

func TestSubmitIssueCarriesTheIssueOntoTheAgent(t *testing.T) {
	f, srv := testFleetWithIssueForge(t)
	defer srv.Close()
	stubGitForForge(t, f)
	registerPATRepoWithAPIBase(t, f, "r1", srv.URL)

	res, err := f.SubmitIssue(context.Background(), "r1", 42)
	if err != nil {
		t.Fatalf("SubmitIssue: %v", err)
	}
	// A non-autonomous path still confirms, exactly like MCP intake.
	if res.Status != "pending" {
		t.Fatalf("status = %q, want pending", res.Status)
	}

	p := f.ws.Pending()[0]
	if p.Origin != OriginIssue {
		t.Errorf("origin = %q, want %q", p.Origin, OriginIssue)
	}
	if !strings.Contains(p.Title, "fix it") {
		t.Errorf("the issue title did not become the submission title: %q", p.Title)
	}
}

func TestApprovedIssueAgentRecordsTheIssueNumber(t *testing.T) {
	f, srv := testFleetWithIssueForge(t)
	defer srv.Close()
	stubGitForForge(t, f)
	registerPATRepoWithAPIBase(t, f, "r1", srv.URL)

	res, _ := f.SubmitIssue(context.Background(), "r1", 42)
	agentID, err := f.Approve(context.Background(), res.PendingID)
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	a, _ := f.ws.Agent(agentID)
	if a.IssueNumber != 42 {
		t.Fatalf("IssueNumber = %d, want 42 — the exit path cannot link the PR", a.IssueNumber)
	}
}

func TestSubmitIssueRefusesAnSSHRepo(t *testing.T) {
	f := testFleetWithSSHRepo(t)
	// Register a repo with no forge and no CredRef (ssh-like).
	if err := f.ws.PutRepo(Repo{ID: "ssh-repo", URL: "git@github.com:you/r.git",
		Branch: "main", OwnerID: DefaultOwnerID}); err != nil {
		t.Fatal(err)
	}
	_, err := f.SubmitIssue(context.Background(), "ssh-repo", 1)
	if err == nil {
		t.Fatal("issue intake was attempted for a repo whose credential cannot call an API")
	}
	if !strings.Contains(err.Error(), "credential") && !strings.Contains(err.Error(), "forge") {
		t.Errorf("the error does not explain why: %v", err)
	}
}

// registerPATRepoWithAPIBase registers a repo with a forge and PAT
// credential, with an explicit APIBase.
func registerPATRepoWithAPIBase(t *testing.T, f *Fleet, repoID, apiBase string) {
	t.Helper()
	repo := Repo{
		ID:      repoID,
		URL:     "https://github.com/you/r.git",
		Branch:  "main",
		CredRef: "pat1",
		OwnerID: DefaultOwnerID,
		Forge:   "github",
		APIBase: apiBase,
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
