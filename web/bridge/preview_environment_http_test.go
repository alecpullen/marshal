package bridge

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

// previewRouteFixture wires a GitHub-wire forge, a registered repository and
// the fleet over them, and returns the server the route is reached through.
func previewRouteFixture(t *testing.T, fixture previewForgeFixture) (*Server, *Fleet, *Workspace, Repo, func() []discoveryRecordedRequest) {
	t.Helper()
	srv, requests := discoveryRecordingServer(t, fixture.handler(t))
	repo := discoveryTestRepo("github", srv.URL)
	fleet, ws := previewReportFleet(t, repo, ptrCredential(previewReportPAT()))
	return NewServer(fleet, ""), fleet, ws, repo, requests
}

// previewRoutePath is the route under test.
func previewRoutePath(repo Repo) string {
	return "/api/repos/" + repo.ID + "/preview-environments"
}

// TestPreviewRouteReturnsTheReport pins the route's shape: a 200 JSON report
// carrying the repository id, the three capabilities in order, the environments,
// the deployments and the gaps.
func TestPreviewRouteReturnsTheReport(t *testing.T) {
	server, _, _, repo, _ := previewRouteFixture(t, previewForgeFixture{
		defaultBranch: "main",
		environments: `{"total_count":1,"environments":[
			{"id":1234567,"name":"production","html_url":"https://github.com/acme/widgets/deployments/activity_log",
			 "protection_rules":[{"id":1}],
			 "deployment_branch_policy":{"protected_branches":true}}]}`,
		deployments: `[{"id":99,"environment":"production","ref":"main","sha":"8e4c1b2","task":"deploy"}]`,
		statuses: map[string]string{
			"99": `[{"id":7,"state":"success","environment_url":"https://preview.example.internal"}]`,
		},
	})

	rec := doReq(t, server, http.MethodGet, previewRoutePath(repo), nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET preview-environments = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var report PreviewReport
	decodeBody(t, rec, &report)
	if report.RepoID != repo.ID {
		t.Errorf("repoId = %q, want %q", report.RepoID, repo.ID)
	}
	if report.Forge != "github" {
		t.Errorf("forge = %q, want github", report.Forge)
	}
	if report.Status != discoveryStatusOK {
		t.Errorf("status = %q, want ok (gaps %+v)", report.Status, report.Gaps)
	}
	keys := previewCapabilityKeys()
	if len(report.Capabilities) != len(keys) {
		t.Fatalf("capabilities = %d, want the frozen %d", len(report.Capabilities), len(keys))
	}
	for i, key := range keys {
		if report.Capabilities[i].Key != key {
			t.Errorf("capability %d = %q, want %q (frozen order)", i, report.Capabilities[i].Key, key)
		}
		if report.Capabilities[i].State != DiscoveryCapabilitySupported {
			t.Errorf("capability %q = %q, want supported", key, report.Capabilities[i].State)
		}
	}
	if len(report.Environments) != 1 || report.Environments[0].Name != "production" {
		t.Errorf("environments = %+v, want the configured production environment", report.Environments)
	}
	if len(report.Deployments) != 1 || report.Deployments[0].State != "success" {
		t.Errorf("deployments = %+v, want the deployment with its status", report.Deployments)
	}
	if report.Gaps == nil {
		t.Error("gaps is nil, want the key present even when nothing failed")
	}

	assertNoCredential(t, "preview-environments response", rec.Body.String())
}

// TestPreviewRouteForwardsRefAndSHA pins that the query parameters reach the
// report's scope and therefore the deployments read.
func TestPreviewRouteForwardsRefAndSHA(t *testing.T) {
	server, _, _, repo, requests := previewRouteFixture(t, previewForgeFixture{defaultBranch: "main"})

	rec := doReq(t, server, http.MethodGet,
		previewRoutePath(repo)+"?ref=feature/x&sha=abc123", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET preview-environments = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var report PreviewReport
	decodeBody(t, rec, &report)
	if report.Ref != "feature/x" || report.SHA != "abc123" {
		t.Errorf("scope = ref %q sha %q, want both forwarded", report.Ref, report.SHA)
	}

	query, err := url.ParseQuery(previewDeploymentRequest(t, requests()).Query)
	if err != nil {
		t.Fatalf("parse deployments query: %v", err)
	}
	if query.Get("ref") != "feature/x" || query.Get("sha") != "abc123" {
		t.Errorf("deployments query = %q, want both filters applied", query.Encode())
	}
}

// TestPreviewRouteRequiresFleet pins the nil-fleet answer: a registry-only
// server has no credential store, so the route reports the capability absent
// rather than panicking.
func TestPreviewRouteRequiresFleet(t *testing.T) {
	s, _, _, _ := newTestServer(t, "")

	rec := doReq(t, s, http.MethodGet, "/api/repos/repo-1/preview-environments", nil, nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET preview-environments without a fleet = %d, want 503 (body %s)", rec.Code, rec.Body.String())
	}
	var body map[string]string
	decodeBody(t, rec, &body)
	if body["error"] == "" {
		t.Errorf("body = %s, want a JSON error explaining fleet mode is required", rec.Body.String())
	}
}

// TestPreviewRouteUnknownRepo pins the 404 and that an unknown id never
// reaches the forge.
func TestPreviewRouteUnknownRepo(t *testing.T) {
	server, _, _, repo, requests := previewRouteFixture(t, previewForgeFixture{defaultBranch: "main"})

	rec := doReq(t, server, http.MethodGet, "/api/repos/missing/preview-environments", nil, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET preview-environments for an unknown repo = %d, want 404 (body %s)", rec.Code, rec.Body.String())
	}
	var body map[string]string
	decodeBody(t, rec, &body)
	if body["error"] == "" {
		t.Errorf("body = %s, want a JSON error naming the unknown repository", rec.Body.String())
	}
	if got := len(requests()); got != 0 {
		t.Errorf("unknown repo made %d forge requests, want none", got)
	}

	// The registered repository still resolves, so the 404 above is about the
	// id and not about the route.
	if rec := doReq(t, server, http.MethodGet, previewRoutePath(repo), nil, nil); rec.Code != http.StatusOK {
		t.Fatalf("GET preview-environments for the registered repo = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
}

// TestPreviewRouteMethodMismatch pins that the route is GET-only: a POST is
// rejected by the mux rather than reaching the handler.
func TestPreviewRouteMethodMismatch(t *testing.T) {
	server, _, _, repo, requests := previewRouteFixture(t, previewForgeFixture{defaultBranch: "main"})

	rec := doReq(t, server, http.MethodPost, previewRoutePath(repo), nil, nil)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST preview-environments = %d, want 405 (body %s)", rec.Code, rec.Body.String())
	}
	if got := len(requests()); got != 0 {
		t.Errorf("a POST made %d forge requests, want none", got)
	}
}

// TestPreviewRouteHonoursBearerAuth pins that the route sits behind the shared
// /api bearer check: it needs no auth code of its own and cannot be reached
// unauthenticated when the server has a token.
func TestPreviewRouteHonoursBearerAuth(t *testing.T) {
	const token = "preview-route-token"
	srv, _ := discoveryRecordingServer(t, previewForgeFixture{defaultBranch: "main"}.handler(t))
	repo := discoveryTestRepo("github", srv.URL)
	fleet, _ := previewReportFleet(t, repo, ptrCredential(previewReportPAT()))
	s := NewServer(fleet, token)

	if rec := doReq(t, s, http.MethodGet, previewRoutePath(repo), nil, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated GET preview-environments = %d, want 401 (body %s)", rec.Code, rec.Body.String())
	}
	rec := doReq(t, s, http.MethodGet, previewRoutePath(repo), nil, map[string]string{
		"Authorization": "Bearer " + token,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("authenticated GET preview-environments = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
}

// TestPreviewRouteDoesNotPersistState pins the read-only guarantee at the HTTP
// boundary: a successful call leaves the workspace file byte-identical and its
// version unchanged.
func TestPreviewRouteDoesNotPersistState(t *testing.T) {
	server, _, ws, repo, _ := previewRouteFixture(t, previewForgeFixture{
		defaultBranch: "main",
		deployments:   `[{"id":99,"environment":"production","ref":"main"}]`,
	})

	before, err := os.ReadFile(ws.path)
	if err != nil {
		t.Fatalf("read workspace before: %v", err)
	}
	var beforeFile workspaceFile
	if err := json.Unmarshal(before, &beforeFile); err != nil {
		t.Fatalf("decode workspace before: %v", err)
	}

	rec := doReq(t, server, http.MethodGet, previewRoutePath(repo), nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET preview-environments = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}

	after, err := os.ReadFile(ws.path)
	if err != nil {
		t.Fatalf("read workspace after: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Errorf("workspace file changed:\nbefore %s\nafter  %s", before, after)
	}
	var afterFile workspaceFile
	if err := json.Unmarshal(after, &afterFile); err != nil {
		t.Fatalf("decode workspace after: %v", err)
	}
	if afterFile.Version != beforeFile.Version {
		t.Errorf("workspace version = %d, want %d unchanged", afterFile.Version, beforeFile.Version)
	}
}

// TestPreviewRouteInternalFailureIs500 pins the status-code split the SPA keys
// on: an unexpected in-process failure is 500, not writeErr's 502, which
// denotes a child-process failure.
func TestPreviewRouteInternalFailureIs500(t *testing.T) {
	ws := NewWorkspace(filepath.Join(t.TempDir(), "workspace.json"))
	if _, err := ws.Load(); err != nil {
		t.Fatalf("load workspace: %v", err)
	}
	// A stored record with no id is the one in-process failure
	// PreviewEnvironments reports: it cannot assemble a report for a repository
	// it cannot name. Seeding it directly is what makes the handler's error
	// branch reachable without a live forge or a child process.
	ws.mu.Lock()
	ws.repos["repo-unidentifiable"] = Repo{
		URL:     "https://forge.example.com/acme/widgets.git",
		OwnerID: "local",
	}
	ws.mu.Unlock()

	fleet := NewFleet(ws, "unused", nil, t.TempDir(), Limits{}, "", nil, "")
	t.Cleanup(fleet.Close)
	s := NewServer(fleet, "")

	rec := doReq(t, s, http.MethodGet, "/api/repos/repo-unidentifiable/preview-environments", nil, nil)
	if rec.Code == http.StatusBadGateway {
		t.Fatalf("GET preview-environments = 502; that status is reserved for child-process failures, want 500")
	}
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("GET preview-environments = %d, want 500 (body %s)", rec.Code, rec.Body.String())
	}
	var body map[string]string
	decodeBody(t, rec, &body)
	if body["error"] == "" {
		t.Errorf("body = %s, want a JSON error describing the in-process failure", rec.Body.String())
	}
}
