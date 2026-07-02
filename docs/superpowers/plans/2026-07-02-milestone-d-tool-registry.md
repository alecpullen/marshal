# Milestone D Tool Registry Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build Marshal's framework-only tool registry with stable tool contracts, risk levels, lookup, argument validation placeholder, and audit event construction.

**Architecture:** Add a single `internal/tools/registry` package. The package owns contracts and registry behavior only; concrete native tools, execution broker, approvals, and persistence remain out of scope for Milestone D.

**Tech Stack:** Go 1.26.1, standard library only (`context`, `encoding/json`, `errors`, `fmt`, `sort`, `strings`, `time`, `testing`).

## Global Constraints

- Scope is pure framework only: do not implement `file.read`, `repo.search`, `git.status`, `git.diff`, `shell.run`, `test.run`, or any other concrete tool.
- Do not execute registered handlers through a broker in this milestone.
- Do not wire tools into `internal/app/tui`, `internal/app/session`, `internal/agent`, `internal/llm`, or `internal/db`.
- Do not implement full JSON Schema validation; only validate schema syntax at registration and JSON-object args at call validation.
- Do not add third-party dependencies.
- Use TDD for every behavior change: write failing tests first, verify failure, implement, verify pass.
- Keep `CLAUDE.md` untracked and untouched.
- Run `gofmt` on all created Go files before committing.

---

## File Structure

- Create `internal/tools/registry/types.go`
  Defines `RiskLevel`, `Tool`, `ToolCall`, `ToolHandler`, and `ToolResult`.
- Create `internal/tools/registry/registry.go`
  Defines `Registry`, registration validation, lookup, and sorted listing.
- Create `internal/tools/registry/validation.go`
  Defines `ValidateArgs`.
- Create `internal/tools/registry/audit.go`
  Defines `ApprovalState`, `AuditEvent`, and `NewAuditEvent`.
- Create `internal/tools/registry/registry_test.go`
  Tests risk validation, registration, lookup, and listing.
- Create `internal/tools/registry/validation_test.go`
  Tests argument validation placeholder behavior.
- Create `internal/tools/registry/audit_test.go`
  Tests audit event construction and copy semantics.
- Modify `docs/10-mvp-implementation-checklist.md`
  Mark Milestone D complete after implementation and verification.

---

### Task 1: Core Tool Contracts and Risk Validation

**Files:**
- Create: `internal/tools/registry/types.go`
- Create: `internal/tools/registry/registry_test.go`

**Interfaces:**
- Produces:
  - `type RiskLevel string`
  - `const RiskReadOnly RiskLevel = "read_only"`
  - `const RiskWorkspaceWrite RiskLevel = "workspace_write"`
  - `const RiskCommand RiskLevel = "command"`
  - `const RiskNetwork RiskLevel = "network"`
  - `const RiskDestructive RiskLevel = "destructive"`
  - `func (r RiskLevel) Valid() bool`
  - `type Tool struct`
  - `type ToolCall struct`
  - `type ToolResult struct`
  - `type ToolHandler func(ctx context.Context, call ToolCall) (ToolResult, error)`

- [ ] **Step 1: Create package directory**

Run:

```bash
mkdir -p internal/tools/registry
```

Expected: directory exists.

- [ ] **Step 2: Write failing tests for risk validation and exported contract usage**

Create `internal/tools/registry/registry_test.go`:

