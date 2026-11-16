package bridge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"marshal/web/bridge/contract"
)

// pullFixture is one record with evidence in every field the store keeps, so a
// round trip through the state file proves none is dropped.
func pullFixture() contract.PullRecord {
	return contract.PullRecord{
		RepoID:      "repo-1",
		ProjectRoot: "/home/u/project",
		Number:      7,
		URL:         "https://github.com/you/r/pull/7",
		Title:       "Fix the flaky test",
		HeadRef:     "marshal/agent-a1",
		BaseRef:     "main",
		HeadSHA:     "abc123",
		Draft:       true,
		Additions:   12,
		Deletions:   3,
		Origin:      contract.PullOriginAgent,
		AgentID:     "a1",
		State:       contract.PullInProgress,
		Checks: []contract.PullCheck{
			{Name: "test / unit", State: contract.CheckRunning, URL: "https://example.test/check/1"},
		},
		Reviews:    "no reviews yet",
		ObservedAt: 1700000000000,
		UpdatedAt:  1700000000000,
	}
}

func TestPullRecordRoundTripsThroughFleetState(t *testing.T) {
	f := testFleet(t)
	if err := f.recordPull(pullFixture()); err != nil {
		t.Fatalf("recordPull: %v", err)
	}

	// The file carries the record and the current version.
	data, err := os.ReadFile(f.ws.path)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	var onDisk struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(data, &onDisk); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	if onDisk.Version != workspaceVersion {
		t.Fatalf("state version = %d, want %d", onDisk.Version, workspaceVersion)
	}

	// A fresh store over the same file sees the record.
	reopened := NewWorkspace(f.ws.path)
	if backup, err := reopened.Load(); err != nil || backup != "" {
		t.Fatalf("Load = %q, %v", backup, err)
	}
	want := pullFixture()
	want.ID = pullID("repo-1", 7)
	got, ok := reopened.Pull(want.ID)
	if !ok {
		t.Fatalf("record %q did not survive the reload", want.ID)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("record round trip\n got: %+v\nwant: %+v", got, want)
	}
}

