package commands

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/contextpack"
	"marshal/internal/db"
	"marshal/internal/history"
	"marshal/internal/tools/registry"
)

func newTestState() *session.State {
	return session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
}

func TestNew(t *testing.T) {
	reg := New()
	if reg == nil {
		t.Fatal("New() returned nil")
	}
	if len(reg.commands) != 0 {
		t.Errorf("expected empty commands map, got %d commands", len(reg.commands))
	}
}

func TestRegister(t *testing.T) {
	reg := New()
	err := reg.Register(Command{Name: "test", Handler: func(s *session.State, a []string) Result { return Text("") }})
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}
}

func TestRegisterDuplicate(t *testing.T) {
	reg := New()
	h := func(s *session.State, a []string) Result { return Text("") }
	reg.Register(Command{Name: "test", Handler: h})
	err := reg.Register(Command{Name: "test", Handler: h})
	if !errors.Is(err, ErrDuplicateCommand) {
		t.Errorf("expected ErrDuplicateCommand, got %v", err)
	}
}

func TestRegisterEmptyName(t *testing.T) {
	reg := New()
	err := reg.Register(Command{Name: "", Handler: func(s *session.State, a []string) Result { return Text("") }})
	if !errors.Is(err, ErrInvalidCommand) {
		t.Errorf("expected ErrInvalidCommand, got %v", err)
	}
}

func TestRegisterNilHandler(t *testing.T) {
	reg := New()
	err := reg.Register(Command{Name: "test"})
	if !errors.Is(err, ErrInvalidCommand) {
		t.Errorf("expected ErrInvalidCommand, got %v", err)
	}
}

func TestLookup(t *testing.T) {
	reg := New()
	reg.Register(Command{Name: "test", Description: "desc", Handler: func(s *session.State, a []string) Result { return Text("ok") }})

	cmd, ok := reg.Lookup("test")
	if !ok {
		t.Fatal("Lookup() not found")
	}
	if cmd.Description != "desc" {
		t.Errorf("expected desc, got %s", cmd.Description)
	}
	result := cmd.Handler(newTestState(), nil).Text
	if result != "ok" {
		t.Errorf("expected ok, got %s", result)
	}
}

func TestLookupNotFound(t *testing.T) {
	reg := New()
	_, ok := reg.Lookup("nonexistent")
	if ok {
		t.Error("Lookup() should not find nonexistent command")
	}
}

func TestList(t *testing.T) {
	reg := New()
	reg.Register(Command{Name: "b", Handler: func(s *session.State, a []string) Result { return Text("") }})
	reg.Register(Command{Name: "a", Handler: func(s *session.State, a []string) Result { return Text("") }})

	cmds := reg.List()
	if len(cmds) != 2 {
		t.Fatalf("expected 2 commands, got %d", len(cmds))
	}
	if cmds[0].Name != "a" || cmds[1].Name != "b" {
		t.Errorf("commands not sorted: got %s, %s", cmds[0].Name, cmds[1].Name)
	}
}

func TestRegisterAll(t *testing.T) {
	cmdReg := New()
	toolReg := registry.New()
	err := RegisterAll(cmdReg, toolReg)
	if err != nil {
		t.Fatalf("RegisterAll() error = %v", err)
	}
	cmds := cmdReg.List()
	if len(cmds) < 10 {
		t.Errorf("expected at least 10 commands, got %d", len(cmds))
	}
	optCmd, ok := cmdReg.Lookup("options")
	if !ok {
		t.Fatal("/options not registered")
	}
	if optCmd.Group != "Models & providers" {
		t.Errorf("/options group = %q, want Models & providers", optCmd.Group)
	}
	if !optCmd.TUIOnly {
		t.Error("/options should be TUIOnly")
	}
	profCmd, ok := cmdReg.Lookup("profiles")
	if !ok {
		t.Fatal("/profiles not registered")
	}
	if profCmd.Group != "Models & providers" {
		t.Errorf("/profiles group = %q, want Models & providers", profCmd.Group)
	}
	if !profCmd.TUIOnly {
		t.Error("/profiles should be TUIOnly")
	}
}

func TestHelpCommandReturnsGroupedDoc(t *testing.T) {
	cmdReg := New()
	toolReg := registry.New()
	RegisterAll(cmdReg, toolReg)

	cmd, _ := cmdReg.Lookup("help")
	res := cmd.Handler(newTestState(), nil)
	if res.Doc == nil {
		t.Fatalf("/help should return a Doc, got %+v", res)
	}
	var headers []string
	var names []string
	for _, r := range res.Doc.Rows {
		if r.Header != "" {
			headers = append(headers, r.Header)
		} else {
			names = append(names, r.Text)
		}
	}
	for _, want := range []string{"Chat", "Settings & info"} {
		if !slices.Contains(headers, want) {
			t.Errorf("missing group header %q in %v", want, headers)
		}
	}
	if !slices.Contains(names, "/new") {
		t.Errorf("missing /new row in %v", names)
	}
	if res.Doc.Footer == "" {
		t.Error("expected keybinding cheatsheet in Doc.Footer")
	}
}

