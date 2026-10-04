package bridge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func wsServer(t *testing.T) (*Server, *wsSpawnEnv) {
	t.Helper()
	e := newWSSpawnEnv(t)
	return NewServer(e.f, ""), e
}

func TestWorkspacesHTTPLifecycle(t *testing.T) {
	s, e := wsServer(t)

	rec := doReq(t, s, http.MethodPost, "/api/workspaces", map[string]string{"name": "svc", "from": "starter:go-service"}, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	if got, _ := e.f.templates.Read("svc", 0); !strings.Contains(string(got), `name = "svc"`) || strings.Contains(string(got), `"go-service"`) {
		t.Fatalf("starter was not renamed:\n%s", got)
	}
	if rec := doReq(t, s, http.MethodPost, "/api/workspaces", map[string]string{"name": "svc"}, nil); rec.Code != http.StatusConflict {
		t.Fatalf("duplicate create = %d", rec.Code)
	}
	if rec := doReq(t, s, http.MethodPost, "/api/workspaces", map[string]string{"name": "Bad Name"}, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad name = %d", rec.Code)
	}
	if rec := doReq(t, s, http.MethodPost, "/api/workspaces", map[string]string{"name": "x", "from": "starter:nope"}, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown starter = %d", rec.Code)
	}
	if rec := doReq(t, s, http.MethodPost, "/api/workspaces", map[string]string{"name": "x", "from": "wat"}, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown source = %d", rec.Code)
	}

	// Draft save, parse, patch.
	doc := sampleDoc("svc")
	rec = doReq(t, s, http.MethodPut, "/api/workspaces/svc/draft", map[string]string{"source": string(wsSrc(t, doc))}, nil)
	var view workspaceView
	decodeBody(t, rec, &view)
	if rec.Code != 200 || view.Doc.Workspace.Base != "debian:bookworm-slim" {
		t.Fatalf("put draft = %d %s", rec.Code, rec.Body)
	}
	rec = doReq(t, s, http.MethodPost, "/api/workspaces/svc/patch", map[string]any{"layer": 3, "value": WSPackages{Apt: []string{"jq"}}}, nil)
	decodeBody(t, rec, &view)
	if rec.Code != 200 || len(view.Doc.Packages.Apt) != 1 || view.Doc.Packages.Apt[0] != "jq" {
		t.Fatalf("patch = %d %s", rec.Code, rec.Body)
	}
	if got, _ := e.f.templates.Read("svc", 0); !strings.Contains(string(got), `"jq"`) {
		t.Fatalf("patch was not saved to the draft: %s", got)
	}

	// Publish v1, change, publish v2, diff.
	if rec := doReq(t, s, http.MethodPost, "/api/workspaces/svc/publish", nil, nil); rec.Code != http.StatusCreated {
		t.Fatalf("publish = %d %s", rec.Code, rec.Body)
	}
	doReq(t, s, http.MethodPost, "/api/workspaces/svc/patch", map[string]any{"layer": 3, "value": WSPackages{Apt: []string{"jq", "git"}}}, nil)
	var v TemplateVersion
	rec = doReq(t, s, http.MethodPost, "/api/workspaces/svc/publish", nil, nil)
	decodeBody(t, rec, &v)
	if v.N != 2 {
		t.Fatalf("second publish = %+v", v)
	}
	rec = doReq(t, s, http.MethodGet, "/api/workspaces/svc/diff?a=1&b=2", nil, nil)
	var diff struct{ Diff string }
	decodeBody(t, rec, &diff)
	if rec.Code != 200 || !strings.Contains(diff.Diff, "--- svc@1") || !strings.Contains(diff.Diff, "+++ svc@2") || !strings.Contains(diff.Diff, "git") {
		t.Fatalf("diff = %d %q", rec.Code, diff.Diff)
	}

	// Read: latest by default, pinned by version.
	rec = doReq(t, s, http.MethodGet, "/api/workspaces/svc?version=1", nil, nil)
	decodeBody(t, rec, &view)
	if rec.Code != 200 || view.Version != 1 || len(view.Doc.Packages.Apt) != 1 {
		t.Fatalf("get v1 = %d %s", rec.Code, rec.Body)
	}
	if rec := doReq(t, s, http.MethodGet, "/api/workspaces/svc?version=9", nil, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("get v9 = %d", rec.Code)
	}
	if rec := doReq(t, s, http.MethodGet, "/api/workspaces/nope", nil, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("get missing = %d", rec.Code)
	}

	// Pool.
	if rec := doReq(t, s, http.MethodPut, "/api/workspaces/svc/pool", map[string]int{"size": 2}, nil); rec.Code != 200 {
		t.Fatalf("pool = %d %s", rec.Code, rec.Body)
	}
	if rec := doReq(t, s, http.MethodPut, "/api/workspaces/svc/pool", map[string]int{"size": 9}, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("pool 9 = %d", rec.Code)
	}
	if m, _ := e.f.templates.Meta("svc"); m.Pool != 2 {
		t.Fatalf("pool = %d", m.Pool)
	}

	// Delete is refused while an agent uses it, allowed after.
	a := Agent{ID: "a1", Project: "/p", Workspace: &AgentWorkspace{Name: "svc", Version: 1, Source: "studio"}}
	e.f.ws.PutAgent(a)
	if rec := doReq(t, s, http.MethodDelete, "/api/workspaces/svc", nil, nil); rec.Code != http.StatusConflict {
		t.Fatalf("delete in use = %d", rec.Code)
	}
	e.f.ws.RemoveAgent("a1")
	if rec := doReq(t, s, http.MethodDelete, "/api/workspaces/svc", nil, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete = %d", rec.Code)
	}

	tail, _ := e.f.audit.Tail(20)
	seen := map[string]bool{}
	for _, ev := range tail {
		seen[ev.Event] = true
	}
	for _, want := range []string{AuditWorkspaceCreated, AuditWorkspacePublished, AuditWorkspaceDeleted} {
		if !seen[want] {
			t.Errorf("no %s audit entry", want)
		}
	}
}

func TestWorkspacesHTTPPublishRefusesErrorDiagnostics(t *testing.T) {
	s, e := wsServer(t)
	e.f.templates.Create("bad", []byte("BAD toml"), "t")
	if rec := doReq(t, s, http.MethodPost, "/api/workspaces/bad/publish", nil, nil); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("publish a broken draft = %d %s", rec.Code, rec.Body)
	}
	if m, _ := e.f.templates.Meta("bad"); m.Published != 0 {
		t.Fatal("a broken draft was published")
	}
}

