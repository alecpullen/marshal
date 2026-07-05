package policy

import (
	"marshal/internal/app/config"
	"strings"
	"sync"
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
