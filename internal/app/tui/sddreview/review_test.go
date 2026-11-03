package sddreview

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/acpwire"
	"marshal/internal/app/tui/dock"
	"marshal/internal/app/tui/theme"
)

var testTheme = theme.LoadFor(false, "xterm-256color")

func init() {
	theme.Reload(testTheme)
}

func inspectionFixture() *acpwire.PlanInspection {
	return &acpwire.PlanInspection{
		Strategy:  acpwire.StrategyAdaptive,
		HasBlocks: true,
		DetOps:    1,
		Tasks:     []acpwire.PlanTask{{TaskN: 1, Title: "Add file", DetOps: 1}},
	}
}

func cleanResult() acpwire.AuthoredPlan {
	return acpwire.AuthoredPlan{
		PlanPath:   "/repo/.marshal/plans/feature.md",
		Inspection: inspectionFixture(),
	}
}

func blockedResult() acpwire.AuthoredPlan {
	insp := inspectionFixture()
	insp.Diagnostics = []acpwire.PlanDiagnostic{{TaskN: 0, Severity: acpwire.DiagnosticError, Message: "blocked"}}
	return acpwire.AuthoredPlan{
		PlanPath:   "/repo/.marshal/plans/feature.md",
		Inspection: insp,
	}
}

// rendered returns a panel that has been drawn once at a supported geometry,
// which is what earns the right to act on the discard key.
func rendered(t *testing.T, result acpwire.AuthoredPlan) *Panel {
	t.Helper()
	p := New(result)
	p.View(80, 20)
	return p
}

func TestPanelRendersCandidateAndMetadata(t *testing.T) {
	p := rendered(t, cleanResult())
	out := ansi.Strip(p.View(80, 20))
	for _, want := range []string{"feature.md", "1 task", "deterministic", "1", "adaptive", "esc keep · d discard"} {
		if !strings.Contains(out, want) {
			t.Errorf("view missing %q:\n%s", want, out)
		}
	}
}

func TestEnterEmitsAcceptWhenNoErrors(t *testing.T) {
	p := New(cleanResult())
	cmd := p.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter should emit a command when inspection has no errors")
	}
	msg := cmd()
	accept, ok := msg.(AcceptMsg)
	if !ok {
		t.Fatalf("msg = %T, want AcceptMsg", msg)
	}
	if accept.Panel != p {
		t.Fatalf("AcceptMsg.Panel = %p, want the originating panel %p", accept.Panel, p)
	}
}

func TestEnterEmitsNoCommandWhenErrorsExist(t *testing.T) {
	p := New(blockedResult())
	if cmd := p.Update(tea.KeyPressMsg{Code: tea.KeyEnter}); cmd != nil {
		t.Fatal("Enter must not emit a command when inspection has errors")
	}
}

func TestEscEmitsKeep(t *testing.T) {
	p := rendered(t, cleanResult())
	cmd := p.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd == nil {
		t.Fatal("Esc should emit a command")
	}
	keep, ok := cmd().(KeepMsg)
	if !ok {
		t.Fatalf("msg = %T, want KeepMsg", cmd())
	}
	if keep.Panel != p {
		t.Fatalf("KeepMsg.Panel = %p, want the originating panel %p", keep.Panel, p)
	}
}

// TestEscKeepsBlockedCandidate pins the recovery path: a candidate that cannot
// be accepted must still be keepable, because the user's next move is to edit
// it and run /sdd.
func TestEscKeepsBlockedCandidate(t *testing.T) {
	p := rendered(t, blockedResult())
	cmd := p.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd == nil {
		t.Fatal("Esc must keep a blocked candidate")
	}
	if _, ok := cmd().(KeepMsg); !ok {
		t.Fatalf("msg = %T, want KeepMsg", cmd())
	}
}

// TestEscKeepsNilInspectionCandidate covers a result with no inspection at all:
// blocked for acceptance, still recoverable.
func TestEscKeepsNilInspectionCandidate(t *testing.T) {
	p := rendered(t, acpwire.AuthoredPlan{PlanPath: "/repo/.marshal/plans/feature.md"})
	if cmd := p.Update(tea.KeyPressMsg{Code: tea.KeyEnter}); cmd != nil {
		t.Fatal("a nil inspection must not be acceptable")
	}
	cmd := p.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd == nil {
		t.Fatal("a nil inspection must still be keepable")
	}
	if _, ok := cmd().(KeepMsg); !ok {
		t.Fatalf("msg = %T, want KeepMsg", cmd())
	}
}

