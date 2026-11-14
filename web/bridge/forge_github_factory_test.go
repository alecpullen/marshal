package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFactoryGitHubEvidence(t *testing.T) {
	for _, conclusion := range []string{"success", "failure", "skipped", "neutral", "cancelled", "timed_out", "action_required", ""} {
		t.Run(conclusion, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.Path, "check-runs") {
					if r.URL.Query().Get("page") == "2" {
						fmt.Fprintf(w, `{"total_count":3,"check_runs":[{"id":3,"name":"build","head_sha":"sha","status":"completed","conclusion":%q,"app":{"id":1}}]}`, conclusion)
						return
					}
					w.Header().Set("Link", "<http://"+r.Host+r.URL.Path+"?page=2>; rel=\"next\"")
					fmt.Fprint(w, `{"total_count":3,"check_runs":[{"id":1,"name":"build","head_sha":"sha","status":"completed","conclusion":"success","app":{"id":1}},{"id":2,"name":"build","head_sha":"sha","status":"completed","conclusion":"success","app":{"id":2}}]}`)
					return
				}
				if strings.Contains(r.URL.Path, "/check-runs/") {
					fmt.Fprint(w, `{"output":{"text":"failed log"}}`)
					return
				}
				if r.URL.Query().Get("page") == "2" {
					fmt.Fprint(w, `[{"id":1,"context":"legacy","state":"failure"}]`)
					return
				}
				w.Header().Set("Link", "<http://"+r.Host+r.URL.Path+"?page=2>; rel=\"next\"")
				fmt.Fprint(w, `[{"id":2,"context":"legacy","state":"success"}]`)
			}))
			defer srv.Close()
			g := &githubForge{client: srv.Client()}
			ci, err := g.ObserveFactoryCI(context.Background(), Repo{URL: "https://github.com/o/r", APIBase: srv.URL}, "sha", Credential{})
			if err != nil || !ci.Complete || len(ci.Checks) != 3 {
				t.Fatalf("ci=%+v err=%v", ci, err)
			}
			state, _ := EvaluateFactoryCI(ci, []FactoryCheckKey{{Kind: "check_run", Name: "build", AppID: 1}}, false)
			if (state == "passed") != (conclusion == "success") {
				t.Fatalf("%s: %s", conclusion, state)
			}
		})
	}
	for _, code := range []int{403, 429, 500} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(code) }))
			defer srv.Close()
			ci, err := (&githubForge{srv.Client()}).ObserveFactoryCI(context.Background(), Repo{URL: "https://github.com/o/r", APIBase: srv.URL}, "sha", Credential{})
			state, _ := EvaluateFactoryCI(ci, nil, true)
			if err == nil || state == "passed" {
				t.Fatal(ci, err, state)
			}
		})
	}
	ci := FactoryCI{HeadSHA: "sha", Complete: true}
	if s, _ := EvaluateFactoryCI(ci, nil, false); s != "blocked" {
		t.Fatal(s)
	}
	if s, _ := EvaluateFactoryCI(ci, []FactoryCheckKey{{Kind: "check_run", Name: "missing"}}, false); s != "waiting" {
		t.Fatal(s)
	}
}

func TestFactoryGitHubQueuedAttemptOutranksSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "check-runs") {
			fmt.Fprint(w, `{"total_count":2,"check_runs":[{"id":10,"name":"build","head_sha":"sha","status":"completed","conclusion":"success","started_at":"2026-01-01T00:00:00Z","app":{"id":1}},{"id":11,"name":"build","head_sha":"sha","status":"queued","conclusion":null,"started_at":null,"app":{"id":1}}]}`)
			return
		}
		fmt.Fprint(w, `[]`)
	}))
	defer srv.Close()
	ci, err := (&githubForge{srv.Client()}).ObserveFactoryCI(context.Background(), Repo{URL: "https://github.com/o/r", APIBase: srv.URL}, "sha", Credential{})
	if err != nil || !ci.Complete || len(ci.Checks) != 1 || ci.Checks[0].ID != "11" {
		t.Fatalf("ci=%+v err=%v", ci, err)
	}
	if state, _ := EvaluateFactoryCI(ci, []FactoryCheckKey{{Kind: "check_run", Name: "build", AppID: 1}}, false); state != "waiting" {
		t.Fatalf("newest queued attempt gave state %q", state)
	}
}

