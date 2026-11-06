package acpwire

// IntelligenceCheck is one line of the doctor's Intelligence section (C11,
// plan 8a). Status is "ok", "warn" or "off".
type IntelligenceCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// PlanCandidate is one plan an SDD run can execute, as plan discovery found
// it. Error says why the plan does not parse, LedgerError why its progress
// ledger could not be read, and Resumable that a previous run left it partly
// done.
type PlanCandidate struct {
	Path        string `json:"path"`
	Name        string `json:"name"`
	Slug        string `json:"slug,omitempty"`
	Tasks       int    `json:"tasks,omitempty"`
	Done        int    `json:"done,omitempty"`
	Error       string `json:"error,omitempty"`
	LedgerError string `json:"ledgerError,omitempty"`
	Resumable   bool   `json:"resumable,omitempty"`
}

// ScaffoldResult is where ScaffoldPlan's starter plan is. Existed says a file
// was already there, and nothing was written.
type ScaffoldResult struct {
	Path    string `json:"path"`
	Existed bool   `json:"existed,omitempty"`
}

// DiffQuery names the range Session.Diff renders, Base..Head.
type DiffQuery struct {
	Base string `json:"base"`
	Head string `json:"head"`
}

// FileContent is one file Session.ReadFile read, as session/file returns it.
// Content is empty for a binary file, and cut at 1 MiB when Truncated.
type FileContent struct {
	Path      string `json:"path"`
	Size      int64  `json:"size,omitempty"`
	Binary    bool   `json:"binary,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
	Content   string `json:"content,omitempty"`
}
