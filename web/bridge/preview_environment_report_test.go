package bridge

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
)

// previewReportTokenEnv is the environment variable the fixture credential
// resolves through. Its value is discoveryTestToken, which every assertion
// treats as a secret that must never surface.
const previewReportTokenEnv = "MARSHAL_PREVIEW_REPORT_TOKEN"

// previewReportPAT is the resolvable personal access token credential the
// preview report fixture registers.
func previewReportPAT() Credential {
	return Credential{ID: "gh", Kind: "pat", EnvVar: previewReportTokenEnv, OwnerID: "local"}
}

// previewReportFleet registers repo and, when cred is non-nil, a credential,
// then builds the fleet over them. No project is linked and no checkout exists:
// the preview surface must need neither.
func previewReportFleet(t *testing.T, repo Repo, cred *Credential) (*Fleet, *Workspace) {
	t.Helper()
	ws := NewWorkspace(filepath.Join(t.TempDir(), "workspace.json"))
	if _, err := ws.Load(); err != nil {
		t.Fatalf("load workspace: %v", err)
	}
	if err := ws.PutRepo(repo); err != nil {
		t.Fatalf("put repo: %v", err)
	}
	t.Setenv(previewReportTokenEnv, discoveryTestToken)
	if cred != nil {
		if err := ws.PutCredential(*cred); err != nil {
			t.Fatalf("put credential: %v", err)
		}
	}
	f := NewFleet(ws, "unused", nil, t.TempDir(), Limits{}, "", nil, "")
	t.Cleanup(f.Close)
	return f, ws
}

// previewForgeFixture serves the GitHub-wire shapes the preview report reads. A
// zero status field means the endpoint answers normally; an empty body means
// the collection is empty.
type previewForgeFixture struct {
	defaultBranch  string
	metadataStatus int
	environments   string
	envStatus      int
	deployments    string
	deployStatus   int
	statuses       map[string]string
	statusStatus   map[string]int
}

func (f previewForgeFixture) handler(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/repos/acme/widgets":
			if f.metadataStatus != 0 {
				http.Error(w, `{"message":"metadata failed"}`, f.metadataStatus)
				return
			}
			fmt.Fprintf(w, `{"default_branch":%q}`, f.defaultBranch)
		case strings.HasSuffix(r.URL.Path, "/environments"):
			if f.envStatus != 0 {
				http.Error(w, `{"message":"environments refused"}`, f.envStatus)
				return
			}
			if f.environments == "" {
				fmt.Fprint(w, `{"total_count":0,"environments":[]}`)
				return
			}
			fmt.Fprint(w, f.environments)
		case strings.Contains(r.URL.Path, "/deployments/") && strings.HasSuffix(r.URL.Path, "/statuses"):
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/repos/acme/widgets/deployments/"), "/statuses")
			if code := f.statusStatus[id]; code != 0 {
				http.Error(w, `{"message":"statuses refused"}`, code)
				return
			}
			body := f.statuses[id]
			if body == "" {
				fmt.Fprint(w, `[]`)
				return
			}
			fmt.Fprint(w, body)
		case strings.HasSuffix(r.URL.Path, "/deployments"):
			if f.deployStatus != 0 {
				http.Error(w, `{"message":"deployments refused"}`, f.deployStatus)
				return
			}
			if f.deployments == "" {
				fmt.Fprint(w, `[]`)
				return
			}
			fmt.Fprint(w, f.deployments)
		default:
			http.Error(w, `{"message":"unexpected path"}`, http.StatusNotFound)
		}
	}
}

// previewReport runs the report against a fixture server and returns it with
// the recorded requests.
func previewReport(t *testing.T, repo Repo, filter PreviewFilter, fixture previewForgeFixture) (*PreviewReport, []discoveryRecordedRequest) {
	t.Helper()
	srv, requests := discoveryRecordingServer(t, fixture.handler(t))
	repo.APIBase = srv.URL
	fleet, _ := previewReportFleet(t, repo, ptrCredential(previewReportPAT()))
	report, err := fleet.previewEnvironments(context.Background(), repo, filter, srv.Client())
	if err != nil {
		t.Fatalf("previewEnvironments error: %v", err)
	}
	return report, requests()
}

func ptrCredential(cred Credential) *Credential { return &cred }

// previewCapability returns one capability entry, failing when it is absent.
func previewCapability(t *testing.T, report *PreviewReport, key string) DiscoveryCapability {
	t.Helper()
	for _, capability := range report.Capabilities {
		if capability.Key == key {
			return capability
		}
	}
	t.Fatalf("capability %q is absent from %+v", key, report.Capabilities)
	return DiscoveryCapability{}
}

// previewGap returns one gap entry, failing when it is absent.
func previewGap(t *testing.T, report *PreviewReport, key string) DiscoveryGap {
	t.Helper()
	for _, gap := range report.Gaps {
		if gap.Key == key {
			return gap
		}
	}
	t.Fatalf("gap %q is absent from %+v", key, report.Gaps)
	return DiscoveryGap{}
}

// previewDeploymentRequest returns the recorded deployments read.
func previewDeploymentRequest(t *testing.T, requests []discoveryRecordedRequest) discoveryRecordedRequest {
	t.Helper()
	for _, r := range requests {
		if strings.HasSuffix(r.Path, "/deployments") {
			return r
		}
	}
	t.Fatalf("no deployments request in %+v", requests)
	return discoveryRecordedRequest{}
}

// previewStatusPaths returns the deployment ids whose statuses were read.
func previewStatusPaths(requests []discoveryRecordedRequest) []string {
	out := make([]string, 0, len(requests))
	for _, r := range requests {
		if strings.Contains(r.Path, "/deployments/") && strings.HasSuffix(r.Path, "/statuses") {
			out = append(out, r.Path)
		}
	}
	return out
}