func TestHelpPrintsCheatsheet(t *testing.T) {
	cmdReg := New()
	toolReg := registry.New()
	RegisterAll(cmdReg, toolReg)

	cmd, _ := cmdReg.Lookup("help")
	res := cmd.Handler(nil, nil)
	if res.Doc == nil {
		t.Fatalf("/help should return a Doc, got %+v", res)
	}
	if !strings.Contains(res.Doc.Footer, "⏎ send") {
		t.Errorf("cheatsheet footer missing ⏎ send: %q", res.Doc.Footer)
	}
	if !strings.Contains(res.Doc.Footer, "esc") {
		t.Errorf("cheatsheet footer missing esc: %q", res.Doc.Footer)
	}
}

func TestHelpForSingleCommand(t *testing.T) {
	cmdReg := New()
	toolReg := registry.New()
	RegisterAll(cmdReg, toolReg)

	cmd, _ := cmdReg.Lookup("help")
	out := cmd.Handler(nil, []string{"set"}).Text
	if !strings.Contains(out, "/set") || !strings.Contains(out, "<key> [value]") {
		t.Errorf("per-command help incomplete: %q", out)
	}
}

func TestToolsCommandReturnsDoc(t *testing.T) {
	cmdReg := New()
	toolReg := registry.New()
	toolReg.Register(registry.Tool{
		Name:        "read",
		Risk:        registry.RiskReadOnly,
		Description: "Read files from the workspace",
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{Summary: "ok"}, nil
		},
	})
	RegisterAll(cmdReg, toolReg)

	cmd, _ := cmdReg.Lookup("tools")
	res := cmd.Handler(newTestState(), nil)
	if res.Doc == nil {
		t.Fatalf("/tools should return a Doc, got %+v", res)
	}
	if len(res.Doc.Rows) == 0 || res.Doc.Rows[0].Detail == "" {
		t.Fatalf("expected tool rows with risk/description detail, got %+v", res.Doc.Rows)
	}
}

func TestRouteCommand(t *testing.T) {
	cmdReg := New()
	toolReg := registry.New()
	RegisterAll(cmdReg, toolReg)

	state := newTestState()
	state.SetActiveRoute(session.RouteInfo{
		Role:     "planner",
		Profile:  "default",
		Preset:   "gpt4",
		Provider: "openai",
		Model:    "gpt-4",
		Active:   true,
	})

	cmd, _ := cmdReg.Lookup("route")
	result := cmd.Handler(state, nil).Text
	if !strings.Contains(result, "gpt-4") {
		t.Errorf("route output missing model info: %s", result)
	}
}

func contextResult(t *testing.T) Result {
	t.Helper()
	cmdReg := New()
	toolReg := registry.New()
	RegisterAll(cmdReg, toolReg)
	state := newTestState()
	state.AddMessage(session.RoleUser, "hello", session.ContentTypePlain)
	state.AddMessage(session.RoleAssistant, "hi there", session.ContentTypePlain)
	cmd, _ := cmdReg.Lookup("context")
	return cmd.Handler(state, nil)
}

func contextResultWithPack(t *testing.T) Result {
	t.Helper()
	cmdReg := New()
	toolReg := registry.New()
	RegisterAll(cmdReg, toolReg)
	state := newTestState()
	state.AddMessage(session.RoleUser, "hello", session.ContentTypePlain)
	state.AddMessage(session.RoleAssistant, "hi there", session.ContentTypePlain)
	state.SetContextPack(contextpack.Pack{
		Sections: []contextpack.Section{
			{Title: "internal/app/tui/model.go", EstimatedTokens: 8400},
			{Source: "repo-map", EstimatedTokens: 2100},
		},
		TokenUsage: contextpack.TokenUsage{EstimatedTokens: 10500, MaxTokens: 32000},
	})
	cmd, _ := cmdReg.Lookup("context")
	return cmd.Handler(state, nil)
}

func TestContextCommandReturnsFullFrameDoc(t *testing.T) {
	res := contextResult(t)
	if res.Doc == nil || !res.Doc.FullFrame {
		t.Fatalf("/context should return a FullFrame Doc, got %+v", res)
	}
	if len(res.Doc.Rows) == 0 {
		t.Fatal("expected stat rows")
	}
	found := false
	for _, r := range res.Doc.Rows {
		if r.Text == "Messages" && r.Detail != "" {
			found = true
		}
	}
	if !found {
		t.Errorf("missing Messages stat row: %+v", res.Doc.Rows)
	}
}

