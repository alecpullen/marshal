package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/acpclient/inproc"
	"marshal/internal/acpwire"
	"marshal/internal/app/session"
	"marshal/internal/postmortem"
)

// The end tests speak the wire's end types directly: the constants below are
// aliases for acpwire's own, so a test reads like it names the seam.
const (
	EndIntentExit    = acpwire.EndIntentExit
	EndIntentReplace = acpwire.EndIntentReplace
)

// testModelEnding builds a model whose session's end service is end.
func testModelEnding(state *session.State, end inproc.EndService, opts ...Option) Model {
	host, sess := inproc.New(inproc.Deps{State: state, End: end})
	return New(host, sess, opts...)
}

// withEndService replaces m's session with one over m's state whose end service
// is end, as withPromptRunner does for the runner. The model's host is left as it
// was, so it stays paired with the session this replaces: a test that also drives
// a swap must build both, as TestNewSessionSwapsTheClientSession does.
func withEndService(m Model, end inproc.EndService) Model {
	_, sess := inproc.New(inproc.Deps{State: stateOf(m), End: end})
	m.sess = sess
	return m
}

// sessionClosed reports whether err is the wire's session-closed refusal: what
// a closed session answers every call with.
func sessionClosed(err error) bool {
	var wireErr *acpwire.Error
	return errors.As(err, &wireErr) && wireErr.Code == acpwire.CodeSessionClosed
}

// hasSystemLine reports whether the state holds a system message with content.
func hasSystemLine(state *session.State, content string) bool {
	for _, msg := range state.Messages() {
		if msg.Role == session.RoleSystem && msg.Content == content {
			return true
		}
	}
	return false
}

// TestQuitClosesTheSession pins that quitting closes the session through the
// seam: the pending approval is settled as a denial, the state is shut down,
// and a later call is refused as closed.
func TestQuitClosesTheSession(t *testing.T) {
	state := postmortemTestState(t, "sess-close")
	state.Config.Postmortem.OnExit = "never"
	m := testModelEnding(state, &endTestController{})

	responses := make(chan session.UserApprovalDecision, 1)
	state.SetPendingApproval(&session.PendingToolCall{
		ID:           "call-1",
		Name:         "shell.run",
		Command:      "rm -rf build",
		ResponseChan: responses,
	})

	_, cmd := runEndCmd(t, m, m.beginEnd(&endIntent{kind: acpwire.EndIntentExit}))
	if cmd == nil {
		t.Fatal("the exit returned no command, want the quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("cmd() = %T, want tea.QuitMsg", cmd())
	}
	select {
	case d := <-responses:
		if d.Approved {
			t.Error("the pending approval was approved, want a denial")
		}
	default:
		t.Error("the pending approval was not settled by quitting")
	}
	select {
	case <-state.Done():
	default:
		t.Error("the state is not shut down after quitting")
	}
	if err := m.sess.Stop(context.Background(), "stop"); !sessionClosed(err) {
		t.Errorf("Stop after quitting = %v, want the session-closed code", err)
	}
}

// TestAnExitWithoutAnEndServiceStillQuits pins the no-runtime fallback: an end
// has nothing to warn about or settle, so it proceeds through the session's
// own settlement and quits.
func TestAnExitWithoutAnEndServiceStillQuits(t *testing.T) {
	state := postmortemTestState(t, "sess-noend")
	state.Config.Postmortem.OnExit = "never"
	m := testModel(state)

	settle := m.beginEnd(&endIntent{kind: acpwire.EndIntentExit})
	if settle == nil {
		t.Fatal("an exit without an end service returned no command, want the settlement")
	}
	_, cmd := runEndCmd(t, m, settle)
	if cmd == nil {
		t.Fatal("the exit did not quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("cmd() = %T, want tea.QuitMsg", cmd())
	}
}

// TestTheExitPostmortemIsWrittenThroughTheSession pins that the report is built
// and written agent side, where the TUI's exit path wrote it before: under the
// user's home, in the project's postmortem directory, named for the session.
func TestTheExitPostmortemIsWrittenThroughTheSession(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	state := postmortemTestState(t, "sess-written")
	state.AddMessage(session.RoleUser, "hello", session.ContentTypePlain)
	state.Config.Postmortem.OnExit = "always"
	m := testModelEnding(state, &endTestController{})

	m, cmd := runEndCmd(t, m, m.beginEnd(&endIntent{kind: acpwire.EndIntentExit}))
	if cmd == nil {
		t.Fatal("the always path did not quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("cmd() = %T, want tea.QuitMsg", cmd())
	}
	path := filepath.Join(postmortem.Dir(home, postmortem.ProjectSlug(state.WorkingDir)), state.SessionID()+".json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the session did not write the report at %s: %v", path, err)
	}
	if want := "Postmortem written to " + path; !hasSystemLine(state, want) {
		t.Errorf("the session did not report the report it wrote: want %q in the transcript", want)
	}
}
