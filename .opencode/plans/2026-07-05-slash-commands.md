# Slash Commands System Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `/`-prefixed command system to the TUI that intercepts user input before agent dispatch, providing quick access to session control, mode switching, configuration, and display of runtime state.

**Architecture:** A new `internal/commands/` package defines a command registry (mirroring the tool registry pattern). Commands are registered with name, description, and handler. The TUI's `Update()` intercepts `/`-prefixed input on Enter and dispatches to the command registry instead of the agent runner. Handlers return display text only; TUI-level side effects (quit, open overlays, cancel agent) are handled by a switch on command name in the TUI model.

**Tech Stack:** Go 1.26.1, Bubble Tea v1.3.10, no new dependencies.

## Global Constraints

- The TUI stays a rendering layer — no policy, prompt, or routing logic in `internal/app/tui/`.
- Commands returning text add a `session.RoleSystem` message to the transcript.
- Slash-command input is NOT sent to the agent runner (not added as `RoleUser`).
- All command handlers must be safe to call concurrently with an active agent turn.
- Follow existing code patterns: functional options for `New()`, mutex-guarded state access, same test structure as existing `model_test.go`.

---

## File Structure

| File | Responsibility |
|------|---------------|
| `internal/commands/types.go` (new) | `Command` struct, `Handler` signature, `Registry` |
| `internal/commands/commands.go` (new) | `RegisterAll()` — all built-in command definitions |
| `internal/commands/commands_test.go` (new) | Tests for registry operations and each command handler |
| `internal/app/session/session.go` (modify) | Add `ClearMessages()` method |
| `internal/app/tui/model.go` (modify) | Command dispatch in `Update()`, `agentCancel` field, extended `AgentRunner` |
| `internal/app/tui/model_test.go` (modify) | Tests for each slash command in the TUI |
| `internal/agent/runner.go` (modify) | Add `ForceClass` field and `SetForceClass()` method |
| `internal/app/app.go` (modify) | Wire command registry + pass to TUI |

---

### Task 1: Define Command Types and Registry

**Files:**
- Create: `internal/commands/types.go`

**Interfaces:**
- Produces: `Command`, `Handler`, `Registry`, `New()`, `Register()`, `Lookup()`, `List()`

- [ ] **Step 1: Write types.go**

```go
package commands

import "marshal/internal/app/session"

type Handler func(state *session.State, args []string) string

type Command struct {
	Name        string
	Description string
	Args        string // human-readable, e.g. "<model-name>" or "" for no args
	Handler     Handler
}
```

- [ ] **Step 2: Write Registry**

```go
package commands

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

var (
	ErrInvalidCommand   = errors.New("invalid command")
	ErrDuplicateCommand = errors.New("duplicate command")
)

type Registry struct {
	commands map[string]Command
}

func New() *Registry {
	return &Registry{commands: make(map[string]Command)}
}

func (r *Registry) Register(cmd Command) error {
	name := strings.TrimSpace(cmd.Name)
	if name == "" {
		return fmt.Errorf("%w: name is required", ErrInvalidCommand)
	}
	if cmd.Handler == nil {
		return fmt.Errorf("%w: handler is required for %q", ErrInvalidCommand, name)
	}
	if _, exists := r.commands[name]; exists {
		return fmt.Errorf("%w: %q", ErrDuplicateCommand, name)
	}
	r.commands[name] = cmd
	return nil
}

func (r *Registry) Lookup(name string) (Command, bool) {
	cmd, ok := r.commands[name]
	return cmd, ok
}

func (r *Registry) List() []Command {
	cmds := make([]Command, 0, len(r.commands))
	for _, cmd := range r.commands {
		cmds = append(cmds, cmd)
	}
	sort.Slice(cmds, func(i, j int) bool {
		return cmds[i].Name < cmds[j].Name
	})
	return cmds
}
```

Types and registry go in the same file `types.go` (149 lines total).

- [ ] **Step 3: Run build to verify compilation**

Run: `go build ./internal/commands/`
Expected: no errors

- [ ] **Step 4: Commit**

```bash
git add internal/commands/types.go
git commit -m "feat: add command types and registry"
```

---

