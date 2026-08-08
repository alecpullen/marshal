package native

import (
	"testing"

	"marshal/internal/app/config"
	"marshal/internal/tools/registry"
)

// TestPlanWriterViewExcludesDiagnosticsAndRecallFromRealNativeRegistry
// asserts the SDD plan-authoring view filters the two side-effectful
// native tools that carry RiskReadOnly but mutate state or dereference a
// nil child database: diagnostics.check (runs configured per-language
// shell commands) and recall_history (queries the session DB and panics
// when nil). The test builds a real native registry so a future addition
// to PlanWriterView that relies on an "exclude-by-Risk" shortcut fails
// here instead of in production.
func TestPlanWriterViewExcludesDiagnosticsAndRecallFromRealNativeRegistry(t *testing.T) {
	root := t.TempDir()
	src := registry.New()
	// Force recall_history to be registered so the view's negative list
	// is exercised against the real tool.
	cfg := config.Config{}
	cfg.Session.Rollover.Enabled = true
	cfg.Session.Rollover.RecallToolEnabled = "always"
	if err := RegisterAll(src, Options{
		WorkspaceRoot: root,
		CommandRunner: &fakeRunner{},
		Config:        cfg,
	}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	// Both tools must be present in the source registry; otherwise the
	// assertion below cannot distinguish a real filter from a missing
	// registration.
	for _, name := range []string{"diagnostics.check", "recall_history"} {
		if _, ok := src.Lookup(name); !ok {
			t.Fatalf("precondition: source registry must register %q", name)
		}
	}

	view := registry.PlanWriterView(src, "@plan", []string{"@plan/feature.md"})

	if _, ok := view.Lookup("diagnostics.check"); ok {
		t.Error("diagnostics.check must be excluded from the plan-author view: it runs configured shell commands")
	}
	if _, ok := view.Lookup("recall_history"); ok {
		t.Error("recall_history must be excluded from the plan-author view: it dereferences the session DB")
	}

	// The view must also keep the safe repository-read tools the
	// authoring child actually needs to inspect the repo.
	for _, name := range []string{"file.read", "repo.search", "symbols.find"} {
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
			if tool.Name != "file.write_patch" {
				t.Errorf("plan-author view exposes unexpected workspace-write tool %q", tool.Name)
			}
		}
	}
}
