package bridge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// discoveryReportTokenEnv is the environment variable the report's PAT
// credential resolves through. Its value is discoveryTestToken, which every
// assertion below treats as a secret that must never surface.
const discoveryReportTokenEnv = "MARSHAL_DISCOVERY_REPORT_TOKEN"

// discoveryReportFleet registers repo, a resolvable PAT credential, and (when
// root is not empty) a project whose intake names repo, then builds the fleet
// over it. The checkout is linked only through project settings, exactly as
// the orchestrator is allowed to find it.
func discoveryReportFleet(t *testing.T, repo Repo, root string) (*Fleet, *Workspace) {
	t.Helper()
	ws := NewWorkspace(filepath.Join(t.TempDir(), "workspace.json"))
	if _, err := ws.Load(); err != nil {
		t.Fatalf("load workspace: %v", err)
	}
	if err := ws.PutRepo(repo); err != nil {
		t.Fatalf("put repo: %v", err)
	}
	t.Setenv(discoveryReportTokenEnv, discoveryTestToken)
	if err := ws.PutCredential(Credential{
		ID: "gh", Kind: "pat", EnvVar: discoveryReportTokenEnv, OwnerID: "local",
	}); err != nil {
		t.Fatalf("put credential: %v", err)
	}
	if root != "" {
		if err := ws.PutProjectSettings(root, ProjectSettings{Intake: ProjectIntake{RepoID: repo.ID}}); err != nil {
			t.Fatalf("link project to repo: %v", err)
		}
	}
	f := NewFleet(ws, "unused", nil, t.TempDir(), Limits{}, "", nil, "")
	f.git = testGitRunner(t)
	t.Cleanup(f.Close)
	return f, ws
}

// discoveryReportCheckout builds a disposable git checkout whose files are
// committed, so the inspection sees a clean tree, and returns its path and
// HEAD.
func discoveryReportCheckout(t *testing.T, files map[string]string) (dir, head string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir = t.TempDir()
	mustGit(t, dir, "init", "--initial-branch=main")
	mustGit(t, dir, "config", "user.email", "test@example.com")
	mustGit(t, dir, "config", "user.name", "Test")
	mustGit(t, dir, "config", "commit.gpgsign", "false")
	for rel, content := range files {
		discoveryWriteFile(t, dir, rel, []byte(content))
	}
	mustGit(t, dir, "add", ".")
	mustGit(t, dir, "commit", "-m", "seed")
	head = strings.TrimSpace(discoveryGitOutput(t, dir, "rev-parse", "HEAD"))
	return dir, head
}

// discoveryReportGitHub serves the GitHub-wire shapes the report reads. A zero
// status field means the endpoint answers normally.
type discoveryReportGitHub struct {
	defaultBranch  string
	branchSHA      string
	metadataStatus int
	branchesStatus int
	checksStatus   int
}

func (g discoveryReportGitHub) handler(t *testing.T) http.HandlerFunc {
	t.Helper()
	defaultBranch := g.defaultBranch
	if defaultBranch == "" {
		defaultBranch = "main"
	}
	branchSHA := g.branchSHA
	if branchSHA == "" {
		branchSHA = "sha-main"
	}
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		base := "/repos/acme/widgets"
		switch {
		case r.URL.Path == base:
			if g.metadataStatus != 0 {
				http.Error(w, `{"message":"Not Found"}`, g.metadataStatus)
				return
			}
			fmt.Fprintf(w, `{"default_branch":%q,"private":false,"visibility":"public",`+
				`"description":"Acme widget factory","topics":["widgets"],"language":"Go","size":12}`,
				defaultBranch)
		case r.URL.Path == base+"/branches":
			if g.branchesStatus != 0 {
				http.Error(w, `{"message":"Forbidden"}`, g.branchesStatus)
				return
			}
			fmt.Fprintf(w, `[{"name":%q,"commit":{"sha":%q}},{"name":"dev","commit":{"sha":"sha-dev"}}]`,
				defaultBranch, branchSHA)
		case r.URL.Path == base+"/pulls":
			fmt.Fprint(w, `[{"number":7,"state":"open","title":"Add widgets","head":{"sha":"sha-head"}}]`)
		case strings.HasPrefix(r.URL.Path, base+"/commits/") &&
			strings.HasSuffix(r.URL.Path, "/check-runs"):
			if g.checksStatus != 0 {
				http.Error(w, `{"message":"Resource not accessible by integration"}`, g.checksStatus)
				return
			}
			fmt.Fprint(w, `{"check_runs":[{"id":9,"name":"build","conclusion":"success",
				"status":"completed","html_url":"https://ci.example.com/build/9",
				"details_url":"https://github.com/acme/widgets/actions/runs/77/job/42",
				"app":{"id":15368}}]}`)
		default:
			http.Error(w, `{"message":"unexpected path"}`, http.StatusNotFound)
		}
	}
}

