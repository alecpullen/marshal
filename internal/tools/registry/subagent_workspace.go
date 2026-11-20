package registry

import "strings"

// SubagentWorkspaceEvidence is the machine-readable lifecycle evidence of one
// owned child workspace: which session and task own it, what the child was
// bound to, the commit it produced and the checkout that work belongs in, and
// what became of the integration and of the workspace's own artifacts.
//
// Every field is a primitive string, and an empty one is unknown rather than a
// zero-valued fact. Empty Execution, Integration or Cleanup means the record
// predates the state or has not settled — never success. CandidateSHA is empty
// for a child that produced no commit, so a reader must not infer an
// integration from its absence either.
//
// The type is declared here, with the payload's JSON names, because the notice
// is decoded from a persisted audit record: this package may not import the
// service, the database or the session to understand its own payload.
type SubagentWorkspaceEvidence struct {
	OwnerSessionID string `json:"ownerSessionId,omitempty"`
	TaskKey        string `json:"taskKey,omitempty"`
	Mode           string `json:"mode,omitempty"`
	Root           string `json:"root,omitempty"`
	Branch         string `json:"branch,omitempty"`
	BaseSHA        string `json:"baseSha,omitempty"`
	CandidateSHA   string `json:"candidateSha,omitempty"`
	TargetRoot     string `json:"targetRoot,omitempty"`
	TargetBranch   string `json:"targetBranch,omitempty"`
	Execution      string `json:"execution,omitempty"`
	Integration    string `json:"integration,omitempty"`
	Cleanup        string `json:"cleanup,omitempty"`
	Reason         string `json:"reason,omitempty"`
}

// NoticeSubagentWorkspace is the ToolNotice.Data key holding a workspace
// lifecycle evidence payload. One stable key keeps the map forward-compatible:
// a consumer that does not know it ignores the value rather than misreading
// it.
const NoticeSubagentWorkspace = "subagent_workspace"

// NewSubagentWorkspaceNotice builds the notice that carries one workspace's
// lifecycle evidence. Its Text is empty by construction: this is metadata, not
// coaching, so the summariser appends no footer to the model's tool result.
func NewSubagentWorkspaceNotice(ev SubagentWorkspaceEvidence) *ToolNotice {
	return &ToolNotice{
		Kind: NoticeSubagentWorkspace,
		Data: map[string]any{NoticeSubagentWorkspace: ev},
	}
}

// SubagentWorkspace decodes the workspace lifecycle evidence a notice carries,
// if any. It tolerates every stored form — the in-memory struct, a pointer, and
// the JSON map a persisted notice decodes to — and returns false for a nil
// notice, a missing key, an unrecognised shape, or a payload with no task key,
// which identifies nothing.
func (n *ToolNotice) SubagentWorkspace() (SubagentWorkspaceEvidence, bool) {
	if n == nil || len(n.Data) == 0 {
		return SubagentWorkspaceEvidence{}, false
	}
	return subagentWorkspaceFromValue(n.Data[NoticeSubagentWorkspace])
}

func subagentWorkspaceFromValue(v any) (SubagentWorkspaceEvidence, bool) {
	switch t := v.(type) {
	case nil:
		return SubagentWorkspaceEvidence{}, false
	case SubagentWorkspaceEvidence:
		return t, validSubagentWorkspace(t)
	case *SubagentWorkspaceEvidence:
		if t == nil {
			return SubagentWorkspaceEvidence{}, false
		}
		return *t, validSubagentWorkspace(*t)
	case map[string]any:
		ev := SubagentWorkspaceEvidence{
			OwnerSessionID: noticeString(t, "ownerSessionId"),
			TaskKey:        noticeString(t, "taskKey"),
			Mode:           noticeString(t, "mode"),
			Root:           noticeString(t, "root"),
			Branch:         noticeString(t, "branch"),
			BaseSHA:        noticeString(t, "baseSha"),
			CandidateSHA:   noticeString(t, "candidateSha"),
			TargetRoot:     noticeString(t, "targetRoot"),
			TargetBranch:   noticeString(t, "targetBranch"),
			Execution:      noticeString(t, "execution"),
			Integration:    noticeString(t, "integration"),
			Cleanup:        noticeString(t, "cleanup"),
			Reason:         noticeString(t, "reason"),
		}
		return ev, validSubagentWorkspace(ev)
	default:
		return SubagentWorkspaceEvidence{}, false
	}
}

// validSubagentWorkspace reports whether a payload identifies the work it is
// evidence of. A payload naming no task belongs to nothing a consumer can pair
// with, so it decodes as absent rather than as an anonymous record.
func validSubagentWorkspace(ev SubagentWorkspaceEvidence) bool {
	return strings.TrimSpace(ev.TaskKey) != ""
}
