package policy

import (
	"fmt"
	"log/slog"
	"marshal/internal/app/config"
	"marshal/internal/permissions"
	"marshal/internal/tools/patch"
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

// guardrailPatterns are conservative hard-coded command patterns that are
// always blocked regardless of user allow rules.
// Note: chmod -r and chown -r were removed from this list in favor of
// argv-aware AST checks below that also catch -R and --recursive.
// See hasRecursiveFlag and the chmod/chown check in analyzeCommand.
var guardrailPatterns = []string{
	"sudo", "rm -rf", "git reset --hard", "git clean -fd",
	"mkfs", "shutdown", "reboot",
}

// destructivePatterns is the subset of guardrailPatterns that are considered
// genuinely destructive. Matches from these patterns trigger the
// AllowDestructive config flag check.
var destructivePatterns = map[string]bool{
	"rm -rf":           true,
	"git reset --hard": true,
	"git clean -fd":    true,
	"mkfs":             true,
	"shutdown":         true,
	"reboot":           true,
}

type PolicyEngine struct {
	config       *config.Config
	sessionRules []string
	rules        []permissions.Rule
	mu           sync.RWMutex
	logger       *slog.Logger
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
	if strings.HasPrefix(toolName, "mcp.") {
		var mcpMatched bool
		var mcpDecision Decision
		var mcpReason string

		if pe.config != nil && pe.config.MCP.Policies != nil {
			// 1. Exact Match (highest priority) — deny returns immediately
			if policyStr, ok := pe.config.MCP.Policies[toolName]; ok {
				switch Decision(policyStr) {
				case DecisionDeny:
					return DecisionDeny, "blocked by MCP policy config exact match", nil
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

			// 2. Pattern Match (prefix, wildcard, regex) — deny returns immediately
			if !mcpMatched {
				for pattern, policyStr := range pe.config.MCP.Policies {
					if matchMCPPolicy(pattern, toolName) {
						switch Decision(policyStr) {
						case DecisionDeny:
							return DecisionDeny, "blocked by MCP policy match: " + pattern, nil
						case DecisionAllow:
							mcpDecision = DecisionAllow
							mcpReason = "allowed by MCP policy match: " + pattern
							mcpMatched = true
						case DecisionConfirm:
							mcpDecision = DecisionConfirm
							mcpReason = "requires approval by MCP policy match: " + pattern
							mcpMatched = true
						}
						break
					}
				}
			}
		}

		// 4. F4 rules — can override allow/confirm from MCP policies
		subjects := subjectsForTool(toolName, args, "")
		if len(subjects) > 0 {
			decision, matched := evaluateSubjects(pe.rules, permissions.PermissionForTool(toolName), subjects)
			if matched {
				return decision, "resolved by permission rule", nil
			}
		}

		// Fallback to MCP policy decision, or secure default confirm
		if mcpMatched {
			return mcpDecision, mcpReason, nil
		}
		return DecisionConfirm, "requires approval (unconfigured MCP tool secure default)", nil
	}

	var normCmd string
	if toolName == "shell.run" || toolName == "test.run" {
		var cmd string
		cmdRaw, ok := args["command"]
		if !ok {
			if toolName == "test.run" {
				cmd = pe.config.Commands.Test
			} else {
				return DecisionConfirm, "missing command arg", nil
			}
		} else {
			var typeOk bool
			cmd, typeOk = cmdRaw.(string)
			if !typeOk {
				return DecisionConfirm, "invalid command arg type", nil
			}
		}

		normCmd = normalizeCommand(cmd)
		if normCmd == "" {
			return DecisionConfirm, "empty command", nil
		}

		// 1. Conservative Safety Guardrails (AST-based; legacy fallback on parse error)
		dynSetting := "deny"
		if pe.config != nil && pe.config.Tools.Shell.GuardrailDynamicArgv0 != "" {
			dynSetting = pe.config.Tools.Shell.GuardrailDynamicArgv0
		}
		dec, reason := pe.EvaluateGuardrails(normCmd, dynSetting)
		if dec != "" {
			return dec, reason, nil
		}
	}

	// 1b. F4 pattern rules (last-matching-rule-wins). Applied AFTER intrinsic
	// guardrails so a structural deny is never overridden by an allow rule.
	// An allow rule here can downgrade an ask to allow; a deny rule forces deny.
	subjects := subjectsForTool(toolName, args, normCmd)
	if len(subjects) > 0 {
		decision, matched := evaluateSubjects(pe.rules, permissions.PermissionForTool(toolName), subjects)
		if matched {
			return decision, "resolved by permission rule", nil
		}
	}

	// Network tools always require explicit approval by default regardless of
	// any later auto-allow path. MUST stay above the generic low-risk fallback
	// below; TestPolicyEngine_Evaluate_WebToolsAlwaysConfirm pins behavior.
	// Users can opt into specific URLs/commands by writing an F4 rule with
	// matching subject (subjectsForTool returns {"web.fetch"} / {"web.search"}).
	if toolName == "web.fetch" || toolName == "web.search" {
		return DecisionConfirm, "network access requires approval", nil
	}

	if toolName != "shell.run" && toolName != "test.run" {
		return DecisionAllow, "low-risk read tool", nil
	}

	// 2. Config Deny Rules
	for _, pattern := range pe.config.Tools.Shell.Deny.Patterns {
		if matchPattern(pattern, normCmd) {
			return DecisionDeny, "blocked by user deny rule: " + pattern, nil
		}
	}

	// 3. Session Rules
	pe.mu.RLock()
	sessionRules := pe.sessionRules
	pe.mu.RUnlock()
	for _, prefix := range sessionRules {
		if matchRule(normCmd, prefix) {
			return DecisionAllow, "allowed by session-approved command: " + prefix, nil
		}
	}

	// 4. Config Allow Rules
	for _, prefix := range pe.config.Tools.Shell.Allow.Commands {
		if matchRule(normCmd, prefix) {
			return DecisionAllow, "allowed by config allow rule: " + prefix, nil
		}
	}

	// 5. Config Confirm Rules
	for _, prefix := range pe.config.Tools.Shell.Confirm.Commands {
		if matchRule(normCmd, prefix) {
			return DecisionConfirm, "requires confirmation by config confirm rule: " + prefix, nil
		}
	}

	// 6. Default Fallback
	if pe.config.Tools.Shell.AutoApprove {
		return DecisionAllow, "allowed by auto-approve fallback", nil
	}

	return DecisionConfirm, "requires approval (default secure configuration)", nil
}

func isBlockedByGuardrailLegacy(cmd string) bool {
	cmd = strings.ToLower(cmd)
	for _, b := range guardrailPatterns {
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
	destructive  bool   // true when the matching pattern is in destructivePatterns
	pattern      string // the matched guardrail pattern, if any
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
				destructive := destructivePatterns[p]
				return guardrailVerdict{blocked: true, reason: "blocked by conservative guardrail: " + p, destructive: destructive, pattern: p}, nil
			}
		}
		name := basenameLower(st.argv0)
		// argv-aware check for chmod/chown with recursive flags.
		// Catches -r, -R (via lowercasing), and --recursive which the
		// substring guardrailPatterns would miss. chmod -r and chown -r
		// have been removed from guardrailPatterns above.
		if name == "chmod" || name == "chown" {
			if hasRecursiveFlag(st) {
				return guardrailVerdict{
					blocked:     true,
					reason:      "blocked by conservative guardrail: " + name + " --recursive",
					destructive: true,
					pattern:     name + " -r",
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

// basenameLower returns the lowercased last path component of argv0.
func basenameLower(argv0 string) string {
	name := argv0
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	return strings.ToLower(name)
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
//
// allowSudo and allowDestructive are read from pe.config.Tools.Shell. When
// true they change the reason text to "(flagged allowed)" so the audit log
// records that the user opted in, but the decision remains DecisionDeny (the
// TUI is still expected to confirm destructive/sudo commands).
func (pe *PolicyEngine) EvaluateGuardrails(cmd, dynSetting string) (Decision, string) {
	allowSudo := pe.config != nil && pe.config.Tools.Shell.AllowSudo
	allowDestructive := pe.config != nil && pe.config.Tools.Shell.AllowDestructive
	verdict, err := analyzeCommand(cmd)
	if err != nil {
		pe.logger.Debug("policy guardrail parse failed, falling back to legacy", "cmd", cmd, "err", err)
		if isBlockedByGuardrailLegacy(cmd) {
			return DecisionDeny, "blocked by conservative guardrail safety checks (legacy)"
		}
		return "", ""
	}
	if verdict.blocked {
		reason := verdict.reason
		if verdict.destructive && allowDestructive {
			reason += " (flagged allowed)"
		} else if verdict.pattern == "sudo" && allowSudo {
			reason += " (flagged allowed)"
		}
		return DecisionDeny, reason
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

func matchRule(command, prefix string) bool {
	if regexMatch(prefix, command) {
		return true
	}
	command = normalizeCommand(command)
	prefix = normalizeCommand(prefix)
	if command == prefix {
		return true
	}
	return strings.HasPrefix(command, prefix+" ")
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
