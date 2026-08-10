package policy

import (
	"fmt"
	"log/slog"
	"marshal/internal/app/config"
	"marshal/internal/permissions"
	"marshal/internal/tools/patch"
	"marshal/internal/tools/registry"
	"regexp"
	"strings"
	"sync"

	"mvdan.cc/sh/v3/syntax"
)

type Decision string

const (
	DecisionAllow   Decision = "allow"
	DecisionConfirm Decision = "confirm"
	DecisionDeny    Decision = "deny"
)

// ApprovalMode is the active interaction/approval mode. It bundles
// turn-classification and approval-gating into one concept. The zero
// value is ModeEdit (confirm-each), preserving pre-modes behavior for
// engines constructed without calling SetApprovalMode.
type ApprovalMode string

const (
	ModePlan    ApprovalMode = "plan"
	ModeDefault ApprovalMode = "default"
	ModeEdit    ApprovalMode = "edit"
	ModeCopilot ApprovalMode = "copilot"
	ModeAuto    ApprovalMode = "auto"
)

// guardrailPatterns are conservative hard-coded command patterns that are
// always blocked regardless of user allow rules.
// Note: chmod -r and chown -r were removed from this list in favor of
// argv-aware AST checks below that also catch -R and --recursive.
// See hasRecursiveFlag and the chmod/chown check in analyzeCommand.
var guardrailPatterns = []string{
	"sudo", "git reset --hard", "git clean -fd",
	"mkfs", "shutdown", "reboot",
}

// legacyRMGuardrail is checked by the legacy fallback because the AST path
// handles rm with argv-aware stageIsRMDestructive / ClassifyCommand and must
// not false-positive on "echo rm -rf /tmp".
var legacyRMGuardrail = []string{"rm -rf", "rm -fr", "rm -r -f"}

// reasonEnvironment prefixes the Confirm reason for environment-mutating
// commands. applyModeTransform matches on it (the same reason-matching
// pattern isGuardrailDeny uses) so the Confirm survives auto-approve modes.
const reasonEnvironment = "mutates state outside the working directory"

// isEnvironmentConfirm reports whether a Confirm reason came from the
// environment-mutation classification, which auto-approve modes must not
// downgrade.
func isEnvironmentConfirm(reason string) bool {
	return strings.HasPrefix(reason, reasonEnvironment)
}

type PolicyEngine struct {
	config       *config.Config
	sessionRules []string
	rules        []permissions.Rule
	mu           sync.RWMutex
	logger       *slog.Logger
	approvalMode ApprovalMode
	registry     *registry.Registry
}

func NewEngine(cfg *config.Config, sessionRules []string) *PolicyEngine {
	var rules []permissions.Rule
	rules = append(rules, permissions.SafeCommands...)
	if cfg != nil {
		for _, r := range cfg.Permissions.Rules {
			action := permissions.Action(r.Action)
			if action != permissions.ActionAllow && action != permissions.ActionAsk && action != permissions.ActionDeny {
				continue
			}
			rules = append(rules, permissions.Rule{
				Permission: r.Permission,
				Pattern:    r.Pattern,
				Action:     action,
			})
		}
	}
	return &PolicyEngine{
		config:       cfg,
		sessionRules: sessionRules,
		rules:        rules,
		logger:       slog.Default(),
		approvalMode: ModeEdit,
	}
}

// SetSessionRules replaces the engine's in-memory session allow-list.
// PolicyEngine is normally constructed once per app run and lives for the
// duration of the process; SetSessionRules is safe for concurrent use with
// Evaluate.
func (pe *PolicyEngine) SetSessionRules(rules []string) {
	pe.mu.Lock()
	defer pe.mu.Unlock()
	pe.sessionRules = rules
}

// SetRules replaces the engine's F4 permission rules. Safe for concurrent use.
func (pe *PolicyEngine) SetRules(rules []permissions.Rule) {
	pe.mu.Lock()
	defer pe.mu.Unlock()
	pe.rules = rules
}

// SetLogger injects a structured logger used for debug-level events
// (e.g. guardrail parse failures). Pass nil to revert to slog.Default().
//
// SetLogger is intended to be called once at construction time. The engine
// is safe for concurrent Evaluate calls but not concurrent SetLogger calls.
func (pe *PolicyEngine) SetLogger(l *slog.Logger) {
	pe.mu.Lock()
	defer pe.mu.Unlock()
	if l == nil {
		pe.logger = slog.Default()
		return
	}
	pe.logger = l
}

