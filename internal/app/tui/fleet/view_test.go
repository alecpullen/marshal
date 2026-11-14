package fleet

import (
	"encoding/json"
	"flag"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/acpclient"
	"marshal/internal/app/tui/glyph"
	"marshal/internal/app/tui/theme"
	"marshal/web/bridge/contract"
)

// updateGoldens rewrites the fleet goldens instead of comparing them.
var updateGoldens = flag.Bool("update", false, "rewrite the fleet goldens")

// fleetNow is the fixed clock every golden scene is rendered on. It lies in
// UTC so the gate's time of day is the same on every machine.
var fleetNow = time.Unix(1_700_000_000, 0).UTC()

// fleetGolden is one golden frame: a size, and the two scene variations.
type fleetGolden struct {
	name          string
	width, height int
	// tab is the right panel's tab: 1 Activity (the zero value), 3 Info.
	tab int
	// down renders the scene with the connection down.
	down bool
	// colour renders with the 256-colour theme instead of NO_COLOR.
	colour bool
}

// fleetGoldens is the six frames W3 asks for.
func fleetGoldens() []fleetGolden {
	return []fleetGolden{
		{name: "fleet-80x24", width: 80, height: 24},
		{name: "fleet-100x30", width: 100, height: 30},
		{name: "fleet-140x40", width: 140, height: 40},
		{name: "fleet-100x30-info", width: 100, height: 30, tab: 3},
		{name: "fleet-100x30-down", width: 100, height: 30, down: true},
		{name: "fleet-too-small", width: 79, height: 24},
	}
}

// fleetSceneState is the one fixed scene: five agents, one per group plus an
// error agent in earlier, an intake submission, two daemon sessions, the
// budgets loaded and one gate.
func fleetSceneState() acpclient.FleetState {
	at := func(d time.Duration) time.Time { return fleetNow.Add(-d) }
	return acpclient.FleetState{
		Server: "local",
		Conn:   acpclient.FleetConnected,
		Loaded: true,
		Epoch:  1,
		Agents: []contract.AgentStatus{
			{
				ID:           "a-needs-you",
				Name:         "fix-flaky-hooks",
				Project:      "/repo/marshal",
				OwnerID:      "alec",
				Origin:       "github",
				Access:       &contract.AccessState{Mode: "act"},
				Status:       contract.AgentAwaitingApproval,
				Activity:     "waiting on an approval",
				ContextPct:   38,
				ChangedFiles: 2,
				UpdatedAt:    at(2 * time.Minute),
				Branch:       "fix-flaky-hooks",
				TargetBranch: "main",
				Workspace:    &contract.AgentWorkspace{Name: "marshal", Source: contract.WorkspaceFromStudio},
				PRUrl:        "https://github.com/alecpullen/marshal/pull/69",
				Pending: &contract.PendingRequest{
					Kind:   contract.PendingApproval,
					ID:     "p1",
					Params: json.RawMessage(`{"command":"go test ./internal/app/tui/..."}`),
				},
			},
			{
				ID:           "b-running",
				Name:         "s3b-egress",
				Project:      "/repo/marshal",
				Access:       &contract.AccessState{Mode: "ask"},
				Status:       contract.AgentRunning,
				Activity:     "go test ./internal/...",
				ContextPct:   41,
				ChangedFiles: 7,
				UpdatedAt:    at(0),
			},
			{
				ID:           "c-ready",
				Name:         "docs-deploy",
				Project:      "/repo/notes",
				Access:       &contract.AccessState{Mode: "act"},
				Status:       contract.AgentIdle,
				Activity:     "ready to land",
				ContextPct:   22,
				ChangedFiles: 4,
				UpdatedAt:    at(time.Hour),
			},
			{
				ID:         "d-earlier",
				Name:       "docs-sweep",
				Project:    "/repo/notes",
				Status:     contract.AgentIdle,
				ContextPct: 12,
				UpdatedAt:  at(5 * time.Second),
			},
			{
				ID:         "e-error",
				Name:       "ci-fixer",
				Project:    "/repo/infra",
				Access:     &contract.AccessState{Mode: "act"},
				Status:     contract.AgentError,
				Activity:   "failed",
				ContextPct: 9,
				UpdatedAt:  at(3 * time.Hour),
			},
		},
		Inbox: contract.Inbox{
			Groups: contract.InboxGroups{
				NeedsYou: []string{"a-needs-you"},
				Running:  []string{"b-running"},
				Ready:    []string{"c-ready"},
				Earlier:  []string{"d-earlier", "e-error"},
			},
			Queue: []contract.InboxItem{
				{Kind: contract.InboxItemAgent, AgentID: "a-needs-you"},
				{
					Kind:       contract.InboxItemIntake,
					Submission: &contract.PendingSubmission{ID: "s1", Origin: "github", Title: "fix the flaky test"},
				},
			},
		},
		Budgets: &contract.BudgetReport{
			Budgets: contract.Budgets{PerAgentUSD: 10},
			Agents:  []contract.BudgetAgentSpend{{AgentID: "a-needs-you", SpentUSD: 1.25}},
			Loaded:  true,
		},
		Gates: map[string]contract.GateRecord{
			"a-needs-you": {Result: &contract.GateResult{OK: true}, At: at(2 * time.Minute)},
		},
		Daemon: &contract.DaemonInfo{
			Version: "0.1.0",
			Profile: "local",
			Children: []contract.DaemonChild{{
				Root: "/repo/marshal",
				Sessions: []contract.DaemonSession{
					{ID: "3f2a91bc7d", Viewers: 2, LastActive: at(3 * time.Minute).UnixMilli()},
					{ID: "9c1d4e5f6a", Viewers: 1, LastActive: at(90 * time.Second).UnixMilli()},
				},
			}},
		},
	}
}

