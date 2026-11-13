package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// factoryRouteSelection reports which of the two routes a retry command
// replaces. The routes are independent: one may change without the other.
//
// A route that is not named is inherited from the latest approval, byte for
// byte. A route that is named replaces the previous route wholesale, so any
// per-role override the previous routing object carried is dropped with it:
// the operator re-stated the route.
type factoryRouteSelection struct {
	implementer    json.RawMessage
	reviewer       json.RawMessage
	hasImplementer bool
	hasReviewer    bool
}

// factoryValidateRoutes applies the route rules to both routes of a
// configuration: an explicit routing profile, a known profile, a resolvable
// preset, a configured provider and its credential.
//
// The preflight and the route-only retry both call this, so "the same rules
// apply" is a structural property rather than a convention. Every message
// names the role whose route was at fault, because the helper checks both
// routes of a configuration.
func (a *factoryAPI) factoryValidateRoutes(ctx context.Context, c FactoryConfig) error {
	if a.fleet == nil {
		return fmt.Errorf("%w: fleet unavailable", ErrFactoryUnavailable)
	}
	raw, err := a.fleet.controlCall(ctx, "config/get", nil, false)
	if err != nil {
		return fmt.Errorf("%w: model configuration unavailable", ErrFactoryUnavailable)
	}
	var modelConfig struct {
		Providers map[string]struct {
			HasKey    bool   `json:"hasKey"`
			Type      string `json:"type"`
			APIKeyEnv string `json:"apiKeyEnv"`
		} `json:"providers"`
		Presets map[string]struct {
			Provider string `json:"provider"`
			Model    string `json:"model"`
		} `json:"presets"`
		Profiles map[string]map[string]struct {
			Preset string `json:"preset"`
		} `json:"profiles"`
		DefaultProfile string `json:"defaultProfile"`
	}
	if json.Unmarshal(raw, &modelConfig) != nil {
		return fmt.Errorf("%w: invalid model configuration", ErrFactoryUnavailable)
	}
	for n, route := range []json.RawMessage{c.ImplementerRouting, c.ReviewerRouting} {
		role := "implementer"
		if n == 1 {
			role = "reviewer"
		}
		var selection struct {
			Profile   string            `json:"profile"`
			Overrides map[string]string `json:"overrides"`
		}
		if len(route) == 0 || json.Unmarshal(route, &selection) != nil {
			return fmt.Errorf("%w: explicit %s routing required", errFactoryConfig, role)
		}
		if selection.Profile == "" {
			return fmt.Errorf("%w: explicit %s routing profile required", errFactoryConfig, role)
		}
		profile := selection.Profile
		if profile == "" {
			profile = modelConfig.DefaultProfile
		}
		bindings, ok := modelConfig.Profiles[profile]
		if !ok {
			return fmt.Errorf("%w: unknown %s routing profile", errFactoryConfig, role)
		}
		preset := selection.Overrides[role]
		if preset == "" {
			preset = bindings[role].Preset
		}
		p, ok := modelConfig.Presets[preset]
		if !ok || p.Model == "" || p.Provider == "" {
			return fmt.Errorf("%w: %s model preset unavailable", errFactoryConfig, role)
		}
		provider, ok := modelConfig.Providers[p.Provider]
		if !ok || provider.Type == "" {
			return fmt.Errorf("%w: %s provider unavailable", errFactoryConfig, role)
		}
		if provider.Type != "openai_codex" && (provider.Type == "anthropic" || provider.APIKeyEnv != "") && !provider.HasKey {
			return fmt.Errorf("%w: %s provider credential unavailable", errFactoryConfig, role)
		}
	}
	return nil
}

// factoryRouteSelectionOf reads the two routing fields a retry command
// carries.
//
// A field that is absent leaves its route alone. A field that is present
// with the JSON value null is invalid input: null is not a route, and
// reading it as "unchanged" would make the client's intent ambiguous.
func factoryRouteSelectionOf(c FactoryCommand) (factoryRouteSelection, error) {
	sel := factoryRouteSelection{}
	if c.ImplementerRouting != nil {
		if bytes.Equal(bytes.TrimSpace(c.ImplementerRouting), []byte("null")) {
			return sel, fmt.Errorf("%w: implementerRouting must be a routing object, not null", errFactoryInput)
		}
		sel.implementer, sel.hasImplementer = c.ImplementerRouting, true
	}
	if c.ReviewerRouting != nil {
		if bytes.Equal(bytes.TrimSpace(c.ReviewerRouting), []byte("null")) {
			return sel, fmt.Errorf("%w: reviewerRouting must be a routing object, not null", errFactoryInput)
		}
		sel.reviewer, sel.hasReviewer = c.ReviewerRouting, true
	}
	return sel, nil
}

