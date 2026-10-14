package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"marshal/internal/agent"
	"marshal/internal/trust"
)

// Ceilings for the fixed prefix of a default native-tools request, set by
// the context-diet Tier 1 work (docs/context-diet/design.md). Lower them as
// later tiers cut more; never raise them without a design note saying why.
const (
	maxSystemPromptBytes = 11000
	maxToolSchemaBytes   = 31500
)

func TestDefaultPromptBudget(t *testing.T) {
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, ".marshal"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, ".marshal", "config.toml"), []byte(`[profile]
default = "p"
[providers.mock]
type = "openai_compatible"
base_url = "http://localhost:11434/v1"
api_key = "k"
[models.presets."mock/m"]
local_only = true
[agent_profiles.p]
implementer = "mock/m"
planner = "mock/m"
repo_scout = "mock/m"
tester = "mock/m"
reviewer = "mock/m"
`), 0644); err != nil {
		t.Fatal(err)
	}
	rt, err := StartRuntime(context.Background(), WithWorkingDir(tmp), WithHomeDir(t.TempDir()), WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent}))
	if err != nil {
		t.Fatalf("StartRuntime: %v", err)
	}
	defer rt.Close(context.Background())
	r := rt.Runner

	sys := agent.BuildSystemPromptWithSystem(agent.SystemPromptOptions{
		Role:         agent.RoleGeneral,
		Tools:        r.Registry.List(),
		Deferred:     r.Registry.ListDeferred(),
		SkillIndex:   r.SkillIndex,
		ActiveSkills: rt.State.ActiveSkills(),
		NativeTools:  true,
		Mode:         r.Policy.ApprovalMode(),
		Addendum:     r.SystemPromptAddendum,
		WorkingDir:   rt.State.WorkingDir,
		Roster:       agent.RenderAgentRoster(rt.State.Config),
		Narration:    r.NarrationPrompt,
	}).Content

	var schemaBytes int
	for _, tl := range r.Registry.List() {
		if tl.Deferred || tl.Internal {
			continue
		}
		b, err := json.Marshal(struct {
			Name        string
			Description string
			Parameters  json.RawMessage
		}{tl.Name, tl.Description, tl.Schema})
		if err != nil {
			t.Fatalf("marshal %s: %v", tl.Name, err)
		}
		schemaBytes += len(b)
		if tl.Name == "memory.write" || (strings.HasPrefix(tl.Name, "config.") && tl.Name != "config.read") {
			t.Errorf("removed tool %s is registered", tl.Name)
		}
	}
	t.Logf("system prompt %d bytes (ceiling %d), tool schemas %d bytes (ceiling %d)", len(sys), maxSystemPromptBytes, schemaBytes, maxToolSchemaBytes)

	if len(sys) > maxSystemPromptBytes {
		t.Errorf("system prompt is %d bytes, over the %d ceiling", len(sys), maxSystemPromptBytes)
	}
	if schemaBytes > maxToolSchemaBytes {
		t.Errorf("tool schemas are %d bytes, over the %d ceiling", schemaBytes, maxToolSchemaBytes)
	}
	for _, banned := range []string{".docs-archive", "dispatch a reviewer subagent", "config.agent.set", "Skill suggestions for this request"} {
		if strings.Contains(sys, banned) {
			t.Errorf("system prompt contains removed text %q", banned)
		}
	}
}