func TestVersion11StateLoadsWithNoPulls(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fleet.json")
	const v11 = `{"version":11,"projects":["/home/u/p"],` +
		`"repos":[{"id":"/home/u/p","url":"/home/u/p","ownerId":"local"}],` +
		`"agents":[],"schedules":[],"statusLinks":[]}`
	if err := os.WriteFile(path, []byte(v11), 0o600); err != nil {
		t.Fatal(err)
	}

	ws := NewWorkspace(path)
	if backup, err := ws.Load(); err != nil || backup != "" {
		t.Fatalf("Load = %q, %v", backup, err)
	}
	if got := ws.Pulls(); len(got) != 0 {
		t.Fatalf("a version 11 file loaded %d pull records, want none", len(got))
	}
	if got := ws.PullAsks(); len(got) != 0 {
		t.Fatalf("a version 11 file loaded %d asks, want none", len(got))
	}

	// Saving from a version 11 file writes the current version and no
	// records, so an upgrade is not itself a change to the file's meaning.
	if err := ws.save(); err != nil {
		t.Fatalf("save: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var onDisk struct {
		Version int             `json:"version"`
		Pulls   json.RawMessage `json:"pulls"`
	}
	if err := json.Unmarshal(data, &onDisk); err != nil {
		t.Fatal(err)
	}
	if onDisk.Version != workspaceVersion {
		t.Fatalf("re-saved version = %d, want %d", onDisk.Version, workspaceVersion)
	}
	if len(onDisk.Pulls) != 0 {
		t.Fatalf("a loaded version 11 file wrote %s, want no records", onDisk.Pulls)
	}
}

func TestRecordPullIsIdempotentByID(t *testing.T) {
	f := testFleet(t)
	first := pullFixture()
	if err := f.recordPull(first); err != nil {
		t.Fatalf("recordPull: %v", err)
	}
	second := first
	second.Title = "A newer title"
	second.HeadSHA = "def456"
	if err := f.recordPull(second); err != nil {
		t.Fatalf("recordPull again: %v", err)
	}

	recs := f.pulls()
	if len(recs) != 1 {
		t.Fatalf("recorded %d records for one pull request, want 1: %+v", len(recs), recs)
	}
	if recs[0].ID != "repo-1#7" {
		t.Fatalf("record id = %q, want repo-1#7", recs[0].ID)
	}
	if recs[0].Title != "A newer title" || recs[0].HeadSHA != "def456" {
		t.Fatalf("the second recording did not replace the first: %+v", recs[0])
	}
}

// pullForge is a GitHub-shaped forge a test drives directly: the open pull
// requests it answers with, the check runs for each head, and the log of the
// job behind a failed check. Every request it serves is recorded, so a test can
// prove a record was never polled.
type pullForge struct {
	*httptest.Server
	mu       sync.Mutex
	open     []map[string]any
	checks   map[string][]map[string]any
	log      string
	requests []string
	// checksStatus, when set, is the status the check-runs endpoint answers
	// with instead of the checks: a read that failed.
	checksStatus int
}

func newPullForge(t *testing.T) *pullForge {
	t.Helper()
	pf := &pullForge{checks: map[string][]map[string]any{}, log: "the failing output\n"}
	pf.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pf.mu.Lock()
		defer pf.mu.Unlock()
		pf.requests = append(pf.requests, r.URL.Path)
		switch {
		case r.URL.Path == "/repos/you/r/pulls":
			_ = json.NewEncoder(w).Encode(pf.open)
		case strings.HasPrefix(r.URL.Path, "/repos/you/r/commits/") && strings.HasSuffix(r.URL.Path, "/check-runs"):
			if pf.checksStatus != 0 {
				w.WriteHeader(pf.checksStatus)
				return
			}
			sha := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/repos/you/r/commits/"), "/check-runs")
			_ = json.NewEncoder(w).Encode(map[string]any{"check_runs": pf.checks[sha]})
		case strings.Contains(r.URL.Path, "/actions/jobs/") && strings.HasSuffix(r.URL.Path, "/logs"):
			_, _ = w.Write([]byte(pf.log))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(pf.Close)
	return pf
}

// failChecks makes the check-runs endpoint answer with status; zero clears it.
func (pf *pullForge) failChecks(status int) {
	pf.mu.Lock()
	defer pf.mu.Unlock()
	pf.checksStatus = status
}

// setOpen is the open pull request list the forge answers with.
func (pf *pullForge) setOpen(number int, head string, draft bool) {
	pf.mu.Lock()
	defer pf.mu.Unlock()
	pf.open = []map[string]any{{
		"number":   number,
		"title":    "A pull request",
		"html_url": "https://github.com/you/r/pull/" + strconv.Itoa(number),
		"draft":    draft,
		"head":     map[string]any{"ref": "marshal/a1", "sha": head},
		"base":     map[string]any{"ref": "main"},
		"user":     map[string]any{"login": "you"},
	}}
}

// setChecks is the check runs the forge reports for one head.
func (pf *pullForge) setChecks(head string, checks ...map[string]any) {
	pf.mu.Lock()
	defer pf.mu.Unlock()
	pf.checks[head] = checks
}

// called reports whether the forge served any request at all.
func (pf *pullForge) called() bool {
	pf.mu.Lock()
	defer pf.mu.Unlock()
	return len(pf.requests) > 0
}

// pullTestFleet is a fleet whose registered repo points at pf with a PAT.
func pullTestFleet(t *testing.T, pf *pullForge) *Fleet {
	t.Helper()
	return pullTestFleetOn(t, testFleet(t), pf)
}

// pullTestFleetOn points an existing fleet's repo r1 at pf with a PAT.
func pullTestFleetOn(t *testing.T, f *Fleet, pf *pullForge) *Fleet {
	t.Helper()
	if err := f.ws.PutRepo(Repo{ID: "r1", URL: "https://github.com/you/r.git", Branch: "main",
		CredRef: "pat1", OwnerID: DefaultOwnerID, Forge: "github", APIBase: pf.URL}); err != nil {
		t.Fatal(err)
	}
	f.creds = NewCredentialStore([]Credential{{ID: "pat1", Kind: "pat", OwnerID: DefaultOwnerID, EnvVar: "TEST_PAT"}})
	t.Setenv("TEST_PAT", "test-token")
	return f
}

// pullDeltas is every "pull" delta the fleet stream carries.
func pullDeltas(f *Fleet) []pullDelta {
	var out []pullDelta
	for _, e := range f.fleetLog.Tail(fleetStreamKey) {
		var d pullDelta
		if json.Unmarshal(e.Data, &d) == nil && d.Kind == "pull" {
			out = append(out, d)
		}
	}
	return out
}

// factoryPullItem journals one item whose open pull request is number, with a
// round that observed checks on head.
func factoryPullItem(t *testing.T, number int, head string, checks []FactoryCheck) (*FactoryStore, FactoryItem) {
	t.Helper()
	s, err := OpenFactoryStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	now := time.Now().UTC()
	created, err := s.Create(factoryReq(), now)
	if err != nil {
		t.Fatal(err)
	}
	item, err := s.Update(created.ID, "item-pull", created.Version, json.RawMessage(`{"kind":"item-pull"}`),
		func(i *FactoryItem) error {
			i.RepoID, i.ProjectRoot, i.Title = "r1", "/p", "Factory work"
			// A checking item is one whose pull request the controller is
			// watching: its round carries the CI evidence.
			i.State = "checking"
			i.PRs = []FactoryPR{{Number: number, URL: "https://github.com/you/r/pull/" + strconv.Itoa(number),
				HeadRef: "factory/x", BaseRef: "main", HeadSHA: head, State: "open", Draft: true}}
			i.Rounds = []FactoryRound{{Number: 1, HeadSHA: head,
				CI: &FactoryCI{HeadSHA: head, Complete: true, Checks: checks}}}
			return nil
		}, now)
	if err != nil {
		t.Fatal(err)
	}
	return s, item
}

func TestMovedHeadClearsChecks(t *testing.T) {
	pf := newPullForge(t)
	pf.setOpen(7, "newhead", false)
	pf.setChecks("newhead") // the new head has not reported yet
	f := pullTestFleet(t, pf)
	if err := f.recordPull(contract.PullRecord{RepoID: "r1", Number: 7, HeadSHA: "oldhead",
		Checks:     []contract.PullCheck{{Name: "build", State: contract.CheckPassed, URL: "https://ci/1"}},
		ObservedAt: 1}); err != nil {
		t.Fatal(err)
	}

	f.observePulls(context.Background())

	rec, ok := f.pull("r1#7")
	if !ok {
		t.Fatal("the open record was removed")
	}
	if rec.HeadSHA != "newhead" {
		t.Fatalf("head = %q, want newhead", rec.HeadSHA)
	}
	if len(rec.Checks) != 0 {
		t.Fatalf("checks = %+v; the old head's checks were carried over", rec.Checks)
	}
	if rec.State != contract.PullInProgress {
		t.Fatalf("state = %q, want in_progress for a head with no evidence", rec.State)
	}
}

// A read that failed is not a credential that cannot read: the record must not
// claim its checks are unavailable, and the next pass reads them.
func TestATransientCheckReadIsNotUnavailable(t *testing.T) {
	pf := newPullForge(t)
	pf.setOpen(7, "abc123", false)
	pf.failChecks(http.StatusBadGateway)
	f := pullTestFleet(t, pf)
	if err := f.recordPull(contract.PullRecord{RepoID: "r1", Number: 7, HeadSHA: "abc123",
		Origin: contract.PullOriginAgent, ObservedAt: 1}); err != nil {
		t.Fatal(err)
	}

	f.observePulls(context.Background())

	rec, ok := f.pull("r1#7")
	if !ok {
		t.Fatal("the open record was removed")
	}
	if rec.Unavailable != "" {
		t.Fatalf("a transient check read was recorded as unavailable: %q", rec.Unavailable)
	}
	if rec.State != contract.PullInProgress {
		t.Fatalf("state = %q, want in_progress", rec.State)
	}

	// The next pass reads the checks that had not reported.
	pf.failChecks(0)
	pf.setChecks("abc123", map[string]any{"id": 1, "name": "lint", "status": "completed", "conclusion": "success"})
	if _, err := f.updatePull("r1#7", func(r *contract.PullRecord) error { r.ObservedAt = 1; return nil }); err != nil {
		t.Fatal(err)
	}
	f.observePulls(context.Background())
	if rec, _ := f.pull("r1#7"); len(rec.Checks) != 1 || rec.State != contract.PullReady {
		t.Fatalf("the next pass did not read the checks: %+v", rec)
	}
}

func TestFactoryPullIsNotPolled(t *testing.T) {
	pf := newPullForge(t)
	f := pullTestFleet(t, pf)
	s, item := factoryPullItem(t, 9, "abc123", []FactoryCheck{{
		Key: FactoryCheckKey{Kind: "check", Name: "test"}, SHA: "abc123",
		Status: "completed", Conclusion: "failure", URL: "https://ci/9",
	}})
	// The record exists first, so wiring the controller's backfill leaves it.
	if err := f.recordPull(contract.PullRecord{RepoID: "r1", Number: 9, HeadSHA: "abc123",
		Origin: contract.PullOriginFactory, FactoryItem: item.ID, ObservedAt: 1}); err != nil {
		t.Fatal(err)
	}
	f.SetFactoryController(NewFactoryController(s, &loopEffects{store: s}, f.now))

	f.observePulls(context.Background())

	if pf.called() {
		t.Fatalf("a factory item's record was polled: %v", pf.requests)
	}
	rec, ok := f.pull("r1#9")
	if !ok {
		t.Fatal("the factory record was removed")
	}
	if len(rec.Checks) != 1 || rec.Checks[0].Name != "test" || rec.Checks[0].State != contract.CheckFailed {
		t.Fatalf("checks = %+v; want the item's own observation", rec.Checks)
	}
	if rec.State != contract.PullFailing {
		t.Fatalf("state = %q, want failing", rec.State)
	}
}

func TestClosedPullIsRemoved(t *testing.T) {
	pf := newPullForge(t)
	// The open list is the forge's answer, and this pull request is not in it.
	f := pullTestFleet(t, pf)
	if err := f.recordPull(contract.PullRecord{RepoID: "r1", Number: 7, HeadSHA: "abc123",
		Checks:     []contract.PullCheck{{Name: "build", State: contract.CheckPassed}},
		ObservedAt: 1}); err != nil {
		t.Fatal(err)
	}

	f.observePulls(context.Background())

	if _, ok := f.pull("r1#7"); ok {
		t.Fatal("a closed or merged pull request stayed in the index")
	}
	var removed string
	for _, d := range pullDeltas(f) {
		if d.Removed != "" {
			removed = d.Removed
		}
	}
	if removed != "r1#7" {
		t.Fatalf("removal delta = %q, want r1#7", removed)
	}
}

func TestDerivePullStateReady(t *testing.T) {
	rec := contract.PullRecord{Checks: []contract.PullCheck{
		{Name: "build", State: contract.CheckPassed},
		{Name: "test", State: contract.CheckPassed},
	}}
	if got := derivePullState(rec, false); got != contract.PullReady {
		t.Fatalf("state = %q, want ready", got)
	}
	// A draft with every check passed is not ready for review.
	rec.Draft = true
	if got := derivePullState(rec, false); got != contract.PullInProgress {
		t.Fatalf("draft state = %q, want in_progress", got)
	}
	// The factory's own verdict makes its draft ready.
	if got := derivePullState(rec, true); got != contract.PullReady {
		t.Fatalf("factory-ready state = %q, want ready", got)
	}
}

func TestDerivePullStateFailing(t *testing.T) {
	rec := contract.PullRecord{Checks: []contract.PullCheck{
		{Name: "build", State: contract.CheckPassed},
		{Name: "test", State: contract.CheckFailed},
	}}
	if got := derivePullState(rec, false); got != contract.PullFailing {
		t.Fatalf("state = %q, want failing", got)
	}
}

func TestDerivePullStateInProgress(t *testing.T) {
	for _, checks := range [][]contract.PullCheck{
		nil,
		{{Name: "build", State: contract.CheckRunning}},
		{{Name: "build", State: contract.CheckPending}},
	} {
		if got := derivePullState(contract.PullRecord{Checks: checks}, false); got != contract.PullInProgress {
			t.Fatalf("state for %+v = %q, want in_progress", checks, got)
		}
	}
}

func TestDerivePullStateBeingFixed(t *testing.T) {
	rec := contract.PullRecord{
		Fix:    &contract.PullFix{ID: "f1", Check: "test", HeadSHA: "abc"},
		Checks: []contract.PullCheck{{Name: "test", State: contract.CheckFailed}},
	}
	if got := derivePullState(rec, false); got != contract.PullBeingFixed {
		t.Fatalf("state = %q, want being_fixed", got)
	}
}

func TestDerivePullStateChecksUnavailable(t *testing.T) {
	rec := contract.PullRecord{Unavailable: "checks unavailable: an ssh credential cannot call the forge's API"}
	if got := derivePullState(rec, false); got != contract.PullChecksUnavailable {
		t.Fatalf("state = %q, want checks_unavailable", got)
	}
}

func TestPullsIndexListsOpenRecords(t *testing.T) {
	f := testFleet(t)
	if err := f.recordPull(contract.PullRecord{RepoID: "r1", Number: 7, Title: "One",
		Checks: []contract.PullCheck{{Name: "build", State: contract.CheckPassed}},
		Origin: contract.PullOriginAgent, ObservedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if err := f.recordPull(contract.PullRecord{RepoID: "r1", Number: 8, Title: "Two",
		Checks: []contract.PullCheck{{Name: "build", State: contract.CheckFailed}},
		Origin: contract.PullOriginAgent, ObservedAt: 1}); err != nil {
		t.Fatal(err)
	}

	srv := &Server{fleet: f}
	w := httptest.NewRecorder()
	srv.listPulls(w, httptest.NewRequest("GET", "/api/pulls", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body)
	}
	var body struct {
		Pulls []contract.PullRecord `json:"pulls"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Pulls) != 2 {
		t.Fatalf("index = %+v, want two records", body.Pulls)
	}
	if body.Pulls[0].ID != "r1#7" || body.Pulls[1].ID != "r1#8" {
		t.Fatalf("index order = %q, %q", body.Pulls[0].ID, body.Pulls[1].ID)
	}
	if body.Pulls[0].State != contract.PullReady || body.Pulls[1].State != contract.PullFailing {
		t.Fatalf("states = %q, %q", body.Pulls[0].State, body.Pulls[1].State)
	}
}

func TestPullDetailCarriesLogTail(t *testing.T) {
	pf := newPullForge(t)
	pf.setOpen(7, "abc123", false)
	pf.setChecks("abc123", map[string]any{
		"id": 412, "name": "lint", "status": "completed", "conclusion": "failure",
		"html_url":    "https://github.com/you/r/runs/412",
		"details_url": "https://github.com/you/r/actions/runs/412/job/9002",
	})
	f := pullTestFleet(t, pf)
	if err := f.recordPull(contract.PullRecord{RepoID: "r1", Number: 7, HeadSHA: "abc123",
		Checks:     []contract.PullCheck{{Name: "lint", State: contract.CheckFailed, URL: "https://github.com/you/r/runs/412"}},
		ObservedAt: 1}); err != nil {
		t.Fatal(err)
	}

	detail := f.pullDetail(context.Background(), "r1#7")
	if len(detail.Checks) != 1 {
		t.Fatalf("checks = %+v", detail.Checks)
	}
	if !strings.Contains(detail.Checks[0].LogTail, "the failing output") {
		t.Fatalf("log tail = %q", detail.Checks[0].LogTail)
	}
	// The index itself never carries the log.
	if rec, _ := f.pull("r1#7"); len(rec.Checks) == 1 && rec.Checks[0].LogTail != "" {
		t.Fatalf("the stored record carries a log tail: %+v", rec.Checks[0])
	}
}

func TestPullChangeEmitsDelta(t *testing.T) {
	f := testFleet(t)
	if err := f.recordPull(contract.PullRecord{RepoID: "r1", Number: 7, Title: "One",
		Origin: contract.PullOriginAgent, ObservedAt: 1}); err != nil {
		t.Fatal(err)
	}
	deltas := pullDeltas(f)
	if len(deltas) != 1 {
		t.Fatalf("pull deltas = %d, want 1", len(deltas))
	}
	if deltas[0].Pull == nil || deltas[0].Pull.ID != "r1#7" {
		t.Fatalf("delta = %+v", deltas[0])
	}
	if deltas[0].SessionID == "" {
		t.Fatal("the delta carries no stream session")
	}
}

func TestPullNumberFromURL(t *testing.T) {
	cases := map[string]int{
		"https://github.com/you/r/pull/7":         7,
		"https://github.com/you/r/pull/7/":        7,
		"https://code.example.com/you/r/pulls/12": 12,
		" https://gitea.example.com/o/r/pulls/3 ": 3,
	}
	for in, want := range cases {
		got, ok := pullNumberFromURL(in)
		if !ok || got != want {
			t.Errorf("pullNumberFromURL(%q) = %d, %v; want %d", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "https://github.com/you/r", "https://github.com/you/r/pull/"} {
		if n, ok := pullNumberFromURL(in); ok {
			t.Errorf("pullNumberFromURL(%q) = %d, want no number", in, n)
		}
	}
}

func TestExitRecordsThePull(t *testing.T) {
	f, srv := testFleetWithForge(t)
	defer srv.Close()
	id := spawnGitAgentWithPAT(t, f, srv.URL)

	res, err := f.Exit(context.Background(), id, ExitOptions{CommitMessage: "work"})
	if err != nil {
		t.Fatalf("Exit: %v", err)
	}
	if res.PRUrl == "" {
		t.Fatal("the exit opened no pull request")
	}

	rec, ok := f.pull(pullID("r1", 7))
	if !ok {
		t.Fatalf("no record for r1#7; records = %+v", f.pulls())
	}
	if rec.Origin != contract.PullOriginAgent || rec.AgentID != id {
		t.Fatalf("record origin/agent = %q/%q, want agent/%s", rec.Origin, rec.AgentID, id)
	}
	if rec.Number != 7 || rec.URL != res.PRUrl {
		t.Fatalf("record = %+v", rec)
	}
	// The exit knows the branch it pushed; the title comes from the agent's
	// name, so a nameless agent records none and the observer fills it in.
	if rec.HeadRef != res.Branch || rec.BaseRef != "main" {
		t.Fatalf("record head/base = %q/%q, want %q/main", rec.HeadRef, rec.BaseRef, res.Branch)
	}
}

func TestFactoryRecordsThePullOnce(t *testing.T) {
	f := testFleet(t)
	c := NewFactoryController(nil, nil, f.now)
	c.SetPullRecorder(f.recordFactoryPull)

	item := FactoryItem{ID: "item-1", RepoID: "repo-1", ProjectRoot: "/p", Title: "Work"}
	withPR := item
	withPR.PRs = []FactoryPR{{
		Number: 9, URL: "https://github.com/you/r/pull/9", HeadRef: "factory/item-1",
		BaseRef: "main", HeadSHA: "abc", Draft: true, State: "open",
	}}
	c.noteNewPulls(item, withPR)
	// The same completion applied again adds no second record.
	c.noteNewPulls(item, withPR)

	recs := f.pulls()
	if len(recs) != 1 {
		t.Fatalf("recorded %d records for one factory pull request, want 1: %+v", len(recs), recs)
	}
	rec := recs[0]
	if rec.ID != "repo-1#9" || rec.Origin != contract.PullOriginFactory || rec.FactoryItem != "item-1" {
		t.Fatalf("record = %+v", rec)
	}
	if rec.HeadRef != "factory/item-1" || rec.HeadSHA != "abc" || !rec.Draft {
		t.Fatalf("record lost the factory evidence: %+v", rec)
	}
}

func TestCIFixerRecordsThePull(t *testing.T) {
	f := testFleet(t)
	if err := f.ws.PutRepo(Repo{ID: "r1", URL: "https://github.com/you/r.git",
		OwnerID: DefaultOwnerID, Forge: "github"}); err != nil {
		t.Fatal(err)
	}

	// The fixer's fix ships through the same exit path as any agent, so the
	// record is written there — and it names the automation, not the worker.
	fixer := Agent{ID: "ci-1", Project: "/p", SourceRef: "r1", OwnerID: DefaultOwnerID, Origin: OriginCI}
	f.recordExitPull(fixer, "marshal/fix-ci", PR{Number: 3, URL: "https://github.com/you/r/pull/3"})
	rec, ok := f.pull("r1#3")
	if !ok {
		t.Fatalf("the CI fixer's pull request was not recorded: %+v", f.pulls())
	}
	if rec.Origin != contract.PullOriginAutomation || rec.Automation != "ci-fixer" {
		t.Fatalf("CI fixer record origin/automation = %q/%q", rec.Origin, rec.Automation)
	}

	// The review bot's pull request is the automation's too.
	bot := Agent{ID: "rb-1", Project: "/p", SourceRef: "r1", OwnerID: DefaultOwnerID, Origin: OriginReviewBot}
	f.recordExitPull(bot, "marshal/review", PR{Number: 4, URL: "https://github.com/you/r/pull/4"})
	rec, ok = f.pull("r1#4")
	if !ok {
		t.Fatalf("the review bot's pull request was not recorded: %+v", f.pulls())
	}
	if rec.Origin != contract.PullOriginAutomation || rec.Automation != "review-bot" {
		t.Fatalf("review bot record origin/automation = %q/%q", rec.Origin, rec.Automation)
	}
}

func TestBackfillAddsOnlyMissingRecords(t *testing.T) {
	f := testFleet(t)
	if err := f.ws.PutRepo(Repo{ID: "r1", URL: "https://github.com/you/r.git",
		OwnerID: DefaultOwnerID, Forge: "github"}); err != nil {
		t.Fatal(err)
	}
	// An agent that shipped before the bridge kept records.
	if err := f.ws.PutAgent(Agent{ID: "a1", Project: "/p", SourceRef: "r1", OwnerID: DefaultOwnerID,
		Origin: OriginUI, PRUrl: "https://github.com/you/r/pull/11", Branch: "marshal/a1"}); err != nil {
		t.Fatal(err)
	}
	// One record already exists, and a backfill must not overwrite it.
	if err := f.recordPull(contract.PullRecord{RepoID: "r1", Number: 11, Title: "already known",
		Origin: contract.PullOriginAgent}); err != nil {
		t.Fatal(err)
	}

	// A factory item with one open pull request and one merged one.
	s, err := OpenFactoryStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	now := time.Now().UTC()
	created, err := s.Create(factoryReq(), now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Update(created.ID, "item-prs", created.Version, json.RawMessage(`{"kind":"item-prs"}`),
		func(i *FactoryItem) error {
			i.RepoID, i.ProjectRoot, i.Title = "r1", "/p", "Factory work"
			i.PRs = []FactoryPR{
				{Number: 21, URL: "https://github.com/you/r/pull/21", HeadRef: "factory/x",
					BaseRef: "main", State: "open", Draft: true},
				{Number: 22, URL: "https://github.com/you/r/pull/22", State: "merged"},
			}
			return nil
		}, now); err != nil {
		t.Fatal(err)
	}
	f.SetFactoryController(NewFactoryController(s, &loopEffects{store: s}, f.now))
	// Wiring backfills; running it again changes nothing.
	f.backfillPulls()

	if rec, ok := f.pull("r1#11"); !ok || rec.Title != "already known" {
		t.Fatalf("the backfill overwrote an existing record: %+v, %v", rec, ok)
	}
	if rec, ok := f.pull("r1#21"); !ok || rec.Origin != contract.PullOriginFactory || rec.FactoryItem != created.ID {
		t.Fatalf("the factory's open pull request was not backfilled: %+v, %v", rec, ok)
	}
	if _, ok := f.pull("r1#22"); ok {
		t.Fatal("the backfill recorded a merged pull request")
	}
	if got := len(f.pulls()); got != 2 {
		t.Fatalf("recorded %d pull requests, want 2: %+v", got, f.pulls())
	}
}

// The poller may start before the factory controller is wired. Its start must
// not spend the factory backfill: the controller arrives later, and its items
// are recorded then.
func TestPollerStartBeforeTheControllerKeepsTheFactoryBackfill(t *testing.T) {
	f := testFleet(t)
	if err := f.ws.PutRepo(Repo{ID: "r1", URL: "https://github.com/you/r.git",
		OwnerID: DefaultOwnerID, Forge: "github"}); err != nil {
		t.Fatal(err)
	}
	// StartPoller's first act, with no controller to ask.
	f.backfillPulls()

	s, item := factoryPullItem(t, 21, "abc123", nil)
	f.SetFactoryController(NewFactoryController(s, &loopEffects{store: s}, f.now))

	rec, ok := f.pull("r1#21")
	if !ok {
		t.Fatal("wiring the controller after the poller started lost the factory backfill")
	}
	if rec.Origin != contract.PullOriginFactory || rec.FactoryItem != item.ID {
		t.Fatalf("record = %+v, want the factory's open pull request", rec)
	}
}