func TestDiscardRequiresTwoConsecutiveKeys(t *testing.T) {
	p := rendered(t, cleanResult())

	if cmd := p.Update(tea.KeyPressMsg{Code: 'd'}); cmd != nil {
		t.Fatalf("first d must only arm the confirmation, got %T", cmd())
	}
	out := ansi.Strip(p.View(80, 20))
	if !strings.Contains(out, "d again to discard") {
		t.Fatalf("armed panel must state the confirmation:\n%s", out)
	}
	if !strings.Contains(out, "feature.md") {
		t.Fatalf("armed panel must name the candidate it would delete:\n%s", out)
	}

	cmd := p.Update(tea.KeyPressMsg{Code: 'd'})
	if cmd == nil {
		t.Fatal("second consecutive d must confirm")
	}
	discard, ok := cmd().(DiscardMsg)
	if !ok {
		t.Fatalf("msg = %T, want DiscardMsg", cmd())
	}
	if discard.Panel != p {
		t.Fatalf("DiscardMsg.Panel = %p, want the originating panel %p", discard.Panel, p)
	}
}

func TestDiscardAnyOtherKeyDisarms(t *testing.T) {
	for _, key := range []string{"j", "k", "enter", "x", "up"} {
		t.Run(key, func(t *testing.T) {
			p := rendered(t, cleanResult())
			if cmd := p.Update(tea.KeyPressMsg{Code: 'd'}); cmd != nil {
				t.Fatalf("first d must only arm, got %T", cmd())
			}
			// Any other key breaks the "two consecutive" pair. Enter may emit
			// AcceptMsg on a clean candidate, which is its own answer.
			p.Update(press(key))
			if cmd := p.Update(tea.KeyPressMsg{Code: 'd'}); cmd != nil {
				t.Fatalf("%q did not disarm the confirmation; next d emitted %T", key, cmd())
			}
		})
	}
}

// press maps a key name to the keypress the tests send.
func press(name string) tea.KeyPressMsg {
	codes := map[string]rune{
		"enter": tea.KeyEnter, "esc": tea.KeyEscape, "up": tea.KeyUp,
		"down": tea.KeyDown, "pgup": tea.KeyPgUp, "pgdown": tea.KeyPgDown,
		"home": tea.KeyHome, "end": tea.KeyEnd,
	}
	if code, ok := codes[name]; ok {
		return tea.KeyPressMsg{Code: code}
	}
	return tea.KeyPressMsg{Code: rune(name[0])}
}

func TestEscWhileArmedOnlyDisarms(t *testing.T) {
	p := rendered(t, cleanResult())
	p.Update(tea.KeyPressMsg{Code: 'd'})
	if cmd := p.Update(tea.KeyPressMsg{Code: tea.KeyEscape}); cmd != nil {
		t.Fatalf("Esc must disarm the pending discard and stay, got %T", cmd())
	}
	// The panel stayed open and a further Esc keeps the candidate.
	cmd := p.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd == nil {
		t.Fatal("the second Esc must keep the candidate")
	}
	if _, ok := cmd().(KeepMsg); !ok {
		t.Fatalf("msg = %T, want KeepMsg", cmd())
	}
}

// TestDiscardRefusedBeforeAnyRender pins the visibility rule at the panel
// level: a panel that never drew its candidate cannot act on `d`.
func TestDiscardRefusedBeforeAnyRender(t *testing.T) {
	p := New(cleanResult())
	if cmd := p.Update(tea.KeyPressMsg{Code: 'd'}); cmd != nil {
		t.Fatalf("unrendered panel must not arm, got %T", cmd())
	}
	if cmd := p.Update(tea.KeyPressMsg{Code: 'd'}); cmd != nil {
		t.Fatalf("unrendered panel must not confirm, got %T", cmd())
	}
}

// TestDiscardRefusedBelowMinimumGeometry pins the undersized rule: a render
// that cannot show the candidate and the action does not earn the permission.
func TestDiscardRefusedBelowMinimumGeometry(t *testing.T) {
	p := rendered(t, cleanResult())
	p.View(80, 2) // below the header + body + action minimum
	if cmd := p.Update(tea.KeyPressMsg{Code: 'd'}); cmd != nil {
		t.Fatalf("undersized render must not arm, got %T", cmd())
	}
	if cmd := p.Update(tea.KeyPressMsg{Code: 'd'}); cmd != nil {
		t.Fatalf("undersized render must not confirm, got %T", cmd())
	}
}