// fleetSceneEvents is the fixed Events ring: three lines, newest last.
func fleetSceneEvents() []string {
	return []string{
		"14:58 connected to local",
		"14:59 docs-deploy is ready",
		"15:00 fix-flaky-hooks needs you: approval",
	}
}

// fleetModel builds the scene's model at a size. The theme is resolved from
// the test's environment, exactly as New does from the TUI config.
func fleetModel(t *testing.T, sc fleetGolden) Model {
	t.Helper()
	if sc.colour {
		t.Setenv("NO_COLOR", "")
	} else {
		t.Setenv("NO_COLOR", "1")
	}
	t.Setenv("TERM", "xterm-256color")
	theme.Reload(theme.LoadWithConfig("", "", theme.DepthFlat, nil))

	f := newFakeFleet()
	f.state = fleetSceneState()
	m := New(f, WithNow(func() time.Time { return fleetNow }))
	m = deliver(t, m)
	m.events = fleetSceneEvents()

	if sc.down {
		down := fleetSceneState()
		down.Conn = acpclient.FleetDown
		down.ConnErr = "dial tcp 127.0.0.1:7700: connect: connection refused"
		f.state = down
		m = deliver(t, m)
		// The down transition adds a line; the fixture's ring stays fixed.
		m.events = fleetSceneEvents()
	}
	if sc.tab != 0 {
		m.tab = sc.tab
	}
	next, _ := m.Update(tea.WindowSizeMsg{Width: sc.width, Height: sc.height})
	return next.(Model)
}

// renderFleet builds the scene twice and fails unless the two renders match, so
// a golden cannot quietly absorb nondeterminism.
func renderFleet(t *testing.T, sc fleetGolden) string {
	t.Helper()
	first := fleetModel(t, sc).View().Content
	second := fleetModel(t, sc).View().Content
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

// checkGolden compares got with testdata/<name>.golden, or rewrites the file
// under -update. The goldens hold text only: the escape sequences are stripped,
// because NO_COLOR keeps attributes (bold, faint) that the colour test checks
// separately.
func checkGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	text := ansi.Strip(got)
	if *updateGoldens {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v (run with -update to write the goldens)", path, err)
	}
	if string(want) == text {
		return
	}
	a := strings.Split(string(want), "\n")
	b := strings.Split(text, "\n")
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			t.Fatalf("%s: line %d differs:\nwant: %q\n got: %q\n\nfull render:\n%s", path, i+1, a[i], b[i], text)
		}
	}
	t.Fatalf("%s: %d lines, want %d\n\nfull render:\n%s", path, len(b), len(a), text)
}

