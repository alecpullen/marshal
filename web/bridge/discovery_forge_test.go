package bridge

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// discoveryTestToken is a credential literal that must never appear in a
// probe's error or result.
const discoveryTestToken = "ghp_discoveryTokenMustNeverLeak"

type discoveryRecordedRequest struct {
	Method string
	Host   string
	Path   string
	Query  string
	Auth   string
}

// discoveryRecordingServer records every request before delegating, so a test
// can assert the wire shape and whether a probe made any call at all.
func discoveryRecordingServer(t *testing.T, handle http.HandlerFunc) (*httptest.Server, func() []discoveryRecordedRequest) {
	t.Helper()
	var mu sync.Mutex
	var recorded []discoveryRecordedRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		recorded = append(recorded, discoveryRecordedRequest{
			Method: r.Method,
			Host:   r.Host,
			Path:   r.URL.Path,
			Query:  r.URL.RawQuery,
			Auth:   r.Header.Get("Authorization"),
		})
		mu.Unlock()
		handle(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []discoveryRecordedRequest {
		mu.Lock()
		defer mu.Unlock()
		return append([]discoveryRecordedRequest(nil), recorded...)
	}
}

// discoveryHostOf strips the scheme from an httptest server URL.
func discoveryHostOf(srv *httptest.Server) string {
	return strings.TrimPrefix(strings.TrimPrefix(srv.URL, "http://"), "https://")
}

// discoveryTestRepo is a registered forge repository pointed at a fixture.
func discoveryTestRepo(forge, apiBase string) Repo {
	return Repo{
		ID:      "repo-1",
		URL:     "https://forge.example.com/acme/widgets.git",
		Forge:   forge,
		APIBase: apiBase,
		CredRef: "gh",
		OwnerID: "local",
	}
}

func discoveryTestCredential() Credential {
	return Credential{ID: "gh", Kind: "pat", OwnerID: "local", literal: discoveryTestToken}
}

// TestDiscoveryForgeGitHubReportsRepositoryMetadata pins the repository facts a
// GitHub probe reports, the Bearer PAT header, and Repo.APIBase as the root.
func TestDiscoveryForgeGitHubReportsRepositoryMetadata(t *testing.T) {
	srv, requests := discoveryRecordingServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/acme/widgets" {
			http.Error(w, `{"message":"unexpected path"}`, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{
			"default_branch": "main",
			"private": true,
			"description": "Acme widget factory",
			"topics": ["widgets", "factory"],
			"language": "Go",
			"size": 12
		}`)
	})

	repo := discoveryTestRepo("github", srv.URL)
	cred := discoveryTestCredential()

	forge, err := DiscoveryForgeFor(repo, srv.Client())
	if err != nil {
		t.Fatalf("DiscoveryForgeFor(github) error: %v", err)
	}

	md, err := forge.RepositoryMetadata(context.Background(), repo, cred)
	if err != nil {
		t.Fatalf("RepositoryMetadata error: %v", err)
	}
	if md.DefaultBranch != "main" {
		t.Errorf("default branch = %q, want main", md.DefaultBranch)
	}
	if md.Visibility != "private" {
		t.Errorf("visibility = %q, want private", md.Visibility)
	}
	if md.Description != "Acme widget factory" {
		t.Errorf("description = %q", md.Description)
	}
	if want := []string{"widgets", "factory"}; !reflect.DeepEqual(md.Topics, want) {
		t.Errorf("topics = %v, want %v", md.Topics, want)
	}
	if md.Language != "Go" {
		t.Errorf("language = %q, want Go", md.Language)
	}
	if want := int64(12) << 10; md.SizeBytes != want {
		t.Errorf("size bytes = %d, want %d (KB must be shifted to bytes)", md.SizeBytes, want)
	}

	reqs := requests()
	if len(reqs) != 1 {
		t.Fatalf("requests = %d, want exactly 1", len(reqs))
	}
	if reqs[0].Method != http.MethodGet {
		t.Errorf("method = %s, want GET", reqs[0].Method)
	}
	if reqs[0].Auth != "Bearer "+discoveryTestToken {
		t.Errorf("authorization = %q, want the Bearer PAT header", reqs[0].Auth)
	}
	if want := discoveryHostOf(srv); reqs[0].Host != want {
		t.Errorf("request host = %q, want %q (Repo.APIBase is the API root)", reqs[0].Host, want)
	}
	if reqs[0].Path != "/repos/acme/widgets" {
		t.Errorf("path = %q, want /repos/acme/widgets", reqs[0].Path)
	}
}

// TestDiscoveryForgeGitHubListsBranchesPullRequestsAndChecks pins the three
// read-only collection paths and the normalised result shapes.
func TestDiscoveryForgeGitHubListsBranchesPullRequestsAndChecks(t *testing.T) {
	const ref = "abc123"
	srv, requests := discoveryRecordingServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/repos/acme/widgets/branches":
			if !strings.Contains(r.URL.RawQuery, "per_page=100") {
				http.Error(w, `{"message":"missing per_page"}`, http.StatusBadRequest)
				return
			}
			fmt.Fprint(w, `[
				{"name": "main", "commit": {"sha": "sha-main"}},
				{"name": "dev", "commit": {"sha": "sha-dev"}}
			]`)
		case "/repos/acme/widgets/pulls":
			if !strings.Contains(r.URL.RawQuery, "state=open") ||
				!strings.Contains(r.URL.RawQuery, "per_page=100") {
				http.Error(w, `{"message":"missing state or per_page"}`, http.StatusBadRequest)
				return
			}
			fmt.Fprint(w, `[{
				"number": 7,
				"state": "open",
				"title": "Add widgets",
				"head": {"sha": "sha-head"}
			}]`)
		case "/repos/acme/widgets/commits/" + ref + "/check-runs":
			if !strings.Contains(r.URL.RawQuery, "per_page=100") {
				http.Error(w, `{"message":"missing per_page"}`, http.StatusBadRequest)
				return
			}
			fmt.Fprint(w, `{"check_runs": [{
				"id": 9,
				"name": "build",
				"conclusion": "failure",
				"status": "completed",
				"html_url": "https://github.com/acme/widgets/runs/9",
				"details_url": "https://github.com/acme/widgets/actions/runs/77/job/42",
				"app": {"id": 15368}
			}]}`)
		case "/repos/acme/widgets/commits/" + ref + "/status":
			fmt.Fprint(w, `{"state":"success","statuses":[]}`)
		default:
			http.Error(w, `{"message":"unexpected path"}`, http.StatusNotFound)
		}
	})

	repo := discoveryTestRepo("github", srv.URL)
	cred := discoveryTestCredential()
	forge, err := DiscoveryForgeFor(repo, srv.Client())
	if err != nil {
		t.Fatalf("DiscoveryForgeFor(github) error: %v", err)
	}
	ctx := context.Background()

	branches, err := forge.Branches(ctx, repo, cred)
	if err != nil {
		t.Fatalf("Branches error: %v", err)
	}
	wantBranches := []DiscoveryBranch{{Name: "main", SHA: "sha-main"}, {Name: "dev", SHA: "sha-dev"}}
	if !reflect.DeepEqual(branches, wantBranches) {
		t.Errorf("branches = %v, want %v", branches, wantBranches)
	}

	prs, err := forge.PullRequests(ctx, repo, cred)
	if err != nil {
		t.Fatalf("PullRequests error: %v", err)
	}
	wantPRs := []DiscoveryPullRequest{{Number: 7, State: "open", Title: "Add widgets", HeadSHA: "sha-head"}}
	if !reflect.DeepEqual(prs, wantPRs) {
		t.Errorf("pull requests = %v, want %v", prs, wantPRs)
	}

	checks, err := forge.Checks(ctx, repo, ref, cred)
	if err != nil {
		t.Fatalf("Checks error: %v", err)
	}
	wantChecks := []DiscoveryCheck{{
		Name:       "build",
		Conclusion: "failure",
		Status:     "completed",
		URL:        "https://github.com/acme/widgets/runs/9",
		AppID:      15368,
		JobID:      42,
	}}
	if !reflect.DeepEqual(checks, wantChecks) {
		t.Errorf("checks = %v, want %v", checks, wantChecks)
	}

	reqs := requests()
	if len(reqs) != 4 {
		t.Fatalf("requests = %d, want branches, pulls, check-runs and commit status: %v", len(reqs), reqs)
	}
	for _, r := range reqs {
		if r.Auth != "Bearer "+discoveryTestToken {
			t.Errorf("%s authorization = %q, want the Bearer PAT header", r.Path, r.Auth)
		}
		if r.Method != http.MethodGet {
			t.Errorf("%s method = %s, want GET", r.Path, r.Method)
		}
	}
}

// TestDiscoveryForgeGitHubReadsCheckLog reuses the Actions job/log shape and
// requires the returned content to be bounded by checkLogLimit.
func TestDiscoveryForgeGitHubReadsCheckLog(t *testing.T) {
	const marker = "FAILURE: widget exploded"
	srv, requests := discoveryRecordingServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/acme/widgets/actions/jobs/42/logs" {
			http.Error(w, `{"message":"unexpected path"}`, http.StatusNotFound)
			return
		}
		fmt.Fprint(w, strings.Repeat("x", checkLogLimit)+"\n"+marker+"\n")
	})

	repo := discoveryTestRepo("github", srv.URL)
	cred := discoveryTestCredential()
	forge, err := DiscoveryForgeFor(repo, srv.Client())
	if err != nil {
		t.Fatalf("DiscoveryForgeFor(github) error: %v", err)
	}
	ctx := context.Background()

	log, err := forge.CheckLogs(ctx, repo, DiscoveryCheck{Name: "build", JobID: 42}, cred)
	if err != nil {
		t.Fatalf("CheckLogs error: %v", err)
	}
	if len(log) > checkLogLimit {
		t.Errorf("log length = %d, want at most %d", len(log), checkLogLimit)
	}
	if !strings.HasSuffix(log, marker+"\n") {
		t.Errorf("log does not end with the failure tail: %q", tailOfLog(log))
	}

	reqs := requests()
	if len(reqs) != 1 {
		t.Fatalf("requests = %d, want 1", len(reqs))
	}
	if reqs[0].Auth != "Bearer "+discoveryTestToken {
		t.Errorf("authorization = %q, want the Bearer PAT header", reqs[0].Auth)
	}

	// A check from another app has no Actions job behind it. The probe must not
	// invent a request for it; the check's identity and link are what can
	// honestly be reported.
	summary, err := forge.CheckLogs(ctx, repo,
		DiscoveryCheck{Name: "lint", URL: "https://ci.example.com/lint/9"}, cred)
	if err != nil {
		t.Fatalf("CheckLogs without a job error: %v", err)
	}
	if !strings.Contains(summary, "lint") || !strings.Contains(summary, "https://ci.example.com/lint/9") {
		t.Errorf("summary = %q, want the check name and link", summary)
	}
	if got := len(requests()); got != 1 {
		t.Errorf("requests = %d after a jobless check, want no extra call", got)
	}
}

// tailOfLog shortens a long log for a readable failure message.
func tailOfLog(log string) string {
	const max = 120
	if len(log) <= max {
		return log
	}
	return "..." + log[len(log)-max:]
}

// TestDiscoveryForgeGiteaUsesTokenAuthAndVersionedBase pins the Gitea auth
// header, the versioned APIBase as the root, and Gitea's status-context check
// identity with the newest status per context winning.
func TestDiscoveryForgeGiteaUsesTokenAuthAndVersionedBase(t *testing.T) {
	const ref = "abc123"
	apiBase := ""
	srv, requests := discoveryRecordingServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		base := "/api/v1/repos/acme/widgets"
		switch r.URL.Path {
		case base:
			fmt.Fprint(w, `{
				"default_branch": "main",
				"private": false,
				"description": "Widgets, self-hosted",
				"size": 3
			}`)
		case base + "/branches":
			fmt.Fprint(w, `[
				{"name": "main", "commit": {"id": "sha-main"}},
				{"name": "dev", "commit": {"id": "sha-dev"}}
			]`)
		case base + "/pulls":
			if !strings.Contains(r.URL.RawQuery, "state=open") {
				http.Error(w, `{"message":"missing state"}`, http.StatusBadRequest)
				return
			}
			fmt.Fprint(w, `[{
				"number": 4,
				"index": 4,
				"state": "open",
				"title": "Fix widget",
				"head": {"sha": "sha-head"}
			}]`)
		case base + "/commits/" + ref + "/statuses":
			fmt.Fprint(w, `[
				{"id": 5, "status": "failure", "context": "ci/build", "description": "build failed", "target_url": "https://ci.example.com/build/5"},
				{"id": 3, "status": "success", "context": "ci/build", "description": "build passed", "target_url": "https://ci.example.com/build/3"},
				{"id": 4, "status": "pending", "context": "ci/test", "description": "waiting for runner", "target_url": "https://ci.example.com/test/4"}
			]`)
		default:
			http.Error(w, `{"message":"unexpected path"}`, http.StatusNotFound)
		}
	})
	apiBase = srv.URL + "/api/v1"

	repo := discoveryTestRepo("gitea", apiBase)
	cred := discoveryTestCredential()
	forge, err := DiscoveryForgeFor(repo, srv.Client())
	if err != nil {
		t.Fatalf("DiscoveryForgeFor(gitea) error: %v", err)
	}
	ctx := context.Background()

	md, err := forge.RepositoryMetadata(ctx, repo, cred)
	if err != nil {
		t.Fatalf("RepositoryMetadata error: %v", err)
	}
	if md.DefaultBranch != "main" {
		t.Errorf("default branch = %q, want main", md.DefaultBranch)
	}
	if md.Visibility != "public" {
		t.Errorf("visibility = %q, want public", md.Visibility)
	}
	if md.Description != "Widgets, self-hosted" {
		t.Errorf("description = %q", md.Description)
	}
	if want := int64(3) << 10; md.SizeBytes != want {
		t.Errorf("size bytes = %d, want %d", md.SizeBytes, want)
	}
	// Gitea exposes neither topics nor a dominant language on the repository
	// object: the adapter omits those facts rather than reporting them absent.
	if len(md.Topics) != 0 {
		t.Errorf("topics = %v, want none (Gitea has no topics fact to report)", md.Topics)
	}
	if md.Language != "" {
		t.Errorf("language = %q, want empty (Gitea has no language fact to report)", md.Language)
	}

	branches, err := forge.Branches(ctx, repo, cred)
	if err != nil {
		t.Fatalf("Branches error: %v", err)
	}
	wantBranches := []DiscoveryBranch{{Name: "main", SHA: "sha-main"}, {Name: "dev", SHA: "sha-dev"}}
	if !reflect.DeepEqual(branches, wantBranches) {
		t.Errorf("branches = %v, want %v", branches, wantBranches)
	}

	prs, err := forge.PullRequests(ctx, repo, cred)
	if err != nil {
		t.Fatalf("PullRequests error: %v", err)
	}
	wantPRs := []DiscoveryPullRequest{{Number: 4, State: "open", Title: "Fix widget", HeadSHA: "sha-head"}}
	if !reflect.DeepEqual(prs, wantPRs) {
		t.Errorf("pull requests = %v, want %v", prs, wantPRs)
	}

	checks, err := forge.Checks(ctx, repo, ref, cred)
	if err != nil {
		t.Fatalf("Checks error: %v", err)
	}
	wantChecks := []DiscoveryCheck{
		{
			Name:       "ci/build",
			Conclusion: "failure",
			Status:     "failure",
			URL:        "https://ci.example.com/build/5",
			Context:    "ci/build",
		},
		{
			Name:       "ci/test",
			Conclusion: "pending",
			Status:     "pending",
			URL:        "https://ci.example.com/test/4",
			Context:    "ci/test",
		},
	}
	if !reflect.DeepEqual(checks, wantChecks) {
		t.Errorf("checks = %v, want %v (newest status per context wins)", checks, wantChecks)
	}

	// A commit status has no generic log endpoint, so the status identity and
	// its link are the whole artefact; it must stay bounded and must not make
	// another call.
	log, err := forge.CheckLogs(ctx, repo, checks[0], cred)
	if err != nil {
		t.Fatalf("CheckLogs error: %v", err)
	}
	if len(log) > checkLogLimit {
		t.Errorf("log length = %d, want at most %d", len(log), checkLogLimit)
	}
	if !strings.Contains(log, "ci/build") || !strings.Contains(log, "https://ci.example.com/build/5") {
		t.Errorf("log = %q, want the status context and target URL", log)
	}

	reqs := requests()
	if len(reqs) != 4 {
		t.Fatalf("requests = %d, want one per probe: %v", len(reqs), reqs)
	}
	wantPaths := []string{
		"/api/v1/repos/acme/widgets",
		"/api/v1/repos/acme/widgets/branches",
		"/api/v1/repos/acme/widgets/pulls",
		"/api/v1/repos/acme/widgets/commits/" + ref + "/statuses",
	}
	for i, r := range reqs {
		if r.Path != wantPaths[i] {
			t.Errorf("request %d path = %q, want %q", i, r.Path, wantPaths[i])
		}
		if r.Auth != "token "+discoveryTestToken {
			t.Errorf("%s authorization = %q, want the Gitea token header", r.Path, r.Auth)
		}
		if r.Host != discoveryHostOf(srv) {
			t.Errorf("%s host = %q, want %q (Repo.APIBase is the API root)", r.Path, r.Host, discoveryHostOf(srv))
		}
	}
}

// discoveryCountingTransport records whether a client made any request at all.
type discoveryCountingTransport struct {
	calls atomic.Int64
}

func (c *discoveryCountingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	c.calls.Add(1)
	return nil, errors.New("discovery probe must not make an HTTP request")
}

// TestDiscoveryForgeGiteaWithoutAPIBaseIsUnconfigured requires the missing
// self-hosted base to be reported as unconfigured, with a detail naming the
// setting, and to make no HTTP request.
func TestDiscoveryForgeGiteaWithoutAPIBaseIsUnconfigured(t *testing.T) {
	transport := &discoveryCountingTransport{}
	client := &http.Client{Transport: transport}

	repo := discoveryTestRepo("gitea", "")
	cred := discoveryTestCredential()

	forge, err := DiscoveryForgeFor(repo, client)
	if err != nil {
		t.Fatalf("DiscoveryForgeFor(gitea) error: %v", err)
	}

	ctx := context.Background()
	probes := []struct {
		name string
		call func() error
	}{
		{"RepositoryMetadata", func() error {
			_, err := forge.RepositoryMetadata(ctx, repo, cred)
			return err
		}},
		{"Branches", func() error {
			_, err := forge.Branches(ctx, repo, cred)
			return err
		}},
		{"PullRequests", func() error {
			_, err := forge.PullRequests(ctx, repo, cred)
			return err
		}},
		{"Checks", func() error {
			_, err := forge.Checks(ctx, repo, "abc123", cred)
			return err
		}},
		{"CheckLogs", func() error {
			_, err := forge.CheckLogs(ctx, repo, DiscoveryCheck{Name: "ci/build"}, cred)
			return err
		}},
	}

	for _, probe := range probes {
		t.Run(probe.name, func(t *testing.T) {
			err := probe.call()
			if err == nil {
				t.Fatalf("%s returned no error for a Gitea instance with no API base", probe.name)
			}
			if !errors.Is(err, errDiscoveryUnconfigured) {
				t.Fatalf("%s error = %v, want errDiscoveryUnconfigured", probe.name, err)
			}
			if !strings.Contains(err.Error(), "apiBase") {
				t.Errorf("%s detail = %q, want it to name the apiBase setting", probe.name, err)
			}
			if !strings.Contains(err.Error(), "API base") {
				t.Errorf("%s detail = %q, want it to name the instance API base", probe.name, err)
			}
		})
	}

	if got := transport.calls.Load(); got != 0 {
		t.Fatalf("HTTP requests = %d, want 0 for a missing API base", got)
	}
}

// TestDiscoveryForgeNeverEchoesCredential fails every endpoint and requires the
// PAT to stay out of every error, result, and request URL.
func TestDiscoveryForgeNeverEchoesCredential(t *testing.T) {
	srv, requests := discoveryRecordingServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		// A forge error body that does not repeat the credential: the probe
		// must not add it either.
		fmt.Fprint(w, `{"message":"Bad credentials"}`)
	})

	for _, tc := range []struct {
		forge   string
		apiBase string
	}{
		{forge: "github", apiBase: srv.URL},
		{forge: "gitea", apiBase: srv.URL + "/api/v1"},
	} {
		t.Run(tc.forge, func(t *testing.T) {
			repo := discoveryTestRepo(tc.forge, tc.apiBase)
			cred := discoveryTestCredential()
			forge, err := DiscoveryForgeFor(repo, srv.Client())
			if err != nil {
				t.Fatalf("DiscoveryForgeFor(%s) error: %v", tc.forge, err)
			}
			ctx := context.Background()

			md, err := forge.RepositoryMetadata(ctx, repo, cred)
			if err == nil {
				t.Fatalf("RepositoryMetadata succeeded against a 401 endpoint: %+v", md)
			}
			assertNoCredential(t, "RepositoryMetadata error", err.Error())

			branches, err := forge.Branches(ctx, repo, cred)
			if err == nil {
				t.Fatalf("Branches succeeded against a 401 endpoint: %v", branches)
			}
			assertNoCredential(t, "Branches error", err.Error())

			prs, err := forge.PullRequests(ctx, repo, cred)
			if err == nil {
				t.Fatalf("PullRequests succeeded against a 401 endpoint: %v", prs)
			}
			assertNoCredential(t, "PullRequests error", err.Error())

			checks, err := forge.Checks(ctx, repo, "abc123", cred)
			if err == nil {
				t.Fatalf("Checks succeeded against a 401 endpoint: %v", checks)
			}
			assertNoCredential(t, "Checks error", err.Error())

			log, err := forge.CheckLogs(ctx, repo, DiscoveryCheck{Name: "build", JobID: 42}, cred)
			if err == nil && tc.forge == "github" {
				t.Fatalf("CheckLogs succeeded against a 401 endpoint: %q", log)
			}
			if err != nil {
				assertNoCredential(t, "CheckLogs error", err.Error())
			}
			assertNoCredential(t, "CheckLogs result", log)

			// A repository URL that cannot be parsed fails before any call; the
			// resulting error must not fall back to quoting the credential.
			broken := repo
			broken.URL = ""
			if _, err := forge.Branches(ctx, broken, cred); err == nil {
				t.Fatalf("Branches accepted an unparseable remote URL")
			} else {
				assertNoCredential(t, "unparseable remote error", err.Error())
			}

			for _, r := range requests() {
				assertNoCredential(t, "request path "+r.Path, r.Path)
				assertNoCredential(t, "request query "+r.Path, r.Query)
			}
		})
	}
}

// assertNoCredential fails when a probe leaked the PAT literal.
func assertNoCredential(t *testing.T, what, got string) {
	t.Helper()
	if strings.Contains(got, discoveryTestToken) {
		t.Fatalf("%s echoes the credential: %s", what, got)
	}
}

// discoveryGitHubBranchPage renders n GitHub branch objects starting at id.
func discoveryGitHubBranchPage(start, n int) string {
	var b strings.Builder
	b.WriteString("[")
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"name":"branch-%03d","commit":{"sha":"sha-%03d"}}`, start+i, start+i)
	}
	b.WriteString("]")
	return b.String()
}

