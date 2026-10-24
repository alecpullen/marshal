package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

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

func TestCallVerbs(t *testing.T) {
	cases := map[string]string{
		"file.read":        "read",
		"file.edit":        "edit",
		"file.write_patch": "edit",
		"patch.apply":      "edit",
		"file.write":       "write",
		"shell.run":        "shell",
		"shell.sudo":       "shell",
		"test.run":         "shell",
		"repo.search":      "search",
		"codebase.search":  "search",
		"symbols.find":     "search",
		"agent.run":        "agent",
		"agent.await":      "agent",
		"web.fetch":        "web",
		"browser.navigate": "web",
		"todo.write":       "plan",
		"mcp.github.issue": "mcp",
		"git.status":       "git",
		"skill.load":       "skill",
	}
	for name, want := range cases {
		if got := callVerb(name); got != want {
			t.Errorf("callVerb(%q) = %q, want %q", name, got, want)
		}
	}
}

// A call whose arguments name nothing falls back to the rest of its tool
// name, but never just repeats the verb.
func TestFallbackSubjectNeverRepeatsTheVerb(t *testing.T) {
	cases := map[string]string{"git.status": "status", "agent.await": "await", "file.read": "", "file.write": "", "todos": ""}
	for name, want := range cases {
		if got := fallbackSubject(name); got != want {
			t.Errorf("fallbackSubject(%q) = %q, want %q", name, got, want)
		}
	}
}

// The verb sits in its column, the subject follows, and the result ends at
// the row's right edge. A long subject gives way to the result.
func TestCallRowLaysOutVerbSubjectAndResult(t *testing.T) {
	ev := registry.AuditEvent{ToolName: "file.read", Args: json.RawMessage(`{"path":"evals/tokenizer.toml"}`), ResultSummary: "18 lines"}
	line := plainLines(renderCallRow(ev, false, nil, 60, ""))[0]
	if !strings.HasPrefix(line, "read    evals/tokenizer.toml") || !strings.HasSuffix(line, "18 lines") || ansi.StringWidth(line) != 60 {
		t.Fatalf("row = %q", line)
	}
	ev.Args = json.RawMessage(`{"path":"` + strings.Repeat("deep/", 20) + `file.go"}`)
	line = plainLines(renderCallRow(ev, false, nil, 60, ""))[0]
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
		{"the summary", registry.AuditEvent{ToolName: "repo.search", ResultSummary: "4 matches"}, "4 matches"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ansi.Strip(callResult(c.ev)); got != c.want {
				t.Fatalf("callResult = %q, want %q", got, c.want)
			}
		})
	}
}

func TestCallSubjects(t *testing.T) {
	cases := []struct {
		name string
		ev   registry.AuditEvent
		want string
	}{
		{"a command, first line, workdir elided", registry.AuditEvent{ToolName: "shell.run", Args: json.RawMessage(`{"command":"cd /w && go test ./...\necho done"}`)}, "⌂ go test ./..."},
		{"a search query, quoted", registry.AuditEvent{ToolName: "repo.search", Args: json.RawMessage(`{"query":"auth flow"}`)}, `"auth flow"`},
		{"a semantic search says so", registry.AuditEvent{ToolName: "codebase.search", Args: json.RawMessage(`{"query":"auth flow"}`)}, `semantic "auth flow"`},
		{"a subagent's task", registry.AuditEvent{ToolName: "agent.run", Args: json.RawMessage(`{"description":"review the config change","role":"reviewer"}`)}, "review the config change"},
		{"a path", registry.AuditEvent{ToolName: "file.read", Args: json.RawMessage(`{"path":"a.go"}`)}, "a.go"},
		{"nothing named", registry.AuditEvent{ToolName: "git.status"}, "status"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := callSubject(c.ev, "/w"); got != c.want {
				t.Fatalf("callSubject = %q, want %q", got, c.want)
			}
		})
	}
}