### Task 2: Implement Built-in Command Handlers

**Files:**
- Create: `internal/commands/commands.go`

**Interfaces:**
- Consumes: `Command`, `Handler`, `Registry` from Task 1, `session.State`, `registry.Tool` from existing code
- Produces: `RegisterAll(cmdReg *Registry, toolReg *registry.Registry, projectName string) error`

- [ ] **Step 1: Write commands.go with all 17 commands**

```go
package commands

import (
	"fmt"
	"strings"

	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
)

func RegisterAll(cmdReg *Registry, toolReg *registry.Registry, projectName string) error {
	commands := []Command{
		{
			Name:        "exit",
			Description: "Exit Marshal",
			Handler:     func(state *session.State, args []string) string { return "Goodbye!" },
		},
		{
			Name:        "quit",
			Description: "Exit Marshal (alias for /exit)",
			Handler:     func(state *session.State, args []string) string { return "Goodbye!" },
		},
		{
			Name:        "new",
			Description: "Start a new conversation",
			Handler: func(state *session.State, args []string) string {
				count := state.ClearMessages()
				return fmt.Sprintf("Started new conversation. Cleared %d messages.", count)
			},
		},
		{
			Name:        "clear",
			Description: "Start a new conversation (alias for /new)",
			Handler: func(state *session.State, args []string) string {
				count := state.ClearMessages()
				return fmt.Sprintf("Started new conversation. Cleared %d messages.", count)
			},
		},
		{
			Name:        "help",
			Description: "Show available commands",
			Handler: func(state *session.State, args []string) string {
				var b strings.Builder
				b.WriteString("Available commands:\n\n")
				for _, cmd := range cmdReg.List() {
					argStr := ""
					if cmd.Args != "" {
						argStr = " " + cmd.Args
					}
					b.WriteString(fmt.Sprintf("  /%s%s — %s\n", cmd.Name, argStr, cmd.Description))
				}
				return b.String()
			},
		},
		{
			Name:        "tools",
			Description: "List available tools",
			Handler: func(state *session.State, args []string) string {
				var b strings.Builder
				b.WriteString("Available tools:\n\n")
				for _, tool := range toolReg.List() {
					b.WriteString(fmt.Sprintf("  %s (%s) — %s\n", tool.Name, tool.Risk, tool.Description))
				}
				return b.String()
			},
		},
		{
			Name:        "route",
			Description: "Show current model route",
			Handler: func(state *session.State, args []string) string {
				route := state.ActiveRoute()
				if !route.Active {
					return "No active route."
				}
				local := ""
				if route.LocalOnly {
					local = " (local only)"
				}
				return fmt.Sprintf(
					"Current route:\n  Profile: %s\n  Role: %s\n  Provider: %s\n  Model: %s\n  Preset: %s%s",
					route.Profile, route.Role, route.Provider, route.Model, route.Preset, local,
				)
			},
		},
		{
			Name:        "context",
			Description: "Show context window usage",
			Handler: func(state *session.State, args []string) string {
				msgs := state.Messages()
				var totalChars int
				for _, m := range msgs {
					totalChars += len(m.Content)
				}
				pack := state.ContextPack()
				var fileCount int
				if pack.Files != nil {
					fileCount = len(pack.Files)
				}
				return fmt.Sprintf(
					"Context stats:\n  Messages: %d\n  Total chars: %d\n  Context pack files: %d",
					len(msgs), totalChars, fileCount,
				)
			},
		},
		{
			Name:        "stop",
			Description: "Cancel the current agent turn",
			Handler:     func(state *session.State, args []string) string { return "" },
		},
		{
			Name:        "ask",
			Description: "Switch to Ask mode (read-only, no planning)",
			Handler:     func(state *session.State, args []string) string { return "Switched to Ask mode. Agent will answer questions without planning or editing." },
		},
		{
			Name:        "edit",
			Description: "Switch to Edit mode (planning + full tools)",
			Handler:     func(state *session.State, args []string) string { return "Switched to Edit mode. Agent will plan and execute changes." },
		},
		{
			Name:        "auto",
			Description: "Switch to Auto mode (classify each turn)",
			Handler:     func(state *session.State, args []string) string { return "Switched to Auto mode. Agent will classify each turn automatically." },
		},
		{
			Name:        "model",
			Description: "Switch to a model preset by name",
			Args:        "<preset-name>",
			Handler:     func(state *session.State, args []string) string { return "" },
		},
		{
			Name:        "config",
			Description: "Show current configuration summary",
			Handler: func(state *session.State, args []string) string {
				cfg := state.Config
				route := state.ActiveRoute()
				return fmt.Sprintf(
					"Configuration:\n  Project: %s\n  Working dir: %s\n  Profile: %s\n  Remote allowed: %v\n  Auto-approve: %v",
					cfg.Project.Name, state.WorkingDir, route.Profile, cfg.RemoteProvidersAllowed, cfg.AutoApprove,
				)
			},
		},
		{
			Name:        "settings",
			Description: "Open settings panel",
			Handler:     func(state *session.State, args []string) string { return "" },
		},
		{
			Name:        "memory",
			Description: "Open memory browser",
			Handler:     func(state *session.State, args []string) string { return "" },
		},
		{
			Name:        "rollback",
			Description: "Rollback last patch",
			Handler: func(state *session.State, args []string) string {
				if !state.HasBackup() {
					return "No backup available to rollback."
				}
				if err := state.RollbackBackup(); err != nil {
					return fmt.Sprintf("Rollback failed: %v", err)
				}
				return "Rolled back last patch. All modified files reverted."
			},
		},
	}

	for _, cmd := range commands {
		if err := cmdReg.Register(cmd); err != nil {
			return err
		}
	}
	return nil
}
```

