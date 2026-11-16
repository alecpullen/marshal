package tui

import (
	"strings"
	"testing"
	"time"

	"marshal/internal/acp/service"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
)

// busyCommandModel builds a model whose agent is ready and whose turn is in
// flight, so a command reaches its busy refusal rather than its AgentReady
// refusal.
func busyCommandModel(t *testing.T) Model {
	t.Helper()
	home := t.TempDir()
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{SessionID: unnamedSessionID})
	runner := &fakeAgentRunner{called: make(chan string, 8)}
	m := testModelWith(t, service.Deps{
		State:    state,
		Commands: setupCmdReg(t),
		Runner:   runnerOf(runner),
		Auth:     newMemAuth(),
		HomeDir:  home,
	}, WithHomeDir(home))
	m.resize(80, 24)
	m.busy = true
	return m
}

// TestBusyCommandRefusalExplainsItself pins AUD-12: every command that refuses
// to run while a turn is in flight says so, in one note that names the command
// and the reason. Before this, Enter had already cleared the composer and the
// command vanished with no explanation at all.
func TestBusyCommandRefusalExplainsItself(t *testing.T) {
	cases := []struct {
		name     string
		line     string
		wantCmd  string
		wantWhat string
	}{
		{"new", "/new", "/new", "a new conversation"},
		{"clear", "/clear", "/clear", "a new conversation"},
		{"swarm", "/swarm refactor the parser", "/swarm", "a swarm run"},
		{"sdd plan", "/sdd .marshal/plans/p1.md", "/sdd", "a plan run"},
		{"sdd new", "/sdd new add a --fix flag", "/sdd", "plan authoring"},
		{"review", "/review the working tree", "/review", "a review"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := busyCommandModel(t)
			before := len(stateOf(m).Messages())

			updated, cmd := m.dispatchCommand(tc.line)
			m = asModel(t, updated)

			if cmd != nil {
				t.Fatalf("a busy %s returned a cmd (%T), want nil", tc.wantCmd, cmd)
			}
			msgs := stateOf(m).Messages()
			if len(msgs) != before+1 {
				t.Fatalf("a busy %s added %d notes, want exactly one", tc.wantCmd, len(msgs)-before)
			}
			note := msgs[len(msgs)-1].Content
			if !strings.Contains(note, tc.wantCmd) {
				t.Errorf("refusal does not name the command: %q", note)
			}
			if !strings.Contains(note, "a turn is running") {
				t.Errorf("refusal does not name the reason: %q", note)
			}
			if !strings.Contains(note, tc.wantWhat) {
				t.Errorf("refusal does not name what was refused (%q): %q", tc.wantWhat, note)
			}
			// Nothing ran: no ending opened, no panel opened.
			if m.endStage != endIdle {
				t.Errorf("a busy %s opened the end flow (stage %v)", tc.wantCmd, m.endStage)
			}
			if m.dock.Panel() != nil {
				t.Errorf("a busy %s opened a panel (%T)", tc.wantCmd, m.dock.Panel())
			}
		})
	}
}

// TestBusyRefusalKeepsTheDraftInTheComposer pins the second half of AUD-12: an
// argument-heavy line is put back so it is not retyped. The Enter handler has
// already cleared the composer by the time the refusal runs.
func TestBusyRefusalKeepsTheDraftInTheComposer(t *testing.T) {
	for _, line := range []string{
		"/swarm refactor the parser",
		"/sdd .marshal/plans/p1.md",
		"/sdd new add a --fix flag",
		"/review the working tree",
	} {
		m := busyCommandModel(t)
		m.input.SetValue("") // what the Enter handler leaves behind

		updated, _ := m.dispatchCommand(line)
		m = asModel(t, updated)

		if got := m.input.Value(); got != line {
			t.Errorf("composer = %q after a busy refusal, want the submitted %q", got, line)
		}
	}
}

// TestBusyRefusalDoesNotStealANewerDraft pins that the restore is a no-op when
// the composer already holds something the user typed after submitting.
func TestBusyRefusalDoesNotStealANewerDraft(t *testing.T) {
	m := busyCommandModel(t)
	m.input.SetValue("/help")

	updated, _ := m.dispatchCommand("/swarm refactor the parser")
	m = asModel(t, updated)

	if got := m.input.Value(); got != "/help" {
		t.Fatalf("composer = %q, want the user's newer draft /help", got)
	}
}
