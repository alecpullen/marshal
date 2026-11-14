package bridge

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

// TestPreviewForgeGitHubReadsEnvironments pins the environments read: the
// object shape GitHub serves (not a bare array), and the provider's own name,
// id, browser URL, protection-rule count and branch policy, carried verbatim.
func TestPreviewForgeGitHubReadsEnvironments(t *testing.T) {
	srv, requests := discoveryRecordingServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/acme/widgets/environments" {
			http.Error(w, `{"message":"unexpected path"}`, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{
			"total_count": 2,
			"environments": [
				{
					"id": 1234567,
					"name": "production",
					"html_url": "https://github.com/acme/widgets/deployments/activity_log?environments_filter=production",
					"protection_rules": [
						{"id": 1, "type": "wait_timer"},
						{"id": 2, "type": "required_reviewers"}
					],
					"deployment_branch_policy": {
						"protected_branches": true,
						"custom_branch_policies": false
					}
				},
				{
					"id": 7654321,
					"name": "staging",
					"html_url": "https://github.com/acme/widgets/deployments/activity_log?environments_filter=staging"
				}
			]
		}`)
	})

	repo := discoveryTestRepo("github", srv.URL)
	cred := discoveryTestCredential()
	forge, err := PreviewForgeFor(repo, srv.Client())
	if err != nil {
		t.Fatalf("PreviewForgeFor(github) error: %v", err)
	}

	environments, err := forge.Environments(context.Background(), repo, cred)
	if err != nil {
		t.Fatalf("Environments error: %v", err)
	}
	if len(environments) != 2 {
		t.Fatalf("environments = %d, want 2", len(environments))
	}
	want := PreviewEnvironment{
		Name:                "production",
		ID:                  "1234567",
		HTMLURL:             "https://github.com/acme/widgets/deployments/activity_log?environments_filter=production",
		BranchPolicy:        "protected_branches",
		ProtectionRuleCount: 2,
	}
	if got := environments[0]; got != want {
		t.Errorf("environment[0] = %+v, want %+v", got, want)
	}
	// A policy the forge does not report stays empty rather than guessed.
	if got := environments[1]; got.BranchPolicy != "" || got.ProtectionRuleCount != 0 {
		t.Errorf("environment[1] = %+v, want an empty branch policy and no protection rules", got)
	}

	recorded := requests()
	if len(recorded) == 0 {
		t.Fatal("no request was recorded")
	}
	if got, want := recorded[0].Auth, "Bearer "+discoveryTestToken; got != want {
		t.Errorf("Authorization = %q, want %q", got, want)
	}
}

// TestPreviewForgeGitHubReadsDeployments pins the deployments read: the bare
// array shape, the provider's own fields, and that an empty filter dimension
// is omitted from the query rather than sent as an empty value.
func TestPreviewForgeGitHubReadsDeployments(t *testing.T) {
	srv, requests := discoveryRecordingServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/acme/widgets/deployments" {
			http.Error(w, `{"message":"unexpected path"}`, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `[{
			"id": 99,
			"sha": "8e4c1b2f0e1d2c3b4a5968778695a4b3c2d1e0f9",
			"ref": "feature/x",
			"task": "deploy",
			"environment": "production",
			"original_environment": "prod",
			"description": "the deployment's own description",
			"created_at": "2026-11-12T09:00:00Z",
			"updated_at": "2026-11-12T09:01:12Z",
			"transient_environment": true,
			"production_environment": true
		}]`)
	})

	repo := discoveryTestRepo("github", srv.URL)
	cred := discoveryTestCredential()
	forge, err := PreviewForgeFor(repo, srv.Client())
	if err != nil {
		t.Fatalf("PreviewForgeFor(github) error: %v", err)
	}

	deployments, err := forge.Deployments(context.Background(), repo, PreviewFilter{Ref: "feature/x"}, cred)
	if err != nil {
		t.Fatalf("Deployments error: %v", err)
	}
	if len(deployments) != 1 {
		t.Fatalf("deployments = %d, want 1", len(deployments))
	}
	want := PreviewDeployment{
		ID:                  "99",
		Environment:         "production",
		OriginalEnvironment: "prod",
		Ref:                 "feature/x",
		SHA:                 "8e4c1b2f0e1d2c3b4a5968778695a4b3c2d1e0f9",
		Task:                "deploy",
		Transient:           true,
		Production:          true,
		CreatedAt:           "2026-11-12T09:00:00Z",
		UpdatedAt:           "2026-11-12T09:01:12Z",
	}
	if got := deployments[0]; got != want {
		t.Errorf("deployment = %+v, want %+v (the deployment's own description is not the row's description)", got, want)
	}

	query, err := url.ParseQuery(requests()[0].Query)
	if err != nil {
		t.Fatalf("parse recorded query %q: %v", requests()[0].Query, err)
	}
	if got := query.Get("ref"); got != "feature/x" {
		t.Errorf("query ref = %q, want feature/x", got)
	}
	if _, ok := query["sha"]; ok {
		t.Errorf("query = %q, want no sha key: an empty dimension is omitted, never sent empty", requests()[0].Query)
	}

	// Both dimensions are applied conjunctively when both are given.
	if _, err := forge.Deployments(context.Background(), repo, PreviewFilter{Ref: "main", SHA: "abc123"}, cred); err != nil {
		t.Fatalf("Deployments with ref and sha: %v", err)
	}
	query, err = url.ParseQuery(requests()[1].Query)
	if err != nil {
		t.Fatalf("parse recorded query %q: %v", requests()[1].Query, err)
	}
	if query.Get("ref") != "main" || query.Get("sha") != "abc123" {
		t.Errorf("query = %q, want both ref and sha applied", requests()[1].Query)
	}

	// An empty filter carries no scope keys at all.
	if _, err := forge.Deployments(context.Background(), repo, PreviewFilter{}, cred); err != nil {
		t.Fatalf("Deployments unfiltered: %v", err)
	}
	query, err = url.ParseQuery(requests()[2].Query)
	if err != nil {
		t.Fatalf("parse recorded query %q: %v", requests()[2].Query, err)
	}
	if query.Get("ref") != "" && strings.Contains(requests()[2].Query, "ref=") {
		t.Errorf("query = %q, want no ref key for an empty filter", requests()[2].Query)
	}
	if _, ok := query["sha"]; ok {
		t.Errorf("query = %q, want no sha key for an empty filter", requests()[2].Query)
	}
}

// TestPreviewForgeGitHubReadsDeploymentStatuses pins the statuses read: the
// per-deployment path and the provider's own state, environment URL, target
// URL, log URL, description and timestamps.
func TestPreviewForgeGitHubReadsDeploymentStatuses(t *testing.T) {
	srv, requests := discoveryRecordingServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/acme/widgets/deployments/99/statuses" {
			http.Error(w, `{"message":"unexpected path"}`, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `[{
			"id": 4242,
			"state": "success",
			"environment_url": "https://preview.example.internal",
			"target_url": "https://ci.example/run/42",
			"log_url": "https://ci.example/run/42/log",
			"description": "deployed 8e4c1b2",
			"created_at": "2026-11-12T09:01:00Z",
			"updated_at": "2026-11-12T09:01:12Z"
		}]`)
	})

	repo := discoveryTestRepo("github", srv.URL)
	cred := discoveryTestCredential()
	forge, err := PreviewForgeFor(repo, srv.Client())
	if err != nil {
		t.Fatalf("PreviewForgeFor(github) error: %v", err)
	}

	statuses, err := forge.DeploymentStatuses(context.Background(), repo, 99, cred)
	if err != nil {
		t.Fatalf("DeploymentStatuses error: %v", err)
	}
	want := []PreviewDeploymentStatus{{
		ID:             "4242",
		State:          "success",
		EnvironmentURL: "https://preview.example.internal",
		TargetURL:      "https://ci.example/run/42",
		LogURL:         "https://ci.example/run/42/log",
		Description:    "deployed 8e4c1b2",
		CreatedAt:      "2026-11-12T09:01:00Z",
		UpdatedAt:      "2026-11-12T09:01:12Z",
	}}
	if len(statuses) != 1 || statuses[0] != want[0] {
		t.Errorf("statuses = %+v, want %+v", statuses, want)
	}
	if got, want := requests()[0].Path, "/repos/acme/widgets/deployments/99/statuses"; got != want {
		t.Errorf("status path = %q, want %q", got, want)
	}
}

// TestPreviewForgeGitHubTruncatesAtThePageBound pins that a full final page is
// truncation: the items read come back together with errDiscoveryTruncated, so
// the caller keeps them and records the bound instead of reporting absence.
func TestPreviewForgeGitHubTruncatesAtThePageBound(t *testing.T) {
	srv, _ := discoveryRecordingServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/acme/widgets/deployments" {
			http.Error(w, `{"message":"unexpected path"}`, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, previewGitHubDeploymentPage(1, discoveryForgePageSize))
	})

	repo := discoveryTestRepo("github", srv.URL)
	cred := discoveryTestCredential()
	forge, err := PreviewForgeFor(repo, srv.Client())
	if err != nil {
		t.Fatalf("PreviewForgeFor(github) error: %v", err)
	}

	deployments, err := forge.Deployments(context.Background(), repo, PreviewFilter{}, cred)
	if !errors.Is(err, errDiscoveryTruncated) {
		t.Fatalf("Deployments error = %v, want errDiscoveryTruncated", err)
	}
	if want := discoveryForgePageSize * discoveryForgeMaxPages; len(deployments) != want {
		t.Errorf("deployments = %d, want the %d items read before the bound", len(deployments), want)
	}
}

// TestPreviewForgeGiteaUsesTokenAuthAndVersionedBase pins the Gitea adapter's
// conventions: "token <pat>" auth against the instance's own API base, and the
// same resource paths as GitHub under that base. The instance serves no such
// resource, so it answers 404 and the adapter reports what it answered.
func TestPreviewForgeGiteaUsesTokenAuthAndVersionedBase(t *testing.T) {
	srv, requests := discoveryRecordingServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"not found"}`, http.StatusNotFound)
	})

	repo := discoveryTestRepo("gitea", srv.URL)
	cred := discoveryTestCredential()
	forge, err := PreviewForgeFor(repo, srv.Client())
	if err != nil {
		t.Fatalf("PreviewForgeFor(gitea) error: %v", err)
	}

	ctx := context.Background()
	if _, err := forge.Environments(ctx, repo, cred); err == nil {
		t.Error("Environments error = nil, want the instance's 404 reported")
	}
	if _, err := forge.Deployments(ctx, repo, PreviewFilter{}, cred); err == nil {
		t.Error("Deployments error = nil, want the instance's 404 reported")
	}
	if _, err := forge.DeploymentStatuses(ctx, repo, 7, cred); err == nil {
		t.Error("DeploymentStatuses error = nil, want the instance's 404 reported")
	}

	recorded := requests()
	wantPaths := []string{
		"/repos/acme/widgets/environments",
		"/repos/acme/widgets/deployments",
		"/repos/acme/widgets/deployments/7/statuses",
	}
	if len(recorded) != len(wantPaths) {
		t.Fatalf("requests = %d, want %d", len(recorded), len(wantPaths))
	}
	for i, want := range wantPaths {
		if got := recorded[i].Path; got != want {
			t.Errorf("request %d path = %q, want %q", i, got, want)
		}
		if got, want := recorded[i].Auth, "token "+discoveryTestToken; got != want {
			t.Errorf("request %d Authorization = %q, want %q", i, got, want)
		}
	}
}

