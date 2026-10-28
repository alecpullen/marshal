package castlist

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/tui/glyph"
	"marshal/internal/app/tui/layout"
	"marshal/internal/app/tui/theme"
	"marshal/internal/llm/routing"
)

var testTheme = theme.LoadFor(false, "xterm-256color")

func init() {
	theme.Reload(testTheme)
}

func TestNewPanelImplementsDockPanel(t *testing.T) {
	p := New("test", nil, nil, "agent")
	if p == nil {
		t.Fatal("New returned nil")
	}
	// Compile-time check: *Panel must satisfy dock.Panel.
	var _ interface {
		Update(msg tea.Msg) tea.Cmd
		View(width, maxHeight int) string
	} = p
}

func TestEnterEmitsStartMsgWhenUnblocked(t *testing.T) {
	p := New("test", []Row{
		{Title: "Alice", Detail: "planner", Badge: "ready"},
		{Title: "Bob", Detail: "implementer", Badge: "ready"},
	}, nil, "agent")

	cmd := p.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected non-nil cmd for Enter on unblocked panel")
	}
	msg := cmd()
	if _, ok := msg.(StartMsg); !ok {
		t.Fatalf("expected StartMsg, got %T", msg)
	}
}

func TestEnterBlockedWhenRowHasError(t *testing.T) {
	p := New("test", []Row{
		{Title: "Alice", Detail: "planner", Badge: "ready"},
		{Title: "Bob", Detail: "implementer", Badge: "error", Err: "model unavailable"},
	}, nil, "agent")

	if !p.blocked() {
		t.Fatal("expected blocked() to be true when a row has Err")
	}

	cmd := p.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("expected nil cmd for Enter when blocked")
	}
}

func TestEscEmitsCancelMsg(t *testing.T) {
	p := New("test", []Row{
		{Title: "Alice", Detail: "planner"},
	}, nil, "agent")

	cmd := p.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd == nil {
		t.Fatal("expected non-nil cmd for Esc")
	}
	msg := cmd()
	if _, ok := msg.(CancelMsg); !ok {
		t.Fatalf("expected CancelMsg, got %T", msg)
	}
}

func TestViewRendersRowsAndMetadata(t *testing.T) {
	p := New("Cast", []Row{
		{Title: "Alice", Detail: "planner", Badge: "ready"},
		{Title: "Bob", Detail: "implementer", Badge: "ready", Err: "model unavailable"},
	}, []string{"mode: sdd2", "model: minimax-m3"}, "agent")

	out := ansi.Strip(p.View(80, 20))
	lines := strings.Split(out, "\n")

	foundTitle := false
	foundMeta := false
	foundAlice := false
	foundBob := false
	foundErr := false
	foundBlocked := false

	for _, l := range lines {
		if strings.Contains(l, "Cast") {
			foundTitle = true
		}
		if strings.Contains(l, "mode: sdd2") {
			foundMeta = true
		}
		if strings.Contains(l, "Alice") {
			foundAlice = true
		}
		if strings.Contains(l, "Bob") {
			foundBob = true
		}
		if strings.Contains(l, "model unavailable") {
			foundErr = true
		}
		if strings.Contains(l, "fix errors") {
			foundBlocked = true
		}
	}

	if !foundTitle {
		t.Error("view missing title")
	}
	if !foundMeta {
		t.Error("view missing metadata")
	}
	if !foundAlice {
		t.Error("view missing Alice row")
	}
	if !foundBob {
		t.Error("view missing Bob row")
	}
	if !foundErr {
		t.Error("view missing error text")
	}
	if !foundBlocked {
		t.Error("view missing blocked indicator")
	}
}

