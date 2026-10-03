package tui

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/session"
	"marshal/internal/app/tui/stack"
	"marshal/internal/tools/registry"
)

// stepFixture seeds one user turn with one settled step: optional narration
// and the given tool calls, all stamped with the step.
func stepFixture(t *testing.T, m *Model, actor session.Actor, narrate string, tools ...registry.AuditEvent) session.StepID {
	t.Helper()
	if len(m.state.Messages()) == 0 {
		m.state.AddMessage(session.RoleUser, "fix the parser", session.ContentTypePlain)
	}
	id := m.state.BeginStep(actor)
	if narrate != "" {
		m.state.AddNarration(id, narrate)
	}
	for i, ev := range tools {
		ev.StepID = id
		if ev.ToolCallID == "" {
			ev.ToolCallID = fmt.Sprintf("s%d-%d", id, i)
		}
		if ev.Timestamp.IsZero() {
			ev.Timestamp = time.Now().Add(time.Duration(i) * time.Millisecond)
		}
		m.state.LogToolCall(ev)
	}
	m.state.EndStep(id)
	return id
}

func readEvent(path string) registry.AuditEvent {
	return registry.AuditEvent{ToolName: "file.read", Args: []byte(fmt.Sprintf(`{"path":%q}`, path)), ResultSummary: path + " (12 lines)", Approval: registry.ApprovalNotRequired}
}

func transcriptLines(m *Model) []string {
	m.invalidateTranscript()
	m.refreshViewport()
	return strings.Split(stripANSI(m.viewport.GetContent()), "\n")
}

func TestNarratedStepRendersHeadlineAndNestedToolRows(t *testing.T) {
	m := newTestModel(t)
	m.resize(100, 30)
	stepFixture(t, &m, session.Actor{}, "Reading the parser to find the guard. It lives somewhere in the lexer.",
		readEvent("parser.go"), registry.AuditEvent{ToolName: "search.text"})
	lines := transcriptLines(&m)

	var header string
	var toolRows []string
	for _, l := range lines {
		if strings.Contains(l, "Reading the parser to find the guard.") {
			header = l
		}
		if strings.Contains(l, "parser.go") && !strings.Contains(l, "Reading") {
			toolRows = append(toolRows, l)
		}
	}
	if header == "" {
		t.Fatalf("headline missing:\n%s", strings.Join(lines, "\n"))
	}
	if !strings.HasPrefix(header, " "+"✓"+" ") {
		t.Errorf("settled step with tools should lead with ✓, got %q", header)
	}
	if strings.Contains(header, "inferred") {
		t.Errorf("narrated step must not be tagged inferred: %q", header)
	}
	if !regexp.MustCompile(`\d+s$`).MatchString(strings.TrimRight(header, " ")) {
		t.Errorf("header should end with the step duration, got %q", header)
	}
	if len(toolRows) == 0 {
		t.Fatalf("no tool row:\n%s", strings.Join(lines, "\n"))
	}
	for _, r := range toolRows {
		if indent := len(r) - len(strings.TrimLeft(r, " ")); indent < stepRowIndent {
			t.Errorf("tool row not at nested indent (%d): %q", indent, r)
		}
	}
	// The rest of the narration is a muted continuation, collapsed to one line.
	var cont bool
	for _, l := range lines {
		if strings.Contains(l, "somewhere in the lexer") {
			cont = true
			if !strings.HasPrefix(l, strings.Repeat(" ", nestedBodyIndent)) {
				t.Errorf("continuation not at nestedBodyIndent: %q", l)
			}
		}
	}
	if !cont {
		t.Error("continuation line missing")
	}
}

func TestUnnarratedStepShowsInferredHeadline(t *testing.T) {
	m := newTestModel(t)
	m.resize(100, 30)
	stepFixture(t, &m, session.Actor{}, "", readEvent("a.go"), readEvent("b.go"))
	var header string
	for _, l := range transcriptLines(&m) {
		if strings.Contains(l, "read 2 files") {
			header = l
		}
	}
	if header == "" {
		t.Fatal("inferred headline missing")
	}
	if !strings.Contains(header, "inferred") {
		t.Errorf("inferred headline's meta must be tagged: %q", header)
	}
}

