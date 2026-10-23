package policy

import (
	"fmt"
	"log/slog"
	"marshal/internal/app/config"
	"marshal/internal/permissions"
	"marshal/internal/strutil"
	"marshal/internal/tools/patch"
	"marshal/internal/tools/registry"
	"path/filepath"
	"regexp"
	"sort"
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
	rules        []permissions.Rule
	mu           sync.RWMutex
	logger       *slog.Logger
	registry     *registry.Registry
	configSource ConfigSource
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

// Clone returns an independent copy of the engine: same config, rules,
// session rules, registry and logger, but with its own lock, so later
// mutations (SetRules, …) on either engine do not leak into the other.
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
		registry:     pe.registry,
		configSource: pe.configSource,
	}
}

// SetConfigSource installs the live configuration source. Every engine and
// clone reads the same source, so a reload is visible without rebuilding the
// engine.
func (pe *PolicyEngine) SetConfigSource(src ConfigSource) {
	pe.mu.Lock()
	defer pe.mu.Unlock()
	pe.configSource = src
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
			// Sort patterns alphabetically for deterministic ordering
			// when multiple same-severity patterns match.
			patterns := make([]string, 0, len(cfg.MCP.Policies))
			for pattern := range cfg.MCP.Policies {
				patterns = append(patterns, pattern)
			}
			sort.Strings(patterns)
			for _, want := range []Decision{DecisionDeny, DecisionConfirm, DecisionAllow} {
				for _, pattern := range patterns {
					policyStr := cfg.MCP.Policies[pattern]
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

// unescapeLit removes the backslash escapes the shell would strip from a
// literal word. mvdan's parser keeps the raw source in Lit.Value, so `\sudo`
// arrives as `\sudo` even though the shell runs `sudo`. Only argv0 is
// unescaped: operand classification deliberately treats a backslash as
// non-literal and fails closed, so unescaping operands would loosen it.
func unescapeLit(s string) string {
	if !strings.Contains(s, "\\") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
			if s[i] == '\n' {
				continue
			}
			b.WriteByte(s[i])
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// checkCommandControls rejects a command holding a character a terminal or
// a bidi-aware renderer acts on instead of showing (strutil.IsDisplayControl).
// A shell never needs the raw byte: $'\e' and printf '\033' produce it at
// run time, and they display as what they are.
func checkCommandControls(cmd string) error {
	if r, bad := strutil.FirstDisplayControl(cmd); bad {
		return fmt.Errorf("the command contains control character U+%04X, which the approval prompt cannot show as written; "+
			"spell it with an escape the shell expands, such as $'\\e' or printf '\\033'", r)
	}
	return nil
}

func normalizeCommand(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	words := strings.Fields(s)
	return strings.Join(words, " ")
}

// matchRule reports whether command matches a plain rule. An explicit regex
// (wrapped in slashes) keeps its semantics; otherwise the whole normalized
// command must equal the normalized rule, so a prefix never inherits a match
// and `git push` does not cover `git push --force`.
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

// hasAbsoluteSubject reports whether any path subject of the tool is an
// absolute path. Used to disclose system-mode scope in Confirm reasons.
func hasAbsoluteSubject(toolName string, args map[string]interface{}, normCmd string) bool {
	for _, s := range subjectsForTool(toolName, args, normCmd) {
		if filepath.IsAbs(s) {
			return true
		}
	}
	return false
}

func subjectsForTool(toolName string, args map[string]interface{}, normCmd string) []string {
	switch toolName {
	case "shell.run", "test.run":
		return []string{normCmd}
	case "file.write_patch", "file.edit":
		patches, ok := patch.FromToolArgs(toolName, args)
		if !ok {
			return nil
		}
		paths := make([]string, 0, len(patches))
		for _, p := range patches {
			paths = append(paths, p.Path)
		}
		return paths
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
