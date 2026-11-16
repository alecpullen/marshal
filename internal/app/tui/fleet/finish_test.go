package fleet

// finish_test.go is the finish flow's tests (decisions 2 to 8): the gate key,
// the modal's steps, and the answers a merge, an exit and a discard give.

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/acpclient"
	"marshal/internal/app/tui/theme"
	"marshal/web/bridge/contract"
)

// finishAgent is the finish tests' agent: Alpha, idle, on branch marshal/alpha
// with main as its target.
func finishAgent() contract.AgentStatus {
	return contract.AgentStatus{
		ID:           "a1",
		Name:         "Alpha",
		Status:       contract.AgentIdle,
		ChangedFiles: 2,
		Branch:       "marshal/alpha",
		TargetBranch: "main",
	}
}

// finishState is a loaded, connected fleet of that one idle agent in Ready.
func finishState() acpclient.FleetState {
	state := loadedState(1)
	state.Agents = []contract.AgentStatus{finishAgent()}
	state.Inbox.Groups.Ready = []string{"a1"}
	return state
}

// gitFinishState is the same agent on a git source, so its work pushes — or,
// read-only, leaves a patch.
func gitFinishState(readOnly bool) acpclient.FleetState {
	state := finishState()
	state.Agents[0].SourceKind = "git"
	state.Agents[0].ReadOnly = readOnly
	return state
}

// finishing is the state at 100×30 on the fixed clock.
func finishing(t *testing.T, f *fakeFleet, state acpclient.FleetState) Model {
	t.Helper()
	return sized(t, f.change(t, New(f, fixedClock()), state))
}

// openedFinish presses f and answers its draft read.
func openedFinish(t *testing.T, m Model) (*finishModal, Model) {
	t.Helper()
	m, cmd := press(t, m, tea.KeyPressMsg{Code: 'f', Text: "f"})
	modal, ok := m.modal.(*finishModal)
	if !ok {
		t.Fatalf("f opened %T, want the finish modal", m.modal)
	}
	if cmd == nil {
		t.Fatal("f returned no command, want the draft's read")
	}
	return modal, answer(t, m, cmd)
}

// finishPress presses one key and answers the command it returned, if any.
func finishPress(t *testing.T, m Model, msg tea.KeyPressMsg) Model {
	t.Helper()
	m, cmd := press(t, m, msg)
	if cmd == nil {
		return m
	}
	next, _ := m.Update(cmd())
	got, ok := next.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want fleet.Model", next)
	}
	return got
}

