package bridge

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"marshal/web/bridge/contract"
)

// These tests are the bridge half of task 4: a real daemon-shaped Server, a
// real workspace and the real coordinator routes, driven over /api exactly as
// the Studio drives them. Nothing here reaches a model provider, a container
// runtime, a forge or a real keychain.
//
// The cases are the ones the plan names: two tasks with different histories,
// host-local scope, a revoked read grant and mixed freshness — plus the two
// claims that make the read safe to expose at all: an attention-only turn
// starts zero workers, and the coordinator conversation is offered no mutation
// tool.

// coordinatorFixture is one daemon with two registered projects and the
// coordinator routes served the way the Studio reaches them.
type coordinatorFixture struct {
	t   *testing.T
	f   *Fleet
	ws  *Workspace
	srv *Server

	rootA, rootB       string
	projectA, projectB string
	now                time.Time
}

// newCoordinatorFixture builds a daemon holding two projects, with the clock
// pinned so evidence age is exact rather than timing-dependent.
func newCoordinatorFixture(t *testing.T) *coordinatorFixture {
	t.Helper()
	fx := &coordinatorFixture{t: t, now: time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)}
	fx.ws = NewWorkspace(filepath.Join(t.TempDir(), "fleet.json"))
	if _, err := fx.ws.Load(); err != nil {
		t.Fatalf("load workspace: %v", err)
	}
	fx.rootA = t.TempDir()
	fx.rootB = t.TempDir()
	for _, root := range []string{fx.rootA, fx.rootB} {
		if err := fx.ws.AddProject(root); err != nil {
			t.Fatalf("register project %s: %v", root, err)
		}
	}
	var ok bool
	if fx.projectA, ok = fx.ws.ProjectID(fx.rootA); !ok {
		t.Fatal("project A has no id")
	}
	if fx.projectB, ok = fx.ws.ProjectID(fx.rootB); !ok {
		t.Fatal("project B has no id")
	}

	fx.f = NewFleet(fx.ws, "unused", nil, t.TempDir(), Limits{}, "")
	fx.f.clock = func() time.Time { return fx.now }
	t.Cleanup(fx.f.Close)

	fx.srv = NewServer(fx.f)
	// A daemon identity is what makes a task host-local and what the access
	// policy is resolved against. It is the only thing this fixture asserts
	// about the host; no other route depends on it here.
	fx.srv.SetDaemonInfo(testMarshalInstance(testMarshalPID, testMarshalStarted))
	return fx
}

// attention reads one task's summary over the API the Studio uses.
func (fx *coordinatorFixture) attention(taskID string) contract.CoordinatorAttention {
	fx.t.Helper()
	rec := doReq(fx.t, fx.srv, http.MethodGet, "/api/coordinator/tasks/"+taskID+"/attention", nil, nil)
	if rec.Code != http.StatusOK {
		fx.t.Fatalf("GET attention for %s: %d %s", taskID, rec.Code, rec.Body)
	}
	var out contract.CoordinatorAttention
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		fx.t.Fatalf("decode attention: %v", err)
	}
	return out
}

// create opens one coordinator task and returns it.
func (fx *coordinatorFixture) create(body contract.CoordinatorCreateRequest) contract.CoordinatorTask {
	fx.t.Helper()
	rec := doReq(fx.t, fx.srv, http.MethodPost, "/api/coordinator/tasks", body, nil)
	if rec.Code != http.StatusCreated {
		fx.t.Fatalf("POST coordinator task: %d %s", rec.Code, rec.Body)
	}
	var out contract.CoordinatorCreateResult
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		fx.t.Fatalf("decode create: %v", err)
	}
	if !out.Created {
		fx.t.Fatal("a fresh request id was reported as a replay")
	}
	return out.Task
}

// sourcesOf indexes the per-source states by project id.
func sourcesOf(a contract.CoordinatorAttention) map[string]contract.CoordinatorSource {
	out := make(map[string]contract.CoordinatorSource, len(a.Sources))
	for _, src := range a.Sources {
		out[src.ProjectID] = src
	}
	return out
}

