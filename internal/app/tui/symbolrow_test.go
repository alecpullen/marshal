package tui

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/tui/glyph"
	"marshal/internal/tools/registry"
)

func ev(tool string, syms ...registry.SymbolRef) registry.AuditEvent {
	return registry.AuditEvent{ToolName: tool, Symbols: syms}
}

func TestSymbolSubjectSingleFunction(t *testing.T) {
	got := symbolSubject(ev("file.write_patch",
		registry.SymbolRef{File: "transcript.go", Name: "renderSubagentCard", Kind: "function"}))
	if got != "transcript.go › renderSubagentCard()" {
		t.Fatalf("got %q", got)
	}
}

func TestSymbolSubjectMethodAndType(t *testing.T) {
	got := symbolSubject(ev("file.write_patch",
		registry.SymbolRef{File: "a.go", Name: "Beta", Kind: "method", Receiver: "*Scanner"},
		registry.SymbolRef{File: "a.go", Name: "Config", Kind: "type"}))
	if got != "a.go › Beta(), Config" {
		t.Fatalf("got %q", got)
	}
}

func TestSymbolSubjectOverflows(t *testing.T) {
	got := symbolSubject(ev("file.write_patch",
		registry.SymbolRef{File: "a.go", Name: "A", Kind: "function"},
		registry.SymbolRef{File: "a.go", Name: "B", Kind: "function"},
		registry.SymbolRef{File: "a.go", Name: "C", Kind: "function"},
		registry.SymbolRef{File: "a.go", Name: "D", Kind: "function"}))
	if got != "a.go › A(), B() +2" {
		t.Fatalf("got %q", got)
	}
}

func TestSymbolSubjectGroupsByFile(t *testing.T) {
	got := symbolSubject(ev("file.write_patch",
		registry.SymbolRef{File: "a.go", Name: "A", Kind: "function"},
		registry.SymbolRef{File: "b.go", Name: "B", Kind: "function"}))
	if !strings.Contains(got, "a.go › A()") || !strings.Contains(got, "b.go › B()") {
		t.Fatalf("got %q", got)
	}
}

