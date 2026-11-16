package fleet

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/acpclient"
	"marshal/internal/app/tui/theme"
	"marshal/web/bridge/contract"
)

// diffState is a loaded, connected fleet of two agents: a1 with count changed
// files, a2 with one, so a cursor move reaches another agent.
func diffState(count int) acpclient.FleetState {
	state := loadedState(1)
	state.Agents = []contract.AgentStatus{
		{ID: "a1", Name: "Alpha", ChangedFiles: count},
		{ID: "a2", Name: "Bravo", ChangedFiles: 1},
	}
	state.Inbox.Groups.Running = []string{"a1", "a2"}
	return state
}

// diffFiles is a two-file answer, so a cursor move has a second file to reach.
func diffFiles() contract.DiffResult {
	return contract.DiffResult{
		Files: []contract.DiffFile{
			{Path: "a.go", Added: 1},
			{Path: "b.go", Added: 2},
		},
		Diff: "--- a/a.go\n+++ b/a.go\n@@ -1,1 +1,2 @@\n package a\n+// added\n",
	}
}

// listReads is how many of the fake's Diff calls asked for the file list.
func listReads(f *fakeFleet) int {
	n := 0
	for _, d := range f.diffs {
		if d.path == "" {
			n++
		}
	}
	return n
}

// reads runs the reads cmd carries — a diff read's answer and the read that
// answer starts — and drops every other message, the re-armed wait included:
// these tests drive the tab's reads, never the screen's loop.
func reads(t *testing.T, m Model, cmds ...tea.Cmd) Model {
	t.Helper()
	for len(cmds) > 0 {
		cmd := cmds[0]
		cmds = cmds[1:]
		if cmd == nil {
			continue
		}
		switch msg := cmd().(type) {
		case tea.BatchMsg:
			cmds = append(cmds, msg...)
		case diffReadMsg:
			next, follow := m.Update(msg)
			m = next.(Model)
			cmds = append(cmds, follow)
		}
	}
	return m
}

// deliverAndRead drives one "the client changed" update on state and runs the
// reads it starts. The change the fake fires answers the wait armed beside
// them, so the wait returns instead of blocking.
func deliverAndRead(t *testing.T, f *fakeFleet, m Model, state acpclient.FleetState) Model {
	t.Helper()
	f.state = state
	f.fire()
	next, cmd := m.Update(changedMsg{})
	return reads(t, next.(Model), cmd)
}

func TestTheDiffTabReadsTheFilesOnce(t *testing.T) {
	f := newFakeFleet()
	f.diffAnswer = diffFiles()
	m := sized(t, f.change(t, New(f, fixedClock()), diffState(2)))

	// Showing the tab reads the list, and the file under the cursor with it.
	m, cmd := press(t, m, tea.KeyPressMsg{Code: '2', Text: "2"})
	m = reads(t, m, cmd)
	if got := listReads(f); got != 1 {
		t.Fatalf("list reads after showing the tab = %d, want 1: %+v", got, f.diffs)
	}
	if got := len(f.diffs); got != 2 {
		t.Fatalf("reads after showing the tab = %d, want the list and one file: %+v", got, f.diffs)
	}

	// A second state with the same count reads nothing: the files are already
	// this agent's, at this count.
	m = deliverAndRead(t, f, m, diffState(2))
	if got := len(f.diffs); got != 2 {
		t.Fatalf("reads after a state with the same count = %d, want 2: %+v", got, f.diffs)
	}
	if got := listReads(f); got != 1 {
		t.Fatalf("list reads after a state with the same count = %d, want 1", got)
	}
	if got := m.diff.count; got != 2 {
		t.Fatalf("the files were re-read at count %d, want 2", got)
	}
}

func TestAChangedCountRereadsTheFiles(t *testing.T) {
	f := newFakeFleet()
	f.diffAnswer = diffFiles()
	m := sized(t, f.change(t, New(f, fixedClock()), diffState(2)))
	m, cmd := press(t, m, tea.KeyPressMsg{Code: '2', Text: "2"})
	m = reads(t, m, cmd)

	// The shown agent's count changed: the files it read are stale.
	m = deliverAndRead(t, f, m, diffState(3))
	if got := listReads(f); got != 2 {
		t.Fatalf("list reads after the count changed = %d, want 2: %+v", got, f.diffs)
	}
	if got := m.diff.count; got != 3 {
		t.Fatalf("the files were read at count %d, want 3", got)
	}
}

func TestTheCursorReadsTheFilesDiff(t *testing.T) {
	f := newFakeFleet()
	f.diffAnswer = diffFiles()
	m := sized(t, f.change(t, New(f, fixedClock()), diffState(2)))
	m, cmd := press(t, m, tea.KeyPressMsg{Code: '2', Text: "2"})
	m = reads(t, m, cmd)

	// The list's answer read the first file.
	if got, want := f.diffs[len(f.diffs)-1].path, "a.go"; got != want {
		t.Fatalf("the diff read after the list = %q, want %q: %+v", got, want, f.diffs)
	}

	// The files are the right panel's list, so a move needs its focus.
	m = tabTo(t, m, 3)
	m, cmd = press(t, m, tea.KeyPressMsg{Code: 'j', Text: "j"})
	m = reads(t, m, cmd)

	if got := m.diff.cursor; got != 1 {
		t.Fatalf("the file cursor after j = %d, want 1", got)
	}
	if got, want := f.diffs[len(f.diffs)-1].path, "b.go"; got != want {
		t.Fatalf("the diff read after j = %q, want %q: %+v", got, want, f.diffs)
	}
	if got, want := m.diff.path, "b.go"; got != want {
		t.Fatalf("the file the panel holds = %q, want %q", got, want)
	}
}

