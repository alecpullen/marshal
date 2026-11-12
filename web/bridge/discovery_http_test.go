package bridge

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// discoveryRouteFixture wires a GitHub-wire forge, a disposable linked
// checkout, and the fleet over them. The checkout deliberately omits
// instructions and an environment definition, so the report carries
// recommendations as well as facts and gaps.
func discoveryRouteFixture(t *testing.T) (*Fleet, *Workspace, Repo, func() []discoveryRecordedRequest) {
	t.Helper()
	dir, _ := discoveryReportCheckout(t, map[string]string{
		"docs/README.md":           "# docs\n",
		"go.mod":                   "module example.com/widgets\n\ngo 1.22\n",
		".github/workflows/ci.yml": "on: push\n",
	})
	srv, requests := discoveryRecordingServer(t, discoveryReportGitHub{}.handler(t))
	repo := discoveryTestRepo("github", srv.URL)
	fleet, ws := discoveryReportFleet(t, repo, dir)
	return fleet, ws, repo, requests
}

// TestDiscoverRouteReturnsCapabilitiesAndReportTogether pins the reason the
// endpoint is one call: the capability summary and the report that explains it
// arrive in the same response, so a caller can never observe one without the
// other.
func TestDiscoverRouteReturnsCapabilitiesAndReportTogether(t *testing.T) {
	fleet, _, repo, _ := discoveryRouteFixture(t)
	s := NewServer(fleet, "")

	rec := doReq(t, s, http.MethodPost, "/api/repos/"+repo.ID+"/discover", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST discover = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var report DiscoveryReport
	decodeBody(t, rec, &report)

	if report.RepoID != repo.ID {
		t.Errorf("repoId = %q, want %q", report.RepoID, repo.ID)
	}
	if report.Forge != "github" {
		t.Errorf("forge = %q, want github", report.Forge)
	}
	switch report.Status {
	case discoveryStatusOK, discoveryStatusDegraded, discoveryStatusUnavailable:
	default:
		t.Errorf("status = %q, outside the frozen report states", report.Status)
	}

	keys := discoveryCapabilityKeys()
	if len(report.Capabilities) != len(keys) {
		t.Fatalf("capabilities = %d, want the frozen %d", len(report.Capabilities), len(keys))
	}
	for i, key := range keys {
		capability := report.Capabilities[i]
		if capability.Key != key {
			t.Errorf("capability %d = %q, want %q (frozen order)", i, capability.Key, key)
		}
		switch capability.State {
		case DiscoveryCapabilitySupported, DiscoveryCapabilityUnconfigured,
			DiscoveryCapabilityInsufficientPermission,
			DiscoveryCapabilityTemporarilyUnavailable, DiscoveryCapabilityUnsupported:
		default:
			t.Errorf("capability %q state = %q, outside the five frozen states", key, capability.State)
		}
	}

	// A report, not just a capability summary: the response carries facts
	// from both sources and advisory recommendations.
	if len(report.Facts) == 0 {
		t.Fatal("facts is empty, want the checkout and forge facts")
	}
	var forge, checkout bool
	for _, fact := range report.Facts {
		switch {
		case fact.Source == "":
			t.Errorf("fact %+v has no source", fact)
		case len(fact.Source) > len("forge:") && fact.Source[:len("forge:")] == "forge:":
			forge = true
		case len(fact.Source) > len("checkout:") && fact.Source[:len("checkout:")] == "checkout:":
			checkout = true
		}
	}
	if !forge || !checkout {
		t.Errorf("fact sources = forge %t, checkout %t; want both", forge, checkout)
	}
	if len(report.Recommended) == 0 {
		t.Error("recommended is empty, want the advisory suggestions for the bare checkout")
	}
	if report.Gaps == nil {
		t.Error("gaps is nil, want the key present even when no section failed")
	}

	assertNoCredential(t, "discover response", rec.Body.String())
}

// TestDiscoverRouteRequiresFleet pins the nil-fleet answer: a registry-only
// bridge has no workspace, no repos and no credentials, so the endpoint
// reports the capability as absent rather than panicking.
func TestDiscoverRouteRequiresFleet(t *testing.T) {
	s, _, _, _ := newTestServer(t, "")

	rec := doReq(t, s, http.MethodPost, "/api/repos/repo-1/discover", nil, nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("POST discover without a fleet = %d, want 503 (body %s)", rec.Code, rec.Body.String())
	}
	var body map[string]string
	decodeBody(t, rec, &body)
	if body["error"] == "" {
		t.Errorf("body = %s, want a JSON error explaining fleet mode is required", rec.Body.String())
	}
}

// TestDiscoverRouteUnknownRepo pins the 404 and that an unknown id never
// reaches the forge.
func TestDiscoverRouteUnknownRepo(t *testing.T) {
	fleet, _, repo, requests := discoveryRouteFixture(t)
	s := NewServer(fleet, "")

	rec := doReq(t, s, http.MethodPost, "/api/repos/missing/discover", nil, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("POST discover for an unknown repo = %d, want 404 (body %s)", rec.Code, rec.Body.String())
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
	if rec := doReq(t, s, http.MethodPost, "/api/repos/"+repo.ID+"/discover", nil, nil); rec.Code != http.StatusOK {
		t.Fatalf("POST discover for the registered repo = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
}

// TestDiscoverRouteMethodMismatch pins that discovery is POST-only: a
// read-shaped GET is rejected by the mux rather than reaching the handler.
func TestDiscoverRouteMethodMismatch(t *testing.T) {
	fleet, _, repo, requests := discoveryRouteFixture(t)
	s := NewServer(fleet, "")

	rec := doReq(t, s, http.MethodGet, "/api/repos/"+repo.ID+"/discover", nil, nil)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET discover = %d, want 405 (body %s)", rec.Code, rec.Body.String())
	}
	if got := len(requests()); got != 0 {
		t.Errorf("a GET made %d forge requests, want none", got)
	}
}

// TestDiscoverRouteHonoursBearerAuth pins that the route sits behind the
// shared /api bearer check: it needs no auth code of its own and cannot be
// reached unauthenticated when the server has a token.
func TestDiscoverRouteHonoursBearerAuth(t *testing.T) {
	const token = "discover-route-token"
	fleet, _, repo, _ := discoveryRouteFixture(t)
	s := NewServer(fleet, token)

	path := "/api/repos/" + repo.ID + "/discover"
	if rec := doReq(t, s, http.MethodPost, path, nil, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated POST discover = %d, want 401 (body %s)", rec.Code, rec.Body.String())
	}
	rec := doReq(t, s, http.MethodPost, path, nil, map[string]string{
		"Authorization": "Bearer " + token,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("authenticated POST discover = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
}

// TestDiscoverRouteDoesNotPersistState pins the read-only guarantee at the
// HTTP boundary: a successful call leaves the workspace file byte-identical,
// its version unchanged, and creates no project, agent or pending work item.
func TestDiscoverRouteDoesNotPersistState(t *testing.T) {
	fleet, ws, repo, _ := discoveryRouteFixture(t)
	s := NewServer(fleet, "")

	before, err := os.ReadFile(ws.path)
	if err != nil {
		t.Fatalf("read workspace before: %v", err)
	}
	var beforeFile workspaceFile
	if err := json.Unmarshal(before, &beforeFile); err != nil {
		t.Fatalf("decode workspace before: %v", err)
	}
	projectsBefore := len(ws.Projects())
	agentsBefore := len(ws.Agents())
	pendingBefore := len(ws.Pending())

	rec := doReq(t, s, http.MethodPost, "/api/repos/"+repo.ID+"/discover", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST discover = %d, want 200 (body %s)", rec.Code, rec.Body.String())
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

	if got := len(ws.Projects()); got != projectsBefore {
		t.Errorf("projects = %d, want %d: discovery must not create a project", got, projectsBefore)
	}
	if got := len(ws.Agents()); got != agentsBefore {
		t.Errorf("agents = %d, want %d: discovery must not start an agent", got, agentsBefore)
	}
	if got := len(ws.Pending()); got != pendingBefore {
		t.Errorf("pending = %d, want %d: discovery must not create a work item", got, pendingBefore)
	}
}

// TestDiscoverRouteInternalFailureIs500 pins the status-code split the SPA
// keys on: an unexpected in-process discovery failure is 500, not writeErr's
// 502, which denotes a child-process failure.
func TestDiscoverRouteInternalFailureIs500(t *testing.T) {
	ws := NewWorkspace(filepath.Join(t.TempDir(), "workspace.json"))
	if _, err := ws.Load(); err != nil {
		t.Fatalf("load workspace: %v", err)
	}
	// A stored record with no id is the one in-process failure DiscoverRepo
	// reports: it cannot assemble a report for a repository it cannot name.
	// Seeding it directly is what makes the handler's error branch reachable
	// without a live forge or a child process.
	ws.mu.Lock()
	ws.repos["repo-unidentifiable"] = Repo{
		URL:     "https://forge.example.com/acme/widgets.git",
		OwnerID: "local",
	}
	ws.mu.Unlock()

	fleet := NewFleet(ws, "unused", nil, t.TempDir(), Limits{}, "", nil, "")
	t.Cleanup(fleet.Close)
	s := NewServer(fleet, "")

	rec := doReq(t, s, http.MethodPost, "/api/repos/repo-unidentifiable/discover", nil, nil)
	if rec.Code == http.StatusBadGateway {
		t.Fatalf("POST discover = 502; that status is reserved for child-process failures, want 500")
	}
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("POST discover = %d, want 500 (body %s)", rec.Code, rec.Body.String())
	}
	var body map[string]string
	decodeBody(t, rec, &body)
	if body["error"] == "" {
		t.Errorf("body = %s, want a JSON error describing the in-process failure", rec.Body.String())
	}
}
