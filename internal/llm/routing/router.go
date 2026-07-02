package routing

import (
	"errors"
	"fmt"
)

var (
	ErrProfileNotFound       = errors.New("routing: profile not found")
	ErrPresetNotFound        = errors.New("routing: preset not found")
	ErrRemoteProviderBlocked = errors.New("routing: remote provider blocked")
	ErrNoRoute               = errors.New("routing: no route available")
)

type StaticRouter struct {
	config Config
}

func NewStaticRouter(config Config) *StaticRouter {
	return &StaticRouter{config: config}
}

func (r *StaticRouter) Resolve(task TaskProfile) (Route, error) {
	role := roleForTaskClass(task.Class)
	route, err := r.resolveProfileRole(role)
	if err == nil {
		return route, nil
	}
	if role != RoleImplementer {
		if fallback, fallbackErr := r.resolveProfileRole(RoleImplementer); fallbackErr == nil {
			return fallback, nil
		}
	}
	if legacy, ok := r.legacyRoute(role); ok {
		return legacy, nil
	}
	return Route{}, err
}

func roleForTaskClass(class string) AgentRole {
	switch class {
	case "question":
		return RoleRepoScout
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
		return Route{}, fmt.Errorf("%w: %s role %s", ErrPresetNotFound, profile.Name, role)
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

func (r *StaticRouter) legacyRoute(role AgentRole) (Route, bool) {
	if r.config.LegacyProvider == "" || r.config.LegacyModel == "" {
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
		Legacy: true,
	}, true
}