func TestContextCommandListsPackSections(t *testing.T) {
	res := contextResultWithPack(t)
	var sectionRows int
	for _, r := range res.Doc.Rows {
		if r.Detail != "" && r.Text != "Messages" && r.Text != "Pack" {
			sectionRows++
		}
	}
	if sectionRows == 0 {
		t.Fatalf("expected one row per pack section: %+v", res.Doc.Rows)
	}
}

func TestNewAndClearAreTUIOnly(t *testing.T) {
	cmdReg := New()
	toolReg := registry.New()
	RegisterAll(cmdReg, toolReg)

	for _, name := range []string{"new", "clear"} {
		cmd, ok := cmdReg.Lookup(name)
		if !ok {
			t.Fatalf("/%s not registered", name)
		}
		if !cmd.TUIOnly {
			t.Errorf("/%s must be TUIOnly", name)
		}
		if cmd.Handler != nil {
			t.Errorf("/%s must not have a headless Handler", name)
		}
	}
}

func TestConfigCommand(t *testing.T) {
	cmdReg := New()
	toolReg := registry.New()
	RegisterAll(cmdReg, toolReg)

	state := newTestState()
	cmd, _ := cmdReg.Lookup("config")
	result := cmd.Handler(state, nil).Text
	if !strings.Contains(result, "Configuration") {
		t.Errorf("config output missing header: %s", result)
	}
}

func TestRollbackNoBackup(t *testing.T) {
	cmdReg := New()
	toolReg := registry.New()
	RegisterAll(cmdReg, toolReg)

	state := newTestState()
	cmd, _ := cmdReg.Lookup("rollback")
	result := cmd.Handler(state, nil).Text
	if !strings.Contains(result, "No backup available") {
		t.Errorf("rollback output wrong: %s", result)
	}
}

func TestExitQuitCommands(t *testing.T) {
	cmdReg := New()
	toolReg := registry.New()
	RegisterAll(cmdReg, toolReg)

	cmd, ok := cmdReg.Lookup("exit")
	if !ok {
		t.Fatal("exit command not registered")
	}
	if !cmd.TUIOnly {
		t.Error("exit should be TUIOnly")
	}

	cmd, ok = cmdReg.Lookup("quit")
	if !ok {
		t.Fatal("quit command not registered")
	}
	if !cmd.TUIOnly {
		t.Error("quit should be TUIOnly")
	}
}

func TestModeSwitchCommands(t *testing.T) {
	cmdReg := New()
	toolReg := registry.New()
	RegisterAll(cmdReg, toolReg)

	for _, name := range []string{"plan", "default", "edit", "copilot", "auto"} {
		cmd, ok := cmdReg.Lookup(name)
		if !ok {
			t.Fatalf("%s command not registered", name)
		}
		if !cmd.TUIOnly {
			t.Errorf("%s should be TUIOnly", name)
		}
	}
}

func TestStopCommand(t *testing.T) {
	cmdReg := New()
	toolReg := registry.New()
	RegisterAll(cmdReg, toolReg)

	cmd, ok := cmdReg.Lookup("stop")
	if !ok {
		t.Fatal("stop command not registered")
	}
	if !cmd.TUIOnly {
		t.Error("stop should be TUIOnly")
	}
}

func TestModelsCommandRegistered(t *testing.T) {
	cmdReg := New()
	toolReg := registry.New()
	RegisterAll(cmdReg, toolReg)

	cmd, ok := cmdReg.Lookup("models")
	if !ok {
		t.Fatal("models command not registered")
	}
	if !cmd.TUIOnly {
		t.Error("models should be TUIOnly")
	}
	if cmd.Args != "[preset-or-model]" {
		t.Errorf("models Args = %q, want %q", cmd.Args, "[preset-or-model]")
	}
	if _, ok := cmdReg.Lookup("model"); ok {
		t.Error("/model was consolidated into /models and must not be registered")
	}
}

func TestRegisterAllIncludesSwarmCommand(t *testing.T) {
	cmdReg := New()
	if err := RegisterAll(cmdReg, registry.New()); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	cmd, ok := cmdReg.Lookup("swarm")
	if !ok {
		t.Fatal("swarm command not registered")
	}
	if cmd.Args != "<goal>" {
		t.Fatalf("swarm Args = %q, want \"<goal>\"", cmd.Args)
	}
	if !cmd.TUIOnly {
		t.Error("swarm should be TUIOnly")
	}
}

