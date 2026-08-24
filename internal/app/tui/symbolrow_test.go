package tui

import (
	"strings"
	"testing"

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
	for _, tool := range []string{"shell.run", "git.status", "test.run", "agent.run"} {
		if subjectFirstTool(tool) {
			t.Errorf("%s must keep the tool-name-first shape", tool)
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
	got := stripANSI(renderCompletedToolCall(event, false, 80))
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
	plain := stripANSI(renderCompletedToolCall(event, false, 80))
	if !strings.Contains(plain, "transcript.go › renderSubagentCard()") {
		t.Fatalf("expected subject-first row:\n%s", plain)
	}
	if strings.Contains(plain, "file.write_patch") {
		t.Fatalf("subject-first row should not lead with the tool name:\n%s", plain)
	}
}
