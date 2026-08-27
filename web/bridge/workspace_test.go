package bridge

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWorkspaceRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fleet.json")
	w := NewWorkspace(path)
	if _, err := w.Load(); err != nil {
		t.Fatalf("Load on missing file: %v", err)
	}
	if err := w.AddProject("/home/u/repo"); err != nil {
		t.Fatalf("AddProject: %v", err)
	}
	a := Agent{ID: "s1", Project: "/home/u/repo", Name: "fix bug", Mode: "edit", CreatedAt: time.Now()}
	if err := w.PutAgent(a); err != nil {
		t.Fatalf("PutAgent: %v", err)
	}

	reloaded := NewWorkspace(path)
	if _, err := reloaded.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := reloaded.Projects(); len(got) != 1 || got[0] != "/home/u/repo" {
		t.Fatalf("Projects() = %v", got)
	}
	got, ok := reloaded.Agent("s1")
	if !ok {
		t.Fatal("agent s1 missing after reload")
	}
	if got.Name != "fix bug" || got.Mode != "edit" {
		t.Fatalf("agent round-trip lost fields: %+v", got)
	}
}

func TestWorkspaceQuarantinesCorruptFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fleet.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	w := NewWorkspace(path)
	backup, err := w.Load()
	if err != nil {
		t.Fatalf("Load must not fail on corrupt file: %v", err)
	}
	if backup == "" {
		t.Fatal("Load must report the quarantine path")
	}
	if _, err := os.Stat(backup); err != nil {
		t.Fatalf("quarantined file missing: %v", err)
	}
	if len(w.Projects()) != 0 {
		t.Fatalf("expected empty projects after quarantine, got %v", w.Projects())
	}
	if err := w.AddProject("/home/u/repo"); err != nil {
		t.Fatalf("AddProject after quarantine: %v", err)
	}
}

func TestWorkspaceRemoveProjectDropsItsAgents(t *testing.T) {
	w := NewWorkspace(filepath.Join(t.TempDir(), "fleet.json"))
	if _, err := w.Load(); err != nil {
		t.Fatal(err)
	}
	_ = w.AddProject("/home/u/a")
	_ = w.AddProject("/home/u/b")
	_ = w.PutAgent(Agent{ID: "s1", Project: "/home/u/a"})
	_ = w.PutAgent(Agent{ID: "s2", Project: "/home/u/b"})

	if err := w.RemoveProject("/home/u/a"); err != nil {
		t.Fatalf("RemoveProject: %v", err)
	}
	if _, ok := w.Agent("s1"); ok {
		t.Error("agent of removed project must be dropped")
	}
	if _, ok := w.Agent("s2"); !ok {
		t.Error("agent of other project must survive")
	}
}

func TestWorkspaceMarkAllInterrupted(t *testing.T) {
	w := NewWorkspace(filepath.Join(t.TempDir(), "fleet.json"))
	if _, err := w.Load(); err != nil {
		t.Fatal(err)
	}
	_ = w.PutAgent(Agent{ID: "s1"})
	if err := w.MarkAllInterrupted(); err != nil {
		t.Fatalf("MarkAllInterrupted: %v", err)
	}
	got, _ := w.Agent("s1")
	if !got.Interrupted {
		t.Error("agent must be marked interrupted")
	}
}

func TestWorkspaceAddProjectRejectsRelative(t *testing.T) {
	w := NewWorkspace(filepath.Join(t.TempDir(), "fleet.json"))
	if _, err := w.Load(); err != nil {
		t.Fatal(err)
	}
	if err := w.AddProject("relative/path"); err == nil {
		t.Fatal("expected error for relative project root")
	}
}

func TestWorkspaceAddProjectIsIdempotent(t *testing.T) {
	w := NewWorkspace(filepath.Join(t.TempDir(), "fleet.json"))
	if _, err := w.Load(); err != nil {
		t.Fatal(err)
	}
	_ = w.AddProject("/home/u/repo")
	_ = w.AddProject("/home/u/repo")
	if got := w.Projects(); len(got) != 1 {
		t.Fatalf("expected 1 project, got %v", got)
	}
}

