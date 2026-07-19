package routing

import (
	"errors"
	"fmt"
	"net/url"
)

var (
	ErrProfileNotFound       = errors.New("routing: profile not found")
	ErrPresetNotFound        = errors.New("routing: preset not found")
	ErrRemoteProviderBlocked = errors.New("routing: remote provider blocked")

	errRoleNotConfigured = errors.New("routing: role not configured")
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
// preset → legacy provider. The swarm orchestrator uses this to give each
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
	if legacy, ok := r.legacyRoute(role); ok {
		return legacy, nil
	}
	if fallbackErr != nil {
		return Route{}, fallbackErr
	}
	return Route{}, err
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
	presetName, ok := profile.Roles[role]
	if !ok || presetName == "" {
		return Route{}, fmt.Errorf("%w: %s role %s", errRoleNotConfigured, profile.Name, role)
	}
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
		Profile:       profile.Name,
		Preset:        preset,
		ContextBudget: r.config.ContextBudgets[role],
	}, nil
}

func isNoConfiguredRoute(err error) bool {
	return errors.Is(err, ErrProfileNotFound) || errors.Is(err, errRoleNotConfigured)
}

func (r *StaticRouter) legacyRoute(role AgentRole) (Route, bool) {
	if r.config.LegacyProvider == "" || r.config.LegacyModel == "" {
		return Route{}, false
	}
	if !r.config.RemoteAllowed && !isLocalProvider(r.config.LegacyProvider) {
		// F-SEC-09: the legacy provider is remote but the user has
		// opted out of remote providers. Returning (Route{}, false)
		// makes the caller's `_, ok` form fall through to the
		// existing errRoleNotConfigured error, which the surface
		// layer turns into a user-visible "no route for role X"
		// message.
		return Route{}, false
	}
	return Route{
		Role:    role,
		Profile: "legacy",
		Preset: ModelPreset{
			Name:     "legacy",
			Provider: r.config.LegacyProvider,
			Model:    r.config.LegacyModel,
		},
		ContextBudget: ContextBudget{
			MaxRepoContextTokens: 8000,
		},
		Legacy: true,
	}, true
}

// isLocalProvider returns true if the provider URL targets the local
// machine. Used to bypass the remote_providers_allowed gate for
// localhost-only deployments. See F-SEC-09.
func isLocalProvider(provider string) bool {
	u, err := url.Parse(provider)
	if err != nil {
		return false
	}
	host := u.Hostname()
	return host == "localhost" || host == "127.0.0.1" || host == "::1" || host == ""
}