// TestDiscardRefusedBelowMinimumInnerWidth pins the narrow rule: a panel too
// narrow to show the candidate and the action does not earn the permission,
// even when the height is generous.
func TestDiscardRefusedBelowMinimumInnerWidth(t *testing.T) {
	p := New(cleanResult())
	out := ansi.Strip(p.View(16, 20)) // a 14-cell interior, under minReviewInner
	if strings.Contains(out, "d discard") {
		t.Fatalf("a too-narrow render must not show the discard action:\n%s", out)
	}
	if cmd := p.Update(tea.KeyPressMsg{Code: 'd'}); cmd != nil {
		t.Fatalf("narrow render must not arm, got %T", cmd())
	}
	if cmd := p.Update(tea.KeyPressMsg{Code: 'd'}); cmd != nil {
		t.Fatalf("narrow render must not confirm, got %T", cmd())
	}
}

// TestActionRowKeepsIdentityWhenNarrow pins the confirmation row's content at
// the narrowest interiors the panel accepts: the label may be abbreviated, but
// the row must still name what `d` would delete and the key that declines.
func TestActionRowKeepsIdentityWhenNarrow(t *testing.T) {
	for _, width := range []int{22, 26, 30, 40, 80} {
		p := rendered(t, cleanResult())
		p.View(width, 20)
		p.discardArmed = true
		row := ansi.Strip(p.actionRow(p.CandidatePath()))
		if got := ansi.StringWidth(row); got > p.inner {
			t.Errorf("width %d: armed row is %d cells, over the %d-cell interior: %q",
				width, got, p.inner, row)
		}
		if !strings.Contains(row, "esc keep") {
			t.Errorf("width %d: armed row dropped the decline key: %q", width, row)
		}
		if !strings.Contains(row, "repo") {
			t.Errorf("width %d: armed row dropped the candidate identity: %q", width, row)
		}
	}
}

// TestActionRowKeepsDeclineWhenNarrow pins the same guarantee for a failed
// discard, whose message is the reason the panel is still open.
func TestActionRowKeepsDeclineWhenNarrow(t *testing.T) {
	for _, width := range []int{22, 30, 80} {
		p := rendered(t, cleanResult())
		p.View(width, 20)
		p.err = errors.New("permission denied")
		row := ansi.Strip(p.actionRow(p.CandidatePath()))
		if got := ansi.StringWidth(row); got > p.inner {
			t.Errorf("width %d: error row is %d cells, over the %d-cell interior: %q",
				width, got, p.inner, row)
		}
		if !strings.Contains(row, "esc keep") {
			t.Errorf("width %d: error row dropped the decline key: %q", width, row)
		}
		if !strings.Contains(row, "permissi") {
			t.Errorf("width %d: error row dropped the failure: %q", width, row)
		}
	}
}

// TestInvalidateVisibilityDisarms pins the resize rule: an arm does not
// survive a geometry change, and the next render re-earns it.
func TestInvalidateVisibilityDisarms(t *testing.T) {
	p := rendered(t, cleanResult())
	p.Update(tea.KeyPressMsg{Code: 'd'})
	p.InvalidateVisibility()
	if cmd := p.Update(tea.KeyPressMsg{Code: 'd'}); cmd != nil {
		t.Fatalf("a disarmed panel must not confirm, got %T", cmd())
	}

	p.View(120, 40) // the fresh render re-earns the permission
	p.Update(tea.KeyPressMsg{Code: 'd'})
	cmd := p.Update(tea.KeyPressMsg{Code: 'd'})
	if cmd == nil {
		t.Fatal("a freshly rendered panel must be able to arm and confirm again")
	}
	if _, ok := cmd().(DiscardMsg); !ok {
		t.Fatalf("msg = %T, want DiscardMsg", cmd())
	}
}