// discoveryReportCapability returns one capability, failing when the report
// omitted it.
func discoveryReportCapability(t *testing.T, report *DiscoveryReport, key string) DiscoveryCapability {
	t.Helper()
	for _, cap := range report.Capabilities {
		if cap.Key == key {
			return cap
		}
	}
	t.Fatalf("capability %q is missing from the report", key)
	return DiscoveryCapability{}
}

// discoveryReportGap returns the gap for one capability key, failing when the
// report has none.
func discoveryReportGap(t *testing.T, report *DiscoveryReport, key string) DiscoveryGap {
	t.Helper()
	for _, gap := range report.Gaps {
		if gap.Key == key {
			return gap
		}
	}
	t.Fatalf("no gap for capability %q in %v", key, report.Gaps)
	return DiscoveryGap{}
}

// discoveryReportRequireContract pins the invariants every report carries: all
// ten capability keys in the frozen order, each in one of the five states, and
// non-nil slices.
func discoveryReportRequireContract(t *testing.T, report *DiscoveryReport) {
	t.Helper()
	if report == nil {
		t.Fatal("report is nil")
	}
	if report.Capabilities == nil || report.Facts == nil ||
		report.Recommended == nil || report.Gaps == nil {
		t.Fatalf("report slices must be non-nil: caps=%v facts=%v recs=%v gaps=%v",
			report.Capabilities == nil, report.Facts == nil,
			report.Recommended == nil, report.Gaps == nil)
	}
	keys := discoveryCapabilityKeys()
	if len(report.Capabilities) != len(keys) {
		t.Fatalf("capabilities = %d, want the frozen %d", len(report.Capabilities), len(keys))
	}
	for i, key := range keys {
		capability := report.Capabilities[i]
		if capability.Key != key {
			t.Fatalf("capability %d = %q, want %q (frozen order)", i, capability.Key, key)
		}
		switch capability.State {
		case DiscoveryCapabilitySupported, DiscoveryCapabilityUnconfigured,
			DiscoveryCapabilityInsufficientPermission,
			DiscoveryCapabilityTemporarilyUnavailable, DiscoveryCapabilityUnsupported:
		default:
			t.Fatalf("capability %q state = %q, outside the five frozen states", key, capability.State)
		}
	}
}

// discoveryReportForgeFacts returns every fact the forge produced.
func discoveryReportForgeFacts(report *DiscoveryReport) []DiscoveryFact {
	facts := make([]DiscoveryFact, 0, len(report.Facts))
	for _, fact := range report.Facts {
		if strings.HasPrefix(fact.Source, "forge:") {
			facts = append(facts, fact)
		}
	}
	return facts
}

// discoveryReportFactByValue returns a fact carrying value, or fails.
func discoveryReportFactByValue(t *testing.T, facts []DiscoveryFact, value string) DiscoveryFact {
	t.Helper()
	for _, fact := range facts {
		if fact.Value == value {
			return fact
		}
	}
	t.Fatalf("no fact with value %q in %+v", value, facts)
	return DiscoveryFact{}
}

// discoveryReportFactOfKind returns the fact with this kind and value. A branch
// named "main" and the default-branch fact both carry the value "main", so a
// lookup by value alone is ambiguous.
func discoveryReportFactOfKind(t *testing.T, facts []DiscoveryFact, kind, value string) DiscoveryFact {
	t.Helper()
	for _, fact := range facts {
		if fact.Kind == kind && fact.Value == value {
			return fact
		}
	}
	t.Fatalf("no %q fact with value %q in %+v", kind, value, facts)
	return DiscoveryFact{}
}

// discoveryReportRequireNoCredential pins the read-only promise that a PAT
// never reaches the wire. The whole report is serialised, so a token in any
// field - capability detail, gap detail, fact, or top-level error - fails.
func discoveryReportRequireNoCredential(t *testing.T, report *DiscoveryReport) {
	t.Helper()
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	assertNoCredential(t, "serialized report", string(raw))
	assertNoCredential(t, "report error", report.Error)
	if report.Worktree != nil {
		assertNoCredential(t, "worktree detail", report.Worktree.Detail)
	}
}

