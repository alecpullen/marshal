package inspector

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func detail() Detail {
	var out []string
	for i := 0; i < 40; i++ {
		out = append(out, fmt.Sprintf("output line %d", i))
	}
	return Detail{
		Title:   "step 3 · tool 1/2",
		Subject: "shell.run go test ./...",
		Fields:  []Field{{"actor", "implementer"}, {"exit code", "1"}},
		Sections: []Section{
			{Name: "args", Body: `{"command": "go test"}`},
			{Name: "output", Body: strings.Join(out, "\n")},
		},
	}
}

// manyFieldDetail is a tool call carrying every fact the browse view records,
// with values long enough to wrap under any of the widths these tests use.
func manyFieldDetail() Detail {
	long := func(label string) string {
		parts := make([]string, 0, 12)
		for i := 0; i < 12; i++ {
			parts = append(parts, fmt.Sprintf("%s%02d", label, i))
		}
		return strings.Join(parts, " ")
	}
	return Detail{
		Title:   "step 9 · tool 1/1",
		Subject: "shell.run go test ./...",
		Fields: []Field{
			{"actor", "implementer · gpt-5 @ openai"},
			{"started", "12:04:05"},
			{"duration", "1m12.4s"},
			{"exit code", "1"},
			{"sandbox", "restricted, network off — " + long("sandbox")},
			{"approval", "allow — " + long("approval")},
			{"hooks", long("hooks")},
			{"rewritten", "yes — arguments changed after approval: " + long("changed")},
			{"finish reason", "stop"},
			{"tool call id", "call_0f3a9c7b2e5d48ab9c1d7e"},
			{"notice", long("notice")},
			{"risk", "high — writes outside the workspace: " + long("risk")},
		},
		Sections: []Section{
			{Name: "args", Body: `{"command": "go test ./..."}`},
			{Name: "output", Body: strings.Repeat("ok pkg/foo\n", 30)},
		},
	}
}

func press(p *Panel, k tea.KeyPressMsg) tea.Msg {
	if cmd := p.Update(k); cmd != nil {
		return cmd()
	}
	return nil
}

func tabKey() tea.KeyPressMsg { return tea.KeyPressMsg{Code: tea.KeyTab} }

func shiftTabKey() tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
}

func key(r rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: r, Text: string(r)} }

// assertFits checks the panel stayed inside the frame the dock allocated.
func assertFits(t *testing.T, view string, w, h int) {
	t.Helper()
	lines := strings.Split(view, "\n")
	if len(lines) > h {
		t.Fatalf("w=%d h=%d: %d rows, want at most %d:\n%s", w, h, len(lines), h, view)
	}
	for _, l := range lines {
		if ansi.StringWidth(l) > w {
			t.Fatalf("w=%d h=%d: line is %d cells wide: %q", w, h, ansi.StringWidth(l), l)
		}
	}
}

func TestTabSwitchesSectionsAndShiftTabWraps(t *testing.T) {
	p := New(detail())
	if p.Section() != -1 {
		t.Fatalf("fields should open the metadata tab, selected section = %d", p.Section())
	}
	press(p, tabKey())
	if p.Section() != 0 {
		t.Fatalf("tab -> %d, want the args section", p.Section())
	}
	press(p, tabKey())
	if p.Section() != 1 {
		t.Fatalf("tab -> %d, want the output section", p.Section())
	}
	press(p, tabKey())
	if p.Section() != -1 {
		t.Fatalf("tab wraps to %d, want the metadata tab", p.Section())
	}
	press(p, shiftTabKey())
	if p.Section() != 1 {
		t.Fatalf("shift+tab wraps back to %d, want the output section", p.Section())
	}
}