func TestSetErrorDisplaysAndDisarms(t *testing.T) {
	p := rendered(t, cleanResult())
	p.Update(tea.KeyPressMsg{Code: 'd'})
	p.SetError(errors.New("sddauthor: candidate is not a regular file"))

	out := ansi.Strip(p.View(80, 20))
	if !strings.Contains(out, "discard failed") {
		t.Fatalf("failed discard must be reported in the panel:\n%s", out)
	}
	if !strings.Contains(out, "not a regular file") {
		t.Fatalf("failed discard must name the error:\n%s", out)
	}
	// Disarmed: the next d arms again rather than deleting on a stale arm.
	if cmd := p.Update(tea.KeyPressMsg{Code: 'd'}); cmd != nil {
		t.Fatalf("a failed discard must disarm, got %T", cmd())
	}
}

// TestSetErrorEscapesControlCharacters keeps a hostile error or path from
// reaching the terminal as an escape sequence.
func TestSetErrorEscapesControlCharacters(t *testing.T) {
	p := rendered(t, cleanResult())
	p.SetError(errors.New("bad\x1b[2Kpath"))
	out := p.View(80, 20)
	if strings.Contains(out, "\x1b[2K") {
		t.Fatalf("raw escape sequence reached the view:\n%q", out)
	}
}

func TestPanelNeverStartsRunner(t *testing.T) {
	// The panel has no reference to a runner; it only emits Accept/Keep/Discard.
	p := New(cleanResult())
	cmd := p.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter should emit a command")
	}
	if _, ok := cmd().(AcceptMsg); !ok {
		t.Fatalf("panel must emit AcceptMsg, not start a runner")
	}
}

// longBlockedResult is a blocked inspection whose diagnostics are much wider
// than the panel at the supported minimum, and whose candidate path is long
// enough to wrap. Each diagnostic is marked at both ends so a row that is
// truncated or dropped altogether is detectable, and the markers avoid
// wrap breakpoints so only a real failure can hide one.
func longBlockedResult(t *testing.T) (acpwire.AuthoredPlan, string) {
	t.Helper()

	dir := t.TempDir()
	segments := []string{"pathelementalphaqz", "pathelementbravoqz", "pathelementcharlieqz", "pathelementdeltaqz"}
	path := filepath.Join(append(append([]string{dir}, segments...), "candidatenameqz.go")...)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create candidate directory: %v", err)
	}
	if err := os.WriteFile(path, []byte("# Candidate\n"), 0o644); err != nil {
		t.Fatalf("write candidate: %v", err)
	}

	filler := strings.Repeat("explanatory words that run well past the panel width ", 2)
	insp := inspectionFixture()
	insp.Diagnostics = []acpwire.PlanDiagnostic{
		{TaskN: 0, Severity: acpwire.DiagnosticError, Line: 12, Message: "planstartqz " + filler + "planendqz"},
		{TaskN: 1, Severity: acpwire.DiagnosticWarning, Message: "taskonestartqz " + filler + "taskoneendqz"},
		{TaskN: 2, Severity: acpwire.DiagnosticError, Line: 344, Message: "tasktwostartqz " + filler + "tasktwoendqz"},
		{TaskN: 3, Severity: acpwire.DiagnosticWarning, Line: 7, Message: "taskthreestartz " + filler + "taskthreeendqz"},
	}
	return acpwire.AuthoredPlan{
		PlanPath:   path,
		Inspection: insp,
	}, path
}

// assertFrameFits fails when a rendered panel is wider or taller than the
// cells its dock geometry allows. The frame spans the dock exactly, so a row
// wider than width or a body taller than maxHeight would corrupt what the dock
// draws around it.
func assertFrameFits(t *testing.T, view, geometry string, width, maxHeight int) {
	t.Helper()
	lines := strings.Split(view, "\n")
	if len(lines) > maxHeight {
		t.Fatalf("%s: panel is %d rows, budget %d:\n%s", geometry, len(lines), maxHeight, ansi.Strip(view))
	}
	for i, line := range lines {
		if w := ansi.StringWidth(line); w > width {
			t.Fatalf("%s: row %d is %d cells, panel allows %d: %q", geometry, i, w, width, ansi.Strip(line))
		}
	}
}

