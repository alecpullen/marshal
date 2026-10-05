package bridge

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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

func TestGitHubListPRsFiltersByLabelAndSince(t *testing.T) {
	var gotURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURL = r.URL.RequestURI()
		_, _ = w.Write([]byte(`[
		 {"number":3,"title":"new","html_url":"u3","draft":true,"updated_at":"2026-01-03T00:00:00Z",
		  "head":{"ref":"feat","sha":"aaa"},"base":{"ref":"main"},"user":{"login":"bob"},"labels":[{"name":"review"}]},
		 {"number":2,"title":"other","updated_at":"2026-01-02T00:00:00Z","labels":[{"name":"x"}]},
		 {"number":1,"title":"old","updated_at":"2025-01-01T00:00:00Z","labels":[{"name":"review"}]}]`))
	}))
	defer srv.Close()
	f := newGitHubForge(srv.Client())
	repo := Repo{URL: "https://github.com/you/r.git", APIBase: srv.URL}
	since := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	prs, err := f.ListPRs(context.Background(), repo, PRQuery{Label: "review", Since: since}, Credential{literal: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(gotURL, "/repos/you/r/pulls?state=open&per_page=100&sort=updated&direction=desc") {
		t.Errorf("url = %s", gotURL)
	}
	if len(prs) != 1 || prs[0].Number != 3 || !prs[0].Draft || prs[0].HeadSHA != "aaa" ||
		prs[0].HeadRef != "feat" || prs[0].BaseRef != "main" || prs[0].Author != "bob" {
		t.Fatalf("prs = %+v", prs)
	}
}

func TestGitHubGetPR(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"number":9,"title":"t","html_url":"u","head":{"ref":"h","sha":"s"},"base":{"ref":"main"}}`))
	}))
	defer srv.Close()
	pr, err := newGitHubForge(srv.Client()).GetPR(context.Background(),
		Repo{URL: "https://github.com/you/r.git", APIBase: srv.URL}, 9, Credential{literal: "t"})
	if err != nil || gotPath != "/repos/you/r/pulls/9" || pr.HeadSHA != "s" || pr.URL != "u" {
		t.Fatalf("pr = %+v, path = %s, err = %v", pr, gotPath, err)
	}
}

func TestGitHubPostReviewMapsInlineComments(t *testing.T) {
	var gotPath, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.Method + " " + r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		_, _ = w.Write([]byte(`{"html_url":"https://github.com/you/r/pull/9#pullrequestreview-1"}`))
	}))
	defer srv.Close()
	url, err := newGitHubForge(srv.Client()).PostReview(context.Background(),
		Repo{URL: "https://github.com/you/r.git", APIBase: srv.URL}, 9,
		Review{Body: "summary", Comments: []ReviewLine{{Path: "a.go", Line: 4, Body: "fix"}}}, Credential{literal: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "POST /repos/you/r/pulls/9/reviews" || !strings.HasSuffix(url, "pullrequestreview-1") {
		t.Errorf("path = %s, url = %s", gotPath, url)
	}
	for _, want := range []string{`"event":"COMMENT"`, `"path":"a.go"`, `"line":4`, `"side":"RIGHT"`, `"body":"summary"`} {
		if !strings.Contains(gotBody, want) {
			t.Errorf("body %s lacks %s", gotBody, want)
		}
	}
}

func TestGitHubListFailedChecksKeepsFailuresAndParsesJobID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/you/r/commits/abc/check-runs" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"check_runs":[
		 {"id":1,"name":"build","conclusion":"success"},
		 {"id":2,"name":"test","conclusion":"failure","html_url":"h","details_url":"https://github.com/you/r/actions/runs/55/job/777"},
		 {"id":3,"name":"lint","conclusion":"failure","details_url":"https://ci.example.com/x"}]}`))
	}))
	defer srv.Close()
	checks, err := newGitHubForge(srv.Client()).ListFailedChecks(context.Background(),
		Repo{URL: "https://github.com/you/r.git", APIBase: srv.URL}, "abc", Credential{literal: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if len(checks) != 2 || checks[0].Name != "test" || checks[0].JobID != 777 || checks[0].Ref != "abc" ||
		checks[1].Name != "lint" || checks[1].JobID != 0 {
		t.Fatalf("checks = %+v", checks)
	}
}

func TestGitHubCheckLogTailsTheJobLogAcrossARedirect(t *testing.T) {
	big := strings.Repeat("x", checkLogLimit+5000) + "\n$ go test ./...\nFAIL\n"
	blob := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(big))
	}))
	defer blob.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/you/r/actions/jobs/777/logs" {
			t.Errorf("path = %s", r.URL.Path)
		}
		http.Redirect(w, r, blob.URL+"/log", http.StatusFound)
	}))
	defer srv.Close()
	log, err := newGitHubForge(srv.Client()).CheckLog(context.Background(),
		Repo{URL: "https://github.com/you/r.git", APIBase: srv.URL}, CheckInfo{ID: 2, JobID: 777}, Credential{literal: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if len(log) != checkLogLimit || !strings.HasSuffix(log, "$ go test ./...\nFAIL\n") {
		t.Fatalf("len = %d, tail = %q", len(log), log[len(log)-30:])
	}
}

func TestGitHubCheckLogFallsBackToOutputText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/you/r/check-runs/2" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"output":{"title":"T","summary":"S","text":"the detail"}}`))
	}))
	defer srv.Close()
	log, err := newGitHubForge(srv.Client()).CheckLog(context.Background(),
		Repo{URL: "https://github.com/you/r.git", APIBase: srv.URL}, CheckInfo{ID: 2}, Credential{literal: "t"})
	if err != nil || log != "the detail" {
		t.Fatalf("log = %q, err = %v", log, err)
	}
}