// TestPreviewReportGitOnlyMakesNoRequest pins the sanctioned Git-only path: no
// forge declared means no adapter and no request at all, with every capability
// unsupported and a gap saying no forge is declared.
func TestPreviewReportGitOnlyMakesNoRequest(t *testing.T) {
	srv, requests := discoveryRecordingServer(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("the Git-only path made a request to %s", r.URL.Path)
	})
	repo := discoveryTestRepo("", srv.URL)
	fleet, _ := previewReportFleet(t, repo, ptrCredential(previewReportPAT()))

	report, err := fleet.previewEnvironments(context.Background(), repo, PreviewFilter{}, srv.Client())
	if err != nil {
		t.Fatalf("previewEnvironments error: %v", err)
	}
	if got := len(requests()); got != 0 {
		t.Errorf("the Git-only path made %d requests, want none", got)
	}
	for _, key := range previewCapabilityKeys() {
		capability := previewCapability(t, report, key)
		if capability.State != DiscoveryCapabilityUnsupported {
			t.Errorf("%s state = %q, want unsupported", key, capability.State)
		}
		if capability.Detail != "no forge is declared for this repository" {
			t.Errorf("%s detail = %q, want the no-forge cause", key, capability.Detail)
		}
		if gap := previewGap(t, report, key); gap.Section == "" {
			t.Errorf("%s gap has no section", key)
		}
	}
	if report.Status != discoveryStatusDegraded {
		t.Errorf("status = %q, want degraded", report.Status)
	}
	if len(report.Environments) != 0 || len(report.Deployments) != 0 {
		t.Errorf("environments/deployments = %d/%d, want empty", len(report.Environments), len(report.Deployments))
	}
}

// TestPreviewReportNamesAFamilyWithNoPreviewAdapter pins decision 1 for a
// declared family this bridge has no preview adapter for: every capability is
// unsupported, no request is made, and the cause names the missing adapter
// instead of degenerating to the bare forge family.
func TestPreviewReportNamesAFamilyWithNoPreviewAdapter(t *testing.T) {
	// gitlab is a forge family Marshal knows, with no read-only preview adapter
	// in this bridge.
	report, requests := previewReport(t, discoveryTestRepo("gitlab", ""), PreviewFilter{},
		previewForgeFixture{defaultBranch: "main"})

	if got := len(requests); got != 0 {
		t.Errorf("requests = %d, want none: no adapter exists to make one", got)
	}
	const cause = `forge "gitlab" has no read-only preview adapter in this bridge`
	if report.Status != discoveryStatusDegraded {
		t.Errorf("status = %q, want degraded", report.Status)
	}
	for _, key := range previewCapabilityKeys() {
		capability := previewCapability(t, report, key)
		if capability.State != DiscoveryCapabilityUnsupported {
			t.Errorf("%s state = %q, want unsupported", key, capability.State)
		}
		if capability.Detail != cause {
			t.Errorf("%s detail = %q, want %q", key, capability.Detail, cause)
		}
		gap := previewGap(t, report, key)
		if gap.State != DiscoveryCapabilityUnsupported {
			t.Errorf("%s gap state = %q, want unsupported", key, gap.State)
		}
		if !strings.HasPrefix(gap.Detail, cause) {
			t.Errorf("%s gap = %q, want the explicit adapter cause first", key, gap.Detail)
		}
		if !strings.Contains(gap.Detail, previewFix(DiscoveryCapabilityUnsupported, true)) {
			t.Errorf("%s gap = %q, want the usual unsupported fix suffix", key, gap.Detail)
		}
	}
}

// TestPreviewReportUnconfiguredWithoutAPATCredential pins that an unusable
// credential is unconfigured for all three keys and touches no host: an absent
// credential and a non-PAT credential alike.
func TestPreviewReportUnconfiguredWithoutAPATCredential(t *testing.T) {
	cases := map[string]*Credential{
		"missing": nil,
		"non-PAT": ptrCredential(Credential{ID: "gh", Kind: "ssh", EnvVar: previewReportTokenEnv, OwnerID: "local"}),
	}
	for name, cred := range cases {
		t.Run(name, func(t *testing.T) {
			srv, requests := discoveryRecordingServer(t, func(w http.ResponseWriter, r *http.Request) {
				t.Errorf("an unusable credential made a request to %s", r.URL.Path)
			})
			repo := discoveryTestRepo("github", srv.URL)
			fleet, _ := previewReportFleet(t, repo, cred)

			report, err := fleet.previewEnvironments(context.Background(), repo, PreviewFilter{}, srv.Client())
			if err != nil {
				t.Fatalf("previewEnvironments error: %v", err)
			}
			if got := len(requests()); got != 0 {
				t.Errorf("an unusable credential made %d requests, want none", got)
			}
			for _, key := range previewCapabilityKeys() {
				if got := previewCapability(t, report, key).State; got != DiscoveryCapabilityUnconfigured {
					t.Errorf("%s state = %q, want unconfigured", key, got)
				}
			}
			if report.Status != discoveryStatusDegraded {
				t.Errorf("status = %q, want degraded", report.Status)
			}
		})
	}
}

// TestPreviewReportUnavailableWhenTheForgeIsUnreachable pins the visible
// outage: a transport failure on the metadata read is unavailable with a
// non-empty error, never a silent "this repository has no previews".
func TestPreviewReportUnavailableWhenTheForgeIsUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	repo := discoveryTestRepo("github", srv.URL)
	srv.Close() // every request now fails at the transport layer
	fleet, _ := previewReportFleet(t, repo, ptrCredential(previewReportPAT()))

	report, err := fleet.previewEnvironments(context.Background(), repo, PreviewFilter{}, srv.Client())
	if err != nil {
		t.Fatalf("previewEnvironments error: %v", err)
	}
	if report.Status != discoveryStatusUnavailable {
		t.Fatalf("status = %q, want unavailable", report.Status)
	}
	if report.Error == "" {
		t.Error("error is empty, want the outage reason")
	}
	for _, key := range previewCapabilityKeys() {
		if got := previewCapability(t, report, key).State; got != DiscoveryCapabilityTemporarilyUnavailable {
			t.Errorf("%s state = %q, want temporarily_unavailable", key, got)
		}
		if gap := previewGap(t, report, key); gap.State != DiscoveryCapabilityTemporarilyUnavailable {
			t.Errorf("%s gap state = %q, want temporarily_unavailable", key, gap.State)
		}
	}
}

// TestPreviewReportClassifies404AfterReadableMetadataAsUnsupported pins the
// 404-after-readable-metadata rule: the instance serves the repository but not
// the preview resource, so the resource is unsupported and the gap names it.
func TestPreviewReportClassifies404AfterReadableMetadataAsUnsupported(t *testing.T) {
	report, requests := previewReport(t, discoveryTestRepo("github", ""), PreviewFilter{}, previewForgeFixture{
		defaultBranch: "main",
		envStatus:     http.StatusNotFound,
		statusStatus:  map[string]int{"*": http.StatusNotFound},
	})
	if got := previewCapability(t, report, "preview_environments").State; got != DiscoveryCapabilityUnsupported {
		t.Errorf("preview_environments = %q, want unsupported", got)
	}
	gap := previewGap(t, report, "preview_environments")
	if !strings.Contains(gap.Detail, "HTTP 404") {
		t.Errorf("environments gap = %q, want the 404 cause", gap.Detail)
	}
	if gap.Section != previewSectionEnvironments {
		t.Errorf("environments gap section = %q, want %q", gap.Section, previewSectionEnvironments)
	}
	// The metadata read is what makes it an instance fact; it must have run.
	if len(requests) == 0 || requests[0].Path != "/repos/acme/widgets" {
		t.Errorf("first request = %+v, want the metadata probe first", requests)
	}
}

