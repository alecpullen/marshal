package bridge

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGiteaUsesTokenAuthAndIndexField(t *testing.T) {
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		w.WriteHeader(http.StatusCreated)
		// Gitea names the pull request identifier "index", not "number".
		_, _ = w.Write([]byte(`{"index":7,"html_url":"https://code.example.com/you/r/pulls/7"}`))
	}))
	defer srv.Close()

	f := newGiteaForge(srv.Client())
	repo := Repo{URL: "https://code.example.com/you/r.git", APIBase: srv.URL + "/api/v1"}
	pr, err := f.CreatePR(context.Background(), repo,
		PRRequest{Title: "t", Head: "marshal/a1", Base: "main"},
		Credential{Kind: "pat", literal: "sk-token"})
	if err != nil {
		t.Fatalf("CreatePR: %v", err)
	}

	if gotPath != "/api/v1/repos/you/r/pulls" {
		t.Errorf("path = %q, want /api/v1/repos/you/r/pulls", gotPath)
	}
	if gotAuth != "token sk-token" {
		t.Errorf("auth = %q, want Gitea's token form", gotAuth)
	}
	if pr.Number != 7 {
		t.Errorf("Number = %d — the index field was not read", pr.Number)
	}
}

func TestGiteaRepoSizeReadsKilobytes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"size":512}`))
	}))
	defer srv.Close()

	f := newGiteaForge(srv.Client())
	got, err := f.RepoSize(context.Background(),
		Repo{URL: "https://code.example.com/you/r.git", APIBase: srv.URL + "/api/v1"},
		Credential{Kind: "pat", literal: "x"})
	if err != nil || got != 512<<10 {
		t.Fatalf("RepoSize = %d, err = %v", got, err)
	}
}

func TestGiteaListIssuesFiltersByLabel(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`[{"number":3,"title":"fix it","body":"please",` +
			`"html_url":"https://code.example.com/you/r/issues/3","labels":[{"name":"marshal"}]}]`))
	}))
	defer srv.Close()

	f := newGiteaForge(srv.Client())
	issues, err := f.ListIssues(context.Background(),
		Repo{URL: "https://code.example.com/you/r.git", APIBase: srv.URL + "/api/v1"},
		IssueQuery{Label: "marshal"}, Credential{Kind: "pat", literal: "x"})
	if err != nil {
		t.Fatalf("ListIssues: %v", err)
	}
	if !strings.Contains(gotQuery, "labels=marshal") {
		t.Errorf("label filter not sent: %q", gotQuery)
	}
	if len(issues) != 1 || issues[0].Number != 3 || issues[0].Title != "fix it" {
		t.Fatalf("got %+v", issues)
	}
}

func TestGiteaListAndGetPR(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/team/r/pulls":
			_, _ = w.Write([]byte(`[{"number":4,"title":"WIP: later","html_url":"u4","updated_at":"2026-01-02T00:00:00Z",
			 "head":{"ref":"f","sha":"s4"},"base":{"ref":"main"},"user":{"login":"amy"},"labels":[{"name":"review"}]},
			 {"number":5,"title":"x","updated_at":"2026-01-02T00:00:00Z","labels":[]}]`))
		case "/repos/team/r/pulls/4":
			_, _ = w.Write([]byte(`{"index":4,"title":"t","html_url":"u4","head":{"ref":"f","sha":"s4"},"base":{"ref":"main"}}`))
		default:
			t.Errorf("path = %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	f := newGiteaForge(srv.Client())
	repo := Repo{URL: "https://code.example.com/team/r.git", APIBase: srv.URL}
	prs, err := f.ListPRs(context.Background(), repo, PRQuery{Label: "review"}, Credential{literal: "t"})
	if err != nil || len(prs) != 1 || prs[0].Number != 4 || !prs[0].Draft || prs[0].Author != "amy" {
		t.Fatalf("prs = %+v, err = %v", prs, err)
	}
	pr, err := f.GetPR(context.Background(), repo, 4, Credential{literal: "t"})
	if err != nil || pr.Number != 4 || pr.HeadSHA != "s4" {
		t.Fatalf("pr = %+v, err = %v", pr, err)
	}
}

func TestGiteaPostReviewUsesNewPosition(t *testing.T) {
	var gotBody, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody, gotAuth = string(b), r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"html_url":"rv"}`))
	}))
	defer srv.Close()
	url, err := newGiteaForge(srv.Client()).PostReview(context.Background(),
		Repo{URL: "https://code.example.com/team/r.git", APIBase: srv.URL}, 4,
		Review{Body: "s", Comments: []ReviewLine{{Path: "a.go", Line: 7, Body: "b"}}}, Credential{literal: "t"})
	if err != nil || url != "rv" || gotAuth != "token t" {
		t.Fatalf("url = %q, auth = %q, err = %v", url, gotAuth, err)
	}
	if !strings.Contains(gotBody, `"new_position":7`) || !strings.Contains(gotBody, `"event":"COMMENT"`) {
		t.Errorf("body = %s", gotBody)
	}
}

func TestGiteaFailedChecksKeepNewestFailurePerContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[
		 {"id":9,"status":"success","context":"test"},
		 {"id":8,"status":"failure","context":"test","description":"old"},
		 {"id":7,"status":"failure","context":"lint","description":"lint broke","target_url":"https://ci/7"}]`))
	}))
	defer srv.Close()
	f := newGiteaForge(srv.Client())
	repo := Repo{URL: "https://code.example.com/team/r.git", APIBase: srv.URL}
	checks, err := f.ListFailedChecks(context.Background(), repo, "abc", Credential{literal: "t"})
	if err != nil || len(checks) != 1 || checks[0].Name != "lint" || checks[0].ID != 7 {
		t.Fatalf("checks = %+v, err = %v", checks, err)
	}
	log, err := f.CheckLog(context.Background(), repo, checks[0], Credential{literal: "t"})
	if err != nil || log != "lint broke\nhttps://ci/7" {
		t.Fatalf("log = %q, err = %v", log, err)
	}
}
