package bridge

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseOwnerRepoHandlesBothURLForms(t *testing.T) {
	cases := map[string][2]string{
		"https://github.com/you/marshal.git":    {"you", "marshal"},
		"https://github.com/you/marshal":        {"you", "marshal"},
		"git@github.com:you/marshal.git":        {"you", "marshal"},
		"https://code.example.com/team/sub.git": {"team", "sub"},
	}
	for in, want := range cases {
		owner, repo, err := parseOwnerRepo(in)
		if err != nil {
			t.Errorf("parseOwnerRepo(%q): %v", in, err)
			continue
		}
		if owner != want[0] || repo != want[1] {
			t.Errorf("parseOwnerRepo(%q) = (%q,%q), want %v", in, owner, repo, want)
		}
	}
}

func TestGitHubCreatePRUsesTheRightPathAndAuth(t *testing.T) {
	var gotPath, gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"number":7,"html_url":"https://github.com/you/r/pull/7"}`))
	}))
	defer srv.Close()

	f := newGitHubForge(srv.Client())
	repo := Repo{URL: "https://github.com/you/r.git", APIBase: srv.URL}
	pr, err := f.CreatePR(context.Background(), repo, PRRequest{
		Title: "t", Body: "Closes #42", Head: "marshal/a1", Base: "main", Draft: true,
	}, Credential{Kind: "pat", literal: "sk-token"})
	if err != nil {
		t.Fatalf("CreatePR: %v", err)
	}

	if gotPath != "/repos/you/r/pulls" {
		t.Errorf("path = %q, want /repos/you/r/pulls", gotPath)
	}
	if gotAuth != "Bearer sk-token" {
		t.Errorf("auth = %q, want Bearer form", gotAuth)
	}
	if !strings.Contains(gotBody, `"draft":true`) {
		t.Errorf("draft not requested: %s", gotBody)
	}
	if pr.Number != 7 || pr.URL == "" {
		t.Errorf("got %+v", pr)
	}
}

func TestGitHubRepoSizeReadsKilobytes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/you/r" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"size":2048}`)) // GitHub reports KB
	}))
	defer srv.Close()

	f := newGitHubForge(srv.Client())
	got, err := f.RepoSize(context.Background(),
		Repo{URL: "https://github.com/you/r.git", APIBase: srv.URL}, Credential{Kind: "pat", literal: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if got != 2048<<10 {
		t.Fatalf("RepoSize = %d bytes, want %d — KB were not converted", got, 2048<<10)
	}
}

func TestOversizeRepoIsRefusedBeforeCloning(t *testing.T) {
	var cloned bool
	f := testFleetWithLimits(t, Limits{MaxCloneMB: 1})
	if f.git == nil {
		t.Skip("git not installed")
	}
	// Stub git so a clone attempt is observable (and would fail offline).
	f.git.exec = func(dir string, env []string, args ...string) ([]byte, error) {
		cloned = true
		return nil, fmt.Errorf("clone should never have been attempted")
	}

	// A forge server reporting a repo far over the 1 MB cap.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"size":4096}`)) // 4096 KB = 4 MB
	}))
	defer srv.Close()

	// Register a PAT repo pointing at the test server (APIBase is empty
	// in registerPATRepo, so set it explicitly).
	repo := Repo{
		ID:      "r1",
		URL:     "https://github.com/you/r.git",
		Branch:  "main",
		CredRef: "pat1",
		OwnerID: DefaultOwnerID,
		Forge:   "github",
		APIBase: srv.URL,
	}
	if err := f.ws.PutRepo(repo); err != nil {
		t.Fatal(err)
	}
	f.creds = NewCredentialStore([]Credential{{
		ID: "pat1", Kind: "pat", OwnerID: DefaultOwnerID, EnvVar: "TEST_PAT",
	}})
	t.Setenv("TEST_PAT", "test-token")

	if _, err := f.Spawn(context.Background(), "", SpawnOptions{RepoID: "r1", Prompt: "x"}); err == nil {
		t.Fatal("an oversize repo was accepted")
	}
	if cloned {
		t.Fatal("the clone started despite a forge size that already exceeded the cap")
	}
}

func TestMissingForgeSizeStillProceedsToClone(t *testing.T) {
	var cloned bool
	f := testFleetWithLimits(t, Limits{MaxCloneMB: 1})
	if f.git == nil {
		t.Skip("git not installed")
	}
	// Stub git so a clone attempt is observable (and would fail offline).
	f.git.exec = func(dir string, env []string, args ...string) ([]byte, error) {
		cloned = true
		return nil, fmt.Errorf("clone should never have been attempted")
	}

	// A forge server that errors on the size lookup.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	repo := Repo{
		ID:      "r1",
		URL:     "https://github.com/you/r.git",
		Branch:  "main",
		CredRef: "pat1",
		OwnerID: DefaultOwnerID,
		Forge:   "github",
		APIBase: srv.URL,
	}
	if err := f.ws.PutRepo(repo); err != nil {
		t.Fatal(err)
	}
	f.creds = NewCredentialStore([]Credential{{
		ID: "pat1", Kind: "pat", OwnerID: DefaultOwnerID, EnvVar: "TEST_PAT",
	}})
	t.Setenv("TEST_PAT", "test-token")

	// A failed forge size lookup must not block the spawn: it proceeds to
	// the clone step, which has its own monitor. The clone here is stubbed
	// to fail, so we only assert that the clone step was reached.
	if _, err := f.Spawn(context.Background(), "", SpawnOptions{RepoID: "r1", Prompt: "x"}); err == nil {
		t.Fatal("expected the stubbed clone to fail")
	}
	if !cloned {
		t.Fatal("a failed forge size lookup prevented the clone step from running")
	}
}

func TestGitHubSurfacesAPIErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"message":"A pull request already exists"}`))
	}))
	defer srv.Close()

	f := newGitHubForge(srv.Client())
	_, err := f.CreatePR(context.Background(),
		Repo{URL: "https://github.com/you/r.git", APIBase: srv.URL},
		PRRequest{Title: "t", Head: "h", Base: "main"}, Credential{Kind: "pat", literal: "x"})
	if err == nil {
		t.Fatal("a 422 was reported as success")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("the forge's message was lost: %v", err)
	}
}

func TestForgeErrorNeverEchoesTheToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	f := newGitHubForge(srv.Client())
	_, err := f.CreatePR(context.Background(),
		Repo{URL: "https://github.com/you/r.git", APIBase: srv.URL},
		PRRequest{Title: "t", Head: "h", Base: "main"},
		Credential{Kind: "pat", literal: "sk-super-secret"})
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "sk-super-secret") {
		t.Fatalf("the token leaked into an error: %v", err)
	}
}
