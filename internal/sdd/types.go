package sdd

// TaskStatus is the lifecycle state of a DAGTask, persisted in dag.json.
type TaskStatus string

const (
	TaskPending    TaskStatus = "pending"
	TaskInProgress TaskStatus = "in_progress"
	TaskMerged     TaskStatus = "merged"
	TaskBlocked    TaskStatus = "blocked"
)

// ReportStatus is the first-line verdict a worker writes to its report file.
// The closed set is enforced by report validation in P2.
type ReportStatus string

const (
	ReportDone         ReportStatus = "DONE"
	ReportBlocked      ReportStatus = "BLOCKED"
	ReportNeedsContext ReportStatus = "NEEDS_CONTEXT"
	ReportPass         ReportStatus = "PASS"
	ReportFail         ReportStatus = "FAIL"
	ReportSkip         ReportStatus = "SKIP"
	ReportNeedsHuman   ReportStatus = "NEEDS_HUMAN"
	ReportHealthAlert  ReportStatus = "HEALTH_ALERT"
)

// DAG is the task graph, stored at .marshal/sdd/dag.json (spec §4).
type DAG struct {
	SpecPath string    `json:"spec_path"`
	Tasks    []DAGTask `json:"tasks"`
}

// DAGTask is one node in the DAG. Base/WorktreePath/Branch/ReviewedHead are
// populated by the worktree subsystem (P2); Review is the optional override
// flag (true=required, false=deferred).
type DAGTask struct {
	ID           string     `json:"id"`
	Title        string     `json:"title"`
	Status       TaskStatus `json:"status"`
	Deps         []string   `json:"deps"`
	Files        []string   `json:"files"`
	Acceptance   []string   `json:"acceptance"`
	Base         string     `json:"base,omitempty"`
	WorktreePath string     `json:"worktree,omitempty"`
	Branch       string     `json:"branch,omitempty"`
	ReviewedHead string     `json:"reviewed_head,omitempty"`
	Review       *bool      `json:"review,omitempty"`
}

// RepoState is the pipeline branch state, stored at
// .marshal/sdd/state/repo.json (spec §4).
type RepoState struct {
	Branch       string            `json:"branch"`
	TargetBranch string            `json:"target_branch"`
	Head         string            `json:"head"`
	Merged       []string          `json:"merged"`
	MergedAt     map[string]string `json:"merged_at"`
	LastMergeAt  string            `json:"last_merge_at,omitempty"`
}

// Checkpoint is a pipeline snapshot, stored at
// .marshal/sdd/checkpoints/<short-sha>.json (spec §4).
type Checkpoint struct {
	Tag       string            `json:"tag"`
	SHA       string            `json:"sha"`
	Timestamp string            `json:"ts"`
	Branch    string            `json:"branch"`
	Merged    []string          `json:"merged"`
	MergedAt  map[string]string `json:"merged_at"`
	Message   string            `json:"message"`
}

// SpecFrontmatter is the YAML frontmatter of .marshal/sdd/spec.md (spec §4).
// Parsing/writing the frontmatter is P4 (controller owns decomposition); this
// type exists here so P2's contract extraction can reference its fields.
type SpecFrontmatter struct {
	Status       string `yaml:"status"`
	SourcePlan   string `yaml:"source_plan"`
	TargetBranch string `yaml:"target_branch"`
}