// TestDiscoverReportReportsSupportedGitHubWithPAT drives the whole report
// against a GitHub-wire PAT endpoint and a real linked checkout.
func TestDiscoverReportReportsSupportedGitHubWithPAT(t *testing.T) {
	dir, head := discoveryReportCheckout(t, map[string]string{
		"AGENTS.md":                "# agents\n",
		"README.md":                "# readme\n",
		"docs/README.md":           "# docs\n",
		"go.mod":                   "module example.com/widgets\n\ngo 1.22\n",
		"Dockerfile":               "FROM scratch\n",
		".github/workflows/ci.yml": "on: push\n",
	})
	srv, requests := discoveryRecordingServer(t, discoveryReportGitHub{}.handler(t))
	repo := discoveryTestRepo("github", srv.URL)
	fleet, _ := discoveryReportFleet(t, repo, dir)

	report, err := fleet.DiscoverRepo(context.Background(), repo)
	if err != nil {
		t.Fatalf("DiscoverRepo: %v", err)
	}
	discoveryReportRequireContract(t, report)

	if report.RepoID != repo.ID {
		t.Errorf("repoId = %q, want %q", report.RepoID, repo.ID)
	}
	if report.Forge != "github" {
		t.Errorf("forge = %q, want github", report.Forge)
	}
	if report.Status != "ok" {
		t.Errorf("status = %q, want ok; gaps = %+v", report.Status, report.Gaps)
	}
	if len(report.Gaps) != 0 {
		t.Errorf("gaps = %+v, want none for a fully supported report", report.Gaps)
	}
	for _, key := range discoveryCapabilityKeys() {
		capability := discoveryReportCapability(t, report, key)
		if capability.State != DiscoveryCapabilitySupported {
			t.Errorf("%s state = %q, want supported (%s)", key, capability.State, capability.Detail)
		}
	}
	if got := discoveryReportCapability(t, report, "ci_logs").Detail; got != "no failing check to exercise" {
		t.Errorf("ci_logs detail = %q, want the not-exercised note", got)
	}

	if report.Revision != head {
		t.Errorf("revision = %q, want the checkout HEAD %q", report.Revision, head)
	}
	if report.Branch != "main" {
		t.Errorf("branch = %q, want main", report.Branch)
	}
	if report.Worktree == nil {
		t.Fatal("worktree is nil for a linked checkout")
	}
	if report.Worktree.Path != dir || report.Worktree.Head != head || report.Worktree.Branch != "main" {
		t.Errorf("worktree = %+v, want the linked clean checkout", *report.Worktree)
	}
	if report.Worktree.Dirty == nil || *report.Worktree.Dirty {
		t.Errorf("worktree.dirty = %v, want a determined clean checkout", report.Worktree.Dirty)
	}

	// Checkout facts carry the checkout HEAD; forge facts carry the
	// default-branch head the probe observed.
	instructions := discoveryReportFactByValue(t, report.Facts, "AGENTS.md")
	if instructions.Source != "checkout:AGENTS.md" || instructions.Revision != head {
		t.Errorf("AGENTS.md fact = %+v, want source checkout:AGENTS.md at %q", instructions, head)
	}
	if instructions.Origin != "detected" || instructions.Section != "instructions" {
		t.Errorf("AGENTS.md fact = %+v, want a detected instructions fact", instructions)
	}
	defaultBranch := discoveryReportFactOfKind(t, discoveryReportForgeFacts(report), "default_branch", "main")
	if defaultBranch.Section != "repository" || defaultBranch.Revision != "sha-main" {
		t.Errorf("default branch fact = %+v, want the repository metadata fact at sha-main", defaultBranch)
	}
	for _, fact := range discoveryReportForgeFacts(report) {
		if fact.Revision != "sha-main" {
			t.Errorf("forge fact %+v revision = %q, want the default-branch head", fact, fact.Revision)
		}
	}

	reqs := requests()
	if len(reqs) != 4 {
		t.Fatalf("requests = %d, want metadata, branches, pulls and checks: %+v", len(reqs), reqs)
	}
	for _, r := range reqs {
		if r.Method != http.MethodGet {
			t.Errorf("%s method = %s, want GET", r.Path, r.Method)
		}
		if r.Auth != "Bearer "+discoveryTestToken {
			t.Errorf("%s authorization = %q, want the Bearer PAT header", r.Path, r.Auth)
		}
	}
	discoveryReportRequireNoCredential(t, report)
}

