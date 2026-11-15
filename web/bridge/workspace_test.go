package bridge

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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
	a := Agent{ID: "s1", Project: "/home/u/repo", Name: "fix bug", Mode: "act", CreatedAt: time.Now()}
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
	if got.Name != "fix bug" || got.Mode != "act" {
		t.Fatalf("agent round-trip lost fields: %+v", got)
	}
}

// TestStoredInteractionDefaultsMigrateWithoutGrantRestore checks that every
// persisted starting default is translated to ask/act in memory, that Auto
// presence survives, and that loading restores no live grant: no System value
// and no revision appear, and the file on disk is not rewritten.
func TestStoredInteractionDefaultsMigrateWithoutGrantRestore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fleet.json")
	// The project identity is already present, so Load has nothing to mint:
	// this fixture pins the interaction migration alone, and minting an
	// identity is the one change Load is allowed to write.
	raw := `{
  "version": 11,
  "projects": ["/p"],
  "agents": [
    {"id": "a1", "project": "/p", "mode": "edit", "ownerId": "local", "origin": "ui", "profile": {"image": "x"}},
    {"id": "a2", "project": "/p", "mode": "auto", "ownerId": "local", "origin": "ui", "profile": {"image": "x"}},
    {"id": "a3", "project": "/p", "mode": "copilot", "ownerId": "local", "origin": "ui", "profile": {"image": "x"}}
  ],
  "pending": [
    {"id": "p1", "origin": "ui", "title": "t", "repoId": "r1", "mode": "plan",
     "createdAt": "2026-01-01T00:00:00Z", "expiresAt": "2026-01-02T00:00:00Z"}
  ],
  "projectSettings": {"/p": {"mode": "copilot"}},
  "projectIdentities": [{"id": "0123456789abcdef0123456789abcdef", "root": "/p"}]
}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}

	w := NewWorkspace(path)
	if _, err := w.Load(); err != nil {
		t.Fatal(err)
	}

	want := map[string]struct {
		mode string
		auto *bool
	}{
		"a1": {"ask", nil},           // edit retired to Ask
		"a2": {"act", boolPtr(true)}, // auto is Act + Auto
		"a3": {"act", nil},           // copilot retired to Act without Auto
	}
	for id, exp := range want {
		a, ok := w.Agent(id)
		if !ok {
			t.Fatalf("agent %s missing", id)
		}
		if a.Mode != exp.mode {
			t.Errorf("%s mode = %q, want %q", id, a.Mode, exp.mode)
		}
		switch {
		case exp.auto == nil && a.Auto != nil:
			t.Errorf("%s auto = %v, want absent", id, *a.Auto)
		case exp.auto != nil && a.Auto == nil:
			t.Errorf("%s auto absent, want %v", id, *exp.auto)
		case exp.auto != nil && *a.Auto != *exp.auto:
			t.Errorf("%s auto = %v, want %v", id, *a.Auto, *exp.auto)
		}
	}
	if p, ok := w.PendingByID("p1"); !ok || p.Mode != "ask" || p.Auto != nil {
		t.Errorf("pending = %+v, want ask with no auto", p)
	}
	if ps := w.ProjectSettingsFor("/p"); ps.Mode != "act" || ps.Auto != nil {
		t.Errorf("project settings = %+v, want act with no auto", ps)
	}

	// Load is memory-only: the stored file keeps its original spellings.
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(onDisk) != raw {
		t.Fatal("Load rewrote the workspace file instead of migrating in memory")
	}

	// An explicit save emits the canonical pair and still stores no grant.
	if err := w.save(); err != nil {
		t.Fatal(err)
	}
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(saved), "system_access") || strings.Contains(string(saved), "access_revision") {
		t.Fatalf("workspace persisted a live grant: %s", saved)
	}
	if !strings.Contains(string(saved), `"mode": "ask"`) || !strings.Contains(string(saved), `"mode": "act"`) {
		t.Fatalf("save did not write canonical modes: %s", saved)
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

func TestWorkspaceV8LoadsAndSavesAtCurrentVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fleet.json")
	v8 := `{"version":8,"projects":["/p"],"agents":[{"id":"a1","project":"/p","ownerId":"local","origin":"ui"}]}`
	if err := os.WriteFile(path, []byte(v8), 0o600); err != nil {
		t.Fatal(err)
	}
	w := NewWorkspace(path)
	if quarantined, err := w.Load(); err != nil || quarantined != "" {
		t.Fatalf("Load = %q, %v", quarantined, err)
	}
	a, ok := w.Agent("a1")
	if !ok || a.Workspace != nil || a.ContainerName != "" {
		t.Fatalf("agent = %+v", a)
	}
	if err := w.PutProjectSettings("/p", ProjectSettings{Workspace: "svc"}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), `"version": 11`) {
		t.Fatalf("saved file is not v11:\n%s", data)
	}
}

func TestProjectSettingsRoundTripAndRemoveWithProject(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fleet.json")
	w := NewWorkspace(path)
	if err := w.AddProject("/p"); err != nil {
		t.Fatal(err)
	}
	yes := true
	want := ProjectSettings{
		Workspace: "svc@2", Mode: "plan", Isolated: &yes, ShipTarget: "push",
		Routing: json.RawMessage(`{"profile":"fast"}`),
		Intake:  ProjectIntake{RepoID: "r", Labels: []string{"marshal"}, Clients: []string{"c1"}},
	}
	if err := w.PutProjectSettings("/p", want); err != nil {
		t.Fatal(err)
	}
	w2 := NewWorkspace(path)
	if _, err := w2.Load(); err != nil {
		t.Fatal(err)
	}
	got := w2.ProjectSettingsFor("/p")
	if got.Workspace != "svc@2" || got.Mode != "ask" || got.Isolated == nil || !*got.Isolated || got.ShipTarget != "push" ||
		compactJSON(t, got.Routing) != `{"profile":"fast"}` || got.Intake.RepoID != "r" || len(got.Intake.Labels) != 1 {
		t.Fatalf("settings = %+v", got)
	}
	if err := w2.RemoveProject("/p"); err != nil {
		t.Fatal(err)
	}
	if got := w2.ProjectSettingsFor("/p"); got.Workspace != "" {
		t.Fatalf("settings survived their project: %+v", got)
	}
}

func TestPinsSurviveASaveAndALoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fleet.json")
	w := NewWorkspace(path)
	if _, err := w.Load(); err != nil {
		t.Fatal(err)
	}
	if err := w.SetPinned("/home/u/zeta", true); err != nil {
		t.Fatalf("SetPinned: %v", err)
	}
	if err := w.SetPinned("/home/u/alpha", true); err != nil {
		t.Fatalf("SetPinned: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Index(string(data), "/home/u/alpha") > strings.Index(string(data), "/home/u/zeta") {
		t.Fatalf("saved pins are not sorted:\n%s", data)
	}
	reloaded := NewWorkspace(path)
	if _, err := reloaded.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := reloaded.Pinned()
	want := []string{"/home/u/alpha", "/home/u/zeta"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("Pinned() = %v, want %v", got, want)
	}
}

func TestUnpinningRemovesOnlyThatRoot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fleet.json")
	w := NewWorkspace(path)
	if _, err := w.Load(); err != nil {
		t.Fatal(err)
	}
	if err := w.SetPinned("/home/u/a", true); err != nil {
		t.Fatal(err)
	}
	if err := w.SetPinned("/home/u/b", true); err != nil {
		t.Fatal(err)
	}
	if err := w.SetPinned("/home/u/a", false); err != nil {
		t.Fatalf("SetPinned(false): %v", err)
	}
	if w.IsPinned("/home/u/a") {
		t.Error("/home/u/a must be unpinned")
	}
	if !w.IsPinned("/home/u/b") {
		t.Error("/home/u/b must stay pinned")
	}
	reloaded := NewWorkspace(path)
	if _, err := reloaded.Load(); err != nil {
		t.Fatal(err)
	}
	if got := reloaded.Pinned(); len(got) != 1 || got[0] != "/home/u/b" {
		t.Fatalf("Pinned() after reload = %v", got)
	}
}

func TestAPinNeedsNoRegisteredProject(t *testing.T) {
	w := NewWorkspace(filepath.Join(t.TempDir(), "fleet.json"))
	if _, err := w.Load(); err != nil {
		t.Fatal(err)
	}
	if err := w.SetPinned("/home/u/unregistered", true); err != nil {
		t.Fatalf("SetPinned: %v", err)
	}
	if got := w.Projects(); len(got) != 0 {
		t.Fatalf("pinning registered a project: %v", got)
	}
	if !w.IsPinned("/home/u/unregistered") {
		t.Error("pin was not kept")
	}
}

func TestRemovingAProjectKeepsItsPin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fleet.json")
	w := NewWorkspace(path)
	if _, err := w.Load(); err != nil {
		t.Fatal(err)
	}
	if err := w.AddProject("/home/u/repo"); err != nil {
		t.Fatal(err)
	}
	if err := w.SetPinned("/home/u/repo", true); err != nil {
		t.Fatal(err)
	}
	if err := w.RemoveProject("/home/u/repo"); err != nil {
		t.Fatalf("RemoveProject: %v", err)
	}
	if got := w.Projects(); len(got) != 0 {
		t.Fatalf("project survived removal: %v", got)
	}
	if !w.IsPinned("/home/u/repo") {
		t.Error("removing a project must keep its pin")
	}
	reloaded := NewWorkspace(path)
	if _, err := reloaded.Load(); err != nil {
		t.Fatal(err)
	}
	if !reloaded.IsPinned("/home/u/repo") {
		t.Error("pin did not survive the reload")
	}
}

func TestARelativePinIsRefused(t *testing.T) {
	w := NewWorkspace(filepath.Join(t.TempDir(), "fleet.json"))
	if _, err := w.Load(); err != nil {
		t.Fatal(err)
	}
	if err := w.SetPinned("relative/path", true); err == nil {
		t.Fatal("expected error for relative pin root")
	}
	if got := w.Pinned(); len(got) != 0 {
		t.Fatalf("refused pin was stored: %v", got)
	}
}

func TestAgentOverridesReachTheContainerConfig(t *testing.T) {
	f := testFleet(t)
	plain := Agent{ID: "a1", Project: "/p", SourceKind: "git", Profile: DefaultRuntimeProfile()}
	cfg, err := f.containerConfigFor(plain, "/usr/bin/docker", "docker")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Name != "marshal-agent-a1" || cfg.WorkSubpath != "work/a1" || cfg.SocketSubpath != "sockets/a1" {
		t.Fatalf("derived names = %q %q %q", cfg.Name, cfg.WorkSubpath, cfg.SocketSubpath)
	}
	pooled := plain
	pooled.ContainerName, pooled.WorkSubpath, pooled.SocketSubpath = "marshal-pool-svc-v1-0", "pool/svc-0/work", "pool/svc-0/sock"
	cfg, err = f.containerConfigFor(pooled, "/usr/bin/docker", "docker")
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Join(newContainerTransport(cfg).buildRunArgs(), " ")
	for _, want := range []string{"--name marshal-pool-svc-v1-0", "volume-subpath=pool/svc-0/work", "volume-subpath=pool/svc-0/sock"} {
		if !strings.Contains(args, want) {
			t.Errorf("args lack %q:\n%s", want, args)
		}
	}
	if cfg.SocketDir != filepath.Join(f.stateDir, "pool/svc-0/sock") {
		t.Fatalf("socket dir = %q", cfg.SocketDir)
	}
}

func compactJSON(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var b bytes.Buffer
	if err := json.Compact(&b, raw); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// TestMCPProjectIdentityStableAcrossRestart checks that a project's ID is
// minted for a registration, written before it is observed, and reused by a
// restart and by a repeated registration of the same root. An older file
// gains identities on its first start, and keeps them afterwards.
func TestMCPProjectIdentityStableAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fleet.json")
	w := NewWorkspace(path)
	if _, err := w.Load(); err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{"/srv/code/app", "/srv/code/lib"} {
		if err := w.AddProject(root); err != nil {
			t.Fatalf("AddProject(%q): %v", root, err)
		}
	}
	appID, ok := w.ProjectID("/srv/code/app")
	if !ok {
		t.Fatal("a registered project has no identity")
	}
	if len(appID) != 2*projectIDBytes {
		t.Fatalf("project id %q is not 128 bits of hex", appID)
	}
	libID, ok := w.ProjectID("/srv/code/lib")
	if !ok || libID == appID {
		t.Fatalf("project ids = %q and %q, want two distinct ids", appID, libID)
	}
	if root, ok := w.ProjectRootByID(appID); !ok || root != "/srv/code/app" {
		t.Fatalf("ProjectRootByID(%q) = (%q, %v)", appID, root, ok)
	}
	// A path is never an ID: only what the bridge minted resolves.
	if _, ok := w.ProjectRootByID("/srv/code/app"); ok {
		t.Fatal("a host path resolved as a project id")
	}

	// Registering the same project again keeps the ID it already has.
	if err := w.AddProject("/srv/code/app"); err != nil {
		t.Fatal(err)
	}
	if again, _ := w.ProjectID("/srv/code/app"); again != appID {
		t.Fatalf("re-registering minted %q, want the original %q", again, appID)
	}

	// The ID reached the file, so the next start finds it instead of minting.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), appID) {
		t.Fatalf("the project identity never reached the file: %s", raw)
	}
	reloaded := NewWorkspace(path)
	if _, err := reloaded.Load(); err != nil {
		t.Fatal(err)
	}
	if got, ok := reloaded.ProjectID("/srv/code/app"); !ok || got != appID {
		t.Fatalf("after restart ProjectID = (%q, %v), want %q", got, ok, appID)
	}
	if root, ok := reloaded.ProjectRootByID(appID); !ok || root != "/srv/code/app" {
		t.Fatalf("after restart ProjectRootByID = (%q, %v)", root, ok)
	}

	// An older file holds no identities at all. The first start mints them
	// and saves before Load returns, so the ID it reports is durable.
	old := filepath.Join(t.TempDir(), "fleet.json")
	if err := os.WriteFile(old, []byte(`{"version":11,"projects":["/srv/code/app"],"agents":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	upgraded := NewWorkspace(old)
	if _, err := upgraded.Load(); err != nil {
		t.Fatal(err)
	}
	minted, ok := upgraded.ProjectID("/srv/code/app")
	if !ok || len(minted) != 2*projectIDBytes {
		t.Fatalf("the upgrade minted no identity: (%q, %v)", minted, ok)
	}
	upgradedRaw, err := os.ReadFile(old)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(upgradedRaw), minted) {
		t.Fatalf("the minted identity was not persisted: %s", upgradedRaw)
	}
	restarted := NewWorkspace(old)
	if _, err := restarted.Load(); err != nil {
		t.Fatal(err)
	}
	if again, _ := restarted.ProjectID("/srv/code/app"); again != minted {
		t.Fatalf("the second start minted %q, want the saved %q", again, minted)
	}
}