// TestCoordinatorTwoTasksKeepSeparateHistories pins the case the plan names
// first: two tasks on one host each read only their own projects, so reopening
// one destination never shows another's asks.
func TestCoordinatorTwoTasksKeepSeparateHistories(t *testing.T) {
	fx := newCoordinatorFixture(t)

	// Project A has an unconfirmed submission; project B has a ready review.
	if err := fx.ws.PutProjectSettings(fx.rootA, ProjectSettings{Intake: ProjectIntake{RepoID: "repo-a"}}); err != nil {
		t.Fatalf("project A settings: %v", err)
	}
	if err := fx.ws.PutPending(PendingSpawn{
		ID: "sub-a", Origin: OriginUI, RepoID: "repo-a", Title: "Ship the parser fix",
		CreatedAt: fx.now.Add(-2 * time.Minute), ExpiresAt: fx.now.Add(time.Hour),
	}); err != nil {
		t.Fatalf("put pending: %v", err)
	}
	fx.recordReadyReview("repo-b", fx.rootB, 7, "Harden the CLI", fx.now.Add(-time.Minute))

	taskA := fx.create(contract.CoordinatorCreateRequest{
		RequestID: "req-a", Title: "Parser work", Projects: []string{fx.projectA},
	})
	taskB := fx.create(contract.CoordinatorCreateRequest{
		RequestID: "req-b", Title: "CLI work", Projects: []string{fx.projectB},
	})
	if taskA.ID == taskB.ID || taskA.SessionID == taskB.SessionID {
		t.Fatal("two creates minted one identity")
	}

	attentionA := fx.attention(taskA.ID)
	if len(attentionA.Entries) != 1 {
		t.Fatalf("task A saw %d entries, want its own one: %+v", len(attentionA.Entries), attentionA.Entries)
	}
	if got := attentionA.Entries[0]; got.Source.ProjectID != fx.projectA || got.Source.Kind != "intake" {
		t.Fatalf("task A's entry names %+v, want the intake of project A", got.Source)
	}
	if sources := sourcesOf(attentionA); len(sources) != 1 || sources[fx.projectA].State != contract.CoordinatorSourceFresh {
		t.Fatalf("task A's sources = %+v, want only its own fresh one", attentionA.Sources)
	}

	attentionB := fx.attention(taskB.ID)
	if len(attentionB.Entries) != 1 {
		t.Fatalf("task B saw %d entries, want its own one: %+v", len(attentionB.Entries), attentionB.Entries)
	}
	if got := attentionB.Entries[0]; got.Source.ProjectID != fx.projectB || got.Kind != contract.CoordinatorAttentionReview {
		t.Fatalf("task B's entry = %+v, want the ready review of project B", got)
	}
	if sources := sourcesOf(attentionB); len(sources) != 1 || sources[fx.projectB].State != contract.CoordinatorSourceFresh {
		t.Fatalf("task B's sources = %+v, want only its own fresh one", attentionB.Sources)
	}

	// Reopening the destination reads the existing tasks, newest first.
	rec := doReq(t, fx.srv, http.MethodGet, "/api/coordinator/tasks", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list tasks: %d %s", rec.Code, rec.Body)
	}
	var list contract.CoordinatorTaskList
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list.Tasks) != 2 {
		t.Fatalf("list holds %d tasks, want 2: %+v", len(list.Tasks), list.Tasks)
	}
	if list.Host != testMarshalSocket {
		t.Fatalf("list host = %q, want the daemon's own identity", list.Host)
	}
	for _, task := range list.Tasks {
		if task.Host != testMarshalSocket {
			t.Fatalf("task %s host = %q, want the serving daemon's identity", task.ID, task.Host)
		}
	}
}

// TestCoordinatorCreateIsIdempotentOnTheRequestID pins the first-send contract:
// a retried request id answers the original task, and the same id with
// different content is refused rather than silently rewriting history.
func TestCoordinatorCreateIsIdempotentOnTheRequestID(t *testing.T) {
	fx := newCoordinatorFixture(t)
	body := contract.CoordinatorCreateRequest{RequestID: "req-1", Title: "First", Projects: []string{fx.projectA}}
	first := fx.create(body)

	rec := doReq(t, fx.srv, http.MethodPost, "/api/coordinator/tasks", body, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("replayed create = %d, want 200: %s", rec.Code, rec.Body)
	}
	var replay contract.CoordinatorCreateResult
	if err := json.Unmarshal(rec.Body.Bytes(), &replay); err != nil {
		t.Fatalf("decode replay: %v", err)
	}
	if replay.Created || replay.Task.ID != first.ID || replay.Task.SessionID != first.SessionID {
		t.Fatalf("a retry minted %+v, want the original %s", replay.Task, first.ID)
	}

	different := body
	different.Title = "Second"
	rec = doReq(t, fx.srv, http.MethodPost, "/api/coordinator/tasks", different, nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("reused request id = %d, want 409: %s", rec.Code, rec.Body)
	}
	if rec := doReq(t, fx.srv, http.MethodGet, "/api/coordinator/tasks", nil, nil); rec.Code != http.StatusOK {
		t.Fatalf("list after conflict: %d", rec.Code)
	}
}