// TestReviewPagesAllDiagnosticsAndLongPath proves every diagnostic and the
// whole candidate path are reachable by paging at the supported minimum and at
// a larger size — not merely counted in the model. It also pins that reading
// cannot accept a blocked candidate or touch its artifact.
func TestReviewPagesAllDiagnosticsAndLongPath(t *testing.T) {
	for _, tc := range []struct {
		name     string
		w, h     int
		geometry string
	}{
		{"minimum 80x24", 80, 24, "80x24"},
		{"larger 120x40", 120, 40, "120x40"},
		{"larger 200x60", 200, 60, "200x60"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, path := longBlockedResult(t)
			p := New(result)

			// The panel receives the dock's real budget, not the frame height.
			maxHeight := dock.MaxRows(tc.h)
			var pages []string
			for page := 0; ; page++ {
				view := p.View(tc.w, maxHeight)
				assertFrameFits(t, view, tc.geometry, tc.w, maxHeight)
				pages = append(pages, ansi.Strip(view))

				before := p.scroll
				p.Update(press("pgdown"))
				if p.scroll == before {
					break
				}
				if page > 100 {
					t.Fatalf("%s: paging did not terminate", tc.geometry)
				}
			}

			// Together the pages must hold every marker, every suffix, the
			// severity and location of each diagnostic, and every segment of
			// the long candidate path.
			text := strings.Join(pages, "\n")
			for _, want := range []string{
				"planstartqz", "planendqz",
				"taskonestartqz", "taskoneendqz",
				"tasktwostartqz", "tasktwoendqz",
				"taskthreestartz", "taskthreeendqz",
				"pathelementalphaqz", "pathelementbravoqz",
				"pathelementcharlieqz", "pathelementdeltaqz",
				"candidatenameqz",
				"plan error", "task 1 warning", "task 2 error", "line 344",
			} {
				if !strings.Contains(text, want) {
					t.Errorf("%s: %q is not reachable by paging:\n%s", tc.geometry, want, text)
				}
			}

			// The first page cannot be the whole review, or paging proved
			// nothing at the supported minimum.
			if tc.geometry == "80x24" && len(pages) < 2 {
				t.Fatalf("expected the diagnostics to overflow the 80x24 body, got %d page(s):\n%s", len(pages), text)
			}

			// Reading is not deciding: Enter stays blocked and the artifact is
			// untouched whatever the window shows.
			if cmd := p.Update(press("enter")); cmd != nil {
				t.Fatalf("%s: Enter must not accept a blocked candidate, got %T", tc.geometry, cmd())
			}
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("%s: paging must not touch the candidate artifact: %v", tc.geometry, err)
			}

			// Back to the top, the candidate path leads the body again.
			p.Update(press("home"))
			if top := ansi.Strip(p.View(tc.w, maxHeight)); !strings.Contains(top, "candidate: ") {
				t.Fatalf("%s: Home must return to the top of the body:\n%s", tc.geometry, top)
			}
		})
	}
}