// SetApprovalMode replaces the active approval mode. Safe for concurrent
// use with Evaluate (mirrors SetSessionRules / SetRules).
func (pe *PolicyEngine) SetApprovalMode(m ApprovalMode) {
	pe.mu.Lock()
	defer pe.mu.Unlock()
	pe.approvalMode = m
}

// ApprovalMode returns the active approval mode. Safe for concurrent use.
func (pe *PolicyEngine) ApprovalMode() ApprovalMode {
	pe.mu.RLock()
	defer pe.mu.RUnlock()
	return pe.approvalMode
}

// Clone returns an independent copy of the engine: same config, rules,
// session rules, registry, logger, and approval mode, but with its own
// lock, so later mutations (SetApprovalMode, SetRules, …) on either engine
// do not leak into the other. Used to give unattended subagent runners an
// auto-approving engine without changing the interactive session's mode.
func (pe *PolicyEngine) Clone() *PolicyEngine {
	pe.mu.RLock()
	defer pe.mu.RUnlock()
	rules := make([]permissions.Rule, len(pe.rules))
	copy(rules, pe.rules)
	sessionRules := make([]string, len(pe.sessionRules))
	copy(sessionRules, pe.sessionRules)
	return &PolicyEngine{
		config:       pe.config,
		sessionRules: sessionRules,
		rules:        rules,
		logger:       pe.logger,
		approvalMode: pe.approvalMode,
		registry:     pe.registry,
	}
}

// WithRegistry sets the tool registry the policy engine consults to look
// up a tool's registered Risk level. When nil (the default), the engine
// falls back to the legacy "low-risk read tool" allow for any non-shell
// tool (preserves the pre-registry behavior for tests and legacy callers).
func (pe *PolicyEngine) WithRegistry(r *registry.Registry) {
	pe.mu.Lock()
	defer pe.mu.Unlock()
	pe.registry = r
}

// Logger returns the logger used by the engine. May be the package
// default if SetLogger was never called. Safe for concurrent use.
func (pe *PolicyEngine) Logger() *slog.Logger {
	pe.mu.RLock()
	defer pe.mu.RUnlock()
	if pe.logger == nil {
		return slog.Default()
	}
	return pe.logger
}

func (pe *PolicyEngine) Evaluate(toolName string, args map[string]interface{}) (Decision, string, error) {
	// Snapshot the mutable fields under the lock: SetRules / WithRegistry /
	// SetSessionRules / SetApprovalMode run from the UI goroutine and must
	// not race a mid-Evaluate read.
	pe.mu.RLock()
	rules := pe.rules
	reg := pe.registry
	sessionRules := pe.sessionRules
	mode := pe.approvalMode
	pe.mu.RUnlock()

	// Evaluate must work with a nil engine config (tests, legacy callers):
	// treat it as a zero config, which lands on the secure-confirm fallback.
	cfg := pe.config
	if cfg == nil {
		cfg = &config.Config{}
	}

	// git push floor: non-bypassable in every mode. Runs before the mode
	// transform so a floor Confirm is returned directly, never downgraded.
	if toolName == "shell.run" || toolName == "test.run" {
		if cmdRaw, ok := args["command"]; ok {
			if cmd, ok := cmdRaw.(string); ok && isGitPushFloor(cmd) {
				return DecisionConfirm, "git push requires approval (non-bypassable floor)", nil
			}
		}
	}

	var decision Decision
	var reason string
	if strings.HasPrefix(toolName, "mcp.") {
		decision, reason = evaluateMCP(cfg, rules, toolName, args)
	} else {
		decision, reason = pe.evaluateShell(cfg, rules, reg, sessionRules, toolName, args)
	}

	// Mode transform: the final step, applied after guardrails, the floor,
	// F4 rules, and risk fallbacks have computed a decision.
	decision, reason = applyModeTransform(mode, toolName, args, decision, reason, reg)
	return decision, reason, nil
}