// discoveryGiteaBranchPage renders n Gitea branch objects starting at id.
func discoveryGiteaBranchPage(start, n int) string {
	var b strings.Builder
	b.WriteString("[")
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"name":"branch-%03d","commit":{"id":"sha-%03d"}}`, start+i, start+i)
	}
	b.WriteString("]")
	return b.String()
}

// discoveryGitHubCheckRunPage renders n GitHub check-run objects.
func discoveryGitHubCheckRunPage(start, n int) string {
	var b strings.Builder
	b.WriteString(`{"check_runs":[`)
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"id":%d,"name":"check-%03d","conclusion":"success","status":"completed"}`, start+i, start+i)
	}
	b.WriteString("]}")
	return b.String()
}

// discoveryGiteaStatusPage renders n distinct-context commit statuses.
func discoveryGiteaStatusPage(start, n int) string {
	var b strings.Builder
	b.WriteString("[")
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b,
			`{"id":%d,"status":"success","context":"ci/%03d","target_url":"https://ci.example.com/%03d"}`,
			start+i, start+i, start+i)
	}
	b.WriteString("]")
	return b.String()
}

// discoveryPageParam reads a forge page parameter, defaulting to page one.
func discoveryPageParam(raw string) int {
	page, err := strconv.Atoi(raw)
	if err != nil || page < 1 {
		return 1
	}
	return page
}