func TestWorkspacesHTTPListIncludesTrustedRepoTemplates(t *testing.T) {
	s, e := wsServer(t)
	home := fakeHome(t)
	publishDoc(t, e.f, "svc", sampleDoc("svc"))
	trusted, untrusted := projectWithConfig(t), projectWithConfig(t)
	writeTrustStore(t, home, `{"`+trusted+`":{"trusted":true}}`)
	writeRepoTemplate(t, trusted, "mine", []byte("x"))
	writeRepoTemplate(t, untrusted, "theirs", []byte("x"))
	e.f.ws.AddProject(trusted)
	e.f.ws.AddProject(untrusted)
	e.f.ws.PutAgent(Agent{ID: "a1", Project: "/p", Workspace: &AgentWorkspace{Name: "svc", Version: 1, Source: "studio"}})
	e.f.ws.PutAgent(Agent{ID: "a2", Project: "/p", Workspace: &AgentWorkspace{Name: "svc", Version: 1, Source: "studio"}})

	var list []workspaceEntry
	decodeBody(t, doReq(t, s, http.MethodGet, "/api/workspaces", nil, nil), &list)
	byName := map[string]workspaceEntry{}
	for _, en := range list {
		byName[en.Source+":"+en.Name] = en
	}
	if byName["studio:svc"].Usage != 2 || byName["studio:svc"].Published != 1 {
		t.Fatalf("studio entry = %+v", byName["studio:svc"])
	}
	if en, ok := byName["repo:mine"]; !ok || en.Project != trusted {
		t.Fatalf("repo entry = %+v ok=%v", en, ok)
	}
	if _, ok := byName["repo:theirs"]; ok {
		t.Fatal("an untrusted project's template was listed")
	}
}

