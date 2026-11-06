package tui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/acpwire"
	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
	"marshal/internal/viewmodel"
)

func groupEvents() []registry.AuditEvent {
	return []registry.AuditEvent{
		{ToolName: "file.read", Args: json.RawMessage(`{"path":"budget.go"}`), ResultSummary: "42 lines"},
		{ToolName: "file.read", Args: json.RawMessage(`{"path":"runner.go"}`), ResultSummary: "88 lines"},
		{ToolName: "file.read", Args: json.RawMessage(`{"path":"execute.go"}`), ResultSummary: "12 lines"},
	}
}

// plainLines strips styling and splits rendered output into lines.
func plainLines(s string) []string {
	return strings.Split(strings.TrimRight(ansi.Strip(s), "\n"), "\n")
}

// The verb sits in its column, the subject follows, and the result ends at
// the row's right edge. A long subject gives way to the result.
func TestCallRowLaysOutVerbSubjectAndResult(t *testing.T) {
	ev := registry.AuditEvent{ToolName: "file.read", Args: json.RawMessage(`{"path":"evals/tokenizer.toml"}`), ResultSummary: "18 lines"}
	line := plainLines(renderCallRow(wireTool("", ev), nil, false, nil, 60))[0]
	if !strings.HasPrefix(line, "read    evals/tokenizer.toml") || !strings.HasSuffix(line, "18 lines") || ansi.StringWidth(line) != 60 {
		t.Fatalf("row = %q", line)
	}
	ev.Args = json.RawMessage(`{"path":"` + strings.Repeat("deep/", 20) + `file.go"}`)
	line = plainLines(renderCallRow(wireTool("", ev), nil, false, nil, 60))[0]
	if !strings.Contains(line, "…") || !strings.HasSuffix(line, "18 lines") || ansi.StringWidth(line) > 60 {
		t.Fatalf("a long subject truncates and keeps the result: %q", line)
	}
}

func TestCallResults(t *testing.T) {
	exit0, exit2 := 0, 2
	diff := "--- a/x.go\n+++ b/x.go\n@@ -1 +1,2 @@\n-a\n+b\n+c\n"
	notice := &registry.ToolNotice{Data: map[string]any{registry.NoticeSubagentWork: registry.SubagentWorkReference{TaskKey: "k", RuntimeEpoch: "e", AgentID: 3}}}
	cases := []struct {
		name string
		ev   registry.AuditEvent
		want string
	}{
		{"an error", registry.AuditEvent{ToolName: "file.read", Error: "no such file\nmore"}, "no such file"},
		{"a command's exit and time", registry.AuditEvent{ToolName: "shell.run", CommandExitCode: &exit0, Duration: 3 * time.Second, ResultSummary: "ignored"}, "exit 0 · 3s"},
		{"a failing exit", registry.AuditEvent{ToolName: "test.run", CommandExitCode: &exit2}, "exit 2"},
		{"an edit's diff stat", registry.AuditEvent{ToolName: "file.edit", ResultContent: diff}, "+2 −1"},
		{"a whole-file write's diff stat", registry.AuditEvent{ToolName: "file.write", ResultContent: diff, ResultSummary: "Wrote x.go"}, "+2 −1"},
		{"a whole-file write without a diff", registry.AuditEvent{ToolName: "file.write", ResultContent: "new contents", ResultSummary: "Wrote x.go"}, "Wrote x.go"},
		{"a started subagent", registry.AuditEvent{ToolName: "agent.run", Notice: notice, ResultSummary: "started as subagent 3 — review"}, "⧉ #3 started"},
		{"a legacy started subagent", registry.AuditEvent{ToolName: "agent.run", ResultSummary: "started as subagent 7 — review"}, "⧉ #7 started"},
		{"a hook leads", registry.AuditEvent{ToolName: "file.read", ResultSummary: "4 lines", Hooks: []registry.HookMetadata{{Rewrote: true}}}, "hook rewrote · 4 lines"},
		{"a blocking hook leads the error", registry.AuditEvent{
			ToolName: "file.write_patch",
			Error:    "blocked by pre_tool_use hook: lint gate",
			Hooks:    []registry.HookMetadata{{Event: "pre_tool_use", Decision: "block", Reason: "lint gate"}},
		}, "hook blocked · blocked by pre_tool_use hook: lint gate"},
		{"the summary", registry.AuditEvent{ToolName: "repo.search", ResultSummary: "4 matches"}, "4 matches"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tool := wireTool("", c.ev)
			if got := ansi.Strip(callResult(tool, tool.Calls[0])); got != c.want {
				t.Fatalf("callResult = %q, want %q", got, c.want)
			}
		})
	}
}