- [ ] **Step 2: Run build to verify compilation**

Run: `go build ./internal/commands/`
Expected: no errors

- [ ] **Step 3: Commit**

```bash
git add internal/commands/commands.go
git commit -m "feat: add built-in command handlers"
```

---

### Task 3: Add ClearMessages to Session State

**Files:**
- Modify: `internal/app/session/session.go` — add `ClearMessages()` method after `Messages()` (line 164)

**Interfaces:**
- Produces: `func (s *State) ClearMessages() int`

- [ ] **Step 1: Add ClearMessages method**

Insert after `Messages()` method (after line 164, before `BeginStreaming()` at line 166):

```go
// ClearMessages removes all messages from the transcript and returns the
// count of messages that were cleared. It does not affect the audit log,
// pending approvals, backups, or context pack.
func (s *State) ClearMessages() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := len(s.messages)
	s.messages = nil
	return count
}
```

- [ ] **Step 2: Run existing session tests**

Run: `go test ./internal/app/session/ -v`
Expected: all pass

- [ ] **Step 3: Commit**

```bash
git add internal/app/session/session.go
git commit -m "feat: add ClearMessages to session state"
```

---

### Task 4: Add ForceClass to Agent Runner

**Files:**
- Modify: `internal/agent/runner.go` — add `ForceClass` field and `SetForceClass` method

**Interfaces:**
- Produces: `ForceClass string` field, `SetForceClass(class string)` method

- [ ] **Step 1: Add ForceClass field**

Add to the `Runner` struct (after `MaxToolResultChars` field, before `callHistory`):

```go
	ForceClass string // if set, overrides Classify() in Run()
```

- [ ] **Step 2: Add SetForceClass method**

Add after `NewRunner()` (after line 103):

```go
func (r *Runner) SetForceClass(class string) {
	r.ForceClass = class
}
```

- [ ] **Step 3: Use ForceClass in Run()**

In `Runner.Run()` at line 118, change:
```go
	task.Class = Classify(goal)
```
to:
```go
	if r.ForceClass != "" {
		task.Class = r.ForceClass
	} else {
		task.Class = Classify(goal)
	}
```

- [ ] **Step 4: Run agent tests**

Run: `go test ./internal/agent/ -v -run TestClassify`
Expected: all pass

- [ ] **Step 5: Commit**

```bash
git add internal/agent/runner.go
git commit -m "feat: add ForceClass to agent runner for slash-command mode switching"
```

---

### Task 5: Extend AgentRunner Interface and Add Command Dispatch to TUI

**Files:**
- Modify: `internal/app/tui/model.go`

