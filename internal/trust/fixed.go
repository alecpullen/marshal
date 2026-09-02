package trust

// FixedResolver implements Resolver with a constant decision. It exists
// for callers that already know the trust answer — most notably the layer
// reloader replaying a session-only trust decision, which never wrote a
// store record. Resolve returns the fixed decision whenever a project
// config exists (the loader only consults a resolver in that case) and
// never prompts; Record is a no-op.
type FixedResolver struct {
	Decision Decision
}

// Resolve returns the resolver's fixed decision without prompting.
func (r FixedResolver) Resolve(workingDir string, hasProjectConfig bool) (Decision, error) {
	if !hasProjectConfig {
		return DecisionDontTrust, nil
	}
	return r.Decision, nil
}

// Record is a no-op: a fixed decision has nothing to persist.
func (r FixedResolver) Record(workingDir string, decision Decision) error {
	return nil
}