// TestPreviewForgeGiteaWithoutAPIBaseIsUnconfigured pins the missing-setting
// path: with no apiBase the adapter reports errDiscoveryUnconfigured without
// building a request, because a request to an empty base would look like a
// transport failure rather than the configuration gap it is.
func TestPreviewForgeGiteaWithoutAPIBaseIsUnconfigured(t *testing.T) {
	srv, requests := discoveryRecordingServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	repo := discoveryTestRepo("gitea", "")
	cred := discoveryTestCredential()
	forge, err := PreviewForgeFor(repo, srv.Client())
	if err != nil {
		t.Fatalf("PreviewForgeFor(gitea) error: %v", err)
	}

	ctx := context.Background()
	envErr := func() error { _, err := forge.Environments(ctx, repo, cred); return err }()
	depErr := func() error { _, err := forge.Deployments(ctx, repo, PreviewFilter{}, cred); return err }()
	statusErr := func() error { _, err := forge.DeploymentStatuses(ctx, repo, 7, cred); return err }()
	for name, err := range map[string]error{
		"Environments": envErr, "Deployments": depErr, "DeploymentStatuses": statusErr,
	} {
		if !errors.Is(err, errDiscoveryUnconfigured) {
			t.Errorf("%s error = %v, want errDiscoveryUnconfigured", name, err)
		}
	}
	if got := len(requests()); got != 0 {
		t.Errorf("unconfigured adapter made %d requests, want none", got)
	}
}