**Interfaces:**
- Consumes: `commands.Registry`, `session.State`, `AgentRunner`
- Produces: Extended `AgentRunner`, `agentCancel`, `cmdRegistry` fields, dispatch in `Update()`

- [ ] **Step 1: Add imports**

Add to the import block:
```go
	"marshal/internal/commands"
	"marshal/internal/llm/routing"
```

- [ ] **Step 2: Extend AgentRunner interface**

Change the `AgentRunner` interface (currently just `Run(ctx context.Context, goal string) error`) to:

```go
type AgentRunner interface {
	Run(ctx context.Context, goal string) error
	SetForceClass(class string)
}
```

- [ ] **Step 3: Add new fields to Model struct**

Add after `memoryProject int64` field:

```go
	cmdRegistry *commands.Registry
	agentCancel context.CancelFunc
	forceMode   string
```

- [ ] **Step 4: Add WithCommandRegistry option**

Add after the existing `WithConfigReloader`:

```go
func WithCommandRegistry(reg *commands.Registry) Option {
	return func(m *Model) {
		m.cmdRegistry = reg
	}
}
```

- [ ] **Step 5: Add command dispatch in Update()'s Enter handler**

Replace the Enter key handler block (lines 393-405) with:

```go
			case tea.KeyEnter:
				value := strings.TrimSpace(m.input.Value())
				if value == "" {
					return m, nil
				}
				m.input.Reset()

				if strings.HasPrefix(value, "/") {
					return m.dispatchCommand(value)
				}

				if m.busy {
					return m, nil
				}
				if m.runner == nil {
					m.state.AddMessage(session.RoleUser, value)
					m.refreshViewport()
					return m, nil
				}
				m.busy = true
				agentCtx, cancel := context.WithCancel(m.ctx)
				m.agentCancel = cancel
				return m, tea.Batch(runAgentCmd(agentCtx, m.runner, value), tickCmd())
```

- [ ] **Step 6: Add dispatchCommand method**

Add as a new method on Model (after `runAgentCmd`/`tickCmd`):

```go
func (m *Model) dispatchCommand(raw string) (tea.Model, tea.Cmd) {
	parts := strings.Fields(raw)
	if len(parts) == 0 {
		return m, nil
	}
	name := strings.TrimPrefix(parts[0], "/")
	var args []string
	if len(parts) > 1 {
		args = parts[1:]
	}

	cmd, ok := m.cmdRegistry.Lookup(name)
	if !ok {
		m.state.AddMessage(session.RoleSystem, fmt.Sprintf("Unknown command: /%s. Type /help for available commands.", name))
		m.refreshViewport()
		return m, nil
	}

	msg := cmd.Handler(m.state, args)

	if msg != "" {
		m.state.AddMessage(session.RoleSystem, msg)
	}

	switch cmd.Name {
	case "exit", "quit":
		m.state.Shutdown()
		return m, tea.Quit

	case "settings":
		m.settingsModel = settings.New(m.state.Config, m.state.WorkingDir, projectConfigPath(m.state.WorkingDir))
		m.settingsModel.SetSize(m.width, m.height)
		m.settingsOpen = true
		m.refreshViewport()
		return m, nil

	case "memory":
		if m.memoryDB == nil {
			m.state.AddMessage(session.RoleSystem, "Memory browser not available (no database configured).")
			m.refreshViewport()
			return m, nil
		}
		m.memoryModel = memory.New(m.memoryDB, m.memoryProject)
		m.memoryModel.SetSize(m.width, m.height)
		m.memoryOpen = true
		m.refreshViewport()
		return m, nil

	case "stop":
		if m.agentCancel != nil {
			m.agentCancel()
			m.agentCancel = nil
			m.state.AddMessage(session.RoleSystem, "Agent turn cancelled.")
		}
		m.refreshViewport()
		return m, nil

	case "ask":
		if m.runner != nil {
			m.runner.SetForceClass("question")
		}
		m.forceMode = "ask"
		m.refreshViewport()
		return m, nil

	case "edit":
		if m.runner != nil {
			m.runner.SetForceClass("edit")
		}
		m.forceMode = "edit"
		m.refreshViewport()
		return m, nil

	case "auto":
		if m.runner != nil {
			m.runner.SetForceClass("")
		}
		m.forceMode = ""
		m.refreshViewport()
		return m, nil

	case "model":
		if len(args) == 0 {
			m.state.AddMessage(session.RoleSystem, "Usage: /model <preset-name>. Available presets are listed in your config.toml.")
			m.refreshViewport()
			return m, nil
		}
		if m.configReloader != nil {
			presetName := args[0]
			newCfg := m.state.Config
			preset, ok := newCfg.Routing.Presets[presetName]
			if !ok {
				m.state.AddMessage(session.RoleSystem, fmt.Sprintf("Unknown preset: %s", presetName))
				m.refreshViewport()
				return m, nil
			}
			newCfg.Routing.DefaultProfile = "switched"
			newCfg.Routing.Profiles = map[string]routing.AgentProfile{
				"switched": {
					Name: "switched",
					Roles: map[routing.AgentRole]string{
						routing.RoleImplementer: presetName,
						routing.RoleRepoScout:   presetName,
						routing.RoleKnowledge:   presetName,
					},
				},
			}
			if err := m.configReloader(newCfg); err != nil {
				m.state.AddMessage(session.RoleSystem, fmt.Sprintf("Failed to switch model: %v", err))
			} else {
				m.state.AddMessage(session.RoleSystem, fmt.Sprintf("Switched to model: %s (%s)", presetName, preset.Model))
			}
		}
		m.refreshViewport()
		return m, nil

	default:
		m.refreshViewport()
		return m, nil
	}
}
```

