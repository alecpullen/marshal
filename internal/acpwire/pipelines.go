package acpwire

// The plan-run strategies (C8). An empty strategy lets the plan choose:
// adaptive when it has executable blocks, agent otherwise.
const (
	StrategyAgent    = "agent"
	StrategyAdaptive = "adaptive"
	StrategyStrict   = "strict"
)

// SwarmParams is one swarm run: its goal, and the role overrides for this run
// only.
type SwarmParams struct {
	Goal      string            `json:"goal"`
	Overrides map[string]string `json:"overrides,omitempty"` // role name -> model preset
}

// SDDParams is one plan run: the plan, its strategy, the role overrides for
// this run only, and the verify commands that replace the configured ones for
// this run. Nil Verify keeps the configured commands.
type SDDParams struct {
	PlanPath  string            `json:"planPath"`
	Strategy  string            `json:"strategy,omitempty"`
	Overrides map[string]string `json:"overrides,omitempty"` // role name -> model preset
	Verify    *VerifyCommands   `json:"verify,omitempty"`
}

// PlanPreflightParams names the plan a preflight inspects and the strategy
// asked for; an empty strategy lets the plan choose.
type PlanPreflightParams struct {
	PlanPath string `json:"planPath"`
	Strategy string `json:"strategy,omitempty"`
}

// PlanPreflight is what a plan run's preflight shows from the agent side: the
// plan's inspection and the verify gate the run would use.
type PlanPreflight struct {
	Inspection PlanInspection `json:"inspection"`
	Verify     VerifyGate     `json:"verify"`
}

// VerifyGate is the build and test commands a plan run's verify gate runs.
type VerifyGate struct {
	Build    string `json:"build,omitempty"`
	Test     string `json:"test,omitempty"`
	Known    bool   `json:"known,omitempty"`    // a command was resolved
	Detected bool   `json:"detected,omitempty"` // found in the repository's manifest, not configured
}

// PlanInspection is a plan's parse, compile and preflight result.
type PlanInspection struct {
	Strategy      string           `json:"strategy"` // the effective strategy
	HasBlocks     bool             `json:"hasBlocks,omitempty"`
	StrictBlocked bool             `json:"strictBlocked,omitempty"`
	DetOps        int              `json:"detOps,omitempty"`
	AgentOps      int              `json:"agentOps,omitempty"`
	EstCalls      int              `json:"estCalls,omitempty"`
	Tasks         []PlanTask       `json:"tasks,omitempty"`
	Diagnostics   []PlanDiagnostic `json:"diagnostics,omitempty"`
}

// PlanTask is one task's compile report.
type PlanTask struct {
	TaskN    int    `json:"taskN"`
	Title    string `json:"title"`
	DetOps   int    `json:"detOps,omitempty"`
	AgentOps int    `json:"agentOps,omitempty"`
	EstCalls int    `json:"estCalls,omitempty"`
}

// PlanDiagnostic is one plan-authoring error or warning.
type PlanDiagnostic struct {
	TaskN    int    `json:"taskN,omitempty"` // 0 for the plan as a whole
	Severity string `json:"severity"`        // DiagnosticError or DiagnosticWarning
	Message  string `json:"message"`
	Line     int    `json:"line,omitempty"`
}

// The diagnostic severities.
const (
	DiagnosticError   = "error"
	DiagnosticWarning = "warning"
)

// AuthorPlanParams asks for one plan authored from a design.
type AuthorPlanParams struct {
	Goal   string `json:"goal"`
	Design string `json:"design,omitempty"` // the design to convert; the goal when empty
}

// AuthoredPlan is the candidate plan an authoring run wrote, for review.
type AuthoredPlan struct {
	StopReason   string          `json:"stopReason"`
	PlanPath     string          `json:"planPath,omitempty"`
	AutoAccepted bool            `json:"autoAccepted,omitempty"`
	Inspection   *PlanInspection `json:"inspection,omitempty"` // nil when the candidate has none
}