func TestOwnerLabelIsRightAlignedAndSurvivesNoColor(t *testing.T) {
	for _, noColor := range []bool{false, true} {
		t.Run(fmt.Sprintf("NO_COLOR=%v", noColor), func(t *testing.T) {
			if noColor {
				t.Setenv("NO_COLOR", "1")
			} else {
				t.Setenv("NO_COLOR", "")
			}
			m := newTestModel(t)
			m.resize(100, 30)
			stepFixture(t, &m, session.Actor{Role: "sdd_reviewer", Label: "reviewer"}, "Checking the diff against the plan.", readEvent("plan.md"))
			var header string
			for _, l := range transcriptLines(&m) {
				if strings.Contains(l, "Checking the diff") {
					header = l
				}
			}
			if header == "" || !strings.Contains(header, "reviewer") {
				t.Fatalf("owner label missing from header %q", header)
			}
			if i, j := strings.Index(header, "Checking"), strings.Index(header, "reviewer"); j < i+20 {
				t.Errorf("owner label should be right-aligned, not adjacent to the headline: %q", header)
			}
		})
	}
}

func TestOrchestratorStepOnDifferentRouteShowsModel(t *testing.T) {
	m := newTestModel(t)
	m.resize(120, 30)
	m.state.SetActiveRoute(session.RouteInfo{Active: true, Model: "qwen", Provider: "ollama"})
	stepFixture(t, &m, session.Actor{Role: "sdd_reviewer", Label: "reviewer", Model: "gpt-5", Provider: "openai"}, "Reviewing the change.", readEvent("x.go"))
	var header string
	for _, l := range transcriptLines(&m) {
		if strings.Contains(l, "Reviewing the change.") {
			header = l
		}
	}
	if !strings.Contains(header, "gpt-5 @ openai") {
		t.Fatalf("a step on another model should name it: %q", header)
	}
	// Same model as the session route: nothing to add.
	m2 := newTestModel(t)
	m2.resize(120, 30)
	m2.state.SetActiveRoute(session.RouteInfo{Active: true, Model: "qwen", Provider: "ollama"})
	stepFixture(t, &m2, session.Actor{Role: "sdd_reviewer", Label: "reviewer", Model: "qwen", Provider: "ollama"}, "Reviewing the change.", readEvent("x.go"))
	for _, l := range transcriptLines(&m2) {
		if strings.Contains(l, "Reviewing the change.") && strings.Contains(l, "qwen") {
			t.Fatalf("model equal to the active route must be omitted: %q", l)
		}
	}
}

func TestFailedToolMakesStepGlyphError(t *testing.T) {
	m := newTestModel(t)
	m.resize(100, 30)
	code := 1
	stepFixture(t, &m, session.Actor{}, "Running the package tests.", registry.AuditEvent{ToolName: "test.run", CommandExitCode: &code, Args: []byte(`{"command":"go test ./..."}`)})
	for _, l := range transcriptLines(&m) {
		if strings.Contains(l, "Running the package tests.") {
			if !strings.HasPrefix(l, " ✗ ") {
				t.Fatalf("a failed tool should flip the step glyph to ✗: %q", l)
			}
			return
		}
	}
	t.Fatal("header missing")
}

func TestStepRowsStayWithinTheFrameAtEveryWidth(t *testing.T) {
	for _, w := range []int{80, 100, 140} { // 80 is the narrowest the TUI lays out
		m := newTestModel(t)
		m.resize(w, 30)
		stepFixture(t, &m, session.Actor{Role: "sdd_branch_reviewer", Label: "branch reviewer #12", Model: "some-long-model-name", Provider: "somewhere"},
			"Cross-checking every changed file against the acceptance criteria in the plan and the spec text.",
			readEvent("internal/some/very/long/path/to/a/file_with_a_long_name.go"), readEvent("another/long/path.go"))
		for i, l := range transcriptLines(&m) {
			if got := ansi.StringWidth(l); got > w {
				t.Errorf("w=%d line %d is %d wide: %q", w, i, got, l)
			}
		}
	}
}

