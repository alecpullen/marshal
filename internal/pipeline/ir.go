// internal/pipeline/ir.go
package pipeline

// Strategy selects how the controller executes a plan's tasks.
type Strategy string

const (
	// StrategyAuto selects the effective strategy from the plan's contents:
	// adaptive when executable marshal.* blocks exist, agent otherwise.
	StrategyAuto Strategy = "auto"

	// StrategyAgent preserves the current model-led behavior. Every task
	// dispatches an implementer subagent regardless of executable blocks.
	StrategyAgent Strategy = "agent"

	// StrategyAdaptive applies deterministic operations first, then
	// dispatches an agent only for unresolved (AgentOp or blocked) work.
	StrategyAdaptive Strategy = "adaptive"

	// StrategyStrict applies deterministic operations only. Any unresolved
	// work fails the run before the worktree is created.
	StrategyStrict Strategy = "strict"
)

// OpStatus is the preflight status of one operation.
type OpStatus string

const (
	OpExecutable OpStatus = "executable"
	OpBlocked    OpStatus = "blocked"
	OpAgent      OpStatus = "agent"
)

// TaskStatus is the derived status of one task after preflight.
type TaskStatus string

const (
	TaskExecutable TaskStatus = "executable"
	TaskPartial    TaskStatus = "partial"
	TaskAgentOnly  TaskStatus = "agent-only"
	TaskBlocked    TaskStatus = "blocked"
)

// ExecType records how a task was executed, for ledger and /run display.
type ExecType string

const (
	ExecAgent         ExecType = "agent"
	ExecDeterministic ExecType = "deterministic"
	ExecMixed         ExecType = "deterministic + agent"
)

// PlanIR is the compiled, validated, and prefchecked execution plan. It is
// an in-memory Go value, not a serialized format. It exists only between
// compile and execute.
type PlanIR struct {
	Version           int
	Name              string
	Slug              string
	SourcePath        string
	GlobalConstraints string
	Tasks             []TaskIR
}

// TaskIR is one task in the compiled plan.
type TaskIR struct {
	ID              string
	TaskN           int
	Title           string
	ProseBody       string
	Operations      []Operation
	Dependencies    []string
	AllowedFiles    []string
	FallbackAllowed bool
	Status          TaskStatus
}

// Operation is one executable step within a task. The concrete types are
// PatchOp, FileOp, RunOp, AssertOp, and AgentOp.
type Operation interface {
	OpKind() string
	OpStatus() OpStatus
	SetStatus(OpStatus)
	SourceLineRange() (int, int)
}

// PatchOp is an exact SEARCH/REPLACE edit to an existing file.
type PatchOp struct {
	File      string
	Search    string
	Replace   string
	Status    OpStatus
	LineStart int
	LineEnd   int
}

func (o *PatchOp) OpKind() string              { return "patch" }
func (o *PatchOp) OpStatus() OpStatus          { return o.Status }
func (o *PatchOp) SetStatus(s OpStatus)        { o.Status = s }
func (o *PatchOp) SourceLineRange() (int, int) { return o.LineStart, o.LineEnd }

// FileOp writes a complete new file. Replace must be true to overwrite an
// existing file.
type FileOp struct {
	Path      string
	Content   string
	Replace   bool
	Status    OpStatus
	LineStart int
	LineEnd   int
}

func (o *FileOp) OpKind() string              { return "file" }
func (o *FileOp) OpStatus() OpStatus          { return o.Status }
func (o *FileOp) SetStatus(s OpStatus)        { o.Status = s }
func (o *FileOp) SourceLineRange() (int, int) { return o.LineStart, o.LineEnd }

// RunOp executes an approved command with an expected exit status.
type RunOp struct {
	Command    []string
	Phase      string // "prepare" or "verify"
	ExpectExit int
	Status     OpStatus
	LineStart  int
	LineEnd    int
}

func (o *RunOp) OpKind() string              { return "run" }
func (o *RunOp) OpStatus() OpStatus          { return o.Status }
func (o *RunOp) SetStatus(s OpStatus)        { o.Status = s }
func (o *RunOp) SourceLineRange() (int, int) { return o.LineStart, o.LineEnd }

// AssertKind identifies the assertion type.
type AssertKind string

const (
	AssertGoSymbol    AssertKind = "go.symbol"
	AssertGoCompiles  AssertKind = "go.compiles"
	AssertFileExists  AssertKind = "file.exists"
	AssertFileMatches AssertKind = "file.matches"
	AssertTestPasses  AssertKind = "test.passes"
)

// AssertOp is a structural or behavioral postcondition.
type AssertOp struct {
	Kind      AssertKind
	File      string
	Symbol    string
	Content   string
	Command   []string
	Package   string
	Status    OpStatus
	LineStart int
	LineEnd   int
}

func (o *AssertOp) OpKind() string              { return "assert" }
func (o *AssertOp) OpStatus() OpStatus          { return o.Status }
func (o *AssertOp) SetStatus(s OpStatus)        { o.Status = s }
func (o *AssertOp) SourceLineRange() (int, int) { return o.LineStart, o.LineEnd }

// AgentOp is an explicit unresolved implementation boundary. It declares
// that agent work is needed, the allowed file scope, and the reason.
type AgentOp struct {
	AllowedFiles []string
	Reason       string
	Status       OpStatus
	LineStart    int
	LineEnd      int
}

func (o *AgentOp) OpKind() string              { return "agent" }
func (o *AgentOp) OpStatus() OpStatus          { return o.Status }
func (o *AgentOp) SetStatus(s OpStatus)        { o.Status = s }
func (o *AgentOp) SourceLineRange() (int, int) { return o.LineStart, o.LineEnd }

// DerivedStatus computes the task's status from its operations' statuses.
func (t *TaskIR) DerivedStatus() TaskStatus {
	if len(t.Operations) == 0 {
		return TaskAgentOnly
	}
	hasExecutable := false
	hasBlocked := false
	hasAgent := false
	for _, op := range t.Operations {
		switch op.OpStatus() {
		case OpExecutable:
			hasExecutable = true
		case OpBlocked:
			hasBlocked = true
		case OpAgent:
			hasAgent = true
		}
	}
	if hasBlocked && !t.FallbackAllowed {
		return TaskBlocked
	}
	if hasBlocked || hasAgent {
		return TaskPartial
	}
	if hasExecutable {
		return TaskExecutable
	}
	return TaskAgentOnly
}

// EstimatedModelCalls returns 1 if any agent work is needed (all agent ops
// go to a single dispatch), 0 otherwise.
func (t *TaskIR) EstimatedModelCalls() int {
	for _, op := range t.Operations {
		if op.OpStatus() == OpAgent || op.OpStatus() == OpBlocked {
			if t.FallbackAllowed || op.OpStatus() == OpAgent {
				return 1
			}
		}
	}
	return 0
}