func TestWorkspacesHTTPDevcontainerImport(t *testing.T) {
	s, e := wsServer(t)
	root := t.TempDir()
	e.f.ws.AddProject(root)
	dc := filepath.Join(root, ".devcontainer")
	os.MkdirAll(dc, 0o700)

	os.WriteFile(filepath.Join(dc, "devcontainer.json"), []byte(`{"image":"node:20"}`), 0o600)
	if rec := doReq(t, s, http.MethodPost, "/api/workspaces", map[string]string{"name": "dc", "from": "devcontainer:" + root}, nil); rec.Code != http.StatusCreated {
		t.Fatalf("import = %d %s", rec.Code, rec.Body)
	}
	src, _ := e.f.templates.Read("dc", 0)
	var doc WSDoc
	json.Unmarshal(src, &doc)
	if doc.Workspace.Base != "node:20" || doc.Workspace.Name != "dc" {
		t.Fatalf("doc = %+v", doc)
	}

	os.WriteFile(filepath.Join(dc, "devcontainer.json"), []byte(`{"build":{"dockerfile":"Dockerfile"}}`), 0o600)
	rec := doReq(t, s, http.MethodPost, "/api/workspaces", map[string]string{"name": "dc2", "from": "devcontainer:" + root}, nil)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "builds from a Dockerfile") {
		t.Fatalf("build refusal = %d %s", rec.Code, rec.Body)
	}
	if rec := doReq(t, s, http.MethodPost, "/api/workspaces", map[string]string{"name": "dc3", "from": "devcontainer:/etc"}, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("unregistered root = %d", rec.Code)
	}
}