// TestMCPProjectIdentityRemintedAfterAFailedSave checks that a registration
// whose identity could not be persisted is given one on the next add, rather
// than being left without an ID until the process restarts.
func TestMCPProjectIdentityRemintedAfterAFailedSave(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fleet.json")
	w := NewWorkspace(path)
	if _, err := w.Load(); err != nil {
		t.Fatal(err)
	}
	// Make every save fail: the path's directory is replaced by a file, as it
	// is for a bridge whose data directory went away.
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := w.AddProject("/srv/code/app"); err == nil {
		t.Fatal("the add succeeded while the workspace could not be saved")
	}
	// The registration stays, and no identity is exposed for it.
	if _, ok := w.ProjectID("/srv/code/app"); ok {
		t.Fatal("an identity that never reached the disk was exposed")
	}
	// The data directory comes back, and registering the project again — what
	// an operator's retry does — mints the identity now.
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := w.AddProject("/srv/code/app"); err != nil {
		t.Fatalf("re-adding the registered project: %v", err)
	}
	id, ok := w.ProjectID("/srv/code/app")
	if !ok || len(id) != 2*projectIDBytes {
		t.Fatalf("the retry minted no identity: (%q, %v)", id, ok)
	}
	if root, ok := w.ProjectRootByID(id); !ok || root != "/srv/code/app" {
		t.Fatalf("ProjectRootByID = (%q, %v)", root, ok)
	}
	reloaded := NewWorkspace(path)
	if _, err := reloaded.Load(); err != nil {
		t.Fatal(err)
	}
	if got, ok := reloaded.ProjectID("/srv/code/app"); !ok || got != id {
		t.Fatalf("after restart ProjectID = (%q, %v), want %q", got, ok, id)
	}
}

