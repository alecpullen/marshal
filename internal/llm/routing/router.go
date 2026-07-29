package routing

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

var (
	ErrProfileNotFound       = errors.New("routing: profile not found")
	ErrPresetNotFound        = errors.New("routing: preset not found")
	ErrRemoteProviderBlocked = errors.New("routing: remote provider blocked")

	errRoleNotConfigured   = errors.New("routing: role not configured")
	errCustomAgentNotFound = errors.New("routing: custom agent not found")

	// ErrEmbeddingNotConfigured is returned by ResolveEmbedding when the active
	// profile has no embedding role. Callers use it to gracefully disable
	// embedding-dependent features rather than fail.
	ErrEmbeddingNotConfigured = errors.New("routing: embedding role not configured")
)

type StaticRouter struct {
	config Config
}

func NewStaticRouter(config Config) *StaticRouter {
	return &StaticRouter{config: config}
}

func (r *StaticRouter) Resolve(class string) (Route, error) {
	return r.ResolveRole(roleForTaskClass(class))
}

// ResolveRole resolves a route for an explicit agent role, with the same
// fallback chain Resolve uses: configured role preset → implementer
// preset. The swarm orchestrator uses this to give each
// role its own model preset (asymmetric local swarm, docs/07).
func (r *StaticRouter) ResolveRole(role AgentRole) (Route, error) {
	route, err := r.resolveProfileRole(role)
	if err == nil {
		return route, nil
	}
	if !isNoConfiguredRoute(err) {
		return Route{}, err
	}
	var fallbackErr error
	if role != RoleImplementer && errors.Is(err, errRoleNotConfigured) {
		fallback, fErr := r.resolveProfileRole(RoleImplementer)
		fallbackErr = fErr
		if fallbackErr == nil {
			return fallback, nil
		}
		if !isNoConfiguredRoute(fallbackErr) {
			return Route{}, fallbackErr
		}
	}
	if fallbackErr != nil {
		return Route{}, fallbackErr
	}
	return Route{}, err
}

// ResolveEmbedding resolves the embedding provider+model from the active
// profile's embedding role. Unlike ResolveRole it has NO implementer fallback
// — a chat model cannot produce vectors. Returns ErrEmbeddingNotConfigured
// when the role is unset (or no profile exists), and ErrRemoteProviderBlocked
// when the resolved preset is remote under remote_providers_allowed = false.
func (r *StaticRouter) ResolveEmbedding() (Route, error) {
	route, err := r.resolveProfileRole(RoleEmbedding)
	if err != nil {
		if isNoConfiguredRoute(err) {
			return Route{}, ErrEmbeddingNotConfigured
		}
		return Route{}, err
	}
	return route, nil
}

func roleForTaskClass(class string) AgentRole {
	switch class {
	case "question":
		return RoleRepoScout
	case "knowledge":
		return RoleKnowledge
	case "edit", "command":
		return RoleImplementer
	default:
		return RoleImplementer
	}
}

func (r *StaticRouter) resolveProfileRole(role AgentRole) (Route, error) {
	profile, ok := r.config.Profiles[r.config.DefaultProfile]
	if !ok {
		return Route{}, fmt.Errorf("%w: %s", ErrProfileNotFound, r.config.DefaultProfile)
	}
	binding, ok := profile.Roles[role]
	if !ok || (binding.Preset == "" && binding.CustomAgent == "") {
		return Route{}, fmt.Errorf("%w: %s role %s", errRoleNotConfigured, profile.Name, role)
	}
	if binding.CustomAgent != "" {
		return r.resolveAgentBinding(binding.CustomAgent, role, profile.Name)
	}
	return r.resolvePresetBinding(binding.Preset, role, profile.Name)
}

func (r *StaticRouter) resolvePresetBinding(presetName string, role AgentRole, profileName string) (Route, error) {
	preset, ok := r.config.Presets[presetName]
	if !ok {
		return Route{}, fmt.Errorf("%w: %s", ErrPresetNotFound, presetName)
	}
	if preset.Name == "" {
		preset.Name = presetName
	}
	if !preset.LocalOnly && !r.config.RemoteAllowed {
		return Route{}, fmt.Errorf("%w: preset %s", ErrRemoteProviderBlocked, presetName)
	}
	return Route{
		Role:          role,
		Profile:       profileName,
		Preset:        preset,
		ContextBudget: r.config.ContextBudgets[role],
	}, nil
}