func TestViewHintsChangeWhenBlocked(t *testing.T) {
	// Unblocked: hints should say "↵ start"
	p := New("Cast", []Row{
		{Title: "Alice"},
	}, nil, "agent")
	out := ansi.Strip(p.View(80, 20))
	if !strings.Contains(out, "start") {
		t.Errorf("unblocked view should contain 'start' hint, got:\n%s", out)
	}

	// Blocked: hints should say "↵ blocked"
	p2 := New("Cast", []Row{
		{Title: "Alice", Err: "error"},
	}, nil, "agent")
	out2 := ansi.Strip(p2.View(80, 20))
	if !strings.Contains(out2, "blocked") {
		t.Errorf("blocked view should contain 'blocked' hint, got:\n%s", out2)
	}
}

func TestViewTruncatesToHeightBudget(t *testing.T) {
	rows := make([]Row, 50)
	for i := range rows {
		rows[i] = Row{Title: "row"}
	}
	p := New("Cast", rows, nil, "agent")
	out := ansi.Strip(p.View(80, 5))
	got := len(strings.Split(out, "\n"))
	if got > 5 {
		t.Fatalf("view must not exceed height budget 5, got %d lines:\n%s", got, out)
	}
}

func TestWarnRowRendersButDoesNotBlock(t *testing.T) {
	p := New("Start run?", []Row{
		{Title: "implementer", Detail: "ollama/qwen"},
		{Title: "verify", Warn: "no build or test command configured"},
	}, nil, "agent")

	got := ansi.Strip(p.View(80, 20))
	if !strings.Contains(got, "no build or test command configured") {
		t.Errorf("warning text must render:\n%s", got)
	}
	if strings.Contains(got, "fix errors above") {
		t.Errorf("a warning must not render the blocked banner:\n%s", got)
	}
	if p.blocked() {
		t.Error("a warning row must not block the run")
	}
}

func TestErrRowStillBlocks(t *testing.T) {
	p := New("Start run?", []Row{{Title: "implementer", Err: "no model configured"}}, nil, "agent")
	if !p.blocked() {
		t.Error("an error row must still block")
	}
}

func TestStrategyCyclesOnLeftRight(t *testing.T) {
	panel := New("Start plan run?", nil, nil, "agent")
	// Right arrow should cycle agent -> adaptive.
	panel.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if panel.strategy != "adaptive" {
		t.Errorf("after right: strategy = %q, want %q", panel.strategy, "adaptive")
	}
	// Right again: adaptive -> strict.
	panel.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if panel.strategy != "strict" {
		t.Errorf("after right 2: strategy = %q, want %q", panel.strategy, "strict")
	}
	// Right again: strict -> agent (wraps).
	panel.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if panel.strategy != "agent" {
		t.Errorf("after right 3: strategy = %q, want %q", panel.strategy, "agent")
	}
}

func TestStrategyLeftCyclesBackward(t *testing.T) {
	panel := New("Start plan run?", nil, nil, "agent")
	panel.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	if panel.strategy != "strict" {
		t.Errorf("after left: strategy = %q, want %q", panel.strategy, "strict")
	}
}

func TestStartMsgCarriesStrategy(t *testing.T) {
	panel := New("Start plan run?", nil, nil, "adaptive")
	cmd := panel.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter should produce a command")
	}
	msg := cmd()
	start, ok := msg.(StartMsg)
	if !ok {
		t.Fatalf("msg = %T, want StartMsg", msg)
	}
	if start.Strategy != "adaptive" {
		t.Errorf("StartMsg.Strategy = %q, want %q", start.Strategy, "adaptive")
	}
}

func TestDisabledStrategyIsSkippedAndCannotStart(t *testing.T) {
	panel := New("Start plan run?", nil, nil, "adaptive")
	panel.SetStrategyOptions([]StrategyOption{
		{Value: "agent"},
		{Value: "adaptive"},
		{Value: "strict", DisabledReason: "fallback work remains"},
	})
	panel.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if panel.SelectedStrategy() != "agent" {
		t.Fatalf("selected strategy = %q, want agent", panel.SelectedStrategy())
	}
	panel.SetStrategy("strict")
	if cmd := panel.Update(tea.KeyPressMsg{Code: tea.KeyEnter}); cmd != nil {
		t.Fatal("disabled strict strategy must not emit StartMsg")
	}
}

