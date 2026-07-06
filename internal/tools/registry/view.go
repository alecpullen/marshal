package registry

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

// TesterView returns a new Registry containing src's read-only and
// command-risk tools: the swarm tester needs to run tests and shell
// commands but must not modify source (docs/07 swarm safety). Write and
// network tools are filtered out so "does not modify source" is enforced
// structurally, not by prompt instruction.
func TesterView(src *Registry) *Registry {
	view := New()
	for _, tool := range src.List() {
		if tool.Risk == RiskReadOnly || tool.Risk == RiskCommand {
			_ = view.Register(tool)
		}
	}
	return view
}
