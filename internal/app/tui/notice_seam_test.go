// internal/app/tui/notice_seam_test.go — the two paths that take a notice
// down (Esc and the TTL tick) reach the session, so every client sharing it
// sees the banner go (plan 6).
package tui

import (
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
)

// noticeOnState builds a model over state and a stub that counts dismissals,
// with a provider notice already up.
func noticeOnState(t *testing.T) (Model, *session.State, *int) {
	t.Helper()
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	stub := &stubSession{}
	dismissals := new(int)
	stub.onDismissNote = func() { *dismissals++ }
	m := testModelStub(state, stub)
	state.SetNotice(session.Notice{
		Category: session.NoticeProvider,
		Severity: session.SeverityError,
		Message:  "provider down",
		SetAt:    time.Unix(100, 0),
	})
	return m, state, dismissals
}

// TestEscDismissesTheNoticeThroughTheSession pins that Esc clears the banner
// on the session rather than only in the model's copy of the state: the
// dismissal is what the web bridge and any later client observe.
func TestEscDismissesTheNoticeThroughTheSession(t *testing.T) {
	m, state, dismissals := noticeOnState(t)

	m = pressKeys(m, "esc")

	if *dismissals != 1 {
		t.Fatalf("DismissNotice called %d times, want 1", *dismissals)
	}
	if _, ok := state.Notice(); ok {
		t.Fatal("notice still set after Esc")
	}
}

// TestTickDismissalGoesThroughTheSession pins the TTL path: the tick clears
// the banner through the session, so a store-stamped runtime notice expires
// for every client and not just this model.
func TestTickDismissalGoesThroughTheSession(t *testing.T) {
	m, state, dismissals := noticeOnState(t)
	m.now = func() time.Time { return time.Unix(100+int64(noticeBannerDuration/time.Second)+1, 0) }

	m.handleAgentTick(agentTickMsg{})

	if *dismissals != 1 {
		t.Fatalf("DismissNotice called %d times, want 1", *dismissals)
	}
	if _, ok := state.Notice(); ok {
		t.Fatal("notice still set after the TTL tick")
	}
}