// TestDiscoveryForgeGitHubMergesCommitStatuses covers finding 1: a repo whose
// CI reports only commit statuses must yield checks, a repo with both must
// yield both, and the newest status per context must win.
func TestDiscoveryForgeGitHubMergesCommitStatuses(t *testing.T) {
	const ref = "abc123"

	t.Run("status-only repo yields status checks", func(t *testing.T) {
		srv, _ := discoveryRecordingServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/repos/acme/widgets/commits/" + ref + "/check-runs":
				fmt.Fprint(w, `{"check_runs":[]}`)
			case "/repos/acme/widgets/commits/" + ref + "/status":
				fmt.Fprint(w, `{"state":"failure","statuses":[
					{"id":7,"context":"ci/build","state":"failure","description":"build failed","target_url":"https://ci.example.com/build/7"}
				]}`)
			default:
				http.Error(w, `{"message":"unexpected path"}`, http.StatusNotFound)
			}
		})
		repo := discoveryTestRepo("github", srv.URL)
		forge, err := DiscoveryForgeFor(repo, srv.Client())
		if err != nil {
			t.Fatalf("DiscoveryForgeFor(github) error: %v", err)
		}
		checks, err := forge.Checks(context.Background(), repo, ref, discoveryTestCredential())
		if err != nil {
			t.Fatalf("Checks error: %v", err)
		}
		want := []DiscoveryCheck{{
			Name:       "ci/build",
			Conclusion: "failure",
			Status:     "failure",
			URL:        "https://ci.example.com/build/7",
			Context:    "ci/build",
		}}
		if !reflect.DeepEqual(checks, want) {
			t.Fatalf("checks = %v, want %v", checks, want)
		}
	})

	t.Run("check-runs and statuses are both reported", func(t *testing.T) {
		srv, _ := discoveryRecordingServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/repos/acme/widgets/commits/" + ref + "/check-runs":
				fmt.Fprint(w, `{"check_runs":[{"id":9,"name":"build","conclusion":"success",
					"status":"completed","html_url":"https://github.com/acme/widgets/runs/9",
					"app":{"id":15368}}]}`)
			case "/repos/acme/widgets/commits/" + ref + "/status":
				fmt.Fprint(w, `{"state":"failure","statuses":[
					{"id":4,"context":"ci/lint","state":"failure","description":"lint failed","target_url":"https://ci.example.com/lint/4"}
				]}`)
			default:
				http.Error(w, `{"message":"unexpected path"}`, http.StatusNotFound)
			}
		})
		repo := discoveryTestRepo("github", srv.URL)
		forge, err := DiscoveryForgeFor(repo, srv.Client())
		if err != nil {
			t.Fatalf("DiscoveryForgeFor(github) error: %v", err)
		}
		checks, err := forge.Checks(context.Background(), repo, ref, discoveryTestCredential())
		if err != nil {
			t.Fatalf("Checks error: %v", err)
		}
		want := []DiscoveryCheck{
			{
				Name:       "build",
				Conclusion: "success",
				Status:     "completed",
				URL:        "https://github.com/acme/widgets/runs/9",
				AppID:      15368,
			},
			{
				Name:       "ci/lint",
				Conclusion: "failure",
				Status:     "failure",
				URL:        "https://ci.example.com/lint/4",
				Context:    "ci/lint",
			},
		}
		if !reflect.DeepEqual(checks, want) {
			t.Fatalf("checks = %v, want %v", checks, want)
		}
	})

	t.Run("newest status per context wins", func(t *testing.T) {
		srv, _ := discoveryRecordingServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/repos/acme/widgets/commits/" + ref + "/check-runs":
				fmt.Fprint(w, `{"check_runs":[]}`)
			case "/repos/acme/widgets/commits/" + ref + "/status":
				// The older success is listed after the newer failure; the
				// status id, not listing order, decides.
				fmt.Fprint(w, `{"state":"failure","statuses":[
					{"id":7,"context":"ci/build","state":"failure","target_url":"https://ci.example.com/build/7"},
					{"id":3,"context":"ci/build","state":"success","target_url":"https://ci.example.com/build/3"}
				]}`)
			default:
				http.Error(w, `{"message":"unexpected path"}`, http.StatusNotFound)
			}
		})
		repo := discoveryTestRepo("github", srv.URL)
		forge, err := DiscoveryForgeFor(repo, srv.Client())
		if err != nil {
			t.Fatalf("DiscoveryForgeFor(github) error: %v", err)
		}
		checks, err := forge.Checks(context.Background(), repo, ref, discoveryTestCredential())
		if err != nil {
			t.Fatalf("Checks error: %v", err)
		}
		want := []DiscoveryCheck{{
			Name:       "ci/build",
			Conclusion: "failure",
			Status:     "failure",
			URL:        "https://ci.example.com/build/7",
			Context:    "ci/build",
		}}
		if !reflect.DeepEqual(checks, want) {
			t.Fatalf("checks = %v, want %v (newest status by id wins)", checks, want)
		}
	})

	t.Run("neither check-runs nor statuses reports no checks", func(t *testing.T) {
		srv, _ := discoveryRecordingServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/repos/acme/widgets/commits/" + ref + "/check-runs":
				fmt.Fprint(w, `{"check_runs":[]}`)
			case "/repos/acme/widgets/commits/" + ref + "/status":
				fmt.Fprint(w, `{"state":"pending","statuses":[]}`)
			default:
				http.Error(w, `{"message":"unexpected path"}`, http.StatusNotFound)
			}
		})
		repo := discoveryTestRepo("github", srv.URL)
		forge, err := DiscoveryForgeFor(repo, srv.Client())
		if err != nil {
			t.Fatalf("DiscoveryForgeFor(github) error: %v", err)
		}
		checks, err := forge.Checks(context.Background(), repo, ref, discoveryTestCredential())
		if err != nil {
			t.Fatalf("Checks error: %v", err)
		}
		if len(checks) != 0 {
			t.Fatalf("checks = %v, want none (genuine absence)", checks)
		}
	})
}