// TestMCPProjectRemoveRevokesAndReregisterGetsNewID checks that removing a
// registration drops its identity and every grant naming it in the same save,
// that the other projects' grants survive, and that registering the project
// again is a new registration with a fresh ID and no inherited authority.
func TestMCPProjectRemoveRevokesAndReregisterGetsNewID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fleet.json")
	w := NewWorkspace(path)
	if _, err := w.Load(); err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{"/srv/app", "/srv/lib"} {
		if err := w.AddProject(root); err != nil {
			t.Fatal(err)
		}
	}
	appID, ok := w.ProjectID("/srv/app")
	if !ok {
		t.Fatal("no identity for the first project")
	}
	libID, ok := w.ProjectID("/srv/lib")
	if !ok {
		t.Fatal("no identity for the second project")
	}
	if err := w.PutClient(MCPClient{
		ID: "c1", Name: "claude-code", TokenHash: "hash", OwnerID: DefaultOwnerID,
		FactoryGrants: []MCPFactoryProjectGrant{
			{ProjectID: appID, Read: true, CreateDraft: true},
			{ProjectID: libID, Read: true, AnswerQuestion: true, ApprovalAuthority: "standing"},
		},
	}); err != nil {
		t.Fatal(err)
	}

	if err := w.RemoveProject("/srv/app"); err != nil {
		t.Fatalf("RemoveProject: %v", err)
	}
	if _, ok := w.ProjectID("/srv/app"); ok {
		t.Fatal("a removed project kept its identity")
	}
	if _, ok := w.ProjectRootByID(appID); ok {
		t.Fatal("a removed project's id still resolves")
	}
	if _, ok := w.FactoryGrant("c1", appID); ok {
		t.Fatal("removal left a grant authorizing the removed project")
	}
	if st, ok := w.FactoryGrant("c1", libID); !ok || !st.Grant.Read || !st.Grant.AnswerQuestion || st.Grant.ApprovalAuthority != "standing" {
		t.Fatalf("the surviving project's grant changed: %+v", st)
	}

	// The revocation is part of the removal's own save, not a later one.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), appID) {
		t.Fatalf("the workspace still references the removed project's id: %s", raw)
	}
	reloaded := NewWorkspace(path)
	if _, err := reloaded.Load(); err != nil {
		t.Fatal(err)
	}
	if _, ok := reloaded.FactoryGrant("c1", appID); ok {
		t.Fatal("the grant survived a restart")
	}

	// Registering the project again mints a new ID, and no stale authority
	// attaches to it.
	if err := reloaded.AddProject("/srv/app"); err != nil {
		t.Fatal(err)
	}
	fresh, ok := reloaded.ProjectID("/srv/app")
	if !ok {
		t.Fatal("re-registration minted no identity")
	}
	if fresh == appID {
		t.Fatalf("re-registration reused the removed id %q", fresh)
	}
	if _, ok := reloaded.FactoryGrant("c1", fresh); ok {
		t.Fatal("a stale grant attached to the new identity")
	}
	if st, ok := reloaded.FactoryGrant("c1", libID); !ok || st.Grant.ApprovalAuthority != "standing" {
		t.Fatalf("re-registration disturbed another project's grant: %+v", st)
	}

	// Deleting the client revokes everything it held.
	if err := reloaded.DeleteClient("c1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := reloaded.FactoryGrant("c1", libID); ok {
		t.Fatal("a deleted client still reports a grant")
	}
}

