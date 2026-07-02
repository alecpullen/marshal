# Milestone F Approval System Implementation Plan

> **For Antigravity:** REQUIRED WORKFLOW: Use `.agent/workflows/execute-plan.md` to execute this plan in single-flow mode.

**Goal:** Implement Marshal's safety policy engine, TUI interactive approvals, session rules tracking, and auditing logs to ensure no unsafe command or tool call runs without matching rules or explicit approval.

**Architecture:** Extend the configuration structs for shell rules. Create a new `internal/tools/policy` package that evaluates tool calls against hardcoded guardrails, config lists, and session-level overrides. Integrate pending approval state into the TUI layout to capture user keybindings (`Enter`, `d`, `a`, `e`), updating the runner loop accordingly.

**Tech Stack:** Go 1.26.1, standard library only, Bubble Tea (`github.com/charmbracelet/bubbletea` and `github.com/charmbracelet/bubbles`).

---

## Global Constraints

- Do not add external third-party dependencies outside the standard library and Bubble Tea.
- Keep the security logic decoupled from TUI rendering so it remains fully unit-testable.
- Use TDD for every behavior change: write failing tests first, verify failure, implement, verify pass.
- Run `gofmt` on all modified/created Go files before committing.
- Keep `CLAUDE.md` untracked and untouched.

---

## File Structure

- Create `internal/tools/policy/policy.go`
  Defines `Decision`, `PolicyEngine`, rules evaluation logic (prefix and glob matching), and hardcoded guardrails.
- Create `internal/tools/policy/policy_test.go`
  Tests all rule-matching scenarios, guardrails, session policies, and fallbacks.
- Modify `internal/app/config/config.go`
  Add structure for `[tools.shell]` rules mapping, parsing, and merging.
- Modify `internal/app/config/config_test.go`
  Test merging of global and local config policy files.
- Modify `internal/app/session/session.go`
  Add fields and methods for `PendingToolCall` and session allowlist rules.
- Modify `internal/app/session/session_test.go`
  Test thread-safe setting/getting of pending approvals and session rules.
- Modify `internal/app/tui/model.go`
  Add conditional rendering for the security banner and custom key handlers for `Enter`, `d`, `a`, `e`.
- Modify `internal/app/tui/model_test.go`
  Test keypress transitions and dynamic rendering of the approval layout.
- Modify `docs/10-mvp-implementation-checklist.md`
  Mark Milestone F complete after verification.

---

### Task 1: Policy Engine Foundation and Rules Matching

**Files:**
- Create: `internal/tools/policy/policy.go`
- Create: `internal/tools/policy/policy_test.go`

- [ ] **Step 1: Create policy directory**
Run:
```bash
mkdir -p internal/tools/policy
```
Expected: directory exists.

- [ ] **Step 2: Write failing unit tests for PolicyEngine**
Create `internal/tools/policy/policy_test.go`:
```go
package policy

import (
	"testing"
	"marshal/internal/app/config"
)

func TestPolicyEngine_Evaluate_Guardrails(t *testing.T) {
	pe := NewEngine(&config.Config{}, []string{})
	
	// Test blocked command
	dec, reason, err := pe.Evaluate("shell.run", map[string]interface{}{"command": "rm -rf /"})
	if err != nil {
		t.Fatalf("Evaluate error: %v", err)
	}
	if dec != DecisionDeny {
		t.Errorf("got %v, want DecisionDeny", dec)
	}
	if reason == "" {
		t.Error("expected non-empty reason")
	}
}
```

- [ ] **Step 3: Write minimal implementation for PolicyEngine**
Create `internal/tools/policy/policy.go`:
```go
package policy

import (
	"errors"
	"fmt"
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
	config *config.Config
}

func NewEngine(cfg *config.Config, sessionRules []string) *PolicyEngine {
	return &PolicyEngine{config: cfg}
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
	cmd = strings.ToLower(strings.TrimSpace(cmd))
	if strings.Contains(cmd, "rm -rf") {
		return DecisionDeny, "blocked by conservative guardrail: rm -rf", nil
	}
	return DecisionConfirm, "requires approval", nil
}
```