- [ ] **Step 7: Reset agentCancel on agent finish**

In the `agentFinishedMsg` handler, add `m.agentCancel = nil`:

```go
	case agentFinishedMsg:
		m.busy = false
		m.agentCancel = nil
		if msg.err != nil {
			m.state.SetProviderError(msg.err)
		}
		m.refreshViewport()
		return m, nil
```

- [ ] **Step 8: Run build**

Run: `go build ./internal/app/tui/`
Expected: no errors

- [ ] **Step 9: Commit**

```bash
git add internal/app/tui/model.go
git commit -m "feat: add command dispatch and agent cancel to TUI"
```

---

### Task 6: Wire Command Registry in App

**Files:**
- Modify: `internal/app/app.go`

- [ ] **Step 1: Add import**

Add to imports:
```go
	"marshal/internal/commands"
```

- [ ] **Step 2: Create and register commands**

After `reg` (tool registry) is created and `native.RegisterAll` is called, and right before `tuiOpts` are built, add:

```go
	cmdReg := commands.New()
	if err := commands.RegisterAll(cmdReg, reg, cfg.Project.Name); err != nil {
		return fmt.Errorf("register commands: %w", err)
	}
```

- [ ] **Step 3: Pass command registry to TUI**

Add to `tuiOpts`:

```go
	tuiOpts = append(tuiOpts, tui.WithCommandRegistry(cmdReg))
```

- [ ] **Step 4: Run full build**

Run: `go build ./cmd/marshal/`
Expected: no errors

- [ ] **Step 5: Commit**

```bash
git add internal/app/app.go
git commit -m "feat: wire command registry into app"
```

---

### Task 7: Write Tests

**Files:**
- Create: `internal/commands/commands_test.go`
- Modify: `internal/app/tui/model_test.go`

- [ ] **Step 1: Write command registry tests**

