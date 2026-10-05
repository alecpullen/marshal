package bridge

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVaultCredentialResolvesIntoGitEnv(t *testing.T) {
	ctx := context.Background()
	p := NewEnvProvider()
	t.Setenv("MARSHAL_TEST_GH", "  ghp_vaulted \n")
	store := NewCredentialStore([]Credential{
		{ID: "v", Kind: "vault", Ref: "vault:env/MARSHAL_TEST_GH", OwnerID: "local", User: "me"},
		{ID: "bad", Kind: "vault", Ref: "env/NOPE", OwnerID: "local"},
		{ID: "missing", Kind: "vault", Ref: "vault:env/MARSHAL_TEST_UNSET", OwnerID: "local"},
	})
	if _, err := store.Resolve(ctx, "local", "v"); err == nil {
		t.Fatal("resolved without a provider")
	}
	store.SetProvider(p)
	cred, err := store.Resolve(ctx, "local", "v")
	if err != nil {
		t.Fatal(err)
	}
	env := gitEnv("/bin/askpass", cred)
	if !hasEnv(env, "MARSHAL_ASKPASS_SECRET=ghp_vaulted") || !hasEnv(env, "MARSHAL_ASKPASS_USER=me") || !hasEnv(env, "GIT_ASKPASS=/bin/askpass") {
		t.Fatalf("env = %v", env)
	}
	if b, _ := json.Marshal(cred); strings.Contains(string(b), "ghp_vaulted") {
		t.Fatalf("credential JSON leaks the value: %s", b)
	}
	if _, err := store.Resolve(ctx, "local", "bad"); err == nil {
		t.Error("bad ref resolved")
	}
	if _, err := store.Resolve(ctx, "local", "missing"); err == nil {
		t.Error("missing secret resolved")
	}
	if _, err := store.Resolve(ctx, "bob", "v"); err != ErrUnknownCredential {
		t.Errorf("other owner: %v", err)
	}
}

func testCredServer(t *testing.T) (*Server, *Fleet) {
	t.Helper()
	f := testFleetWithAudit(t)
	return NewServer(f, ""), f
}

func TestCredentialRoutes(t *testing.T) {
	s, f := testCredServer(t)
	t.Setenv("MARSHAL_TEST_PAT", "x")
	post := func(body map[string]string) int {
		return doReq(t, s, http.MethodPost, "/api/credentials", body, nil).Code
	}
	if c := post(map[string]string{"id": "pat1", "kind": "pat", "envVar": "MARSHAL_TEST_PAT"}); c != http.StatusOK {
		t.Fatalf("POST pat = %d", c)
	}
	if c := post(map[string]string{"id": "v1", "kind": "vault", "ref": "vault:env/MARSHAL_TEST_PAT", "user": "bot"}); c != http.StatusOK {
		t.Fatalf("POST vault = %d", c)
	}
	if c := post(map[string]string{"id": "v2", "kind": "vault", "ref": "vault:git/unset"}); c != http.StatusOK {
		t.Fatalf("POST vault2 = %d", c)
	}
	for name, body := range map[string]map[string]string{
		"bad id":     {"id": "a b", "kind": "none"},
		"bad kind":   {"id": "k", "kind": "oauth"},
		"pat no env": {"id": "k", "kind": "pat"},
		"bad ref":    {"id": "k", "kind": "vault", "ref": "vault:../x"},
		"ssh no key": {"id": "k", "kind": "ssh"},
	} {
		if c := post(body); c != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", name, c)
		}
	}
	rec := doReq(t, s, http.MethodGet, "/api/credentials", nil, nil)
	var list []credentialView
	decodeBody(t, rec, &list)
	if len(list) != 3 || list[0].ID != "pat1" || !list[0].Set || !list[1].Set || list[2].Set {
		t.Fatalf("list = %+v", list)
	}
	if strings.Contains(rec.Body.String(), `"x"`) {
		t.Fatalf("response carries a value: %s", rec.Body)
	}
	// Credentials are persisted.
	if got := f.ws.Credentials(); len(got) != 3 {
		t.Fatalf("persisted = %d", len(got))
	}
	// A repo referencing a credential blocks its deletion.
	rec = doReq(t, s, http.MethodPost, "/api/repos", map[string]any{"id": "r1", "url": "https://github.com/a/b.git", "forge": "github", "credRef": "pat1"}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST repo = %d %s", rec.Code, rec.Body)
	}
	if c := doReq(t, s, http.MethodDelete, "/api/credentials/pat1", nil, nil).Code; c != http.StatusConflict {
		t.Fatalf("DELETE in-use credential = %d", c)
	}
	if c := doReq(t, s, http.MethodDelete, "/api/credentials/v1", nil, nil).Code; c != http.StatusNoContent {
		t.Fatalf("DELETE = %d", c)
	}
	if c := doReq(t, s, http.MethodDelete, "/api/credentials/v1", nil, nil).Code; c != http.StatusNotFound {
		t.Fatalf("second DELETE = %d", c)
	}
	if _, ok := f.creds.Get("v1"); ok {
		t.Error("live store still holds the deleted credential")
	}
}

