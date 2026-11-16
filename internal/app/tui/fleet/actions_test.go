package fleet

import (
	"regexp"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/acpclient"
	"marshal/web/bridge/contract"
)

// ansiEscape is the theme's colour, which a test asserting on words drops.
var ansiEscape = regexp.MustCompile("\x1b\\[[0-9;]*[a-zA-Z]")

// textOf is the frame's words with its colour and its borders dropped, so a
// sentence a frame wraps across lines and panes still reads as one.
func textOf(view string) string {
	view = ansiEscape.ReplaceAllString(view, "")
	view = strings.NewReplacer(
		"│", " ", "─", " ", "╭", " ", "╮", " ", "╰", " ", "╯", " ",
		"┌", " ", "┐", " ", "└", " ", "┘", " ", "├", " ", "┤", " ",
	).Replace(view)
	return strings.Join(strings.Fields(view), " ")
}

// spawnProjects are the projects the spawn tests offer: the one the selected
// agent runs in, a second available one, and one that is not available.
func spawnProjects() []contract.ProjectStatus {
	return []contract.ProjectStatus{
		{Root: "/work/one", Available: true},
		{Root: "/work/two", Available: true},
		{Root: "/work/gone", Available: false},
	}
}

// spawnState is a loaded, connected fleet of one running agent in the first
// project, with the three projects above.
func spawnState() acpclient.FleetState {
	state := loadedState(1)
	state.Agents = []contract.AgentStatus{{
		ID:      "a1",
		Name:    "Alpha",
		Project: "/work/one",
		Status:  contract.AgentRunning,
	}}
	state.Inbox.Groups.Running = []string{"a1"}
	state.Projects = spawnProjects()
	return state
}

// spawning is the spawn state at 100×30, on the fixed clock.
func spawning(t *testing.T, f *fakeFleet) Model {
	t.Helper()
	return sized(t, f.change(t, New(f, fixedClock()), spawnState()))
}

// openedSpawn presses n and returns the modal it opened.
func openedSpawn(t *testing.T, m Model) (*spawnModal, Model) {
	t.Helper()
	m, cmd := press(t, m, tea.KeyPressMsg{Code: 'n', Text: "n"})
	if cmd != nil {
		t.Fatalf("n returned a command")
	}
	s, ok := m.modal.(*spawnModal)
	if !ok {
		t.Fatalf("the modal is %T, want the spawn modal", m.modal)
	}
	return s, m
}

// tabTo presses tab n times, moving the focus over the modal's fields.
func tabTo(t *testing.T, m Model, n int) Model {
	t.Helper()
	for i := 0; i < n; i++ {
		m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	}
	return m
}

// spawnFilled opens the spawn modal, types the name, cycles the mode spaces
// times and types the prompt, then enters it: the command the enter returned.
func spawnFilled(t *testing.T, f *fakeFleet, name, prompt string, spaces int) (Model, tea.Cmd) {
	t.Helper()
	m := spawning(t, f)
	_, m = openedSpawn(t, m)
	m = tabTo(t, m, 1) // the project, to the name
	m = typeText(t, m, name)
	m = tabTo(t, m, 1) // the name, to the mode
	for i := 0; i < spaces; i++ {
		m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	}
	m = tabTo(t, m, 1) // the mode, to the prompt
	m = typeText(t, m, prompt)
	return press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
}

func TestTheSpawnModalSpawnsWithItsFields(t *testing.T) {
	f := newFakeFleet()
	f.spawnAnswer = contract.SpawnResult{AgentID: "b1"}

	m, cmd := spawnFilled(t, f, "Bravo", "fix the build", 3)
	m = answer(t, m, cmd)

	if m.modal != nil {
		t.Fatalf("the modal stayed open after the spawn")
	}
	if len(f.spawns) != 1 {
		t.Fatalf("spawns = %+v, want one", f.spawns)
	}
	got := f.spawns[0]
	if got.Project != "/work/one" {
		t.Fatalf("spawned in %q, want the selected agent's project /work/one", got.Project)
	}
	if got.Name != "Bravo" {
		t.Fatalf("name = %q, want Bravo", got.Name)
	}
	if got.Mode != "act" {
		t.Fatalf("mode = %q, want act", got.Mode)
	}
	if got.Auto == nil || !*got.Auto {
		t.Fatalf("auto = %v, want true with act", got.Auto)
	}
	if got.Prompt != "fix the build" {
		t.Fatalf("prompt = %q, want fix the build", got.Prompt)
	}
	if m.selected != "b1" {
		t.Fatalf("selected after the spawn = %q, want the new agent b1", m.selected)
	}

	// The new agent is selected when the next state names it.
	next := spawnState()
	next.Agents = append(next.Agents, contract.AgentStatus{ID: "b1", Name: "Bravo", Project: "/work/one", Status: contract.AgentRunning})
	next.Inbox.Groups.Running = []string{"a1", "b1"}
	m = f.change(t, m, next)
	if m.selected != "b1" {
		t.Fatalf("selected = %q, want the new agent b1", m.selected)
	}
	if _, rows := m.agentLines(); len(rows) != 2 || m.agentCursor != 1 {
		t.Fatalf("the cursor is not on the new agent's row: rows %+v, cursor %d", rows, m.agentCursor)
	}
}

