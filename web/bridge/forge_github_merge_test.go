package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// factoryMergePRJSON is a GitHub pull request at the reviewed revision. A
// merged pull request keeps GitHub's "closed" state and sets merged, which is
// what factoryGitHubPR.factory maps back to "merged".
func factoryMergePRJSON(state, head, base string) string {
	ghState := "open"
	merged := false
	switch state {
	case "merged":
		ghState, merged = "closed", true
	case "closed":
		ghState = "closed"
	}
	return fmt.Sprintf(`{"number":7,"html_url":"https://github.com/o/r/pull/7","node_id":"node","state":%q,"merged":%t,"draft":false,"head":{"ref":"branch","sha":%q,"repo":{"full_name":"o/r"}},"base":{"ref":%q,"sha":"basesha"}}`, ghState, merged, head, base)
}

func factoryMergeReviewed() FactoryPRMergeRequest {
	return FactoryPRMergeRequest{Number: 7, NodeID: "node", HeadSHA: "headsha", BaseRef: "main", Strategy: "squash"}
}

func TestGitHubFactoryMergeRequest(t *testing.T) {
	var method, path, auth string
	var body map[string]any
	gets, puts := 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			puts++
			method, path, auth = r.Method, r.URL.Path, r.Header.Get("Authorization")
			_ = json.NewDecoder(r.Body).Decode(&body)
			fmt.Fprint(w, `{"merged":true,"sha":"headsha"}`)
			return
		}
		gets++
		state := "open"
		if gets > 1 {
			state = "merged"
		}
		fmt.Fprint(w, factoryMergePRJSON(state, "headsha", "main"))
	}))
	defer srv.Close()
	g := &githubForge{client: srv.Client()}
	err := g.MergeFactoryPR(context.Background(), Repo{URL: "https://github.com/o/r", APIBase: srv.URL}, factoryMergeReviewed(), Credential{Kind: "pat", literal: "tok"})
	if err != nil {
		t.Fatal(err)
	}
	if puts != 1 {
		t.Fatalf("issued %d merge requests, want exactly 1", puts)
	}
	if method != "PUT" || path != "/repos/o/r/pulls/7/merge" {
		t.Fatalf("method=%s path=%s", method, path)
	}
	if auth != "Bearer tok" {
		t.Fatalf("merge request authorization = %q", auth)
	}
	if body["sha"] != "headsha" || body["merge_method"] != "squash" {
		t.Fatalf("merge body = %v", body)
	}
	if len(body) != 2 {
		t.Fatalf("merge body carried more than the reviewed identity: %v", body)
	}
}

func TestGitHubFactoryMergeRejected(t *testing.T) {
	req := factoryMergeReviewed()
	repo := func(srv *httptest.Server) Repo {
		return Repo{URL: "https://github.com/o/r", APIBase: srv.URL}
	}

	t.Run("head_moved", func(t *testing.T) {
		puts := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPut {
				puts++
			}
			fmt.Fprint(w, factoryMergePRJSON("open", "otherhead", "main"))
		}))
		defer srv.Close()
		err := (&githubForge{client: srv.Client()}).MergeFactoryPR(context.Background(), repo(srv), req, Credential{})
		if !errors.Is(err, ErrFactoryMergeRejected) {
			t.Fatalf("err = %v, want ErrFactoryMergeRejected", err)
		}
		if puts != 0 {
			t.Fatalf("sent %d merge requests for an outdated review", puts)
		}
	})

	t.Run("not_mergeable", func(t *testing.T) {
		puts := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPut {
				puts++
				w.WriteHeader(http.StatusMethodNotAllowed)
				fmt.Fprint(w, `{"message":"Pull Request is not mergeable"}`)
				return
			}
			fmt.Fprint(w, factoryMergePRJSON("open", "headsha", "main"))
		}))
		defer srv.Close()
		err := (&githubForge{client: srv.Client()}).MergeFactoryPR(context.Background(), repo(srv), req, Credential{})
		if !errors.Is(err, ErrFactoryMergeRejected) {
			t.Fatalf("err = %v, want ErrFactoryMergeRejected", err)
		}
		if puts != 1 {
			t.Fatalf("issued %d merge requests, want exactly 1", puts)
		}
	})
}