// TestDiscoverReportKeepsCheckoutFactsWhenForgeUnavailable makes the declared
// forge unreachable at the transport layer. The report must stay a successful,
// visibly unavailable result that still carries checkout facts.
func TestDiscoverReportKeepsCheckoutFactsWhenForgeUnavailable(t *testing.T) {
	dir, head := discoveryReportCheckout(t, map[string]string{
		"AGENTS.md": "# agents\n",
		"go.mod":    "module example.com/widgets\n",
	})

	srv := httptest.NewServer(discoveryReportGitHub{}.handler(t))
	apiBase := srv.URL
	srv.Close() // every request now fails at the transport layer

	repo := discoveryTestRepo("github", apiBase)
	fleet, _ := discoveryReportFleet(t, repo, dir)

	report, err := fleet.DiscoverRepo(context.Background(), repo)
	if err != nil {
		t.Fatalf("DiscoverRepo must return the report, not a transport error: %v", err)
	}
	discoveryReportRequireContract(t, report)

	if report.Status != "unavailable" {
		t.Errorf("status = %q, want unavailable", report.Status)
	}
	if report.Error == "" {
		t.Error("error is empty, want a visible explanation of the outage")
	}
	discoveryReportRequireNoCredential(t, report)

	for _, key := range []string{"repository_metadata", "branches", "pull_requests", "ci_checks", "ci_logs"} {
		capability := discoveryReportCapability(t, report, key)
		if capability.State != DiscoveryCapabilityTemporarilyUnavailable {
			t.Errorf("%s state = %q, want temporarily_unavailable", key, capability.State)
		}
		gap := discoveryReportGap(t, report, key)
		if gap.State != DiscoveryCapabilityTemporarilyUnavailable {
			t.Errorf("%s gap state = %q, want temporarily_unavailable", key, gap.State)
		}
		if gap.Detail == "" {
			t.Errorf("%s gap has no actionable detail", key)
		}
	}
	for _, key := range []string{"checkout_instructions", "checkout_tooling"} {
		if got := discoveryReportCapability(t, report, key).State; got != DiscoveryCapabilitySupported {
			t.Errorf("%s state = %q, want the checkout inspected independently", key, got)
		}
	}

	instructions := discoveryReportFactByValue(t, report.Facts, "AGENTS.md")
	if instructions.Revision != head {
		t.Errorf("AGENTS.md revision = %q, want the checkout HEAD %q", instructions.Revision, head)
	}
	if report.Worktree == nil || report.Worktree.Head != head {
		t.Errorf("worktree = %+v, want the linked checkout preserved", report.Worktree)
	}
	if report.Revision != head {
		t.Errorf("revision = %q, want the checkout HEAD %q", report.Revision, head)
	}
}

// TestDiscoverReportDegradesOnlyTheDeniedSection denies CI checks and requires
// every other section - forge and checkout - to keep reporting.
func TestDiscoverReportDegradesOnlyTheDeniedSection(t *testing.T) {
	dir, _ := discoveryReportCheckout(t, map[string]string{
		"AGENTS.md":      "# agents\n",
		"go.mod":         "module example.com/widgets\n",
		"docs/README.md": "# docs\n",
	})
	srv, _ := discoveryRecordingServer(t, discoveryReportGitHub{
		checksStatus: http.StatusForbidden,
	}.handler(t))
	repo := discoveryTestRepo("github", srv.URL)
	fleet, _ := discoveryReportFleet(t, repo, dir)

	report, err := fleet.DiscoverRepo(context.Background(), repo)
	if err != nil {
		t.Fatalf("DiscoverRepo: %v", err)
	}
	discoveryReportRequireContract(t, report)

	if report.Status != "degraded" {
		t.Errorf("status = %q, want degraded", report.Status)
	}
	if report.Error != "" {
		t.Errorf("error = %q, want none: a denied section is not a total outage", report.Error)
	}

	denied := discoveryReportCapability(t, report, "ci_checks")
	if denied.State != DiscoveryCapabilityInsufficientPermission {
		t.Fatalf("ci_checks state = %q, want insufficient_permission", denied.State)
	}
	gap := discoveryReportGap(t, report, "ci_checks")
	if gap.State != DiscoveryCapabilityInsufficientPermission {
		t.Errorf("ci_checks gap state = %q, want insufficient_permission", gap.State)
	}
	if !strings.Contains(gap.Detail, "permission") || !strings.Contains(gap.Detail, "grant") {
		t.Errorf("ci_checks gap detail = %q, want the cause and what would fix it", gap.Detail)
	}
	if got := discoveryReportCapability(t, report, "ci_logs").State; got != DiscoveryCapabilitySupported {
		t.Errorf("ci_logs state = %q, want supported (not exercised), not a second degraded section", got)
	}
	if len(report.Gaps) != 1 {
		t.Errorf("gaps = %+v, want exactly the denied CI section", report.Gaps)
	}

	for _, key := range []string{
		"repository_metadata", "branches", "pull_requests",
		"checkout_instructions", "checkout_docs", "checkout_tooling",
	} {
		if got := discoveryReportCapability(t, report, key).State; got != DiscoveryCapabilitySupported {
			t.Errorf("%s state = %q, want supported: a denied section must not suppress another", key, got)
		}
	}
	if len(discoveryReportForgeFacts(report)) == 0 {
		t.Error("no forge facts survived the denied CI section")
	}
	if len(report.Facts) == 0 {
		t.Error("no checkout facts survived the denied CI section")
	}
	discoveryReportRequireNoCredential(t, report)
}

