// internal/app/session/sudoauth_test.go — privileged-auth pending protocol.
package session

import (
	"testing"
	"time"
)

func TestPendingSudoAuthRespondOnce(t *testing.T) {
	sa := &PendingSudoAuth{
		Command:      "systemctl restart nginx",
		ResponseChan: make(chan SudoAuthDecision, 1),
	}
	sa.Respond(SudoAuthDecision{OK: true, Password: []byte("first")})
	sa.Respond(SudoAuthDecision{OK: true, Password: []byte("second")})

	got := <-sa.ResponseChan
	if !got.OK || string(got.Password) != "first" {
		t.Fatalf("decision = %+v, want the first response only", got)
	}
	if _, open := <-sa.ResponseChan; open {
		t.Fatal("channel should be closed after the first response")
	}
}

func TestPendingSudoAuthNeverBlocksOnUnbuffered(t *testing.T) {
	sa := &PendingSudoAuth{ResponseChan: make(chan SudoAuthDecision)} // unbuffered, no receiver
	done := make(chan struct{})
	go func() { sa.Respond(SudoAuthDecision{OK: true}); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Respond blocked on an unbuffered channel with no receiver")
	}
}

func TestSetPendingSudoAuthSnapshotCarriesNoPassword(t *testing.T) {
	s := newTestState()
	sa := &PendingSudoAuth{
		Command:      "systemctl restart nginx",
		Reason:       "restart the web server",
		AllowUnlock:  true,
		ResponseChan: make(chan SudoAuthDecision, 1),
	}
	s.SetPendingSudoAuth(sa)

	got := s.PendingSudoAuth()
	if got == nil {
		t.Fatal("PendingSudoAuth() = nil after SetPendingSudoAuth")
	}
	if got.Command != "systemctl restart nginx" || !got.AllowUnlock {
		t.Fatalf("snapshot = %+v, want the command and unlock flag", got)
	}
	if got.ResponseChan != sa.ResponseChan {
		t.Fatal("snapshot must share ResponseChan so a subscriber can answer")
	}
}

func TestResolvePendingForShutdownDeniesSudoAuth(t *testing.T) {
	s := newTestState()
	sa := &PendingSudoAuth{
		Command:      "systemctl restart nginx",
		ResponseChan: make(chan SudoAuthDecision, 1),
	}
	s.SetPendingSudoAuth(sa)

	s.ResolvePendingForShutdown()

	if s.PendingSudoAuth() != nil {
		t.Fatal("PendingSudoAuth should be nil after ResolvePendingForShutdown")
	}
	got := <-sa.ResponseChan
	if got.OK {
		t.Fatal("shutdown must deny the privileged-auth prompt")
	}
	if len(got.Password) != 0 {
		t.Fatalf("shutdown decision carries a password: %q", got.Password)
	}
}

func TestResolvePendingForShutdownNeverBlocksOnSudoAuth(t *testing.T) {
	s := newTestState()
	s.SetPendingSudoAuth(&PendingSudoAuth{
		Command:      "id",
		ResponseChan: make(chan SudoAuthDecision), // unbuffered, no receiver
	})
	done := make(chan struct{})
	go func() { s.ResolvePendingForShutdown(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("ResolvePendingForShutdown blocked on an unbuffered sudo-auth channel")
	}
	if s.PendingSudoAuth() != nil {
		t.Fatal("PendingSudoAuth should be nil even when the send was dropped")
	}
}

func TestSecretPromptCapableDefaultsFalse(t *testing.T) {
	s := newTestState()
	if s.SecretPromptCapable() {
		t.Fatal("SecretPromptCapable() = true on a fresh session, want false (fail closed)")
	}
	s.SetSecretPromptCapable(true)
	if !s.SecretPromptCapable() {
		t.Fatal("SecretPromptCapable() = false after SetSecretPromptCapable(true)")
	}
}

func TestTryLockSudoIsSingleFlight(t *testing.T) {
	s := newTestState()
	if !s.TryLockSudo() {
		t.Fatal("first TryLockSudo() = false, want true")
	}
	if s.TryLockSudo() {
		t.Fatal("second TryLockSudo() = true, want false while held")
	}
	s.UnlockSudo()
	if !s.TryLockSudo() {
		t.Fatal("TryLockSudo() after Unlock = false, want true")
	}
	s.UnlockSudo()
}