// TestPreviewReportClassifies404BeforeReadableMetadataAsInsufficientPermission
// pins the other half of the 404 rule: with metadata unreadable, a 404 means
// the credential cannot see the resource, not that the instance lacks it.
func TestPreviewReportClassifies404BeforeReadableMetadataAsInsufficientPermission(t *testing.T) {
	report, _ := previewReport(t, discoveryTestRepo("github", ""), PreviewFilter{}, previewForgeFixture{
		metadataStatus: http.StatusNotFound,
		envStatus:      http.StatusNotFound,
	})
	if got := previewCapability(t, report, "preview_environments").State; got != DiscoveryCapabilityInsufficientPermission {
		t.Errorf("preview_environments = %q, want insufficient_permission", got)
	}
	if got := previewGap(t, report, "preview_environments").State; got != DiscoveryCapabilityInsufficientPermission {
		t.Errorf("environments gap = %q, want insufficient_permission", got)
	}
}

// TestPreviewReportGiteaInstanceAnswering404IsUnsupported pins the Gitea/Forgejo
// instance that serves no deployment resource: every capability is unsupported
// in the instance's own wording, never "this repository has no previews".
func TestPreviewReportGiteaInstanceAnswering404IsUnsupported(t *testing.T) {
	repo := discoveryTestRepo("gitea", "")
	report, _ := previewReport(t, repo, PreviewFilter{}, previewForgeFixture{
		defaultBranch: "main",
		envStatus:     http.StatusNotFound,
		deployStatus:  http.StatusNotFound,
	})
	for _, key := range previewCapabilityKeys() {
		capability := previewCapability(t, report, key)
		if capability.State != DiscoveryCapabilityUnsupported {
			t.Errorf("%s state = %q, want unsupported", key, capability.State)
		}
		gap := previewGap(t, report, key)
		if !strings.Contains(gap.Detail, "this forge instance does not expose this resource") {
			t.Errorf("%s gap = %q, want the instance wording", key, gap.Detail)
		}
		if strings.Contains(gap.Detail, "adapter support would be needed") {
			t.Errorf("%s gap = %q, want no adapter-support wording", key, gap.Detail)
		}
	}
	if report.Status != discoveryStatusDegraded {
		t.Errorf("status = %q, want degraded", report.Status)
	}
	if len(report.Deployments) != 0 || len(report.Environments) != 0 {
		t.Errorf("the report claims facts for a resource the instance does not serve: %+v", report)
	}
}

// TestPreviewReportDegradesOnlyTheDeniedEnvironmentsRead pins partial failure:
// a refused environments read degrades that key alone, and the deployments and
// statuses facts stay in the report.
func TestPreviewReportDegradesOnlyTheDeniedEnvironmentsRead(t *testing.T) {
	report, _ := previewReport(t, discoveryTestRepo("github", ""), PreviewFilter{Ref: "main"}, previewForgeFixture{
		defaultBranch: "main",
		envStatus:     http.StatusForbidden,
		deployments:   `[{"id":99,"environment":"production","ref":"main","sha":"abc","task":"deploy"}]`,
		statuses: map[string]string{
			"99": `[{"id":7,"state":"success","environment_url":"https://preview.example.internal"}]`,
		},
	})
	if got := previewCapability(t, report, "preview_environments").State; got != DiscoveryCapabilityInsufficientPermission {
		t.Errorf("preview_environments = %q, want insufficient_permission", got)
	}
	if got := previewCapability(t, report, "preview_deployments").State; got != DiscoveryCapabilitySupported {
		t.Errorf("preview_deployments = %q, want supported", got)
	}
	if got := previewCapability(t, report, "preview_statuses").State; got != DiscoveryCapabilitySupported {
		t.Errorf("preview_statuses = %q, want supported", got)
	}
	if len(report.Gaps) != 1 || report.Gaps[0].Key != "preview_environments" {
		t.Errorf("gaps = %+v, want exactly the environments key", report.Gaps)
	}
	if len(report.Deployments) != 1 || report.Deployments[0].State != "success" {
		t.Errorf("deployments = %+v, want the deployment with its status intact", report.Deployments)
	}
}

// TestPreviewReportReportsSupportedGitHubWithPAT pins the supported path: the
// provider's ids, URLs, status strings and revisions arrive verbatim and the
// resolved scope is echoed.
func TestPreviewReportReportsSupportedGitHubWithPAT(t *testing.T) {
	report, _ := previewReport(t, discoveryTestRepo("github", ""), PreviewFilter{Ref: "feature/x"}, previewForgeFixture{
		defaultBranch: "main",
		environments: `{"total_count":1,"environments":[
			{"id":1234567,"name":"production","html_url":"https://github.com/acme/widgets/deployments/activity_log?environments_filter=production",
			 "protection_rules":[{"id":1},{"id":2}],
			 "deployment_branch_policy":{"protected_branches":true,"custom_branch_policies":false}}]}`,
		deployments: `[{"id":99,"environment":"production","original_environment":"prod","ref":"feature/x",
			"sha":"8e4c1b2f0e1d2c3b4a5968778695a4b3c2d1e0f9","task":"deploy",
			"created_at":"2026-11-12T09:00:00Z","updated_at":"2026-11-12T09:01:12Z",
			"transient_environment":false,"production_environment":true}]`,
		statuses: map[string]string{
			"99": `[{"id":4242,"state":"success","environment_url":"https://preview.example.internal",
				"target_url":"https://ci.example/run/42","log_url":"https://ci.example/run/42/log",
				"description":"deployed 8e4c1b2"}]`,
		},
	})

	if report.Status != discoveryStatusOK {
		t.Errorf("status = %q, want ok (gaps %+v)", report.Status, report.Gaps)
	}
	if report.Forge != "github" || report.RepoID != "repo-1" {
		t.Errorf("repoId/forge = %q/%q, want repo-1/github", report.RepoID, report.Forge)
	}
	if report.Ref != "feature/x" || report.SHA != "" {
		t.Errorf("scope = ref %q sha %q, want feature/x and an empty sha", report.Ref, report.SHA)
	}
	wantEnvironment := PreviewEnvironment{
		Name:                "production",
		ID:                  "1234567",
		HTMLURL:             "https://github.com/acme/widgets/deployments/activity_log?environments_filter=production",
		BranchPolicy:        "protected_branches",
		ProtectionRuleCount: 2,
	}
	if len(report.Environments) != 1 || report.Environments[0] != wantEnvironment {
		t.Errorf("environments = %+v, want %+v", report.Environments, wantEnvironment)
	}
	wantDeployment := PreviewDeployment{
		ID:                  "99",
		Environment:         "production",
		OriginalEnvironment: "prod",
		Ref:                 "feature/x",
		SHA:                 "8e4c1b2f0e1d2c3b4a5968778695a4b3c2d1e0f9",
		Task:                "deploy",
		State:               "success",
		EnvironmentURL:      "https://preview.example.internal",
		TargetURL:           "https://ci.example/run/42",
		LogURL:              "https://ci.example/run/42/log",
		Description:         "deployed 8e4c1b2",
		Production:          true,
		CreatedAt:           "2026-11-12T09:00:00Z",
		UpdatedAt:           "2026-11-12T09:01:12Z",
	}
	if len(report.Deployments) != 1 || report.Deployments[0] != wantDeployment {
		t.Errorf("deployments = %+v, want %+v", report.Deployments, wantDeployment)
	}
}