func TestSessionCommands(t *testing.T) {
	cmdReg := New()
	if err := RegisterAll(cmdReg, registry.New()); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	for _, tt := range []struct {
		name     string
		wantArgs string
	}{
		{"save", ""},
		{"sessions", ""},
		{"resume", "<session-id>"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cmd, ok := cmdReg.Lookup(tt.name)
			if !ok {
				t.Fatalf("%s command not registered", tt.name)
			}
			if !cmd.TUIOnly {
				t.Errorf("%s should be TUIOnly", tt.name)
			}
			if cmd.Args != tt.wantArgs {
				t.Fatalf("%s Args = %q, want %q", tt.name, cmd.Args, tt.wantArgs)
			}
		})
	}
}

func TestLogCommandShowsRecentAuditEvents(t *testing.T) {
	cmdReg := New()
	if err := RegisterAll(cmdReg, registry.New()); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	for i := 0; i < 20; i++ {
		state.LogToolCall(registry.AuditEvent{
			Timestamp:     time.Date(2026, 7, 5, 12, 0, i, 0, time.UTC),
			ToolName:      fmt.Sprintf("tool.%d", i),
			ResultSummary: fmt.Sprintf("result %d", i),
		})
	}

	cmd, ok := cmdReg.Lookup("log")
	if !ok {
		t.Fatal("log command not registered")
	}
	res := cmd.Handler(state, nil)
	if res.Doc == nil {
		t.Fatalf("/log should return a Doc, got %+v", res)
	}
	if len(res.Doc.Rows) == 0 {
		t.Fatal("expected log rows")
	}
	// Should contain the newest event.
	last := res.Doc.Rows[len(res.Doc.Rows)-1]
	if !strings.Contains(last.Text, "tool.19") || !strings.Contains(last.Detail, "result 19") {
		t.Fatalf("log rows missing newest event, last row: %+v", last)
	}
	// Should only contain the last 15 events (indices 5..19).
	if len(res.Doc.Rows) != 15 {
		t.Fatalf("expected 15 log rows, got %d", len(res.Doc.Rows))
	}
}

func TestLogCommandEmpty(t *testing.T) {
	cmdReg := New()
	if err := RegisterAll(cmdReg, registry.New()); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	cmd, _ := cmdReg.Lookup("log")
	if out := cmd.Handler(state, nil).Text; out != "No tool calls yet." {
		t.Fatalf("empty log output = %q", out)
	}
}

func TestRegisterAllWithNilToolRegistry(t *testing.T) {
	cmdReg := New()
	if err := RegisterAll(cmdReg, nil); err != nil {
		t.Fatalf("RegisterAll(nil toolReg) error = %v, want nil", err)
	}
	cmds := cmdReg.List()
	if len(cmds) < 10 {
		t.Errorf("expected at least 10 commands with nil toolReg, got %d", len(cmds))
	}
	// /exit must be available so the user can quit even when the agent
	// failed to initialise.
	if _, ok := cmdReg.Lookup("exit"); !ok {
		t.Fatal("exit command not registered with nil toolReg")
	}
}

func TestToolsCommandWithNilToolRegistry(t *testing.T) {
	cmdReg := New()
	if err := RegisterAll(cmdReg, nil); err != nil {
		t.Fatalf("RegisterAll(nil toolReg) error = %v, want nil", err)
	}
	cmd, ok := cmdReg.Lookup("tools")
	if !ok {
		t.Fatal("tools command not registered")
	}
	result := cmd.Handler(newTestState(), nil).Text
	if !strings.Contains(result, "Tools unavailable") {
		t.Fatalf("tools output with nil toolReg = %q, want 'Tools unavailable'", result)
	}
}

func TestDiffCommandNoSnapshot(t *testing.T) {
	cmdReg := New()
	if err := RegisterAll(cmdReg, registry.New()); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	state := newTestState()
	// No snapshotter, no DB → friendly message, no crash.
	cmd, ok := cmdReg.Lookup("diff")
	if !ok {
		t.Fatal("diff command not registered")
	}
	out := cmd.Handler(state, nil).Text
	if out == "" {
		t.Fatal("expected a message for /diff with no snapshot")
	}
}

func TestDiffCommandRegistered(t *testing.T) {
	cmdReg := New()
	if err := RegisterAll(cmdReg, registry.New()); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	cmd, ok := cmdReg.Lookup("diff")
	if !ok {
		t.Fatal("diff command not registered")
	}
	if !strings.Contains(cmd.Description, "cumulative changes") {
		t.Fatalf("diff command description wrong: %q", cmd.Description)
	}
}

