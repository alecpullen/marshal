# Milestone F: Approval System Design

## Goal

Milestone F adds Marshal's interactive security layer and safety policy engine. This ensures the agent cannot execute arbitrary shell commands, destructive operations, or unauthorized file edits without matching safe rules or obtaining explicit user confirmation in the TUI.

Specifically, it implements:
- **Config Allow/Confirm/Deny rules** for shell execution.
- **Command Risk Classifier & Policy Engine** to evaluate proposed tool calls.
- **Per-Session Allow list** for runtime approvals.
- **Interactive TUI Approval Screen** supporting Approve, Deny, Edit, and Always Allow (Session) actions.
- **Persistent Tool Call Logging** using the `AuditEvent` and a registry/session logging flow.

## Design Architecture

The approval flow bridges tool registration, session state management, and the Bubble Tea TUI.

```mermaid
graph TD
    A[Agent Runtime] -->|1. Request Tool Execution| B[Policy Engine]
    B -->|2. Check Rules & Risk| C{Decision}
    
    C -->|DecisionAllow| D[Execute Tool Handler]
    C -->|DecisionDeny| E[Return Safety Error to Agent]
    C -->|DecisionConfirm| F[Set PendingToolCall in Session]
    
    F -->|3. Trigger TUI Render| G[Show TUI Approval Mode]
    G -->|4. User Key Press| H{User Action}
    
    H -->|Approve [Enter]| D
    H -->|Deny [d]| E
    H -->|Always Allow [a]| I[Add Prefix to SessionRules] --> D
    H -->|Edit [e]| J[Edit Command Line] --> D
```

---

## 1. Config Extensions & Policy Engine

### Configuration Structure (`internal/app/config/config.go`)
Extend the global configuration to support rules mapping:

```toml
[tools.shell]
default_timeout_seconds = 120
max_output_bytes = 200000
allow_network = false
allow_sudo = false
allow_destructive = false
auto_approve = false # Set to true for autonomous low-risk runs

[tools.shell.allow]
commands = [
  "go test",
  "git status",
  "git diff"
]

[tools.shell.confirm]
commands = [
  "go get",
  "npm install"
]

[tools.shell.deny]
patterns = [
  "rm -rf",
  "sudo",
  "curl * | sh"
]
```

These settings map directly into Go structs in the `config` package:
- `ToolsConfig` wrapping `ShellToolConfig`
- `ShellToolConfig` holding `Allow`, `Confirm`, and `Deny` rules fields.

### The Policy Engine (`internal/tools/policy`)
Create `internal/tools/policy/policy.go` containing:
- `Decision` string type (`DecisionAllow`, `DecisionConfirm`, `DecisionDeny`).
- `PolicyEngine` matching engine:
  ```go
  type PolicyEngine struct {
      config *config.Config
  }
  ```
- Methods:
  - `Evaluate(toolName string, args map[string]interface{}, sessionRules []string) (Decision, string, error)`: Determines how the tool call should be handled.
  - `MatchRule(command string, rules []string) bool`: Evaluates prefix-based matching on word boundaries.
  - `MatchPattern(command string, patterns []string) bool`: Evaluates glob/substring matching.

### Safety Checks and Guardrails
1. **Conservative Guardrails**: Hardcoded checks inside `policy.go` will automatically return `DecisionDeny` for commands containing `sudo`, `rm -rf`, `git reset --hard`, `git clean -fd`, etc., or piped network installers.
2. **Deny Lists**: Any command matching `tools.shell.deny` patterns returns `DecisionDeny`.
3. **Session Rules**: Any command whose prefix matches a string in the current session's allow list returns `DecisionAllow`.
4. **Allow Lists**: Any command whose prefix matches an entry in `tools.shell.allow` returns `DecisionAllow`.
5. **Confirm Lists**: Any command whose prefix matches `tools.shell.confirm` returns `DecisionConfirm`.
6. **Fallback**: Default to `DecisionConfirm` unless `auto_approve` is `true`.

---

## 2. Session State Integration (`internal/app/session`)

The `session.State` struct manages the active conversation, workspace root, configuration, and logs. It will be extended to coordinate:

- **Session-level Rules**:
  A slice of approved command prefixes (e.g. `[]string{"go test"}`) that are reset when the CLI application exits.
- **Pending Tool Calls**:
  ```go
  type PendingToolCall struct {
      ID      string
      Name    string
      Args    string // Raw JSON args (e.g. {"command": "go test ./..."})
      Command string // Extracted target command if shell.run/test.run
      Risk    string // Risk level name
      Reason  string // Context explanation
  }
  ```
  When a tool call is intercepted and requires user validation, `State.SetPendingApproval(tc)` suspends execution.

---

## 3. TUI Approval Prompt (`internal/app/tui`)

When `session.State.PendingApproval()` is not `nil`, the TUI shifts into **Approval Mode**.

### Layout & UI Spec
Instead of focusing the user input field, the bottom pane displays:
```text
┌── SECURITY APPROVAL REQUIRED ───────────────────────────────────────────┐
│ Agent wants to run:                                                     │
│   go test ./...                                                         │
│                                                                         │
│ Reason:                                                                 │
│   Validate the package parser after changes.                            │
│                                                                         │
│ Risk Level: Command (Low-risk pattern)                                  │
└─────────────────────────────────────────────────────────────────────────┘
[Enter] Approve  [d] Deny  [e] Edit  [a] Always Allow in this Session
```

### Keyboard Actions
- **`Enter` (Approve)**: Unblocks execution, returning approval state as `approved`.
- **`d` (Deny)**: Returns `denied` state. The tool call fails immediately.
- **`a` (Always Allow)**: Dynamically appends the command prefix to `SessionRules` and auto-approves execution.
- **`e` (Edit)**: Focuses the text input field loaded with the proposed command. The user can modify the string and press `Enter` to run the edited version.

---

## 4. Auditing & Log Persistence

Every tool call execution—regardless of approval state—is logged in memory (or using the `AuditEvent` structure defined in Milestone D).
- Recorded fields: Timestamp, ToolName, Args, Risk, Approval, ResultSummary, ExitCode, and any Error.
- Log entries are rendered in the TUI's "Tool Log" section for inspection.

---

## Testing Strategy

- **Policy Unit Tests (`internal/tools/policy/policy_test.go`)**:
  - Test prefix matching boundaries (e.g., config `"go test"` matches `"go test ./..."` but not `"go test-helper"`).
  - Verify blocked commands (guardrails + deny-list) return `DecisionDeny`.
  - Verify session rules trigger `DecisionAllow`.
  - Validate default fallback with `auto_approve = true` vs `false`.
- **TUI Model Tests (`internal/app/tui/model_test.go`)**:
  - Verify that presence of a pending tool call shifts rendering to the approval modal.
  - Test transitions for `Enter`, `d`, `a`, and `e` inputs.
- **Config Merge Tests (`internal/app/config/config_test.go`)**:
  - Test correct TOML parsing and merging of shell rules sections.
