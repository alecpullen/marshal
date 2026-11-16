package bridge

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func TestNewClientTokenIsUnpredictableAndHashed(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		plain, hash, err := NewClientToken()
		if err != nil {
			t.Fatalf("NewClientToken: %v", err)
		}
		if len(plain) < 32 {
			t.Fatalf("token %q is too short to resist guessing", plain)
		}
		if seen[plain] {
			t.Fatal("NewClientToken repeated a token")
		}
		seen[plain] = true
		if hash == plain {
			t.Fatal("the stored hash equals the plaintext token")
		}
		if HashToken(plain) != hash {
			t.Fatal("HashToken does not reproduce the stored hash")
		}
	}
}

func TestClientSerialisationNeverCarriesAPlaintextToken(t *testing.T) {
	plain, hash, err := NewClientToken()
	if err != nil {
		t.Fatal(err)
	}
	c := MCPClient{ID: "c1", Name: "claude-code", TokenHash: hash, OwnerID: DefaultOwnerID}
	blob, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(blob), plain) {
		t.Fatalf("a plaintext token reached the serialised client: %s", blob)
	}
}

func TestSweepExpiredRemovesStalePending(t *testing.T) {
	ws := NewWorkspace(t.TempDir() + "/fleet.json")
	now := time.Now().UTC()
	if err := ws.PutPending(PendingSpawn{ID: "fresh", ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := ws.PutPending(PendingSpawn{ID: "stale", ExpiresAt: now.Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}

	if n := ws.SweepExpired(now); n != 1 {
		t.Fatalf("swept %d, want 1", n)
	}
	left := ws.Pending()
	if len(left) != 1 || left[0].ID != "fresh" {
		t.Fatalf("after sweep: %+v", left)
	}
}

func TestV4MigratesToV5(t *testing.T) {
	path := t.TempDir() + "/fleet.json"
	v4 := `{"version":4,"repos":[{"id":"r","url":"u","ownerId":"local"}],"agents":[{"id":"a1","ownerId":"local","origin":"ui"}]}`
	if err := os.WriteFile(path, []byte(v4), 0o600); err != nil {
		t.Fatal(err)
	}
	ws := NewWorkspace(path)
	backup, err := ws.Load()
	if err != nil {
		t.Fatal(err)
	}
	if backup != "" {
		t.Fatalf("v4 was quarantined to %q; it must migrate", backup)
	}
	if len(ws.Agents()) != 1 || len(ws.Repos()) != 1 {
		t.Fatal("v4 content did not survive")
	}
	if len(ws.Clients()) != 0 || len(ws.Pending()) != 0 {
		t.Fatal("migration invented clients or pending spawns")
	}
}

// TestMCPFactoryGrantLegacyDefaultsDeny pins that a client registered before
// factory grants existed holds no authority at all: absence is denial, not a
// default allow, and the surrounding record is untouched by the upgrade.
func TestMCPFactoryGrantLegacyDefaultsDeny(t *testing.T) {
	path := t.TempDir() + "/fleet.json"
	hash := strings.Repeat("ab", 32)
	legacy := `{
  "version": 11,
  "projects": ["/srv/code/app"],
  "agents": [],
  "clients": [
    {"id": "c1", "name": "claude-code", "tokenHash": "` + hash + `", "autonomous": true,
     "maxConcurrent": 2, "maxPerDay": 10, "allowedRepos": ["app"], "ownerId": "local",
     "createdAt": "2026-01-01T00:00:00Z"}
  ]
}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	ws := NewWorkspace(path)
	if _, err := ws.Load(); err != nil {
		t.Fatal(err)
	}

	client, ok := ws.Client("c1")
	if !ok {
		t.Fatal("the legacy client did not survive the upgrade")
	}
	if len(client.FactoryGrants) != 0 {
		t.Fatalf("an upgrade invented grants: %+v", client.FactoryGrants)
	}
	projectID, ok := ws.ProjectID("/srv/code/app")
	if !ok {
		t.Fatal("the registered project received no identity")
	}
	if _, stored := ws.FactoryGrant("c1", projectID); stored {
		t.Fatal("a client that was never granted anything reports a stored grant")
	}

	// The pair's zero state is what an operator's editor starts from, and it
	// authorizes nothing: no capability, and no approval authority at all.
	zero := factoryGrantState("c1", projectID, MCPFactoryProjectGrant{ProjectID: projectID})
	if zero.Grant.Read || zero.Grant.CreateDraft || zero.Grant.EditDraft ||
		zero.Grant.AnswerQuestion || zero.Grant.ApproveAndQueue {
		t.Fatalf("the zero grant authorizes something: %+v", zero.Grant)
	}
	if zero.Grant.ApprovalAuthority == "standing" || zero.Grant.ApprovalAuthority == "user_directed" {
		t.Fatalf("the zero grant names an approval authority: %q", zero.Grant.ApprovalAuthority)
	}
	if zero.Version == "" || zero.Version != factoryGrantVersion(MCPFactoryProjectGrant{ProjectID: projectID}) {
		t.Fatalf("the zero grant's version is not stable: %q", zero.Version)
	}

	// The optional field stays absent, so the record still reads as the file
	// an older bridge wrote.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "factoryGrants") {
		t.Fatalf("a client without grants gained the field: %s", raw)
	}
	if !strings.Contains(string(raw), hash) {
		t.Fatalf("the upgrade lost the client's token hash: %s", raw)
	}
}

// TestMCPFactoryGrantRoundTripPreservesClientTokenAndFleetPolicy checks that a
// grant is additive: the client's credential hash and fleet-spawn policy ride
// through unchanged, the grant survives a restart, and no read surface hands
// back storage the caller can edit.
func TestMCPFactoryGrantRoundTripPreservesClientTokenAndFleetPolicy(t *testing.T) {
	path := t.TempDir() + "/fleet.json"
	w := NewWorkspace(path)
	if _, err := w.Load(); err != nil {
		t.Fatal(err)
	}
	if err := w.AddProject("/srv/app"); err != nil {
		t.Fatal(err)
	}
	projectID, ok := w.ProjectID("/srv/app")
	if !ok {
		t.Fatal("AddProject minted no identity")
	}

	plain, hash, err := NewClientToken()
	if err != nil {
		t.Fatal(err)
	}
	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := w.PutClient(MCPClient{
		ID: "c1", Name: "claude-code", TokenHash: hash, Autonomous: true,
		MaxConcurrent: 3, MaxPerDay: 40, AllowedRepos: []string{"app"},
		OwnerID: DefaultOwnerID, CreatedAt: created,
	}); err != nil {
		t.Fatal(err)
	}
	grant := MCPFactoryProjectGrant{
		ProjectID: projectID, Read: true, CreateDraft: true, EditDraft: true,
		AnswerQuestion: true, ApproveAndQueue: true, ApprovalAuthority: "user_directed",
	}
	wrote, state, err := w.PutFactoryGrantIfMatch("c1", grant, factoryGrantVersion(MCPFactoryProjectGrant{ProjectID: projectID}))
	if err != nil {
		t.Fatalf("PutFactoryGrantIfMatch: %v", err)
	}
	if !wrote || state.Grant != grant {
		t.Fatalf("wrote %v, state %+v, want the grant as sent", wrote, state)
	}

	// Read surfaces hand back copies: editing them cannot reach the store.
	clients := w.Clients()
	clients[0].FactoryGrants[0].Read = false
	clients[0].AllowedRepos[0] = "elsewhere"
	if got, ok := w.FactoryGrant("c1", projectID); !ok || !got.Grant.Read {
		t.Fatal("a caller edited the stored grant through Clients()")
	}
	if got, ok := w.Client("c1"); !ok || len(got.AllowedRepos) != 1 || got.AllowedRepos[0] != "app" {
		t.Fatalf("a caller edited the stored allowlist through Clients(): %+v", got.AllowedRepos)
	}

	// The persisted file holds the hash, never the token.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), plain) {
		t.Fatalf("the plaintext token reached the workspace: %s", raw)
	}

	reloaded := NewWorkspace(path)
	if _, err := reloaded.Load(); err != nil {
		t.Fatal(err)
	}
	c, ok := reloaded.Client("c1")
	if !ok {
		t.Fatal("the client did not survive the restart")
	}
	if c.TokenHash != hash || !c.Autonomous || c.MaxConcurrent != 3 || c.MaxPerDay != 40 ||
		len(c.AllowedRepos) != 1 || c.AllowedRepos[0] != "app" ||
		c.OwnerID != DefaultOwnerID || !c.CreatedAt.Equal(created) {
		t.Fatalf("the grant disturbed the client record: %+v", c)
	}
	if len(c.FactoryGrants) != 1 {
		t.Fatalf("client grants = %+v, want exactly the one stored", c.FactoryGrants)
	}
	stored, ok := reloaded.FactoryGrant("c1", projectID)
	if !ok || stored.Grant != grant || stored.ClientID != "c1" || stored.ProjectID != projectID {
		t.Fatalf("grant round trip = %+v", stored)
	}

	// A grant state is a management response: it never carries the hash.
	blob, err := json.Marshal(stored)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(blob), hash) {
		t.Fatalf("a grant state exposed the token hash: %s", blob)
	}
	// Identity still resolves by token, so the grant changed nothing about
	// how the client is recognized.
	if matched, ok := MatchClient(reloaded.Clients(), plain); !ok || matched.ID != "c1" {
		t.Fatalf("MatchClient = (%+v, %v)", matched, ok)
	}
}