// TestCastListSelectionVisibleAfterNavigation walks a cast list taller than its
// panel and checks that the selected role — and the first line of its wrapped
// diagnostics — is on screen after every move, survives resizes, and keeps the
// override it carries.
func TestCastListSelectionVisibleAfterNavigation(t *testing.T) {
	const width, height = 80, 12

	rows := make([]Row, 12)
	errText := make([]string, len(rows))
	warnText := make([]string, len(rows))
	for i := range rows {
		rows[i] = Row{
			Title:  fmt.Sprintf("role %02d", i),
			Role:   routing.AgentRole(fmt.Sprintf("role%02d", i)),
			Detail: "ollama/qwen3",
			Badge:  "ready",
		}
		if i%3 == 0 {
			errText[i] = fmt.Sprintf("error %02d: no provider matched this role's constraints and the fallback preset is still unresolved", i)
			rows[i].Err = errText[i]
		}
		if i%4 == 0 {
			warnText[i] = fmt.Sprintf("warn %02d: no build or test command is configured for this repository, so the verify gate stays unknown until one is added", i)
			rows[i].Warn = warnText[i]
		}
	}

	p := New("Cast", rows, []string{"mode: sdd2", "model: minimax-m3"}, "agent")
	frame := func(h int) string { return ansi.Strip(p.View(width, h)) }

	// assertVisible checks the height budget, the selection marker, and the
	// first line of the selected role's error and warning.
	assertVisible := func(t *testing.T, out string, h int) {
		t.Helper()
		if got := len(strings.Split(out, "\n")); got > h {
			t.Fatalf("view is %d lines, budget %d:\n%s", got, h, out)
		}
		if want := "▸ " + fmt.Sprintf("role %02d", p.cursor); !strings.Contains(out, want) {
			t.Fatalf("selected role %q is not visible:\n%s", want, out)
		}
		if errText[p.cursor] != "" && !strings.Contains(out, fmt.Sprintf("error %02d:", p.cursor)) {
			t.Fatalf("selected role's error is not visible:\n%s", out)
		}
		if warnText[p.cursor] != "" && !strings.Contains(out, fmt.Sprintf("warn %02d:", p.cursor)) {
			t.Fatalf("selected role's warning is not visible:\n%s", out)
		}
	}

	// The list overflows the panel, so the selection must scroll with it.
	assertVisible(t, frame(height), height)
	seen := map[routing.AgentRole]bool{}
	for i := 1; i < len(rows); i++ {
		p.Update(tea.KeyPressMsg{Code: tea.KeyDown})
		role := p.rows[p.cursor].Role
		if seen[role] {
			t.Fatalf("Down did not advance the selection: stuck on %q", role)
		}
		seen[role] = true
		assertVisible(t, frame(height), height)
	}

	// An override applied to the selected role must show on its row and stay
	// attached to that role.
	target := p.rows[p.cursor].Role
	p.setOverride(target, "opus-5")
	out := frame(height)
	for _, want := range []string{"opus-5", "override"} {
		if !strings.Contains(out, want) {
			t.Fatalf("overridden row is missing %q:\n%s", want, out)
		}
	}
	if got := p.Overrides()[target]; got != "opus-5" {
		t.Fatalf("override for %q = %q, want opus-5", target, got)
	}

	// Resizing must keep the selected role reachable and never render past the
	// budget. Navigating re-arms follow-the-selection after each resize.
	for _, h := range []int{20, 9, 6, 12} {
		if got := len(strings.Split(frame(h), "\n")); got > h {
			t.Fatalf("after resize to %d the view is %d lines:\n%s", h, got, frame(h))
		}
		p.Update(tea.KeyPressMsg{Code: tea.KeyDown})
		p.Update(tea.KeyPressMsg{Code: tea.KeyUp})
		assertVisible(t, frame(h), h)
	}
}