// An edit is one line until it is expanded; then its diff hangs under the
// subject.
func TestEditRowIsOneLineUntilExpanded(t *testing.T) {
	ev := registry.AuditEvent{ToolName: "file.edit", Args: json.RawMessage(`{"path":"x.go"}`),
		ResultContent: "--- a/x.go\n+++ b/x.go\n@@ -1 +1 @@\n-old line\n+new line\n"}
	if got := plainLines(renderCallRow(ev, false, nil, 80, "")); len(got) != 1 {
		t.Fatalf("collapsed edit = %q", got)
	}
	open := plainLines(renderCallRow(ev, true, nil, 80, ""))
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
	lines := plainLines(renderCallRow(ev, false, nil, 80, ""))
	if len(lines) != 1+failedTailLines || !strings.Contains(lines[len(lines)-1], "--- FAIL: TestX") || strings.Contains(strings.Join(lines, "\n"), "line 1") {
		t.Fatalf("collapsed failure = %q", lines)
	}
	if open := ansi.Strip(renderCallRow(ev, true, nil, 80, "")); !strings.Contains(open, "line 1") {
		t.Fatalf("expanded failure = %q", open)
	}
}

func TestGroupRowCollapsedIsOneLineExpandedOnePerCall(t *testing.T) {
	collapsed := plainLines(renderGroupRow(groupEvents(), false, 80, ""))
	if len(collapsed) != 1 || !strings.Contains(collapsed[0], "budget.go, runner.go, execute.go") || !strings.HasSuffix(collapsed[0], "×3") {
		t.Fatalf("collapsed run = %q", collapsed)
	}
	expanded := plainLines(renderGroupRow(groupEvents(), true, 80, ""))
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
		lines := plainLines(renderGroupRow(evs, false, w, ""))
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
	if line := plainLines(renderActiveCallRow(edit, c, false, 80))[0]; !strings.HasPrefix(line, "edit    evals/thresholds.toml") || !strings.HasSuffix(line, "⠋ 2s") {
		t.Fatalf("running edit = %q", line)
	}
	var out []string
	for i := 1; i <= 10; i++ {
		out = append(out, fmt.Sprintf("PASS pkg/%d", i))
	}
	run := session.ActiveToolCall{Name: "shell.run", Args: "go test ./...", StartedAt: now, Output: strings.Join(out, "\n")}
	collapsed := plainLines(renderActiveCallRow(run, c, false, 80))
	if len(collapsed) != 1+liveTailLines || strings.Contains(strings.Join(collapsed, "\n"), "pkg/1\n") || !strings.Contains(collapsed[len(collapsed)-1], "pkg/10") {
		t.Fatalf("collapsed running command = %q", collapsed)
	}
	if expanded := plainLines(renderActiveCallRow(run, c, true, 80)); len(expanded) != 11 {
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
			if got := plainLines(renderPlanRow(&c.p, 80))[0]; got != c.want {
				t.Fatalf("plan row = %q, want %q", got, c.want)
			}
		})
	}
	long := viewmodel.PlanUpdate{Status: make([]string, maxPlanMarks+1), Active: 1, ActiveText: "first"}
	for i := range long.Status {
		long.Status[i] = "pending"
	}
	long.Status[0] = "in_progress"
	if got := plainLines(renderPlanRow(&long, 80))[0]; strings.Contains(got, "·") || !strings.Contains(got, "▱") || !strings.Contains(got, "1/13  first") {
		t.Fatalf("a long plan draws blocks: %q", got)
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
	out, _ := renderStep(n, &stepRenderCtx{expanded: func(id viewmodel.NodeID, inherited bool) bool {
		return id == n.Children[0].ID || id == n.Children[2].ID
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
	for _, width := range []int{40, 80, 120} {
		for _, open := range []bool{false, true} {
			out, _ := renderStep(n, &stepRenderCtx{now: t0}, width, open)
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
	line := plainLines(renderCallRow(ev, false, nil, 80, ""))[0]
	if !strings.Contains(line, "⧉ #3 started") {
		t.Fatalf("dispatch row = %q, want the subagent it started", line)
	}
}
