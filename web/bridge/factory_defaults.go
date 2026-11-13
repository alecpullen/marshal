package bridge

import "fmt"

// factoryResolveDefaults applies the project's factory defaults to a handoff.
//
// The project is the one the handoff names, because a valid handoff always
// names a registered, trusted root and the handoff is the primary place
// configuration is selected. The repository's intake project is consulted only
// when the handoff names no root, and then only to choose which defaults are
// read: that fallback never writes a root into the configuration, and a
// rootless handoff is rejected by preflight before any work item exists.
//
// The defaults are validated again here, even though they were validated when
// they were written: a workspace file can be written by an older binary that
// does not know the field, or edited by hand. A default that is invalid on its
// own is an error naming the project's defaults as the source, never a silent
// fallback to the handoff's own values.
//
// The only cross-source conflict — a default workspace against a handoff that
// asks for a native run — is reported with both sources. A handoff that sets
// both fields itself is a handoff-internal conflict and is left to
// factoryNormalize, because the project's defaults are not involved.
//
// The retry path never calls this function: a retry replacement is an
// explicit, operator-authored configuration and is taken as written.
func (a *factoryAPI) factoryResolveDefaults(req FactoryCreate) (FactoryCreate, error) {
	if a == nil || a.fleet == nil || a.fleet.ws == nil {
		return req, nil
	}
	root := req.Config.ProjectRoot
	if root == "" {
		// Defensive only: this chooses which defaults are consulted for a
		// handoff that preflight is about to reject. It never supplies a root.
		root = a.fleet.ws.ProjectRootByRepo(req.Config.RepoID)
	}
	if root == "" {
		return req, nil
	}
	defaults := a.fleet.ws.ProjectSettingsFor(root).FactoryDefaults
	if defaults == nil {
		return req, nil
	}
	if err := factoryValidateDefaults(defaults); err != nil {
		return FactoryCreate{}, fmt.Errorf("%w: project factory defaults for %s: %v", errFactoryConfig, root, err)
	}
	// The only cross-source conflict is a project default workspace against a
	// handoff that asks for a native run. Native is the only non-inheritable
	// field that can collide with an inheritable one, so it is the only
	// conflict resolution has to report. A handoff that sets both itself is a
	// handoff-internal conflict: the project's defaults are not involved, and
	// factoryNormalize stays responsible for it unchanged.
	if req.Config.Native && req.Config.Workspace == "" && defaults.Workspace != "" {
		return FactoryCreate{}, fmt.Errorf("%w: factory configuration conflict: workspace and native are mutually exclusive (project defaults for %s set workspace; handoff sets native)", errFactoryInput, root)
	}
	req.Config = factoryMergeDefaults(req.Config, defaults)
	return req, nil
}

// factoryMergeDefaults applies a project's defaults to a handoff's
// configuration. The handoff wins for every field it sets; "unset" is
// per-type, as the specification's §4.3 defines it: an empty string, a zero
// int, a nil or zero-length raw message, and a nil check list. A non-nil empty
// check list is an explicit choice and survives.
//
// The defaults are never mutated: the inherited check list is copied rather
// than aliased.
func factoryMergeDefaults(c FactoryConfig, d *FactoryDefaults) FactoryConfig {
	if d == nil {
		return c
	}
	if c.TargetBranch == "" {
		c.TargetBranch = d.TargetBranch
	}
	if c.Workspace == "" {
		c.Workspace = d.Workspace
	}
	if c.HostID == "" {
		c.HostID = d.HostID
	}
	if len(c.ImplementerRouting) == 0 {
		c.ImplementerRouting = d.ImplementerRouting
	}
	if len(c.ReviewerRouting) == 0 {
		c.ReviewerRouting = d.ReviewerRouting
	}
	if c.ReviewerInstructions == "" {
		c.ReviewerInstructions = d.ReviewerInstructions
	}
	if c.ExpectedChecks == nil {
		// make, not append, so an inherited empty list stays non-nil.
		if d.ExpectedChecks != nil {
			c.ExpectedChecks = make([]FactoryCheckKey, len(d.ExpectedChecks))
			copy(c.ExpectedChecks, d.ExpectedChecks)
		}
	}
	if c.MaxRounds == 0 {
		c.MaxRounds = d.MaxRounds
	}
	if c.CIPollSeconds == 0 {
		c.CIPollSeconds = d.CIPollSeconds
	}
	if c.CIWaitSeconds == 0 {
		c.CIWaitSeconds = d.CIWaitSeconds
	}
	return c
}

// factoryValidateDefaults checks a project's factory defaults on their own.
// It covers the parts of factoryNormalize that do not depend on a handoff:
// the limits, the local-host rule, and every expected check's identity.
//
// It never fills anything in. A zero limit means "unset" and stays unset, so
// a handoff can inherit it; the built-in limits are applied later, to the
// merged configuration.
//
// The checks run in a fixed order so the error is deterministic.
func factoryValidateDefaults(d *FactoryDefaults) error {
	if d == nil {
		return nil
	}
	if d.MaxRounds < 0 {
		return fmt.Errorf("factory defaults: maxRounds must not be negative")
	}
	if d.CIPollSeconds < 0 {
		return fmt.Errorf("factory defaults: ciPollSeconds must not be negative")
	}
	if d.CIWaitSeconds < 0 {
		return fmt.Errorf("factory defaults: ciWaitSeconds must not be negative")
	}
	if d.HostID != "" && d.HostID != "local" {
		return fmt.Errorf("factory defaults: hostId must be local, got %q", d.HostID)
	}
	for n, k := range d.ExpectedChecks {
		// Duplicated from factoryNormalize deliberately: that function is
		// untouched by this slice, so the rule is not extracted from it.
		if k.Name == "" || (k.Kind != "check_run" && k.Kind != "commit_status") || (k.Kind == "check_run" && k.AppID <= 0) || (k.Kind == "commit_status" && k.AppID != 0) {
			return fmt.Errorf("factory defaults: expectedChecks[%d] has an invalid check identity", n)
		}
	}
	return nil
}
