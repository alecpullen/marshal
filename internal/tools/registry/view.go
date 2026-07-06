package registry

import (
	"context"
)

// ReadOnlyView returns a new Registry containing only src's read-only
// tools. Swarm roles that must not modify the workspace (planner, repo
// scout, reviewer) are given this view: write tools disappear from their
// system-prompt tool list and Lookup fails for them, so read-only access
// is enforced structurally rather than by prompt instructions.
func ReadOnlyView(src *Registry) *Registry {
	view := New()
	for _, tool := range src.List() {
		if tool.Risk == RiskReadOnly {
			// Tools were valid when registered with src; re-registering
			// the same Tool value cannot fail.
			_ = view.Register(tool)
		}
	}
	return view
}

// TesterView returns a new Registry containing src's read-only tools plus a
// constrained test.run. The swarm tester needs to inspect and run the
// configured test command, but must not get arbitrary shell execution or
// write/network tools. This keeps "does not modify source" structural rather
// than prompt-only.
func TesterView(src *Registry) *Registry {
	view := New()
	for _, tool := range src.List() {
		switch {
		case tool.Risk == RiskReadOnly:
			_ = view.Register(tool)
		case tool.Name == "test.run":
			_ = view.Register(testerTestRunTool(tool))
		}
	}
	return view
}

func testerTestRunTool(tool Tool) Tool {
	testerTool := tool
	testerTool.Description = "Run the configured test command in the workspace."
	testerTool.Schema = []byte(`{"type":"object","properties":{}}`)
	original := tool.Handler
	testerTool.Handler = func(ctx context.Context, call ToolCall) (ToolResult, error) {
		call.Args = []byte(`{}`)
		return original(ctx, call)
	}
	return testerTool
}