// TestDiscoverReportClassifiesHiddenRepositoryAsPermissionDenied makes the
// repository itself invisible. A 404 before metadata is known readable is
// insufficient_permission, never unsupported.
func TestDiscoverReportClassifiesHiddenRepositoryAsPermissionDenied(t *testing.T) {
	dir, _ := discoveryReportCheckout(t, map[string]string{"go.mod": "module x\n"})
	srv, _ := discoveryRecordingServer(t, discoveryReportGitHub{
		metadataStatus: http.StatusNotFound,
		branchesStatus: http.StatusNotFound,
	}.handler(t))
	repo := discoveryTestRepo("github", srv.URL)
	fleet, _ := discoveryReportFleet(t, repo, dir)

	report, err := fleet.DiscoverRepo(context.Background(), repo)
	if err != nil {
		t.Fatalf("DiscoverRepo: %v", err)
	}
	discoveryReportRequireContract(t, report)

	if report.Status != "degraded" {
		t.Errorf("status = %q, want degraded", report.Status)
	}
	metadata := discoveryReportCapability(t, report, "repository_metadata")
	if metadata.State != DiscoveryCapabilityInsufficientPermission {
		t.Fatalf("repository_metadata state = %q, want insufficient_permission", metadata.State)
	}
	if strings.Contains(metadata.Detail, "does not expose") {
		t.Errorf("repository_metadata detail = %q, reads as unsupported, not hidden", metadata.Detail)
	}
	gap := discoveryReportGap(t, report, "repository_metadata")
	if gap.State != DiscoveryCapabilityInsufficientPermission {
		t.Errorf("repository_metadata gap state = %q, want insufficient_permission", gap.State)
	}
	// Metadata was not readable, so the later sections cannot be reclassified
	// as unsupported either.
	if got := discoveryReportCapability(t, report, "branches").State; got != DiscoveryCapabilityInsufficientPermission {
		t.Errorf("branches state = %q, want insufficient_permission", got)
	}
	discoveryReportRequireNoCredential(t, report)
}

// TestDiscoverReportClassifiesMissingGiteaBaseAsUnconfigured requires a
// declared self-hosted forge with no API base to be unconfigured, with an
// actionable detail, and to make no HTTP request at all.
func TestDiscoverReportClassifiesMissingGiteaBaseAsUnconfigured(t *testing.T) {
	dir, _ := discoveryReportCheckout(t, map[string]string{"go.mod": "module x\n"})
	transport := &discoveryCountingTransport{}
	repo := discoveryTestRepo("gitea", "")
	fleet, _ := discoveryReportFleet(t, repo, dir)

	report, err := fleet.discoverRepo(context.Background(), repo, &http.Client{Transport: transport})
	if err != nil {
		t.Fatalf("DiscoverRepo: %v", err)
	}
	discoveryReportRequireContract(t, report)

	if report.Status != "degraded" {
		t.Errorf("status = %q, want degraded", report.Status)
	}
	metadata := discoveryReportCapability(t, report, "repository_metadata")
	if metadata.State != DiscoveryCapabilityUnconfigured {
		t.Fatalf("repository_metadata state = %q, want unconfigured", metadata.State)
	}
	if !strings.Contains(metadata.Detail, "apiBase") || !strings.Contains(metadata.Detail, "API base") {
		t.Errorf("detail = %q, want it to name the instance API base setting", metadata.Detail)
	}
	for _, key := range []string{"repository_metadata", "branches", "pull_requests", "ci_checks", "ci_logs"} {
		if got := discoveryReportCapability(t, report, key).State; got != DiscoveryCapabilityUnconfigured {
			t.Errorf("%s state = %q, want unconfigured", key, got)
		}
		if gap := discoveryReportGap(t, report, key); gap.State != DiscoveryCapabilityUnconfigured {
			t.Errorf("%s gap state = %q, want unconfigured", key, gap.State)
		}
	}
	if got := transport.calls.Load(); got != 0 {
		t.Fatalf("HTTP requests = %d, want 0 for a missing API base", got)
	}
	discoveryReportRequireNoCredential(t, report)
}

