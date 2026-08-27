package bridge

import (
	"context"
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