// TestCastListPagesEveryDiagnostic pages a blocked cast list and accumulates the
// frames to prove that every wrapped segment of every blocking reason, disabled
// reason and warning is reachable, that paging leaves the selected override
// target alone, and that Enter stays refused until both conditions clear.
func TestCastListPagesEveryDiagnostic(t *testing.T) {
	const width, height = 80, 8
	inner := layout.PanelWidth(width) - 3

	const (
		blockErr = "verify gate: no build or test command is configured, and the proposal left " +
			"the command list empty, so the gate cannot be evaluated"
		strictReason = "fallback work remains on three roles, so the strict strategy cannot be " +
			"selected until every fallback preset is replaced with an explicit model and the " +
			"router re-validates the cast"
		adaptiveReason = "adaptive needs a verify gate with at least one command before it can " +
			"choose between the fast and careful paths"
		longWarn = "no build or test command is configured for this repository, so the verify " +
			"gate stays unknown and the run will start without a post-implementation check"
	)

	p := New("Cast", []Row{
		{Title: "scout", Role: routing.AgentRole("scout"), Detail: "ollama/qwen3", Badge: "ready"},
		{Title: "planner", Role: routing.AgentRole("planner"), Detail: "ollama/qwen3", Badge: "ready"},
		{Title: "implementer", Role: routing.AgentRole("implementer"), Detail: "ollama/qwen3", Badge: "ready", Warn: longWarn},
		{Title: "reviewer", Role: routing.AgentRole("reviewer"), Detail: "ollama/qwen3", Badge: "ready"},
		{Title: "verify gate", Err: blockErr},
	}, []string{"mode: sdd2", "model: minimax-m3"}, "agent")
	p.SetStrategyOptions([]StrategyOption{
		{Value: "agent"},
		{Value: "adaptive", DisabledReason: adaptiveReason},
		{Value: "strict", DisabledReason: strictReason},
	})
	p.SetStrategy("strict")

	// Enter is refused while the gate row errors and the selected strategy is
	// disabled.
	if !p.blocked() {
		t.Fatal("panel must be blocked before the conditions clear")
	}
	if cmd := p.Update(tea.KeyPressMsg{Code: tea.KeyEnter}); cmd != nil {
		t.Fatal("Enter must not start a blocked run")
	}

	// Park the cursor on the row carrying the long warning and give it an
	// override: paging must move neither.
	for i := 0; p.rows[p.cursor].Role != routing.AgentRole("implementer"); i++ {
		if i > len(p.rows) {
			t.Fatalf("cursor never reached the implementer row, stuck on %q", p.rows[p.cursor].Role)
		}
		p.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	}
	p.setOverride(routing.AgentRole("implementer"), "opus-5")

	var frames []string
	render := func() string {
		out := ansi.Strip(p.View(width, height))
		frames = append(frames, out)
		if got := len(strings.Split(out, "\n")); got > height {
			t.Fatalf("view is %d lines, budget %d:\n%s", got, height, out)
		}
		return out
	}
	render()
	// Start the page-through at the top of the body so the accumulation covers
	// the strategy rows as well as everything below them.
	p.Update(tea.KeyPressMsg{Code: tea.KeyHome})
	render()
	for i := 0; i < 20; i++ {
		p.Update(tea.KeyPressMsg{Code: tea.KeyPgDown})
		render()
	}
	all := strings.Join(frames, "\n")

	// Every wrapped segment of every diagnostic must appear in some frame.
	warnPrefix := glyph.Warning + " "
	diags := []struct {
		name   string
		text   string
		budget int
	}{
		{"blocking error", blockErr, inner - 4},
		{"selected strategy reason", strictReason, inner - 4},
		{"disabled option reason", "  adaptive: " + adaptiveReason, inner - 2},
		{"later warning", longWarn, inner - 4 - ansi.StringWidth(warnPrefix)},
	}
	for _, d := range diags {
		segments := wrapSegments(d.text, d.budget)
		if len(segments) < 2 {
			t.Fatalf("%s did not wrap into several segments at width %d: %q", d.name, width, segments)
		}
		for i, seg := range segments {
			if !strings.Contains(all, seg) {
				t.Errorf("%s segment %d/%d is unreachable by paging: %q", d.name, i+1, len(segments), seg)
			}
		}
	}

	// The overflow affordances must be there, and the override of a row that
	// starts off screen must be reachable too.
	if !strings.Contains(all, "↓ more") || !strings.Contains(all, "↑ more") {
		t.Error("a paged body must show both overflow indicators")
	}
	if !strings.Contains(all, "⇞⇟ page") {
		t.Error("overflowing bodies must advertise the paging keys in the hints")
	}
	if !regexp.MustCompile(`\d+–\d+/\d+`).MatchString(all) {
		t.Errorf("a paged body must show its position:\n%s", all)
	}
	if !strings.Contains(all, "opus-5") {
		t.Errorf("the overridden row never became reachable:\n%s", all)
	}

	// Home and End are independent of the selection.
	if out := homeEnd(t, p, tea.KeyHome, width, height); !strings.Contains(out, "mode: sdd2") {
		t.Errorf("Home must return to the top of the body:\n%s", out)
	}
	if out := homeEnd(t, p, tea.KeyEnd, width, height); !strings.Contains(out, "fix errors above") {
		t.Errorf("End must reach the blocked banner:\n%s", out)
	}

	// Paging must not have moved the override target.
	if got := p.rows[p.cursor].Role; got != routing.AgentRole("implementer") {
		t.Fatalf("paging moved the selection to %q", got)
	}
	if got := p.Overrides()[routing.AgentRole("implementer")]; got != "opus-5" {
		t.Fatalf("after paging the override target is %q, want implementer", got)
	}

	// Clearing one condition is not enough.
	for i := range p.rows {
		if p.rows[i].Title == "verify gate" {
			p.rows[i].Err = ""
		}
	}
	if !p.blocked() {
		t.Fatal("the disabled strategy must still block once the row error clears")
	}
	if cmd := p.Update(tea.KeyPressMsg{Code: tea.KeyEnter}); cmd != nil {
		t.Fatal("Enter must stay refused while the selected strategy is disabled")
	}

	// Clearing both lets the run start, with the override that survived paging.
	p.SetStrategy("agent")
	if p.blocked() {
		t.Fatal("panel must be unblocked once the strategy is selectable")
	}
	cmd := p.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter must start once the conditions clear")
	}
	start, ok := cmd().(StartMsg)
	if !ok {
		t.Fatalf("msg = %T, want StartMsg", cmd())
	}
	if start.Strategy != "agent" {
		t.Errorf("StartMsg.Strategy = %q, want agent", start.Strategy)
	}
	if got := start.Overrides[routing.AgentRole("implementer")]; got != "opus-5" {
		t.Errorf("StartMsg.Overrides[implementer] = %q, want opus-5", got)
	}
}

// wrapSegments reports the trimmed, non-empty lines ansi.Wrap produces for text
// at the given budget: the physical lines the panel must be able to show.
func wrapSegments(text string, budget int) []string {
	var out []string
	for _, seg := range strings.Split(ansi.Wrap(text, max(budget, 1), ""), "\n") {
		if seg = strings.TrimSpace(seg); seg != "" {
			out = append(out, seg)
		}
	}
	return out
}

// homeEnd presses one of Home/End and returns the rendered frame.
func homeEnd(t *testing.T, p *Panel, code rune, width, height int) string {
	t.Helper()
	p.Update(tea.KeyPressMsg{Code: code})
	out := ansi.Strip(p.View(width, height))
	if got := len(strings.Split(out, "\n")); got > height {
		t.Fatalf("view is %d lines, budget %d:\n%s", got, height, out)
	}
	return out
}
