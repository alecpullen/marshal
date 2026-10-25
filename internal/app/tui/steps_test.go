package tui

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/db"
	"marshal/internal/tools/registry"
	"marshal/internal/viewmodel"
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
	if !strings.HasPrefix(header, " ● ") {
		t.Errorf("a settled beat leads with a muted ●, got %q", header)
	}
	if strings.Contains(header, "inferred") {
		t.Errorf("narrated step must not be tagged inferred: %q", header)
	}
	if regexp.MustCompile(`\d+s$`).MatchString(strings.TrimRight(header, " ")) {
		t.Errorf("a quick step does not carry a duration, got %q", header)
	}
	if len(toolRows) == 0 {
		t.Fatalf("no tool row:\n%s", strings.Join(lines, "\n"))
	}
	// The read is the first of the beat's two calls.
	for _, r := range toolRows {
		if !strings.HasPrefix(r, strings.Repeat(" ", gutterWidth)+"├ read    parser.go") {
			t.Errorf("a call hangs off the beat's spine: %q", r)
		}
	}
	// The complete narration is visible without expanding the step.
	var cont bool
	for _, l := range lines {
		if strings.Contains(l, "somewhere in the lexer") {
			cont = true

		}
	}
	if !cont {
		t.Error("continuation line missing")
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

func TestNarrationWrapsCompletelyWithoutDuplication(t *testing.T) {
	for _, d := range []bool{false, true} {
		for _, width := range []int{40, 80, 120} {
			t.Run(fmt.Sprintf("expanded=%v/width=%d", d, width), func(t *testing.T) {
				narration := "The baseline tests are running; I’ll delegate the document import while I prepare the task-by-task execution plan. Second sentence with important details.\n\nThird paragraph with detail."
				// Exercise the production renderer directly at narrow widths too.
				si := &viewmodel.StepInfo{Narration: []*session.Message{{Content: narration}}}
				n := &viewmodel.Node{Kind: viewmodel.KindStep, Step: si}
				out, _ := renderStep(n, &stepRenderCtx{}, width, d)
				plain := stripANSI(out)
				if strings.Contains(plain, "…") {
					t.Fatalf("narration truncated:\n%s", plain)
				}
				for _, word := range strings.Fields(narration) {
					if strings.Count(plain, word) != strings.Count(narration, word) {
						t.Fatalf("word %q missing or repeated:\n%s", word, plain)
					}
				}
				for _, line := range strings.Split(plain, "\n") {
					if ansi.StringWidth(line) > width {
						t.Fatalf("line exceeds width %d: %q", width, line)
					}
				}
			})
		}
	}
}

func TestNarrationMarkdownRemainsWhole(t *testing.T) {
	for _, text := range []string{"**Checking the parser. Then checking the lexer.**", "```go\nfunc example() {}\n```"} {
		si := &viewmodel.StepInfo{Narration: []*session.Message{{Content: text}}}
		out, _ := renderStep(&viewmodel.Node{Kind: viewmodel.KindStep, Step: si}, &stepRenderCtx{}, 80, false)
		plain := stripANSI(out)
		if strings.Contains(plain, "**") || strings.Contains(plain, "```") {
			t.Fatalf("markdown split or unrendered:\n%s", plain)
		}
		if strings.Contains(text, "example") && !strings.Contains(plain, "example") {
			t.Fatalf("code missing:\n%s", plain)
		}
	}
}

// A narration that wraps keeps its todo tag on its first line: the head
// re-wraps the prose narrower rather than dropping the meta onto a line of
// its own under the head.
func TestWrappedNarrationKeepsItsMetaOnTheFirstLine(t *testing.T) {
	m := newTestModel(t)
	m.resize(80, 30)
	m.state.AddMessage(session.RoleUser, "go", session.ContentTypePlain)
	todos := []db.TodoItem{
		{ID: "t1", Content: "Read", Status: "in_progress", StartedAt: time.Now()},
		{ID: "t2", Content: "Write", Status: "pending"},
		{ID: "t3", Content: "Test", Status: "pending"},
		{ID: "t4", Content: "Ship", Status: "pending"},
	}
	if err := m.state.SetTodos(append([]db.TodoItem(nil), todos...)); err != nil {
		t.Fatalf("SetTodos: %v", err)
	}
	// About 150 characters of plain prose: wide enough to wrap at every
	// width below.
	const narration = "Reading the parser to find the guard. The guard rejects an empty input, and it sits just past the token stream, before the rest of the pass runs at all."
	stepUnder(&m, narration, "a")
	// A second, short beat under the same todo. Only the beat that starts a
	// todo carries its tag.
	stepUnder(&m, "Checking the lexer tables now.", "b")
	m.busy = false

	for _, size := range [][2]int{{80, 30}, {96, 34}} {
		m.resize(size[0], size[1])
		lines := transcriptLines(&m)
		head, later := -1, -1
		for i, l := range lines {
			if strings.Contains(l, "Reading the parser to find the guard.") {
				head = i
			}
			if strings.Contains(l, "Checking the lexer tables now.") {
				later = i
			}
			if strings.TrimSpace(l) == "▸ 1/4" {
				t.Errorf("w=%d: the tag got a line of its own:\n%s", size[0], strings.Join(lines, "\n"))
			}
		}
		if head < 0 {
			t.Fatalf("w=%d: the narration head is missing:\n%s", size[0], strings.Join(lines, "\n"))
		}
		// Every word of the narration must survive the re-wrap: the narrow
		// render moves words off the first line, so taking only the head from
		// it would drop them.
		words := map[string]bool{}
		for _, l := range lines {
			for _, w := range strings.Fields(l) {
				words[w] = true
			}
		}
		for _, want := range strings.Fields(narration) {
			if !words[want] {
				t.Errorf("w=%d: the re-wrap dropped %q:\n%s", size[0], want, strings.Join(lines, "\n"))
			}
		}
		if got := strings.TrimRight(lines[head], " "); !strings.HasSuffix(got, "▸ 1/4") {
			t.Errorf("w=%d: the wrapped head must end with its tag, got %q\n%s", size[0], lines[head], strings.Join(lines, "\n"))
		}
		if later < 0 {
			t.Fatalf("w=%d: the second beat's narration is missing:\n%s", size[0], strings.Join(lines, "\n"))
		}
		got := strings.TrimRight(lines[later], " ")
		if !strings.HasSuffix(got, "Checking the lexer tables now.") {
			t.Errorf("w=%d: a short narration stays on one line, got %q", size[0], lines[later])
		}
		if strings.Contains(got, "▸") {
			t.Errorf("w=%d: a later beat under one todo carries no tag: %q", size[0], lines[later])
		}
	}
}

// A beat's reasoning is its thinking time in the head; expanding the beat
// shows the reasoning under the head, before its calls.
func TestThinkingTimeSitsInTheBeatHead(t *testing.T) {
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
	head := -1
	for i, l := range lines {
		if strings.Contains(l, "Looking at the failing test.") {
			head = i
		}
	}
	if head < 0 || !strings.HasSuffix(strings.TrimRight(lines[head], " "), "thought 2s") {
		t.Fatalf("the head carries the thinking time:\n%s", strings.Join(lines, "\n"))
	}
	if strings.Contains(strings.Join(lines, "\n"), "private reasoning text") {
		t.Error("a collapsed beat must not show its reasoning")
	}

	m.toggleExpanded(viewmodel.NodeID{Kind: viewmodel.KindStep, Key: fmt.Sprintf("step:%d", id)})
	m.invalidateTranscript()
	lines = transcriptLines(&m)
	reason, call := -1, -1
	for i, l := range lines {
		if strings.Contains(l, "private reasoning text") {
			reason = i
		}
		if strings.Contains(l, "└ read") {
			call = i
		}
	}
	if !(head < reason && reason < call) {
		t.Fatalf("an expanded beat shows its reasoning between its head and its calls:\n%s", strings.Join(lines, "\n"))
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

	row := viewmodel.NodeID{Kind: viewmodel.KindTool, Key: fmt.Sprintf("tool:%d:row_a", id)}
	region, ok := regionOf(&m, row)
	if !ok {
		t.Fatal("tool row recorded no region of its own")
	}
	stepRegion, ok := regionOf(&m, viewmodel.NodeID{Kind: viewmodel.KindStep, Key: fmt.Sprintf("step:%d", id)})
	if !ok || stepRegion.endLine-stepRegion.startLine <= region.endLine-region.startLine {
		t.Fatalf("row region %+v should sit inside the step region %+v", region, stepRegion)
	}
	y := m.scrollHintRows() + region.startLine - m.viewport.YOffset()
	updated := clickAt(t, m, 2, y)
	mm := asModel(t, updated)
	if !mm.isExpanded(row) {
		t.Fatal("click on the row did not expand it")
	}
	if mm.isExpanded(viewmodel.NodeID{Kind: viewmodel.KindStep, Key: fmt.Sprintf("step:%d", id)}) || mm.isExpanded(viewmodel.NodeID{Kind: viewmodel.KindTool, Key: fmt.Sprintf("tool:%d:row_b", id)}) {
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

	var rendered []viewmodel.NodeID
	nodeRenderHook = func(id viewmodel.NodeID) { rendered = append(rendered, id) }
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
	var rendered []viewmodel.NodeID
	nodeRenderHook = func(id viewmodel.NodeID) { rendered = append(rendered, id) }
	t.Cleanup(func() { nodeRenderHook = nil })
	target := viewmodel.NodeID{Kind: viewmodel.KindStep, Key: fmt.Sprintf("step:%d", ids[2])}
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

func TestRightMetaDropOrder(t *testing.T) {
	p := metaParts{owner: "reviewer #1", role: "reviewer", model: "gpt-5 @ openai", dur: "1m 4s"}
	plain := func(width int) string {
		text, _ := rightMeta(p, 40, width)
		return stripANSI(text)
	}
	if got := plain(200); got != "reviewer #1 · gpt-5 @ openai · 1m 4s" {
		t.Errorf("full meta = %q", got)
	}
	// Narrowing drops the model first, then the duration, then shortens the
	// owner, and only then drops the owner.
	steps := []struct {
		width int
		want  string
	}{
		{3 + 2 + 24 + len("reviewer #1 · 1m 4s"), "reviewer #1 · 1m 4s"},
		{3 + 2 + 24 + len("reviewer #1"), "reviewer #1"},
		{3 + 2 + 24 + len("reviewer"), "reviewer"},
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
	var rendered []viewmodel.NodeID
	nodeRenderHook = func(id viewmodel.NodeID) { rendered = append(rendered, id) }
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
	m.expandAll = true // ctrl+g on: settled rows open, running rows stay closed

	row := viewmodel.NodeID{Kind: viewmodel.KindTool, Key: fmt.Sprintf("tool:%d:run", id)}
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

// Each session numbers its steps from 1; expanding step 1 in the main
// transcript must not expand step 1 inside a subagent's view.
func TestExpandStateDoesNotLeakIntoSubagentViews(t *testing.T) {
	m := newTestModel(t)
	child := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	v := m.state.RegisterSubagent("reviewer", child)
	step1 := viewmodel.NodeID{Kind: viewmodel.KindStep, Key: "step:1"}

	m.toggleExpanded(step1)
	if !m.isExpanded(step1) {
		t.Fatal("main toggle did not stick")
	}
	m.viewStack = append(m.viewStack, v)
	if m.isExpanded(step1) {
		t.Fatal("the main transcript's expansion leaked into the subagent view")
	}
	m.toggleExpanded(step1)
	m.viewStack = nil
	if !m.isExpanded(step1) {
		t.Fatal("toggling inside the subagent flipped the main transcript's step")
	}
}