// ResolveCustomAgent resolves a named custom agent. If the agent's own
// Preset is empty, it falls back through the role it was invoked as
// (ResolveRole's implementer→legacy chain). The agent's overrides are
// attached as Route.CustomAgent so runner construction can apply them.
func (r *StaticRouter) ResolveCustomAgent(name string, asRole AgentRole) (Route, error) {
	agent, ok := r.config.CustomAgents[name]
	if !ok {
		return Route{}, fmt.Errorf("%w: custom agent %s", errCustomAgentNotFound, name)
	}
	agent.Name = name
	if agent.Preset != "" {
		profileName := r.config.DefaultProfile
		route, err := r.resolvePresetBinding(agent.Preset, asRole, profileName)
		if err != nil {
			return Route{}, err
		}
		route.CustomAgent = &agent
		if agent.Context.MaxRepoContextTokens > 0 {
			route.ContextBudget = agent.Context
		}
		return route, nil
	}
	// No preset: fall back through the role's resolution, but attach the agent.
	route, err := r.ResolveRole(asRole)
	if err != nil {
		return Route{}, err
	}
	route.CustomAgent = &agent
	return route, nil
}

func (r *StaticRouter) resolveAgentBinding(name string, role AgentRole, profileName string) (Route, error) {
	agent, ok := r.config.CustomAgents[name]
	if !ok {
		return Route{}, fmt.Errorf("%w: custom agent %s", errCustomAgentNotFound, name)
	}
	agent.Name = name
	if agent.Preset == "" {
		// Fall back through ResolveRole (implementer→legacy), attach agent.
		route, err := r.ResolveRole(role)
		if err != nil {
			return Route{}, err
		}
		route.CustomAgent = &agent
		route.Profile = profileName
		if agent.Context.MaxRepoContextTokens > 0 {
			route.ContextBudget = agent.Context
		}
		return route, nil
	}
	preset, ok := r.config.Presets[agent.Preset]
	if !ok {
		return Route{}, fmt.Errorf("%w: custom agent %s preset %s", ErrPresetNotFound, name, agent.Preset)
	}
	if preset.Name == "" {
		preset.Name = agent.Preset
	}
	if !preset.LocalOnly && !r.config.RemoteAllowed {
		return Route{}, fmt.Errorf("%w: custom agent %s", ErrRemoteProviderBlocked, name)
	}
	route := Route{
		Role:          role,
		Profile:       profileName,
		Preset:        preset,
		ContextBudget: r.config.ContextBudgets[role],
		CustomAgent:   &agent,
	}
	if agent.Context.MaxRepoContextTokens > 0 {
		route.ContextBudget = agent.Context
	}
	return route, nil
}

func isNoConfiguredRoute(err error) bool {
	return errors.Is(err, ErrProfileNotFound) || errors.Is(err, errRoleNotConfigured)
}

// IsLocalProvider reports whether the provider URL targets the local
// machine. Used to bypass the remote_providers_allowed gate for
// localhost-only deployments (F-SEC-09) and by the TUI for local badges.
func IsLocalProvider(provider string) bool {
	u, err := url.Parse(provider)
	if err != nil {
		return false
	}
	host := u.Hostname()
	switch host {
	case "", "localhost", "127.0.0.1", "0.0.0.0", "::1":
		return true
	}
	return strings.HasPrefix(host, "::1%")
}

// CastEntry holds the result of resolving a single role via Cast.
type CastEntry struct {
	Role  AgentRole
	Route Route
	Err   error
}

// Cast resolves every role in the given list using the same resolution and
// fallback chain as ResolveRole. Unlike ResolveRole, Cast never short-circuits
// on errors — it resolves every requested role and returns all results. It is
// used only for pre-flight display (e.g. the startup banner).
func (r *StaticRouter) Cast(roles []AgentRole) []CastEntry {
	entries := make([]CastEntry, 0, len(roles))
	for _, role := range roles {
		route, err := r.ResolveRole(role)
		entries = append(entries, CastEntry{Role: role, Route: route, Err: err})
	}
	return entries
}

// WithRoleOverride returns a copy of the config with one role's binding in
// the default profile overridden to the given preset. Used by the SDD
// controller's model-escalation swap (rebuild RunnerFactory with a stronger
// model for RoleSDDOrchestrator). The original config is not mutated.
func (c Config) WithRoleOverride(role AgentRole, preset string) Config {
	out := c
	out.Profiles = make(map[string]AgentProfile, len(c.Profiles))
	for name, profile := range c.Profiles {
		p := profile
		if name == c.DefaultProfile {
			p.Roles = make(map[AgentRole]RoleBinding, len(profile.Roles))
			for r, rb := range profile.Roles {
				p.Roles[r] = rb
			}
			p.Roles[role] = RoleBinding{Preset: preset}
		}
		out.Profiles[name] = p
	}
	return out
}

var (
	// SwarmCastRoles lists the agent roles used by the swarm orchestrator.
	// Used by Cast for pre-flight display.
	SwarmCastRoles = []AgentRole{RolePlanner, RoleRepoScout, RoleImplementer, RoleTester, RoleReviewer}

	// SDDCastRoles lists the worker roles used by the SDD pipeline. The
	// orchestrator is intentionally excluded — it is dispatched by the
	// controller (Go state machine), not shown as a worker in the pre-flight
	// cast list.
	SDDCastRoles = []AgentRole{
		RoleSDDImplementer,
		RoleSDDReviewer,
		RoleSDDBranchReviewer,
	}
)
