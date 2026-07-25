package sdd

import (
	"fmt"
	"strings"
)

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

// SpecStatus is the lifecycle of the spec.md frontmatter `status` field.
type SpecStatus string

const (
	SpecStatusDraft    SpecStatus = "draft"
	SpecStatusApproved SpecStatus = "approved"
)

// ParseSpecFrontmatter extracts the YAML frontmatter block (delimited by
// `---` lines) from a spec.md body. It returns the three fields the SDD
// pipeline needs; the full task-block parsing lives in P4. Returns an error
// if no frontmatter is present.
func ParseSpecFrontmatter(specText string) (SpecFrontmatter, error) {
	lines := strings.Split(specText, "\n")
	if len(lines) < 2 || strings.TrimSpace(lines[0]) != "---" {
		return SpecFrontmatter{}, fmt.Errorf("sdd: spec has no YAML frontmatter")
	}
	var fm SpecFrontmatter
	for i := 1; i < len(lines); i++ {
		line := lines[i]
		if strings.TrimSpace(line) == "---" {
			break
		}
		if v, ok := parseYAMLScalar(line, "status"); ok {
			fm.Status = v
		}
		if v, ok := parseYAMLScalar(line, "source_plan"); ok {
			fm.SourcePlan = v
		}
		if v, ok := parseYAMLScalar(line, "target_branch"); ok {
			fm.TargetBranch = v
		}
	}
	return fm, nil
}

// parseYAMLScalar reads a "key: value" line, returning the trimmed value.
// ok is false if the line is not a scalar for that key.
func parseYAMLScalar(line, key string) (string, bool) {
	prefix := key + ":"
	if !strings.HasPrefix(line, prefix) {
		return "", false
	}
	v := strings.TrimSpace(strings.TrimPrefix(line, prefix))
	v = strings.Trim(v, "\"'")
	return v, true
}