func TestALateDiffForAnotherAgentIsDropped(t *testing.T) {
	f := newFakeFleet()
	f.diffAnswer = contract.DiffResult{Files: []contract.DiffFile{{Path: "a.go"}}}
	m := sized(t, f.change(t, New(f, fixedClock()), diffState(1)))
	m, cmd := press(t, m, tea.KeyPressMsg{Code: '2', Text: "2"})
	m = reads(t, m, cmd)

	// The cursor moves to the other agent, whose files are read instead.
	f.diffAnswer = contract.DiffResult{Files: []contract.DiffFile{{Path: "b.go"}}}
	m, cmd = press(t, m, tea.KeyPressMsg{Code: 'j', Text: "j"})
	m = reads(t, m, cmd)
	if got := m.diff.agent; got != "a2" {
		t.Fatalf("the Diff tab holds agent %q, want a2", got)
	}

	// a1's answer arrives after the screen left it.
	next, _ := m.Update(diffReadMsg{
		agentID: "a1",
		result:  contract.DiffResult{Files: []contract.DiffFile{{Path: "late.go"}}, Diff: "late"},
	})
	m = next.(Model)

	if got := m.diff.files[0].Path; got != "b.go" {
		t.Fatalf("the files after the late answer = %q, want b.go", got)
	}
	if m.diff.patch != "" {
		t.Fatalf("the late answer's diff was shown: %q", m.diff.patch)
	}
}

func TestTheDiffHeaderTotals(t *testing.T) {
	f := newFakeFleet()
	f.diffAnswer = contract.DiffResult{
		Files: []contract.DiffFile{
			{Path: "a.go", Added: 3, Removed: 1},
			{Path: "b.go", Added: 2, Removed: 1},
		},
		Diff: "--- a/a.go\n+++ b/a.go\n@@ -1,1 +1,2 @@\n package a\n+// added\n",
	}
	m := sized(t, f.change(t, New(f, fixedClock()), diffState(2)))
	m, cmd := press(t, m, tea.KeyPressMsg{Code: '2', Text: "2"})
	m = reads(t, m, cmd)

	if got := ansi.Strip(m.View().Content); !strings.Contains(got, "2 files +5 −2") {
		t.Fatalf("the header does not read \"2 files +5 −2\":\n%s", got)
	}
	if got, want := diffTotalsText(2, 5, 2), "2 files +5 −2"; got != want {
		t.Fatalf("the header text = %q, want %q", got, want)
	}
}

// diffGoldens is the Diff tab at the two sizes plan 3 pins, on the same scene.
func diffGoldens() []fleetGolden {
	return []fleetGolden{
		{name: "fleet-100x30-diff", width: 100, height: 30, tab: diffTab},
		{name: "fleet-140x40-diff", width: 140, height: 40, tab: diffTab},
	}
}

// diffGoldenResult is the golden scene's read: two files, +5 −2 in all, and
// the first one's patch.
func diffGoldenResult() contract.DiffResult {
	return contract.DiffResult{
		Files: []contract.DiffFile{
			{Path: "internal/app/tui/fleet/diff.go", Added: 4},
			{Path: "internal/app/tui/fleet/keys.go", Added: 1, Removed: 2},
		},
		Diff: "--- a/internal/app/tui/fleet/diff.go\n" +
			"+++ b/internal/app/tui/fleet/diff.go\n" +
			"@@ -1,3 +1,7 @@\n" +
			" package fleet\n" +
			"+\n" +
			"+// diffTab is the Diff tab's key, as plan 3 reserved it.\n" +
			"+const diffTab = 2\n" +
			"+\n" +
			"+// diffMinus is the header's removed marker.\n" +
			" const diffMinus = \"−\"\n",
	}
}

// diffGoldenModel is plan 3's golden scene with the Diff tab shown: the tab is
// opened with its key, its reads answered, and the right panel focused, so the
// footer names the tab's keys.
func diffGoldenModel(t *testing.T, sc fleetGolden) Model {
	t.Helper()
	if sc.colour {
		t.Setenv("NO_COLOR", "")
	} else {
		t.Setenv("NO_COLOR", "1")
	}
	t.Setenv("TERM", "xterm-256color")
	theme.Reload(theme.LoadWithConfig("", "", theme.DepthFlat, nil))

	f := newFakeFleet()
	f.state = goldenSceneState(sc)
	f.diffAnswer = diffGoldenResult()
	m := New(f, WithNow(func() time.Time { return fleetNow }))
	m = deliver(t, m)
	m.events = fleetSceneEvents()
	next, _ := m.Update(tea.WindowSizeMsg{Width: sc.width, Height: sc.height})
	m = next.(Model)

	m, cmd := press(t, m, tea.KeyPressMsg{Code: '2', Text: "2"})
	m = reads(t, m, cmd)
	// focusAgents -> Needs you -> Events -> the right panel.
	return tabTo(t, m, 3)
}

// renderDiffFleet builds the frame twice and fails unless the two renders
// match, so a golden cannot quietly absorb nondeterminism.
func renderDiffFleet(t *testing.T, sc fleetGolden) string {
	t.Helper()
	first := diffGoldenModel(t, sc).View().Content
	second := diffGoldenModel(t, sc).View().Content
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

// TestTheDiffTabMatchesItsGoldens compares the two frames with the goldens plan
// 3's frames are compared with, and pins that neither overflows its terminal.
func TestTheDiffTabMatchesItsGoldens(t *testing.T) {
	for _, sc := range diffGoldens() {
		t.Run(sc.name, func(t *testing.T) {
			got := renderDiffFleet(t, sc)
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