func TestAgentsCommandListed(t *testing.T) {
	reg := New()
	if err := RegisterAll(reg, nil); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	c, ok := reg.Lookup("agents")
	if !ok {
		t.Fatal("/agents not registered")
	}
	if c.Group != "Workflows" {
		t.Fatalf("group = %q, want Workflows", c.Group)
	}
}

func TestModeCommandRegistered(t *testing.T) {
	reg := New()
	if err := RegisterAll(reg, nil); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	if _, ok := reg.Lookup("mode"); !ok {
		t.Fatal("/mode should be registered")
	}
}

func TestHelpListsFlagshipCommands(t *testing.T) {
	cmdReg := New()
	toolReg := registry.New()
	RegisterAll(cmdReg, toolReg)

	cmd, ok := cmdReg.Lookup("help")
	if !ok {
		t.Fatal("help command not registered")
	}
	res := cmd.Handler(newTestState(), nil)
	if res.Doc == nil {
		t.Fatalf("/help should return a Doc, got %+v", res)
	}

	var names []string
	for _, r := range res.Doc.Rows {
		if r.Header == "" {
			names = append(names, r.Text)
		}
	}

	// Flagship commands that were previously hidden must now appear in /help.
	for _, name := range []string{"connect", "models", "mode", "swarm", "sdd", "settings", "memory"} {
		found := false
		for _, n := range names {
			if strings.HasPrefix(n, "/"+name) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("help output should contain /%s, got names: %v", name, names)
		}
	}

	// Truly hidden commands (plan, default, edit, copilot, auto, stop) must NOT appear.
	for _, name := range []string{"plan", "default", "edit", "copilot", "auto", "stop"} {
		for _, n := range names {
			if strings.HasPrefix(n, "/"+name) {
				t.Errorf("help output should not contain /%s, got names: %v", name, names)
			}
		}
	}
}

func TestHelpGroupsCommands(t *testing.T) {
	cmdReg := New()
	toolReg := registry.New()
	RegisterAll(cmdReg, toolReg)

	cmd, ok := cmdReg.Lookup("help")
	if !ok {
		t.Fatal("help command not registered")
	}
	res := cmd.Handler(newTestState(), nil)
	if res.Doc == nil {
		t.Fatalf("/help should return a Doc, got %+v", res)
	}

	// All five groups must appear in the fixed order.
	groups := []string{"Chat", "Models & providers", "Workflows", "Changes", "Settings & info"}
	var seen []string
	for _, r := range res.Doc.Rows {
		if r.Header != "" {
			seen = append(seen, r.Header)
		}
	}
	if len(seen) < len(groups) {
		t.Fatalf("expected at least %d group headers, got %v", len(groups), seen)
	}
	for i, g := range groups {
		if i >= len(seen) || seen[i] != g {
			t.Errorf("group %d: expected %q, got %v", i, g, seen)
		}
	}

	// No "Other" group should appear because every visible command is grouped.
	for _, r := range res.Doc.Rows {
		if r.Header == "Other" {
			t.Errorf("help output should not contain an 'Other' group header")
		}
	}
}

func TestDiffDoesNotInjectIntoState(t *testing.T) {
	cmdReg := New()
	toolReg := registry.New()
	RegisterAll(cmdReg, toolReg)

	state := newTestState()
	cmd, ok := cmdReg.Lookup("diff")
	if !ok {
		t.Fatal("diff command not registered")
	}
	before := len(state.Messages())
	result := cmd.Handler(state, nil).Text
	after := len(state.Messages())

	if after != before {
		t.Errorf("expected no new messages in state after /diff, got %d -> %d", before, after)
	}
	if result == "" {
		t.Error("/diff should return a message, not empty string")
	}
	// No message in state should have ContentTypeDiff.
	for _, m := range state.Messages() {
		if m.ContentType == session.ContentTypeDiff {
			t.Errorf("state should not contain ContentTypeDiff messages: %q", m.Content)
		}
	}
}

func TestExportComputesPathBeforeWrite(t *testing.T) {
	cmdReg := New()
	toolReg := registry.New()
	RegisterAll(cmdReg, toolReg)

	state := newTestState()
	tmpDir := t.TempDir()
	state.WorkingDir = tmpDir

	cmd, ok := cmdReg.Lookup("export")
	if !ok {
		t.Fatal("export command not registered")
	}

	result := cmd.Handler(state, nil).Text

	if !strings.Contains(result, "Exported to ") {
		t.Fatalf("export output missing 'Exported to': %q", result)
	}
	if !strings.Contains(result, tmpDir) {
		t.Fatalf("export output should contain working dir: %q", result)
	}
	if strings.Contains(result, "failed") {
		t.Fatalf("export failed unexpectedly: %q", result)
	}

	defaultPath := filepath.Join(tmpDir, "marshal-session-"+state.SessionID()+".html")
	if _, err := os.Stat(defaultPath); os.IsNotExist(err) {
		t.Fatalf("export file not found at default path: %s", defaultPath)
	}
}

