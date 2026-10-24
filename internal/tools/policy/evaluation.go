package policy

import (
	"path/filepath"
	"strings"

	"marshal/internal/app/config"
	"marshal/internal/interaction"
	"marshal/internal/pathutil"
	"marshal/internal/permissions"
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
		return applyExecutionMode(in.ActorState, pe.evaluateNonShell(in, cfg)), nil
	}
	pe.mu.RLock()
	rules := pe.rules
	pe.mu.RUnlock()
	return applyExecutionMode(in.ActorState, pe.evaluateShellInput(in, cfg, sessionRules, rules)), nil
}

// Act authorizes the requested work. Only an explicit confirmation rule or a
// missing capability can interrupt it. Full Auto also preauthorizes explicit
// confirmations; neither mode overrides a denial or a child capability ceiling.
func applyExecutionMode(state interaction.State, ev Evaluation) Evaluation {
	if state.Mode != interaction.ModeAct || ev.Decision != DecisionConfirm {
		return ev
	}
	if ev.Required.System && !state.System {
		if state.Auto {
			ev.Decision = DecisionDeny
			ev.ExactCommandApproval = false
		}
		return ev
	}
	if !state.Auto {
		for _, reason := range ev.Reasons {
			if reason.Code == ReasonExplicitConfirm {
				return ev
			}
		}
	}
	ev.Decision = DecisionAllow
	ev.Required = CapabilityRequirement{}
	ev.ExactCommandApproval = false
	return ev
}

// evaluateNonShell classifies a non-shell tool from its registry risk and the
// configured permission rules. The rules are consulted before the risk
// fallback so an explicit ask/deny rule still prompts (or refuses) a tool the
// registry would otherwise allow, and an explicit allow rule can preauthorize
// a write the registry would otherwise confirm.
func (pe *PolicyEngine) evaluateNonShell(in Input, cfg config.Config) Evaluation {
	pe.mu.RLock()
	rules := pe.rules
	pe.mu.RUnlock()

	risk := pe.toolRisk(in.ToolName)

	// Session-state tools manage pure in-memory session state (the todo
	// list). They should not prompt the user even though they declare
	// RiskWorkspaceWrite, because they do not modify files on disk.
	if in.ToolName == "todo.write" {
		return Evaluation{Decision: DecisionAllow}
	}

	// Ask admits only positively read-only operations. A rule cannot widen the
	// capability: an Ask actor is denied a write tool even if a rule allows it.
	// This gate runs before every other branch, including MCP, so no explicit
	// classification can lift the Ask ceiling.
	if in.ActorState.Mode == interaction.ModeAsk && risk != "read_only" {
		return Evaluation{
			Decision: DecisionDeny,
			Required: CapabilityRequirement{Act: true},
			Reasons:  []PolicyReason{{Code: ReasonRequiresAct, Message: "this tool requires Act"}},
		}
	}

	// MCP tools are workspace-write unless explicitly classified: an
	// unconfigured MCP tool is a secure-default confirm, never a read. The
	// MCP policy and the F4 rules are both consulted here, so an explicit
	// allow/deny still wins over the secure default.
	if strings.HasPrefix(in.ToolName, "mcp.") {
		decision, reason, explicit := evaluateMCP(&cfg, rules, in.ToolName, in.Args)
		code := ReasonToolApproval
		if explicit {
			code = ReasonExplicitConfirm
		}
		if decision == DecisionDeny {
			code = ReasonExplicitDeny
		}
		return Evaluation{Decision: decision, Reasons: []PolicyReason{{Code: code, Message: reason}}}
	}

	// Explicit deny/confirm rules are collected before any allow so a later
	// allow cannot mask them.
	subjects := subjectsForTool(in.ToolName, in.Args, "")
	if len(subjects) > 0 {
		if decision, matched := evaluateSubjects(rules, permissions.PermissionForTool(in.ToolName), subjects); matched {
			code := ReasonExplicitConfirm
			if decision == DecisionDeny {
				code = ReasonExplicitDeny
			}
			return Evaluation{Decision: decision, Reasons: []PolicyReason{{Code: code, Message: "resolved by permission rule"}}}
		}
	}

	// A routine workspace write is Act's ordinary work: file edits inside the
	// workspace need no per-change approval, in Auto or not. Scope is what
	// makes a write routine — a target outside the workspace needs System
	// first, and one that cannot be placed fails closed onto the same
	// requirement rather than being assumed inside.
	if risk == "workspace_write" {
		if !in.ActorState.System && writesOutsideWorkspace(in) {
			return Evaluation{
				Decision: DecisionConfirm,
				Required: CapabilityRequirement{System: true},
				Reasons:  []PolicyReason{{Code: ReasonRequiresSystem, Message: "edits outside the workspace require System"}},
			}
		}
		return Evaluation{Decision: DecisionAllow}
	}

	switch risk {
	case "read_only":
		return Evaluation{Decision: DecisionAllow}
	case "network":
		return Evaluation{
			Decision: DecisionConfirm,
			Reasons:  []PolicyReason{{Code: ReasonToolApproval, Message: "network access"}},
		}
	default:
		// Preserve the tool's classification; applyExecutionMode supplies Act's
		// operation preauthorization after capability and deny checks.
		return Evaluation{
			Decision: DecisionConfirm,
			Required: CapabilityRequirement{Act: true},
			Reasons:  []PolicyReason{{Code: ReasonToolApproval, Message: "tool execution"}},
		}
	}
}