// applyModeTransform rewrites the computed decision based on the active
// approval mode. It runs as the final step in Evaluate, after guardrails,
// the git-push floor, F4 rules, and risk fallbacks.
//
//   - plan / default: write-capable tools and shell writes are denied
//     (directing the agent to mode.request). Read-only tools pass.
//   - edit: no transform (today's confirm-each behavior).
//   - copilot / auto: a computed Confirm is downgraded to Allow
//     (auto-approve), EXCEPT the floor and guardrails already returned
//     early in Evaluate, so any Confirm reaching here is auto-approvable.
func applyModeTransform(mode ApprovalMode, toolName string, args map[string]interface{}, decision Decision, reason string, reg *registry.Registry) (Decision, string) {
	switch mode {
	case ModePlan, ModeDefault, "":
		if decision == DecisionAllow {
			// Read-only tools and explicitly-allowed read commands pass.
			return decision, reason
		}
		// Everything else (Confirm, Deny that isn't a guardrail) is denied
		// with a mode.request directive. Guardrail Denys are preserved.
		if decision == DecisionDeny && isGuardrailDeny(reason) {
			return decision, reason
		}
		return DecisionDeny, fmt.Sprintf("denied: in %s mode, cannot modify files; call mode.request to switch to an editing mode", mode)
	case ModeEdit:
		return decision, reason
	case ModeCopilot, ModeAuto:
		if decision == DecisionConfirm && !isEnvironmentConfirm(reason) {
			return DecisionAllow, fmt.Sprintf("auto-approved in %s mode", mode)
		}
		return decision, reason
	default:
		// Unknown mode: safest default is deny writes.
		if decision == DecisionAllow {
			return decision, reason
		}
		if decision == DecisionDeny && isGuardrailDeny(reason) {
			return decision, reason
		}
		return DecisionDeny, fmt.Sprintf("denied: unknown mode %q, cannot modify files; switch to edit/copilot/auto", mode)
	}
}

// isGuardrailDeny reports whether a Deny reason originated from a
// conservative guardrail (not from the mode transform). Guardrail denies
// must be preserved even in plan/default modes so destructive commands
// are never silently allowed.
func isGuardrailDeny(reason string) bool {
	return strings.Contains(reason, "guardrail") || strings.Contains(reason, "blocked by conservative")
}

// evaluateMCP evaluates an mcp.* tool against the configured MCP policies
// and the F4 permission rules. Precedence: exact-match deny > pattern deny
// (two-pass, deny-first) > F4 rule > exact/pattern allow-or-confirm >
// secure-default confirm.
func evaluateMCP(cfg *config.Config, rules []permissions.Rule, toolName string, args map[string]interface{}) (Decision, string) {
	var mcpMatched bool
	var mcpDecision Decision
	var mcpReason string

	if cfg.MCP.Policies != nil {
		// 1. Exact match (highest priority) — deny returns immediately.
		if policyStr, ok := cfg.MCP.Policies[toolName]; ok {
			switch Decision(policyStr) {
			case DecisionDeny:
				return DecisionDeny, "blocked by MCP policy config exact match"
			case DecisionAllow:
				mcpDecision = DecisionAllow
				mcpReason = "allowed by MCP policy config exact match"
				mcpMatched = true
			case DecisionConfirm:
				mcpDecision = DecisionConfirm
				mcpReason = "requires approval by MCP policy config exact match"
				mcpMatched = true
			}
		}

		// 2. Pattern match (prefix, wildcard, regex), most restrictive
		// first — deny returns immediately.
		if !mcpMatched {
			for _, want := range []Decision{DecisionDeny, DecisionConfirm, DecisionAllow} {
				for pattern, policyStr := range cfg.MCP.Policies {
					if Decision(policyStr) != want || !matchMCPPolicy(pattern, toolName) {
						continue
					}
					switch want {
					case DecisionDeny:
						return DecisionDeny, "blocked by MCP policy match: " + pattern
					case DecisionConfirm:
						mcpDecision = DecisionConfirm
						mcpReason = "requires approval by MCP policy match: " + pattern
					case DecisionAllow:
						mcpDecision = DecisionAllow
						mcpReason = "allowed by MCP policy match: " + pattern
					}
					mcpMatched = true
					break
				}
				if mcpMatched {
					break
				}
			}
		}
	}

	// 3. F4 rules — can override allow/confirm from MCP policies.
	subjects := subjectsForTool(toolName, args, "")
	if len(subjects) > 0 {
		decision, matched := evaluateSubjects(rules, permissions.PermissionForTool(toolName), subjects)
		if matched {
			return decision, "resolved by permission rule"
		}
	}

	// 4. Fall back to the MCP policy decision, or the secure default.
	if mcpMatched {
		return mcpDecision, mcpReason
	}
	return DecisionConfirm, "requires approval (unconfigured MCP tool secure default)"
}