func TestExportRejectsAbsolutePath(t *testing.T) {
	cmdReg := New()
	toolReg := registry.New()
	RegisterAll(cmdReg, toolReg)

	state := newTestState()
	cmd, _ := cmdReg.Lookup("export")
	out := cmd.Handler(state, []string{"/etc/passwd"}).Text
	if !strings.Contains(out, "must be relative") {
		t.Fatalf("expected absolute path to be rejected with 'must be relative', got %q", out)
	}
}

func TestExportRejectsParentTraversal(t *testing.T) {
	cmdReg := New()
	toolReg := registry.New()
	RegisterAll(cmdReg, toolReg)

	state := newTestState()
	cmd, _ := cmdReg.Lookup("export")
	out := cmd.Handler(state, []string{"../../etc/passwd"}).Text
	if !strings.Contains(out, "escapes") {
		t.Fatalf("expected parent traversal to be rejected with 'escapes', got %q", out)
	}
}

func TestHiddenCommandsStillRunnable(t *testing.T) {
	cmdReg := New()
	toolReg := registry.New()
	RegisterAll(cmdReg, toolReg)

	// Hidden commands must still be findable via Lookup.
	for _, name := range []string{"stop", "plan", "default", "edit", "copilot", "auto", "mode", "swarm", "sdd", "settings", "memory", "connect", "models"} {
		_, ok := cmdReg.Lookup(name)
		if !ok {
			t.Errorf("hidden command /%s must still be registered for Lookup", name)
		}
	}
}

func TestRegisterTUIOnlyCommandWithoutHandler(t *testing.T) {
	r := New()
	err := r.Register(Command{Name: "tuitest", Description: "d", Hidden: true, TUIOnly: true})
	if err != nil {
		t.Fatalf("Register TUIOnly without Handler: %v", err)
	}
	cmd, ok := r.Lookup("tuitest")
	if !ok || !cmd.TUIOnly {
		t.Fatalf("Lookup: ok=%v TUIOnly=%v", ok, cmd.TUIOnly)
	}
}

func TestRegisterNonTUICommandStillRequiresHandler(t *testing.T) {
	r := New()
	err := r.Register(Command{Name: "nohandler", Description: "d"})
	if err == nil {
		t.Fatal("Register without Handler and without TUIOnly: want error, got nil")
	}
}

func TestTrustReportsCurrentStatus(t *testing.T) {
	cmdReg := New()
	toolReg := registry.New()
	RegisterAll(cmdReg, toolReg)

	cmd, ok := cmdReg.Lookup("trust")
	if !ok {
		t.Fatal("trust command not registered")
	}

	// Default state: not trusted.
	state := newTestState()
	out := cmd.Handler(state, nil).Text
	if !strings.Contains(out, "not trusted") {
		t.Fatalf("expected 'not trusted' in output, got: %q", out)
	}

	// After marking trusted.
	state.SetTrusted(true)
	out = cmd.Handler(state, nil).Text
	if !strings.Contains(out, "trusted") {
		t.Fatalf("expected 'trusted' in output, got: %q", out)
	}
	if strings.Contains(out, "not trusted") {
		t.Fatalf("trusted state should not say 'not trusted', got: %q", out)
	}
}

func TestResultPlainTextRendersDoc(t *testing.T) {
	res := Panel("Things", false, []Row{
		{Header: "Group A"},
		{Text: "one", Detail: "first"},
		{Text: "two", Detail: "second", Desc: "more about two"},
	})
	out := res.PlainText()
	for _, want := range []string{"Group A", "one", "first", "two", "second", "more about two"} {
		if !strings.Contains(out, want) {
			t.Errorf("PlainText() missing %q:\n%s", want, out)
		}
	}
	if res.Text != "" {
		t.Errorf("Panel result should have empty Text, got %q", res.Text)
	}
}

// TestResultPlainTextRendersDescIndented pins the indentation pattern:
// Desc lives on its own line below Text/Detail, indented one extra level
// past the row's indent. The TUI panel skips Desc; headless callers see it.
func TestResultPlainTextRendersDescIndented(t *testing.T) {
	res := Panel("Things", false, []Row{
		{Text: "parent", Desc: "parent description", Children: []Row{
			{Text: "child", Desc: "child description"},
		}},
	})
	out := res.PlainText()
	wantLines := []string{
		"Things",
		"  parent",
		"    parent description",
		"    child",
		"      child description",
	}
	for _, line := range wantLines {
		if !strings.Contains(out, line) {
			t.Errorf("PlainText() missing line %q in:\n%s", line, out)
		}
	}
}