// TestPreviewReportResolvesScopeFromRefAndSHA pins that both query parameters
// are applied conjunctively and both are echoed.
func TestPreviewReportResolvesScopeFromRefAndSHA(t *testing.T) {
	report, requests := previewReport(t, discoveryTestRepo("github", ""),
		PreviewFilter{Ref: "feature/x", SHA: "abc123"}, previewForgeFixture{defaultBranch: "main"})

	if report.Ref != "feature/x" || report.SHA != "abc123" {
		t.Errorf("scope = ref %q sha %q, want both echoed", report.Ref, report.SHA)
	}
	query, err := url.ParseQuery(previewDeploymentRequest(t, requests).Query)
	if err != nil {
		t.Fatalf("parse deployments query: %v", err)
	}
	if query.Get("ref") != "feature/x" || query.Get("sha") != "abc123" {
		t.Errorf("deployments query = %q, want both filters applied", query.Encode())
	}
}

// TestPreviewReportFallsBackToTheRegisteredBranch pins the first fallback: with
// no query scope the registered branch is what the repository was connected on.
func TestPreviewReportFallsBackToTheRegisteredBranch(t *testing.T) {
	repo := discoveryTestRepo("github", "")
	repo.Branch = "develop"
	report, requests := previewReport(t, repo, PreviewFilter{}, previewForgeFixture{defaultBranch: "main"})

	if report.Ref != "develop" || report.SHA != "" {
		t.Errorf("scope = ref %q sha %q, want the registered branch develop", report.Ref, report.SHA)
	}
	query, err := url.ParseQuery(previewDeploymentRequest(t, requests).Query)
	if err != nil {
		t.Fatalf("parse deployments query: %v", err)
	}
	if query.Get("ref") != "develop" {
		t.Errorf("deployments query = %q, want ref=develop", query.Encode())
	}
}

// TestPreviewReportFallsBackToTheForgeDefaultBranch pins the second fallback:
// with no registered branch, the forge's default branch is the scope.
func TestPreviewReportFallsBackToTheForgeDefaultBranch(t *testing.T) {
	report, requests := previewReport(t, discoveryTestRepo("github", ""), PreviewFilter{},
		previewForgeFixture{defaultBranch: "trunk"})

	if report.Ref != "trunk" || report.SHA != "" {
		t.Errorf("scope = ref %q sha %q, want the forge default branch trunk", report.Ref, report.SHA)
	}
	query, err := url.ParseQuery(previewDeploymentRequest(t, requests).Query)
	if err != nil {
		t.Fatalf("parse deployments query: %v", err)
	}
	if query.Get("ref") != "trunk" {
		t.Errorf("deployments query = %q, want ref=trunk", query.Encode())
	}
}

// TestPreviewReportWithoutAnyScopeIsUnfiltered pins the terminal case: no scope
// is resolvable at all, so the read is unfiltered and the capability says so in
// exactly the contract's words.
func TestPreviewReportWithoutAnyScopeIsUnfiltered(t *testing.T) {
	report, requests := previewReport(t, discoveryTestRepo("github", ""), PreviewFilter{},
		previewForgeFixture{deployments: `[{"id":1,"environment":"production"}]`})

	if report.Ref != "" || report.SHA != "" {
		t.Errorf("scope = ref %q sha %q, want both empty", report.Ref, report.SHA)
	}
	const want = "no revision scope was resolvable; every deployment the forge reports is listed"
	if got := previewCapability(t, report, "preview_deployments").Detail; got != want {
		t.Errorf("preview_deployments detail = %q, want %q", got, want)
	}
	if got := previewCapability(t, report, "preview_deployments").State; got != DiscoveryCapabilitySupported {
		t.Errorf("preview_deployments = %q, want supported", got)
	}
	query, err := url.ParseQuery(previewDeploymentRequest(t, requests).Query)
	if err != nil {
		t.Fatalf("parse deployments query: %v", err)
	}
	if _, ok := query["ref"]; ok {
		t.Errorf("deployments query = %q, want no ref filter", query.Encode())
	}
	if _, ok := query["sha"]; ok {
		t.Errorf("deployments query = %q, want no sha filter", query.Encode())
	}
	if len(report.Deployments) != 1 {
		t.Errorf("deployments = %d, want the one the forge reported", len(report.Deployments))
	}
}

