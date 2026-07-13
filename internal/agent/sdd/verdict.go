package sdd

import "strings"

// VerdictStatus is the spec-compliance or quality outcome for a task.
type VerdictStatus string

const (
	VerdictPass       VerdictStatus = "pass"
	VerdictFail       VerdictStatus = "fail"
	VerdictWarning    VerdictStatus = "warning"
	VerdictApproved   VerdictStatus = "approved"
	VerdictNeedsFixes VerdictStatus = "needs_fixes"
	VerdictUnknown    VerdictStatus = "unknown"
)

// Finding is one issue a reviewer raises.
type Finding struct {
	Severity string // "Critical", "Important", "Minor"
	Text     string
}

// TaskVerdict is the parsed output of a task reviewer.
type TaskVerdict struct {
	SpecCompliance VerdictStatus
	TaskQuality    VerdictStatus
	Findings       []Finding
}

// BranchVerdict is the parsed output of the branch reviewer.
type BranchVerdict struct {
	Ready    bool
	Findings []Finding
}

// ParseTaskVerdict extracts the spec compliance and task quality verdicts
// from a task reviewer's report text. It also populates Findings by parsing
// bullet items under #### Critical / #### Important / #### Minor headings
// in the Issues section.
func ParseTaskVerdict(text string) TaskVerdict {
	v := TaskVerdict{SpecCompliance: VerdictUnknown, TaskQuality: VerdictUnknown}
	lower := strings.ToLower(text)
	if strings.Contains(text, "✅ Spec compliant") || (strings.Contains(lower, "spec compliant") && !strings.Contains(text, "❌")) {
		v.SpecCompliance = VerdictPass
	}
	if strings.Contains(text, "❌") && strings.Contains(lower, "spec compliance") {
		v.SpecCompliance = VerdictFail
	}
	if strings.Contains(text, "⚠️") && strings.Contains(lower, "spec compliance") && v.SpecCompliance == VerdictUnknown {
		v.SpecCompliance = VerdictWarning
	}
	if strings.Contains(lower, "task quality:") {
		if strings.Contains(lower, "approved") {
			v.TaskQuality = VerdictApproved
		}
		if strings.Contains(lower, "needs fixes") {
			v.TaskQuality = VerdictNeedsFixes
		}
	}

	// Extract findings from #### <Severity> heading sections.
	lines := strings.Split(text, "\n")
	var currentSeverity string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "#### Critical"):
			currentSeverity = "Critical"
		case strings.HasPrefix(trimmed, "#### Important"):
			currentSeverity = "Important"
		case strings.HasPrefix(trimmed, "#### Minor"):
			currentSeverity = "Minor"
		case strings.HasPrefix(trimmed, "- ") && currentSeverity != "":
			v.Findings = append(v.Findings, Finding{
				Severity: currentSeverity,
				Text:     strings.TrimPrefix(trimmed, "- "),
			})
		default:
			// A non-empty line that isn't a heading or bullet resets the
			// current severity section (e.g. ### Assessment closes Issues).
			if trimmed != "" && !strings.HasPrefix(trimmed, "- ") && !strings.HasPrefix(trimmed, "####") {
				currentSeverity = ""
			}
		}
	}

	return v
}

// ParseBranchVerdict extracts the merge readiness from a branch
// reviewer's report text.
func ParseBranchVerdict(text string) BranchVerdict {
	v := BranchVerdict{}
	if strings.Contains(text, "✅ Ready to merge") || (strings.Contains(strings.ToLower(text), "ready to merge") && !strings.Contains(text, "❌")) {
		v.Ready = true
	}
	if strings.Contains(text, "❌") && strings.Contains(strings.ToLower(text), "not ready") {
		v.Ready = false
	}
	return v
}

// HasBlockingFindings returns true if the verdict has Critical or
// Important findings that must be fixed before proceeding.
func (v TaskVerdict) HasBlockingFindings() bool {
	return v.SpecCompliance == VerdictFail || v.TaskQuality == VerdictNeedsFixes
}