```go
package registry

import (
	"context"
	"testing"
)

func TestRiskLevelValidAcceptsDocumentedValues(t *testing.T) {
	for _, risk := range []RiskLevel{
		RiskReadOnly,
		RiskWorkspaceWrite,
		RiskCommand,
		RiskNetwork,
		RiskDestructive,
	} {
		if !risk.Valid() {
			t.Fatalf("%q Valid() = false, want true", risk)
		}
	}
}

func TestRiskLevelValidRejectsUnknownValue(t *testing.T) {
	if RiskLevel("surprise").Valid() {
		t.Fatal(`RiskLevel("surprise").Valid() = true, want false`)
	}
}

func TestToolHandlerSignature(t *testing.T) {
	handler := ToolHandler(func(ctx context.Context, call ToolCall) (ToolResult, error) {
		if call.Name != "example.tool" {
			t.Fatalf("call.Name = %q, want example.tool", call.Name)
		}
		return ToolResult{Summary: "ok"}, nil
	})

	result, err := handler(context.Background(), ToolCall{Name: "example.tool"})
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if result.Summary != "ok" {
		t.Fatalf("result.Summary = %q, want ok", result.Summary)
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run:

```bash
go test ./internal/tools/registry
```

Expected: FAIL with undefined symbols such as `RiskLevel`, `ToolHandler`, `ToolCall`, or `ToolResult`.

- [ ] **Step 4: Implement core types**

Create `internal/tools/registry/types.go`:

```go
package registry

import (
	"context"
	"encoding/json"
)

type RiskLevel string

const (
	RiskReadOnly       RiskLevel = "read_only"
	RiskWorkspaceWrite RiskLevel = "workspace_write"
	RiskCommand        RiskLevel = "command"
	RiskNetwork        RiskLevel = "network"
	RiskDestructive    RiskLevel = "destructive"
)

func (r RiskLevel) Valid() bool {
	switch r {
	case RiskReadOnly, RiskWorkspaceWrite, RiskCommand, RiskNetwork, RiskDestructive:
		return true
	default:
		return false
	}
}

type Tool struct {
	Name        string
	Description string
	Schema      json.RawMessage
	Risk        RiskLevel
	Handler     ToolHandler
}

type ToolCall struct {
	ID   string
	Name string
	Args json.RawMessage
}

type ToolHandler func(ctx context.Context, call ToolCall) (ToolResult, error)

type ToolResult struct {
	Summary         string
	Content         string
	FilesChanged    []string
	CommandExitCode *int
}
```

- [ ] **Step 5: Run tests to verify Task 1 passes**

Run:

```bash
go test ./internal/tools/registry
```

Expected: PASS.

- [ ] **Step 6: Format and commit Task 1**

Run:

```bash
gofmt -w internal/tools/registry/types.go internal/tools/registry/registry_test.go
go test ./internal/tools/registry
git add internal/tools/registry/types.go internal/tools/registry/registry_test.go
git commit -m "feat: add tool registry contracts"
```

Expected: commit succeeds.

---

### Task 2: Registry Registration, Lookup, and Listing

**Files:**
- Modify: `internal/tools/registry/registry_test.go`
- Create: `internal/tools/registry/registry.go`

**Interfaces:**
- Consumes:
  - `Tool`
  - `RiskLevel.Valid() bool`
- Produces:
  - `var ErrInvalidTool error`
  - `var ErrDuplicateTool error`
  - `func New() *Registry`
  - `func (r *Registry) Register(tool Tool) error`
  - `func (r *Registry) Lookup(name string) (Tool, bool)`
  - `func (r *Registry) List() []Tool`

- [ ] **Step 1: Add failing registry behavior tests**

Append to `internal/tools/registry/registry_test.go`:

```go
func TestRegisterAcceptsValidTool(t *testing.T) {
	reg := New()
	tool := testTool("example.beta")

	if err := reg.Register(tool); err != nil {
		t.Fatalf("Register returned error: %v", err)
	}

	got, ok := reg.Lookup("example.beta")
	if !ok {
		t.Fatal("Lookup returned ok=false")
	}
	if got.Name != "example.beta" {
		t.Fatalf("Lookup tool name = %q, want example.beta", got.Name)
	}
}

func TestRegisterRejectsEmptyName(t *testing.T) {
	reg := New()
	tool := testTool("   ")

	err := reg.Register(tool)
	if err == nil {
		t.Fatal("Register returned nil error, want invalid tool")
	}
	if !errors.Is(err, ErrInvalidTool) {
		t.Fatalf("Register error = %v, want ErrInvalidTool", err)
	}
}

