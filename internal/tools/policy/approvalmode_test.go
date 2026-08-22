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