```go
package commands

import (
	"errors"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
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
	err := reg.Register(Command{Name: "test", Handler: func(s *session.State, a []string) string { return "" }})
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}
}

func TestRegisterDuplicate(t *testing.T) {
	reg := New()
	h := func(s *session.State, a []string) string { return "" }
	reg.Register(Command{Name: "test", Handler: h})
	err := reg.Register(Command{Name: "test", Handler: h})
	if !errors.Is(err, ErrDuplicateCommand) {
		t.Errorf("expected ErrDuplicateCommand, got %v", err)
	}
}

func TestRegisterEmptyName(t *testing.T) {
	reg := New()
	err := reg.Register(Command{Name: "", Handler: func(s *session.State, a []string) string { return "" }})
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
	reg.Register(Command{Name: "test", Description: "desc", Handler: func(s *session.State, a []string) string { return "ok" }})

	cmd, ok := reg.Lookup("test")
	if !ok {
		t.Fatal("Lookup() not found")
	}
	if cmd.Description != "desc" {
		t.Errorf("expected desc, got %s", cmd.Description)
	}
	result := cmd.Handler(newTestState(), nil)
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
	reg.Register(Command{Name: "b", Handler: func(s *session.State, a []string) string { return "" }})
	reg.Register(Command{Name: "a", Handler: func(s *session.State, a []string) string { return "" }})

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
	err := RegisterAll(cmdReg, toolReg, "test-project")
	if err != nil {
		t.Fatalf("RegisterAll() error = %v", err)
	}
	cmds := cmdReg.List()
	if len(cmds) < 10 {
		t.Errorf("expected at least 10 commands, got %d", len(cmds))
	}
}

func TestHelpCommand(t *testing.T) {
	cmdReg := New()
	toolReg := registry.New()
	RegisterAll(cmdReg, toolReg, "test")

	cmd, _ := cmdReg.Lookup("help")
	result := cmd.Handler(newTestState(), nil)
	if !strings.Contains(result, "Available commands") {
		t.Errorf("help output missing header: %s", result)
	}
}

func TestToolsCommand(t *testing.T) {
	cmdReg := New()
	toolReg := registry.New()
	RegisterAll(cmdReg, toolReg, "test")

	cmd, _ := cmdReg.Lookup("tools")
	result := cmd.Handler(newTestState(), nil)
	if !strings.Contains(result, "Available tools") {
		t.Errorf("tools output missing header: %s", result)
	}
}

func TestRouteCommand(t *testing.T) {
	cmdReg := New()
	toolReg := registry.New()
	RegisterAll(cmdReg, toolReg, "test")

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
	result := cmd.Handler(state, nil)
	if !strings.Contains(result, "gpt-4") {
		t.Errorf("route output missing model info: %s", result)
	}
}

func TestContextCommand(t *testing.T) {
	cmdReg := New()
	toolReg := registry.New()
	RegisterAll(cmdReg, toolReg, "test")

	state := newTestState()
	state.AddMessage(session.RoleUser, "hello")
	state.AddMessage(session.RoleAssistant, "hi there")

	cmd, _ := cmdReg.Lookup("context")
	result := cmd.Handler(state, nil)
	if !strings.Contains(result, "Messages: 2") {
		t.Errorf("context output missing message count: %s", result)
	}
}

func TestNewCommand(t *testing.T) {
	cmdReg := New()
	toolReg := registry.New()
	RegisterAll(cmdReg, toolReg, "test")

	state := newTestState()
	state.AddMessage(session.RoleUser, "hello")
	state.AddMessage(session.RoleAssistant, "hi")

	cmd, _ := cmdReg.Lookup("new")
	result := cmd.Handler(state, nil)
	if !strings.Contains(result, "Cleared 2 messages") {
		t.Errorf("new command output wrong: %s", result)
	}
	if len(state.Messages()) != 0 {
		t.Errorf("expected 0 messages after clear, got %d", len(state.Messages()))
	}
}

func TestConfigCommand(t *testing.T) {
	cmdReg := New()
	toolReg := registry.New()
	RegisterAll(cmdReg, toolReg, "test")

	state := newTestState()
	cmd, _ := cmdReg.Lookup("config")
	result := cmd.Handler(state, nil)
	if !strings.Contains(result, "Configuration") {
		t.Errorf("config output missing header: %s", result)
	}
}

func TestRollbackNoBackup(t *testing.T) {
	cmdReg := New()
	toolReg := registry.New()
	RegisterAll(cmdReg, toolReg, "test")

	state := newTestState()
	cmd, _ := cmdReg.Lookup("rollback")
	result := cmd.Handler(state, nil)
	if !strings.Contains(result, "No backup available") {
		t.Errorf("rollback output wrong: %s", result)
	}
}

func TestExitQuitCommands(t *testing.T) {
	cmdReg := New()
	toolReg := registry.New()
	RegisterAll(cmdReg, toolReg, "test")

	state := newTestState()
	cmd, _ := cmdReg.Lookup("exit")
	result := cmd.Handler(state, nil)
	if result != "Goodbye!" {
		t.Errorf("expected Goodbye!, got %s", result)
	}

	cmd, _ = cmdReg.Lookup("quit")
	result = cmd.Handler(state, nil)
	if result != "Goodbye!" {
		t.Errorf("expected Goodbye!, got %s", result)
	}
}

func TestModeSwitchCommands(t *testing.T) {
	cmdReg := New()
	toolReg := registry.New()
	RegisterAll(cmdReg, toolReg, "test")

	state := newTestState()
	cmd, _ := cmdReg.Lookup("ask")
	result := cmd.Handler(state, nil)
	if !strings.Contains(result, "Ask mode") {
		t.Errorf("ask output wrong: %s", result)
	}

	cmd, _ = cmdReg.Lookup("edit")
	result = cmd.Handler(state, nil)
	if !strings.Contains(result, "Edit mode") {
		t.Errorf("edit output wrong: %s", result)
	}

	cmd, _ = cmdReg.Lookup("auto")
	result = cmd.Handler(state, nil)
	if !strings.Contains(result, "Auto mode") {
		t.Errorf("auto output wrong: %s", result)
	}
}

func TestStopCommand(t *testing.T) {
	cmdReg := New()
	toolReg := registry.New()
	RegisterAll(cmdReg, toolReg, "test")

	state := newTestState()
	cmd, _ := cmdReg.Lookup("stop")
	result := cmd.Handler(state, nil)
	if result != "" {
		t.Errorf("stop handler should return empty string, got %s", result)
	}
}

func TestModelCommandEmptyArgs(t *testing.T) {
	cmdReg := New()
	toolReg := registry.New()
	RegisterAll(cmdReg, toolReg, "test")

	state := newTestState()
	cmd, _ := cmdReg.Lookup("model")
	result := cmd.Handler(state, nil)
	if result != "" {
		t.Errorf("model handler should return empty string when no args, got %s", result)
	}
}
```

