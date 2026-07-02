package policy

import (
	"strings"
	"marshal/internal/app/config"
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
}

func NewEngine(cfg *config.Config, sessionRules []string) *PolicyEngine {
	return &PolicyEngine{
		config:       cfg,
		sessionRules: sessionRules,
	}
}

func (pe *PolicyEngine) Evaluate(toolName string, args map[string]interface{}) (Decision, string, error) {
	if toolName != "shell.run" && toolName != "test.run" {
		return DecisionAllow, "low-risk read tool", nil
	}

	cmdRaw, ok := args["command"]
	if !ok {
		return DecisionConfirm, "missing command arg", nil
	}
	cmd, ok := cmdRaw.(string)
	if !ok {
		return DecisionConfirm, "invalid command arg type", nil
	}

	normCmd := normalizeCommand(cmd)
	if normCmd == "" {
		return DecisionConfirm, "empty command", nil
	}

	// 1. Conservative Safety Guardrails
	if isBlockedByGuardrail(normCmd) {
		return DecisionDeny, "blocked by conservative guardrail safety checks", nil
	}

	// 2. Config Deny Rules
	for _, pattern := range pe.config.Tools.Shell.Deny.Patterns {
		if matchPattern(pattern, normCmd) {
			return DecisionDeny, "blocked by user deny rule: " + pattern, nil
		}
	}

	// 3. Session Rules
	for _, prefix := range pe.sessionRules {
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

func isBlockedByGuardrail(cmd string) bool {
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
		for _, shell := range []string{"sh", "bash", "zsh"} {
			if matchPattern("*|*"+shell, cmd) || matchPattern("*|* "+shell, cmd) {
				return true
			}
		}
	}
	return false
}

func normalizeCommand(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	words := strings.Fields(s)
	return strings.Join(words, " ")
}

func matchRule(command, prefix string) bool {
	command = normalizeCommand(command)
	prefix = normalizeCommand(prefix)
	if command == prefix {
		return true
	}
	return strings.HasPrefix(command, prefix+" ")
}

func matchPattern(pattern, command string) bool {
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