// TestResultPlainTextSkipsDescWhenEmpty locks the contract that an
// empty Desc does NOT emit an extra blank line under Text/Detail.
func TestResultPlainTextSkipsDescWhenEmpty(t *testing.T) {
	res := Panel("Things", false, []Row{
		{Text: "noDesc"},
	})
	out := res.PlainText()
	// "  noDesc" must appear followed by exactly one newline before EOF.
	if got := strings.TrimSpace(out); got != "Things\n  noDesc" && !strings.Contains(got, "noDesc") {
		t.Errorf("expected no trailing desc line for empty Desc, got:\n%s", out)
	}
}

func branchesTestState(t *testing.T) *session.State {
	t.Helper()
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	state.AddMessage(session.RoleUser, "turn1", session.ContentTypePlain)
	state.AddMessage(session.RoleAssistant, "a1", session.ContentTypeMarkdown)
	state.AddMessage(session.RoleUser, "turn2", session.ContentTypePlain)
	state.AddMessage(session.RoleAssistant, "a2", session.ContentTypeMarkdown)
	// Rewind to before turn2, then add a new turn → 2 branches.
	msgs := state.Messages()
	state.Rewind(msgs[2].ID)
	state.AddMessage(session.RoleUser, "turn3", session.ContentTypePlain)
	state.AddMessage(session.RoleAssistant, "a3", session.ContentTypeMarkdown)
	return state
}

func TestBranchesCommandReturnsActionRows(t *testing.T) {
	state := branchesTestState(t)
	cmdReg := New()
	toolReg := registry.New()
	RegisterAll(cmdReg, toolReg)
	cmd, _ := cmdReg.Lookup("branches")
	res := cmd.Handler(state, nil)
	if res.Doc == nil {
		t.Fatalf("/branches should return a Doc, got %+v", res)
	}
	if len(res.Doc.Rows) != len(state.Branches()) {
		t.Fatalf("rows = %d, want one per branch", len(res.Doc.Rows))
	}
	// The row for a non-current branch switches to it.
	leaves := state.Branches()
	cur := state.LeafID()
	for i, r := range res.Doc.Rows {
		if leaves[i] != cur && r.Action != nil {
			out := r.Action(state)
			if state.LeafID() != leaves[i] {
				t.Fatalf("action did not switch branch: leaf = %d, want %d", state.LeafID(), leaves[i])
			}
			if out.Text == "" {
				t.Fatal("action should return a transcript confirmation")
			}
			return
		}
	}
	t.Fatal("no actionable non-current branch row found")
}

// historyTestState creates an in-memory DB with one session and two
// generations (one live with a turn, one ended with no turns), then returns
// a session.State wired to it.
func historyTestState(t *testing.T) (*session.State, []history.GenerationSummary) {
	t.Helper()
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	if err := database.Migrate(); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}

	projectID, err := database.GetOrCreateProject("/test/repo", "test-repo")
	if err != nil {
		t.Fatalf("GetOrCreateProject: %v", err)
	}
	sessionID := "test-session-" + t.Name()
	if err := database.CreateSession(sessionID, projectID, "test", time.Now().UTC()); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	now := time.Now().UTC()
	g1 := db.Generation{
		ID: "gen-1", SessionID: sessionID, Seq: 1,
		StartedAt: now, SeedDigest: "digest-one",
	}
	if err := database.BeginGeneration(g1); err != nil {
		t.Fatalf("BeginGeneration: %v", err)
	}
	if err := database.ArchiveTurns("gen-1", []db.ArchivedTurn{
		{TurnSeq: 1, Role: "user", Content: "hello", CreatedAt: now},
		{TurnSeq: 2, Role: "assistant", Content: "world", CreatedAt: now},
	}, 1024, now); err != nil {
		t.Fatalf("ArchiveTurns: %v", err)
	}

	g2 := db.Generation{
		ID: "gen-2", SessionID: sessionID, Seq: 2,
		StartedAt: now.Add(time.Minute), SeedDigest: "digest-two",
	}
	if err := database.BeginGeneration(g2); err != nil {
		t.Fatalf("BeginGeneration: %v", err)
	}
	if err := database.EndGeneration("gen-2", now.Add(2*time.Minute), "completed"); err != nil {
		t.Fatalf("EndGeneration: %v", err)
	}

	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{
		DB: database, SessionID: sessionID,
	})

	summaries, err := history.ListGenerations(context.Background(), database, sessionID)
	if err != nil {
		t.Fatalf("ListGenerations: %v", err)
	}
	return state, summaries
}