// TestPreviewReportNonNumericIDLosesWithinItsEnvironment pins plan decision
// 10: a non-numeric id loses to a numeric id within its environment group, and
// survives grouping only as the sole member of its group. It is ordered after
// every numeric survivor, keeps its row with empty status fields, and its
// status is never read because the status URL needs a usable id.
func TestPreviewReportNonNumericIDLosesWithinItsEnvironment(t *testing.T) {
	report, requests := previewReport(t, discoveryTestRepo("github", ""), PreviewFilter{Ref: "main"},
		previewForgeFixture{
			defaultBranch: "main",
			// id 0 is a deployment the forge reported without an id, which the
			// report carries as "" — the non-numeric case.
			deployments: `[
				{"id":0,"environment":"production","ref":"main"},
				{"id":99,"environment":"production","ref":"main"},
				{"id":0,"environment":"staging","ref":"main"}]`,
			statuses: map[string]string{
				"99": `[{"id":7,"state":"success","environment_url":"https://preview.example.internal"}]`,
			},
		})

	if len(report.Deployments) != 2 {
		t.Fatalf("deployments = %+v, want one row per environment", report.Deployments)
	}
	named := report.Deployments[0]
	if named.Environment != "production" || named.ID != "99" {
		t.Errorf("first row = %q/%q, want production/99: the numeric id wins its group",
			named.Environment, named.ID)
	}
	if named.State != "success" || named.EnvironmentURL != "https://preview.example.internal" {
		t.Errorf("first row = %+v, want its status folded in", named)
	}
	sole := report.Deployments[1]
	if sole.Environment != "staging" || sole.ID != "" {
		t.Errorf("second row = %q/%q, want staging with an empty id: the non-numeric survivor comes after the numeric ones",
			sole.Environment, sole.ID)
	}
	if sole.State != "" || sole.EnvironmentURL != "" || sole.TargetURL != "" ||
		sole.LogURL != "" || sole.Description != "" {
		t.Errorf("second row = %+v, want empty status fields", sole)
	}
	got := previewStatusPaths(requests)
	want := []string{"/repos/acme/widgets/deployments/99/statuses"}
	if len(got) != len(want) || (len(want) > 0 && got[0] != want[0]) {
		t.Errorf("status reads = %v, want only %v: a non-numeric id has no status URL", got, want)
	}
}