- [ ] **Step 2: Write TUI command dispatch tests in model_test.go**

Add to `model_test.go`:

```go
func TestSlashCommandExit(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	cmdReg := setupCmdReg(t)
	model := New(state, WithCommandRegistry(cmdReg))
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	model = updated.(Model)

	model.input.SetValue("/exit")
	_, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})

	if cmd == nil {
		t.Error("expected quit command from /exit, got nil")
	}
}

func TestSlashCommandHelp(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	cmdReg := setupCmdReg(t)
	model := New(state, WithCommandRegistry(cmdReg))
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	model = updated.(Model)

	model.input.SetValue("/help")
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)

	msgs := model.state.Messages()
	if len(msgs) == 0 {
		t.Fatal("expected system message from /help")
	}
	if !strings.Contains(msgs[0].Content, "Available commands") {
		t.Errorf("help output missing header: %s", msgs[0].Content)
	}
}

func TestSlashCommandUnknown(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	cmdReg := setupCmdReg(t)
	model := New(state, WithCommandRegistry(cmdReg))
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	model = updated.(Model)

	model.input.SetValue("/nonexistent")
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)

	msgs := model.state.Messages()
	if len(msgs) == 0 {
		t.Fatal("expected error message for unknown command")
	}
	if !strings.Contains(msgs[0].Content, "Unknown command") {
		t.Errorf("expected unknown command message, got: %s", msgs[0].Content)
	}
}

func TestSlashCommandNotSentToAgent(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	cmdReg := setupCmdReg(t)
	runner := &fakeAgentRunner{called: make(chan string, 1)}
	model := New(state, WithCommandRegistry(cmdReg), WithRunner(context.Background(), runner))
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	model = updated.(Model)

	model.input.SetValue("/help")
	model.Update(tea.KeyMsg{Type: tea.KeyEnter})

	select {
	case <-runner.called:
		t.Error("/help should not be sent to agent runner")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestSlashCommandClearMessages(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	cmdReg := setupCmdReg(t)
	model := New(state, WithCommandRegistry(cmdReg))
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	model = updated.(Model)

	model.state.AddMessage(session.RoleUser, "hello")
	model.input.SetValue("/new")
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)

	if len(model.state.Messages()) != 0 {
		t.Errorf("expected 0 messages after /new, got %d", len(model.state.Messages()))
	}
}

func TestSlashCommandBusyStillDispatched(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	cmdReg := setupCmdReg(t)
	model := New(state, WithCommandRegistry(cmdReg))
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	model = updated.(Model)
	model.busy = true

	model.input.SetValue("/help")
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)

	msgs := model.state.Messages()
	if len(msgs) == 0 {
		t.Fatal("commands should work even when busy")
	}
}

func setupCmdReg(t *testing.T) *commands.Registry {
	t.Helper()
	cmdReg := commands.New()
	if err := commands.RegisterAll(cmdReg, registry.New(), "test"); err != nil {
		t.Fatalf("RegisterAll() error = %v", err)
	}
	return cmdReg
}
```