// TestTheSpawnModalLeavesTheDefaultModeOut covers decision 6's other side: on
// the project's default the request carries neither mode nor auto, so the
// bridge applies the project's own setting.
func TestTheSpawnModalLeavesTheDefaultModeOut(t *testing.T) {
	f := newFakeFleet()
	m, cmd := spawnFilled(t, f, "", "", 0)
	m = answer(t, m, cmd)

	if m.modal != nil {
		t.Fatalf("the modal stayed open after the spawn")
	}
	if len(f.spawns) != 1 {
		t.Fatalf("spawns = %+v, want one", f.spawns)
	}
	got := f.spawns[0]
	if got.Mode != "" || got.Auto != nil {
		t.Fatalf("mode, auto = %q, %v; want both left out on the project's default", got.Mode, got.Auto)
	}
	if got.Name != "" || got.Prompt != "" {
		t.Fatalf("name, prompt = %q, %q; want both empty", got.Name, got.Prompt)
	}
}

func TestASpawnWarningGoesToEvents(t *testing.T) {
	f := newFakeFleet()
	f.spawnAnswer = contract.SpawnResult{AgentID: "b1", Warning: "the workspace was seeded from a snapshot"}

	m, cmd := spawnFilled(t, f, "Bravo", "fix the build", 0)
	m = answer(t, m, cmd)

	if got, want := lastEvent(m), "15:04 the workspace was seeded from a snapshot"; got != want {
		t.Fatalf("last event = %q, want %q", got, want)
	}
	if got := m.statusText(); got != "" {
		t.Fatalf("status line = %q, want none for a warning", got)
	}
}

func TestCtrlRCyclesRecentPrompts(t *testing.T) {
	f := newFakeFleet()
	f.prompts = []string{"newest", "older"}
	m := spawning(t, f)
	_, m = openedSpawn(t, m)

	// The prompts are read once, when the modal opens, for the project it
	// opened on, at most 20 of them.
	if len(f.promptReads) != 1 {
		t.Fatalf("prompt reads = %+v, want one at the modal's open", f.promptReads)
	}
	if got := f.promptReads[0]; got.project != "/work/one" || got.limit != recentPromptLimit {
		t.Fatalf("prompt read = %+v, want /work/one and %d", got, recentPromptLimit)
	}

	m = tabTo(t, m, 3) // to the prompt field
	for i, want := range []string{"newest", "older"} {
		m, _ = press(t, m, tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl})
		s, ok := m.modal.(*spawnModal)
		if !ok {
			t.Fatalf("the modal is %T after ctrl+r %d", m.modal, i+1)
		}
		if got := s.prompt.Value(); got != want {
			t.Fatalf("prompt after ctrl+r %d = %q, want %q", i+1, got, want)
		}
	}
	if len(f.promptReads) != 1 {
		t.Fatalf("prompt reads = %+v, want the one the open made", f.promptReads)
	}
}

// noProjectState is a fleet whose only project cannot be spawned in: what the
// refusal sentence is for.
func noProjectState() acpclient.FleetState {
	state := spawnState()
	state.Projects = []contract.ProjectStatus{{Root: "/work/gone", Available: false}}
	return state
}

// refusedSpawn opens the spawn modal over a fleet whose only project is
// unavailable, presses enter, and answers the screen it shows.
func refusedSpawn(t *testing.T, state acpclient.FleetState) string {
	t.Helper()
	f := newFakeFleet()
	m := sized(t, f.change(t, New(f, fixedClock()), state))

	s, m := openedSpawn(t, m)
	if len(s.projects) != 0 {
		t.Fatalf("the modal offers %+v, want no project", s.projects)
	}

	m, cmd := press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil {
		t.Fatalf("enter returned a command")
	}
	if len(f.spawns) != 0 {
		t.Fatalf("spawns = %+v, want none", f.spawns)
	}
	return textOf(m.View().Content)
}

func TestTheSpawnModalNeedsAProject(t *testing.T) {
	state := noProjectState()
	// The local daemon's fleet carries its info, so the sentence names the pin
	// that can add a project here (decisions 6 and 11).
	state.Daemon = &contract.DaemonInfo{Profile: "local"}

	if view := refusedSpawn(t, state); !strings.Contains(view, noProjectText) {
		t.Fatalf("the modal does not say what to do without a project:\n%s", view)
	}
}

// TestTheSpawnRefusalOnTheDaemonNamesPin pins the daemon's half of decision 11:
// a project can be added from the web UI or pinned on this machine.
func TestTheSpawnRefusalOnTheDaemonNamesPin(t *testing.T) {
	state := noProjectState()
	state.Daemon = &contract.DaemonInfo{Profile: "local"}

	if view := refusedSpawn(t, state); !strings.Contains(view, "`marshal daemon pin`") {
		t.Fatalf("the daemon's refusal does not name the pin:\n%s", view)
	}
}