// TestDiscoveryForgeGitHubPaginatesCollections covers finding 4 for GitHub: a
// full page followed by a short page is complete, while a full page every time
// is truncated.
func TestDiscoveryForgeGitHubPaginatesCollections(t *testing.T) {
	const ref = "abc123"

	t.Run("full then short page is not truncated", func(t *testing.T) {
		srv, _ := discoveryRecordingServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			page := discoveryPageParam(r.URL.Query().Get("page"))
			switch r.URL.Path {
			case "/repos/acme/widgets/branches":
				if page == 1 {
					fmt.Fprint(w, discoveryGitHubBranchPage(0, 100))
				} else {
					fmt.Fprint(w, discoveryGitHubBranchPage(100, 2))
				}
			case "/repos/acme/widgets/pulls":
				fmt.Fprint(w, `[]`)
			case "/repos/acme/widgets/commits/" + ref + "/check-runs":
				if page == 1 {
					fmt.Fprint(w, discoveryGitHubCheckRunPage(0, 100))
				} else {
					fmt.Fprint(w, discoveryGitHubCheckRunPage(100, 1))
				}
			case "/repos/acme/widgets/commits/" + ref + "/status":
				fmt.Fprint(w, `{"state":"success","statuses":[]}`)
			default:
				http.Error(w, `{"message":"unexpected path"}`, http.StatusNotFound)
			}
		})
		repo := discoveryTestRepo("github", srv.URL)
		forge, err := DiscoveryForgeFor(repo, srv.Client())
		if err != nil {
			t.Fatalf("DiscoveryForgeFor(github) error: %v", err)
		}
		ctx := context.Background()
		branches, err := forge.Branches(ctx, repo, discoveryTestCredential())
		if err != nil {
			t.Fatalf("Branches error: %v", err)
		}
		if len(branches) != 102 {
			t.Fatalf("branches = %d, want 102 (page 1 full, page 2 short)", len(branches))
		}
		checks, err := forge.Checks(ctx, repo, ref, discoveryTestCredential())
		if err != nil {
			t.Fatalf("Checks error: %v", err)
		}
		if len(checks) != 101 {
			t.Fatalf("checks = %d, want 101 (page 1 full, page 2 short)", len(checks))
		}
	})

	t.Run("full page every time is truncated", func(t *testing.T) {
		srv, _ := discoveryRecordingServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/repos/acme/widgets/branches":
				page := discoveryPageParam(r.URL.Query().Get("page"))
				fmt.Fprint(w, discoveryGitHubBranchPage(page*100, 100))
			case "/repos/acme/widgets/pulls":
				fmt.Fprint(w, `[]`)
			case "/repos/acme/widgets/commits/" + ref + "/check-runs":
				page := discoveryPageParam(r.URL.Query().Get("page"))
				fmt.Fprint(w, discoveryGitHubCheckRunPage(page*100, 100))
			case "/repos/acme/widgets/commits/" + ref + "/status":
				fmt.Fprint(w, `{"state":"success","statuses":[]}`)
			default:
				http.Error(w, `{"message":"unexpected path"}`, http.StatusNotFound)
			}
		})
		repo := discoveryTestRepo("github", srv.URL)
		forge, err := DiscoveryForgeFor(repo, srv.Client())
		if err != nil {
			t.Fatalf("DiscoveryForgeFor(github) error: %v", err)
		}
		ctx := context.Background()
		branches, err := forge.Branches(ctx, repo, discoveryTestCredential())
		if !errors.Is(err, errDiscoveryTruncated) {
			t.Fatalf("Branches error = %v, want errDiscoveryTruncated", err)
		}
		if len(branches) != discoveryForgeMaxPages*discoveryForgePageSize {
			t.Fatalf("branches = %d, want the %d-page bound", len(branches), discoveryForgeMaxPages)
		}
		checks, err := forge.Checks(ctx, repo, ref, discoveryTestCredential())
		if !errors.Is(err, errDiscoveryTruncated) {
			t.Fatalf("Checks error = %v, want errDiscoveryTruncated", err)
		}
		if len(checks) != discoveryForgeMaxPages*discoveryForgePageSize {
			t.Fatalf("checks = %d, want the %d-page bound", len(checks), discoveryForgeMaxPages)
		}
	})
}