// TestCoordinatorScopeIsHostLocal pins that a task's scope is what this host
// registered: an unnamed scope covers every project here, and a project id this
// daemon never minted names no scope at all.
func TestCoordinatorScopeIsHostLocal(t *testing.T) {
	fx := newCoordinatorFixture(t)
	if err := fx.ws.PutProjectSettings(fx.rootA, ProjectSettings{Intake: ProjectIntake{RepoID: "repo-a"}}); err != nil {
		t.Fatalf("project A settings: %v", err)
	}
	if err := fx.ws.PutPending(PendingSpawn{
		ID: "sub-a", Origin: OriginUI, RepoID: "repo-a", Title: "Ship the parser fix",
		CreatedAt: fx.now.Add(-time.Minute), ExpiresAt: fx.now.Add(time.Hour),
	}); err != nil {
		t.Fatalf("put pending: %v", err)
	}

	// No projects named: the task covers every project registered on this host.
	task := fx.create(contract.CoordinatorCreateRequest{RequestID: "req-all", Title: "Everything"})
	if len(task.Projects) != 0 {
		t.Fatalf("an unnamed scope stored %v, want none", task.Projects)
	}
	attention := fx.attention(task.ID)
	if sources := sourcesOf(attention); len(sources) != 2 || sources[fx.projectA].State == "" || sources[fx.projectB].State == "" {
		t.Fatalf("host-local scope read %+v, want both of this host's projects", attention.Sources)
	}

	// A project id this daemon never minted is refused at create: a task may
	// only be opened over scope the host can resolve.
	rec := doReq(t, fx.srv, http.MethodPost, "/api/coordinator/tasks", contract.CoordinatorCreateRequest{
		RequestID: "req-foreign", Projects: []string{"0123456789abcdef0123456789abcdef"},
	}, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("a foreign project id = %d, want 400: %s", rec.Code, rec.Body)
	}

	// An unregistered host (no daemon identity) has no task to name.
	other := newCoordinatorFixture(t)
	other.srv.SetDaemonInfo(DaemonInfo{})
	rec = doReq(t, other.srv, http.MethodGet, "/api/coordinator/tasks/"+task.ID, nil, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("another host resolved a task id = %d, want 404: %s", rec.Code, rec.Body)
	}
}