// An edit is one line until it is expanded; then its diff hangs under the
// subject.
func TestEditRowIsOneLineUntilExpanded(t *testing.T) {
	ev := registry.AuditEvent{ToolName: "file.edit", Args: json.RawMessage(`{"path":"x.go"}`),
		ResultContent: "--- a/x.go\n+++ b/x.go\n@@ -1 +1 @@\n-old line\n+new line\n"}
	if got := plainLines(renderCallRow(wireTool("", ev), nil, false, nil, 80)); len(got) != 1 {
		t.Fatalf("collapsed edit = %q", got)
	}
	open := plainLines(renderCallRow(wireTool("", ev), nil, true, nil, 80))
	body := strings.Join(open[1:], "\n")
	if !strings.Contains(body, "new line") || !strings.HasPrefix(open[1], strings.Repeat(" ", verbWidth)) {
		t.Fatalf("expanded edit = %q", open)
	}
}

// A failed call shows the last lines of its output collapsed; expanding it
// shows the whole output and the error.
func TestFailedCallShowsItsTailCollapsed(t *testing.T) {
	code := 1
	ev := registry.AuditEvent{ToolName: "shell.run", Args: json.RawMessage(`{"command":"go test ./..."}`), CommandExitCode: &code,
		ResultContent: "line 1\nline 2\nline 3\nline 4\n--- FAIL: TestX"}
	lines := plainLines(renderCallRow(wireTool("", ev), nil, false, nil, 80))
	if len(lines) != 1+viewmodel.FailedTailLines || !strings.Contains(lines[len(lines)-1], "--- FAIL: TestX") || strings.Contains(strings.Join(lines, "\n"), "line 1") {
		t.Fatalf("collapsed failure = %q", lines)
	}
	if open := ansi.Strip(renderCallRow(wireTool("", ev), nil, true, nil, 80)); !strings.Contains(open, "line 1") {
		t.Fatalf("expanded failure = %q", open)
	}
}

func TestGroupRowCollapsedIsOneLineExpandedOnePerCall(t *testing.T) {
	collapsed := plainLines(renderGroupRow(wireTool("", groupEvents()...), false, 80))
	if len(collapsed) != 1 || !strings.Contains(collapsed[0], "budget.go, runner.go, execute.go") || !strings.HasSuffix(collapsed[0], "×3") {
		t.Fatalf("collapsed run = %q", collapsed)
	}
	expanded := plainLines(renderGroupRow(wireTool("", groupEvents()...), true, 80))
	if len(expanded) != 4 {
		t.Fatalf("expanded run = %q", expanded)
	}
	for i, want := range []string{"budget.go · 42 lines", "runner.go · 88 lines", "execute.go · 12 lines"} {
		if !strings.Contains(expanded[i+1], want) {
			t.Fatalf("expanded line %d = %q, want %q", i+1, expanded[i+1], want)
		}
	}
}

// An overflowing run truncates its subjects and keeps its count, wide runes
// included.
func TestGroupRowFitsItsWidth(t *testing.T) {
	var evs []registry.AuditEvent
	for _, p := range []string{"内部/非常に長いパス/ファイル一.go", "internal/app/tui/steps.go", "internal/app/tui/viewport_render.go", "internal/app/tui/model.go"} {
		evs = append(evs, registry.AuditEvent{ToolName: "file.read", ResultSummary: "ok", Args: json.RawMessage(`{"path":"` + p + `"}`)})
	}
	for _, w := range []int{30, 60} {
		lines := plainLines(renderGroupRow(wireTool("", evs...), false, w))
		if len(lines) != 1 || ansi.StringWidth(lines[0]) > w || !strings.Contains(lines[0], "…") || !strings.HasSuffix(lines[0], "×4") {
			t.Fatalf("w=%d: run = %q", w, lines)
		}
	}
}