func TestRegisterRejectsNilHandler(t *testing.T) {
	reg := New()
	tool := testTool("example.no_handler")
	tool.Handler = nil

	err := reg.Register(tool)
	if err == nil {
		t.Fatal("Register returned nil error, want invalid tool")
	}
	if !errors.Is(err, ErrInvalidTool) {
		t.Fatalf("Register error = %v, want ErrInvalidTool", err)
	}
}

func TestRegisterRejectsUnknownRisk(t *testing.T) {
	reg := New()
	tool := testTool("example.risky")
	tool.Risk = RiskLevel("unknown")

	err := reg.Register(tool)
	if err == nil {
		t.Fatal("Register returned nil error, want invalid tool")
	}
	if !errors.Is(err, ErrInvalidTool) {
		t.Fatalf("Register error = %v, want ErrInvalidTool", err)
	}
}

func TestRegisterRejectsInvalidSchemaJSON(t *testing.T) {
	reg := New()
	tool := testTool("example.bad_schema")
	tool.Schema = json.RawMessage(`{"type":`)

	err := reg.Register(tool)
	if err == nil {
		t.Fatal("Register returned nil error, want invalid tool")
	}
	if !errors.Is(err, ErrInvalidTool) {
		t.Fatalf("Register error = %v, want ErrInvalidTool", err)
	}
}

func TestRegisterRejectsDuplicateName(t *testing.T) {
	reg := New()

	if err := reg.Register(testTool("example.same")); err != nil {
		t.Fatalf("first Register returned error: %v", err)
	}
	err := reg.Register(testTool("example.same"))
	if err == nil {
		t.Fatal("second Register returned nil error, want duplicate")
	}
	if !errors.Is(err, ErrDuplicateTool) {
		t.Fatalf("Register error = %v, want ErrDuplicateTool", err)
	}
}

func TestLookupMissReturnsFalse(t *testing.T) {
	reg := New()

	_, ok := reg.Lookup("missing.tool")
	if ok {
		t.Fatal("Lookup ok = true, want false")
	}
}

func TestListReturnsToolsSortedByName(t *testing.T) {
	reg := New()
	for _, name := range []string{"example.zed", "example.alpha", "example.middle"} {
		if err := reg.Register(testTool(name)); err != nil {
			t.Fatalf("Register(%q): %v", name, err)
		}
	}

	got := reg.List()
	if len(got) != 3 {
		t.Fatalf("len(List()) = %d, want 3", len(got))
	}
	for i, want := range []string{"example.alpha", "example.middle", "example.zed"} {
		if got[i].Name != want {
			t.Fatalf("List()[%d].Name = %q, want %q", i, got[i].Name, want)
		}
	}
}

func testTool(name string) Tool {
	return Tool{
		Name:        name,
		Description: "test tool",
		Schema:      json.RawMessage(`{"type":"object"}`),
		Risk:        RiskReadOnly,
		Handler: func(ctx context.Context, call ToolCall) (ToolResult, error) {
			return ToolResult{Summary: "ok"}, nil
		},
	}
}
```

Update the imports in `registry_test.go` to:

```go
import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)
```

- [ ] **Step 2: Run tests to verify they fail**

Run:

```bash
go test ./internal/tools/registry
```

Expected: FAIL with undefined symbols such as `New`, `ErrInvalidTool`, or `ErrDuplicateTool`.

- [ ] **Step 3: Implement registry behavior**

Create `internal/tools/registry/registry.go`:

```go
package registry

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

var (
	ErrInvalidTool   = errors.New("invalid tool")
	ErrDuplicateTool = errors.New("duplicate tool")
)

type Registry struct {
	tools map[string]Tool
}

func New() *Registry {
	return &Registry{
		tools: make(map[string]Tool),
	}
}