// TestCoordinatorRevokedReadNeverReportsAllClear pins the review focus: a
// revoked grant arrives as an unauthorized source, its content and its links
// are omitted, and the answer is never an all-clear.
func TestCoordinatorRevokedReadNeverReportsAllClear(t *testing.T) {
	fx := newCoordinatorFixture(t)
	task := fx.create(contract.CoordinatorCreateRequest{
		RequestID: "req-a", Title: "Both projects", Projects: []string{fx.projectA, fx.projectB},
	})
	if err := fx.ws.PutProjectSettings(fx.rootA, ProjectSettings{Intake: ProjectIntake{RepoID: "repo-a"}}); err != nil {
		t.Fatalf("project A settings: %v", err)
	}
	if err := fx.ws.PutPending(PendingSpawn{
		ID: "sub-a", Origin: OriginUI, RepoID: "repo-a", Title: "Ship the parser fix",
		CreatedAt: fx.now.Add(-time.Minute), ExpiresAt: fx.now.Add(time.Hour),
	}); err != nil {
		t.Fatalf("put pending: %v", err)
	}

	// Revoke project A's read capability for everyone. The default is
	// read-only, so the write is a deliberate denial against the version the
	// store currently holds (or the version a never-written default carries).
	expected := marshalAccessState(fx.projectA, "", "", MarshalFactoryAccess{}).Version
	if current, ok := fx.ws.marshalAccessDefault(fx.projectA); ok {
		expected = current.Version
	}
	if _, _, err := fx.srv.factoryAuth.putMarshalAccessDefault(fx.projectA, MarshalFactoryAccess{Read: boolPtr(false)}, expected); err != nil {
		t.Fatalf("revoke read: %v", err)
	}

	attention := fx.attention(task.ID)
	sources := sourcesOf(attention)
	if sources[fx.projectA].State != contract.CoordinatorSourceUnauthorized {
		t.Fatalf("project A source = %+v, want unauthorized", sources[fx.projectA])
	}
	if sources[fx.projectB].State != contract.CoordinatorSourceFresh {
		t.Fatalf("project B source = %+v, want fresh", sources[fx.projectB])
	}
	for _, entry := range attention.Entries {
		if entry.Source.ProjectID == fx.projectA {
			t.Fatalf("a revoked source contributed %+v", entry)
		}
		if entry.Destination == "" {
			t.Fatalf("entry %+v carries no destination", entry)
		}
	}
	if attention.AllClear {
		t.Fatal("a revoked grant produced an all-clear")
	}
	if !attention.Partial {
		t.Fatal("a revoked grant was not reported as partial coverage")
	}
}

// TestCoordinatorAttentionReportsMixedFreshness pins that freshness is per
// source: a quiet project whose evidence is old is stale, a project with recent
// evidence is fresh, and the answer names both rather than summarising them
// into one verdict.
func TestCoordinatorAttentionReportsMixedFreshness(t *testing.T) {
	fx := newCoordinatorFixture(t)

	// Project A's ask arrived a minute ago; project B's was observed long ago.
	if err := fx.ws.PutProjectSettings(fx.rootA, ProjectSettings{Intake: ProjectIntake{RepoID: "repo-a"}}); err != nil {
		t.Fatalf("project A settings: %v", err)
	}
	if err := fx.ws.PutPending(PendingSpawn{
		ID: "sub-a", Origin: OriginUI, RepoID: "repo-a", Title: "Ship the parser fix",
		CreatedAt: fx.now.Add(-time.Minute), ExpiresAt: fx.now.Add(time.Hour),
	}); err != nil {
		t.Fatalf("put pending: %v", err)
	}
	fx.recordReadyReview("repo-b", fx.rootB, 9, "Harden the CLI", fx.now.Add(-time.Hour))

	task := fx.create(contract.CoordinatorCreateRequest{
		RequestID: "req", Title: "Both", Projects: []string{fx.projectA, fx.projectB},
	})
	attention := fx.attention(task.ID)
	sources := sourcesOf(attention)
	if sources[fx.projectA].State != contract.CoordinatorSourceFresh {
		t.Fatalf("project A = %+v, want fresh", sources[fx.projectA])
	}
	if sources[fx.projectB].State != contract.CoordinatorSourceStale {
		t.Fatalf("project B = %+v, want stale (its evidence is an hour old)", sources[fx.projectB])
	}
	if sources[fx.projectB].Reason == "" {
		t.Fatal("a stale source carried no reason")
	}
	if !attention.Partial || attention.AllClear {
		t.Fatalf("mixed freshness reported partial=%v allClear=%v, want partial and no all-clear", attention.Partial, attention.AllClear)
	}
	// Both sources still contribute their entries: a stale ask is still an ask.
	kinds := map[contract.CoordinatorAttentionKind]bool{}
	for _, entry := range attention.Entries {
		kinds[entry.Kind] = true
	}
	if !kinds[contract.CoordinatorAttentionAsk] || !kinds[contract.CoordinatorAttentionReview] {
		t.Fatalf("entries = %+v, want both an ask and a review", attention.Entries)
	}
}