// TestPreviewReportNamesTheNewestDeploymentPerEnvironment pins the grouping
// step: one row per environment, the greatest id, ordered by id descending, and
// statuses read only for the rows named.
func TestPreviewReportNamesTheNewestDeploymentPerEnvironment(t *testing.T) {
	report, requests := previewReport(t, discoveryTestRepo("github", ""), PreviewFilter{Ref: "main"},
		previewForgeFixture{
			defaultBranch: "main",
			deployments: `[
				{"id":1,"environment":"production","ref":"main"},
				{"id":99,"environment":"production","ref":"main"},
				{"id":50,"environment":"staging","ref":"main"}]`,
		})

	if len(report.Deployments) != 2 {
		t.Fatalf("deployments = %+v, want one row per environment", report.Deployments)
	}
	if report.Deployments[0].ID != "99" || report.Deployments[1].ID != "50" {
		t.Errorf("deployments = %s, %s; want 99 then 50 (id descending)",
			report.Deployments[0].ID, report.Deployments[1].ID)
	}
	got := previewStatusPaths(requests)
	want := []string{
		"/repos/acme/widgets/deployments/99/statuses",
		"/repos/acme/widgets/deployments/50/statuses",
	}
	if len(got) != len(want) {
		t.Fatalf("status reads = %v, want only the named rows %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("status read %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestPreviewReportRecordsTheNameCapGap pins the name cap: more environments
// than the report names yields exactly the cap and a gap saying which bound was
// hit, with the capability still supported.
func TestPreviewReportRecordsTheNameCapGap(t *testing.T) {
	var deployments strings.Builder
	deployments.WriteString("[")
	for id := 1; id <= previewNamedDeploymentLimit+1; id++ {
		if id > 1 {
			deployments.WriteString(",")
		}
		fmt.Fprintf(&deployments, `{"id":%d,"environment":"env-%d","ref":"main"}`, id, id)
	}
	deployments.WriteString("]")

	report, _ := previewReport(t, discoveryTestRepo("github", ""), PreviewFilter{Ref: "main"},
		previewForgeFixture{defaultBranch: "main", deployments: deployments.String()})

	if got := len(report.Deployments); got != previewNamedDeploymentLimit {
		t.Errorf("deployments = %d, want the cap of %d", got, previewNamedDeploymentLimit)
	}
	capability := previewCapability(t, report, "preview_deployments")
	if capability.State != DiscoveryCapabilitySupported {
		t.Errorf("preview_deployments = %q, want supported (a cap is not a failure)", capability.State)
	}
	gap := previewGap(t, report, "preview_deployments")
	const want = "the forge reported deployments for more environments than this report names; the newest deployment for each of the most recently updated environments is listed"
	if gap.Detail != want {
		t.Errorf("gap = %q, want %q", gap.Detail, want)
	}
	if strings.Contains(gap.Detail, "adapter support") {
		t.Errorf("gap = %q, want no adapter-support wording", gap.Detail)
	}
}

// TestPreviewReportRecordsThePageBoundGap pins the page bound: a truncated
// deployments read keeps the capability supported and records the bound.
func TestPreviewReportRecordsThePageBoundGap(t *testing.T) {
	full := previewPageOfDeployments(discoveryForgePageSize)
	report, _ := previewReport(t, discoveryTestRepo("github", ""), PreviewFilter{Ref: "main"},
		previewForgeFixture{defaultBranch: "main", deployments: full})

	capability := previewCapability(t, report, "preview_deployments")
	if capability.State != DiscoveryCapabilitySupported {
		t.Errorf("preview_deployments = %q, want supported (a bound is not a failure)", capability.State)
	}
	if len(report.Deployments) == 0 {
		t.Error("deployments is empty, want the items read before the bound")
	}
	gap := previewGap(t, report, "preview_deployments")
	const want = "the forge reported more deployments than this report reads; the list is not exhaustive and a larger page bound would be needed to read the rest"
	if gap.Detail != want {
		t.Errorf("gap = %q, want %q", gap.Detail, want)
	}
	if strings.Contains(gap.Detail, "adapter support") {
		t.Errorf("gap = %q, want no adapter-support wording", gap.Detail)
	}
}

// TestPreviewReportDeploymentsBoundsTogetherKeepThePageBound pins the first-wins
// rule when both deployments bounds are hit at once: the page bound and the name
// cap are keyed on preview_deployments, and the page bound is recorded first, so
// that is the one gap that lands.
func TestPreviewReportDeploymentsBoundsTogetherKeepThePageBound(t *testing.T) {
	// A full page of deployments over more than the name cap's environments:
	// the page bound is hit because a full final page means more to read, and
	// the name cap is hit because there are more environments than it names.
	var deployments strings.Builder
	deployments.WriteString("[")
	for id := 1; id <= discoveryForgePageSize; id++ {
		if id > 1 {
			deployments.WriteString(",")
		}
		fmt.Fprintf(&deployments, `{"id":%d,"environment":"env-%d","ref":"main"}`, id, id)
	}
	deployments.WriteString("]")
	if distinct := discoveryForgePageSize; distinct <= previewNamedDeploymentLimit {
		t.Fatalf("the fixture must exceed the name cap: %d environments, cap %d",
			distinct, previewNamedDeploymentLimit)
	}

	report, _ := previewReport(t, discoveryTestRepo("github", ""), PreviewFilter{Ref: "main"},
		previewForgeFixture{defaultBranch: "main", deployments: deployments.String()})

	if got := previewCapability(t, report, "preview_deployments").State; got != DiscoveryCapabilitySupported {
		t.Errorf("preview_deployments = %q, want supported (a bound is not a failure)", got)
	}
	if got := len(report.Deployments); got == 0 || got > previewNamedDeploymentLimit {
		t.Errorf("deployments = %d, want between 1 and the name cap of %d", got, previewNamedDeploymentLimit)
	}
	count := 0
	for _, gap := range report.Gaps {
		if gap.Key == "preview_deployments" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("preview_deployments gaps = %d in %+v, want exactly one", count, report.Gaps)
	}
	gap := previewGap(t, report, "preview_deployments")
	const want = "the forge reported more deployments than this report reads; the list is not exhaustive and a larger page bound would be needed to read the rest"
	if gap.Detail != want {
		t.Errorf("gap = %q, want the deployments page bound %q", gap.Detail, want)
	}
	if strings.Contains(gap.Detail, "more environments than this report names") {
		t.Errorf("gap = %q, want the page bound, not the name cap", gap.Detail)
	}
}

// previewPageOfDeployments renders exactly one full page of GitHub deployments
// over two environment names, so the page bound is hit without the name cap.
func previewPageOfDeployments(n int) string {
	var b strings.Builder
	b.WriteString("[")
	for i := 1; i <= n; i++ {
		if i > 1 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"id":%d,"environment":"env-%d","ref":"main"}`, i, i%2)
	}
	b.WriteString("]")
	return b.String()
}

// TestPreviewReportRecordsTheEnvironmentsPageBoundGap pins the environments
// page bound: a truncated read keeps the environments it read, keeps the
// capability supported, and records the bound in its own gap.
func TestPreviewReportRecordsTheEnvironmentsPageBoundGap(t *testing.T) {
	full := previewPageOfEnvironments(discoveryForgePageSize)
	report, _ := previewReport(t, discoveryTestRepo("github", ""), PreviewFilter{Ref: "main"},
		previewForgeFixture{defaultBranch: "main", environments: full})

	capability := previewCapability(t, report, "preview_environments")
	if capability.State != DiscoveryCapabilitySupported {
		t.Errorf("preview_environments = %q, want supported (a bound is not a failure)", capability.State)
	}
	if len(report.Environments) == 0 {
		t.Error("environments is empty, want the items read before the bound")
	}
	gap := previewGap(t, report, "preview_environments")
	const want = "the forge reported more environments than this report reads; the list is not exhaustive and a larger page bound would be needed to read the rest"
	if gap.Detail != want {
		t.Errorf("gap = %q, want %q", gap.Detail, want)
	}
	if gap.State != DiscoveryCapabilityTemporarilyUnavailable {
		t.Errorf("gap state = %q, want temporarily_unavailable", gap.State)
	}
	if strings.Contains(gap.Detail, "adapter support") {
		t.Errorf("gap = %q, want no adapter-support wording", gap.Detail)
	}
}

// previewPageOfEnvironments renders exactly one full page of GitHub
// environments, so the environments page bound is hit.
func previewPageOfEnvironments(n int) string {
	var b strings.Builder
	fmt.Fprintf(&b, `{"total_count":%d,"environments":[`, n)
	for i := 1; i <= n; i++ {
		if i > 1 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"id":%d,"name":"env-%d"}`, i, i)
	}
	b.WriteString("]}")
	return b.String()
}

// TestPreviewReportRecordsTheStatusesPageBoundGap pins the statuses page bound:
// a truncated statuses read keeps its items, keeps the capability supported,
// and records the bound — the listed status may not be the newest.
func TestPreviewReportRecordsTheStatusesPageBoundGap(t *testing.T) {
	report, _ := previewReport(t, discoveryTestRepo("github", ""), PreviewFilter{Ref: "main"},
		previewForgeFixture{
			defaultBranch: "main",
			deployments:   `[{"id":99,"environment":"production","ref":"main"}]`,
			statuses: map[string]string{
				"99": previewPageOfStatuses(discoveryForgePageSize),
			},
		})

	if len(report.Deployments) != 1 {
		t.Fatalf("deployments = %+v, want the row kept", report.Deployments)
	}
	if got := report.Deployments[0].State; got != "success" {
		t.Errorf("state = %q, want the status folded from the items read", got)
	}
	capability := previewCapability(t, report, "preview_statuses")
	if capability.State != DiscoveryCapabilitySupported {
		t.Errorf("preview_statuses = %q, want supported (a bound is not a failure)", capability.State)
	}
	gap := previewGap(t, report, "preview_statuses")
	const want = "the forge reported more statuses for at least one deployment than this report reads; the listed status may not be the newest"
	if gap.Detail != want {
		t.Errorf("gap = %q, want %q", gap.Detail, want)
	}
	if gap.State != DiscoveryCapabilityTemporarilyUnavailable {
		t.Errorf("gap state = %q, want temporarily_unavailable", gap.State)
	}
	if strings.Contains(gap.Detail, "adapter support") {
		t.Errorf("gap = %q, want no adapter-support wording", gap.Detail)
	}
}

// previewPageOfStatuses renders exactly one full page of GitHub deployment
// statuses, so the statuses page bound is hit.
func previewPageOfStatuses(n int) string {
	var b strings.Builder
	b.WriteString("[")
	for i := 1; i <= n; i++ {
		if i > 1 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"id":%d,"state":"success","environment_url":"https://preview.example.internal"}`, i)
	}
	b.WriteString("]")
	return b.String()
}

// TestPreviewReportFoldsTheNewestStatusOnly pins decision 4a: the newest status
// supplies the row's status fields, an absent field stays empty rather than
// being back-filled from an older status, and the deployment's own description
// is never used.
func TestPreviewReportFoldsTheNewestStatusOnly(t *testing.T) {
	report, _ := previewReport(t, discoveryTestRepo("github", ""), PreviewFilter{Ref: "main"},
		previewForgeFixture{
			defaultBranch: "main",
			deployments:   `[{"id":99,"environment":"production","ref":"main","description":"the deployment's own description"}]`,
			statuses: map[string]string{
				"99": `[
					{"id":1,"state":"pending","environment_url":"https://old.example.internal","description":"old"},
					{"id":2,"state":"success","environment_url":"https://preview.example.internal"}]`,
			},
		})
	if len(report.Deployments) != 1 {
		t.Fatalf("deployments = %+v, want one row", report.Deployments)
	}
	row := report.Deployments[0]
	if row.State != "success" || row.EnvironmentURL != "https://preview.example.internal" {
		t.Errorf("row = %+v, want the newest status's state and environment URL", row)
	}
	if row.TargetURL != "" || row.LogURL != "" || row.Description != "" {
		t.Errorf("row = %+v, want absent newest-status fields empty and not back-filled", row)
	}
}

// TestPreviewReportDeploymentWithNoStatusesKeepsEmptyState pins that an empty
// status collection is a completed read: the row stays, the state is empty, the
// capability is supported and no gap is recorded.
func TestPreviewReportDeploymentWithNoStatusesKeepsEmptyState(t *testing.T) {
	report, _ := previewReport(t, discoveryTestRepo("github", ""), PreviewFilter{Ref: "main"},
		previewForgeFixture{
			defaultBranch: "main",
			deployments:   `[{"id":99,"environment":"production","ref":"main"}]`,
		})
	if len(report.Deployments) != 1 {
		t.Fatalf("deployments = %+v, want the row kept", report.Deployments)
	}
	if report.Deployments[0].State != "" {
		t.Errorf("state = %q, want empty for a deployment with no statuses", report.Deployments[0].State)
	}
	if got := previewCapability(t, report, "preview_statuses").State; got != DiscoveryCapabilitySupported {
		t.Errorf("preview_statuses = %q, want supported", got)
	}
	if len(report.Gaps) != 0 {
		t.Errorf("gaps = %+v, want none", report.Gaps)
	}
}

// TestPreviewReportStatusNotExercisedWithoutDeployments pins the not-exercised
// convention: with nothing to read statuses for, the capability is supported
// and says it was not exercised.
func TestPreviewReportStatusNotExercisedWithoutDeployments(t *testing.T) {
	report, requests := previewReport(t, discoveryTestRepo("github", ""), PreviewFilter{Ref: "main"},
		previewForgeFixture{defaultBranch: "main"})
	const want = "not exercised: no deployment was reported for this scope"
	capability := previewCapability(t, report, "preview_statuses")
	if capability.State != DiscoveryCapabilitySupported {
		t.Errorf("preview_statuses = %q, want supported", capability.State)
	}
	if capability.Detail != want {
		t.Errorf("preview_statuses detail = %q, want %q", capability.Detail, want)
	}
	if got := len(previewStatusPaths(requests)); got != 0 {
		t.Errorf("status reads = %d, want none", got)
	}
}

// TestPreviewReportStatusNotExercisedWithoutAUsableID pins the other
// not-exercised case: deployments were reported, but not one carries an id the
// status URL can use, so the detail says exactly that rather than claiming no
// deployment was reported while the deployments section shows rows.
func TestPreviewReportStatusNotExercisedWithoutAUsableID(t *testing.T) {
	report, requests := previewReport(t, discoveryTestRepo("github", ""), PreviewFilter{Ref: "main"},
		previewForgeFixture{
			defaultBranch: "main",
			// GitHub reports a deployment it has no id for as 0, which the
			// report carries as "" so it never becomes "0".
			deployments: `[{"id":0,"environment":"production","ref":"main"}]`,
		})

	if len(report.Deployments) != 1 {
		t.Fatalf("deployments = %+v, want the row reported", report.Deployments)
	}
	const want = "not exercised: no deployment carried a usable id"
	capability := previewCapability(t, report, "preview_statuses")
	if capability.State != DiscoveryCapabilitySupported {
		t.Errorf("preview_statuses = %q, want supported", capability.State)
	}
	if capability.Detail != want {
		t.Errorf("preview_statuses detail = %q, want %q", capability.Detail, want)
	}
	if got := len(previewStatusPaths(requests)); got != 0 {
		t.Errorf("status reads = %d, want none for a deployment with no usable id", got)
	}
	if len(report.Gaps) != 0 {
		t.Errorf("gaps = %+v, want none: not exercising a read is not a failure", report.Gaps)
	}
}

// TestPreviewReportStatusFailureKeepsTheKeyAgainstTheBound pins the ordering
// that motivated recording the statuses bound last: when one status read fails
// and another is truncated, the failure keeps the preview_statuses key and the
// bound detail is not the one recorded.
func TestPreviewReportStatusFailureKeepsTheKeyAgainstTheBound(t *testing.T) {
	report, _ := previewReport(t, discoveryTestRepo("github", ""), PreviewFilter{Ref: "main"},
		previewForgeFixture{
			defaultBranch: "main",
			deployments: `[
				{"id":99,"environment":"production","ref":"main"},
				{"id":50,"environment":"staging","ref":"main"}]`,
			statuses: map[string]string{
				// 99 fails, 50 answers with a full page and is truncated.
				"50": previewPageOfStatuses(discoveryForgePageSize),
			},
			statusStatus: map[string]int{"99": http.StatusForbidden},
		})

	count := 0
	for _, gap := range report.Gaps {
		if gap.Key == "preview_statuses" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("preview_statuses gaps = %d in %+v, want exactly one", count, report.Gaps)
	}
	gap := previewGap(t, report, "preview_statuses")
	if gap.State != DiscoveryCapabilityInsufficientPermission {
		t.Errorf("gap state = %q, want insufficient_permission", gap.State)
	}
	if capability := previewCapability(t, report, "preview_statuses"); capability.State == DiscoveryCapabilitySupported {
		t.Errorf("preview_statuses = %q, want a state that is not supported", capability.State)
	}
	const boundDetail = "the forge reported more statuses for at least one deployment than this report reads; the listed status may not be the newest"
	if !strings.Contains(gap.Detail, "HTTP 403") {
		t.Errorf("gap = %q, want the failed read's cause", gap.Detail)
	}
	if strings.Contains(gap.Detail, boundDetail) {
		t.Errorf("gap = %q, want the failure's cause, not the statuses page bound", gap.Detail)
	}
}

// TestPreviewReportStatusFailureDegradesOnlyStatuses pins partial failure in
// the status fan-out: one refused read degrades that capability alone and the
// rows that did read keep their statuses.
func TestPreviewReportStatusFailureDegradesOnlyStatuses(t *testing.T) {
	report, _ := previewReport(t, discoveryTestRepo("github", ""), PreviewFilter{Ref: "main"},
		previewForgeFixture{
			defaultBranch: "main",
			deployments: `[
				{"id":99,"environment":"production","ref":"main"},
				{"id":50,"environment":"staging","ref":"main"}]`,
			statuses: map[string]string{
				"99": `[{"id":7,"state":"success","environment_url":"https://preview.example.internal"}]`,
			},
			statusStatus: map[string]int{"50": http.StatusForbidden},
		})
	if got := previewCapability(t, report, "preview_statuses").State; got != DiscoveryCapabilityInsufficientPermission {
		t.Errorf("preview_statuses = %q, want insufficient_permission", got)
	}
	if got := previewCapability(t, report, "preview_deployments").State; got != DiscoveryCapabilitySupported {
		t.Errorf("preview_deployments = %q, want supported", got)
	}
	if got := previewCapability(t, report, "preview_environments").State; got != DiscoveryCapabilitySupported {
		t.Errorf("preview_environments = %q, want supported", got)
	}
	if len(report.Gaps) != 1 || report.Gaps[0].Key != "preview_statuses" {
		t.Errorf("gaps = %+v, want exactly the statuses key", report.Gaps)
	}
	byID := map[string]PreviewDeployment{}
	for _, row := range report.Deployments {
		byID[row.ID] = row
	}
	if got := byID["99"].State; got != "success" {
		t.Errorf("deployment 99 state = %q, want its status kept", got)
	}
	if got := byID["50"].State; got != "" {
		t.Errorf("deployment 50 state = %q, want empty after a refused read", got)
	}
}

// TestPreviewReportStatusFailureTakesTheMostActionableState pins the severity
// fold: several status failures yield the state the operator can act on first,
// in the capability and in its single gap.
func TestPreviewReportStatusFailureTakesTheMostActionableState(t *testing.T) {
	report, _ := previewReport(t, discoveryTestRepo("github", ""), PreviewFilter{Ref: "main"},
		previewForgeFixture{
			defaultBranch: "main",
			deployments: `[
				{"id":99,"environment":"production","ref":"main"},
				{"id":50,"environment":"staging","ref":"main"}]`,
			statusStatus: map[string]int{
				"99": http.StatusForbidden,
				"50": http.StatusInternalServerError,
			},
		})
	if got := previewCapability(t, report, "preview_statuses").State; got != DiscoveryCapabilityInsufficientPermission {
		t.Errorf("preview_statuses = %q, want the most actionable state insufficient_permission", got)
	}
	if len(report.Gaps) != 1 {
		t.Fatalf("gaps = %+v, want exactly one", report.Gaps)
	}
	if got := report.Gaps[0].State; got != DiscoveryCapabilityInsufficientPermission {
		t.Errorf("gap = %q, want insufficient_permission", got)
	}
}

// TestPreviewReportReportsEmptyCollectionsAsSupported pins that absence is not
// a failure: an empty repository reports empty arrays, every capability
// supported, no gaps and status ok.
func TestPreviewReportReportsEmptyCollectionsAsSupported(t *testing.T) {
	report, _ := previewReport(t, discoveryTestRepo("github", ""), PreviewFilter{}, previewForgeFixture{
		defaultBranch: "main",
	})
	if report.Status != discoveryStatusOK {
		t.Errorf("status = %q, want ok", report.Status)
	}
	if report.Environments == nil || report.Deployments == nil || report.Gaps == nil {
		t.Errorf("an array is nil, want an empty slice: %+v", report)
	}
	if len(report.Environments) != 0 || len(report.Deployments) != 0 || len(report.Gaps) != 0 {
		t.Errorf("collections = %d/%d/%d, want empty", len(report.Environments), len(report.Deployments), len(report.Gaps))
	}
	for _, key := range previewCapabilityKeys() {
		if got := previewCapability(t, report, key).State; got != DiscoveryCapabilitySupported {
			t.Errorf("%s = %q, want supported", key, got)
		}
	}
}

// TestPreviewReportReadsNoLocalPath pins that the report needs no checkout: a
// repository with no linked project and no local path still reports fully.
func TestPreviewReportReadsNoLocalPath(t *testing.T) {
	// A remote URL for a repository that exists on no local path, and no
	// project linked to it: the report must still be complete.
	repo := discoveryTestRepo("github", "")
	srv, _ := discoveryRecordingServer(t, previewForgeFixture{defaultBranch: "main"}.handler(t))
	repo.APIBase = srv.URL
	fleet, ws := previewReportFleet(t, repo, ptrCredential(previewReportPAT()))
	if got := len(ws.Projects()); got != 0 {
		t.Fatalf("projects = %d, want none linked", got)
	}

	report, err := fleet.previewEnvironments(context.Background(), repo, PreviewFilter{}, srv.Client())
	if err != nil {
		t.Fatalf("previewEnvironments error: %v", err)
	}
	if report.Status != discoveryStatusOK {
		t.Errorf("status = %q, want ok: the preview surface must not need a local path", report.Status)
	}
	if got := len(ws.Projects()); got != 0 {
		t.Errorf("projects = %d, want none created", got)
	}
}

// TestPreviewReportDoesNotChangeTheDiscoveryContract pins that this surface
// leaves the discovery report's ten keys, their order and its route alone.
func TestPreviewReportDoesNotChangeTheDiscoveryContract(t *testing.T) {
	want := []string{
		"repository_metadata",
		"branches",
		"pull_requests",
		"ci_checks",
		"ci_logs",
		"checkout_instructions",
		"checkout_docs",
		"checkout_tooling",
		"checkout_environment",
		"checkout_ci_definition",
	}
	if got := discoveryCapabilityKeys(); len(got) != len(want) {
		t.Fatalf("discoveryCapabilityKeys() = %v, want the frozen ten", got)
	} else {
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("discovery key %d = %q, want %q", i, got[i], want[i])
			}
		}
	}

	// The discovery route is still registered: it answers 404 for an unknown
	// repository rather than 405, which is what a missing route would.
	repo := discoveryTestRepo("github", "")
	fleet, _ := previewReportFleet(t, repo, ptrCredential(previewReportPAT()))
	s := NewServer(fleet, "")
	rec := doReq(t, s, http.MethodPost, "/api/repos/missing/discover", nil, nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("POST /api/repos/missing/discover = %d, want 404 (the route still exists)", rec.Code)
	}
}