func TestScrollingMovesTheVisibleWindowAndStopsAtTheEnd(t *testing.T) {
	p := New(detail())
	press(p, tabKey()) // args
	press(p, tabKey()) // output
	first := ansi.Strip(p.View(100, 20))
	if !strings.Contains(first, "output line 0") || strings.Contains(first, "output line 39") {
		t.Fatalf("initial window wrong:\n%s", first)
	}
	for i := 0; i < 100; i++ {
		press(p, tea.KeyPressMsg{Code: tea.KeyDown})
		p.View(100, 20)
	}
	last := ansi.Strip(p.View(100, 20))
	if !strings.Contains(last, "output line 39") || strings.Contains(last, "output line 0\n") {
		t.Fatalf("scroll did not reach the end:\n%s", last)
	}
	press(p, key('g'))
	if !strings.Contains(ansi.Strip(p.View(100, 20)), "output line 0") {
		t.Fatal("g returns to the top")
	}
}

func TestEachSectionScrollsIndependently(t *testing.T) {
	p := New(detail())
	press(p, tabKey()) // args
	press(p, tabKey()) // output
	p.View(100, 20)
	press(p, tea.KeyPressMsg{Code: tea.KeyPgDown})
	scrolled := ansi.Strip(p.View(100, 20))
	if !strings.Contains(scrolled, "output line 15") || strings.Contains(scrolled, "output line 0\n") {
		t.Fatalf("pgdown did not move the output window:\n%s", scrolled)
	}
	press(p, tabKey()) // wraps to metadata
	press(p, tabKey()) // args
	if !strings.Contains(ansi.Strip(p.View(100, 20)), `"command"`) {
		t.Fatal("the args section keeps its own offset")
	}
	press(p, tabKey()) // back to output
	back := ansi.Strip(p.View(100, 20))
	if !strings.Contains(back, "output line 15") || strings.Contains(back, "output line 0\n") {
		t.Fatalf("the output section kept its own offset:\n%s", back)
	}
}

func TestMessagesFromKeys(t *testing.T) {
	p := New(detail())
	cases := map[string]tea.Msg{
		"esc": ClosedMsg{},
		"n":   NavigateMsg{Delta: 1},
		"p":   NavigateMsg{Delta: -1},
		"o":   OpenMsg{},
	}
	for k, want := range cases {
		var msg tea.Msg
		if k == "esc" {
			msg = press(p, tea.KeyPressMsg{Code: tea.KeyEscape})
		} else {
			msg = press(p, key(rune(k[0])))
		}
		if fmt.Sprintf("%#v", msg) != fmt.Sprintf("%#v", want) {
			t.Errorf("%s -> %#v, want %#v", k, msg, want)
		}
	}
	// New opens on the metadata tab, so y copies every field.
	msg := press(p, key('y'))
	c, ok := msg.(CopyMsg)
	if !ok || c.Text != "actor: implementer\nexit code: 1" {
		t.Errorf("y should copy the metadata fields, got %#v", msg)
	}
	press(p, tabKey()) // args
	msg = press(p, key('y'))
	c, ok = msg.(CopyMsg)
	if !ok || !strings.Contains(c.Text, "command") || !strings.HasPrefix(c.Text, "{") {
		t.Errorf("y should copy the current section's body, got %#v", msg)
	}
}

func TestSetDetailKeepsTheSelectedSectionByName(t *testing.T) {
	p := New(detail())
	press(p, tabKey()) // args
	press(p, tabKey()) // output
	if p.Section() != 1 {
		t.Fatalf("selected section = %d, want the output section", p.Section())
	}
	next := detail()
	next.Title = "step 4"
	next.Sections = []Section{{Name: "narration", Body: "x"}, {Name: "output", Body: "y"}}
	p.SetDetail(next)
	if p.Section() != 1 {
		t.Fatalf("selected section = %d, want the output section kept", p.Section())
	}
}

func TestViewFitsItsFrame(t *testing.T) {
	p := New(detail())
	for _, w := range []int{80, 100, 140} {
		for _, h := range []int{12, 24, 40} {
			assertFits(t, p.View(w, h), w, h)
		}
	}
}

