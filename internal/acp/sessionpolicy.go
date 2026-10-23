package acp

import (
	"strings"

	"marshal/internal/app"
	"marshal/internal/interaction"
)

// PolicyParams is the session/new policy: a starting interaction tuple and
// command patterns that become session rules. A pattern ending in " *" such as
// "go test *" allows that command with any arguments (checked per shell stage
// by the policy engine); any other pattern allows exactly that command.
//
// Mode accepts the canonical ask/act pair and the retired five-mode spelling at
// this protocol boundary. Auto is optional: nil inherits the configured
// default, while an explicit false is preserved as a real patch value.
type PolicyParams struct {
	Mode  string   `json:"mode,omitempty"`
	Auto  *bool    `json:"auto,omitempty"`
	Allow []string `json:"allow,omitempty"`
}

// validate checks the policy before any runtime is started and returns the one
// canonical interaction patch it requests. Legacy modes are translated here;
// the runtime commits the canonical tuple.
func (pp *PolicyParams) validate() (interaction.Patch, error) {
	var patch interaction.Patch
	mode := strings.ToLower(strings.TrimSpace(pp.Mode))
	if mode != "" {
		m, derivedAuto, err := ingressMode(mode)
		if err != nil {
			return interaction.Patch{}, invalidParamsError("invalid policy.mode %q: want ask or act (legacy spellings accepted)", pp.Mode)
		}
		patch.Mode = &m
		if derivedAuto {
			on := true
			patch.Auto = &on
		}
	}
	// Explicit Auto overrides a legacy mode's derived value, including false.
	if pp.Auto != nil {
		on := *pp.Auto
		patch.Auto = &on
	}
	if patch.Mode != nil && *patch.Mode == interaction.ModeAsk && patch.Auto != nil && *patch.Auto {
		return interaction.Patch{}, invalidParamsError("policy.mode ask cannot enable auto")
	}
	for i, pat := range pp.Allow {
		if strings.TrimSpace(pat) == "" {
			return interaction.Patch{}, invalidParamsError("policy.allow[%d] must not be empty", i)
		}
	}
	return patch, nil
}

// applyPolicy commits the starting tuple through the runtime's interaction
// seam and adds each allow pattern as a session rule. The ACP transport makes
// no policy decision and never calls a runner setter directly.
func applyPolicy(rt *app.Runtime, pp *PolicyParams, patch interaction.Patch) error {
	if patch.Mode != nil || patch.Auto != nil || patch.System != nil {
		if rt == nil {
			return serverErrorf("policy cannot be applied: session has no runtime")
		}
		if _, err := rt.SetInteraction(patch); err != nil {
			return serverErrorf("apply session policy: %v", err)
		}
	}
	if rt == nil || rt.State == nil {
		return nil
	}
	for _, pat := range pp.Allow {
		rt.State.AddSessionRule(strings.TrimSpace(pat))
	}
	return nil
}

// sessionInteractionState reads the session's authoritative committed tuple
// for the session/new response. An absent state reports the zero tuple rather
// than inventing a granted one.
func sessionInteractionState(rt *app.Runtime) InteractionState {
	if rt == nil || rt.State == nil {
		return InteractionState{}
	}
	return interactionStateFrom(rt.State.InteractionState())
}