func TestFactoryGitHubDraftMutationRejectsMismatchedConfirmation(t *testing.T) {
	for _, tc := range []struct {
		name, response string
		draft          bool
	}{
		{"wrong_node", `{"data":{"mutation":{"pullRequest":{"id":"other","isDraft":true}}}}`, true},
		{"wrong_draft_state", `{"data":{"mutation":{"pullRequest":{"id":"node","isDraft":false}}}}`, true},
		{"wrong_ready_state", `{"data":{"mutation":{"pullRequest":{"id":"node","isDraft":true}}}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, tc.response) }))
			defer srv.Close()
			err := (&githubForge{srv.Client()}).SetFactoryDraft(context.Background(), Repo{URL: "https://github.com/o/r", APIBase: srv.URL}, FactoryPR{NodeID: "node"}, tc.draft, Credential{})
			if err == nil {
				t.Fatal("accepted mismatched mutation confirmation")
			}
		})
	}
}

func TestFactoryGitHubPRLifecycle(t *testing.T) {
	creates := 0
	mutations := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && r.URL.Path == "/graphql" {
			var p map[string]any
			json.NewDecoder(r.Body).Decode(&p)
			if !strings.Contains(p["query"].(string), "PullRequest") {
				t.Error(p)
			}
			mutations++
			draft := strings.Contains(p["query"].(string), "convertPullRequestToDraft")
			fmt.Fprintf(w, `{"data":{"mutation":{"pullRequest":{"id":"node","isDraft":%t}}}}`, draft)
			return
		}
		if r.Method == "POST" {
			creates++
			var p PRRequest
			var raw map[string]any
			json.NewDecoder(r.Body).Decode(&raw)
			_ = p
			if raw["draft"] != true {
				t.Error(raw)
			}
			fmt.Fprint(w, `{"number":7,"html_url":"pr"}`)
			return
		}
		pr := `{"number":7,"node_id":"node","state":"open","draft":true,"head":{"ref":"branch","sha":"h"},"base":{"ref":"main","sha":"b"}}`
		if strings.HasSuffix(r.URL.Path, "/7") {
			fmt.Fprint(w, pr)
			return
		}
		if r.URL.Query().Get("page") == "2" {
			fmt.Fprint(w, "["+pr+"]")
			return
		}
		w.Header().Set("Link", "<http://"+r.Host+r.URL.Path+"?page=2>; rel=\"next\"")
		fmt.Fprint(w, `[]`)
	}))
	defer srv.Close()
	g := &githubForge{srv.Client()}
	repo := Repo{URL: "https://github.com/o/r", APIBase: srv.URL}
	ctx := context.Background()
	if _, err := g.CreatePR(ctx, repo, PRRequest{Draft: true}, Credential{}); err != nil {
		t.Fatal(err)
	}
	pr, err := g.FindFactoryPR(ctx, repo, "branch", "main", Credential{})
	if err != nil || pr == nil || pr.Number != 7 {
		t.Fatal(pr, err)
	}
	full, err := g.GetFactoryPR(ctx, repo, 7, Credential{})
	if err != nil || full.NodeID != "node" || full.BaseSHA != "b" {
		t.Fatal(full, err)
	}
	for _, draft := range []bool{false, true} {
		if err := g.SetFactoryDraft(ctx, repo, full, draft, Credential{}); err != nil {
			t.Fatal(err)
		}
	}
	if creates != 1 || mutations != 2 {
		t.Fatal(creates, mutations)
	}
}

func TestFactoryGitHubIncompleteAndAmbiguous(t *testing.T) {
	for _, mode := range []string{"truncated", "bad_second_page", "ambiguous", "closed", "merged", "graphql_error"} {
		t.Run(mode, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/graphql" {
					fmt.Fprint(w, `{"errors":[{"message":"denied"}]}`)
					return
				}
				if strings.Contains(r.URL.Path, "check-runs") {
					if r.URL.Query().Get("page") == "2" {
						fmt.Fprint(w, `{`)
						return
					}
					if mode == "bad_second_page" {
						w.Header().Set("Link", "<http://"+r.Host+r.URL.Path+"?page=2>; rel=\"next\"")
					}
					fmt.Fprint(w, `{"total_count":2,"check_runs":[{"id":1,"name":"build","head_sha":"sha","status":"completed","conclusion":"success","app":{"id":1}}]}`)
					return
				}
				merged := "null"
				if mode == "merged" {
					merged = `"2026-01-01T00:00:00Z"`
				}
				pr := fmt.Sprintf(`{"number":1,"node_id":"node","state":"closed","merged_at":%s,"head":{"ref":"branch","sha":"sha"},"base":{"ref":"main","sha":"base"}}`, merged)
				if mode == "ambiguous" {
					fmt.Fprint(w, "["+pr+","+pr+"]")
				} else {
					fmt.Fprint(w, "["+pr+"]")
				}
			}))
			defer srv.Close()
			g := &githubForge{srv.Client()}
			repo := Repo{URL: "https://github.com/o/r", APIBase: srv.URL}
			ctx := context.Background()
			switch mode {
			case "truncated", "bad_second_page":
				ci, err := g.ObserveFactoryCI(ctx, repo, "sha", Credential{})
				if err == nil || ci.Complete {
					t.Fatal(ci, err)
				}
			case "graphql_error":
				if g.SetFactoryDraft(ctx, repo, FactoryPR{NodeID: "node"}, false, Credential{}) == nil {
					t.Fatal("accepted GraphQL errors")
				}
			default:
				pr, err := g.FindFactoryPR(ctx, repo, "branch", "main", Credential{})
				if mode == "ambiguous" {
					if err == nil {
						t.Fatal("accepted ambiguous PR")
					}
				} else if err != nil || pr == nil || pr.State != mode {
					t.Fatal(pr, err)
				}
			}
		})
	}
}

func TestFactoryGitHubLostCreateResponse(t *testing.T) {
	creates := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			creates++
			fmt.Fprint(w, `{`)
			return
		}
		fmt.Fprint(w, `[{"number":1,"head":{"ref":"branch"},"base":{"ref":"main"}}]`)
	}))
	defer srv.Close()
	g := &githubForge{srv.Client()}
	repo := Repo{URL: "https://github.com/o/r", APIBase: srv.URL}
	ctx := context.Background()
	// A controller reconciles uncertain creation by lookup, never blindly repeating creation.
	if _, err := g.CreatePR(ctx, repo, PRRequest{Draft: true}, Credential{}); err == nil {
		t.Fatal("expected lost response")
	}
	pr, err := g.FindFactoryPR(ctx, repo, "branch", "main", Credential{})
	if err != nil || pr == nil || creates != 1 {
		t.Fatal(pr, err, creates)
	}
}

func TestFactoryGitHubRejectsMissingEvidence(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "check-runs") {
			fmt.Fprint(w, `{}`)
		} else {
			fmt.Fprint(w, `[]`)
		}
	}))
	defer srv.Close()
	ci, err := (&githubForge{srv.Client()}).ObserveFactoryCI(context.Background(), Repo{URL: "https://github.com/o/r", APIBase: srv.URL}, "sha", Credential{})
	if err == nil || ci.Complete {
		t.Fatal(ci, err)
	}
}

func TestFactoryGitHubEvaluation(t *testing.T) {
	key := FactoryCheckKey{Kind: "check_run", Name: "build", AppID: 1}
	for _, tc := range []struct {
		name, status, conclusion, want string
		required                       bool
	}{
		{"pending", "queued", "", "waiting", true},
		{"optional_skipped", "completed", "skipped", "passed", false},
		{"optional_neutral", "completed", "neutral", "passed", false},
		{"optional_failure", "completed", "failure", "failed", false},
		{"optional_timeout", "completed", "timed_out", "blocked", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var expected []FactoryCheckKey
			if tc.required {
				expected = []FactoryCheckKey{key}
			}
			ci := FactoryCI{HeadSHA: "sha", Complete: true, Checks: []FactoryCheck{{Key: key, SHA: "sha", Status: tc.status, Conclusion: tc.conclusion}}}
			if s, _ := EvaluateFactoryCI(ci, expected, true); s != tc.want {
				t.Fatal(s, tc.want)
			}
		})
	}
	ci := FactoryCI{HeadSHA: "sha", Complete: true, Checks: []FactoryCheck{{Key: FactoryCheckKey{Kind: "check_run", Name: "build", AppID: 2}, SHA: "sha", Status: "completed", Conclusion: "success"}}}
	if s, _ := EvaluateFactoryCI(ci, []FactoryCheckKey{key}, false); s != "waiting" {
		t.Fatal(s)
	}
	ci.Checks[0].SHA = "old"
	if s, _ := EvaluateFactoryCI(ci, nil, true); s != "unavailable" {
		t.Fatal(s)
	}
	f, _ := ForgeFor(Repo{Forge: "gitea"}, http.DefaultClient)
	if _, err := FactoryForgeFor(f); err != ErrFactoryForgeUnsupported {
		t.Fatal(err)
	}
}

func TestFactoryGitHubUpdatesPRDescription(t *testing.T) {
	var method, path string
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&body)
		fmt.Fprint(w, `{"number":7,"node_id":"node","state":"open","draft":true,"head":{"ref":"branch","sha":"h"},"base":{"ref":"main","sha":"b"}}`)
	}))
	defer srv.Close()
	g := &githubForge{srv.Client()}
	err := g.UpdateFactoryPRDescription(context.Background(), Repo{URL: "https://github.com/o/r", APIBase: srv.URL}, 7, "## CI\n\nbody", Credential{})
	if err != nil {
		t.Fatal(err)
	}
	if method != "PATCH" || path != "/repos/o/r/pulls/7" || body["body"] != "## CI\n\nbody" {
		t.Fatalf("method=%s path=%s body=%v", method, path, body)
	}
}

func TestFactoryGitHubDescriptionUpdateRejectsFailure(t *testing.T) {
	repo := Repo{URL: "https://github.com/o/r"}
	for _, code := range []int{403, 422, 500} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(code)
				fmt.Fprint(w, `{"message":"denied"}`)
			}))
			defer srv.Close()
			repo.APIBase = srv.URL
			if err := (&githubForge{srv.Client()}).UpdateFactoryPRDescription(context.Background(), repo, 7, "body", Credential{}); err == nil {
				t.Fatalf("accepted %d", code)
			}
		})
	}
	t.Run("mismatched pull request", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"number":8,"node_id":"node"}`)
		}))
		defer srv.Close()
		repo.APIBase = srv.URL
		if err := (&githubForge{srv.Client()}).UpdateFactoryPRDescription(context.Background(), repo, 7, "body", Credential{}); err == nil {
			t.Fatal("accepted a mismatched pull request number")
		}
	})
}

func TestFactoryPRDescriptionCapability(t *testing.T) {
	d, err := FactoryPRDescriptionFor(&githubForge{http.DefaultClient})
	if err != nil || d == nil {
		t.Fatalf("GitHub forge lost the description capability: %v", err)
	}
	f, _ := ForgeFor(Repo{Forge: "gitea"}, http.DefaultClient)
	if _, err := FactoryPRDescriptionFor(f); !errors.Is(err, ErrFactoryPRDescriptionUnsupported) {
		t.Fatalf("forge without the capability reported %v", err)
	}
}