// evaluateShell evaluates a non-MCP tool: shell.run/test.run commands run
// the guardrail analysis and then the shell rule lists; every other tool
// resolves through F4 rules, the network-tool confirm, and the registry
// risk fallback.
func (pe *PolicyEngine) evaluateShell(cfg *config.Config, rules []permissions.Rule, reg *registry.Registry, sessionRules []string, toolName string, args map[string]interface{}) (Decision, string) {
	var normCmd string
	if toolName == "shell.run" || toolName == "test.run" {
		var cmd string
		cmdRaw, ok := args["command"]
		if !ok {
			if toolName == "test.run" {
				cmd = cfg.Commands.Test
			} else {
				return DecisionConfirm, "missing command arg"
			}
		} else {
			var typeOk bool
			cmd, typeOk = cmdRaw.(string)
			if !typeOk {
				return DecisionConfirm, "invalid command arg type"
			}
		}

		normCmd = normalizeCommand(cmd)
		if normCmd == "" {
			return DecisionConfirm, "empty command"
		}

		// 1. Conservative safety guardrails (AST-based; legacy fallback on
		// parse error).
		dynSetting := "deny"
		if cfg.Tools.Shell.GuardrailDynamicArgv0 != "" {
			dynSetting = cfg.Tools.Shell.GuardrailDynamicArgv0
		}
		dec, reason := pe.EvaluateGuardrails(normCmd, dynSetting)
		if dec != "" {
			return dec, reason
		}
	}

	// 2. F4 pattern rules (last-matching-rule-wins). Applied AFTER intrinsic
	// guardrails so a structural deny is never overridden by an allow rule.
	// An allow rule here can downgrade an ask to allow; a deny rule forces deny.
	subjects := subjectsForTool(toolName, args, normCmd)
	if len(subjects) > 0 {
		decision, matched := evaluateSubjects(rules, permissions.PermissionForTool(toolName), subjects)
		if matched {
			return decision, "resolved by permission rule"
		}
	}

	// 3. Network tools always require explicit approval by default regardless
	// of any later auto-allow path. MUST stay above the generic low-risk
	// fallback below; TestPolicyEngine_Evaluate_WebToolsAlwaysConfirm pins
	// behavior. Users can opt into specific URLs/commands by writing an F4
	// rule with matching subject (subjectsForTool returns {"web.fetch"} /
	// {"web.search"}).
	if toolName == "web.fetch" || toolName == "web.search" {
		return DecisionConfirm, "network access requires approval"
	}

	// Session-state tools manage pure in-memory session state (todo list,
	// scratchpad). They should not prompt the user even though they declare
	// RiskWorkspaceWrite, because they do not modify files on disk.
	switch toolName {
	case "todo.write", "scratchpad.write", "scratchpad.delete":
		return DecisionAllow, "session-state tool"
	}

	// 4. Non-shell tools resolve via the registered risk level.
	if toolName != "shell.run" && toolName != "test.run" {
		if reg != nil {
			if tool, ok := reg.Lookup(toolName); ok {
				switch tool.Risk {
				case registry.RiskReadOnly:
					return DecisionAllow, "read-only tool"
				case registry.RiskWorkspaceWrite, registry.RiskCommand,
					registry.RiskNetwork, registry.RiskDestructive:
					return DecisionConfirm,
						fmt.Sprintf("%s tool requires approval", tool.Risk)
				}
				// Unknown risk: fall through to the existing
				// "low-risk read tool" allow (preserves current behavior
				// for tools that didn't declare Risk).
			}
		}
		return DecisionAllow, "low-risk read tool"
	}

	// 5. Shell commands fall through to the configured rule lists.
	return evaluateShellRules(cfg, sessionRules, normCmd)
}

