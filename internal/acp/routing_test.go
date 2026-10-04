package acp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"marshal/internal/app"
	"marshal/internal/trust"
)

const routingTestConfig = `[providers.mock]
type = "openai_compatible"
base_url = "http://localhost:11434/v1"
api_key = "mock-key"

[models.presets."mock/small"]
provider = "mock"
model = "small"
local_only = true

[models.presets."mock/big"]
provider = "mock"
model = "big"
local_only = true

[profile]
default = "base"

[agent_profiles.base]
implementer = "mock/small"
planner = "mock/small"
repo_scout = "mock/small"
tester = "mock/small"
reviewer = "mock/small"

[agent_profiles.strong]
implementer = "mock/big"
planner = "mock/big"
repo_scout = "mock/big"
tester = "mock/big"
reviewer = "mock/big"
`

// newRoutingSessionManager starts real runtimes against a temp user config.
func newRoutingSessionManager(t *testing.T) (*SessionManager, string) {
	t.Helper()
	home := t.TempDir()
	cfgDir := filepath.Join(home, ".config", "marshal")
	t.Setenv("XDG_CONFIG_HOME", "")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(routingTestConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	m := NewSessionManager(SessionManagerConfig{
		StartRuntime: app.StartRuntime,
		Options: []app.Option{
			app.WithHomeDir(home),
			app.WithTrustResolver(trust.FixedResolver{Decision: trust.DecisionTrustPermanent}),
		},
		Notify: func(string, any) error { return nil },
	})
	return m, work
}

func rosterFor(t *testing.T, m *SessionManager, sessionID string) map[string]RosterRole {
	t.Helper()
	mem := NewMemoryManager(MemoryManagerConfig{
		Lookup: func(id string) (*MemoryRuntime, bool) {
			rt, ok := m.Get(id)
			if !ok {
				return nil, false
			}
			return &MemoryRuntime{State: rt.State}, true
		},
	})
	raw, _ := json.Marshal(map[string]any{"sessionId": sessionID})
	res, err := mem.AgentsRoster(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]RosterRole{}
	for _, r := range res.(AgentsRosterResult).Roles {
		out[r.Role] = r
	}
	return out
}

func newRoutedSession(t *testing.T, m *SessionManager, work, routing string) (string, error) {
	t.Helper()
	params := `{"cwd":` + jsonString(work) + `,"mcpServers":[]` + routing + `}`
	v, err := m.Create(context.Background(), json.RawMessage(params))
	if err != nil {
		return "", err
	}
	id := v.(SessionResponse).SessionID
	t.Cleanup(func() { _ = m.Close(context.Background(), id) })
	return id, nil
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestSessionNewRoutingOverrideShowsInRoster(t *testing.T) {
	m, work := newRoutingSessionManager(t)
	id, err := newRoutedSession(t, m, work, `,"routing":{"overrides":{"implementer":"mock/big"}}`)
	if err != nil {
		t.Fatal(err)
	}
	roster := rosterFor(t, m, id)
	if got := roster["implementer"].PresetName; got != "mock/big" {
		t.Fatalf("implementer preset = %q, want mock/big", got)
	}
	if got := roster["reviewer"].PresetName; got != "mock/small" {
		t.Fatalf("reviewer preset = %q, want mock/small (unchanged)", got)
	}
}

func TestSessionNewRoutingProfile(t *testing.T) {
	m, work := newRoutingSessionManager(t)
	id, err := newRoutedSession(t, m, work, `,"routing":{"profile":"strong"}`)
	if err != nil {
		t.Fatal(err)
	}
	roster := rosterFor(t, m, id)
	if got := roster["planner"].PresetName; got != "mock/big" {
		t.Fatalf("planner preset = %q, want mock/big", got)
	}
	if roster["planner"].Profile != "strong" {
		t.Fatalf("profile = %q", roster["planner"].Profile)
	}
}

func TestSessionNewRoutingRejectsUnknownNames(t *testing.T) {
	m, work := newRoutingSessionManager(t)
	for _, routing := range []string{
		`,"routing":{"overrides":{"implementer":"mock/nope"}}`,
		`,"routing":{"overrides":{"not_a_role":"mock/big"}}`,
		`,"routing":{"profile":"missing"}`,
	} {
		_, err := newRoutedSession(t, m, work, routing)
		rpc, ok := err.(*jsonRPCError)
		if !ok || rpc.Code != invalidParams {
			t.Fatalf("routing %s: err = %v, want invalid params", routing, err)
		}
	}
}

func TestSessionNewWithoutRoutingIsUnchanged(t *testing.T) {
	m, work := newRoutingSessionManager(t)
	id, err := newRoutedSession(t, m, work, "")
	if err != nil {
		t.Fatal(err)
	}
	roster := rosterFor(t, m, id)
	if got := roster["implementer"].PresetName; got != "mock/small" || roster["implementer"].Profile != "base" {
		t.Fatalf("implementer = %+v", roster["implementer"])
	}
}