- [ ] **Step 4: Run tests to verify pass**
Run: `go test ./internal/tools/policy`
Expected: PASS

- [ ] **Step 5: Add detailed matching logic and tests**
Expand `internal/tools/policy/policy_test.go` and `policy.go` to support:
- `MatchRule` (prefix matching with word boundaries).
- `MatchPattern` (glob matching/substring matching).
- Checking session-level rules.
- Merging in `allow`, `confirm`, `deny` configs.
- Run tests and commit.

---

### Task 2: Config Rules Schema Integration

**Files:**
- Modify: `internal/app/config/config.go`
- Modify: `internal/app/config/config_test.go`

- [ ] **Step 1: Add failing test for config parsing**
Write a test in `internal/app/config/config_test.go` that defines mock TOML files containing `[tools.shell.allow]`, `confirm`, `deny` and checks that they parser correctly.
Run `go test ./internal/app/config` and verify failure.

- [ ] **Step 2: Add Go structs and parser mappings**
Update `internal/app/config/config.go` with the `ToolsConfig`, `ShellToolConfig`, `CommandRules`, and `PatternRules` fields. Populate `Default()` with sensible fallback configurations. Update `merge()` to safely combine tools sections.
Run `go test ./internal/app/config` and verify pass.

- [ ] **Step 3: Commit**
```bash
git add internal/app/config/
git commit -m "feat: implement shell rules configuration parsing"
```

---

### Task 3: Session State Extensions

**Files:**
- Modify: `internal/app/session/session.go`
- Modify: `internal/app/session/session_test.go`

- [ ] **Step 1: Write failing tests for pending approval and session rules**
Update `internal/app/session/session_test.go` to test:
- Setting/Getting `PendingToolCall`.
- Adding and checking prefix-based `SessionRules` thread-safely.
Run `go test ./internal/app/session` to verify failures.

- [ ] **Step 2: Implement session state extensions**
Add thread-safe properties `pendingApproval` and `sessionRules` to `session.State` with corresponding locks, getters, and setters.
Run `go test ./internal/app/session` to verify pass.

- [ ] **Step 3: Commit**
```bash
git add internal/app/session/
git commit -m "feat: add pending tool call and session rules to state"
```

---

### Task 4: Interactive TUI Approval Pane

**Files:**
- Modify: `internal/app/tui/model.go`
- Modify: `internal/app/tui/model_test.go`

- [ ] **Step 1: Add failing test for approval banner rendering**
In `internal/app/tui/model_test.go`, write a test where `session.State` has a mock `PendingToolCall` set. Call `model.View()` and assert that the rendering output contains the warning banner and instructions.
Run `go test ./internal/app/tui` and verify failure.

- [ ] **Step 2: Implement TUI rendering and keybindings**
Update `internal/app/tui/model.go`'s `View` method to conditionally render the security approval UI when `m.state.PendingApproval()` is not nil.
Update `Update()` keymsg switch to handle keypresses:
- `Enter` -> approve (returns tea.Cmd to trigger callback or channel write).
- `d` -> deny.
- `a` -> always allow (session rules write).
- `e` -> edit mode (focus text input box).
Run tests to make sure it compiles and passes.

- [ ] **Step 3: Commit**
```bash
git add internal/app/tui/
git commit -m "feat: implement interactive TUI security approval pane"
```

---

### Task 5: Final Check and Verification

- [ ] **Step 1: Check off Milestone F items**
Mark items under `## Milestone F: Approval system` as complete in `docs/10-mvp-implementation-checklist.md`.

- [ ] **Step 2: Verify whole project compiles and passes tests**
Run:
```bash
go test ./...
go vet ./...
```
Expected: all packages pass tests and vet cleanly.

- [ ] **Step 3: Final Commit**
```bash
git add docs/10-mvp-implementation-checklist.md
git commit -m "docs: mark milestone f complete"
```