// TestPreviewForgeIssuesOnlyGetRequests pins the read-only guarantee at the
// wire: every request either adapter issues is a GET.
func TestPreviewForgeIssuesOnlyGetRequests(t *testing.T) {
	serve := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/environments"):
			fmt.Fprint(w, `{"total_count":0,"environments":[]}`)
		case strings.HasSuffix(r.URL.Path, "/deployments"):
			fmt.Fprint(w, `[]`)
		case strings.HasSuffix(r.URL.Path, "/statuses"):
			fmt.Fprint(w, `[]`)
		default:
			http.Error(w, `{"message":"unexpected path"}`, http.StatusNotFound)
		}
	}
	srv, requests := discoveryRecordingServer(t, serve)
	ctx := context.Background()
	cred := discoveryTestCredential()

	for _, forge := range []string{"github", "gitea"} {
		repo := discoveryTestRepo(forge, srv.URL)
		probe, err := PreviewForgeFor(repo, srv.Client())
		if err != nil {
			t.Fatalf("PreviewForgeFor(%s) error: %v", forge, err)
		}
		if _, err := probe.Environments(ctx, repo, cred); err != nil {
			t.Fatalf("%s Environments error: %v", forge, err)
		}
		if _, err := probe.Deployments(ctx, repo, PreviewFilter{Ref: "main"}, cred); err != nil {
			t.Fatalf("%s Deployments error: %v", forge, err)
		}
		if _, err := probe.DeploymentStatuses(ctx, repo, 7, cred); err != nil {
			t.Fatalf("%s DeploymentStatuses error: %v", forge, err)
		}
	}

	recorded := requests()
	if len(recorded) == 0 {
		t.Fatal("no request was recorded")
	}
	for _, r := range recorded {
		if r.Method != http.MethodGet {
			t.Errorf("%s %s used %s, want GET only", r.Host, r.Path, r.Method)
		}
	}
}