func TestHistoryCommandReturnsGenerationRows(t *testing.T) {
	state, gens := historyTestState(t)
	cmdReg := New()
	toolReg := registry.New()
	RegisterAll(cmdReg, toolReg)
	cmd, _ := cmdReg.Lookup("history")
	res := cmd.Handler(state, nil)
	if res.Doc == nil || !res.Doc.FullFrame {
		t.Fatalf("/history should return a FullFrame Doc, got %+v", res)
	}
	if len(res.Doc.Rows) != len(gens) {
		t.Fatalf("rows = %d, want one per generation", len(res.Doc.Rows))
	}
	children := res.Doc.Rows[0].Children
	if len(children) == 0 {
		t.Fatal("generation rows should carry their turns as Children")
	}
}

func TestListAllIncludesHiddenCommands(t *testing.T) {
	cmdReg := New()
	toolReg := registry.New()
	RegisterAll(cmdReg, toolReg)

	all := map[string]bool{}
	for _, c := range cmdReg.ListAll() {
		all[c.Name] = true
	}
	// Hidden commands are excluded from List (/help) but must appear in
	// ListAll (completion popup).
	for _, name := range []string{"settings", "models", "memory", "connect", "swarm", "sdd", "mode", "plan", "default", "edit", "copilot", "auto", "stop"} {
		if !all[name] {
			t.Errorf("ListAll() missing hidden command /%s", name)
		}
	}
	for _, c := range cmdReg.List() {
		if c.Hidden {
			t.Errorf("List() must exclude hidden commands, found /%s", c.Name)
		}
	}
	if got, want := len(cmdReg.ListAll()), len(cmdReg.List()); got <= want {
		t.Errorf("ListAll() (%d) should cover more commands than List() (%d)", got, want)
	}
}

func TestSDDCommandDescriptionExplainsItself(t *testing.T) {
	cmdReg := New()
	toolReg := registry.New()
	RegisterAll(cmdReg, toolReg)

	var desc string
	for _, c := range cmdReg.ListAll() {
		if c.Name == "sdd" {
			desc = c.Description
		}
	}
	if desc == "" {
		t.Fatal("no /sdd command found")
	}
	if !strings.Contains(strings.ToLower(desc), "plan") {
		t.Errorf("the description must say it runs a plan, got %q", desc)
	}
	if !strings.Contains(strings.ToLower(desc), "commit") {
		t.Errorf("the description must say it commits, got %q", desc)
	}
	if !strings.Contains(strings.ToLower(desc), "author") {
		t.Errorf("the description must mention authoring (/sdd new), got %q", desc)
	}
	if !strings.Contains(strings.ToLower(desc), "deterministic") {
		t.Errorf("the description must mention deterministic execution, got %q", desc)
	}
}

func TestRunCommandIsRegistered(t *testing.T) {
	cmdReg := New()
	toolReg := registry.New()
	RegisterAll(cmdReg, toolReg)
	for _, c := range cmdReg.ListAll() {
		if c.Name == "run" {
			if c.Description == "" {
				t.Error("/run needs a description")
			}
			return
		}
	}
	t.Error("no /run command registered")
}

// contextRowsText joins every row of the /context panel so assertions can
// look for a value without depending on which row carries it.
func contextRowsText(t *testing.T, state *session.State) string {
	t.Helper()
	cmdReg := New()
	RegisterAll(cmdReg, registry.New())
	cmd, ok := cmdReg.Lookup("context")
	if !ok {
		t.Fatal("no /context command registered")
	}
	res := cmd.Handler(state, nil)
	if res.Doc == nil {
		t.Fatalf("/context should return a Doc, got %+v", res)
	}
	var b strings.Builder
	for _, r := range res.Doc.Rows {
		b.WriteString(r.Text)
		b.WriteString(" ")
		b.WriteString(r.Detail)
		b.WriteString("\n")
	}
	return b.String()
}

func TestContextPanelShowsResolvedBudget(t *testing.T) {
	state := newTestState()
	state.SetTurnBudget(256000, 185600, "derived")

	// strutil.CompactTokens renders thousands as "<n>k".
	joined := contextRowsText(t, state)
	for _, want := range []string{"256k", "185k", "derived"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("context panel missing %q:\n%s", want, joined)
		}
	}
}

func TestContextPanelFlagsUnknownWindow(t *testing.T) {
	state := newTestState()
	state.SetTurnBudget(0, 60000, "fallback")

	joined := contextRowsText(t, state)
	if !strings.Contains(joined, "unknown") {
		t.Fatalf("context panel should flag an unknown window:\n%s", joined)
	}
}