// TestReviewScrollResizeAndNoColor pins the scroll behaviour on its own:
// reflow, clamping at both ends, action keys that stay readable in a narrow
// frame, a discard confirmation that navigation disarms, and a monochrome
// render that neither leaks an untrusted escape nor needs colour to be read.
func TestReviewScrollResizeAndNoColor(t *testing.T) {
	result, _ := longBlockedResult(t)

	// Reflow: a wider panel needs fewer physical rows, and the offset is
	// clamped into the shorter body rather than left past its end. The panel is
	// drawn at the dock's real 80x24 budget, where the body must scroll.
	dockH := dock.MaxRows(24)
	p := New(result)
	p.View(80, dockH)
	narrowRows := p.rowCount
	if narrowRows <= p.bodyH {
		t.Fatalf("fixture: the body must overflow at the minimum (%d rows, body %d)", narrowRows, p.bodyH)
	}
	p.Update(press("end"))
	p.View(80, dockH)
	if p.scroll == 0 {
		t.Fatalf("fixture: End must scroll a body that overflows (%d rows, body %d)", p.rowCount, p.bodyH)
	}
	p.View(200, 60)
	if p.rowCount >= narrowRows {
		t.Errorf("wider panel should wrap into fewer rows: %d at 200 wide, %d at 80", p.rowCount, narrowRows)
	}
	if maxOffset := max(p.rowCount-p.bodyH, 0); p.scroll > maxOffset {
		t.Errorf("scroll %d left past the end of a shortened body (max %d)", p.scroll, maxOffset)
	}

	// Top and bottom clamping: the window cannot move off either end, and a
	// key at the end changes nothing.
	p.Update(press("home"))
	p.View(80, dockH)
	if p.scroll != 0 {
		t.Fatalf("Home must reach the first row, got offset %d", p.scroll)
	}
	p.Update(press("up"))
	if p.scroll != 0 {
		t.Fatalf("Up at the top must not move the window, got offset %d", p.scroll)
	}
	p.Update(press("end"))
	p.View(80, dockH)
	bottom := p.scroll
	if bottom == 0 {
		t.Fatalf("End must reach the last row, got offset 0")
	}
	p.Update(press("down"))
	if p.scroll != bottom {
		t.Fatalf("Down at the end must not move the window: %d -> %d", bottom, p.scroll)
	}
	p.Update(press("pgup"))
	if p.scroll >= bottom {
		t.Fatalf("PgUp must move the window back: %d -> %d", bottom, p.scroll)
	}

	// Action keys stay readable in a narrow frame, where the border hint is
	// dropped and the action row is the only place they can appear. The width
	// respects layout.PanelWidth's floor, below which the frame itself would be
	// wider than the dock.
	const narrowW = 40
	narrow := ansi.Strip(p.View(narrowW, dockH))
	for _, want := range []string{"esc keep", "d discard"} {
		if !strings.Contains(narrow, want) {
			t.Errorf("%d-wide panel must still name %q:\n%s", narrowW, want, narrow)
		}
	}
	assertFrameFits(t, p.View(narrowW, dockH), "narrow", narrowW, dockH)

	// Navigation disarms a pending discard: reading can never delete.
	q := rendered(t, cleanResult())
	q.Update(press("d"))
	if !q.discardArmed {
		t.Fatal("fixture: d must arm the discard confirmation")
	}
	q.Update(press("down"))
	if q.discardArmed {
		t.Fatal("scrolling must disarm the pending discard")
	}
	if cmd := q.Update(press("d")); cmd != nil {
		t.Fatalf("one d after navigation must only arm, got %T", cmd())
	}

	// Control characters in the diagnostics and the path are escaped, never
	// executed: no untrusted sequence may reach the view as an escape.
	hostile := blockedResult()
	hostile.PlanPath = "/repo/\x1b[2Kplans/feature.md"
	hostile.Inspection.Diagnostics = []acpwire.PlanDiagnostic{
		{TaskN: 1, Severity: acpwire.DiagnosticError, Line: 3, Message: "bad \x1b[2K erase\r and \x07 bell"},
	}
	hp := rendered(t, hostile)
	out := hp.View(80, 20)
	if strings.Contains(out, "\x1b[2K") {
		t.Fatalf("raw escape sequence reached the view:\n%q", out)
	}
	if strings.Contains(out, "\x07") {
		t.Fatalf("raw bell reached the view:\n%q", out)
	}
	if stripped := ansi.Strip(out); !strings.Contains(stripped, `\x1b[2K`) {
		t.Fatalf("a control character must be shown, not silently removed:\n%s", stripped)
	}

	// NO_COLOR: the same diagnostics must be readable with no styling at all.
	prevTheme := theme.Current()
	t.Cleanup(func() { theme.Reload(prevTheme) })
	theme.Reload(theme.LoadFor(true, "xterm-256color"))

	mono := New(result)
	// The candidate path leads the body, so it is checked at the top; the
	// diagnostics are checked at the end, where the window has scrolled to them.
	monoTop := mono.View(80, dockH)
	if strings.Contains(monoTop, "\x1b") {
		t.Fatalf("monochrome render must emit no escape sequences:\n%q", monoTop)
	}
	assertFrameFits(t, monoTop, "80x24 no-color", 80, dockH)
	for _, want := range []string{"esc keep", "d discard", "candidatenameqz"} {
		if !strings.Contains(monoTop, want) {
			t.Errorf("monochrome view is missing %q:\n%s", want, monoTop)
		}
	}

	mono.Update(press("end"))
	monoView := mono.View(80, dockH)
	if strings.Contains(monoView, "\x1b") {
		t.Fatalf("monochrome render must emit no escape sequences:\n%q", monoView)
	}
	assertFrameFits(t, monoView, "80x24 no-color", 80, dockH)
	for _, want := range []string{"esc keep", "d discard", "warning", "error", "taskthreeendqz"} {
		if !strings.Contains(monoView, want) {
			t.Errorf("monochrome view is missing %q:\n%s", want, monoView)
		}
	}
}