func TestLoadMigratesV1AgentsToV2(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fleet.json")
	v1 := `{"version":1,"projects":["/p"],"agents":[{"id":"a1","project":"/p","name":"one"}]}`
	if err := os.WriteFile(path, []byte(v1), 0o600); err != nil {
		t.Fatal(err)
	}

	ws := NewWorkspace(path)
	backup, err := ws.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if backup != "" {
		t.Fatalf("v1 file was quarantined to %q; it must migrate instead", backup)
	}

	agents := ws.Agents()
	if len(agents) != 1 {
		t.Fatalf("got %d agents, want 1", len(agents))
	}
	got := agents[0]
	if got.OwnerID != DefaultOwnerID {
		t.Errorf("OwnerID = %q, want %q", got.OwnerID, DefaultOwnerID)
	}
	if got.Origin != OriginUI {
		t.Errorf("Origin = %q, want %q", got.Origin, OriginUI)
	}
	if got.Profile.Image == "" {
		t.Error("migrated agent has no runtime profile image")
	}
}

func TestLoadStillQuarantinesUnknownVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fleet.json")
	if err := os.WriteFile(path, []byte(`{"version":99}`), 0o600); err != nil {
		t.Fatal(err)
	}
	ws := NewWorkspace(path)
	backup, err := ws.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if backup == "" {
		t.Fatal("a future version must be quarantined, not migrated")
	}
}

func TestLoadMigratesV2ProjectsToRepos(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fleet.json")
	v2 := `{"version":2,"projects":["/srv/code/marshal"],"agents":[]}`
	if err := os.WriteFile(path, []byte(v2), 0o600); err != nil {
		t.Fatal(err)
	}

	ws := NewWorkspace(path)
	backup, err := ws.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if backup != "" {
		t.Fatalf("v2 file was quarantined to %q; it must migrate", backup)
	}

	repos := ws.Repos()
	if len(repos) != 1 {
		t.Fatalf("got %d repos, want 1 promoted from projects", len(repos))
	}
	if repos[0].URL != "/srv/code/marshal" {
		t.Errorf("URL = %q, want the original project path", repos[0].URL)
	}
	if repos[0].OwnerID != DefaultOwnerID {
		t.Errorf("OwnerID = %q, want %q", repos[0].OwnerID, DefaultOwnerID)
	}
}

func TestV1StillMigratesThroughToV3(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fleet.json")
	v1 := `{"version":1,"projects":["/p"],"agents":[{"id":"a1","project":"/p"}]}`
	if err := os.WriteFile(path, []byte(v1), 0o600); err != nil {
		t.Fatal(err)
	}
	ws := NewWorkspace(path)
	if _, err := ws.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	agents := ws.Agents()
	if len(agents) != 1 || agents[0].OwnerID != DefaultOwnerID {
		t.Fatalf("v1 agent did not migrate through v2: %+v", agents)
	}
	if len(ws.Repos()) != 1 {
		t.Fatal("v1 projects did not reach the v3 registry")
	}
}

func TestPutRepoRoundTrips(t *testing.T) {
	ws := NewWorkspace(filepath.Join(t.TempDir(), "fleet.json"))
	r := Repo{ID: "marshal", URL: "git@github.com:you/marshal.git",
		Branch: "main", CredRef: "gh", OwnerID: DefaultOwnerID}
	if err := ws.PutRepo(r); err != nil {
		t.Fatalf("PutRepo: %v", err)
	}
	got, ok := ws.Repo("marshal")
	if !ok || got.CredRef != "gh" {
		t.Fatalf("Repo(marshal) = (%+v, %v)", got, ok)
	}
}

func TestGateOverridePersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fleet.json")
	ws := NewWorkspace(path)
	a := Agent{ID: "a1", Project: "/p", OwnerID: DefaultOwnerID, Origin: OriginUI,
		GateOverride: &GateOverride{
			Reason: "known-flaky integration suite", At: time.Now().UTC(),
			By: DefaultOwnerID, FailedCommand: "go test ./...",
		}}
	if err := ws.PutAgent(a); err != nil {
		t.Fatal(err)
	}

	reloaded := NewWorkspace(path)
	if _, err := reloaded.Load(); err != nil {
		t.Fatal(err)
	}
	got, ok := reloaded.Agent("a1")
	if !ok || got.GateOverride == nil {
		t.Fatal("GateOverride did not survive a reload")
	}
	if got.GateOverride.Reason != "known-flaky integration suite" {
		t.Fatalf("Reason = %q", got.GateOverride.Reason)
	}
	if got.GateOverride.FailedCommand != "go test ./..." {
		t.Fatalf("FailedCommand = %q", got.GateOverride.FailedCommand)
	}
}

func TestV3MigratesToV4(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fleet.json")
	v3 := `{"version":3,"repos":[{"id":"r","url":"u","ownerId":"local"}],"agents":[{"id":"a1","project":"/p","ownerId":"local","origin":"ui"}]}`
	if err := os.WriteFile(path, []byte(v3), 0o600); err != nil {
		t.Fatal(err)
	}
	ws := NewWorkspace(path)
	backup, err := ws.Load()
	if err != nil {
		t.Fatal(err)
	}
	if backup != "" {
		t.Fatalf("v3 was quarantined to %q; it must migrate", backup)
	}
	if len(ws.Agents()) != 1 || len(ws.Repos()) != 1 {
		t.Fatal("v3 content did not survive migration")
	}
	// A v3 agent never had a gate decision; nil is correct and must not
	// be confused with "override granted".
	if ws.Agents()[0].GateOverride != nil {
		t.Fatal("migration invented a GateOverride")
	}
}

func TestRepoCarriesForgeIdentity(t *testing.T) {
	ws := NewWorkspace(t.TempDir() + "/fleet.json")
	r := Repo{ID: "r1", URL: "https://code.example.com/you/repo.git",
		Forge: "gitea", APIBase: "https://code.example.com/api/v1",
		OwnerID: DefaultOwnerID}
	if err := ws.PutRepo(r); err != nil {
		t.Fatal(err)
	}
	got, ok := ws.Repo("r1")
	if !ok || got.Forge != "gitea" || got.APIBase == "" {
		t.Fatalf("forge identity did not round-trip: %+v", got)
	}
}

func TestSubmittedIssuesPreventResubmission(t *testing.T) {
	ws := NewWorkspace(t.TempDir() + "/fleet.json")
	if err := ws.MarkIssueSubmitted("r1", 42); err != nil {
		t.Fatal(err)
	}
	got := ws.SubmittedIssues("r1")
	if len(got) != 1 || got[0] != 42 {
		t.Fatalf("got %v, want [42]", got)
	}
	// A different repo must not inherit it.
	if len(ws.SubmittedIssues("r2")) != 0 {
		t.Fatal("submitted issues leaked across repos")
	}
	// Marking twice must not duplicate.
	if err := ws.MarkIssueSubmitted("r1", 42); err != nil {
		t.Fatal(err)
	}
	if len(ws.SubmittedIssues("r1")) != 1 {
		t.Fatal("MarkIssueSubmitted duplicated an entry")
	}
}

func TestV5MigratesToV6(t *testing.T) {
	path := t.TempDir() + "/fleet.json"
	v5 := `{"version":5,"repos":[{"id":"r","url":"u","ownerId":"local"}],` +
		`"agents":[{"id":"a1","ownerId":"local","origin":"ui"}],"clients":[],"pending":[]}`
	if err := os.WriteFile(path, []byte(v5), 0o600); err != nil {
		t.Fatal(err)
	}
	ws := NewWorkspace(path)
	backup, err := ws.Load()
	if err != nil {
		t.Fatal(err)
	}
	if backup != "" {
		t.Fatalf("v5 was quarantined to %q; it must migrate", backup)
	}
	r, _ := ws.Repo("r")
	if r.Watch {
		t.Fatal("migration turned watching on; it must default off")
	}
	if ws.Agents()[0].IssueNumber != 0 {
		t.Fatal("migration invented an issue number")
	}
}