func TestExpandedStepShowsFullContinuation(t *testing.T) {
	m := newTestModel(t)
	m.resize(100, 40)
	id := stepFixture(t, &m, session.Actor{}, "Checking the guard first. "+strings.Repeat("Second sentence filler. ", 8)+"\n\nThird paragraph with detail.", readEvent("guard.go"))
	collapsed := strings.Join(transcriptLines(&m), "\n")
	if strings.Contains(collapsed, "Third paragraph") {
		t.Fatal("collapsed continuation must be one truncated line")
	}
	m.toggleExpanded(stack.NodeID{Kind: stack.KindStep, Key: fmt.Sprintf("step:%d", id)})
	if expanded := strings.Join(transcriptLines(&m), "\n"); !strings.Contains(expanded, "Third paragraph with detail.") {
		t.Fatalf("expanded step should show the whole narration:\n%s", expanded)
	}
}

func TestThinkingRowLivesInsideTheStep(t *testing.T) {
	m := newTestModel(t)
	m.resize(100, 30)
	m.state.AddMessage(session.RoleUser, "go", session.ContentTypePlain)
	id := m.state.BeginStep(session.Actor{})
	m.state.AddNarration(id, "Looking at the failing test.")
	ts := time.Now()
	m.state.LogThinking(session.ThinkingEntry{Text: "private reasoning text", Duration: 2 * time.Second, StartedAt: ts, StepID: id})
	m.state.LogToolCall(registry.AuditEvent{ToolName: "file.read", StepID: id, ToolCallID: "c", Timestamp: ts.Add(time.Millisecond), Args: []byte(`{"path":"a.go"}`)})
	m.state.EndStep(id)
	lines := transcriptLines(&m)
	headerAt, thoughtAt := -1, -1
	for i, l := range lines {
		if strings.Contains(l, "Looking at the failing test.") {
			headerAt = i
		}
		if strings.Contains(l, "thought for 2s") {
			thoughtAt = i
			if !strings.HasPrefix(l, strings.Repeat(" ", stepRowIndent)) {
				t.Errorf("thinking row not at nested indent: %q", l)
			}
		}
	}
	if headerAt < 0 || thoughtAt <= headerAt {
		t.Fatalf("thinking row should sit under its step header (header %d, thought %d)", headerAt, thoughtAt)
	}
	if strings.Contains(strings.Join(lines, "\n"), "private reasoning text") {
		t.Error("collapsed thinking must not show its text")
	}
}

func TestClickOnToolRowTogglesThatRowOnly(t *testing.T) {
	m := newTestModel(t)
	m.resize(100, 30)
	id := stepFixture(t, &m, session.Actor{}, "Reading two files.",
		registry.AuditEvent{ToolName: "shell.run", Args: []byte(`{"command":"echo hi"}`), ToolCallID: "row_a", ResultSummary: "ok", ResultContent: "line one\nline two"},
		registry.AuditEvent{ToolName: "file.read", Args: []byte(`{"path":"b.go"}`), ToolCallID: "row_b"})
	m.invalidateTranscript()
	m.refreshViewport()

	row := stack.NodeID{Kind: stack.KindTool, Key: "tool:row_a"}
	region, ok := regionOf(&m, row)
	if !ok {
		t.Fatal("tool row recorded no region of its own")
	}
	stepRegion, ok := regionOf(&m, stack.NodeID{Kind: stack.KindStep, Key: fmt.Sprintf("step:%d", id)})
	if !ok || stepRegion.endLine-stepRegion.startLine <= region.endLine-region.startLine {
		t.Fatalf("row region %+v should sit inside the step region %+v", region, stepRegion)
	}
	y := m.scrollHintRows() + region.startLine - m.viewport.YOffset()
	updated, _ := m.Update(tea.MouseClickMsg{X: 2, Y: y, Button: tea.MouseLeft})
	mm := asModel(t, updated)
	if !mm.isExpanded(row) {
		t.Fatal("click on the row did not expand it")
	}
	if mm.isExpanded(stack.NodeID{Kind: stack.KindStep, Key: fmt.Sprintf("step:%d", id)}) || mm.isExpanded(stack.NodeID{Kind: stack.KindTool, Key: "tool:row_b"}) {
		t.Fatal("click on a row must not toggle the step or its siblings")
	}
}