// A running call names what it acts on, never its raw arguments, and shows a
// bounded tail of its output until it is opened.
func TestActiveCallRowNamesItsSubject(t *testing.T) {
	now := time.Unix(1000, 0)
	c := &stepRenderCtx{toolSpinner: "⠋", now: now}
	edit := session.ActiveToolCall{Name: "file.edit", Path: "evals/thresholds.toml", Args: "evals/thresholds.toml", StartedAt: now.Add(-2 * time.Second)}
	if line := plainLines(renderActiveCallRow(wireRunning(edit), c, false, 80))[0]; !strings.HasPrefix(line, "edit    evals/thresholds.toml") || !strings.HasSuffix(line, "⠋ 2s") {
		t.Fatalf("running edit = %q", line)
	}
	var out []string
	for i := 1; i <= 10; i++ {
		out = append(out, fmt.Sprintf("PASS pkg/%d", i))
	}
	run := session.ActiveToolCall{Name: "shell.run", Args: "go test ./...", StartedAt: now, Output: strings.Join(out, "\n")}
	collapsed := plainLines(renderActiveCallRow(wireRunning(run), c, false, 80))
	if len(collapsed) != 1+liveTailLines || strings.Contains(strings.Join(collapsed, "\n"), "pkg/1\n") || !strings.Contains(collapsed[len(collapsed)-1], "pkg/10") {
		t.Fatalf("collapsed running command = %q", collapsed)
	}
	if expanded := plainLines(renderActiveCallRow(wireRunning(run), c, true, 80)); len(expanded) != 11 {
		t.Fatalf("expanded running command = %q", expanded)
	}
}

func TestPlanRow(t *testing.T) {
	cases := []struct {
		name string
		p    viewmodel.PlanUpdate
		want string
	}{
		{"active item", viewmodel.PlanUpdate{Status: []string{"completed", "completed", "in_progress", "pending"}, Active: 3, ActiveText: "Run the eval harness unit tests"},
			"plan    ✓ ✓ ▸ ·  3/4  Run the eval harness unit tests"},
		{"all done", viewmodel.PlanUpdate{Status: []string{"completed", "completed"}}, "plan    ✓ ✓  2/2  done"},
		{"nothing in progress", viewmodel.PlanUpdate{Status: []string{"completed", "pending"}}, "plan    ✓ ·  1/2"},
		{"cleared", viewmodel.PlanUpdate{Cleared: true}, "plan    cleared"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := plainLines(renderPlanRow(wirePlan(c.p), 80))[0]; got != c.want {
				t.Fatalf("plan row = %q, want %q", got, c.want)
			}
		})
	}
	long := viewmodel.PlanUpdate{Status: make([]string, maxPlanMarks+1), Active: 1, ActiveText: "first"}
	for i := range long.Status {
		long.Status[i] = "pending"
	}
	long.Status[0] = "in_progress"
	if got := plainLines(renderPlanRow(wirePlan(long), 80))[0]; strings.Contains(got, "·") || !strings.Contains(got, "▱") || !strings.Contains(got, "1/13  first") {
		t.Fatalf("a long plan draws blocks: %q", got)
	}
}

// Streaming reasoning is drawn below every row of the newest turn, so it never
// lands between a beat's head and its calls.
func TestLiveThinkingDrawsBelowTheSpine(t *testing.T) {
	m := newTestModel(t)
	m.resize(100, 40)
	m.busy = true
	id := stateOf(m).BeginStep(session.Actor{})
	stateOf(m).AddNarration(id, "Running the suite.")
	ev := readEvent("a.go")
	ev.StepID = id
	ev.ToolCallID = "r"
	ev.Timestamp = time.Now()
	stateOf(m).LogToolCall(ev)
	stateOf(m).SetActiveToolCall(session.ActiveToolCall{Name: "shell.run", Args: "go test ./...", StepID: id, ToolCallID: "run", StartedAt: time.Now()})
	stateOf(m).BeginStreaming()
	stateOf(m).AppendThinking("weighing the next step\n")
	m.refreshViewport()

	content := stripANSI(m.viewport.GetContent())
	lines := strings.Split(content, "\n")
	call, think := -1, -1
	for i, l := range lines {
		if call < 0 && strings.Contains(l, "go test") {
			call = i
		}
		if think < 0 && strings.Contains(l, "thinking") {
			think = i
		}
	}
	if call < 0 || think < 0 {
		t.Fatalf("call line %d, thinking line %d:\n%s", call, think, content)
	}
	if call > think {
		t.Fatalf("the running call must come before the reasoning:\n%s", content)
	}
	for _, l := range lines[think+1:] {
		if strings.Contains(l, "├") || strings.Contains(l, "└") {
			t.Fatalf("no spine row may follow the reasoning:\n%s", content)
		}
	}
}