func (r *Registry) Register(tool Tool) error {
	if strings.TrimSpace(tool.Name) == "" {
		return fmt.Errorf("%w: name is required", ErrInvalidTool)
	}
	if tool.Handler == nil {
		return fmt.Errorf("%w: handler is required for %q", ErrInvalidTool, tool.Name)
	}
	if !tool.Risk.Valid() {
		return fmt.Errorf("%w: unknown risk level %q for %q", ErrInvalidTool, tool.Risk, tool.Name)
	}
	if len(tool.Schema) > 0 && !json.Valid(tool.Schema) {
		return fmt.Errorf("%w: schema for %q is not valid JSON", ErrInvalidTool, tool.Name)
	}
	if _, exists := r.tools[tool.Name]; exists {
		return fmt.Errorf("%w: %q", ErrDuplicateTool, tool.Name)
	}

	r.tools[tool.Name] = tool
	return nil
}

func (r *Registry) Lookup(name string) (Tool, bool) {
	tool, ok := r.tools[name]
	return tool, ok
}

func (r *Registry) List() []Tool {
	tools := make([]Tool, 0, len(r.tools))
	for _, tool := range r.tools {
		tools = append(tools, tool)
	}
	sort.Slice(tools, func(i, j int) bool {
		return tools[i].Name < tools[j].Name
	})
	return tools
}
```

- [ ] **Step 4: Run tests to verify Task 2 passes**

Run:

```bash
gofmt -w internal/tools/registry/registry.go internal/tools/registry/registry_test.go
go test ./internal/tools/registry
```

Expected: PASS.

- [ ] **Step 5: Commit Task 2**

Run:

```bash
git add internal/tools/registry/registry.go internal/tools/registry/registry_test.go
git commit -m "feat: add tool registry lookup"
```

Expected: commit succeeds.

---

### Task 3: Schema Validation Placeholder

**Files:**
- Create: `internal/tools/registry/validation.go`
- Create: `internal/tools/registry/validation_test.go`

**Interfaces:**
- Consumes:
  - `Tool`
- Produces:
  - `var ErrInvalidArgs error`
  - `func ValidateArgs(tool Tool, args json.RawMessage) error`

- [ ] **Step 1: Write failing validation tests**

Create `internal/tools/registry/validation_test.go`:

```go
package registry

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestValidateArgsAcceptsEmptyArgs(t *testing.T) {
	if err := ValidateArgs(testTool("example.empty"), nil); err != nil {
		t.Fatalf("ValidateArgs returned error: %v", err)
	}
}

func TestValidateArgsAcceptsObjectArgs(t *testing.T) {
	args := json.RawMessage(`{"path":"README.md"}`)
	if err := ValidateArgs(testTool("example.object"), args); err != nil {
		t.Fatalf("ValidateArgs returned error: %v", err)
	}
}

func TestValidateArgsRejectsMalformedJSON(t *testing.T) {
	err := ValidateArgs(testTool("example.bad"), json.RawMessage(`{"path":`))
	if err == nil {
		t.Fatal("ValidateArgs returned nil error, want invalid args")
	}
	if !errors.Is(err, ErrInvalidArgs) {
		t.Fatalf("ValidateArgs error = %v, want ErrInvalidArgs", err)
	}
}