func TestSettledStepsAreNotReRenderedOnSpinnerTicks(t *testing.T) {
	m := newTestModel(t)
	m.resize(100, 50)
	for i := 0; i < 20; i++ {
		stepFixture(t, &m, session.Actor{}, fmt.Sprintf("Step number %d is reading.", i), readEvent(fmt.Sprintf("f%d.go", i)))
	}
	// One live step: open, with a running tool.
	live := m.state.BeginStep(session.Actor{})
	m.state.AddNarration(live, "Still working on it.")
	m.state.SetActiveToolCall(session.ActiveToolCall{Name: "shell.run", Args: "go test", StartedAt: time.Now(), StepID: live, ToolCallID: "run"})
	m.busy = true
	m.turnStartedAt = time.Now()

	var rendered []stack.NodeID
	nodeRenderHook = func(id stack.NodeID) { rendered = append(rendered, id) }
	t.Cleanup(func() { nodeRenderHook = nil })

	m.invalidateTranscript()
	m.refreshViewport()
	if len(rendered) < 21 {
		t.Fatalf("first render should draw every block, drew %d", len(rendered))
	}

	// A spinner tick: new frame, later clock.
	rendered = nil
	m.spinnerFrame = "⠙"
	base := m.now()
	m.now = func() time.Time { return base.Add(3 * time.Second) }
	m.refreshViewport()
	want := fmt.Sprintf("step:%d", live)
	if len(rendered) != 1 || rendered[0].Key != want {
		t.Fatalf("a spinner tick should re-render only the live step %q, rendered %v", want, rendered)
	}

	// Nothing changed at all: nothing renders and the viewport is untouched.
	rendered = nil
	m.refreshViewport()
	if len(rendered) != 0 {
		t.Fatalf("an unchanged refresh rendered %v", rendered)
	}
}

func TestExpandingOneStepRendersOnlyThatStep(t *testing.T) {
	m := newTestModel(t)
	m.resize(100, 50)
	var ids []session.StepID
	for i := 0; i < 5; i++ {
		ids = append(ids, stepFixture(t, &m, session.Actor{}, fmt.Sprintf("Step %d did something. More detail here.", i), readEvent("x.go")))
	}
	m.invalidateTranscript()
	m.refreshViewport()
	var rendered []stack.NodeID
	nodeRenderHook = func(id stack.NodeID) { rendered = append(rendered, id) }
	t.Cleanup(func() { nodeRenderHook = nil })
	target := stack.NodeID{Kind: stack.KindStep, Key: fmt.Sprintf("step:%d", ids[2])}
	m.toggleExpanded(target)
	m.refreshViewport()
	if len(rendered) != 1 || rendered[0] != target {
		t.Fatalf("expanding one step should render only it, rendered %v", rendered)
	}
}