// TestTheSpawnRefusalOnAServerOmitsPin pins the server's half of decision 11: a
// server's state has no daemon info, and there is no daemon here to pin a
// project on, so the sentence names only the web UI.
func TestTheSpawnRefusalOnAServerOmitsPin(t *testing.T) {
	state := noProjectState()
	state.Daemon = nil

	view := refusedSpawn(t, state)
	if strings.Contains(view, "marshal daemon pin") {
		t.Fatalf("a server's refusal names the daemon's pin:\n%s", view)
	}
	if !strings.Contains(view, noProjectOnServerText) {
		t.Fatalf("a server's refusal does not say where a project is added:\n%s", view)
	}
}

func TestXCancelsARunningAgent(t *testing.T) {
	f := newFakeFleet()
	m := spawning(t, f)

	m, cmd := press(t, m, tea.KeyPressMsg{Code: 'x', Text: "x"})
	m = answer(t, m, cmd)

	if len(f.cancels) != 1 {
		t.Fatalf("cancels = %+v, want one", f.cancels)
	}
	if got := f.cancels[0]; got != "a1" {
		t.Fatalf("cancelled %q, want the selected agent a1", got)
	}
}

func TestXDoesNothingOnAnIdleAgent(t *testing.T) {
	f := newFakeFleet()
	state := spawnState()
	state.Agents[0].Status = contract.AgentIdle
	state.Inbox.Groups.Running = nil
	state.Inbox.Groups.Ready = []string{"a1"}
	m := sized(t, f.change(t, New(f, fixedClock()), state))

	m, cmd := press(t, m, tea.KeyPressMsg{Code: 'x', Text: "x"})
	if cmd != nil {
		t.Fatalf("x returned a command on an idle agent")
	}
	if len(f.cancels) != 0 {
		t.Fatalf("cancels = %+v, want none", f.cancels)
	}
	if got := m.statusText(); got != "" {
		t.Fatalf("status line = %q, want none", got)
	}
}

// intakeState is a loaded, connected fleet whose Needs you queue holds one
// submission, with that panel focused.
func intakeState(t *testing.T, f *fakeFleet) Model {
	t.Helper()
	state := loadedState(1)
	state.Inbox.Queue = []contract.InboxItem{{
		Kind:       contract.InboxItemIntake,
		Submission: &contract.PendingSubmission{ID: "s1", Title: "Ship the release", Origin: "github"},
	}}
	m := sized(t, f.change(t, New(f, fixedClock()), state))
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	if m.focus != focusNeedsYou {
		t.Fatalf("focus = %d, want the Needs you panel", m.focus)
	}
	return m
}

func TestEnterOnAnIntakeItemApprovesOrDenies(t *testing.T) {
	f := newFakeFleet()
	f.approveAnswer = contract.ApproveResult{AgentID: "i1", Status: "running"}
	m := intakeState(t, f)

	m, cmd := press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil {
		t.Fatalf("enter returned a command")
	}
	if _, ok := m.modal.(*intakePrompt); !ok {
		t.Fatalf("the modal is %T, want the intake prompt", m.modal)
	}
	view := m.View().Content
	for _, want := range []string{"approve Ship the release?", "[y]es [n]o, deny"} {
		if !strings.Contains(view, want) {
			t.Fatalf("the prompt does not show %q:\n%s", want, view)
		}
	}

	m, cmd = press(t, m, tea.KeyPressMsg{Code: 'y', Text: "y"})
	m = answer(t, m, cmd)
	if m.modal != nil {
		t.Fatalf("the prompt stayed open after the answer")
	}
	if len(f.approvals) != 1 || f.approvals[0] != "s1" {
		t.Fatalf("approvals = %+v, want the submission s1", f.approvals)
	}
	if m.selected != "i1" {
		t.Fatalf("selected after the approval = %q, want the spawned agent i1", m.selected)
	}

	// The same item again, denied this time.
	m, cmd = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil {
		t.Fatalf("enter returned a command")
	}
	m, cmd = press(t, m, tea.KeyPressMsg{Code: 'n', Text: "n"})
	m = answer(t, m, cmd)
	if len(f.denials) != 1 || f.denials[0] != "s1" {
		t.Fatalf("denials = %+v, want the submission s1", f.denials)
	}
	if len(f.approvals) != 1 {
		t.Fatalf("approvals = %+v, want still one: n denied nothing", f.approvals)
	}
}

// TestNOutsideTheApprovalIsSpawn covers task 4's deferral: n is the spawn key
// unless an approval is open, which owns y and n.
func TestNOutsideTheApprovalIsSpawn(t *testing.T) {
	f := newFakeFleet()
	m := spawning(t, f)

	m, cmd := press(t, m, tea.KeyPressMsg{Code: 'n', Text: "n"})
	if cmd != nil {
		t.Fatalf("n returned a command")
	}
	if _, ok := m.modal.(*spawnModal); !ok {
		t.Fatalf("n opened %T, want the spawn modal", m.modal)
	}
	if len(f.permissions) != 0 || len(f.questions) != 0 {
		t.Fatalf("n answered something: permissions %+v, questions %+v", f.permissions, f.questions)
	}
}