// TestDiscoverReportGitOnlyUsesLocalFacts requires a repository with no
// declared forge to report unsupported forge capabilities, degraded status, no
// HTTP at all, and the local checkout facts.
func TestDiscoverReportGitOnlyUsesLocalFacts(t *testing.T) {
	dir, head := discoveryReportCheckout(t, map[string]string{
		"AGENTS.md": "# agents\n",
		"go.mod":    "module example.com/widgets\n",
	})
	transport := &discoveryCountingTransport{}
	repo := Repo{
		ID:      "repo-1",
		URL:     "https://forge.example.com/acme/widgets.git",
		OwnerID: "local",
	}
	fleet, _ := discoveryReportFleet(t, repo, dir)

	report, err := fleet.discoverRepo(context.Background(), repo, &http.Client{Transport: transport})
	if err != nil {
		t.Fatalf("DiscoverRepo: %v", err)
	}
	discoveryReportRequireContract(t, report)

	if report.Forge != "" {
		t.Errorf("forge = %q, want empty for the Git-only path", report.Forge)
	}
	if report.Status != "degraded" {
		t.Errorf("status = %q, want degraded: forge capabilities are unsupported", report.Status)
	}
	for _, key := range []string{"repository_metadata", "branches", "pull_requests", "ci_checks", "ci_logs"} {
		capability := discoveryReportCapability(t, report, key)
		if capability.State != DiscoveryCapabilityUnsupported {
			t.Errorf("%s state = %q, want unsupported", key, capability.State)
		}
		gap := discoveryReportGap(t, report, key)
		if gap.State != DiscoveryCapabilityUnsupported {
			t.Errorf("%s gap state = %q, want unsupported", key, gap.State)
		}
		if !strings.Contains(gap.Detail, "github") && !strings.Contains(gap.Detail, "gitea") {
			t.Errorf("%s gap detail = %q, want the fix (declare github or gitea)", key, gap.Detail)
		}
	}
	if got := transport.calls.Load(); got != 0 {
		t.Fatalf("HTTP requests = %d, want 0 with no declared forge", got)
	}

	instructions := discoveryReportFactByValue(t, report.Facts, "AGENTS.md")
	if instructions.Source != "checkout:AGENTS.md" || instructions.Revision != head {
		t.Errorf("AGENTS.md fact = %+v, want the local checkout fact at %q", instructions, head)
	}
	if report.Revision != head || report.Branch != "main" {
		t.Errorf("revision/branch = %q/%q, want the local checkout %q/main", report.Revision, report.Branch, head)
	}
	if report.Worktree == nil || report.Worktree.Head != head {
		t.Errorf("worktree = %+v, want the local checkout", report.Worktree)
	}
	discoveryReportRequireNoCredential(t, report)
}

