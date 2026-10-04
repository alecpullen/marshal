package acp

import (
	"strings"

	"marshal/internal/app"
	"marshal/internal/tools/policy"
)

// PolicyParams is the session/new policy: an approval mode and command
// patterns that become session rules. A pattern ending in " *" such as
// "go test *" allows that command with any arguments (checked per shell
// stage by the policy engine); any other pattern allows exactly that
// command.
type PolicyParams struct {
	Mode  string   `json:"mode,omitempty"`
	Allow []string `json:"allow,omitempty"`
}

// validate checks the policy before any runtime is started and returns
// the lowercase-normalized mode.
func (pp *PolicyParams) validate() (string, error) {
	mode := strings.ToLower(strings.TrimSpace(pp.Mode))
	if mode != "" && !policy.ValidApprovalMode(mode) {
		return "", invalidParamsError("invalid policy.mode %q: want one of plan, default, edit, copilot, auto", pp.Mode)
	}
	for i, pat := range pp.Allow {
		if strings.TrimSpace(pat) == "" {
			return "", invalidParamsError("policy.allow[%d] must not be empty", i)
		}
	}
	return mode, nil
}

// applyPolicy sets the approval mode and adds each allow pattern as a
// session rule. A requested mode that cannot be applied (the session has
// no runner) is an error, so a policy meant to constrain the session is
// never silently dropped.
func applyPolicy(rt *app.Runtime, pp *PolicyParams, mode string) error {
	if mode != "" {
		if rt.Runner == nil {
			return serverErrorf("policy.mode %q cannot be applied: session has no agent runner", mode)
		}
		rt.Runner.SetApprovalMode(policy.ParseApprovalMode(mode))
	}
	if rt.State == nil {
		return nil
	}
	for _, pat := range pp.Allow {
		rt.State.AddSessionRule(strings.TrimSpace(pat))
	}
	return nil
}