// Every row but the last hangs off ├ and the last off └; an expanded row's
// detail carries the spine down to the next row, and nothing does after the
// last.
func TestSpineConnectsRowsToTheirBeat(t *testing.T) {
	n := beatNode("Reading three files.", 3)
	for i := range n.Children {
		n.Children[i].Tools[0].ToolName = "git.status" // distinct rows that do not merge
		n.Children[i].Tools[0].ResultContent = fmt.Sprintf("body %d", i)
	}
	beat, rows := wireOf(n, "")
	rowID := func(i int) nodeID {
		return nodeID{Kind: n.Children[i].Kind.String(), Key: n.Children[i].ID.Key}
	}
	out, _ := renderStep(beat, rows, &stepRenderCtx{expanded: func(id nodeID, inherited bool) bool {
		return id == rowID(0) || id == rowID(2)
	}}, 80, false)
	lines := plainLines(out)
	want := []string{"├", "│", "├", "└", " "}
	if len(lines) != 1+len(want) {
		t.Fatalf("beat = %q", lines)
	}
	for i, g := range want {
		if got := string([]rune(lines[i+1])[gutterWidth]); got != g {
			t.Errorf("line %d connector = %q, want %q: %q", i+1, got, g, lines[i+1])
		}
	}
}

// No row on the spine is wider than the frame, at any width.
func TestSpineRowsFitTheFrame(t *testing.T) {
	code := 1
	n := beatNode("A beat with every kind of row and a long narration that wraps across lines.", 0)
	t0 := n.Step.Step.StartedAt
	add := func(ch *viewmodel.Node) { n.Children = append(n.Children, ch) }
	for i, ev := range []registry.AuditEvent{
		{ToolName: "shell.run", Args: json.RawMessage(`{"command":"` + strings.Repeat("go test ./... ", 12) + `"}`), CommandExitCode: &code, ResultContent: strings.Repeat("x", 300)},
		{ToolName: "file.edit", Args: json.RawMessage(`{"path":"` + strings.Repeat("dir/", 30) + `f.go"}`), ResultContent: "--- a\n+++ b\n@@ -1 +1 @@\n-" + strings.Repeat("y", 200) + "\n+z\n"},
	} {
		ev.Timestamp, ev.ToolCallID = t0.Add(time.Duration(i)*time.Second), fmt.Sprintf("c%d", i)
		add(&viewmodel.Node{ID: viewmodel.ToolID(ev), Kind: viewmodel.KindTool, Tools: []registry.AuditEvent{ev}})
	}
	add(&viewmodel.Node{ID: viewmodel.NodeID{Kind: viewmodel.KindTool, Key: "group"}, Kind: viewmodel.KindTool, Tools: groupEvents()})
	add(&viewmodel.Node{ID: viewmodel.NodeID{Kind: viewmodel.KindPlanUpdate, Key: "plan"}, Kind: viewmodel.KindPlanUpdate,
		PlanUpdate: &viewmodel.PlanUpdate{Status: []string{"completed", "in_progress"}, Active: 2, ActiveText: strings.Repeat("long todo ", 20)}})
	add(&viewmodel.Node{ID: viewmodel.NodeID{Kind: viewmodel.KindTool, Key: "live"}, Kind: viewmodel.KindTool, Live: true,
		Active: &session.ActiveToolCall{Name: "shell.run", Args: strings.Repeat("make all ", 20), StartedAt: t0, Output: strings.Repeat("o", 300)}})
	beat, rows := wireOf(n, "")
	for _, width := range []int{40, 80, 120} {
		for _, open := range []bool{false, true} {
			out, _ := renderStep(beat, rows, &stepRenderCtx{now: t0}, width, open)
			for _, l := range plainLines(out) {
				if w := ansi.StringWidth(l); w > width {
					t.Errorf("width=%d open=%v: %d-cell line %q", width, open, w, l)
				}
			}
		}
	}
}

