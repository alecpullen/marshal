package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// forgeStub is a GitHub-shaped forge server for the automations' tests.
type forgeStub struct {
	srv *httptest.Server
	mu  sync.Mutex
	// prs maps a PR number to its JSON; list serves them all.
	prs map[int]string
	// checkRuns maps a ref to the check_runs JSON array.
	checkRuns map[string]string
	reviews   []string
	reviewURL string
	// rejectInline answers 422 to a review that carries inline comments.
	rejectInline bool
}

func newForgeStub(t *testing.T) *forgeStub {
	t.Helper()
	s := &forgeStub{prs: map[int]string{}, checkRuns: map[string]string{}, reviewURL: "https://github.com/you/r/pull/7#review"}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		p := r.URL.Path
		switch {
		case r.Method == "GET" && p == "/repos/you/r/pulls":
			var all []string
			for _, v := range s.prs {
				all = append(all, v)
			}
			_, _ = w.Write([]byte("[" + strings.Join(all, ",") + "]"))
		case r.Method == "GET" && strings.HasPrefix(p, "/repos/you/r/pulls/"):
			var n int
			_, _ = fmt.Sscanf(strings.TrimPrefix(p, "/repos/you/r/pulls/"), "%d", &n)
			if v, ok := s.prs[n]; ok {
				_, _ = w.Write([]byte(v))
				return
			}
			http.NotFound(w, r)
		case r.Method == "POST" && strings.HasSuffix(p, "/reviews"):
			b, _ := io.ReadAll(r.Body)
			if s.rejectInline && !strings.Contains(string(b), `"comments":[]`) {
				w.WriteHeader(http.StatusUnprocessableEntity)
				_, _ = w.Write([]byte(`{"message":"Line could not be resolved"}`))
				return
			}
			s.reviews = append(s.reviews, string(b))
			_, _ = w.Write([]byte(`{"html_url":"` + s.reviewURL + `"}`))
		case r.Method == "GET" && strings.HasPrefix(p, "/repos/you/r/commits/") && strings.HasSuffix(p, "/check-runs"):
			ref := strings.TrimSuffix(strings.TrimPrefix(p, "/repos/you/r/commits/"), "/check-runs")
			_, _ = w.Write([]byte(`{"check_runs":` + orEmpty(s.checkRuns[ref]) + `}`))
		case r.Method == "GET" && strings.HasPrefix(p, "/repos/you/r/check-runs/"):
			_, _ = w.Write([]byte(`{"output":{"text":"log line\n$ go test ./...\nFAIL"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func orEmpty(s string) string {
	if s == "" {
		return "[]"
	}
	return s
}

func prJSON(number int, title, sha, headRef, author string, draft bool, labels ...string) string {
	var ls []string
	for _, l := range labels {
		ls = append(ls, fmt.Sprintf(`{"name":%q}`, l))
	}
	return fmt.Sprintf(`{"number":%d,"title":%q,"html_url":"https://github.com/you/r/pull/%d","draft":%t,
		"updated_at":%q,"head":{"ref":%q,"sha":%q},"base":{"ref":"main"},"user":{"login":%q},"labels":[%s]}`,
		number, title, number, draft, time.Now().UTC().Format(time.RFC3339), headRef, sha, author, strings.Join(ls, ","))
}

func (s *forgeStub) postedReviews() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.reviews...)
}

// autoEnv is a fleet with a registered project and repo r1 pointed at a
// forge stub.
type autoEnv struct {
	f     *Fleet
	srv   *Server
	forge *forgeStub
	root  string
	// tips is each branch's tip commit, as the repo's mirror reports it.
	tips map[string]string
}

func newAutoEnv(t *testing.T, a *Automations) *autoEnv {
	t.Helper()
	return newAutoEnvOn(t, newTestFleetWithLimit(t, 4), a)
}

// newAutoEnvOn wires the automations' environment onto an existing fleet.
func newAutoEnvOn(t *testing.T, f *Fleet, a *Automations) *autoEnv {
	t.Helper()
	f.SetSecrets(&memSecrets{})
	stub := newForgeStub(t)
	registerPATRepoWithAPIBase(t, f, "r1", stub.srv.URL)
	root := t.TempDir()
	if err := f.ws.AddProject(root); err != nil {
		t.Fatal(err)
	}
	if err := f.ws.PutProjectSettings(root, ProjectSettings{Automations: a}); err != nil {
		t.Fatal(err)
	}
	e := &autoEnv{f: f, srv: NewServer(f, ""), forge: stub, root: root, tips: map[string]string{}}
	f.auto.branchHead = func(_ context.Context, _ Repo, branch string) (string, error) {
		if sha, ok := e.tips[branch]; ok {
			return sha, nil
		}
		return "", fmt.Errorf("no such branch %q", branch)
	}
	return e
}

func (e *autoEnv) setAutomations(t *testing.T, a *Automations) {
	t.Helper()
	if err := e.f.ws.PutProjectSettings(e.root, ProjectSettings{Automations: a}); err != nil {
		t.Fatal(err)
	}
}

// automationDeltas returns the "automation" deltas on the fleet stream.
func automationDeltas(f *Fleet) []automationDelta {
	var out []automationDelta
	for _, e := range f.fleetLog.Tail(fleetStreamKey) {
		var d automationDelta
		if json.Unmarshal(e.Data, &d) == nil && d.Kind == "automation" {
			out = append(out, d)
		}
	}
	return out
}

// recipeSpy replaces the fleet's recipe runner and records each request.
type recipeSpy struct {
	mu    sync.Mutex
	names []string
	reqs  []RecipeRunRequest
	err   error
}

func (e *autoEnv) spyRecipes() *recipeSpy {
	spy := &recipeSpy{}
	e.f.auto.runRecipe = func(_ context.Context, name string, req RecipeRunRequest) (string, error) {
		spy.mu.Lock()
		defer spy.mu.Unlock()
		spy.names = append(spy.names, name)
		spy.reqs = append(spy.reqs, req)
		if spy.err != nil {
			return "", spy.err
		}
		return "agent-1", nil
	}
	return spy
}

func (s *recipeSpy) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.reqs)
}

func (s *recipeSpy) last() (string, RecipeRunRequest) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.names[len(s.names)-1], s.reqs[len(s.reqs)-1]
}
