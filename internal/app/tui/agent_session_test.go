package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/acp/service"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/app/tui/glyph"
)

const (
	// testAgentID/testAgentName are the agent a fleet agent's session is
	// opened as, and the refusal every ending produces on it.
	testAgentID   = "agent-7"
	testAgentName = "scout"
	endRefusal    = "This is agent " + testAgentName + "'s session: finish it from the fleet screen."
)

// agentSessionModel is detachableModel with a fleet agent's session, as the
// fleet screen opens one: a daemon session, marked with the agent's id and
// name, over a command registry so the ending commands resolve.
func agentSessionModel(t *testing.T) (Model, *time.Time) {
	t.Helper()
	state := session.New(config.Default(), t.TempDir(), time.Unix(1000, 0), session.Persistence{SessionID: unnamedSessionID})
	m := testModelWith(t, service.Deps{State: state, Commands: setupCmdReg(t)},
		WithDetachOnQuit(), WithAgentSession(testAgentID, testAgentName))
	now := time.Unix(1000, 0)
	m.now = func() time.Time { return now }
	return m, &now
}

// TestEveryEndingIsRefusedOnAnAgentsSession: an agent's session belongs to the
// fleet, so every path that would end it notes the refusal, starts no end
// flow and leaves nothing detached (P5 decision 4).
func TestEveryEndingIsRefusedOnAnAgentsSession(t *testing.T) {
	cases := []struct {
		name string
		end  func(m Model) (tea.Model, tea.Cmd)
	}{
		{"/exit", func(m Model) (tea.Model, tea.Cmd) { return m.dispatchCommand("/exit") }},
		{"/quit", func(m Model) (tea.Model, tea.Cmd) { return m.dispatchCommand("/quit") }},
		{"/new", func(m Model) (tea.Model, tea.Cmd) { return m.dispatchCommand("/new") }},
		{"/clear", func(m Model) (tea.Model, tea.Cmd) { return m.dispatchCommand("/clear") }},
		{"a resume", func(m Model) (tea.Model, tea.Cmd) { return m.beginResume("sess-target") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := agentSessionModel(t)
			if m.AgentSession() != testAgentID {
				t.Fatalf("AgentSession() = %q, want %q", m.AgentSession(), testAgentID)
			}

			updated, cmd := tc.end(m)
			m = asModel(t, updated)
			if cmd != nil {
				t.Fatalf("%s returned a command", tc.name)
			}
			if m.endStage != endIdle || m.end != nil {
				t.Fatalf("%s started the end flow (stage %v)", tc.name, m.endStage)
			}
			if m.detached || m.Detached() != "" {
				t.Fatalf("%s recorded a detach", tc.name)
			}
			if got := m.ResumeSession(); got != "" {
				t.Fatalf("%s recorded the resume target %q", tc.name, got)
			}
			if n := countContaining(stateOf(m).Messages(), endRefusal); n != 1 {
				t.Fatalf("%s noted the refusal %d times, want exactly one", tc.name, n)
			}
			assertNotShutDown(t, m)
		})
	}
}

// TestDetachStillLeavesAnAgentsSession: leaving the screen is not ending the
// session, so /detach stays allowed on an agent's session.
func TestDetachStillLeavesAnAgentsSession(t *testing.T) {
	m, _ := agentSessionModel(t)
	wantID := m.sess.ID()

	updated, cmd := m.dispatchCommand("/detach")
	m = asModel(t, updated)
	if cmd == nil {
		t.Fatal("/detach returned no command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("/detach must quit the program")
	}
	if got := m.Detached(); got != wantID {
		t.Fatalf("Detached() = %q, want the session %q", got, wantID)
	}
	if m.ToFleet() {
		t.Fatal("/detach must not ask for the fleet screen")
	}
	assertNotShutDown(t, m)
}

// TestTheStatusLineNamesTheAgent: the agent segment follows the mode cue, in
// the identity style, and an ordinary session carries none.
func TestTheStatusLineNamesTheAgent(t *testing.T) {
	m, _ := agentSessionModel(t)

	segs := m.statusLeftSegments()
	if len(segs) < 2 {
		t.Fatalf("got %d status segments, want the mode and agent segments at least", len(segs))
	}
	want := glyph.Agent + " " + testAgentName
	if got := stripANSI(segs[1].text); got != want {
		t.Fatalf("segment after the mode cue = %q, want %q", got, want)
	}
	if segs[1].priority != 0 {
		t.Fatalf("agent segment priority = %d, want 0", segs[1].priority)
	}
	if segs[1].pinned {
		t.Fatal("the agent segment must not be pinned")
	}
	if !segs[0].pinned {
		t.Fatal("the mode segment must stay the pinned first segment")
	}
	if line := stripANSI(m.renderStatusLine(160)); !strings.Contains(line, want) {
		t.Fatalf("status line %q does not name the agent", line)
	}

	plain, _ := clockedModel(t)
	for _, seg := range plain.statusLeftSegments() {
		if strings.Contains(stripANSI(seg.text), glyph.Agent) {
			t.Fatalf("an ordinary session rendered an agent segment: %q", stripANSI(seg.text))
		}
	}
}

// TestSlashFleetDetachesToTheFleet: /fleet leaves the session running, as
// /detach does, and records that the fleet screen comes next.
func TestSlashFleetDetachesToTheFleet(t *testing.T) {
	m, _ := agentSessionModel(t)
	wantID := m.sess.ID()

	updated, cmd := m.dispatchCommand("/fleet")
	m = asModel(t, updated)
	if cmd == nil {
		t.Fatal("/fleet returned no command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("/fleet must quit the program")
	}
	if !m.ToFleet() {
		t.Fatal("/fleet did not record the fleet screen")
	}
	if got := m.Detached(); got != wantID {
		t.Fatalf("Detached() = %q, want the session %q", got, wantID)
	}
	assertNotShutDown(t, m)
}

// TestSlashFleetNeedsADaemonSession: an embedded session has no fleet screen to
// open, so /fleet refuses instead of quitting.
func TestSlashFleetNeedsADaemonSession(t *testing.T) {
	m, _ := clockedModel(t)

	updated, cmd := m.dispatchCommand("/fleet")
	m = asModel(t, updated)
	if cmd != nil {
		t.Fatal("/fleet in embedded mode returned a command")
	}
	if m.ToFleet() || m.detached || m.Detached() != "" {
		t.Fatal("/fleet in embedded mode must not leave the screen")
	}
	msgs := stateOf(m).Messages()
	if n := countContaining(msgs, "The fleet screen needs the daemon; this session runs embedded."); n != 1 {
		t.Fatalf("got %d refusal notes, want exactly one", n)
	}
	assertNotShutDown(t, m)
}