// Copying a call row starts at its subject: the connector and the verb are
// gutter, and the detail under a row that is not the last starts at the same
// column, so a copied failure tail lines up with its command.
func TestSpineRowsCopyFromTheSubjectColumn(t *testing.T) {
	subject := spineWidth + verbWidth
	for _, text := range []string{
		"   ├ shell   go test ./...                 exit 1",
		"   └ read    a.go                          5 lines",
		"   │         --- FAIL: TestX (0.01s)",
	} {
		if sp := spanOf(selRow{text: text}, 80); sp.start != subject || sp.bare {
			t.Errorf("%q copies from column %d (bare=%v), want %d", text, sp.start, sp.bare, subject)
		}
	}
}

// A running call whose work already has a row in the box points at that row
// instead of spinning and repeating the work's own output.
func TestRunningCallPointsAtItsBoxRow(t *testing.T) {
	now := time.Unix(1000, 0)
	item := acpwire.WorkItem{
		TaskKey: "task-1", Kind: "agent", SourceID: "3", DisplayID: "3", State: "running",
		Origin: acpwire.WorkOrigin{ToolCallID: "g"},
	}
	c := &stepRenderCtx{now: now, toolSpinner: "⠋", pointer: workPointer([]acpwire.WorkItem{item})}
	call := session.ActiveToolCall{Name: "agent.run", Args: `{"description":"review the config change"}`,
		ToolCallID: "g", StartedAt: now, Output: "reviewing the config change"}
	lines := plainLines(renderActiveCallRow(wireRunning(call), c, false, 80))
	if len(lines) != 1 || !strings.HasSuffix(lines[0], "→ ⧉ #3") {
		t.Fatalf("a pointed call = %q", lines)
	}
	if body := strings.Join(lines, "\n"); strings.Contains(body, "⠋") || strings.Contains(body, "reviewing the config change") {
		t.Fatalf("a pointed call keeps its spinner or its output: %q", lines)
	}
}

// A call whose work has no box row keeps its spinner: only a live item that
// names this call as its origin points a run at the box.
func TestRunningCallWithoutWorkKeepsItsSpinner(t *testing.T) {
	now := time.Unix(1000, 0)
	items := []acpwire.WorkItem{
		{TaskKey: "task-1", Kind: "agent", SourceID: "3", DisplayID: "3", State: "running",
			Origin: acpwire.WorkOrigin{ToolCallID: "other"}},
		{TaskKey: "task-2", Kind: "agent", SourceID: "4", DisplayID: "4", State: "completed",
			Origin: acpwire.WorkOrigin{ToolCallID: "r"}},
	}
	c := &stepRenderCtx{now: now, toolSpinner: "⠋", pointer: workPointer(items)}
	call := session.ActiveToolCall{Name: "file.read", Path: "a.go", ToolCallID: "r", StartedAt: now.Add(-2 * time.Second)}
	line := plainLines(renderActiveCallRow(wireRunning(call), c, false, 80))[0]
	if !strings.HasSuffix(line, "⠋ 2s") || strings.Contains(line, "→") {
		t.Fatalf("a call without work = %q", line)
	}
}

// A dispatch row names the subagent it started in its call result, so the row
// pairs with the child it launched without parsing display text.
func TestStepRowNamesItsSubagentInCallResult(t *testing.T) {
	ev := registry.AuditEvent{
		ToolName: "agent.run",
		Args:     json.RawMessage(`{"description":"review the config change","role":"reviewer"}`),
		Notice: &registry.ToolNotice{Kind: registry.NoticeSubagentWork,
			Data: map[string]any{registry.NoticeSubagentWork: registry.SubagentWorkReference{
				TaskKey: "task-1", RuntimeEpoch: "epoch-1", AgentID: 3, ChildSessionID: "child"}}},
	}
	line := plainLines(renderCallRow(wireTool("", ev), nil, false, nil, 80))[0]
	if !strings.Contains(line, "⧉ #3 started") {
		t.Fatalf("dispatch row = %q, want the subagent it started", line)
	}
}