// enterFinish presses ⏎.
func enterFinish(t *testing.T, m Model) Model {
	t.Helper()
	return finishPress(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
}

// finishScreen is the whole screen's words.
func finishScreen(m Model) string {
	return textOf(m.View().Content)
}

func TestFinishIsRefusedWhileRunning(t *testing.T) {
	for _, status := range []contract.AgentState{
		contract.AgentRunning,
		contract.AgentAwaitingApproval,
		contract.AgentAwaitingQuestion,
	} {
		t.Run(string(status), func(t *testing.T) {
			f := newFakeFleet()
			state := finishState()
			state.Agents[0].Status = status
			m := finishing(t, f, state)

			m, cmd := press(t, m, tea.KeyPressMsg{Code: 'f', Text: "f"})
			if cmd != nil {
				t.Fatalf("f returned a command, want none")
			}
			if m.modal != nil {
				t.Fatalf("f opened %T, want no modal", m.modal)
			}
			want := "finish needs Alpha idle: cancel its turn first (x)"
			if got := m.statusText(); got != want {
				t.Fatalf("the status line = %q, want %q", got, want)
			}
			if len(f.commitDrafts) != 0 {
				t.Fatalf("the draft was read for an agent that cannot finish: %v", f.commitDrafts)
			}
		})
	}
}

func TestTheMessageStartsFromTheDraft(t *testing.T) {
	f := newFakeFleet()
	f.commitDraftAnswer = contract.CommitDraft{Message: "marshal: finish the panel"}
	m := finishing(t, f, finishState())

	modal, _ := openedFinish(t, m)
	if modal.step != finishMessage {
		t.Fatalf("the modal is on step %d, want the message step", modal.step)
	}
	if got, want := modal.message.Value(), "marshal: finish the panel"; got != want {
		t.Fatalf("the message = %q, want the draft %q", got, want)
	}
	if len(f.commitDrafts) != 1 || f.commitDrafts[0] != "a1" {
		t.Fatalf("the draft reads = %v, want one for a1", f.commitDrafts)
	}
}

func TestAFailedDraftLeavesTheMessageEmpty(t *testing.T) {
	f := newFakeFleet()
	f.commitDraftErr = errors.New("no draft for this agent")
	m := finishing(t, f, finishState())

	modal, _ := openedFinish(t, m)
	if got := modal.message.Value(); got != "" {
		t.Fatalf("the message = %q, want it empty", got)
	}
	if modal.step != finishMessage {
		t.Fatalf("the modal is on step %d, want the message step", modal.step)
	}
}

func TestALocalAgentMerges(t *testing.T) {
	f := newFakeFleet()
	f.mergeAnswer = contract.MergeResult{Merged: true, Branch: "marshal/alpha", Target: "main"}
	m := finishing(t, f, finishState())

	modal, m := openedFinish(t, m)
	m = typeText(t, m, "finish the fleet screen")
	m = enterFinish(t, m)

	if len(f.merges) != 1 {
		t.Fatalf("merges = %+v, want one", f.merges)
	}
	if got, want := f.merges[0].agentID, "a1"; got != want {
		t.Fatalf("the merge named %q, want %q", got, want)
	}
	if got, want := f.merges[0].req.CommitMessage, "finish the fleet screen"; got != want {
		t.Fatalf("the merge's message = %q, want %q", got, want)
	}
	if len(f.exits) != 0 {
		t.Fatalf("a local agent exited: %+v", f.exits)
	}

	if modal.step != finishResult {
		t.Fatalf("the modal is on step %d, want the result", modal.step)
	}
	if got := finishScreen(m); !strings.Contains(got, "merged marshal/alpha into main") {
		t.Fatalf("the result does not name the branch and the target:\n%s", got)
	}
	if got, want := lastEvent(m), "15:04 merged marshal/alpha into main"; got != want {
		t.Fatalf("the last event = %q, want %q", got, want)
	}
}

func TestADirtyMergeAsksForAMessageAndRetries(t *testing.T) {
	f := newFakeFleet()
	f.mergeAnswer = contract.MergeResult{Merged: false, Reason: "dirty"}
	m := finishing(t, f, finishState())

	modal, m := openedFinish(t, m)
	m = enterFinish(t, m)

	if len(f.merges) != 1 {
		t.Fatalf("merges = %+v, want one", f.merges)
	}
	if got := f.merges[0].req.CommitMessage; got != "" {
		t.Fatalf("the first merge carried %q, want no message", got)
	}
	if modal.step != finishMessage {
		t.Fatalf("the refusal moved the modal to step %d, want the message step", modal.step)
	}
	if got := finishScreen(m); !strings.Contains(got, "give a commit message") {
		t.Fatalf("the refusal does not ask for a message:\n%s", got)
	}

	// The message the refusal asks for is what the retry carries.
	f.mergeAnswer = contract.MergeResult{Merged: true, Branch: "marshal/alpha", Target: "main"}
	m = typeText(t, m, "fix the flake")
	m = enterFinish(t, m)

	if len(f.merges) != 2 {
		t.Fatalf("merges = %+v, want the retry as a second one", f.merges)
	}
	if got, want := f.merges[1].req.CommitMessage, "fix the flake"; got != want {
		t.Fatalf("the retry's message = %q, want %q", got, want)
	}
	if modal.step != finishResult {
		t.Fatalf("the retry is on step %d, want the result", modal.step)
	}
}

func TestAConflictSaysWhere(t *testing.T) {
	f := newFakeFleet()
	f.mergeAnswer = contract.MergeResult{
		Merged:    false,
		Reason:    "conflicts",
		Branch:    "marshal/alpha",
		Target:    "main",
		Conflicts: []string{"keys.go", "view.go"},
	}
	m := finishing(t, f, finishState())

	modal, m := openedFinish(t, m)
	m = enterFinish(t, m)

	if len(f.merges) != 1 {
		t.Fatalf("the refusal retried: %+v", f.merges)
	}
	if modal.step != finishMessage {
		t.Fatalf("the refusal is on step %d, want the message step", modal.step)
	}
	screen := finishScreen(m)
	for _, want := range []string{"marshal/alpha into main", "keys.go, view.go"} {
		if !strings.Contains(screen, want) {
			t.Fatalf("the refusal does not say %q:\n%s", want, screen)
		}
	}
}

func TestAGitAgentExits(t *testing.T) {
	f := newFakeFleet()
	f.exitAnswer = contract.ExitResult{
		Destination: "push",
		Branch:      "marshal/alpha",
		PRUrl:       "https://example.test/alec/marshal/pull/7",
	}
	m := finishing(t, f, gitFinishState(false))

	modal, m := openedFinish(t, m)
	m = typeText(t, m, "ship it")
	m = enterFinish(t, m)

	if len(f.exits) != 1 {
		t.Fatalf("exits = %+v, want one", f.exits)
	}
	if got, want := f.exits[0].req.CommitMessage, "ship it"; got != want {
		t.Fatalf("the exit's message = %q, want %q", got, want)
	}
	if got := f.exits[0].req.Override; got != nil {
		t.Fatalf("the first exit carried an override: %+v", got)
	}
	if len(f.merges) != 0 {
		t.Fatalf("a git agent merged: %+v", f.merges)
	}
	if modal.step != finishResult {
		t.Fatalf("the modal is on step %d, want the result", modal.step)
	}
	screen := finishScreen(m)
	if !strings.Contains(screen, "pushed marshal/alpha") {
		t.Fatalf("the result does not name the pushed branch:\n%s", screen)
	}
	// The panel clips a long URL, so the result itself is what names it.
	if got, want := strings.Join(modal.result, " "), "pull request https://example.test/alec/marshal/pull/7"; !strings.Contains(got, want) {
		t.Fatalf("the result = %q, want it to name %q", got, want)
	}
}

// blockedAnswer is the answer of a push the gate refused.
func blockedAnswer() contract.ExitResult {
	return contract.ExitResult{
		Destination: "push",
		Blocked:     true,
		Verify: &contract.GateResult{
			FailedCommand: "go test ./internal/app/tui/fleet/",
			Output:        "--- FAIL: TestTheFinishModal\n    view_test.go:371: line 3 differs\nexit status 1\n",
		},
	}
}

func TestABlockedExitOffersTheOverride(t *testing.T) {
	f := newFakeFleet()
	f.exitAnswer = blockedAnswer()
	m := finishing(t, f, gitFinishState(false))

	modal, m := openedFinish(t, m)
	m = enterFinish(t, m)

	if modal.step != finishBlocked {
		t.Fatalf("the blocked push is on step %d, want the blocked step", modal.step)
	}
	screen := finishScreen(m)
	for _, want := range []string{"go test ./internal/app/tui/fleet/", "--- FAIL: TestTheFinishModal", "exit status 1"} {
		if !strings.Contains(screen, want) {
			t.Fatalf("the blocked push does not show %q:\n%s", want, screen)
		}
	}

	// A reason is what overrides the gate: the exit runs again with it.
	f.exitAnswer = contract.ExitResult{Destination: "push", Branch: "marshal/alpha"}
	m = typeText(t, m, "the suite is known flaky")
	m = enterFinish(t, m)

	if len(f.exits) != 2 {
		t.Fatalf("exits = %+v, want the override's as a second one", f.exits)
	}
	override := f.exits[1].req.Override
	if override == nil || override.Reason != "the suite is known flaky" {
		t.Fatalf("the second exit's override = %+v, want the typed reason", override)
	}
	if modal.step != finishResult {
		t.Fatalf("the override is on step %d, want the result", modal.step)
	}
}

func TestAnEmptyOverrideReasonIsRefused(t *testing.T) {
	f := newFakeFleet()
	f.exitAnswer = blockedAnswer()
	m := finishing(t, f, gitFinishState(false))

	modal, m := openedFinish(t, m)
	m = enterFinish(t, m)
	m = enterFinish(t, m)

	if len(f.exits) != 1 {
		t.Fatalf("an empty reason exited again: %+v", f.exits)
	}
	if modal.step != finishBlocked {
		t.Fatalf("the empty reason moved the modal to step %d, want the blocked step", modal.step)
	}
	if got := finishScreen(m); !strings.Contains(got, emptyReasonText) {
		t.Fatalf("the field does not refuse the empty reason:\n%s", got)
	}
}

func TestAnExitToAnotherDestinationSaysSo(t *testing.T) {
	f := newFakeFleet()
	f.exitAnswer = contract.ExitResult{Destination: "patch"}
	m := finishing(t, f, gitFinishState(false))

	modal, m := openedFinish(t, m)
	m = enterFinish(t, m)

	if modal.step != finishResult {
		t.Fatalf("the modal is on step %d, want the result", modal.step)
	}
	screen := finishScreen(m)
	if !strings.Contains(screen, "patch") {
		t.Fatalf("the result does not say the work is a patch:\n%s", screen)
	}
	if strings.Contains(screen, "pushed") {
		t.Fatalf("an exit that answered a patch pretended it pushed:\n%s", screen)
	}
}

func TestDiscardNeedsTheNameTyped(t *testing.T) {
	f := newFakeFleet()
	m := finishing(t, f, finishState())

	modal, m := openedFinish(t, m)
	m = finishPress(t, m, tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	if modal.step != finishDiscard {
		t.Fatalf("ctrl+d is on step %d, want the discard's confirmation", modal.step)
	}

	// A wrong name calls nothing.
	m = typeText(t, m, "alpha")
	m = enterFinish(t, m)
	if len(f.discards) != 0 {
		t.Fatalf("a wrong name discarded: %v", f.discards)
	}
	if modal.step != finishDiscard {
		t.Fatalf("the wrong name moved the modal to step %d", modal.step)
	}
	if got := finishScreen(m); !strings.Contains(got, "type Alpha to discard") {
		t.Fatalf("the confirmation does not name what to type:\n%s", got)
	}

	// The exact name discards.
	for i := 0; i < len("alpha"); i++ {
		m = finishPress(t, m, tea.KeyPressMsg{Code: tea.KeyBackspace})
	}
	m = typeText(t, m, "Alpha")
	m = enterFinish(t, m)

	if len(f.discards) != 1 || f.discards[0] != "a1" {
		t.Fatalf("discards = %v, want one for a1", f.discards)
	}
	if modal.step != finishResult {
		t.Fatalf("the discard is on step %d, want the result", modal.step)
	}
}

func TestEscClosesTheModalWithoutActing(t *testing.T) {
	for _, step := range []finishStep{finishMessage, finishDiscard} {
		t.Run("", func(t *testing.T) {
			f := newFakeFleet()
			m := finishing(t, f, finishState())
			_, m = openedFinish(t, m)
			if step == finishDiscard {
				m = finishPress(t, m, tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
			}

			m = finishPress(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})
			if m.modal != nil {
				t.Fatalf("esc left %T open", m.modal)
			}
			if n := len(f.merges) + len(f.exits) + len(f.discards); n != 0 {
				t.Fatalf("esc acted %d times, want none", n)
			}
		})
	}
}

func TestDestinationFollowsTheSource(t *testing.T) {
	cases := []struct {
		name  string
		agent contract.AgentStatus
		want  string
	}{
		{name: "local", agent: contract.AgentStatus{SourceKind: "local"}, want: "merge"},
		{name: "no source", agent: contract.AgentStatus{}, want: "merge"},
		{name: "read-only git", agent: contract.AgentStatus{SourceKind: "git", ReadOnly: true}, want: "patch"},
		{name: "git", agent: contract.AgentStatus{SourceKind: "git"}, want: "push"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := destination(c.agent); got != c.want {
				t.Fatalf("destination(%+v) = %q, want %q", c.agent, got, c.want)
			}
		})
	}
}

// TestVerifyRunsTheGate covers decision 2: v on the Info tab runs the gate, the
// status line reads "verifying…" while the call is out, and a refused call is
// an action failure.
func TestVerifyRunsTheGate(t *testing.T) {
	f := newFakeFleet()
	f.verifyAnswer = contract.GateRecord{Result: &contract.GateResult{OK: true}, At: eventTime}
	m := finishing(t, f, finishState())
	m.focus = focusRight
	m.tab = infoTab

	m, cmd := press(t, m, tea.KeyPressMsg{Code: 'v', Text: "v"})
	if got := m.statusText(); got != "verifying…" {
		t.Fatalf("the status line = %q, want \"verifying…\"", got)
	}
	if cmd == nil {
		t.Fatal("v returned no command")
	}
	m = answer(t, m, cmd)
	if len(f.verifies) != 1 || f.verifies[0] != "a1" {
		t.Fatalf("verifies = %v, want one for a1", f.verifies)
	}
	if got := m.statusText(); got != "" {
		t.Fatalf("the status line after the gate = %q, want none", got)
	}

	f.verifyErr = errors.New("the gate could not run")
	_, cmd = press(t, m, tea.KeyPressMsg{Code: 'v', Text: "v"})
	m = answer(t, m, cmd)
	if got := m.statusText(); !strings.Contains(got, "verify Alpha failed") {
		t.Fatalf("the refused gate's status line = %q", got)
	}
	if got := lastEvent(m); !strings.Contains(got, "verify Alpha failed") {
		t.Fatalf("the refused gate's event = %q", got)
	}
}

// The finish flow's two golden scenes.
const (
	sceneFinish  = "finish"
	sceneBlocked = "blocked"
)

// finishGoldens is the message step and a blocked push, at plan 3's size.
func finishGoldens() []fleetGolden {
	return []fleetGolden{
		{name: "fleet-100x30-finish", width: 100, height: 30, scene: sceneFinish},
		{name: "fleet-100x30-blocked", width: 100, height: 30, scene: sceneBlocked},
	}
}

// finishGoldenState is the golden scene with an idle agent to finish: its
// ready row, on a git source when the frame pins a push.
func finishGoldenState(sc fleetGolden) acpclient.FleetState {
	state := goldenSceneState(sc)
	for i := range state.Agents {
		if state.Agents[i].ID != "c-ready" {
			continue
		}
		state.Agents[i].Status = contract.AgentIdle
		state.Agents[i].Branch = "marshal/docs-deploy"
		state.Agents[i].TargetBranch = "main"
		if sc.scene == sceneBlocked {
			state.Agents[i].SourceKind = "git"
		}
	}
	return state
}

// finishGoldenModel is the golden scene on its idle agent, with the modal
// opened by f and its draft answered: the message step, or a blocked push.
func finishGoldenModel(t *testing.T, sc fleetGolden) Model {
	t.Helper()
	if sc.colour {
		t.Setenv("NO_COLOR", "")
	} else {
		t.Setenv("NO_COLOR", "1")
	}
	t.Setenv("TERM", "xterm-256color")
	theme.Reload(theme.LoadWithConfig("", "", theme.DepthFlat, nil))

	f := newFakeFleet()
	f.state = finishGoldenState(sc)
	f.commitDraftAnswer = contract.CommitDraft{Message: "marshal: finish the job"}
	f.exitAnswer = blockedAnswer()
	m := New(f, WithNow(func() time.Time { return fleetNow }))
	m = deliver(t, m)
	m.events = fleetSceneEvents()
	next, _ := m.Update(tea.WindowSizeMsg{Width: sc.width, Height: sc.height})
	m = next.(Model)

	// The scene's ready row is the idle agent f can finish.
	m.selectRow("c-ready")
	if sc.scene == sceneBlocked {
		m, cmd := press(t, m, tea.KeyPressMsg{Code: 'f', Text: "f"})
		m = answer(t, m, cmd)
		m = enterFinish(t, m)
		return m
	}
	_, m = openedFinish(t, m)
	return m
}

// renderFinishFleet builds the frame twice and fails unless the two renders
// match, so a golden cannot quietly absorb nondeterminism.
func renderFinishFleet(t *testing.T, sc fleetGolden) string {
	t.Helper()
	first := finishGoldenModel(t, sc).View().Content
	second := finishGoldenModel(t, sc).View().Content
	if first == second {
		return first
	}
	a := strings.Split(first, "\n")
	b := strings.Split(second, "\n")
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			t.Fatalf("scene %s is not deterministic: line %d differs:\nfirst: %q\nsecond: %q", sc.name, i+1, a[i], b[i])
		}
	}
	t.Fatalf("scene %s is not deterministic: %d lines, then %d", sc.name, len(a), len(b))
	return ""
}

// TestTheFinishModalMatchesItsGoldens compares the two frames with the goldens
// the other tabs are compared with, and pins that neither overflows.
func TestTheFinishModalMatchesItsGoldens(t *testing.T) {
	for _, sc := range finishGoldens() {
		t.Run(sc.name, func(t *testing.T) {
			got := renderFinishFleet(t, sc)
			for i, line := range strings.Split(got, "\n") {
				if n := ansi.StringWidth(line); n > sc.width {
					t.Errorf("line %d is %d cells, wider than the terminal's %d: %q", i+1, n, sc.width, ansi.Strip(line))
				}
			}
			if lines := strings.Count(got, "\n") + 1; lines > sc.height {
				t.Errorf("render is %d rows, taller than the terminal's %d", lines, sc.height)
			}
			checkGolden(t, sc.name, got)
		})
	}
}
