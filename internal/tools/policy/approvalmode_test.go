package policy

import "testing"

func TestValidApprovalMode(t *testing.T) {
	valid := []string{"plan", "default", "edit", "copilot", "auto", "PLAN", "Auto"}
	for _, mode := range valid {
		if !ValidApprovalMode(mode) {
			t.Errorf("ValidApprovalMode(%q) = false, want true", mode)
		}
	}
	invalid := []string{"", "yolo", "read-only", " default ", "defaults"}
	for _, mode := range invalid {
		if ValidApprovalMode(mode) {
			t.Errorf("ValidApprovalMode(%q) = true, want false", mode)
		}
	}
}

// TestApprovalModeParity guards against drift between the two functions that
// read the canonical approvalModes list. Every canonical mode must round-trip
// through ParseApprovalMode into a string that ValidApprovalMode accepts, and
// every mode ValidApprovalMode accepts must parse to a non-default non-empty
// ApprovalMode (i.e. the two must never disagree).
func TestApprovalModeParity(t *testing.T) {
	for _, name := range approvalModes {
		mode := ParseApprovalMode(name)
		if !ValidApprovalMode(name) {
			t.Errorf("ParseApprovalMode(%q) = %q but ValidApprovalMode(%q) = false; mode accepted by one but not the other", name, mode, name)
		}
		if string(mode) != name {
			t.Errorf("ParseApprovalMode(%q) = %q, want canonical %q", name, mode, name)
		}
	}
}