// A patch touching several files renders every one of them: stats and body,
// each under its own header.
func TestCallRowRendersEveryFileOfAPatch(t *testing.T) {
	diff := unifiedDiffForFile("alpha.go", 4) + unifiedDiffForFile("beta.go", 4) + unifiedDiffForFile("gamma.go", 4)
	out := stripANSI(renderCallRow(wireTool("", registry.AuditEvent{
		ToolName:      "file.write_patch",
		ResultSummary: "applied patch",
		ResultContent: diff,
	}), nil, true, nil, 80))
	for _, path := range []string{"alpha.go", "beta.go", "gamma.go"} {
		if !strings.Contains(out, path) {
			t.Fatalf("diff output missing stat header for %s:\n%s", path, out)
		}
	}
	if !strings.Contains(out, "+4 −4") {
		t.Fatalf("diff output missing +N −M stats:\n%s", out)
	}
	// The flat 20-line cap this replaces would have dropped gamma.go's body.
	if !strings.Contains(out, "new line 3") {
		t.Fatalf("later files lost their body lines:\n%s", out)
	}
}

// A long diff line must be clipped, not wrapped: a wrapped continuation
// carries no nested rail and breaks the chrome.
func TestDiffLinesAreTruncatedNotWrapped(t *testing.T) {
	long := strings.Repeat("x", 400)
	diff := "--- a/x.go\n+++ b/x.go\n@@ -1,1 +1,2 @@\n+" + long + "\n"
	event := registry.AuditEvent{
		ToolName:      "file.write_patch",
		ResultContent: diff,
		FilesChanged:  []string{"x.go"},
	}
	const width = 80
	for i, line := range strings.Split(strings.TrimRight(renderCallRow(wireTool("", event), nil, true, nil, width), "\n"), "\n") {
		if got := ansi.StringWidth(line); got > width {
			t.Fatalf("row %d is %d cells wide, want <= %d — it will wrap and break the rail", i, got, width)
		}
	}
}

// A collapsed row carries no body; expanding it is how a reader asks for one.
func TestCallRowHidesItsResultWhenCollapsed(t *testing.T) {
	out := stripANSI(renderCallRow(wireTool("", registry.AuditEvent{
		ToolName:      "agent.run",
		ResultSummary: "subagent completed",
		ResultContent: "full subagent report body",
	}), nil, false, nil, 80))
	if strings.Contains(out, "full subagent report body") {
		t.Fatalf("collapsed row must not render result content:\n%s", out)
	}
}

func TestCallRowShowsItsResultWhenExpanded(t *testing.T) {
	out := stripANSI(renderCallRow(wireTool("", registry.AuditEvent{
		ToolName:      "agent.run",
		ResultSummary: "subagent completed",
		ResultContent: "full subagent report body",
	}), nil, true, nil, 80))
	if !strings.Contains(out, "full subagent report body") {
		t.Fatalf("expanded row must render result content:\n%s", out)
	}
}

func TestFailedEditRowShowsItsFailureWhenExpanded(t *testing.T) {
	exitCode := 2
	out := stripANSI(renderCallRow(wireTool("", registry.AuditEvent{
		ToolName:        "file.write_patch",
		Error:           "patch rejected",
		Args:            []byte(`{"path":"a.go"}`),
		CommandExitCode: &exitCode,
		ResultContent:   "patch output",
	}), nil, true, nil, 80))
	if !strings.Contains(out, "(exit code 2)") || !strings.Contains(out, `args: {"path":"a.go"}`) {
		t.Fatalf("expanded failed diff tool must show failure details:\n%s", out)
	}
}

