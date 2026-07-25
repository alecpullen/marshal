package sdd

import (
	"fmt"
	"os"
	"path/filepath"
)

// AuditGate is the gate between implementer return and reviewer dispatch
// (spec §9). It reads the auditor's report, normalizes + validates it, and
// returns a typed decision:
//   - AUDIT_MISSING (block): no report exists; dispatch the auditor first
//   - AUDIT_MALFORMED (block): report exists but fails validation
//   - AUDIT_FAIL (block): auditor reported FAIL
//   - AUDIT_PASS (accept): auditor reported PASS; proceed to reviewer
//
// The caller logs one progress event using r.Event + r.KV.
func AuditGate(ws *Workspace, taskID string) GateResult {
	path := filepath.Join(ws.Dir(), "reports", taskID+"-audit.md")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return GateResult{Decision: DecisionBlock, Event: "AUDIT_MISSING", Reason: "no audit report; dispatch auditor first"}
	}
	if err != nil {
		return GateResult{Decision: DecisionBlock, Event: "AUDIT_MALFORMED", Reason: fmt.Sprintf("read audit report: %v", err)}
	}
	normalized := NormalizeReport(string(data))
	// Parse the normalized string directly (no re-read from disk).
	rep := parseReportFromString(taskID, normalized)
	if err := rep.Validate(); err != nil {
		return GateResult{Decision: DecisionBlock, Event: "AUDIT_MALFORMED", Reason: err.Error()}
	}
	switch rep.Status {
	case ReportPass:
		return GateResult{Decision: DecisionAccept, Event: "AUDIT_PASS", KV: []string{"task", taskID}}
	case ReportFail:
		return GateResult{Decision: DecisionBlock, Event: "AUDIT_FAIL", KV: []string{"task", taskID}}
	default:
		return GateResult{Decision: DecisionBlock, Event: "AUDIT_FAIL", Reason: fmt.Sprintf("unexpected audit status %q", rep.Status), KV: []string{"task", taskID}}
	}
}