func TestSymbolSubjectEmptyWithoutSymbols(t *testing.T) {
	if got := symbolSubject(ev("file.write_patch")); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

func TestSubjectFirstOnlyForSubjectTools(t *testing.T) {
	for _, tool := range []string{"file.write_patch", "file.write", "file.read", "symbols.find"} {
		if !subjectFirstTool(tool) {
			t.Errorf("%s should render subject-first", tool)
		}
	}
	for _, tool := range []string{"git.status", "agent.run"} {
		if subjectFirstTool(tool) {
			t.Errorf("%s must keep the tool-name-first shape", tool)
		}
	}
}

func shellEvent(cmd string, exit int, d time.Duration) registry.AuditEvent {
	return registry.AuditEvent{
		ToolName:        "shell.run",
		Args:            json.RawMessage(`{"command":` + strconv.Quote(cmd) + `}`),
		ResultSummary:   fmt.Sprintf("command %q exited with code %d", cmd, exit),
		CommandExitCode: &exit,
		Duration:        d,
	}
}

func TestShellSubjectLeadsWithCommand(t *testing.T) {
	got := shellSubject(shellEvent("go test ./...", 0, 12400*time.Millisecond))
	if !strings.HasPrefix(got, "go test ./...") {
		t.Fatalf("shell row must lead with the command, got %q", got)
	}
	if !strings.Contains(got, "exit 0") {
		t.Fatalf("missing exit code: %q", got)
	}
	if !strings.Contains(got, "12s") {
		t.Fatalf("missing duration: %q", got)
	}
}

func TestShellRowDoesNotRepeatTheCommand(t *testing.T) {
	out := stripANSI(renderCompletedToolCall(shellEvent("go test ./...", 0, time.Second), false, nil, 100))
	if n := strings.Count(out, "go test ./..."); n != 1 {
		t.Fatalf("command appears %d times, want 1:\n%s", n, out)
	}
	if strings.Contains(out, "exited with code") {
		t.Fatalf("shell row must not append ResultSummary:\n%s", out)
	}
	if strings.Contains(out, "shell.run") || strings.Contains(out, "Run command") {
		t.Fatalf("shell row must not lead with the tool name:\n%s", out)
	}
}

func TestShellRowNilExitCodeRendersNoExitSegment(t *testing.T) {
	e := shellEvent("go test ./...", 0, time.Second)
	e.CommandExitCode = nil
	if got := shellSubject(e); strings.Contains(got, "exit") {
		t.Fatalf("nil exit code must render no exit segment, got %q", got)
	}
}

func TestShellRowZeroDurationOmitsDuration(t *testing.T) {
	e := shellEvent("ls", 0, 0)
	got := shellSubject(e)
	if strings.Contains(got, "0s") || strings.Contains(got, "0ms") {
		t.Fatalf("zero duration must be omitted, got %q", got)
	}
	if !strings.Contains(got, "exit 0") {
		t.Fatalf("exit code should still render: %q", got)
	}
}

func TestFailedShellRowKeepsErrorTreatment(t *testing.T) {
	e := shellEvent("make", 2, time.Second)
	e.Error = "exit status 2"
	out := stripANSI(renderCompletedToolCall(e, false, nil, 100))
	if !strings.Contains(out, glyph.Error) {
		t.Fatalf("failed shell row must keep the error glyph:\n%s", out)
	}
}

func TestTestRunIsShellFamily(t *testing.T) {
	if !isShellFamily("test.run") || !isShellFamily("shell.run") {
		t.Fatal("shell.run and test.run are the shell family")
	}
	for _, n := range []string{"file.read", "repo.search", "git.status", "agent.run"} {
		if isShellFamily(n) {
			t.Errorf("%s is not shell family", n)
		}
	}
}

func searchEvent(tool, query, summary string) registry.AuditEvent {
	return registry.AuditEvent{
		ToolName:      tool,
		Args:          json.RawMessage(`{"query":` + strconv.Quote(query) + `}`),
		ResultSummary: summary,
	}
}

func TestSearchSubjectLeadsWithQuery(t *testing.T) {
	got := searchSubject(searchEvent("repo.search", "gutterPrefix", "4 matches"))
	if !strings.Contains(got, "gutterPrefix") {
		t.Fatalf("search row must carry the query: %q", got)
	}
	if !strings.HasPrefix(got, "search ") {
		t.Fatalf("search row must keep a short qualifier: %q", got)
	}
	if strings.Contains(got, "Search repo") {
		t.Fatalf("the verbose display name should be replaced: %q", got)
	}
}

func TestEverySearchToolHasADistinctQualifier(t *testing.T) {
	tools := []string{"repo.search", "codebase.search", "symbols.find", "json.query", "csv.inspect"}
	seen := map[string]string{}
	for _, tool := range tools {
		q, ok := searchQualifier(tool)
		if !ok || q == "" {
			t.Fatalf("%s has no qualifier", tool)
		}
		if prev, dup := seen[q]; dup {
			t.Fatalf("%s and %s share the qualifier %q", prev, tool, q)
		}
		seen[q] = tool
	}
}

func TestNonSearchToolHasNoQualifier(t *testing.T) {
	for _, n := range []string{"shell.run", "file.read", "git.status"} {
		if _, ok := searchQualifier(n); ok {
			t.Errorf("%s must not have a search qualifier", n)
		}
	}
}

func TestSearchRowKeepsItsSummary(t *testing.T) {
	out := stripANSI(renderCompletedToolCall(searchEvent("repo.search", "gutterPrefix", "4 matches"), false, nil, 100))
	if !strings.Contains(out, "4 matches") {
		t.Fatalf("search row must keep its outcome summary:\n%s", out)
	}
	if !strings.Contains(out, "gutterPrefix") {
		t.Fatalf("search row must carry the query:\n%s", out)
	}
}

func TestSymbolsFindPrefersSymbolSubject(t *testing.T) {
	e := searchEvent("symbols.find", "renderSubagentCard", "3 found")
	e.Symbols = []registry.SymbolRef{{File: "transcript.go", Name: "renderSubagentCard", Kind: "function"}}
	out := stripANSI(renderCompletedToolCall(e, false, nil, 100))
	if !strings.Contains(out, "transcript.go › renderSubagentCard()") {
		t.Fatalf("symbol attribution must win over the search qualifier:\n%s", out)
	}
}

func TestSearchRowWithNoQueryFallsBack(t *testing.T) {
	e := registry.AuditEvent{ToolName: "repo.search", ResultSummary: "no matches"}
	out := stripANSI(renderCompletedToolCall(e, false, nil, 100))
	if !strings.Contains(out, "no matches") {
		t.Fatalf("a query-less search row must still render its summary:\n%s", out)
	}
}

func TestJsonQuerySubjectPrefersQueryOverPath(t *testing.T) {
	e := registry.AuditEvent{
		ToolName:      "json.query",
		Args:          json.RawMessage(`{"path":"config.toml","query":".dependencies | keys"}`),
		ResultSummary: "3 results",
	}
	got := searchSubject(e)
	if !strings.Contains(got, ".dependencies | keys") {
		t.Fatalf("json.query row must show the jq expression, got %q", got)
	}
	if strings.Contains(got, "config.toml") {
		t.Fatalf("json.query row must not lead with the path, got %q", got)
	}
}

func TestJsonQueryRowRendersQueryAndSummary(t *testing.T) {
	e := registry.AuditEvent{
		ToolName:      "json.query",
		Args:          json.RawMessage(`{"path":"config.toml","query":".deps | keys"}`),
		ResultSummary: "3 results",
	}
	out := stripANSI(renderCompletedToolCall(e, false, nil, 100))
	if !strings.Contains(out, "json") {
		t.Fatalf("json.query row must carry the qualifier:\n%s", out)
	}
	if !strings.Contains(out, ".deps | keys") {
		t.Fatalf("json.query row must carry the jq expression:\n%s", out)
	}
	if !strings.Contains(out, "3 results") {
		t.Fatalf("json.query row must keep its summary:\n%s", out)
	}
}

func TestBackgroundShellRowKeepsSummary(t *testing.T) {
	e := registry.AuditEvent{
		ToolName:      "shell.run",
		Args:          json.RawMessage(`{"command":"go build ./...","background":true}`),
		ResultSummary: "started background job job_1",
	}
	out := stripANSI(renderCompletedToolCall(e, false, nil, 100))
	if !strings.Contains(out, "go build ./...") {
		t.Fatalf("background shell row must carry the command:\n%s", out)
	}
	if !strings.Contains(out, "started background job") {
		t.Fatalf("background shell row must keep its summary:\n%s", out)
	}
}

func TestKilledShellRowKeepsKillReason(t *testing.T) {
	exit := -1
	e := registry.AuditEvent{
		ToolName:        "shell.run",
		Args:            json.RawMessage(`{"command":"sleep 100"}`),
		ResultSummary:   `command "sleep 100" killed: timeout`,
		CommandExitCode: &exit,
		Duration:        30 * time.Second,
		Sandbox:         registry.SandboxMeta{Enabled: true, KilledReason: "timeout"},
	}
	out := stripANSI(renderCompletedToolCall(e, false, nil, 100))
	if !strings.Contains(out, "sleep 100") {
		t.Fatalf("killed shell row must carry the command:\n%s", out)
	}
	if !strings.Contains(out, "killed: timeout") {
		t.Fatalf("killed shell row must keep the kill reason:\n%s", out)
	}
}

func TestNonMigratedToolsUnchanged(t *testing.T) {
	for _, tool := range []string{"git.status", "agent.run", "web.fetch", "todos"} {
		e := registry.AuditEvent{ToolName: tool, ResultSummary: "did a thing"}
		out := stripANSI(renderCompletedToolCall(e, false, nil, 100))
		if !strings.Contains(out, DisplayToolName(tool)) {
			t.Errorf("%s must keep the tool-name-first shape:\n%s", tool, out)
		}
	}
}

func TestDiffStat(t *testing.T) {
	diff := "--- a/x\n+++ b/x\n@@ -1,2 +1,3 @@\n-old\n+new\n+another\n context\n"
	if got := diffStat(diff); got != "+2 −1" {
		t.Fatalf("got %q, want %q", got, "+2 −1")
	}
	if got := diffStat(""); got != "" {
		t.Fatalf("empty diff got %q, want empty", got)
	}
}

// THE regression test. On most repositories — every language outside the
// five with grammars, and every whole-file rewrite — there are no symbols,
// so the no-symbol path is the common path and must not change at all.
func TestRowWithoutSymbolsIsUnchanged(t *testing.T) {
	event := registry.AuditEvent{
		ToolName:      "file.write_patch",
		ResultSummary: "Applied patches to: a.rb",
		FilesChanged:  []string{"a.rb"},
	}
	got := stripANSI(renderCompletedToolCall(event, false, nil, 80))
	if !strings.Contains(got, "Edit file") {
		t.Fatalf("no-symbol row must keep the tool-name-first shape:\n%s", got)
	}
	if strings.Contains(got, "›") {
		t.Fatalf("no-symbol row must not render a symbol subject:\n%s", got)
	}
}

func TestRowWithSymbolsIsSubjectFirst(t *testing.T) {
	event := registry.AuditEvent{
		ToolName:      "file.write_patch",
		ResultSummary: "Applied patches to: transcript.go",
		FilesChanged:  []string{"transcript.go"},
		Symbols: []registry.SymbolRef{
			{File: "transcript.go", Name: "renderSubagentCard", Kind: "function"},
		},
	}
	plain := stripANSI(renderCompletedToolCall(event, false, nil, 80))
	if !strings.Contains(plain, "transcript.go › renderSubagentCard()") {
		t.Fatalf("expected subject-first row:\n%s", plain)
	}
	if strings.Contains(plain, "file.write_patch") {
		t.Fatalf("subject-first row should not lead with the tool name:\n%s", plain)
	}
}
