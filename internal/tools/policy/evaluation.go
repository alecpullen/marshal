package policy

import (
	"strings"

	"marshal/internal/app/config"
	"marshal/internal/interaction"
)

// snapshot returns the live config, session rules and revision. A configured
// source wins; otherwise the engine's own fields are used.
func (pe *PolicyEngine) snapshot() (config.Config, []string, uint64) {
	pe.mu.RLock()
	src := pe.configSource
	cfg := pe.config
	rules := pe.sessionRules
	pe.mu.RUnlock()
	if src != nil {
		return src()
	}
	if cfg != nil {
		return *cfg, rules, 0
	}
	return config.Config{}, rules, 0
}

// EvaluateInput evaluates one tool invocation against the live policy. The
// actor's revision must match the configuration source revision; a mismatch
// is a stale evaluation, never an old config under a new grant.
func (pe *PolicyEngine) EvaluateInput(in Input) (Evaluation, error) {
	cfg, sessionRules, rev := pe.snapshot()
	if in.ActorState.Revision != 0 && rev != 0 && in.ActorState.Revision != rev {
		return Evaluation{}, ErrStalePolicy
	}
	if in.Shell == nil {
		return pe.evaluateNonShell(in), nil
	}
	return pe.evaluateShellInput(in, cfg, sessionRules), nil
}

// evaluateNonShell classifies a non-shell tool from its registry risk.
func (pe *PolicyEngine) evaluateNonShell(in Input) Evaluation {
	risk := pe.toolRisk(in.ToolName)
	switch risk {
	case "read_only":
		return Evaluation{Decision: DecisionAllow}
	case "network":
		if in.ActorState.Mode == interaction.ModeAsk {
			return Evaluation{
				Decision: DecisionDeny,
				Required: CapabilityRequirement{Act: true},
				Reasons:  []PolicyReason{{Code: ReasonRequiresAct, Message: "network access requires Act"}},
			}
		}
		return Evaluation{Decision: DecisionConfirm, Required: CapabilityRequirement{Act: true}}
	default:
		if in.ActorState.Mode == interaction.ModeAsk {
			return Evaluation{
				Decision: DecisionDeny,
				Required: CapabilityRequirement{Act: true},
				Reasons:  []PolicyReason{{Code: ReasonRequiresAct, Message: "this tool requires Act"}},
			}
		}
		return Evaluation{Decision: DecisionConfirm, Required: CapabilityRequirement{Act: true}}
	}
}

// toolRisk returns the registered risk for a tool, defaulting to
// workspace_write so an unclassified tool is never treated as a read.
func (pe *PolicyEngine) toolRisk(name string) string {
	pe.mu.RLock()
	reg := pe.registry
	pe.mu.RUnlock()
	if reg == nil {
		return "read_only"
	}
	if t, ok := reg.Lookup(name); ok {
		return string(t.Risk)
	}
	return "workspace_write"
}

// evaluateShell applies the decision matrix to a parsed command.
func (pe *PolicyEngine) evaluateShellInput(in Input, cfg config.Config, sessionRules []string) Evaluation {
	var effects ShellEffect
	var reasons []PolicyReason
	for _, op := range in.Shell.Operations {
		effects |= op.Effects
		reasons = append(reasons, op.Reasons...)
	}

	// Explicit deny/confirm rules are collected before any allow so a later
	// allow cannot mask them.
	if reason, denied := matchShellDeny(cfg, in.Shell); denied {
		return Evaluation{Decision: DecisionDeny, Reasons: append(reasons, reason)}
	}
	confirmRule, hasConfirm := matchShellConfirm(cfg, in.Shell)

	// Ask admits only positively read-only operations.
	if in.ActorState.Mode == interaction.ModeAsk && effects&^ShellRead != 0 {
		return Evaluation{
			Decision: DecisionDeny,
			Required: CapabilityRequirement{Act: true},
			Reasons:  append(reasons, PolicyReason{Code: ReasonRequiresAct, Message: "Ask admits only read-only operations"}),
		}
	}

	// Catastrophic and privilege operations are never allowed.
	if effects&ShellCatastrophic != 0 {
		return Evaluation{Decision: DecisionDeny, Reasons: reasons}
	}
	if effects&ShellPrivilege != 0 {
		return Evaluation{Decision: DecisionDeny, Reasons: reasons}
	}

	// Publishing always retains exact approval.
	if effects&ShellPublish != 0 {
		return Evaluation{Decision: DecisionConfirm, ExactCommandApproval: true, Reasons: reasons}
	}

	// Destructive workspace operations are preauthorized only by Auto and
	// only when fully known.
	if effects&ShellDestructive != 0 {
		if in.ActorState.Auto && effects&(ShellEnvironment|ShellUnknown) == 0 {
			return Evaluation{Decision: DecisionAllow, Reasons: reasons}
		}
		return Evaluation{Decision: DecisionConfirm, ExactCommandApproval: true, Reasons: reasons}
	}

	// Environment effects require System.
	if effects&ShellEnvironment != 0 {
		if !in.ActorState.System {
			return Evaluation{
				Decision: DecisionConfirm,
				Required: CapabilityRequirement{System: true},
				Reasons:  append(reasons, PolicyReason{Code: ReasonRequiresSystem, Message: "environment changes require System"}),
			}
		}
		return Evaluation{Decision: DecisionConfirm, Reasons: reasons}
	}

	// Unknown effects always retain exact approval.
	if effects&ShellUnknown != 0 {
		return Evaluation{Decision: DecisionConfirm, ExactCommandApproval: true, Reasons: reasons}
	}

	if hasConfirm {
		return Evaluation{Decision: DecisionConfirm, Reasons: append(reasons, confirmRule)}
	}
	return Evaluation{Decision: DecisionAllow, Reasons: reasons}
}

// matchShellDeny reports whether any operation matches a configured deny rule.
func matchShellDeny(cfg config.Config, a *ShellAnalysis) (PolicyReason, bool) {
	for _, pattern := range cfg.Tools.Shell.Deny.Patterns {
		for _, op := range a.Operations {
			if shellPatternMatches(pattern, &op) {
				return PolicyReason{Code: ReasonExplicitDeny, OperationID: op.ID, Rule: pattern, Message: "denied by rule"}, true
			}
		}
	}
	return PolicyReason{}, false
}

// matchShellConfirm reports whether any operation matches a confirm rule.
func matchShellConfirm(cfg config.Config, a *ShellAnalysis) (PolicyReason, bool) {
	for _, pattern := range cfg.Tools.Shell.Confirm.Commands {
		for _, op := range a.Operations {
			if shellPatternMatches(pattern, &op) {
				return PolicyReason{Code: ReasonExplicitConfirm, OperationID: op.ID, Rule: pattern, Message: "confirmed by rule"}, true
			}
		}
	}
	return PolicyReason{}, false
}

// shellPatternMatches matches a rule pattern against an operation's executable
// and argv. A plain prefix matches the executable; a glob/regex pattern keeps
// its explicit semantics.
func shellPatternMatches(pattern string, op *ShellOperation) bool {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return false
	}
	exe := op.Executable.Value
	if !strings.ContainsAny(pattern, "*?[]") {
		return exe == pattern || strings.HasPrefix(exe, pattern+" ")
	}
	// Glob: match against the executable and the joined argv.
	joined := exe
	for _, a := range op.Argv[1:] {
		joined += " " + a.Value
	}
	if globMatch(pattern, exe) {
		return true
	}
	return globMatch(pattern, joined)
}
