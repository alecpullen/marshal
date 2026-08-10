package routing

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"marshal/internal/llm/catalog"
)

var (
	ErrProfileNotFound       = errors.New("routing: profile not found")
	ErrPresetNotFound        = errors.New("routing: preset not found")
	ErrRemoteProviderBlocked = errors.New("routing: remote provider blocked")
	// ErrUnknownProvider is returned when an explicit provider/model pair
	// names a provider that has no configured preset.
	ErrUnknownProvider = errors.New("routing: unknown provider")

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

// ResolveRole resolves a route for an explicit agent role using the
// profile's inheritance chain: the role's own binding → the fast model (for
// roles in FastRoles) → the implementer preset. The swarm orchestrator uses
// this to give each role its own model preset (asymmetric local swarm,
// docs/07).
func (r *StaticRouter) ResolveRole(role AgentRole) (Route, error) {
	return r.resolveProfileRole(role)
}

// ResolveEmbedding resolves the embedding provider+model. It prefers the
// structural [indexing] embedding_preset (r.config.EmbeddingPreset), falling
// back to the active profile's legacy embedding role binding for configs
// built without the load-time migration. Unlike ResolveRole it has NO
// implementer fallback — a chat model cannot produce vectors. Returns
// ErrEmbeddingNotConfigured when neither source is set (or no profile
// exists), and ErrRemoteProviderBlocked when the resolved preset is remote
// under remote_providers_allowed = false.
func (r *StaticRouter) ResolveEmbedding() (Route, error) {
	if r.config.EmbeddingPreset != "" {
		// Structural [indexing] embedding_preset — resolve directly. A
		// missing preset name surfaces ErrPresetNotFound (not the friendly
		// "not configured"), so a typo is visible.
		return r.resolvePresetBinding(r.config.EmbeddingPreset, RoleEmbedding, r.config.DefaultProfile)
	}
	// Legacy fallback: per-profile embedding role binding.
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
	binding, from, ok := profile.EffectiveBinding(role)
	if !ok {
		// The whole chain failed. Name the final fallback rung so the error
		// points at what also wasn't found, matching the pre-fast-chain
		// behavior where the implementer fallback error won.
		errRole := role
		if role != RoleImplementer && role != RoleEmbedding {
			errRole = RoleImplementer
		}
		return Route{}, fmt.Errorf("%w: %s role %s", errRoleNotConfigured, profile.Name, errRole)
	}
	if binding.CustomAgent != "" {
		return r.resolveAgentBinding(binding.CustomAgent, from, profile.Name)
	}
	// The route reports the rung that supplied the binding: a fallback to
	// the implementer preset surfaces as RoleImplementer (the pre-existing
	// contract), and a fast-rung resolution surfaces as RoleFast.
	return r.resolvePresetBinding(binding.Preset, from, profile.Name)
}

// IsCanonicalPresetName reports whether name is exactly "<provider>/<model>".
func IsCanonicalPresetName(name string) bool {
	provider, model, ok := strings.Cut(name, "/")
	return ok && provider != "" && model != "" && !strings.Contains(provider, "/")
}

// defaultPreset synthesizes a ModelPreset for providerName/modelID from the
// catalog and provider base URL. It is used when no explicit
// [models.presets.<provider>/<model>] override exists.
func defaultPreset(providerName, modelID, baseURL string) ModelPreset {
	ctx, out := catalog.Lookup(modelID)
	return ModelPreset{
		Name:            providerName + "/" + modelID,
		Provider:        providerName,
		Model:           modelID,
		ContextWindow:   ctx,
		MaxOutputTokens: out,
		LocalOnly:       IsLocalProvider(baseURL),
	}
}

// presetProviderBaseURL returns the configured base URL for a provider name,
// or "" when the provider is unknown.
func (r *StaticRouter) presetProviderBaseURL(providerName string) string {
	return r.config.ProviderBaseURLs[providerName]
}

func (r *StaticRouter) resolvePresetBinding(presetName string, role AgentRole, profileName string) (Route, error) {
	preset, ok := r.config.Presets[presetName]
	if !ok {
		provider, model, ok := strings.Cut(presetName, "/")
		if !ok {
			return Route{}, fmt.Errorf("%w: %s", ErrPresetNotFound, presetName)
		}
		// Synthesize a default preset only when the provider is actually
		// configured. A typo in a profile binding (e.g. "ollma/gpt-4o")
		// should error, not silently route to a non-existent provider.
		if _, known := r.config.ProviderBaseURLs[provider]; !known {
			return Route{}, fmt.Errorf("%w: %s", ErrPresetNotFound, presetName)
		}
		preset = defaultPreset(provider, model, r.presetProviderBaseURL(provider))
	}
	preset.Name = presetName
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

// ResolveExplicitModel resolves an explicit "provider/model" pair (e.g.
// "openai/gpt-4o-mini") against the configured presets. The preset map key
// must be exactly the pair string; there is no fuzzy scan. It returns a Route
// carrying that preset, so pricing, local-only gating, and context budgets
// resolve exactly as if the model were bound to a role.
func (r *StaticRouter) ResolveExplicitModel(pair string, asRole AgentRole) (Route, error) {
	provider, model, ok := strings.Cut(pair, "/")
	if !ok || provider == "" || model == "" {
		return Route{}, fmt.Errorf("%w: invalid provider/model pair %q", ErrUnknownProvider, pair)
	}
	preset, ok := r.config.Presets[pair]
	if !ok {
		if _, known := r.config.ProviderBaseURLs[provider]; !known {
			return Route{}, fmt.Errorf("%w: no preset configured for %q", ErrUnknownProvider, pair)
		}
		preset = defaultPreset(provider, model, r.presetProviderBaseURL(provider))
	}
	preset.Name = pair
	if !preset.LocalOnly && !r.config.RemoteAllowed {
		return Route{}, fmt.Errorf("%w: preset %s", ErrRemoteProviderBlocked, pair)
	}
	return Route{
		Role:          asRole,
		Profile:       r.config.DefaultProfile,
		Preset:        preset,
		ContextBudget: r.config.ContextBudgets[asRole],
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
// the default profile overridden to the given preset. The original config
// is not mutated.
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