// evaluateShellRules applies, in order: config deny rules, session-approved
// prefixes, config allow rules, config confirm rules, then the auto-approve
// or secure-confirm fallback.
func evaluateShellRules(cfg *config.Config, sessionRules []string, normCmd string) (Decision, string) {
	for _, pattern := range cfg.Tools.Shell.Deny.Patterns {
		if matchPattern(pattern, normCmd) {
			return DecisionDeny, "blocked by user deny rule: " + pattern
		}
	}
	for _, prefix := range sessionRules {
		if matchRule(normCmd, prefix) {
			return DecisionAllow, "allowed by session-approved command: " + prefix
		}
	}
	for _, prefix := range cfg.Tools.Shell.Allow.Commands {
		if matchRule(normCmd, prefix) {
			return DecisionAllow, "allowed by config allow rule: " + prefix
		}
	}
	for _, prefix := range cfg.Tools.Shell.Confirm.Commands {
		if matchRule(normCmd, prefix) {
			return DecisionConfirm, "requires confirmation by config confirm rule: " + prefix
		}
	}
	// Environment-mutating commands (cache wipes, global config, system
	// package managers) always require explicit approval, even in
	// auto-approve modes. Runs after explicit user allow/confirm rules so
	// users can opt into specific environment commands, and before the
	// auto-approve fallback so auto modes don't silently approve it.
	if cls, err := ClassifyCommand(normCmd); err == nil && cls.Risk == registry.RiskEnvironment {
		return DecisionConfirm, reasonEnvironment + ": " + cls.Reason
	}
	if cfg.Tools.Shell.AutoApprove {
		return DecisionAllow, "allowed by auto-approve fallback"
	}
	return DecisionConfirm, "requires approval (default secure configuration)"
}

func isBlockedByGuardrailLegacy(cmd string) bool {
	cmd = strings.ToLower(cmd)
	for _, b := range guardrailPatterns {
		if strings.Contains(cmd, b) {
			return true
		}
	}
	for _, b := range legacyRMGuardrail {
		if strings.Contains(cmd, b) {
			return true
		}
	}

	// Network installer check (curl/wget piped to sh/bash/zsh)
	if (strings.Contains(cmd, "curl") || strings.Contains(cmd, "wget")) && strings.Contains(cmd, "|") {
		parts := strings.Split(cmd, "|")
		for i := 1; i < len(parts); i++ {
			subCmd := strings.TrimSpace(parts[i])
			words := strings.Fields(subCmd)
			if len(words) > 0 {
				firstWord := words[0]
				for _, shell := range []string{"sh", "bash", "zsh"} {
					if firstWord == shell || strings.HasSuffix(firstWord, "/"+shell) {
						return true
					}
				}
			}
		}
	}
	return false
}

// stage is one pipeline stage: argv0 word + its arguments + the full printed
// stage text.
type stage struct {
	argv0    string
	fullText string
	args     []string // individual argument tokens (excluding argv0)
	dynamic  bool
}