func TestFirstSentence(t *testing.T) {
	cases := []struct{ in, head, rest string }{
		{"Reading the parser. Then the lexer.", "Reading the parser.", "Then the lexer."},
		{"Reading the parser", "Reading the parser", ""},
		{"Short. Next one is longer than eight.", "Short. Next one is longer than eight.", ""}, // a split needs 8 runes first
		{"Checking the guard!\nSecond line", "Checking the guard!", "Second line"},
		{"Why is this failing? Let me look.", "Why is this failing?", "Let me look."},
		{"Version 1.2 is fine. Moving on", "Version 1.2 is fine.", "Moving on"},
		{"", "", ""},
	}
	for _, c := range cases {
		if h, r := firstSentence(c.in); h != c.head || r != c.rest {
			t.Errorf("firstSentence(%q) = (%q, %q), want (%q, %q)", c.in, h, r, c.head, c.rest)
		}
	}
	if got := stripEmphasis("**Reading** the `parser` __now__"); got != "Reading the parser now" {
		t.Errorf("stripEmphasis = %q", got)
	}
}

func TestInferHeadline(t *testing.T) {
	tool := func(name, args string) *stack.Node {
		return &stack.Node{Kind: stack.KindTool, Tools: []registry.AuditEvent{{ToolName: name, Args: []byte(args)}}}
	}
	cases := []struct {
		name string
		rows []*stack.Node
		want string
	}{
		{"reads aggregate", []*stack.Node{tool("file.read", `{"path":"a"}`), tool("file.read", `{"path":"b"}`)}, "read 2 files"},
		{"one read", []*stack.Node{tool("file.read", `{"path":"a"}`)}, "read 1 file"},
		{"search", []*stack.Node{tool("repo.search", `{"query":"ErrEmpty"}`)}, `searched "ErrEmpty"`},
		{"shell", []*stack.Node{tool("shell.run", `{"command":"go test ./..."}`)}, "ran go …"},
		{"edit", []*stack.Node{tool("file.write_patch", `{"path":"internal/a/parser.go"}`)}, "edited parser.go"},
		{"two clauses", []*stack.Node{tool("file.read", `{"path":"a"}`), tool("repo.search", `{"query":"x"}`)}, `read 1 file · searched "x"`},
		{"overflow", []*stack.Node{tool("file.read", `{"path":"a"}`), tool("repo.search", `{"query":"x"}`), tool("shell.run", `{"command":"ls"}`)}, `read 1 file · searched "x" …`},
		{"other tool uses its display name", []*stack.Node{tool("web.fetch", `{}`)}, "Fetch page"},
		{"agents", []*stack.Node{{Kind: stack.KindSubagent}, {Kind: stack.KindSubagent}}, "dispatched 2 agents"},
		{"nothing", nil, ""},
	}
	for _, c := range cases {
		if got := inferHeadline(c.rows); got != c.want {
			t.Errorf("%s: inferHeadline = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestRightMetaDropOrder(t *testing.T) {
	p := metaParts{tag: "inferred", owner: "reviewer #1", role: "reviewer", model: "gpt-5 @ openai", dur: "1m 4s"}
	plain := func(width int) string {
		text, _ := rightMeta(p, 40, width)
		return stripANSI(text)
	}
	if got := plain(200); got != "inferred · reviewer #1 · gpt-5 @ openai · 1m 4s" {
		t.Errorf("full meta = %q", got)
	}
	// Narrowing drops the model first, then the duration, then shortens the
	// owner, and only then drops the owner.
	steps := []struct {
		width int
		want  string
	}{
		{3 + 2 + 24 + len("inferred · reviewer #1 · 1m 4s"), "inferred · reviewer #1 · 1m 4s"},
		{3 + 2 + 24 + len("inferred · reviewer #1"), "inferred · reviewer #1"},
		{3 + 2 + 24 + len("inferred · reviewer"), "inferred · reviewer"},
		{3 + 2 + 24 + len("inferred"), "inferred"},
		{3 + 2 + 24, ""},
	}
	for _, s := range steps {
		if got := plain(s.width); got != s.want {
			t.Errorf("width %d: meta = %q, want %q", s.width, got, s.want)
		}
	}
}

func TestActorColorIsStableAndOrchestratorHasNone(t *testing.T) {
	if actorColor("") != nil {
		t.Error("the orchestrator has no colour")
	}
	if actorColor("sdd_reviewer") != actorColor("sdd_reviewer") {
		t.Error("colour must be stable per role")
	}
}