// TestMCPFactoryGrantConditionalConflict checks that one grant is written
// against one version: a stale version is refused with the current state, a
// missing version and an unknown client are errors, and a refused write
// changes nothing on disk.
func TestMCPFactoryGrantConditionalConflict(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fleet.json")
	w := NewWorkspace(path)
	if _, err := w.Load(); err != nil {
		t.Fatal(err)
	}
	if err := w.AddProject("/srv/app"); err != nil {
		t.Fatal(err)
	}
	projectID, ok := w.ProjectID("/srv/app")
	if !ok {
		t.Fatal("no identity for the project")
	}
	if err := w.PutClient(MCPClient{ID: "c1", Name: "claude-code", TokenHash: "hash", OwnerID: DefaultOwnerID}); err != nil {
		t.Fatal(err)
	}
	if _, stored := w.FactoryGrant("c1", projectID); stored {
		t.Fatal("a fresh pair already has a stored grant")
	}
	// With nothing stored, the pair still has a stable version: the zero
	// grant's. An editor that read it can write against it.
	zeroVersion := factoryGrantVersion(MCPFactoryProjectGrant{ProjectID: projectID})
	if zeroVersion != factoryGrantVersion(MCPFactoryProjectGrant{ProjectID: projectID}) {
		t.Fatal("the zero grant's version is not stable")
	}

	first := MCPFactoryProjectGrant{ProjectID: projectID, Read: true}
	wrote, state, err := w.PutFactoryGrantIfMatch("c1", first, zeroVersion)
	if err != nil {
		t.Fatalf("PutFactoryGrantIfMatch: %v", err)
	}
	if !wrote || state.Grant != first {
		t.Fatalf("wrote %v, state %+v, want the first grant", wrote, state)
	}
	if state.Version == zeroVersion {
		t.Fatal("writing the grant did not move its version")
	}

	// A second editor holding that version wins, and the version moves again.
	second := MCPFactoryProjectGrant{ProjectID: projectID, Read: true, CreateDraft: true, ApprovalAuthority: "user_directed"}
	wrote, state2, err := w.PutFactoryGrantIfMatch("c1", second, state.Version)
	if err != nil || !wrote {
		t.Fatalf("the second write = %v, %v", wrote, err)
	}
	if state2.Grant != second || state2.Version == state.Version {
		t.Fatalf("second state = %+v, want the second grant at a new version", state2)
	}

	// The first editor's retry now holds a stale version: refused, told what
	// it lost to, and nothing written.
	wrote, current, err := w.PutFactoryGrantIfMatch("c1", first, state.Version)
	if err != nil {
		t.Fatalf("a conflict must not be an error: %v", err)
	}
	if wrote {
		t.Fatal("a stale version overwrote a newer grant")
	}
	if current.Grant != second || current.Version != state2.Version {
		t.Fatalf("conflict reported %+v, want the current state %+v", current, state2)
	}
	if stored, ok := w.FactoryGrant("c1", projectID); !ok || stored.Grant != second {
		t.Fatalf("the refused write changed the store: %+v", stored)
	}
	reloaded := NewWorkspace(path)
	if _, err := reloaded.Load(); err != nil {
		t.Fatal(err)
	}
	if stored, ok := reloaded.FactoryGrant("c1", projectID); !ok || stored.Grant != second || stored.Version != state2.Version {
		t.Fatalf("after restart the grant = %+v, want %+v", stored, state2)
	}

	// A write naming no version is not a conditional write.
	if _, _, err := w.PutFactoryGrantIfMatch("c1", first, ""); err == nil {
		t.Fatal("an empty version must be refused")
	}
	// A grant that names no project is refused, and an unknown client is
	// reported rather than silently dropped.
	if _, _, err := w.PutFactoryGrantIfMatch("c1", MCPFactoryProjectGrant{Read: true}, state2.Version); err == nil {
		t.Fatal("a grant with no project id must be refused")
	}
	if _, _, err := w.PutFactoryGrantIfMatch("ghost", first, state2.Version); err == nil {
		t.Fatal("an unknown client must be reported")
	}
}