func TestWorkspacesHTTPSnapshot(t *testing.T) {
	s, e := wsServer(t)
	id, err := e.spawn(t, SpawnOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// A host-process agent cannot be snapshotted.
	if rec := doReq(t, s, http.MethodPost, "/api/workspaces", map[string]string{"name": "snap", "from": "snapshot:" + id}, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("non-container snapshot = %d %s", rec.Code, rec.Body)
	}
	rt, _ := e.f.runtimeForAgent(id)
	rt.containerized = true
	rec := doReq(t, s, http.MethodPost, "/api/workspaces", map[string]string{"name": "snap", "from": "snapshot:" + id}, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("snapshot = %d %s", rec.Code, rec.Body)
	}
	if e.imgs.count("commit", "marshal-agent-"+id) != 1 {
		t.Fatalf("commands = %v", e.imgs.cmds)
	}
	src, _ := e.f.templates.Read("snap", 0)
	var doc WSDoc
	json.Unmarshal(src, &doc)
	if !strings.HasPrefix(doc.Workspace.Base, "marshal-ws/snap-snap:") {
		t.Fatalf("base = %q", doc.Workspace.Base)
	}
	tail, _ := e.f.audit.Tail(10)
	found := false
	for _, ev := range tail {
		found = found || (ev.Event == AuditWorkspaceSnapshot && ev.AgentID == id)
	}
	if !found {
		t.Fatalf("no workspace_snapshot audit entry: %+v", tail)
	}
	if rec := doReq(t, s, http.MethodPost, "/api/workspaces", map[string]string{"name": "snap2", "from": "snapshot:nope"}, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown agent = %d", rec.Code)
	}
}

func TestWorkspacesHTTPBuildsAndEvents(t *testing.T) {
	s, e := wsServer(t)
	publishDoc(t, e.f, "svc", sampleDoc("svc"))

	rec := doReq(t, s, http.MethodPost, "/api/workspaces/svc/builds", nil, nil)
	var out struct{ Version int }
	decodeBody(t, rec, &out)
	if rec.Code != http.StatusAccepted || out.Version != 1 {
		t.Fatalf("start build = %d %s", rec.Code, rec.Body)
	}
	waitFor(t, 5*time.Second, "build to finish", func() bool {
		m, _ := e.f.templates.Meta("svc")
		return m.Versions[0].BuildStatus == "ok"
	})
	if rec := doReq(t, s, http.MethodPost, "/api/workspaces/svc/builds", map[string]int{"version": 7}, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("build of a missing version = %d", rec.Code)
	}
	if rec := doReq(t, s, http.MethodPost, "/api/workspaces/nope/builds", nil, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("build of a missing template = %d", rec.Code)
	}

	rec = doReq(t, s, http.MethodGet, "/api/workspaces/svc/builds", nil, nil)
	var list struct {
		Versions []TemplateVersion `json:"versions"`
	}
	decodeBody(t, rec, &list)
	if rec.Code != 200 || len(list.Versions) != 1 || list.Versions[0].BuildStatus != "ok" {
		t.Fatalf("list builds = %d %s", rec.Code, rec.Body)
	}

	// The log replays over SSE, ending with the done event. Cancel the
	// request once the replay is written: the stream stays open for live events.
	req := httptest.NewRequest(http.MethodGet, "/api/workspaces/svc/builds/1/events", nil)
	ctx, cancel := contextWithCancelAfter(req, 300*time.Millisecond)
	defer cancel()
	srec := httptest.NewRecorder()
	s.ServeHTTP(srec, req.WithContext(ctx))
	body := srec.Body.String()
	if !strings.Contains(body, "building l1") || !strings.Contains(body, `"done":true`) || !strings.Contains(body, `"status":"ok"`) {
		t.Fatalf("SSE body:\n%s", body)
	}
	if rec := doReq(t, s, http.MethodGet, "/api/workspaces/svc/builds/zero/events", nil, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad build number = %d", rec.Code)
	}
}

func TestWorkspacesHTTPBuildBusy(t *testing.T) {
	s, e := wsServer(t)
	e.imgs.buildHit, e.imgs.buildGo = make(chan struct{}, 8), make(chan struct{})
	publishDoc(t, e.f, "svc", sampleDoc("svc"))
	if rec := doReq(t, s, http.MethodPost, "/api/workspaces/svc/builds", nil, nil); rec.Code != http.StatusAccepted {
		t.Fatalf("first = %d", rec.Code)
	}
	<-e.imgs.buildHit
	if rec := doReq(t, s, http.MethodPost, "/api/workspaces/svc/builds", nil, nil); rec.Code != http.StatusConflict {
		t.Fatalf("second = %d %s", rec.Code, rec.Body)
	}
	close(e.imgs.buildGo)
	waitFor(t, 5*time.Second, "build to finish", func() bool {
		m, _ := e.f.templates.Meta("svc")
		return m.Versions[0].BuildStatus == "ok"
	})
}

func TestSpawnWorkspaceOverHTTPNotBuiltIs409(t *testing.T) {
	s, e := wsServer(t)
	publishDoc(t, e.f, "svc", sampleDoc("svc"))
	rec := doReq(t, s, http.MethodPost, "/api/agents", map[string]string{"project": t.TempDir(), "workspace": "svc"}, nil)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "Build it first") {
		t.Fatalf("spawn = %d %s", rec.Code, rec.Body)
	}
}

func TestUnifiedDiff(t *testing.T) {
	if got := unifiedDiff("a\nb\n", "a\nb\n", "x", "y"); got != "" {
		t.Fatalf("identical inputs diffed: %q", got)
	}
	got := unifiedDiff("a\nb\nc\n", "a\nB\nc\nd\n", "x", "y")
	want := "--- x\n+++ y\n@@ -1,3 +1,4 @@\n a\n-b\n+B\n c\n+d\n"
	if got != want {
		t.Fatalf("diff:\n%s\nwant:\n%s", got, want)
	}
	// Distant changes become separate hunks.
	var a, b []string
	for i := 0; i < 20; i++ {
		a = append(a, "line")
		b = append(b, "line")
	}
	b[1], b[18] = "first", "last"
	got = unifiedDiff(strings.Join(a, "\n")+"\n", strings.Join(b, "\n")+"\n", "x", "y")
	if n := strings.Count(got, "@@ -"); n != 2 {
		t.Fatalf("hunks = %d:\n%s", n, got)
	}
}

// contextWithCancelAfter returns a context cancelled after d, so a
// streaming handler returns once its replay has been written.
func contextWithCancelAfter(r *http.Request, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), d)
}
