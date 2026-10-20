// internal/app/session/sudoapproval_test.go — combined-prompt metadata.
package session

import "testing"

// TestSudoAuthSnapshotCarriesCommandApprovalRole pins the mechanical metadata
// extension: a combined privileged prompt publishes its command-approval role
// so the TUI presents one dialog instead of opening a second approval.
func TestSudoAuthSnapshotCarriesCommandApprovalRole(t *testing.T) {
	s := newTestState()
	s.SetPendingSudoAuth(&PendingSudoAuth{
		Command:         "git reset --hard",
		CommandApproval: true,
		ResponseChan:    make(chan SudoAuthDecision, 1),
	})

	got := s.PendingSudoAuth()
	if got == nil {
		t.Fatal("PendingSudoAuth() = nil after SetPendingSudoAuth")
	}
	if !got.CommandApproval {
		t.Fatalf("snapshot = %+v, want the command-approval role preserved", got)
	}
	if got.Command != "git reset --hard" {
		t.Fatalf("snapshot command = %q, want the exact command", got.Command)
	}
}