// parseStages parses cmd with mvdan.cc/sh and returns one stage per
// *syntax.CallExpr, in Walk order. Returns an error if the command is not
// valid shell — the caller MUST fall back to isBlockedByGuardrailLegacy.
func parseStages(cmd string) ([]stage, error) {
	f, err := syntax.NewParser().Parse(strings.NewReader(cmd), "")
	if err != nil {
		return nil, err
	}
	var stages []stage
	syntax.Walk(f, func(node syntax.Node) bool {
		call, ok := node.(*syntax.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		var b strings.Builder
		syntax.NewPrinter().Print(&b, call.Args[0])
		var full strings.Builder
		var args []string
		for i, w := range call.Args {
			if i > 0 {
				full.WriteString(" ")
				var argBuf strings.Builder
				syntax.NewPrinter().Print(&argBuf, w)
				args = append(args, argBuf.String())
			}
			syntax.NewPrinter().Print(&full, w)
		}
		dyn := false
		syntax.Walk(call.Args[0], func(nn syntax.Node) bool {
			switch nn.(type) {
			case *syntax.CmdSubst, *syntax.ParamExp, *syntax.ArithmExp:
				dyn = true
				return false
			}
			return true
		})
		stages = append(stages, stage{argv0: b.String(), fullText: full.String(), args: args, dynamic: dyn})
		return true
	})
	return stages, nil
}

// guardrailVerdict is the result of the AST-based guardrail analysis.
type guardrailVerdict struct {
	blocked      bool
	reason       string
	dynamicArgv0 bool
}

// analyzeCommand parses cmd and classifies it against the hardcoded guardrail
// set. On parse error it returns a non-nil error; the caller falls back to
// isBlockedByGuardrailLegacy.
func analyzeCommand(cmd string) (guardrailVerdict, error) {
	stages, err := parseStages(cmd)
	if err != nil {
		return guardrailVerdict{}, err
	}
	if len(stages) == 0 {
		return guardrailVerdict{}, nil
	}

	// Stage-level rm guardrail: catches rm with recursive+force after common
	// wrappers (env, nice) and as an xargs payload. ClassifyCommand only
	// inspects the top-level argv0 of the whole command string, so these
	// variants would otherwise evade the guardrail.
	for _, st := range stages {
		if stageIsRMDestructive(st) {
			return guardrailVerdict{
				blocked: true,
				reason:  "blocked by conservative guardrail: rm -r -f",
			}, nil
		}
	}

	// Use argv-aware classification (shlex-based) for destructive patterns
	// that the substring guardrailPatterns would miss (e.g. rm -fr, rm -r -f).
	// This is additive: ClassifyCommand catches destructive patterns, then the
	// stage loop below catches non-destructive substring patterns (sudo, mkfs).
	cls, clsErr := ClassifyCommand(cmd)
	if clsErr == nil && cls.Risk == registry.RiskDestructive {
		return guardrailVerdict{
			blocked: true,
			reason:  "blocked by conservative guardrail: " + cls.Reason,
		}, nil
	}

	shellNames := map[string]bool{"sh": true, "bash": true, "zsh": true}
	hasFetch := false
	hasShell := false

	for _, st := range stages {
		if st.dynamic {
			return guardrailVerdict{dynamicArgv0: true, reason: "dynamic command name unclassifiable"}, nil
		}
		ft := strings.ToLower(st.fullText)
		for _, p := range guardrailPatterns {
			if strings.Contains(ft, p) {
				return guardrailVerdict{blocked: true, reason: "blocked by conservative guardrail: " + p}, nil
			}
		}
		name := strings.ToLower(lastSegment(st.argv0))
		// argv-aware check for chmod/chown with recursive flags.
		// Catches -r, -R (via lowercasing), and --recursive which the
		// substring guardrailPatterns would miss. chmod -r and chown -r
		// have been removed from guardrailPatterns above.
		if name == "chmod" || name == "chown" {
			if hasRecursiveFlag(st) {
				return guardrailVerdict{
					blocked: true,
					reason:  "blocked by conservative guardrail: " + name + " --recursive",
				}, nil
			}
		}
		if name == "curl" || name == "wget" {
			hasFetch = true
		}
		if shellNames[name] {
			hasShell = true
		}
	}
	if hasFetch && hasShell {
		return guardrailVerdict{blocked: true, reason: "blocked by conservative guardrail: network installer (curl/wget to shell)"}, nil
	}
	return guardrailVerdict{}, nil
}

// hasRecursiveFlag checks whether the stage's arguments include a recursive
// flag (-r, -R, or --recursive) for chmod/chown. This is used instead of
// the substring guardrail patterns (which miss --recursive).
func hasRecursiveFlag(st stage) bool {
	for _, arg := range st.args {
		a := strings.ToLower(arg)
		if a == "-r" || a == "--recursive" {
			return true
		}
	}
	return false
}

// EvaluateGuardrails runs the AST-based guardrail analysis and returns the
// resulting Decision + reason. Returns Decision("") (empty) to signal
// "not blocked — continue to rule matching".
func (pe *PolicyEngine) EvaluateGuardrails(cmd, dynSetting string) (Decision, string) {
	verdict, err := analyzeCommand(cmd)
	if err != nil {
		pe.Logger().Debug("policy guardrail parse failed, falling back to legacy", "cmd", cmd, "err", err)
		if isBlockedByGuardrailLegacy(cmd) {
			return DecisionDeny, "blocked by conservative guardrail safety checks (legacy)"
		}
		return "", ""
	}
	if verdict.blocked {
		return DecisionDeny, verdict.reason
	}
	if verdict.dynamicArgv0 {
		switch dynSetting {
		case "off":
			return "", ""
		case "confirm":
			return DecisionConfirm, "requires approval: " + verdict.reason
		default:
			return DecisionDeny, verdict.reason
		}
	}
	return "", ""
}

// GuardrailCheck returns an error if the policy engine would deny the
// given command based on its conservative guardrails. The error wraps
// the deny reason. shell.run / test.run call this as a final pre-flight
// check before handing the command to the sandbox.
func (pe *PolicyEngine) GuardrailCheck(command string) error {
	dec, reason := pe.EvaluateGuardrails(command, "deny")
	if dec == DecisionDeny {
		return fmt.Errorf("command blocked by conservative guardrail: %s", reason)
	}
	return nil
}

func normalizeCommand(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	words := strings.Fields(s)
	return strings.Join(words, " ")
}

// isGitPushFloor reports whether cmd is a `git push` invocation (any
// variant: --force, -f, --tags, --force-with-lease, with a remote, etc.).
// It is the non-bypassable floor: no mode downgrades a git-push Confirm.
// It uses the AST parser to distinguish `git push` from `git pushd` and
// to handle pipes/subshells; on parse failure it falls back to a trimmed
// prefix check.
func isGitPushFloor(cmd string) bool {
	stages, err := parseStages(cmd)
	if err == nil {
		for _, s := range stages {
			if stageIsGitPush(s) {
				return true
			}
		}
		return false
	}
	// Legacy fallback: trimmed, lowercased, words-based. Only recognizes a
	// plain `git push` (no path prefix or wrapper) when parsing fails.
	trimmed := strings.TrimSpace(strings.ToLower(cmd))
	words := strings.Fields(trimmed)
	if len(words) >= 2 && words[0] == "git" && words[1] == "push" {
		return true
	}
	return false
}

// stageIsGitPush reports whether a parsed pipeline stage is a git push
// invocation. It handles path-prefixed git binaries, git global options such
// as `-c key=val`, and common prefix wrappers like `env` and `nice`.
func stageIsGitPush(s stage) bool {
	argv := skipCommonWrappers(append([]string{s.argv0}, s.args...))
	if len(argv) == 0 || lastSegment(argv[0]) != "git" {
		return false
	}
	i := 1
	for i < len(argv) {
		a := argv[i]
		// Git global options that take a value.
		if a == "-c" || a == "--config" || a == "--exec-path" || a == "--git-dir" ||
			a == "--work-tree" || a == "--namespace" || a == "-C" {
			i += 2
			continue
		}
		// Other options (flag or long-flag) are skipped; the first non-option
		// token must be the git subcommand.
		if strings.HasPrefix(a, "-") && len(a) > 1 {
			i++
			continue
		}
		break
	}
	return i < len(argv) && argv[i] == "push"
}

// skipCommonWrappers strips leading wrapper commands such as env and nice
// (and their simplest flags/assignments) so that the real command argv is
// exposed for guardrail and floor checks.
func skipCommonWrappers(argv []string) []string {
	i := 0
	for i < len(argv) {
		name := lastSegment(argv[i])
		switch name {
		case "env":
			i++
			for i < len(argv) {
				a := argv[i]
				// Environment assignments: FOO=bar
				if strings.Contains(a, "=") && !strings.HasPrefix(a, "-") {
					i++
					continue
				}
				if a == "-i" || a == "--ignore-environment" {
					i++
					continue
				}
				if a == "-u" || a == "--unset" {
					i += 2
					continue
				}
				if strings.HasPrefix(a, "--unset=") {
					i++
					continue
				}
				break
			}
		case "nice":
			i++
			if i < len(argv) && (argv[i] == "-n" || argv[i] == "--adjustment") {
				i += 2
			} else if i < len(argv) && strings.HasPrefix(argv[i], "--adjustment=") {
				i++
			}
		default:
			return argv[i:]
		}
	}
	return argv[i:]
}

// stageIsRMDestructive reports whether a pipeline stage runs rm with both
// recursive and force flags. It inspects the effective command after common
// wrappers and also detects rm as an xargs payload.
func stageIsRMDestructive(s stage) bool {
	argv := skipCommonWrappers(append([]string{s.argv0}, s.args...))
	if len(argv) > 0 && lastSegment(argv[0]) == "rm" {
		if hasFlagInArgs(argv[1:], "r", "R", "recursive") && hasFlagInArgs(argv[1:], "f", "force") {
			return true
		}
	}

	// xargs payload: xargs [options] rm -fr ...
	if len(argv) > 0 && lastSegment(argv[0]) == "xargs" {
		i := 1
		for i < len(argv) {
			a := argv[i]
			if a == "-I" || a == "-i" || a == "-L" || a == "-n" || a == "-P" || a == "-s" {
				i += 2
				continue
			}
			if strings.HasPrefix(a, "-") {
				i++
				continue
			}
			break
		}
		if i < len(argv) && lastSegment(argv[i]) == "rm" {
			if hasFlagInArgs(argv[i+1:], "r", "R", "recursive") && hasFlagInArgs(argv[i+1:], "f", "force") {
				return true
			}
		}
	}

	return false
}

func matchRule(command, prefix string) bool {
	if regexMatch(prefix, command) {
		return true
	}
	command = normalizeCommand(command)
	prefix = normalizeCommand(prefix)
	return command == prefix
}

func regexMatch(pattern, subject string) bool {
	if strings.HasPrefix(pattern, "/") && strings.HasSuffix(pattern, "/") && len(pattern) > 2 {
		reStr := pattern[1 : len(pattern)-1]
		re, err := regexp.Compile(reStr)
		if err == nil {
			return re.MatchString(subject)
		}
	}
	return false
}

func matchPattern(pattern, command string) bool {
	if regexMatch(pattern, command) {
		return true
	}
	command = normalizeCommand(command)
	pattern = normalizeCommand(pattern)
	if !strings.Contains(pattern, "*") {
		return strings.Contains(command, pattern)
	}
	return globMatch(pattern, command)
}

func matchMCPPolicy(pattern, toolName string) bool {
	if regexMatch(pattern, toolName) {
		return true
	}
	if strings.Contains(pattern, "*") {
		return globMatch(pattern, toolName)
	}
	if strings.HasPrefix(toolName, pattern+".") || toolName == pattern {
		return true
	}
	return false
}

func globMatch(pattern, subject string) bool {
	parts := strings.Split(pattern, "*")
	if parts[0] != "" && !strings.HasPrefix(subject, parts[0]) {
		return false
	}
	if parts[len(parts)-1] != "" && !strings.HasSuffix(subject, parts[len(parts)-1]) {
		return false
	}
	idx := 0
	for _, part := range parts {
		if part == "" {
			continue
		}
		found := strings.Index(subject[idx:], part)
		if found == -1 {
			return false
		}
		idx += found + len(part)
	}
	return true
}

func subjectsForTool(toolName string, args map[string]interface{}, normCmd string) []string {
	switch toolName {
	case "shell.run", "test.run":
		return []string{normCmd}
	case "file.write_patch":
		if patchArg, ok := args["patch"]; ok {
			if patchStr, ok := patchArg.(string); ok {
				patches, err := patch.Parse(patchStr)
				if err == nil {
					var paths []string
					for _, p := range patches {
						paths = append(paths, p.Path)
					}
					return paths
				}
			}
		}
		return nil
	case "file.write":
		if path, ok := args["path"].(string); ok && path != "" {
			return []string{path}
		}
		return nil
	default:
		return []string{toolName}
	}
}

func evaluateSubjects(rules []permissions.Rule, permissionName string, subjects []string) (Decision, bool) {
	var matched bool
	var result Decision

	for _, subject := range subjects {
		action, found := permissions.Evaluate(rules, permissionName, subject)
		if !found {
			continue
		}
		matched = true
		// Most restrictive wins: deny > confirm/ask > allow
		if action == permissions.ActionDeny {
			return DecisionDeny, true
		}
		if action == permissions.ActionAsk && result != DecisionDeny {
			result = DecisionConfirm
		} else if action == permissions.ActionAllow && result == "" {
			result = DecisionAllow
		}
	}
	if !matched {
		return "", false
	}
	if result == "" {
		result = DecisionConfirm
	}
	return result, true
}

// ParseApprovalMode converts a config string to an ApprovalMode. Unknown or
// empty values fall back to ModeDefault — the safe, read-only mode.
//
// This lives in policy rather than in the app or TUI packages because both
// need it and neither can import the other: app wires the policy engine from
// config, and the TUI has to seed its own mode label from the same value or
// the status line contradicts the engine.
func ParseApprovalMode(s string) ApprovalMode {
	switch strings.ToLower(s) {
	case "plan":
		return ModePlan
	case "default":
		return ModeDefault
	case "edit":
		return ModeEdit
	case "copilot":
		return ModeCopilot
	case "auto":
		return ModeAuto
	}
	return ModeDefault
}
