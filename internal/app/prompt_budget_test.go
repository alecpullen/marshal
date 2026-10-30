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
	maxSystemPromptBytes = 9000
	maxToolSchemaBytes   = 15000
)

// budgetFixtureWorkingDir is the fixed working-directory input used when
// measuring the system prompt budget. The measurement is about prompt
// content, not about how long an incidental temporary path happens to be
// (t.TempDir() varies per run and per machine), so the directory is pinned
// to a stable absolute string instead of the runtime's real one.
const budgetFixtureWorkingDir = "/marshal-budget-fixture"

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
[lsp]
enabled = false
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
		Access:       rt.State.InteractionState(),
		Addendum:     r.SystemPromptAddendum,
		// The fixture measures the prompt's fixed prefix, so the
		// working-directory input is pinned rather than taken from the
		// runtime: the budget excludes incidental temporary-path length,
		// not the working-directory content. The real runtime above still
		// supplies every other input and all existing checks.
		WorkingDir: budgetFixtureWorkingDir,
		Roster:     agent.RenderAgentRoster(rt.State.Config),
		Narration:  r.NarrationPrompt,
	}).Content

	var schemaBytes, modelTools int
	for _, tl := range r.Registry.List() {
		if tl.Deferred || tl.Internal {
			continue
		}
		modelTools++
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
	t.Logf("system prompt %d bytes (ceiling %d), tool schemas %d bytes (ceiling %d) across %d model-facing tools", len(sys), maxSystemPromptBytes, schemaBytes, maxToolSchemaBytes, modelTools)

	if len(sys) > maxSystemPromptBytes {
		t.Errorf("system prompt is %d bytes, over the %d ceiling", len(sys), maxSystemPromptBytes)
	}
	if schemaBytes > maxToolSchemaBytes {
		t.Errorf("tool schemas are %d bytes, over the %d ceiling", schemaBytes, maxToolSchemaBytes)
	}
	for _, banned := range []string{".docs-archive", "dispatch a reviewer subagent", "config.agent.set", "Skill suggestions for this request", "file.write_patch format", "scratchpad.write", "recall_history", "diagnostics.check", "symbols.find"} {
		if strings.Contains(sys, banned) {
			t.Errorf("system prompt contains removed text %q", banned)
		}
	}
}

// TestBudgetFixtureWorkingDirIsStable pins the budget fixture's
// working-directory input: the prompt must contain that exact string, and
// pointing the builder at a different directory must change the result.
// Together these show the fixture still measures a real path rather than
// stripping or normalizing the working-directory content out of the prompt.
func TestBudgetFixtureWorkingDirIsStable(t *testing.T) {
	sys := agent.BuildSystemPromptWithSystem(agent.SystemPromptOptions{
		Role:       agent.RoleGeneral,
		WorkingDir: budgetFixtureWorkingDir,
	}).Content

	if !strings.Contains(sys, budgetFixtureWorkingDir) {
		t.Fatalf("system prompt does not contain the fixture working directory %q", budgetFixtureWorkingDir)
	}

	other := t.TempDir()
	otherSys := agent.BuildSystemPromptWithSystem(agent.SystemPromptOptions{
		Role:       agent.RoleGeneral,
		WorkingDir: other,
	}).Content

	if otherSys == sys {
		t.Fatalf("system prompt unchanged when the working directory changed from %q to %q", budgetFixtureWorkingDir, other)
	}
	if strings.Contains(otherSys, budgetFixtureWorkingDir) {
		t.Fatalf("prompt built with %q still contains the fixture working directory %q", other, budgetFixtureWorkingDir)
	}
	if !strings.Contains(otherSys, other) {
		t.Fatalf("prompt built with %q does not contain it", other)
	}
}