// TestMCPFactoryDelegationDefaultsOff checks that planner delegation is off
// for a project that predates the fields, that a settings record without them
// still marshals as it did, and that turning delegation on persists through
// the existing conditional settings write.
func TestMCPFactoryDelegationDefaultsOff(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fleet.json")
	old := `{
  "version": 11,
  "projects": ["/srv/app"],
  "agents": [],
  "projectSettings": {"/srv/app": {"workspace": "svc", "mode": "act"}}
}`
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	w := NewWorkspace(path)
	if _, err := w.Load(); err != nil {
		t.Fatal(err)
	}
	if err := w.AddProject("/srv/other"); err != nil {
		t.Fatal(err)
	}

	// An unset project starts with no delegation and no designated client.
	ps := w.ProjectSettingsFor("/srv/other")
	if ps.PlannerDelegationEnabled || ps.DesignatedMCPClientID != "" {
		t.Fatalf("delegation defaults on: %+v", ps)
	}
	// The old record defaults the same way and, being unset, keeps its
	// original encoding: neither field appears.
	ps = w.ProjectSettingsFor("/srv/app")
	if ps.PlannerDelegationEnabled || ps.DesignatedMCPClientID != "" || ps.Workspace != "svc" {
		t.Fatalf("settings = %+v, want the stored value with delegation off", ps)
	}
	blob, err := json.Marshal(ps)
	if err != nil {
		t.Fatal(err)
	}
	if string(blob) != `{"workspace":"svc","mode":"act","intake":{}}` {
		t.Fatalf("an unset delegation field changed the encoding: %s", blob)
	}

	// The operator turns delegation on and designates one client, through
	// the conditional settings write the settings surface already uses.
	etag, err := w.ProjectSettingsETag("/srv/app")
	if err != nil {
		t.Fatal(err)
	}
	ps.PlannerDelegationEnabled = true
	ps.DesignatedMCPClientID = "c1"
	wrote, _, err := w.PutProjectSettingsIfMatch("/srv/app", ps, etag)
	if err != nil || !wrote {
		t.Fatalf("PutProjectSettingsIfMatch = %v, %v", wrote, err)
	}
	// A writer holding the pre-change tag loses, so two settings surfaces
	// cannot disagree about delegation.
	stale := ProjectSettings{Workspace: "svc", Mode: "act"}
	if wrote, _, err := w.PutProjectSettingsIfMatch("/srv/app", stale, etag); err != nil || wrote {
		t.Fatalf("a stale settings write = %v, %v", wrote, err)
	}

	reloaded := NewWorkspace(path)
	if _, err := reloaded.Load(); err != nil {
		t.Fatal(err)
	}
	got := reloaded.ProjectSettingsFor("/srv/app")
	if !got.PlannerDelegationEnabled || got.DesignatedMCPClientID != "c1" {
		t.Fatalf("delegation did not persist: %+v", got)
	}
	if got.Workspace != "svc" || got.Mode != "act" {
		t.Fatalf("the write dropped unrelated settings: %+v", got)
	}
	if ps := reloaded.ProjectSettingsFor("/srv/other"); ps.PlannerDelegationEnabled || ps.DesignatedMCPClientID != "" {
		t.Fatalf("delegation leaked to another project: %+v", ps)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"plannerDelegationEnabled": true`) ||
		!strings.Contains(string(raw), `"designatedMCPClientId": "c1"`) {
		t.Fatalf("the delegation fields are not on disk: %s", raw)
	}
}