// TestInspectorMetadataTabReachesEveryField walks the metadata tab from the top
// one line at a time: every field key shows up, every value appears whole, and
// every frame stays inside its frame, including after a resize.
func TestInspectorMetadataTabReachesEveryField(t *testing.T) {
	d := manyFieldDetail()
	p := New(d)

	// squeeze drops whitespace runs and the frame border, so a value hard
	// wrapped across lines reads as one unbroken run of characters.
	squeeze := func(s string) string {
		return strings.Join(strings.Fields(strings.ReplaceAll(s, "│", "")), "")
	}

	collect := func(w, h int) []string {
		t.Helper()
		press(p, key('g'))
		var frames []string
		prev := ""
		for i := 0; i < 400; i++ {
			view := p.View(w, h)
			assertFits(t, view, w, h)
			if p.Section() != -1 {
				t.Fatalf("w=%d h=%d: metadata should stay selected, section = %d", w, h, p.Section())
			}
			frame := ansi.Strip(view)
			if frame == prev {
				break
			}
			frames = append(frames, frame)
			prev = frame
			press(p, tea.KeyPressMsg{Code: tea.KeyDown})
		}
		return frames
	}

	frames := collect(100, 20)
	if len(frames) < 2 {
		t.Fatalf("the metadata tab should scroll, got %d frame(s)", len(frames))
	}
	frames = append(frames, collect(64, 14)...)

	all := squeeze(strings.Join(frames, "\n"))
	for _, f := range d.Fields {
		if !strings.Contains(all, squeeze(f.Key)) {
			t.Fatalf("key %q is unreachable on the metadata tab", f.Key)
		}
		whole := false
		for _, frame := range frames {
			if strings.Contains(squeeze(frame), squeeze(f.Value)) {
				whole = true
				break
			}
		}
		if !whole {
			t.Fatalf("field %q: its full value is never on screen: %q", f.Key, f.Value)
		}
	}

	// Page navigation reaches both ends of the long tab.
	press(p, key('g'))
	top := ansi.Strip(p.View(100, 20))
	if !strings.Contains(top, "actor") || strings.Contains(top, "risk") {
		t.Fatalf("g should return to the first field:\n%s", top)
	}
	press(p, tea.KeyPressMsg{Code: tea.KeyPgDown})
	paged := ansi.Strip(p.View(100, 20))
	assertFits(t, p.View(100, 20), 100, 20)
	if paged == top {
		t.Fatal("pgdown should move the metadata window")
	}
	press(p, key('G'))
	bottom := ansi.Strip(p.View(100, 20))
	if !strings.Contains(bottom, "risk") {
		t.Fatalf("G should reach the last field:\n%s", bottom)
	}
	press(p, tea.KeyPressMsg{Code: tea.KeyPgUp})
	assertFits(t, p.View(100, 20), 100, 20)

	// A resize clamps the window instead of rendering past the end.
	for _, size := range [][2]int{{80, 8}, {40, 6}, {140, 30}} {
		w, h := size[0], size[1]
		assertFits(t, p.View(w, h), w, h)
		press(p, key('G'))
		assertFits(t, p.View(w, h), w, h)
		press(p, key('g'))
		assertFits(t, p.View(w, h), w, h)
	}
}