Add imports to model_test.go if not present:
```go
	"marshal/internal/commands"
	"marshal/internal/tools/registry"
```

Update fakeAgentRunner to satisfy the extended AgentRunner interface:
```go
type fakeAgentRunner struct {
	called chan string
	err    error
}

func (f *fakeAgentRunner) Run(ctx context.Context, goal string) error {
	f.called <- goal
	return f.err
}

func (f *fakeAgentRunner) SetForceClass(class string) {}
```

- [ ] **Step 3: Run command tests**

Run: `go test ./internal/commands/ -v`
Expected: all pass

- [ ] **Step 4: Run TUI slash command tests**

Run: `go test ./internal/app/tui/ -v -run TestSlash`
Expected: 6 tests pass

- [ ] **Step 5: Run all tests**

Run: `go test ./...`
Expected: all pass

- [ ] **Step 6: Commit**

```bash
git add internal/commands/commands_test.go internal/app/tui/model_test.go
git commit -m "test: add tests for slash commands and TUI dispatch"
```

---

### Task 8: Final Verification

- [ ] **Step 1: Build the full binary**

Run: `go build ./cmd/marshal/`
Expected: no errors

- [ ] **Step 2: Run vet**

Run: `go vet ./...`
Expected: no warnings

- [ ] **Step 3: Run all tests final pass**

Run: `go test ./...`
Expected: all pass

- [ ] **Step 4: Commit**

```bash
git commit -m "chore: final verification for slash commands" --allow-empty
```

---

## Command Reference

| Command | Args | Description | Handler Location |
|---------|------|-------------|-----------------|
| `/exit` | — | Exit Marshal | `commands.go` |
| `/quit` | — | Alias for /exit | `commands.go` |
| `/new` | — | Start new conversation, clear all messages | `commands.go` |
| `/clear` | — | Alias for /new | `commands.go` |
| `/help` | — | Show all available commands | `commands.go` |
| `/tools` | — | List all registered tools with risk levels | `commands.go` |
| `/route` | — | Show current model, provider, role, preset | `commands.go` |
| `/context` | — | Show message count, total chars, context pack size | `commands.go` |
| `/stop` | — | Cancel the currently running agent turn | TUI dispatch |
| `/ask` | — | Switch to Ask mode (read-only, no planning) | TUI dispatch |
| `/edit` | — | Switch to Edit mode (planning + full tools) | TUI dispatch |
| `/auto` | — | Switch to Auto mode (classify each turn) | TUI dispatch |
| `/model` | `<preset>` | Switch to a model preset from config | TUI dispatch |
| `/config` | — | Show configuration summary | `commands.go` |
| `/settings` | — | Open settings panel (Ctrl+O to close) | TUI dispatch |
| `/memory` | — | Open memory browser (Ctrl+K to close) | TUI dispatch |
| `/rollback` | — | Rollback last patch to original state | `commands.go` |

## Future Commands (Phase 2)

| Command | Description |
|---------|-------------|
| `/diff` | Show git working tree diff (submit to agent) |
| `/status` | Show git status (submit to agent) |
| `/commit <msg>` | Commit changes with message |
| `/search <query>` | Search codebase |
| `/file <path>` | Read and display a file |
| `/retry` | Retry the last agent turn |