func TestCallRowCapsItsExpandedResult(t *testing.T) {
	var lines []string
	for i := 0; i < 20; i++ {
		lines = append(lines, fmt.Sprintf("report line %d", i))
	}
	out := stripANSI(renderCallRow(wireTool("", registry.AuditEvent{
		ToolName:      "agent.run",
		ResultSummary: "subagent completed",
		ResultContent: strings.Join(lines, "\n"),
	}), nil, true, nil, 80))
	if !strings.Contains(out, "… 8 more lines") {
		t.Fatalf("expanded content must be capped with an elision marker:\n%s", out)
	}
	if strings.Contains(out, "report line 19") {
		t.Fatalf("expanded content exceeded the cap:\n%s", out)
	}
}

func TestCallRowFitsWidthWithWideRunes(t *testing.T) {
	out := renderCallRow(wireTool("", registry.AuditEvent{
		ToolName:      "file.read",
		ResultSummary: "読み込み完了 — これは幅を超えるとても長い日本語のサマリーです",
	}), nil, false, nil, 40)
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if w := ansi.StringWidth(line); w > 40 {
			t.Fatalf("line is %d cells wide, budget 40: %q", w, line)
		}
	}
}

// Expanded, a diff shows every line: no per-file cap and no file cap.
func TestExpandedCallRowShowsTheWholeDiff(t *testing.T) {
	var diff strings.Builder
	diff.WriteString("--- a/file.go\n+++ b/file.go\n")
	for i := 0; i < 17; i++ {
		diff.WriteString(fmt.Sprintf("+line %d\n", i))
	}
	event := registry.AuditEvent{
		ToolName:      "file.write_patch",
		ResultContent: diff.String(),
	}
	expanded := renderCallRow(wireTool("", event), nil, true, nil, 80)
	if strings.Contains(expanded, "more lines") {
		t.Fatalf("expected expanded diff to show every line, got: %s", expanded)
	}
	if n := strings.Count(expanded, "+line "); n != 17 {
		t.Fatalf("expected all 17 added lines, got %d", n)
	}
}

func TestExpandedCallRowShowsTheFailureDetail(t *testing.T) {
	longArgs := `{"command": "run a very long command that keeps going and going until it wraps well past the sixty cell budget of this test renderer"}`
	event := registry.AuditEvent{ToolName: "shell.run", Error: "boom", Args: []byte(longArgs)}

	expanded := stripANSI(renderCallRow(wireTool("", event), nil, true, nil, 60))
	if !strings.Contains(expanded, "error: boom") {
		t.Fatalf("expected the error detail line, got:\n%q", expanded)
	}
	if !strings.Contains(expanded, "args: ") {
		t.Fatalf("expected the args detail line, got:\n%q", expanded)
	}
	for _, line := range strings.Split(expanded, "\n") {
		if w := ansi.StringWidth(line); w > 60 {
			t.Errorf("line %d cells wide exceeds the 60-cell budget: %q", w, line)
		}
	}

	collapsed := stripANSI(renderCallRow(wireTool("", event), nil, false, nil, 60))
	if strings.Contains(collapsed, "args: ") {
		t.Fatalf("collapsed render should not contain the args body:\n%q", collapsed)
	}
}

// The wire caps a call's text. An expanded row it cut reads the whole diff
// from the cache once the row is open, and draws the cut text until then.
func TestExpandedCallRowReadsItsWholeDiff(t *testing.T) {
	m := newTestModel(t)
	m.resize(100, 40)
	var diff strings.Builder
	diff.WriteString("--- a/x.go\n+++ b/x.go\n")
	for i := 0; i < 400; i++ {
		diff.WriteString(fmt.Sprintf("+added line %03d xx\n", i))
	}
	diff.WriteString("+LAST LINE\n")
	stepFixture(t, &m, session.Actor{}, "Editing the file.", registry.AuditEvent{
		ToolName: "file.edit", Args: []byte(`{"path":"x.go"}`), ResultContent: diff.String()})
	counter := &stackNodeCounter{Session: m.sess}
	m.sess = counter
	content := func() string { return stripANSI(strings.Join(m.contentLines, "\n")) }

	m.invalidateTranscript()
	m.refreshViewport()
	if len(m.detailWant) != 0 {
		t.Fatalf("a collapsed row asked for %d details", len(m.detailWant))
	}
	if strings.Contains(content(), "LAST LINE") {
		t.Fatal("a collapsed row must not draw the diff")
	}

	updated, cmd := m.Update(tea.KeyPressMsg{Code: 'g', Mod: tea.ModCtrl})
	m = asModel(t, updated)
	if strings.Contains(content(), "LAST LINE") {
		t.Fatal("the wire's cut text is all the row has until the detail lands")
	}
	if counter.calls != 0 {
		t.Fatalf("a render made %d StackNode calls, want none", counter.calls)
	}
	if cmd == nil {
		t.Fatal("expanding the row must send the detail it asked for")
	}

	m = drainCmds(m, cmd)
	if !strings.Contains(content(), "LAST LINE") {
		t.Fatalf("the expanded row must draw the whole diff:\n%s", content())
	}
	if counter.calls != 1 {
		t.Fatalf("want one StackNode call, got %d", counter.calls)
	}
}