// writesOutsideWorkspace reports whether a non-shell write targets a path that
// cannot be proven inside the active workspace. A relative path is resolved by
// the tool against the workspace root, so it is inside by construction; an
// absolute one is resolved and compared with the root. An unplaceable target
// (no root, unresolvable path) counts as outside: routine workspace work must
// never be inferred from what cannot be located.
func writesOutsideWorkspace(in Input) bool {
	for _, subject := range subjectsForTool(in.ToolName, in.Args, "") {
		if !filepath.IsAbs(subject) {
			continue
		}
		if in.WorkspaceRoot == "" {
			return true
		}
		resolved, err := pathutil.ResolveAbsolute(subject)
		if err != nil {
			resolved = filepath.Clean(subject)
		}
		root, rerr := pathutil.ResolveAbsolute(in.WorkspaceRoot)
		if rerr != nil {
			root = filepath.Clean(in.WorkspaceRoot)
		}
		if !isWithinRoot(resolved, root) {
			return true
		}
	}
	return false
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
func (pe *PolicyEngine) evaluateShellInput(in Input, cfg config.Config, sessionRules []string, rules []permissions.Rule) Evaluation {
	// A command with statements but no analyzed operations is not a proven
	// read: it means the walker missed something. Fail closed instead of
	// allowing it. A command with nothing to run (empty, comments only) stays a
	// no-op.
	if len(in.Shell.Operations) == 0 && !in.Shell.NoStatements {
		if in.ActorState.Mode == interaction.ModeAsk {
			return Evaluation{
				Decision: DecisionDeny,
				Required: CapabilityRequirement{Act: true},
				Reasons:  []PolicyReason{{Code: ReasonRequiresAct, Message: "Ask admits only read-only operations"}},
			}
		}
		return Evaluation{
			Decision:             DecisionConfirm,
			ExactCommandApproval: true,
			Reasons:              []PolicyReason{{Code: ReasonUnknownPayload, Message: "the command could not be analyzed into operations"}},
		}
	}

	var effects ShellEffect
	var reasons []PolicyReason
	for _, op := range in.Shell.Operations {
		effects |= op.Effects
		reasons = append(reasons, op.Reasons...)
	}

	// Catastrophic and privilege operations are never allowed, whatever the
	// rules say.
	if effects&ShellCatastrophic != 0 {
		return Evaluation{Decision: DecisionDeny, Reasons: reasons}
	}
	if effects&ShellPrivilege != 0 {
		return Evaluation{Decision: DecisionDeny, Reasons: reasons}
	}

	// Ask admits only positively read-only operations. This ceiling runs
	// before every allow rule so no explicit classification can lift it.
	if in.ActorState.Mode == interaction.ModeAsk && effects&^ShellRead != 0 {
		return Evaluation{
			Decision: DecisionDeny,
			Required: CapabilityRequirement{Act: true},
			Reasons:  append(reasons, PolicyReason{Code: ReasonRequiresAct, Message: "Ask admits only read-only operations"}),
		}
	}

	// An explicit F4 deny refuses the operation outright and must do so before
	// any gate that could ask instead: a rule that refuses a destructive
	// command must not turn into an approval prompt. The allow/ask half of F4
	// is applied after the destructive gate, so a user allow rule can never
	// preauthorize destruction.
	subjects := subjectsForTool(in.ToolName, in.Args, normalizeCommand(in.Shell.Input.Command))
	var f4Decision Decision
	var f4Matched bool
	if len(subjects) > 0 {
		if decision, matched := evaluateSubjects(rules, permissions.PermissionForTool(in.ToolName), subjects); matched {
			if decision == DecisionDeny {
				return Evaluation{Decision: DecisionDeny, Reasons: append(reasons, PolicyReason{Code: ReasonExplicitDeny, Message: "resolved by permission rule"})}
			}
			f4Decision, f4Matched = decision, true
		}
	}

	// Explicit deny/confirm rules are collected before any allow so a later
	// allow cannot mask them.
	if reason, denied := matchShellDeny(cfg, in.Shell); denied {
		return Evaluation{Decision: DecisionDeny, Reasons: append(reasons, reason)}
	}
	confirmRule, hasConfirm := matchShellConfirm(cfg, in.Shell)
	if hasConfirm {
		reasons = append(reasons, confirmRule)
	}
	if f4Matched && f4Decision == DecisionConfirm {
		reasons = append(reasons, PolicyReason{Code: ReasonExplicitConfirm, Message: "resolved by permission rule"})
	}
	// Scope applies to the entire command, including mixed commands with a
	// publishing, destructive or configured-confirm operation. No earlier
	// classification may hide another operation's missing System capability.
	if !in.ActorState.System && (in.ToolName == "shell.sudo" || effects&ShellEnvironment != 0 || destructiveOutsideWorkspace(in.Shell)) {
		return Evaluation{
			Decision:             DecisionConfirm,
			Required:             CapabilityRequirement{System: true},
			Reasons:              append(reasons, PolicyReason{Code: ReasonRequiresSystem, Message: "operations outside the workspace require System"}),
			ExactCommandApproval: effects&ShellDestructive != 0,
		}
	}
	if match := configuredFloorMatch(cfg, in.Shell); match != "" {
		reasons = append(reasons, PolicyReason{Code: ReasonExplicitConfirm, Message: match + " requires approval (configured floor)"})
		return Evaluation{Decision: DecisionConfirm, ExactCommandApproval: true, Reasons: reasons}
	}
	if effects&ShellPublish != 0 {
		return Evaluation{Decision: DecisionConfirm, ExactCommandApproval: true, Reasons: reasons}
	}

	// Preserve exact-command scope when an explicit confirmation rule applies
	// to destruction. Act/Auto preauthorization is resolved by applyExecutionMode.
	if effects&ShellDestructive != 0 {
		return Evaluation{Decision: DecisionConfirm, ExactCommandApproval: true, Reasons: reasons}
	}
	if hasConfirm {
		return Evaluation{Decision: DecisionConfirm, Reasons: reasons}
	}

	// The allow/ask half of F4, now that the destructive gate has had its say.
	if f4Matched {
		return Evaluation{Decision: f4Decision, Reasons: append(reasons, PolicyReason{Code: ReasonExplicitConfirm, Message: "resolved by permission rule"})}
	}

	// Explicit session approvals are authored rules: they win over the
	// environment and uncertainty fallbacks, but never over the catastrophic,
	// privilege, publishing or destructive gates above.
	for _, rule := range sessionRules {
		if matchSessionRule(normalizeCommand(in.Shell.Input.Command), in.Shell.Input.Command, rule) {
			return Evaluation{Decision: DecisionAllow, Reasons: append(reasons, PolicyReason{Code: ReasonExplicitAllow, Message: "allowed by session-approved command: " + rule})}
		}
	}

	// Configured allow rules are authored rules too.
	if rule, allowed := matchShellAllow(cfg, in.Shell); allowed {
		return Evaluation{Decision: DecisionAllow, Reasons: append(reasons, PolicyReason{Code: ReasonExplicitAllow, Rule: rule, Message: "allowed by config allow rule: " + rule})}
	}

	// Environment effects require System.
	if effects&ShellEnvironment != 0 {
		return Evaluation{Decision: DecisionConfirm, Reasons: reasons}
	}

	// Unknown effects always retain exact approval.
	if effects&ShellUnknown != 0 {
		return Evaluation{Decision: DecisionConfirm, ExactCommandApproval: true, Reasons: reasons}
	}

	return Evaluation{Decision: DecisionAllow, Reasons: reasons}
}

// configuredFloorMatch reports the configured floor prefix an operation
// invokes, or "". Prefixes match token-wise, so `npm publish` does not floor
// `npm publishd`.
func configuredFloorMatch(cfg config.Config, a *ShellAnalysis) string {
	if a == nil {
		return ""
	}
	for _, raw := range cfg.Tools.Shell.FloorCommands {
		prefix := strings.Fields(raw)
		if len(prefix) == 0 {
			continue
		}
		for _, op := range a.Operations {
			argv := make([]string, 0, len(op.Argv))
			for _, w := range op.Argv {
				argv = append(argv, w.Value)
			}
			if len(argv) < len(prefix) {
				continue
			}
			matched := true
			for i, p := range prefix {
				if argv[i] != p {
					matched = false
					break
				}
			}
			if matched {
				return strings.Join(prefix, " ")
			}
		}
	}
	return ""
}

// destructiveOutsideWorkspace reports whether any destructive operation has a
// target that is not provably inside the workspace. An unresolved target
// counts as outside: the caller must not preauthorize what it cannot place.
func destructiveOutsideWorkspace(a *ShellAnalysis) bool {
	if a == nil {
		return false
	}
	for _, op := range a.Operations {
		if op.Effects&ShellDestructive == 0 {
			continue
		}
		for _, t := range op.Targets {
			if t.Scope != ShellScopeWorkspace && t.Scope != ShellScopeReadRoot {
				return true
			}
		}
	}
	return false
}

// matchShellDeny reports whether any operation matches a configured deny rule.
func matchShellDeny(cfg config.Config, a *ShellAnalysis) (PolicyReason, bool) {
	for _, pattern := range cfg.Tools.Shell.Deny.Patterns {
		for _, op := range a.Operations {
			if shellDenyMatches(pattern, &op) {
				return PolicyReason{Code: ReasonExplicitDeny, OperationID: op.ID, Rule: pattern, Message: "denied by rule"}, true
			}
		}
	}
	return PolicyReason{}, false
}

// matchShellConfirm reports whether the command matches a confirm rule.
func matchShellConfirm(cfg config.Config, a *ShellAnalysis) (PolicyReason, bool) {
	for _, pattern := range cfg.Tools.Shell.Confirm.Commands {
		if shellRuleMatches(pattern, a) {
			return PolicyReason{Code: ReasonExplicitConfirm, Rule: pattern, Message: "confirmed by rule"}, true
		}
	}
	return PolicyReason{}, false
}

// matchShellAllow reports whether the command matches a configured allow
// rule, returning the matched rule.
func matchShellAllow(cfg config.Config, a *ShellAnalysis) (string, bool) {
	for _, pattern := range cfg.Tools.Shell.Allow.Commands {
		if shellRuleMatches(pattern, a) {
			return pattern, true
		}
	}
	return "", false
}

// joinedArgv returns an operation's executable and arguments as one string.
func joinedArgv(op *ShellOperation) string {
	joined := op.Executable.Value
	for _, a := range op.Argv[1:] {
		joined += " " + a.Value
	}
	return joined
}

// shellDenyMatches matches a deny pattern against an operation. A regex or
// glob keeps its explicit semantics; a plain pattern is a substring match, so
// `*destructive*` and `kill -9 *` behave as before.
func shellDenyMatches(pattern string, op *ShellOperation) bool {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return false
	}
	joined := joinedArgv(op)
	if regexMatch(pattern, joined) {
		return true
	}
	pattern = normalizeCommand(pattern)
	joined = normalizeCommand(joined)
	if !strings.Contains(pattern, "*") {
		return strings.Contains(joined, pattern)
	}
	return globMatch(pattern, joined)
}

// shellRuleMatches matches an allow/confirm rule against the whole command. A
// regex or glob keeps its explicit semantics; a plain pattern must equal the
// whole normalized command, so `go test` does not match `go test ./...` and a
// suffix cannot inherit an approval.
func shellRuleMatches(pattern string, a *ShellAnalysis) bool {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" || a == nil {
		return false
	}
	command := normalizeCommand(a.Input.Command)
	if regexMatch(pattern, command) {
		return true
	}
	if strings.ContainsAny(pattern, "*?[]") {
		return globMatch(pattern, command)
	}
	return command == normalizeCommand(pattern)
}