// TestDiscoverReportDistinguishesAbsentFromUnreadable requires an absent
// instruction file to be a fact and an unreadable one to be a gap, textually
// distinct, with absence never inferred from a gap.
func TestDiscoverReportDistinguishesAbsentFromUnreadable(t *testing.T) {
	files := map[string]string{
		"src/main.go":              "package main\n",
		"docs/README.md":           "# docs\n",
		"go.mod":                   "module example.com/widgets\n",
		"Dockerfile":               "FROM scratch\n",
		".github/workflows/ci.yml": "on: push\n",
	}

	absentDir, _ := discoveryReportCheckout(t, files)
	absentRepo := Repo{ID: "repo-1", URL: "https://forge.example.com/acme/widgets.git", OwnerID: "local"}
	absentFleet, _ := discoveryReportFleet(t, absentRepo, absentDir)
	absent, err := absentFleet.discoverRepo(context.Background(), absentRepo, nil)
	if err != nil {
		t.Fatalf("DiscoverRepo (absent): %v", err)
	}
	discoveryReportRequireContract(t, absent)

	// Same checkout, but AGENTS.md is a directory where a file belongs: the
	// search cannot complete, so the outcome is a gap, not absence.
	unreadableDir, _ := discoveryReportCheckout(t, files)
	if err := os.Mkdir(filepath.Join(unreadableDir, "AGENTS.md"), 0o755); err != nil {
		t.Fatalf("create AGENTS.md directory: %v", err)
	}
	unreadableRepo := absentRepo
	unreadableFleet, _ := discoveryReportFleet(t, unreadableRepo, unreadableDir)
	unreadable, err := unreadableFleet.discoverRepo(context.Background(), unreadableRepo, nil)
	if err != nil {
		t.Fatalf("DiscoverRepo (unreadable): %v", err)
	}
	discoveryReportRequireContract(t, unreadable)

	if got := discoveryReportCapability(t, absent, "checkout_instructions").State; got != DiscoveryCapabilitySupported {
		t.Errorf("absent instructions state = %q, want supported", got)
	}
	absence := discoveryReportFactByValue(t, absent.Facts, "absent")
	if absence.Section != "instructions" || absence.Origin != "detected" {
		t.Errorf("absence fact = %+v, want a detected instructions fact", absence)
	}
	for _, gap := range absent.Gaps {
		if gap.Key == "checkout_instructions" {
			t.Errorf("absent instructions produced a gap: %+v", gap)
		}
	}

	if got := discoveryReportCapability(t, unreadable, "checkout_instructions").State; got == DiscoveryCapabilitySupported {
		t.Errorf("unreadable instructions state = %q, want a non-supported state", got)
	}
	gap := discoveryReportGap(t, unreadable, "checkout_instructions")
	if !strings.Contains(gap.Detail, "AGENTS.md") {
		t.Errorf("gap detail = %q, want it to name the unreadable target", gap.Detail)
	}
	if strings.Contains(gap.Detail, "absent") {
		t.Errorf("gap detail = %q, must not read as absence", gap.Detail)
	}
	for _, fact := range unreadable.Facts {
		if fact.Section == "instructions" && fact.Value == "absent" {
			t.Errorf("unreadable instructions produced an absence fact: %+v", fact)
		}
	}

	// The two responses must be textually distinct in the instructions
	// section: absence is a fact, unreadability is a gap.
	absentJSON, err := json.Marshal(absent)
	if err != nil {
		t.Fatalf("marshal absent report: %v", err)
	}
	unreadableJSON, err := json.Marshal(unreadable)
	if err != nil {
		t.Fatalf("marshal unreadable report: %v", err)
	}
	absenceMark := `"section":"instructions","kind":"instructions","value":"absent"`
	if !strings.Contains(string(absentJSON), absenceMark) {
		t.Errorf("absent report does not carry the instructions absence fact: %s", absentJSON)
	}
	if strings.Contains(string(unreadableJSON), `"value":"absent"`) {
		t.Errorf("unreadable report carries an absence fact: %s", unreadableJSON)
	}
	discoveryReportRequireNoCredential(t, absent)
	discoveryReportRequireNoCredential(t, unreadable)
}

// discoveryReportTreeDigest hashes every path and byte below root, so a report
// that writes anything into the checkout changes the digest.
func discoveryReportTreeDigest(t *testing.T, root string) string {
	t.Helper()
	digest := sha256.New()
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		fmt.Fprintf(digest, "%s\x00", rel)
		if entry.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		digest.Write(data)
		return nil
	})
	if err != nil {
		t.Fatalf("digest %s: %v", root, err)
	}
	return hex.EncodeToString(digest.Sum(nil))
}