// TestDiscoveryForgeGiteaPaginatesCollections covers finding 4 for Gitea.
func TestDiscoveryForgeGiteaPaginatesCollections(t *testing.T) {
	const ref = "abc123"

	t.Run("full then short page is not truncated", func(t *testing.T) {
		var apiBase string
		srv, _ := discoveryRecordingServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			page := discoveryPageParam(r.URL.Query().Get("page"))
			base := "/api/v1/repos/acme/widgets"
			switch r.URL.Path {
			case base + "/branches":
				if page == 1 {
					fmt.Fprint(w, discoveryGiteaBranchPage(0, 100))
				} else {
					fmt.Fprint(w, discoveryGiteaBranchPage(100, 2))
				}
			case base + "/pulls":
				fmt.Fprint(w, `[]`)
			case base + "/commits/" + ref + "/statuses":
				if page == 1 {
					fmt.Fprint(w, discoveryGiteaStatusPage(0, 100))
				} else {
					fmt.Fprint(w, discoveryGiteaStatusPage(100, 1))
				}
			default:
				http.Error(w, `{"message":"unexpected path"}`, http.StatusNotFound)
			}
		})
		apiBase = srv.URL + "/api/v1"
		repo := discoveryTestRepo("gitea", apiBase)
		forge, err := DiscoveryForgeFor(repo, srv.Client())
		if err != nil {
			t.Fatalf("DiscoveryForgeFor(gitea) error: %v", err)
		}
		ctx := context.Background()
		branches, err := forge.Branches(ctx, repo, discoveryTestCredential())
		if err != nil {
			t.Fatalf("Branches error: %v", err)
		}
		if len(branches) != 102 {
			t.Fatalf("branches = %d, want 102", len(branches))
		}
		checks, err := forge.Checks(ctx, repo, ref, discoveryTestCredential())
		if err != nil {
			t.Fatalf("Checks error: %v", err)
		}
		if len(checks) != 101 {
			t.Fatalf("checks = %d, want 101", len(checks))
		}
	})

	t.Run("full page every time is truncated", func(t *testing.T) {
		var apiBase string
		srv, _ := discoveryRecordingServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			base := "/api/v1/repos/acme/widgets"
			switch r.URL.Path {
			case base + "/branches":
				page := discoveryPageParam(r.URL.Query().Get("page"))
				fmt.Fprint(w, discoveryGiteaBranchPage(page*100, 100))
			case base + "/pulls":
				fmt.Fprint(w, `[]`)
			case base + "/commits/" + ref + "/statuses":
				page := discoveryPageParam(r.URL.Query().Get("page"))
				fmt.Fprint(w, discoveryGiteaStatusPage(page*100, 100))
			default:
				http.Error(w, `{"message":"unexpected path"}`, http.StatusNotFound)
			}
		})
		apiBase = srv.URL + "/api/v1"
		repo := discoveryTestRepo("gitea", apiBase)
		forge, err := DiscoveryForgeFor(repo, srv.Client())
		if err != nil {
			t.Fatalf("DiscoveryForgeFor(gitea) error: %v", err)
		}
		ctx := context.Background()
		branches, err := forge.Branches(ctx, repo, discoveryTestCredential())
		if !errors.Is(err, errDiscoveryTruncated) {
			t.Fatalf("Branches error = %v, want errDiscoveryTruncated", err)
		}
		if len(branches) != discoveryForgeMaxPages*discoveryForgePageSize {
			t.Fatalf("branches = %d, want the %d-page bound", len(branches), discoveryForgeMaxPages)
		}
		checks, err := forge.Checks(ctx, repo, ref, discoveryTestCredential())
		if !errors.Is(err, errDiscoveryTruncated) {
			t.Fatalf("Checks error = %v, want errDiscoveryTruncated", err)
		}
		if len(checks) != discoveryForgeMaxPages*discoveryForgePageSize {
			t.Fatalf("checks = %d, want the %d-page bound", len(checks), discoveryForgeMaxPages)
		}
	})
}
