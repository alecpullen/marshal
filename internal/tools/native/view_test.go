package native

import (
	"testing"

	"marshal/internal/app/config"
	"marshal/internal/tools/registry"
)

// TestPlanWriterViewOverRealNativeRegistry asserts the SDD plan-authoring
// view keeps the read-only repository tools the authoring child needs and
// never exposes a side-effectful one. The test builds a real native registry
// so a future addition to PlanWriterView that relies on an "exclude-by-Risk"
// shortcut fails here instead of in production.
func TestPlanWriterViewOverRealNativeRegistry(t *testing.T) {
	root := t.TempDir()
	src := registry.New()
	if err := RegisterAll(src, Options{
		WorkspaceRoot: root,
		CommandRunner: &fakeRunner{},
		Config:        config.Config{},
		Symbols:       &fakeQuerier{},
	}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	view := registry.PlanWriterView(src, "@plan", []string{"@plan/feature.md"})

	// The view must keep the safe repository-read tools the authoring child
	// actually needs to inspect the repo.
	for _, name := range []string{"file.read", "repo.search", "symbols"} {
		if _, ok := view.Lookup(name); !ok {
			t.Errorf("plan-author view must retain read-only tool %q", name)
		}
	}

	// No command, network, destructive, or non-candidate workspace-write
	// tool is allowed.
	for _, tool := range view.List() {
		switch tool.Risk {
		case registry.RiskCommand, registry.RiskNetwork, registry.RiskDestructive, registry.RiskEnvironment:
			t.Errorf("plan-author view exposes %q at risk %q", tool.Name, tool.Risk)
		case registry.RiskWorkspaceWrite:
			switch tool.Name {
			case "file.write_patch", "file.write", "file.edit":
				// The candidate plan is written with any of the path-scoped
				// write tools; each is narrowed to the allowed plan paths.
			default:
				t.Errorf("plan-author view exposes unexpected workspace-write tool %q", tool.Name)
			}
		}
	}
}
