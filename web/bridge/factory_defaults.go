package bridge

import (
	"encoding/json"
	"fmt"
)

// factoryResolveDefaults applies the project's defaults to a handoff.
//
// Resolution has two precedence levels, and the handoff wins over both. Each
// level only fills a field the levels above it left unset, so a value the
// handoff states itself is never rewritten:
//
//  1. the project's factory defaults (FactoryDefaults), which cover every
//     inheritable factory field;
//  2. the project's agent defaults (ProjectSettings.Workspace and the routing
//     profile in ProjectSettings.Routing), which apply to exactly three
//     fields: the workspace, the implementer's routing and the reviewer's
//     routing.
//
// Both levels are read from one snapshot of the project's settings, and
// existing normalisation and validation then run on the result unchanged.
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
	// One read of the settings serves both precedence levels, so resolution
	// never mixes a factory default from one snapshot with an agent default
	// from another.
	settings := a.fleet.ws.ProjectSettingsFor(root)
	defaults := settings.FactoryDefaults
	if defaults != nil {
		if err := factoryValidateDefaults(defaults); err != nil {
			return FactoryCreate{}, fmt.Errorf("%w: project factory defaults for %s: %v", errFactoryConfig, root, err)
		}
		// The only cross-source conflict is a project default workspace
		// against a handoff that asks for a native run. Native is the only
		// non-inheritable field that can collide with an inheritable one, so
		// it is the only conflict resolution has to report. A handoff that
		// sets both itself is a handoff-internal conflict: the project's
		// defaults are not involved, and factoryNormalize stays responsible
		// for it unchanged. The check runs before the lower level's workspace
		// fallback, which a native handoff suppresses rather than conflicts
		// with.
		if req.Config.Native && req.Config.Workspace == "" && defaults.Workspace != "" {
			return FactoryCreate{}, fmt.Errorf("%w: factory configuration conflict: workspace and native are mutually exclusive (project defaults for %s set workspace; handoff sets native)", errFactoryInput, root)
		}
		req.Config = factoryMergeDefaults(req.Config, defaults)
	}
	req.Config = factoryApplyProjectAgentDefaults(req.Config, settings)
	return req, nil
}

// factoryApplyProjectAgentDefaults is resolution's lower precedence level: it
// fills the three fields a project's agent settings cover — the workspace, the
// implementer's routing and the reviewer's routing — where the handoff and the
// project's factory defaults both left them unset.
//
// An explicit native handoff suppresses the workspace fallback instead of
// conflicting with it: native and a workspace are mutually exclusive, so
// inheriting a project workspace into a native run would turn a valid handoff
// into an invalid one. The fallback is the only direction the suppression
// runs: the backend never infers native from an empty project workspace, so a
// project with none leaves the handoff exactly as it was.
//
// The routing fallback is the project's routing *profile* and nothing else.
// The project's per-role overrides belong to its interactive agents, not to
// this run, and an explicit factory or handoff route is never rewritten. A
// project with no usable profile contributes no fallback at all, and the
// stored routing message is only ever read.
func factoryApplyProjectAgentDefaults(c FactoryConfig, ps ProjectSettings) FactoryConfig {
	if !c.Native && c.Workspace == "" {
		c.Workspace = ps.Workspace
	}
	profile := factoryProjectRoutingProfile(ps.Routing)
	if profile == "" {
		return c
	}
	if len(c.ImplementerRouting) == 0 {
		c.ImplementerRouting = factoryProfileRouting(profile)
	}
	if len(c.ReviewerRouting) == 0 {
		c.ReviewerRouting = factoryProfileRouting(profile)
	}
	return c
}

// projectRoutingProfileRoute is the whole route the fallback constructs: the
// project's routing profile, with no per-role overrides.
type projectRoutingProfileRoute struct {
	Profile string `json:"profile"`
}

// factoryProfileRouting renders the fallback route for a profile.
func factoryProfileRouting(profile string) json.RawMessage {
	raw, err := json.Marshal(projectRoutingProfileRoute{Profile: profile})
	if err != nil {
		// A string always marshals; keeping the route unset is the only
		// safe answer if that ever changed.
		return nil
	}
	return raw
}

// factoryProjectRoutingProfile reads the routing profile a project's agent
// settings select, or "" when they select none. Routing is stored as raw JSON,
// so an absent, null, malformed or non-string profile all read as "none": the
// fallback contributes nothing rather than guessing, and nothing is written
// back to the stored message.
func factoryProjectRoutingProfile(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var route projectRoutingProfileRoute
	if err := json.Unmarshal(raw, &route); err != nil {
		return ""
	}
	return route.Profile
}

// factoryMergeDefaults is resolution's upper precedence level: it applies a
// project's factory defaults to a handoff's configuration. The handoff wins
// for every field it sets; "unset" is per-type, as the specification's §4.3
// defines it: an empty string, a zero int, a nil or zero-length raw message,
// and a nil check list. A non-nil empty check list is an explicit choice and
// survives. Native is not among these fields: it is non-inheritable, so the
// handoff's value is the only one that can be in effect when the lower level
// (factoryApplyProjectAgentDefaults) decides whether to suppress its workspace
// fallback.
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