func TestValidateArgsRejectsNonObjectJSON(t *testing.T) {
	for _, args := range []json.RawMessage{
		json.RawMessage(`[]`),
		json.RawMessage(`"README.md"`),
		json.RawMessage(`true`),
	} {
		err := ValidateArgs(testTool("example.non_object"), args)
		if err == nil {
			t.Fatalf("ValidateArgs(%s) returned nil error, want invalid args", args)
		}
		if !errors.Is(err, ErrInvalidArgs) {
			t.Fatalf("ValidateArgs(%s) error = %v, want ErrInvalidArgs", args, err)
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run:

```bash
go test ./internal/tools/registry
```

Expected: FAIL with undefined symbols `ValidateArgs` and `ErrInvalidArgs`.

- [ ] **Step 3: Implement validation placeholder**

Create `internal/tools/registry/validation.go`:

```go
package registry

import (
	"encoding/json"
	"errors"
	"fmt"
)

var ErrInvalidArgs = errors.New("invalid tool arguments")

func ValidateArgs(tool Tool, args json.RawMessage) error {
	if len(args) == 0 {
		return nil
	}

	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(args, &decoded); err != nil {
		return fmt.Errorf("%w for %q: %v", ErrInvalidArgs, tool.Name, err)
	}
	if decoded == nil {
		return fmt.Errorf("%w for %q: expected JSON object", ErrInvalidArgs, tool.Name)
	}
	return nil
}
```

- [ ] **Step 4: Run tests to verify Task 3 passes**

Run:

```bash
gofmt -w internal/tools/registry/validation.go internal/tools/registry/validation_test.go
go test ./internal/tools/registry
```

Expected: PASS.

- [ ] **Step 5: Commit Task 3**

Run:

```bash
git add internal/tools/registry/validation.go internal/tools/registry/validation_test.go
git commit -m "feat: add tool argument validation placeholder"
```

Expected: commit succeeds.

---

### Task 4: Audit Event Shape

**Files:**
- Create: `internal/tools/registry/audit.go`
- Create: `internal/tools/registry/audit_test.go`

**Interfaces:**
- Consumes:
  - `Tool`
  - `ToolCall`
  - `ToolResult`
  - `RiskLevel`
- Produces:
  - `type ApprovalState string`
  - `const ApprovalNotRequired ApprovalState = "not_required"`
  - `const ApprovalPending ApprovalState = "pending"`
  - `const ApprovalApproved ApprovalState = "approved"`
  - `const ApprovalDenied ApprovalState = "denied"`
  - `type AuditEvent struct`
  - `func NewAuditEvent(now time.Time, tool Tool, call ToolCall, result ToolResult, approval ApprovalState, err error) AuditEvent`

- [ ] **Step 1: Write failing audit tests**

Create `internal/tools/registry/audit_test.go`:

```go
package registry

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestNewAuditEventCopiesToolCallResultAndError(t *testing.T) {
	now := time.Unix(123, 0)
	exitCode := 2
	tool := testTool("shell.run")
	tool.Risk = RiskCommand
	call := ToolCall{
		ID:   "call-1",
		Name: "shell.run",
		Args: json.RawMessage(`{"cmd":"go test ./..."}`),
	}
	result := ToolResult{
		Summary:         "tests failed",
		Content:         "FAIL",
		FilesChanged:    []string{"internal/example.go"},
		CommandExitCode: &exitCode,
	}

	event := NewAuditEvent(now, tool, call, result, ApprovalApproved, errors.New("exit status 2"))

	if !event.Timestamp.Equal(now) {
		t.Fatalf("Timestamp = %v, want %v", event.Timestamp, now)
	}
	if event.ToolName != "shell.run" {
		t.Fatalf("ToolName = %q, want shell.run", event.ToolName)
	}
	if string(event.Args) != `{"cmd":"go test ./..."}` {
		t.Fatalf("Args = %s", event.Args)
	}
	if event.Risk != RiskCommand {
		t.Fatalf("Risk = %q, want command", event.Risk)
	}
	if event.Approval != ApprovalApproved {
		t.Fatalf("Approval = %q, want approved", event.Approval)
	}
	if event.ResultSummary != "tests failed" {
		t.Fatalf("ResultSummary = %q, want tests failed", event.ResultSummary)
	}
	if len(event.FilesChanged) != 1 || event.FilesChanged[0] != "internal/example.go" {
		t.Fatalf("FilesChanged = %#v", event.FilesChanged)
	}
	if event.CommandExitCode == nil || *event.CommandExitCode != 2 {
		t.Fatalf("CommandExitCode = %#v, want 2", event.CommandExitCode)
	}
	if event.Error != "exit status 2" {
		t.Fatalf("Error = %q, want exit status 2", event.Error)
	}
	if event.AgentRole != "" {
		t.Fatalf("AgentRole = %q, want empty", event.AgentRole)
	}
	if event.Model != "" {
		t.Fatalf("Model = %q, want empty", event.Model)
	}
}

func TestNewAuditEventCopiesFilesChangedSlice(t *testing.T) {
	now := time.Unix(123, 0)
	result := ToolResult{
		Summary:      "changed files",
		FilesChanged: []string{"a.go"},
	}

	event := NewAuditEvent(now, testTool("file.write_patch"), ToolCall{Name: "file.write_patch"}, result, ApprovalNotRequired, nil)
	result.FilesChanged[0] = "mutated.go"

	if len(event.FilesChanged) != 1 || event.FilesChanged[0] != "a.go" {
		t.Fatalf("FilesChanged = %#v, want independent copy", event.FilesChanged)
	}
	if event.Error != "" {
		t.Fatalf("Error = %q, want empty", event.Error)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run:

```bash
go test ./internal/tools/registry
```

Expected: FAIL with undefined symbols such as `NewAuditEvent`, `ApprovalApproved`, or `AuditEvent`.

- [ ] **Step 3: Implement audit event types**

Create `internal/tools/registry/audit.go`:

```go
package registry

import (
	"encoding/json"
	"time"
)

type ApprovalState string

const (
	ApprovalNotRequired ApprovalState = "not_required"
	ApprovalPending     ApprovalState = "pending"
	ApprovalApproved    ApprovalState = "approved"
	ApprovalDenied      ApprovalState = "denied"
)

type AuditEvent struct {
	Timestamp       time.Time
	AgentRole       string
	Model           string
	ToolName        string
	Args            json.RawMessage
	Risk            RiskLevel
	Approval        ApprovalState
	ResultSummary   string
	FilesChanged    []string
	CommandExitCode *int
	Error           string
}

func NewAuditEvent(now time.Time, tool Tool, call ToolCall, result ToolResult, approval ApprovalState, err error) AuditEvent {
	event := AuditEvent{
		Timestamp:       now,
		ToolName:        call.Name,
		Args:            append(json.RawMessage(nil), call.Args...),
		Risk:            tool.Risk,
		Approval:        approval,
		ResultSummary:   result.Summary,
		FilesChanged:    append([]string(nil), result.FilesChanged...),
		CommandExitCode: result.CommandExitCode,
	}
	if event.ToolName == "" {
		event.ToolName = tool.Name
	}
	if err != nil {
		event.Error = err.Error()
	}
	return event
}
```

- [ ] **Step 4: Run tests to verify Task 4 passes**

Run:

```bash
gofmt -w internal/tools/registry/audit.go internal/tools/registry/audit_test.go
go test ./internal/tools/registry
```

Expected: PASS.

- [ ] **Step 5: Commit Task 4**

Run:

```bash
git add internal/tools/registry/audit.go internal/tools/registry/audit_test.go
git commit -m "feat: add tool audit events"
```

Expected: commit succeeds.

---

### Task 5: Milestone Checklist and Whole-Project Verification

**Files:**
- Modify: `docs/10-mvp-implementation-checklist.md`

**Interfaces:**
- Consumes:
  - completed `internal/tools/registry` package
- Produces:
  - Milestone D checklist marked complete

- [ ] **Step 1: Mark Milestone D checklist complete**

Modify `docs/10-mvp-implementation-checklist.md`:

```markdown
## Milestone D: Tool registry

- [x] Define `Tool` type
- [x] Define `ToolHandler`
- [x] Define risk levels
- [x] Add registry lookup
- [x] Add schema validation placeholder
- [x] Add tool call audit event
```

- [ ] **Step 2: Run focused tests**

Run:

```bash
go test ./internal/tools/registry
```

Expected: PASS.

- [ ] **Step 3: Run full test suite**

Run:

```bash
go test ./...
```

Expected: PASS for all packages.

- [ ] **Step 4: Run vet**

Run:

```bash
go vet ./...
```

Expected: no output, exit 0.

- [ ] **Step 5: Check git status**

Run:

```bash
git status --short
```

Expected: only Milestone D implementation files and `docs/10-mvp-implementation-checklist.md` are modified/untracked, plus any pre-existing unrelated untracked `CLAUDE.md`.

- [ ] **Step 6: Commit checklist completion**

Run:

```bash
git add docs/10-mvp-implementation-checklist.md
git commit -m "docs: mark milestone d complete"
```

Expected: commit succeeds.

- [ ] **Step 7: Final verification**

Run:

```bash
go test ./...
go vet ./...
```

Expected: both commands pass.