// TestTheFleetScreenMatchesItsGoldens compares the six frames, and pins that
// none of them overflows its terminal.
func TestTheFleetScreenMatchesItsGoldens(t *testing.T) {
	for _, sc := range fleetGoldens() {
		t.Run(sc.name, func(t *testing.T) {
			got := renderFleet(t, sc)
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

// TestTheScreenNamesTheBridgeBeforeTheFirstRead is the regression for a model
// that did not seed itself from the client: until the first read landed the
// server's name was missing, so every panel said "Connecting to …" with
// nothing after the "to".
func TestTheScreenNamesTheBridgeBeforeTheFirstRead(t *testing.T) {
	f := newFakeFleet()
	f.state = acpclient.FleetState{Server: "local", Conn: acpclient.FleetConnecting}

	m := New(f, fixedClock())
	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	got := next.(Model).View().Content
	if !strings.Contains(got, "Connecting to local…") {
		t.Fatalf("the screen does not name the bridge before the first read:\n%s", got)
	}
}

// TestColourAddsNoText renders each frame with the 256-colour theme and checks
// that stripping its ANSI gives the golden byte for byte: colour changes how
// the screen looks, never what it says.
func TestColourAddsNoText(t *testing.T) {
	for _, sc := range fleetGoldens() {
		t.Run(sc.name, func(t *testing.T) {
			sc.colour = true
			got := ansi.Strip(renderFleet(t, sc))
			want, err := os.ReadFile(filepath.Join("testdata", sc.name+".golden"))
			if err != nil {
				t.Fatalf("read golden: %v (run with -update to write the goldens)", err)
			}
			if got == string(want) {
				return
			}
			a := strings.Split(string(want), "\n")
			b := strings.Split(got, "\n")
			for i := 0; i < len(a) && i < len(b); i++ {
				if a[i] != b[i] {
					t.Fatalf("%s: line %d differs:\nwant: %q\n got: %q", sc.name, i+1, a[i], b[i])
				}
			}
			t.Fatalf("%s: %d lines, want %d", sc.name, len(b), len(a))
		})
	}
}

// TestTheScreenAsksForTheAlternateScreenAndNoMouse pins decision 14: the
// alternate screen, and no mouse capture, so the terminal's own selection
// keeps working.
func TestTheScreenAsksForTheAlternateScreenAndNoMouse(t *testing.T) {
	v := New(newFakeFleet(), fixedClock()).View()

	if !v.AltScreen {
		t.Error("the screen does not ask for the alternate screen")
	}
	if v.MouseMode != tea.MouseModeNone {
		t.Errorf("the screen's mouse mode = %v, want none", v.MouseMode)
	}
}

// TestEachStateGlyphHasItsColour checks every ST state glyph is styled in its
// colour (decision 9): no state is told by colour alone, and none lacks its
// colour.
func TestEachStateGlyphHasItsColour(t *testing.T) {
	sc := fleetGolden{name: "fleet-100x30", width: 100, height: 30, colour: true}
	got := renderFleet(t, sc)

	theme.Reload(theme.LoadWithConfig("", "", theme.DepthFlat, nil))
	th := theme.Current()

	cases := []struct {
		name   string
		glyph  string
		colour color.Color
	}{
		{"needs you", glyph.Warning, th.StatusWarning},
		{"running", glyph.Live, th.StatusInfo},
		{"ready", glyph.OK, th.StatusSuccess},
		{"error", glyph.Error, th.StatusError},
		{"ambient", glyph.Ambient, th.FGMuted},
	}
	for _, tc := range cases {
		want := lipgloss.NewStyle().Foreground(tc.colour).Render(tc.glyph)
		if !strings.Contains(got, want) {
			t.Errorf("the %s glyph %q is not drawn in its colour; want %q", tc.name, tc.glyph, want)
		}
	}
}