// TestDiscoverReportRecommendationsAreAdvisory requires a missing environment
// and missing instructions to become recommendations, with nothing written to
// the repository or the workspace.
func TestDiscoverReportRecommendationsAreAdvisory(t *testing.T) {
	dir := t.TempDir()
	discoveryWriteFile(t, dir, "src/main.go", []byte("package main\n"))
	discoveryWriteFile(t, dir, "docs/README.md", []byte("# docs\n"))
	discoveryWriteFile(t, dir, "go.mod", []byte("module example.com/widgets\n"))
	discoveryWriteFile(t, dir, ".github/workflows/ci.yml", []byte("on: push\n"))

	repo := Repo{ID: "repo-1", URL: "https://forge.example.com/acme/widgets.git", OwnerID: "local"}
	fleet, ws := discoveryReportFleet(t, repo, dir)

	digestBefore := discoveryReportTreeDigest(t, dir)
	workspaceBefore, err := os.ReadFile(ws.path)
	if err != nil {
		t.Fatalf("read workspace: %v", err)
	}
	projectsBefore := len(ws.Projects())

	report, err := fleet.discoverRepo(context.Background(), repo, nil)
	if err != nil {
		t.Fatalf("DiscoverRepo: %v", err)
	}
	discoveryReportRequireContract(t, report)

	sections := make(map[string]DiscoveryRecommendation, len(report.Recommended))
	for _, recommendation := range report.Recommended {
		if recommendation.Origin != "recommended" {
			t.Errorf("recommendation %+v origin = %q, want recommended", recommendation, recommendation.Origin)
		}
		if recommendation.Reason == "" {
			t.Errorf("recommendation %+v has no reason", recommendation)
		}
		sections[recommendation.Section] = recommendation
	}
	environment, ok := sections["environment"]
	if !ok {
		t.Fatalf("no environment recommendation in %+v", report.Recommended)
	}
	if environment.Kind != "environment" || environment.Value == "" {
		t.Errorf("environment recommendation = %+v, want a suggested environment definition", environment)
	}
	instructions, ok := sections["instructions"]
	if !ok {
		t.Errorf("no instructions recommendation in %+v", report.Recommended)
	} else if !strings.Contains(instructions.Value, "AGENTS.md") {
		t.Errorf("instructions recommendation = %+v, want it to suggest AGENTS.md", instructions)
	}

	if after := discoveryReportTreeDigest(t, dir); after != digestBefore {
		t.Errorf("the checkout changed: digest %s -> %s", digestBefore, after)
	}
	workspaceAfter, err := os.ReadFile(ws.path)
	if err != nil {
		t.Fatalf("read workspace after discovery: %v", err)
	}
	if string(workspaceAfter) != string(workspaceBefore) {
		t.Errorf("the workspace changed:\n before %s\n after  %s", workspaceBefore, workspaceAfter)
	}
	if got := len(ws.Projects()); got != projectsBefore {
		t.Errorf("projects = %d, want %d: discovery creates no project", got, projectsBefore)
	}
	if got := len(ws.Agents()); got != 0 {
		t.Errorf("agents = %d, want 0: discovery starts nothing", got)
	}
}

// TestDiscoverReportRevisionAdvancesWithDefaultBranch pins both revision
// sources: forge facts follow the observed default-branch head, while the
// top-level revision and branch stay with the linked checkout, and nothing is
// fetched to learn either.
func TestDiscoverReportRevisionAdvancesWithDefaultBranch(t *testing.T) {
	dir, head := discoveryReportCheckout(t, map[string]string{
		"AGENTS.md": "# agents\n",
		"go.mod":    "module example.com/widgets\n",
	})
	headBefore, statusBefore, treeBefore := discoverySnapshot(t, dir)

	run := func(sha string) *DiscoveryReport {
		t.Helper()
		srv, _ := discoveryRecordingServer(t, discoveryReportGitHub{branchSHA: sha}.handler(t))
		repo := discoveryTestRepo("github", srv.URL)
		fleet, _ := discoveryReportFleet(t, repo, dir)
		report, err := fleet.DiscoverRepo(context.Background(), repo)
		if err != nil {
			t.Fatalf("DiscoverRepo (%s): %v", sha, err)
		}
		discoveryReportRequireContract(t, report)
		return report
	}

	first := run("sha-main-1")
	second := run("sha-main-2")

	for _, fact := range discoveryReportForgeFacts(first) {
		if fact.Revision != "sha-main-1" {
			t.Fatalf("first report forge fact %+v revision = %q, want sha-main-1", fact, fact.Revision)
		}
	}
	advanced := false
	for _, fact := range discoveryReportForgeFacts(second) {
		if fact.Revision != "sha-main-2" {
			t.Fatalf("second report forge fact %+v revision = %q, want sha-main-2", fact, fact.Revision)
		}
		if fact.Kind == "default_branch" {
			advanced = true
		}
	}
	if !advanced {
		t.Fatal("the second report lost its default-branch fact")
	}
	for _, report := range []*DiscoveryReport{first, second} {
		if report.Revision != head {
			t.Errorf("revision = %q, want the checkout HEAD %q (checkout wins)", report.Revision, head)
		}
		if report.Branch != "main" {
			t.Errorf("branch = %q, want the checkout branch main", report.Branch)
		}
		for _, fact := range report.Facts {
			if strings.HasPrefix(fact.Source, "checkout:") && fact.Revision != head {
				t.Errorf("checkout fact %+v revision = %q, want %q", fact, fact.Revision, head)
			}
		}
	}

	headAfter, statusAfter, treeAfter := discoverySnapshot(t, dir)
	if headAfter != headBefore {
		t.Errorf("HEAD moved: %q -> %q", headBefore, headAfter)
	}
	if statusAfter != statusBefore {
		t.Errorf("status changed:\n before %q\n after  %q", statusBefore, statusAfter)
	}
	if !reflect.DeepEqual(treeBefore, treeAfter) {
		t.Errorf("working tree changed:\n before %v\n after  %v", treeBefore, treeAfter)
	}
}
