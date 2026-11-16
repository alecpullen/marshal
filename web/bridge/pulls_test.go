package bridge

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
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