func TestGitHubFactoryMergeUncertain(t *testing.T) {
	req := factoryMergeReviewed()
	repo := func(srv *httptest.Server) Repo {
		return Repo{URL: "https://github.com/o/r", APIBase: srv.URL}
	}

	for _, tc := range []struct {
		name   string
		after  string
		status int
	}{
		{"read_fails", "", http.StatusBadGateway},
		{"still_open", "open", http.StatusBadGateway},
	} {
		t.Run(tc.name, func(t *testing.T) {
			puts, gets := 0, 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPut {
					puts++
					w.WriteHeader(tc.status)
					return
				}
				gets++
				if gets > 1 && tc.after == "" {
					// The post-read itself fails: nothing proves the outcome.
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				fmt.Fprint(w, factoryMergePRJSON("open", "headsha", "main"))
			}))
			defer srv.Close()
			err := (&githubForge{client: srv.Client()}).MergeFactoryPR(context.Background(), repo(srv), req, Credential{})
			if !errors.Is(err, ErrFactoryMergeUncertain) {
				t.Fatalf("err = %v, want ErrFactoryMergeUncertain", err)
			}
			if puts != 1 {
				t.Fatalf("issued %d merge requests, want exactly 1", puts)
			}
		})
	}

	t.Run("dropped_but_merged", func(t *testing.T) {
		var putDone atomic.Bool
		puts := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPut {
				puts++
				// Answer nothing and drop the connection: the merge may have
				// been applied all the same.
				hj, ok := w.(http.Hijacker)
				if !ok {
					t.Error("server cannot hijack the merge response")
					return
				}
				conn, _, err := hj.Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				putDone.Store(true)
				conn.Close()
				return
			}
			state := "open"
			if putDone.Load() {
				state = "merged"
			}
			fmt.Fprint(w, factoryMergePRJSON(state, "headsha", "main"))
		}))
		defer srv.Close()
		err := (&githubForge{client: srv.Client()}).MergeFactoryPR(context.Background(), repo(srv), req, Credential{})
		if err != nil {
			t.Fatalf("err = %v, want nil once the forge proves the merge", err)
		}
		if puts != 1 {
			t.Fatalf("issued %d merge requests, want exactly 1", puts)
		}
	})

	t.Run("claimed_but_open", func(t *testing.T) {
		puts := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPut {
				puts++
				fmt.Fprint(w, `{"merged":true,"sha":"headsha"}`)
				return
			}
			fmt.Fprint(w, factoryMergePRJSON("open", "headsha", "main"))
		}))
		defer srv.Close()
		err := (&githubForge{client: srv.Client()}).MergeFactoryPR(context.Background(), repo(srv), req, Credential{})
		if !errors.Is(err, ErrFactoryMergeUncertain) {
			t.Fatalf("err = %v, want ErrFactoryMergeUncertain", err)
		}
		if puts != 1 {
			t.Fatalf("issued %d merge requests, want exactly 1", puts)
		}
	})
}

func TestFactoryMergeUnsupported(t *testing.T) {
	gitea, err := ForgeFor(Repo{Forge: "gitea"}, http.DefaultClient)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := FactoryPRMergerFor(gitea); !errors.Is(err, ErrFactoryMergeUnsupported) {
		t.Fatalf("gitea merge capability = %v, want ErrFactoryMergeUnsupported", err)
	}
	if _, err := FactoryPRMergerFor(struct{ Forge }{gitea}); !errors.Is(err, ErrFactoryMergeUnsupported) {
		t.Fatalf("plain forge merge capability = %v, want ErrFactoryMergeUnsupported", err)
	}
	github, err := ForgeFor(Repo{Forge: "github"}, http.DefaultClient)
	if err != nil {
		t.Fatal(err)
	}
	merger, err := FactoryPRMergerFor(github)
	if err != nil || merger == nil {
		t.Fatalf("github merge capability = %v, %v", merger, err)
	}
}
