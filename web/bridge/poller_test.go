package bridge

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// registerWatchingRepo registers a repo with watch enabled and a label.
func registerWatchingRepo(t *testing.T, f *Fleet, repoID, label string) {
	t.Helper()
	repo := Repo{
		ID:         repoID,
		URL:        "https://github.com/you/r.git",
		Branch:     "main",
		CredRef:    "pat1",
		OwnerID:    DefaultOwnerID,
		Forge:      "github",
		APIBase:    "",
		Watch:      true,
		WatchLabel: label,
	}
	if err := f.ws.PutRepo(repo); err != nil {
		t.Fatal(err)
	}
	f.creds = NewCredentialStore([]Credential{{
		ID:      "pat1",
		Kind:    "pat",
		OwnerID: DefaultOwnerID,
		EnvVar:  "TEST_PAT",
	}})
	t.Setenv("TEST_PAT", "test-token")
}

// testFleetPointedAt builds a fleet whose forge points at the given
// server, for rate-limit testing.
func testFleetPointedAt(t *testing.T, srv *httptest.Server) *Fleet {
	t.Helper()
	f := testFleetWithGate(t, gateResult{OK: true})
	return f
}

func TestPollSubmitsALabelledIssueExactlyOnce(t *testing.T) {
	f, srv := testFleetWithIssueForge(t)
	defer srv.Close()
	stubGitForForge(t, f)
	registerWatchingRepo(t, f, "r1", "marshal")
	// Point the repo's APIBase at the test server.
	r, _ := f.ws.Repo("r1")
	r.APIBase = srv.URL
	if err := f.ws.PutRepo(r); err != nil {
		t.Fatal(err)
	}

	f.pollOnce(context.Background())
	if got := len(f.ws.Pending()); got != 1 {
		t.Fatalf("after first poll: %d pending, want 1", got)
	}

	// The same issue, still labelled, must not be submitted again — a
	// touched issue reappears in a `since` query.
	f.pollOnce(context.Background())
	if got := len(f.ws.Pending()); got != 1 {
		t.Fatalf("after second poll: %d pending, want 1 — the issue was resubmitted", got)
	}
}

func TestPollSkipsUnwatchedRepos(t *testing.T) {
	f, srv := testFleetWithIssueForge(t)
	defer srv.Close()
	stubGitForForge(t, f)
	registerPATRepoWithAPIBase(t, f, "r1", srv.URL) // Watch is false

	f.pollOnce(context.Background())
	if len(f.ws.Pending()) != 0 {
		t.Fatal("an unwatched repo was polled; watching must be opt-in")
	}
}

func TestPollRecordsItsErrorOnTheRepo(t *testing.T) {
	f, srv := testFleetWithFailingForge(t)
	defer srv.Close()
	stubGitForForge(t, f)
	registerWatchingRepo(t, f, "r1", "marshal")
	r, _ := f.ws.Repo("r1")
	r.APIBase = srv.URL
	if err := f.ws.PutRepo(r); err != nil {
		t.Fatal(err)
	}

	f.pollOnce(context.Background())
	r, _ = f.ws.Repo("r1")
	if r.LastPollErr == "" {
		t.Fatal("a failing poll left no error; a dead watcher looks like 'no issues'")
	}
	if r.LastPolled.IsZero() {
		t.Fatal("LastPolled was not recorded on failure")
	}
}

func TestPollClearsAStaleErrorOnSuccess(t *testing.T) {
	f, srv := testFleetWithIssueForge(t)
	defer srv.Close()
	stubGitForForge(t, f)
	registerWatchingRepo(t, f, "r1", "marshal")
	r, _ := f.ws.Repo("r1")
	r.APIBase = srv.URL
	r.LastPollErr = "previous failure"
	if err := f.ws.PutRepo(r); err != nil {
		t.Fatal(err)
	}

	f.pollOnce(context.Background())
	got, _ := f.ws.Repo("r1")
	if got.LastPollErr != "" {
		t.Fatalf("a successful poll left a stale error: %q", got.LastPollErr)
	}
}

func TestPollBacksOffOnRateLimit(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	f := testFleetPointedAt(t, srv)
	stubGitForForge(t, f)
	registerWatchingRepo(t, f, "r1", "marshal")
	r, _ := f.ws.Repo("r1")
	r.APIBase = srv.URL
	if err := f.ws.PutRepo(r); err != nil {
		t.Fatal(err)
	}

	f.pollOnce(context.Background())

	// The Retry-After: 120 header must set a 120-second backoff, not
	// the default 60. Verify the exact "not before" time.
	f.rateMu.Lock()
	notBefore := f.rateLimits["r1"]
	f.rateMu.Unlock()
	if got := time.Until(notBefore); got < 100*time.Second || got > 130*time.Second {
		t.Fatalf("backoff = %v, want ~120s (from Retry-After header)", got)
	}

	f.pollOnce(context.Background())

	// The second poll must respect the backoff rather than hammering.
	if calls.Load() > 1 {
		t.Fatalf("made %d calls despite a Retry-After; watchers share one token's budget", calls.Load())
	}
}
