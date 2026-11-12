package contract

import "time"

// DiffFile is one changed file's stat line.
type DiffFile struct {
	Path    string `json:"path"`
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
}

// DiffResult is the session/diff result.
type DiffResult struct {
	Files []DiffFile `json:"files"`
	Diff  string     `json:"diff,omitempty"`
}

// MergeResult is the session/merge result. Merged is false for every
// refusal, with Reason naming which.
type MergeResult struct {
	Merged    bool     `json:"merged"`
	Branch    string   `json:"branch"`
	Target    string   `json:"target"`
	Reason    string   `json:"reason,omitempty"`
	Conflicts []string `json:"conflicts,omitempty"`
}

// CommitDraft is the session/commit_draft result.
type CommitDraft struct {
	Message string `json:"message"`
}

// GateResult is the bridge-side mirror of acp.VerifyReply.
type GateResult struct {
	OK            bool   `json:"ok"`
	Skipped       bool   `json:"skipped"`
	FailedCommand string `json:"failedCommand,omitempty"`
	Output        string `json:"output,omitempty"`
}

// GateRecord is an agent's latest verify result and when it ran.
type GateRecord struct {
	Result *GateResult `json:"result"`
	At     time.Time   `json:"at"`
}

// ExitResult reports the outcome. Blocked means the gate refused and no
// override was supplied — nothing was pushed.
type ExitResult struct {
	Destination string      `json:"destination"`
	Branch      string      `json:"branch,omitempty"`
	PRUrl       string      `json:"prUrl,omitempty"`
	Verify      *GateResult `json:"verify,omitempty"`
	Blocked     bool        `json:"blocked,omitempty"`
}

// ExitRequest is an exit's operator decisions.
type ExitRequest struct {
	CommitMessage string `json:"commitMessage"`
	// Override, when non-nil, pushes despite a failed or skipped gate.
	Override *GateOverride `json:"override,omitempty"`
}

// MergeRequest is a merge's operator decisions.
type MergeRequest struct {
	CommitMessage string `json:"commitMessage,omitempty"`
}