func TestRepoRoutes(t *testing.T) {
	s, f := testCredServer(t)
	f.creds.Put(Credential{ID: "pat1", Kind: "pat", EnvVar: "X", OwnerID: DefaultOwnerID})
	post := func(body map[string]any) int {
		return doReq(t, s, http.MethodPost, "/api/repos", body, nil).Code
	}
	if c := post(map[string]any{"id": "r1", "url": "https://github.com/a/b.git", "branch": "main", "forge": "github", "credRef": "pat1", "watch": true, "watchLabel": "marshal"}); c != http.StatusOK {
		t.Fatalf("POST = %d", c)
	}
	for name, body := range map[string]map[string]any{
		"no url":         {"id": "r2"},
		"bad id":         {"id": "r/2", "url": "u"},
		"bad forge":      {"id": "r2", "url": "u", "forge": "gitlab"},
		"unknown cred":   {"id": "r2", "url": "u", "credRef": "nope"},
		"watch no forge": {"id": "r2", "url": "u", "watch": true},
	} {
		if c := post(body); c != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", name, c)
		}
	}
	var repos []Repo
	decodeBody(t, doReq(t, s, http.MethodGet, "/api/repos", nil, nil), &repos)
	if len(repos) != 1 || repos[0].ID != "r1" || !repos[0].Watch || repos[0].OwnerID != DefaultOwnerID {
		t.Fatalf("repos = %+v", repos)
	}
	if findEvent(auditTail(t, f), AuditRepoRegistered) == nil {
		t.Fatal("no repo_registered audit entry")
	}
	// An agent using the repo blocks removal.
	f.ws.PutAgent(Agent{ID: "a1", Project: "/p", SourceKind: "git", SourceRef: "r1", OwnerID: DefaultOwnerID})
	if c := doReq(t, s, http.MethodDelete, "/api/repos/r1", nil, nil).Code; c != http.StatusConflict {
		t.Fatalf("DELETE in-use repo = %d", c)
	}
	f.ws.mu.Lock()
	delete(f.ws.agents, "a1")
	f.ws.mu.Unlock()
	if c := doReq(t, s, http.MethodDelete, "/api/repos/r1", nil, nil).Code; c != http.StatusNoContent {
		t.Fatalf("DELETE = %d", c)
	}
	if c := doReq(t, s, http.MethodDelete, "/api/repos/r1", nil, nil).Code; c != http.StatusNotFound {
		t.Fatalf("second DELETE = %d", c)
	}
	if findEvent(auditTail(t, f), AuditRepoRemoved) == nil {
		t.Fatal("no repo_removed audit entry")
	}
}

func TestCredentialsSurviveReloadAndMigrate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fleet.json")
	ws := NewWorkspace(path)
	if _, err := ws.Load(); err != nil {
		t.Fatal(err)
	}
	if err := ws.PutCredential(Credential{ID: "v", Kind: "vault", Ref: "vault:git/x", OwnerID: "local"}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), `"version": 11`) || !strings.Contains(string(raw), "vault:git/x") {
		t.Fatalf("file = %s", raw)
	}
	ws2 := NewWorkspace(path)
	if _, err := ws2.Load(); err != nil {
		t.Fatal(err)
	}
	if got := ws2.Credentials(); len(got) != 1 || got[0].Ref != "vault:git/x" {
		t.Fatalf("reloaded = %+v", got)
	}
	f := NewFleet(ws2, "unused", nil, t.TempDir(), Limits{}, "", nil, "")
	t.Cleanup(f.Close)
	if _, ok := f.creds.Get("v"); !ok {
		t.Fatal("NewFleet did not load persisted credentials")
	}
	// A v8 file loads, gets no credentials, and is rewritten at v10.
	old := filepath.Join(t.TempDir(), "fleet.json")
	os.WriteFile(old, []byte(`{"version":8,"projects":[],"agents":[]}`), 0o600)
	ws3 := NewWorkspace(old)
	if q, err := ws3.Load(); err != nil || q != "" {
		t.Fatalf("v8 load: %q %v", q, err)
	}
	if len(ws3.Credentials()) != 0 {
		t.Fatal("v8 file grew credentials")
	}
	ws3.PutCredential(Credential{ID: "n", Kind: "none", OwnerID: "local"})
	raw, _ = os.ReadFile(old)
	if !strings.Contains(string(raw), `"version": 11`) {
		t.Fatalf("v8 not rewritten: %s", raw)
	}
	// Removing an unknown credential is reported.
	if err := ws3.RemoveCredential("zzz"); err != ErrUnknownCredential {
		t.Fatalf("RemoveCredential = %v", err)
	}
}
