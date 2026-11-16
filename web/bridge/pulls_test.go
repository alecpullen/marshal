package bridge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

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