// TestPreviewForgeNeverEchoesTheCredential pins that the PAT literal stays in
// the Authorization header: it appears in no request path or query, and in no
// error string the adapters return.
func TestPreviewForgeNeverEchoesTheCredential(t *testing.T) {
	srv, requests := discoveryRecordingServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"the instance failed"}`, http.StatusInternalServerError)
	})
	ctx := context.Background()
	cred := discoveryTestCredential()

	var errs []error
	for _, forge := range []string{"github", "gitea"} {
		repo := discoveryTestRepo(forge, srv.URL)
		probe, err := PreviewForgeFor(repo, srv.Client())
		if err != nil {
			t.Fatalf("PreviewForgeFor(%s) error: %v", forge, err)
		}
		_, envErr := probe.Environments(ctx, repo, cred)
		_, depErr := probe.Deployments(ctx, repo, PreviewFilter{Ref: "feature/x", SHA: "abc123"}, cred)
		_, statusErr := probe.DeploymentStatuses(ctx, repo, 7, cred)
		errs = append(errs, envErr, depErr, statusErr)
	}

	for _, err := range errs {
		if err == nil {
			t.Fatal("error = nil, want the instance failure reported")
		}
		assertNoCredential(t, "adapter error", err.Error())
	}
	for _, r := range requests() {
		assertNoCredential(t, "request path "+r.Path, r.Path)
		assertNoCredential(t, "request query "+r.Path, r.Query)
	}
}

// previewGitHubDeploymentPage renders n GitHub deployment objects starting at
// id start, so a test can fill a page exactly.
func previewGitHubDeploymentPage(start, n int) string {
	var b strings.Builder
	b.WriteString("[")
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b,
			`{"id":%s,"sha":"sha-%d","ref":"main","task":"deploy","environment":"env-%d","created_at":"2026-11-12T09:00:00Z","updated_at":"2026-11-12T09:01:00Z"}`,
			strconv.Itoa(start+i), start+i, start+i)
	}
	b.WriteString("]")
	return b.String()
}