// factoryRouteApplied replaces the named routes in a configuration. A route
// the selection does not name is left byte for byte alone.
func factoryRouteApplied(conf FactoryConfig, sel factoryRouteSelection) FactoryConfig {
	if sel.hasImplementer {
		conf.ImplementerRouting = sel.implementer
	}
	if sel.hasReviewer {
		conf.ReviewerRouting = sel.reviewer
	}
	return conf
}

// factoryRouteCandidate returns the configuration a retry command is
// validated and applied against, and whether the command names a route at
// all.
//
// A command that carries a replacement is validated against the
// replacement's configuration; a command that does not is validated against
// the latest approval's, which is what a route-only retry inherits from.
func factoryRouteCandidate(item FactoryItem, cmd FactoryCommand) (FactoryConfig, bool, error) {
	sel, err := factoryRouteSelectionOf(cmd)
	if err != nil {
		return FactoryConfig{}, false, err
	}
	if !sel.hasImplementer && !sel.hasReviewer {
		return FactoryConfig{}, false, nil
	}
	base := FactoryConfig{}
	switch {
	case cmd.Replacement != nil:
		base = cmd.Replacement.Config
	case len(item.Approvals) > 0:
		base = item.Approvals[len(item.Approvals)-1].Config
	default:
		return FactoryConfig{}, false, fmt.Errorf("%w: no approved configuration to change", errFactoryConfig)
	}
	return factoryRouteApplied(base, sel), true, nil
}

// factoryRouteOnlyApproval returns the approval a route-only retry appends,
// and whether one is appended at all.
//
// The plan, the specification and the plan hash are carried over verbatim: a
// route change moves the approval revision, not the approved work. An
// approval is appended only when at least one route actually differs from
// the latest approval's, so a retry that re-states the routes in force
// behaves exactly as a plain retry.
func factoryRouteOnlyApproval(i FactoryItem, c FactoryCommand, now time.Time) (FactoryApproval, bool, error) {
	sel, err := factoryRouteSelectionOf(c)
	if err != nil {
		return FactoryApproval{}, false, err
	}
	if !sel.hasImplementer && !sel.hasReviewer {
		return FactoryApproval{}, false, nil
	}
	if len(i.Approvals) == 0 {
		return FactoryApproval{}, false, fmt.Errorf("%w: no approved configuration to change", errFactoryConfig)
	}
	last := i.Approvals[len(i.Approvals)-1]
	candidate := factoryRouteApplied(last.Config, sel)
	if bytes.Equal(candidate.ImplementerRouting, last.Config.ImplementerRouting) && bytes.Equal(candidate.ReviewerRouting, last.Config.ReviewerRouting) {
		return FactoryApproval{}, false, nil
	}
	return FactoryApproval{
		Revision:   uint64(len(i.Approvals) + 1),
		Plan:       last.Plan,
		Spec:       last.Spec,
		PlanHash:   last.PlanHash,
		Config:     candidate,
		ApprovedAt: now,
	}, true, nil
}

// factoryRouteChangeMessage names the old and the new profile of every role
// whose route changed.
func factoryRouteChangeMessage(before, after FactoryConfig) string {
	parts := []string{}
	for _, r := range []struct {
		role      string
		old, next json.RawMessage
	}{
		{"implementer", before.ImplementerRouting, after.ImplementerRouting},
		{"reviewer", before.ReviewerRouting, after.ReviewerRouting},
	} {
		if bytes.Equal(r.old, r.next) {
			continue
		}
		parts = append(parts, r.role+" route "+factoryRouteProfile(r.old)+" -> "+factoryRouteProfile(r.next))
	}
	return strings.Join(parts, "; ")
}

// factoryRouteProfile names the routing profile a route selects, or "-" when
// the route names none.
func factoryRouteProfile(route json.RawMessage) string {
	var selection struct {
		Profile string `json:"profile"`
	}
	if json.Unmarshal(route, &selection) != nil || selection.Profile == "" {
		return "-"
	}
	return selection.Profile
}