// TestCoordinatorAttentionStartsNoWorker pins the read's purity: an
// attention-only turn starts zero workers and creates no pending submission.
func TestCoordinatorAttentionStartsNoWorker(t *testing.T) {
	fx := newCoordinatorFixture(t)
	if err := fx.ws.PutProjectSettings(fx.rootA, ProjectSettings{Intake: ProjectIntake{RepoID: "repo-a"}}); err != nil {
		t.Fatalf("project A settings: %v", err)
	}
	if err := fx.ws.PutPending(PendingSpawn{
		ID: "sub-a", Origin: OriginUI, RepoID: "repo-a", Title: "Ship the parser fix",
		CreatedAt: fx.now.Add(-time.Minute), ExpiresAt: fx.now.Add(time.Hour),
	}); err != nil {
		t.Fatalf("put pending: %v", err)
	}
	task := fx.create(contract.CoordinatorCreateRequest{RequestID: "req", Title: "Read only", Projects: []string{fx.projectA}})

	beforeAgents, beforePending := len(fx.ws.Agents()), len(fx.ws.Pending())
	attention := fx.attention(task.ID)
	if len(attention.Entries) == 0 {
		t.Fatal("the fixture's ask was not reported")
	}
	if got := len(fx.ws.Agents()); got != beforeAgents {
		t.Fatalf("an attention read started %d workers", got-beforeAgents)
	}
	if got := len(fx.ws.Pending()); got != beforePending {
		t.Fatalf("an attention read created %d pending submissions", got-beforePending)
	}
	if running, waiting := fx.f.slots.stats(); running != 0 || waiting != 0 {
		t.Fatalf("an attention read took a worker slot (running=%d waiting=%d)", running, waiting)
	}
	if _, err := fx.f.runtimeForAgent("any"); err == nil {
		t.Fatal("an attention read started a runtime")
	}
}

// TestCoordinatorStoreLoadsOldDocumentsWithNoTasks pins the upgrade path: a
// store written before coordinated tasks existed loads as an empty one, and a
// document this build cannot read is unavailable rather than empty.
func TestCoordinatorStoreLoadsOldDocumentsWithNoTasks(t *testing.T) {
	dir := filepath.Join(t.TempDir(), coordinatorStateSubpath)
	if err := os.MkdirAll(dir, coordinatorDirMode); err != nil {
		t.Fatal(err)
	}
	// A document from a build with no coordinated tasks: no version, no tasks.
	if err := os.WriteFile(filepath.Join(dir, coordinatorTasksName), []byte(`{}`), coordinatorFileMode); err != nil {
		t.Fatal(err)
	}
	store, err := openCoordinatorStore(dir, nil)
	if err != nil {
		t.Fatalf("an old document must load: %v", err)
	}
	tasks, err := store.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(tasks) != 0 {
		t.Fatalf("an old document held %d tasks, want none", len(tasks))
	}
	// A task created now is written with this build's version and reads back.
	if _, _, err := store.Create(contract.CoordinatorCreateRequest{RequestID: "r1", Title: "t"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	reopened, err := openCoordinatorStore(dir, nil)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	again, err := reopened.List()
	if err != nil || len(again) != 1 {
		t.Fatalf("reopened store holds %d tasks (%v), want 1", len(again), err)
	}

	// A newer document is refused: guessing at a schema this build does not
	// know would be inventing tasks or dropping them.
	if err := os.WriteFile(filepath.Join(dir, coordinatorTasksName), []byte(`{"version":99}`), coordinatorFileMode); err != nil {
		t.Fatal(err)
	}
	if _, err := openCoordinatorStore(dir, nil); err == nil {
		t.Fatal("a newer document loaded instead of being refused")
	} else if !errors.Is(err, ErrCoordinatorUnavailable) {
		t.Fatalf("a newer document failed as %v, want unavailable", err)
	}
}

// recordReadyReview records one pull request whose checks have settled, so the
// attention read sees it as a review that is ready.
func (fx *coordinatorFixture) recordReadyReview(repoID, root string, number int, title string, observed time.Time) {
	fx.t.Helper()
	rec := contract.PullRecord{
		RepoID:      repoID,
		ProjectRoot: root,
		Number:      number,
		Title:       title,
		HeadRef:     "feature",
		BaseRef:     "main",
		Checks:      []contract.PullCheck{{Name: "build", State: contract.CheckPassed}},
		Origin:      contract.PullOriginAgent,
		State:       contract.PullReady,
		UpdatedAt:   observed.UnixMilli(),
		ObservedAt:  observed.UnixMilli(),
	}
	rec.ID = pullID(repoID, number)
	if err := fx.ws.PutPull(rec); err != nil {
		fx.t.Fatalf("put pull: %v", err)
	}
}