// A running row never asks for detail: it draws the wire's own tail, however
// much the wire cut.
func TestRunningRowsNeverAskForDetail(t *testing.T) {
	m := newTestModel(t)
	m.resize(100, 40)
	m.busy, m.turnStartedAt = true, time.Now()
	stateOf(m).AddMessage(session.RoleUser, "Run the evals", session.ContentTypePlain)
	id := stateOf(m).BeginStep(session.Actor{})
	stateOf(m).AddNarration(id, "Running the eval suite now.")
	atc := session.ActiveToolCall{Name: "shell.run", Args: "go test ./evals/...", StepID: id,
		ToolCallID: "c1", StartedAt: time.Now()}
	stateOf(m).SetActiveToolCall(atc)
	stateOf(m).AppendActiveToolCallOutput("c1", strings.Repeat("ok\n", acpwire.WireTextCap))
	m.toggleExpanded(activeNodeID(atc))

	m.invalidateTranscript()
	m.refreshViewport()
	if len(m.detailWant) != 0 {
		t.Fatalf("a running row asked for %d details", len(m.detailWant))
	}
}

// toolBody draws the wire's text, and the cache's whole text when the wire
// cut it, so a cut call still shows all of it.
func TestToolBodyTakesTheWholeTextFromTheDetail(t *testing.T) {
	full := `{"path":"a.go","content":"` + strings.Repeat("x", 5000) + `END"}`
	var indented bytes.Buffer
	if err := json.Indent(&indented, []byte(full), "", "  "); err != nil {
		t.Fatalf("json.Indent: %v", err)
	}
	// Joined with no separator, so a wrap cannot split what the test looks
	// for.
	joined := func(lines []string) string { return stripANSI(strings.Join(lines, "")) }

	failed := acpwire.WireCall{Args: full[:acpwire.WireTextCap], Error: "boom", Truncated: true}
	got := joined(toolBody("file.write", failed, &acpwire.CallDetail{Args: indented.String()}, 60))
	if !strings.Contains(got, `args: {"path":"a.go","content":"`) || !strings.HasSuffix(got, `END"}`) {
		t.Fatalf("the detail's args = %q", got)
	}

	got = joined(toolBody("file.write", failed, &acpwire.CallDetail{Args: "not json"}, 60))
	if !strings.HasSuffix(got, "args: not json") {
		t.Fatalf("args that do not parse pass through = %q", got)
	}

	// An empty wire output means the call had no result, and the detail's
	// summary fallback is not one.
	run := acpwire.WireCall{Args: "go test", Truncated: true}
	if lines := toolBody("shell.run", run, &acpwire.CallDetail{Output: "exit 0"}, 60); len(lines) != 0 {
		t.Fatalf("a call with no result drew %q", lines)
	}
}

// The renderer decides which rows carry a command line's exit and sandbox, and
// the projection decides which calls elide the workdir. The two sets must stay
// equal: a divergence shows a row without its exit code, or a subject that
// repeats the path the row already implies.
func TestShellFamilyMatchesTheProjection(t *testing.T) {
	for _, name := range []string{"shell.run", "test.run", "file.read", "file.edit", "repo.search", "agent.run", "git.diff", ""} {
		if got, want := isShellFamily(name), viewmodel.IsShellFamily(name); got != want {
			t.Errorf("isShellFamily(%q) = %v, want %v", name, got, want)
		}
	}
}
