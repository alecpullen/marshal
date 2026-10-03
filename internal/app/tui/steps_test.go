package tui

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/config"
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

	row := stack.NodeID{Kind: stack.KindTool, Key: fmt.Sprintf("tool:%d:row_a", id)}
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
	if mm.isExpanded(stack.NodeID{Kind: stack.KindStep, Key: fmt.Sprintf("step:%d", id)}) || mm.isExpanded(stack.NodeID{Kind: stack.KindTool, Key: fmt.Sprintf("tool:%d:row_b", id)}) {
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

func TestLiveSubagentCardShowsChildHeadlineAndTool(t *testing.T) {
	child := newChildState(t)
	child.AddNarration(child.BeginStep(session.Actor{}), "Tracing the config loader. It has three layers.")
	child.SetActiveToolCall(session.ActiveToolCall{Name: "file.read", ToolCallID: "c", StartedAt: time.Now()})
	v := session.SubagentView{ID: 1, Label: "explore repo", Status: session.SubagentRunning, StartedAt: time.Now(), Child: child}
	out := stripANSI(renderSubagentCard(v, false, "⠋", regionView{}, 80))
	if !strings.Contains(out, "Tracing the config loader.") {
		t.Fatalf("card should show the child's headline:\n%s", out)
	}
	if strings.Contains(out, "three layers") {
		t.Errorf("only the first sentence is the headline:\n%s", out)
	}
	if !strings.Contains(out, "Read file") {
		t.Errorf("card should name the running tool:\n%s", out)
	}

	// Before the child narrates, the card keeps its raw activity tail.
	quiet := newChildState(t)
	v.Child = quiet
	if out := stripANSI(renderSubagentCard(v, false, "⠋", regionView{}, 80)); strings.Contains(out, "Tracing") {
		t.Errorf("a child that has not narrated must not show another's headline:\n%s", out)
	}
}

func TestNowBarAgentRowsShowHeadlineAndModelOnlyWhenDifferent(t *testing.T) {
	in := nowBarBase()
	in.Model, in.Provider = "qwen", "ollama"
	in.Agents = nowBarAgents(2)
	in.AgentHeadlines = []string{"Scanning the handlers.", ""}
	in.Agents[0].Model, in.Agents[0].Provider = "qwen", "ollama" // same as the parent
	in.Agents[1].Model, in.Agents[1].Provider = "gpt-5", "openai"
	plain := stripANSI(strings.Join(planNowBar(in).rows, "\n"))
	if !strings.Contains(plain, "#1  agent1  Scanning the handlers.") {
		t.Errorf("agent row should carry the child's headline:\n%s", plain)
	}
	if strings.Contains(plain, "qwen") {
		t.Errorf("a child on the parent's own model adds nothing:\n%s", plain)
	}
	if !strings.Contains(plain, "gpt-5 @ openai") {
		t.Errorf("a child on another model should name it:\n%s", plain)
	}
}

func TestNowBarLiveMirrorRow(t *testing.T) {
	in := nowBarBase()
	in.Busy, in.TurnStartedAt, in.Spinner = true, nowBarT0.Add(-time.Second), "⠋"
	in.LiveHeadline, in.LiveToolGlyph = "Running the package tests.", "$"
	plan := planNowBar(in)
	if len(plan.rows) != 2 {
		t.Fatalf("mirror + turn row = %d rows:\n%s", len(plan.rows), stripANSI(strings.Join(plan.rows, "\n")))
	}
	first := stripANSI(plan.rows[0])
	for _, want := range []string{"↓", "Running the package tests.", "⠋", "End"} {
		if !strings.Contains(first, want) {
			t.Errorf("mirror row missing %q: %q", want, first)
		}
	}
	if !strings.HasSuffix(strings.TrimRight(first, " "), "End") {
		t.Errorf("End hint should be right-aligned: %q", first)
	}
	if plan.agentRowStart != 2 {
		t.Errorf("agent rows start after the mirror and turn rows, got %d", plan.agentRowStart)
	}

	// Actor rows overflow before the mirror gives way: the 4-row cap holds.
	in.Agents = nowBarAgents(5)
	plan = planNowBar(in)
	if len(plan.rows) != nowBarMaxRows {
		t.Fatalf("rows = %d, want the cap %d", len(plan.rows), nowBarMaxRows)
	}
	if !strings.Contains(stripANSI(plan.rows[0]), "↓") || !strings.Contains(stripANSI(plan.rows[len(plan.rows)-1]), "more") {
		t.Errorf("mirror stays first and agents overflow:\n%s", stripANSI(strings.Join(plan.rows, "\n")))
	}

	// No headline (the viewport follows the live step): no mirror row.
	in.LiveHeadline = ""
	if got := stripANSI(strings.Join(planNowBar(in).rows, "\n")); strings.Contains(got, "End") {
		t.Errorf("mirror must be absent when following:\n%s", got)
	}
}

func TestMirrorRowComesFromTheLiveStepWhenScrolledAway(t *testing.T) {
	m := newTestModel(t)
	m.resize(100, 40)
	m.state.AddMessage(session.RoleUser, "go", session.ContentTypePlain)
	id := m.state.BeginStep(session.Actor{})
	m.state.AddNarration(id, "Running the tests. This may take a while.")
	m.state.SetActiveToolCall(session.ActiveToolCall{Name: "test.run", StepID: id, ToolCallID: "t", StartedAt: time.Now()})
	m.busy, m.turnStartedAt = true, time.Now()

	m.viewportFollow = true
	if got := nowBarOut(m); strings.Contains(stripANSI(got), "End") {
		t.Fatalf("no mirror while following:\n%s", got)
	}
	m.viewportFollow = false
	got := stripANSI(nowBarOut(m))
	if !strings.Contains(got, "↓ Running the tests.") || !strings.Contains(got, "End") {
		t.Fatalf("scrolled away from a live step should mirror it:\n%s", got)
	}
}

func TestApprovalShowsOwnerAndWhy(t *testing.T) {
	m := newTestModel(t)
	m.state.AddMessage(session.RoleUser, "go", session.ContentTypePlain)
	id := m.state.BeginStep(session.Actor{Role: "sdd_implementer", Label: "implementer"})
	m.state.AddNarration(id, "Installing the dependency the build needs. It is pinned.")
	tc := &session.PendingToolCall{Name: "shell.run", Command: "go get example.com/x@v1", Risk: "command", StepID: id}

	w := m.approvalWhyFor(m.state, tc)
	if w.owner != "implementer" || w.why != "Installing the dependency the build needs." {
		t.Fatalf("approvalWhyFor = %+v", w)
	}
	for name, view := range map[string]string{
		"summary":  approvalSummary(tc, session.SandboxInfo{}, false, 80, w),
		"fallback": renderApprovalPanel(tc, session.SandboxInfo{}, false, 80, w),
	} {
		plain := stripANSI(view)
		if !strings.Contains(plain, "implementer wants to run a command") {
			t.Errorf("%s: owner line missing:\n%s", name, plain)
		}
		if !strings.Contains(plain, `why  "Installing the dependency the build needs."`) {
			t.Errorf("%s: why line missing:\n%s", name, plain)
		}
	}

	// The orchestrator is not named; a step without narration has no why.
	m2 := newTestModel(t)
	m2.state.AddMessage(session.RoleUser, "go", session.ContentTypePlain)
	silent := m2.state.BeginStep(session.Actor{})
	tc2 := &session.PendingToolCall{Name: "shell.run", Command: "ls", StepID: silent}
	if w := m2.approvalWhyFor(m2.state, tc2); w != (approvalWhy{}) {
		t.Fatalf("orchestrator step with no narration should add nothing, got %+v", w)
	}
	plain := stripANSI(renderApprovalPanel(tc2, session.SandboxInfo{}, false, 80, m2.approvalWhyFor(m2.state, tc2)))
	if strings.Contains(plain, "why") || strings.Contains(plain, "wants to") {
		t.Errorf("no owner and no narration means no extra lines:\n%s", plain)
	}
	if w := m2.approvalWhyFor(m2.state, &session.PendingToolCall{Name: "x"}); w != (approvalWhy{}) {
		t.Fatalf("unstamped approval should add nothing, got %+v", w)
	}
}

// A child's step IDs live in the child's State. The display copy keeps the
// StepID, and the lookup must use the child, not the parent whose step with
// the same number belongs to someone else.
func TestSubagentApprovalResolvesOwnerAgainstChildState(t *testing.T) {
	m := newTestModel(t)
	m.state.AddMessage(session.RoleUser, "go", session.ContentTypePlain)
	pid := m.state.BeginStep(session.Actor{})
	m.state.AddNarration(pid, "Parent narration that must not leak.")

	child := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	child.AddMessage(session.RoleUser, "review", session.ContentTypePlain)
	cid := child.BeginStep(session.Actor{Role: "reviewer", Label: "reviewer"})
	child.AddNarration(cid, "Checking the migration. It touches two tables.")
	if cid != pid {
		t.Fatalf("test needs colliding step IDs, got parent %d child %d", pid, cid)
	}
	m.state.RegisterSubagent("reviewer", child)
	tc := &session.PendingToolCall{Name: "shell.run", Command: "go vet ./...", Risk: "command", StepID: cid}
	child.SetPendingApproval(tc)

	disp, label := m.pendingApprovalDisplay()
	if disp == nil || label != "reviewer" || disp.StepID != cid {
		t.Fatalf("display copy = %+v label %q; StepID must survive the copy", disp, label)
	}
	w := m.approvalWhyFor(m.approvalOwner(), disp)
	if w.owner != "reviewer" || w.why != "Checking the migration." {
		t.Fatalf("why = %+v", w)
	}
}

// A step that is not the live one can still hold a running call (a later
// step opened while an async call from an earlier one is outstanding); its
// row must keep ticking rather than be served from the cache.
func TestOlderStepWithRunningCallKeepsRendering(t *testing.T) {
	m := newTestModel(t)
	m.resize(100, 40)
	old := stepFixture(t, &m, session.Actor{}, "Kicking off a build.", readEvent("a.go"))
	m.state.SetActiveToolCall(session.ActiveToolCall{Name: "shell.run", Args: "make", StartedAt: time.Now(), StepID: old, ToolCallID: "bg"})
	live := m.state.BeginStep(session.Actor{})
	m.state.AddNarration(live, "Meanwhile reading more.")
	m.busy, m.turnStartedAt = true, time.Now()

	m.invalidateTranscript()
	m.refreshViewport()
	var rendered []stack.NodeID
	nodeRenderHook = func(id stack.NodeID) { rendered = append(rendered, id) }
	t.Cleanup(func() { nodeRenderHook = nil })
	base := m.now()
	m.now = func() time.Time { return base.Add(5 * time.Second) }
	m.refreshViewport()
	want := fmt.Sprintf("step:%d", old)
	found := false
	for _, id := range rendered {
		found = found || id.Key == want
	}
	if !found {
		t.Fatalf("older step %q holds a running call and must re-render on a tick, rendered %v", want, rendered)
	}
}

func TestRunningToolRowInsideStepExpandsOnClickEvenWithGlobalExpand(t *testing.T) {
	m := newTestModel(t)
	m.resize(100, 40)
	id := m.state.BeginStep(session.Actor{})
	m.state.AddNarration(id, "Running the suite.")
	m.state.SetActiveToolCall(session.ActiveToolCall{Name: "shell.run", Args: "go test ./...", StartedAt: time.Now(), StepID: id, ToolCallID: "run"})
	m.state.AppendActiveToolCallOutput("run", "PASS pkg/first\nPASS pkg/b\nPASS pkg/c\nPASS pkg/d\nPASS pkg/e\nPASS pkg/f\nPASS pkg/g\nPASS pkg/h\nPASS pkg/last")
	m.busy, m.turnStartedAt = true, time.Now()
	m.detailExpanded = true // ctrl+g on: settled rows open, running rows stay closed

	row := stack.NodeID{Kind: stack.KindTool, Key: fmt.Sprintf("tool:%d:run", id)}
	m.invalidateTranscript()
	m.refreshViewport()
	if strings.Contains(stripANSI(m.viewport.GetContent()), "PASS pkg/first") {
		t.Fatal("a running call starts collapsed")
	}
	m.toggleExpanded(row)
	m.invalidateTranscript()
	m.refreshViewport()
	if !strings.Contains(stripANSI(m.viewport.GetContent()), "PASS pkg/first") {
		t.Fatalf("one click must expand the running row's output tail:\n%s", stripANSI(m.viewport.GetContent()))
	}
}
