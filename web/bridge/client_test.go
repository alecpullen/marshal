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
