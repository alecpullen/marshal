package policy

import (
	"log/slog"
	"marshal/internal/app/config"
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

type PolicyEngine struct {
	config       *config.Config
	sessionRules []string
	mu           sync.RWMutex
}

func NewEngine(cfg *config.Config, sessionRules []string) *PolicyEngine {
	return &PolicyEngine{
		config:       cfg,
		sessionRules: sessionRules,
	}
}

// SetSessionRules replaces the engine's in-memory session allow-list.
// PolicyEngine is normally constructed once per app run and lives for the
// whole session, but session rules (added via the TUI's "Always Allow"
// action) accrue after construction — callers with a long-lived engine
// must call this before Evaluate to see rules added since the last call.
func (pe *PolicyEngine) SetSessionRules(rules []string) {
	pe.mu.Lock()
	defer pe.mu.Unlock()
	pe.sessionRules = rules
}

func (pe *PolicyEngine) Evaluate(toolName string, args map[string]interface{}) (Decision, string, error) {
	if strings.HasPrefix(toolName, "mcp.") {
		if pe.config != nil && pe.config.MCP.Policies != nil {
			// 1. Exact Match (highest priority)
			if policyStr, ok := pe.config.MCP.Policies[toolName]; ok {
				switch Decision(policyStr) {
				case DecisionAllow:
					return DecisionAllow, "allowed by MCP policy config exact match", nil
				case DecisionConfirm:
					return DecisionConfirm, "requires approval by MCP policy config exact match", nil
				case DecisionDeny:
					return DecisionDeny, "blocked by MCP policy config exact match", nil
				}
			}

			// 2. Pattern Match (prefix, wildcard, regex)
			for pattern, policyStr := range pe.config.MCP.Policies {
				if matchMCPPolicy(pattern, toolName) {
					switch Decision(policyStr) {
					case DecisionAllow:
						return DecisionAllow, "allowed by MCP policy match: " + pattern, nil
					case DecisionConfirm:
						return DecisionConfirm, "requires approval by MCP policy match: " + pattern, nil
					case DecisionDeny:
						return DecisionDeny, "blocked by MCP policy match: " + pattern, nil
					}
				}
			}
		}
		// Default confirm fallback for write-like MCP tools
		return DecisionConfirm, "requires approval (unconfigured MCP tool secure default)", nil
	}

	// Network tools always require explicit approval regardless of any
	// later auto-allow path. MUST stay above the generic low-risk fallback
	// below; TestPolicyEngine_Evaluate_WebToolsAlwaysConfirm pins behavior.
	if toolName == "web.fetch" || toolName == "web.search" {
		return DecisionConfirm, "network access requires approval", nil
	}

	if toolName != "shell.run" && toolName != "test.run" {
		return DecisionAllow, "low-risk read tool", nil
	}

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

	normCmd := normalizeCommand(cmd)
	if normCmd == "" {
		return DecisionConfirm, "empty command", nil
	}

	// 1. Conservative Safety Guardrails (AST-based; legacy fallback on parse error)
	dynSetting := "deny"
	if pe.config != nil && pe.config.Tools.Shell.GuardrailDynamicArgv0 != "" {
		dynSetting = pe.config.Tools.Shell.GuardrailDynamicArgv0
	}
	dec, reason := evaluateGuardrails(normCmd, dynSetting)
	if dec != "" {
		return dec, reason, nil
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
	blocked := []string{
		"sudo",
		"rm -rf",
		"git reset --hard",
		"git clean -fd",
		"mkfs",
		"shutdown",
		"reboot",
		"chmod -r",
		"chown -r",
	}
	for _, b := range blocked {
		if matchPattern(b, cmd) {
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

// stage is one pipeline stage: argv0 word + the full printed stage text.
type stage struct {
	argv0    string
	fullText string
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
		for i, w := range call.Args {
			if i > 0 {
				full.WriteString(" ")
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
		stages = append(stages, stage{argv0: b.String(), fullText: full.String(), dynamic: dyn})
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

	blockedPatterns := []string{
		"sudo", "rm -rf", "git reset --hard", "git clean -fd",
		"mkfs", "shutdown", "reboot", "chmod -r", "chown -r",
	}

	shellNames := map[string]bool{"sh": true, "bash": true, "zsh": true}
	hasFetch := false
	hasShell := false

	for _, st := range stages {
		if st.dynamic {
			return guardrailVerdict{dynamicArgv0: true, reason: "dynamic command name unclassifiable"}, nil
		}
		ft := strings.ToLower(st.fullText)
		for _, p := range blockedPatterns {
			if strings.Contains(ft, p) {
				return guardrailVerdict{blocked: true, reason: "blocked by conservative guardrail: " + p}, nil
			}
		}
		name := basenameLower(st.argv0)
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

// evaluateGuardrails runs the AST-based guardrail analysis and returns the
// resulting Decision + reason. Returns Decision("") (empty) to signal
// "not blocked — continue to rule matching".
func evaluateGuardrails(cmd, dynSetting string) (Decision, string) {
	verdict, err := analyzeCommand(cmd)
	if err != nil {
		slog.Default().Debug("policy guardrail parse failed, falling back to legacy", "cmd", cmd, "err", err)
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

func normalizeCommand(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	words := strings.Fields(s)
	return strings.Join(words, " ")
}

func matchRule(command, prefix string) bool {
	if strings.HasPrefix(prefix, "/") && strings.HasSuffix(prefix, "/") && len(prefix) > 2 {
		reStr := prefix[1 : len(prefix)-1]
		re, err := regexp.Compile(reStr)
		if err == nil {
			return re.MatchString(command)
		}
	}
	command = normalizeCommand(command)
	prefix = normalizeCommand(prefix)
	if command == prefix {
		return true
	}
	return strings.HasPrefix(command, prefix+" ")
}

func matchPattern(pattern, command string) bool {
	if strings.HasPrefix(pattern, "/") && strings.HasSuffix(pattern, "/") && len(pattern) > 2 {
		reStr := pattern[1 : len(pattern)-1]
		re, err := regexp.Compile(reStr)
		if err == nil {
			return re.MatchString(command)
		}
	}
	command = normalizeCommand(command)
	pattern = normalizeCommand(pattern)
	if !strings.Contains(pattern, "*") {
		return strings.Contains(command, pattern)
	}

	parts := strings.Split(pattern, "*")
	if parts[0] != "" && !strings.HasPrefix(command, parts[0]) {
		return false
	}
	if parts[len(parts)-1] != "" && !strings.HasSuffix(command, parts[len(parts)-1]) {
		return false
	}

	idx := 0
	for _, part := range parts {
		if part == "" {
			continue
		}
		found := strings.Index(command[idx:], part)
		if found == -1 {
			return false
		}
		idx += found + len(part)
	}
	return true
}

func matchMCPPolicy(pattern, toolName string) bool {
	if strings.HasPrefix(pattern, "/") && strings.HasSuffix(pattern, "/") && len(pattern) > 2 {
		reStr := pattern[1 : len(pattern)-1]
		re, err := regexp.Compile(reStr)
		if err == nil {
			return re.MatchString(toolName)
		}
	}

	if strings.Contains(pattern, "*") {
		parts := strings.Split(pattern, "*")
		if parts[0] != "" && !strings.HasPrefix(toolName, parts[0]) {
			return false
		}
		if parts[len(parts)-1] != "" && !strings.HasSuffix(toolName, parts[len(parts)-1]) {
			return false
		}

		idx := 0
		for _, part := range parts {
			if part == "" {
				continue
			}
			found := strings.Index(toolName[idx:], part)
			if found == -1 {
				return false
			}
			idx += found + len(part)
		}
		return true
	}

	if strings.HasPrefix(toolName, pattern+".") || toolName == pattern {
		return true
	}

	return false
}
