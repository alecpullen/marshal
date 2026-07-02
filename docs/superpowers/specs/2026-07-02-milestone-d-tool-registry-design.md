# Milestone D: Tool Registry Design

## Goal

Milestone D adds Marshal's tool registry framework: stable contracts for tool metadata, handlers, risk levels, lookup, argument validation, and audit events.

This milestone is framework-only. It does not add native tools, execute commands, read or write files, request approvals, or persist audit logs. Concrete tools start in Milestone E, approval policy starts in Milestone F, and agent-driven tool use starts in Milestone H.

## Package Boundary

Create one package:

```text
internal/tools/registry
```

The package owns the contracts that later tool implementations import. It should not import `internal/app/tui`, `internal/app/session`, `internal/llm`, `internal/agent`, `internal/db`, or future native tool packages.

Keeping Milestone D in one package is intentional. Splitting contracts, registry, validation, and audit event types into multiple packages before any concrete tools exist would add package boundaries that have not been proven by usage.

## Core Types

### RiskLevel

Use a string type for stable logs, prompt text, and test expectations:

```go
type RiskLevel string

const (
	RiskReadOnly       RiskLevel = "read_only"
	RiskWorkspaceWrite RiskLevel = "workspace_write"
	RiskCommand        RiskLevel = "command"
	RiskNetwork        RiskLevel = "network"
	RiskDestructive    RiskLevel = "destructive"
)
```

The package should expose validation for risk values. Unknown values are rejected during registration.

### Tool

`Tool` describes one callable tool:

```go
type Tool struct {
	Name        string
	Description string
	Schema      json.RawMessage
	Risk        RiskLevel
	Handler     ToolHandler
}
```

`Name` is the stable lookup key, such as `file.read` or `git.status` in future milestones. Milestone D does not register those tools.

`Description` is human/model-facing metadata.

`Schema` is the JSON Schema shape the model and future validator will use for arguments. Milestone D only verifies that this field is syntactically valid JSON when present.

`Risk` is the default risk level for the tool. Later milestones may refine risk per call, especially for `shell.run`.

`Handler` is required. Registration fails if it is nil.

### ToolCall

`ToolCall` represents one requested invocation:

```go
type ToolCall struct {
	ID   string
	Name string
	Args json.RawMessage
}
```

`ID` is optional for Milestone D but included now so future audit logs and agent loops can correlate requests and results.

### ToolHandler

Handlers receive context and the raw call:

```go
type ToolHandler func(ctx context.Context, call ToolCall) (ToolResult, error)
```

Handlers should decode `call.Args` into tool-specific structs once concrete tools exist. The registry does not know those concrete argument types.

### ToolResult

`ToolResult` is the common output shape for later tools:

```go
type ToolResult struct {
	Summary         string
	Content         string
	FilesChanged    []string
	CommandExitCode *int
}
```

`Summary` is the short model-facing result.

`Content` is optional detailed output, such as command output or file contents.

`FilesChanged` records workspace paths changed by a write-capable tool.

`CommandExitCode` is nil for non-command tools and populated by future shell/test tools.

## Registry Behavior

`Registry` owns registered tools by name:

```go
type Registry struct {
	tools map[string]Tool
}
```

Public API:

```go
func New() *Registry
func (r *Registry) Register(tool Tool) error
func (r *Registry) Lookup(name string) (Tool, bool)
func (r *Registry) List() []Tool
```

`Register` rejects:

- empty or whitespace-only names
- nil handlers
- unknown risk levels
- invalid schema JSON
- duplicate tool names

`Lookup` returns a copy of the registered `Tool` and a boolean hit flag.

`List` returns tools sorted by name. Deterministic ordering matters for tests, TUI rendering, and future model prompts.

The registry does not execute handlers in Milestone D. It only stores and returns them.

## Schema Validation Placeholder

Add a placeholder function:

```go
func ValidateArgs(tool Tool, args json.RawMessage) error
```

Milestone D validation rules:

- empty args are accepted as `{}` semantics
- malformed JSON returns a clear error
- valid JSON that is not an object returns a clear error
- valid JSON objects are accepted

This is deliberately not full JSON Schema validation. The function exists so later milestones can call one stable API before tool execution, and so the full validator can replace the placeholder without changing callers.

The placeholder should not inspect `tool.Schema` beyond the registration-time JSON syntax check.

## Audit Event Shape

Add plain audit event types in `internal/tools/registry`:

```go
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
```

Add a helper:

```go
func NewAuditEvent(now time.Time, tool Tool, call ToolCall, result ToolResult, approval ApprovalState, err error) AuditEvent
```

The helper copies the tool name, args, risk, result summary, files changed, command exit code, approval state, timestamp, and error string. `AgentRole` and `Model` remain empty in Milestone D because agent runtime and model routing are not wired yet.

The event is not persisted. SQLite persistence starts in Milestone I.

## Error Handling

Use package-level sentinel errors only where callers are likely to branch on them:

- duplicate registration
- tool not valid for registration
- invalid arguments

Otherwise, descriptive `fmt.Errorf` messages are enough. Tests should assert behavior and useful substrings rather than exact full error strings unless a sentinel is being checked with `errors.Is`.

## Test Strategy

Milestone D should add tests for:

- `RiskLevel` validation accepts only documented values
- `Register` accepts a valid tool
- `Register` rejects empty names
- `Register` rejects nil handlers
- `Register` rejects unknown risk values
- `Register` rejects invalid schema JSON
- `Register` rejects duplicate names
- `Lookup` returns registered tools and misses unknown names
- `List` returns tools sorted by name
- `ValidateArgs` accepts empty args
- `ValidateArgs` accepts valid JSON object args
- `ValidateArgs` rejects malformed JSON
- `ValidateArgs` rejects valid non-object JSON
- `NewAuditEvent` copies call/result/risk/approval/error fields
- `NewAuditEvent` copies slices so later mutation of `ToolResult.FilesChanged` does not mutate the event

Verification commands:

```bash
go test ./internal/tools/registry
go test ./...
go vet ./...
```

## Non-Goals

Milestone D does not:

- implement `file.read`, `repo.search`, `git.status`, `git.diff`, `shell.run`, or `test.run`
- classify shell command risk per invocation
- request or render user approvals
- execute registered tools through a broker
- wire tools into the TUI or agent loop
- persist audit events
- implement full JSON Schema validation
- add third-party dependencies

## Acceptance Criteria

Milestone D is complete when:

- `docs/10-mvp-implementation-checklist.md` marks all Milestone D items complete
- `internal/tools/registry` exposes the contracts described above
- registry behavior is covered by unit tests
- schema validation placeholder behavior is covered by unit tests
- audit event construction is covered by unit tests
- `go test ./...` passes
- `go vet ./...` passes
