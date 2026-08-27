package bridge

import (
	"context"
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
