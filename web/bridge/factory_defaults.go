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
	// The subagent overrides are not among the fields this level fills: the
	// project's factory defaults state them with pointer fields, and the
	// draft keeps only what it states itself. Both are carried here exactly
	// as the handoff wrote them, so resolution still has the original
	// overrides and their absence, not a copy of a resolved value.
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
	// The subagent overrides are resolved per role and per field, not copied:
	// the handoff keeps only the overrides it stated, and the project's
	// overrides stay on the defaults where resolution reads them. Copying
	// either direction would collapse "inherit" into a value, which is what
	// lets a draft follow a changed default while its own override stays
	// fixed. Both fields are therefore carried unchanged, and
	// factoryResolveSubagents is the resolution.
	return c
}

// factorySubagentRuntime is the Marshal side of the inheritance chain: the
// child settings a session reports, and the digest of the config and route
// inputs they were derived from. It is read from the session's status JSON —
// the bridge never imports the agent side — and an absent report is nil, which
// contributes nothing to resolution.
type factorySubagentRuntime struct {
	// Settings is whether the session advertises per-session child settings.
	// A session that reports defaults but not the capability cannot be made to
	// enforce an override, so an override that demands one is unsupported.
	Settings    bool
	Enabled     bool
	Model       string
	Concurrency int
	Digest      string
}

// The sources a resolved field can come from, most specific first. They are
// part of the result: they are how a reviewer tells an inherited value from a
// stated one, and how the approval proves what it froze.
const (
	factorySubagentSourceItem    = "item"
	factorySubagentSourceProject = "project"
	factorySubagentSourceRuntime = "runtime"
)

// factorySubagentResolution is one role's resolved settings and, per field,
// the source it came from. A field no source states is empty and holds its
// zero value, which is exactly what an approval journalled before subagent
// settings existed records.
type factorySubagentResolution struct {
	Settings          FactorySubagentSettings
	EnabledSource     string
	ModelSource       string
	ConcurrencySource string
}

// factoryResolveSubagentRole resolves one role's settings: the item's
// override, then the project's, then the session's reported default. Each
// field resolves on its own, so a draft that states only a model still
// inherits enablement and concurrency.
//
// An override is a pointer, so an explicit false and an explicit empty model
// are values, not absences: they win over every source below them.
func factoryResolveSubagentRole(item, project *FactorySubagentOverrides, rt *factorySubagentRuntime) factorySubagentResolution {
	var out factorySubagentResolution
	for _, level := range []struct {
		overrides *FactorySubagentOverrides
		source    string
	}{
		{item, factorySubagentSourceItem},
		{project, factorySubagentSourceProject},
	} {
		if level.overrides == nil {
			continue
		}
		if out.EnabledSource == "" && level.overrides.Enabled != nil {
			out.Settings.Enabled = *level.overrides.Enabled
			out.EnabledSource = level.source
		}
		if out.ModelSource == "" && level.overrides.Model != nil {
			out.Settings.Model = *level.overrides.Model
			out.ModelSource = level.source
		}
		if out.ConcurrencySource == "" && level.overrides.Concurrency != nil {
			out.Settings.Concurrency = *level.overrides.Concurrency
			out.ConcurrencySource = level.source
		}
	}
	if rt != nil {
		if out.EnabledSource == "" {
			out.Settings.Enabled = rt.Enabled
			out.EnabledSource = factorySubagentSourceRuntime
		}
		if out.ModelSource == "" {
			out.Settings.Model = rt.Model
			out.ModelSource = factorySubagentSourceRuntime
		}
		if out.ConcurrencySource == "" {
			out.Settings.Concurrency = rt.Concurrency
			out.ConcurrencySource = factorySubagentSourceRuntime
		}
	}
	return out
}

// factoryResolveSubagents resolves both roles of a draft against the project's
// factory defaults and the session's reported defaults. It is the only place
// the chain is applied, so a draft view and an approval resolve the same way;
// it reads the stored overrides and never writes the draft.
func factoryResolveSubagents(c FactoryConfig, d *FactoryDefaults, rt *factorySubagentRuntime) (implementer, reviewer factorySubagentResolution) {
	var projectImplementer, projectReviewer *FactorySubagentOverrides
	if d != nil {
		projectImplementer, projectReviewer = d.ImplementerSubagents, d.ReviewerSubagents
	}
	return factoryResolveSubagentRole(c.ImplementerSubagents, projectImplementer, rt),
		factoryResolveSubagentRole(c.ReviewerSubagents, projectReviewer, rt)
}

// factorySubagentOverridesPresent reports whether one role's overrides state
// anything at all.
func factorySubagentOverridesPresent(o *FactorySubagentOverrides) bool {
	return o != nil && (o.Enabled != nil || o.Model != nil || o.Concurrency != nil)
}

// factorySubagentEnforcement reports whether an approval is asking the runtime
// to enforce subagent settings. A project default that states an override
// demands it just as a draft override does, because the run inherits it.
func factorySubagentEnforcement(c FactoryConfig, d *FactoryDefaults) bool {
	if factorySubagentOverridesPresent(c.ImplementerSubagents) || factorySubagentOverridesPresent(c.ReviewerSubagents) {
		return true
	}
	return d != nil && (factorySubagentOverridesPresent(d.ImplementerSubagents) || factorySubagentOverridesPresent(d.ReviewerSubagents))
}

// The cap a session enforces on agent.run children in flight. A snapshot
// outside this range is refused at approval rather than clamped: a silently
// reduced cap is not the setting the operator approved.
const (
	factorySubagentMinConcurrency = 1
	factorySubagentMaxConcurrency = 8
)

// factorySubagentConcurrencySupported reports whether a resolved concurrency
// is one a session can enforce. Zero means "no snapshot" and is allowed only
// where nothing is being enforced; a cap is validated, never clamped.
func factorySubagentConcurrencySupported(n int) bool {
	return n >= factorySubagentMinConcurrency && n <= factorySubagentMaxConcurrency
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
