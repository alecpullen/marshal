package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"marshal/internal/agent/agenttest"
	"marshal/internal/app/config"
	"marshal/internal/llm/schema"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

func TestSanitizeToolNameReplacesDotsWithUnderscore(t *testing.T) {
	cases := map[string]string{
		"file.read":               "file_read",
		"file.write_patch":        "file_write_patch",
		"shell.run":               "shell_run",
		"mcp.github.create_issue": "mcp_github_create_issue",
		"ask_user":                "ask_user",
	}
	for in, want := range cases {
		if got := sanitizeToolName(in); got != want {
			t.Errorf("sanitizeToolName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSanitizeToolNameAlwaysStartsWithLetter(t *testing.T) {
	if got := sanitizeToolName(".hidden"); len(got) == 0 || !(got[0] >= 'a' && got[0] <= 'z' || got[0] >= 'A' && got[0] <= 'Z') {
		t.Fatalf("sanitizeToolName(%q) = %q, does not start with a letter", ".hidden", got)
	}
}

// Regression guard: the actual current native-tool naming convention (every
// name is a dot-separated pair, e.g. "file.read") must never produce two
// identical aliases. If this ever fails after adding a new native tool, the
// new tool's name collides with an existing one after sanitization and
// needs to be renamed, not silently allowed to shadow the other.
func TestToolAliasMapIsInjectiveForCurrentNativeToolSet(t *testing.T) {
	names := []string{
		"file.read", "file.write_patch", "shell.run", "test.run",
		"job.output", "job.kill", "job.list", "diagnostics.check",
		"question.ask", "mode.request", "repo.card", "repo.index",
		"repo.search", "symbols.find", "todo.write", "repo.map",
		"tools.select", "git.status", "git.diff",
	}
	tools := make([]registry.Tool, len(names))
	for i, n := range names {
		tools[i] = registry.Tool{Name: n}
	}
	aliasToName := toolAliasMap(tools)
	if len(aliasToName) != len(names) {
		t.Fatalf("toolAliasMap produced %d aliases for %d distinct tool names: %v", len(aliasToName), len(names), aliasToName)
	}
}

// Two distinct canonical names that sanitize to the same alias must not
// silently collapse into one entry — the later one gets a disambiguating
// numeric suffix, and both remain independently resolvable.
func TestToolAliasMapDisambiguatesCollision(t *testing.T) {
	tools := []registry.Tool{
		{Name: "a.b"}, // sanitizes to "a_b"
		{Name: "a_b"}, // already "a_b" -- collides with the one above
	}
	aliasToName := toolAliasMap(tools)
	if len(aliasToName) != 2 {
		t.Fatalf("expected 2 distinct aliases for 2 distinct tool names, got %d: %v", len(aliasToName), aliasToName)
	}
	seen := make(map[string]bool)
	for _, name := range aliasToName {
		if seen[name] {
			t.Fatalf("canonical name %q reachable via more than one alias, or missing: %v", name, aliasToName)
		}
		seen[name] = true
	}
	if !seen["a.b"] || !seen["a_b"] {
		t.Fatalf("expected both a.b and a_b to be reachable, got %v", aliasToName)
	}
}

// End-to-end: the tool schema sent to the provider must use the dotless
// alias (Kimi's kimi-for-coding rejects any dotted function name outright,
// confirmed live), and when the model calls back using that alias — as a
// well-behaved provider echoes it verbatim — the runner must resolve it to
// the real registered tool and execute it.
func TestNativeToolCallResolvesAliasedNameEndToEnd(t *testing.T) {
	state := newTestState(t)
	reg := registry.New()
	var gotArgs json.RawMessage
	if err := reg.Register(registry.Tool{
		Name: "file.read", Risk: registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			gotArgs = call.Args
			return registry.ToolResult{Summary: "read ok", Content: "file contents"}, nil
		},
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	p := &agenttest.ScriptedProvider{
		Responses: []string{"Reading.", "Done."},
		ToolCalls: [][]schema.ToolCall{
			{{ID: "c1", Name: "file_read", Args: json.RawMessage(`{"path":"a.go"}`)}},
			nil,
		},
	}
	r := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.NativeTools = true
	r.SetForceClass(string(ClassQuestion))

	task, err := r.RunTask(context.Background(), "read a.go")
	if err != nil {
		t.Fatalf("RunTask err = %v", err)
	}
	if task.Status != TaskStatusCompleted {
		t.Fatalf("Status = %v, want Completed", task.Status)
	}
	if string(gotArgs) != `{"path":"a.go"}` {
		t.Fatalf("handler args = %s, tool never actually executed (alias did not resolve)", gotArgs)
	}
	if len(p.Requests) == 0 {
		t.Fatal("no requests recorded")
	}
	for _, tool := range p.Requests[0].Tools {
		if strings.Contains(tool.Name, ".") {
			t.Fatalf("outgoing tool definition name %q contains a dot; Kimi rejects this", tool.Name)
		}
	}
}

// Real, live bug found testing against Kimi's kimi-for-coding-highspeed:
// "ask_user" is registered as a genuine native tool (internal/tools/native/
// question.go's askUserTool, wired in native.go) AND buildToolDefinitions
// separately hardcodes a second "ask_user" ToolDefinition for RoleGeneral.
// When a runner's registry actually contains the real ask_user tool, this
// produces two schema entries with the identical name — Kimi's API rejects
// the whole request with HTTP 400 "function name ask_user is duplicated".
// The hardcoded fallback must only fire when the registry does NOT already
// provide ask_user, not unconditionally.
func TestBuildToolDefinitionsDoesNotDuplicateRegisteredAskUser(t *testing.T) {
	reg := registry.New()
	if err := reg.Register(registry.Tool{
		Name: "ask_user", Risk: registry.RiskReadOnly, Handler: nopHandler,
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	r := NewRunner(&agenttest.ScriptedProvider{}, reg, policy.NewEngine(&config.Config{}, nil), newTestState(t), "test-model")
	r.Role = RoleGeneral

	defs := r.buildToolDefinitions()
	count := 0
	for _, d := range defs {
		if d.Name == "ask_user" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("got %d ask_user tool definitions, want exactly 1: %+v", count, defs)
	}
}

// The fallback must still fire when the registry genuinely has no ask_user
// tool at all (matches existing tests like
// TestNativeAskUserCountsAgainstIterationBudget, which script ask_user tool
// calls against an empty registry.New()).
func TestBuildToolDefinitionsFallsBackToAskUserWhenNotRegistered(t *testing.T) {
	r := NewRunner(&agenttest.ScriptedProvider{}, registry.New(), policy.NewEngine(&config.Config{}, nil), newTestState(t), "test-model")
	r.Role = RoleGeneral

	defs := r.buildToolDefinitions()
	count := 0
	for _, d := range defs {
		if d.Name == "ask_user" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("got %d ask_user tool definitions, want exactly 1 (fallback): %+v", count, defs)
	}
}