// TestInspectorMetadataAndSectionsRemainDistinct covers the seams between the
// view-only metadata tab and real sections: copying, a detail with only one of
// them, a section that shares the metadata label, and tab identity across
// SetDetail.
func TestInspectorMetadataAndSectionsRemainDistinct(t *testing.T) {
	copied := func(t *testing.T, p *Panel) string {
		t.Helper()
		msg := press(p, key('y'))
		c, ok := msg.(CopyMsg)
		if !ok {
			t.Fatalf("y -> %#v, want CopyMsg", msg)
		}
		return c.Text
	}

	t.Run("copy", func(t *testing.T) {
		p := New(detail())
		if got := copied(t, p); got != "actor: implementer\nexit code: 1" {
			t.Fatalf("metadata copy = %q", got)
		}
		press(p, tabKey()) // args
		if got := copied(t, p); got != p.Detail().Sections[0].Body {
			t.Fatalf("section copy = %q, want its body %q", got, p.Detail().Sections[0].Body)
		}
		if len(p.Detail().Sections) != 2 {
			t.Fatalf("Detail() changed: %d sections", len(p.Detail().Sections))
		}
	})

	t.Run("no sections", func(t *testing.T) {
		d := Detail{Title: "step 3", Subject: "x", Fields: []Field{{"risk", "high"}}}
		p := New(d)
		if p.Section() != -1 {
			t.Fatalf("section = %d, want the metadata tab", p.Section())
		}
		press(p, tabKey()) // wraps onto itself
		if p.Section() != -1 {
			t.Fatalf("section = %d after tab, want the metadata tab", p.Section())
		}
		view := ansi.Strip(p.View(80, 20))
		assertFits(t, view, 80, 20)
		if !strings.Contains(view, "risk") || !strings.Contains(view, "high") {
			t.Fatalf("metadata tab does not render its fields:\n%s", view)
		}
		if got := copied(t, p); got != "risk: high" {
			t.Fatalf("metadata copy = %q", got)
		}
		if p.Detail().Sections != nil {
			t.Fatalf("Detail() grew sections: %#v", p.Detail().Sections)
		}
	})

	t.Run("no fields", func(t *testing.T) {
		d := Detail{Title: "step 3", Subject: "x", Sections: []Section{
			{Name: "args", Body: "the args body"},
			{Name: "output", Body: "the output body"},
		}}
		p := New(d)
		if p.Section() != 0 {
			t.Fatalf("section = %d, want the first section when there are no fields", p.Section())
		}
		press(p, tabKey())
		if p.Section() != 1 {
			t.Fatalf("section = %d after tab, want the second section", p.Section())
		}
		press(p, tabKey())
		if p.Section() != 0 {
			t.Fatalf("section = %d after wrap, want the first section", p.Section())
		}
		view := ansi.Strip(p.View(80, 20))
		assertFits(t, view, 80, 20)
		if strings.Contains(view, "metadata") {
			t.Fatalf("a detail without fields shows no metadata tab:\n%s", view)
		}
		if got := copied(t, p); got != "the args body" {
			t.Fatalf("section copy = %q", got)
		}
	})

	t.Run("section named metadata", func(t *testing.T) {
		d := Detail{
			Title:    "step 3",
			Subject:  "x",
			Fields:   []Field{{"actor", "implementer"}},
			Sections: []Section{{Name: "metadata", Body: "the real section body"}, {Name: "output", Body: "out"}},
		}
		p := New(d)
		if p.Section() != -1 {
			t.Fatalf("section = %d, want the metadata tab first", p.Section())
		}
		if got := copied(t, p); got != "actor: implementer" {
			t.Fatalf("metadata copy = %q", got)
		}
		press(p, tabKey()) // the real section called "metadata"
		if p.Section() != 0 {
			t.Fatalf("section = %d, want the section named metadata", p.Section())
		}
		view := ansi.Strip(p.View(80, 20))
		assertFits(t, view, 80, 20)
		if !strings.Contains(view, "the real section body") {
			t.Fatalf("the section named metadata renders its own body:\n%s", view)
		}
		if got := copied(t, p); got != "the real section body" {
			t.Fatalf("copy of the section named metadata = %q", got)
		}

		next := d
		next.Title = "step 4"
		p.SetDetail(next)
		if p.Section() != 0 {
			t.Fatalf("section = %d, want the section named metadata kept by identity", p.Section())
		}
		press(p, shiftTabKey())
		if p.Section() != -1 {
			t.Fatalf("section = %d, want the metadata tab", p.Section())
		}
		p.SetDetail(next)
		if p.Section() != -1 {
			t.Fatalf("section = %d, want the metadata tab kept by identity", p.Section())
		}
		if got := copied(t, p); got != "actor: implementer" {
			t.Fatalf("metadata copy after SetDetail = %q", got)
		}
		if len(p.Detail().Sections) != 2 || p.Detail().Sections[0].Name != "metadata" {
			t.Fatalf("Detail() changed: %#v", p.Detail().Sections)
		}
	})
}
