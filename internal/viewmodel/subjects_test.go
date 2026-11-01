package viewmodel

import (
	"encoding/json"
	"strings"
	"testing"

	"marshal/internal/tools/registry"
)

// symbolEvent is a settled call carrying symbol attribution.
func symbolEvent(tool string, syms ...registry.SymbolRef) registry.AuditEvent {
	return registry.AuditEvent{ToolName: tool, Symbols: syms}
}

func TestSymbolSubjectSingleFunction(t *testing.T) {
	got := SymbolSubject(symbolEvent("file.write_patch",
		registry.SymbolRef{File: "transcript.go", Name: "renderSubagentCard", Kind: "function"}))
	if got != "transcript.go › renderSubagentCard()" {
		t.Fatalf("got %q", got)
	}
}

func TestSymbolSubjectMethodAndType(t *testing.T) {
	got := SymbolSubject(symbolEvent("file.write_patch",
		registry.SymbolRef{File: "a.go", Name: "Beta", Kind: "method", Receiver: "*Scanner"},
		registry.SymbolRef{File: "a.go", Name: "Config", Kind: "type"}))
	if got != "a.go › Beta(), Config" {
		t.Fatalf("got %q", got)
	}
}

func TestSymbolSubjectOverflows(t *testing.T) {
	got := SymbolSubject(symbolEvent("file.write_patch",
		registry.SymbolRef{File: "a.go", Name: "A", Kind: "function"},
		registry.SymbolRef{File: "a.go", Name: "B", Kind: "function"},
		registry.SymbolRef{File: "a.go", Name: "C", Kind: "function"},
		registry.SymbolRef{File: "a.go", Name: "D", Kind: "function"}))
	if got != "a.go › A(), B() +2" {
		t.Fatalf("got %q", got)
	}
}

func TestSymbolSubjectGroupsByFile(t *testing.T) {
	got := SymbolSubject(symbolEvent("file.write_patch",
		registry.SymbolRef{File: "a.go", Name: "A", Kind: "function"},
		registry.SymbolRef{File: "b.go", Name: "B", Kind: "function"}))
	if !strings.Contains(got, "a.go › A()") || !strings.Contains(got, "b.go › B()") {
		t.Fatalf("got %q", got)
	}
}

func TestSymbolSubjectEmptyWithoutSymbols(t *testing.T) {
	if got := SymbolSubject(symbolEvent("file.write_patch")); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

func TestSubjectFirstOnlyForSubjectTools(t *testing.T) {
	for _, tool := range []string{"file.write_patch", "file.write", "file.read", "symbols", "symbols.find"} {
		if !SubjectFirstTool(tool) {
			t.Errorf("%s should render subject-first", tool)
		}
	}
	for _, tool := range []string{"git.status", "agent.run"} {
		if SubjectFirstTool(tool) {
			t.Errorf("%s must keep the tool-name-first shape", tool)
		}
	}
}

func TestTestRunIsShellFamily(t *testing.T) {
	if !IsShellFamily("test.run") || !IsShellFamily("shell.run") {
		t.Fatal("shell.run and test.run are the shell family")
	}
	for _, n := range []string{"file.read", "repo.search", "git.status", "agent.run"} {
		if IsShellFamily(n) {
			t.Errorf("%s is not shell family", n)
		}
	}
}

func TestEverySearchToolHasADistinctQualifier(t *testing.T) {
	// The live search family, plus the retired names that stay in the map so
	// persisted transcripts still render a qualifier. symbols.find is absent
	// deliberately: it is the same tool as symbols under its old name, so a
	// shared qualifier is the intent, not a collision.
	tools := []string{"repo.search", "codebase.search", "symbols", "json.query", "csv.inspect"}
	seen := map[string]string{}
	for _, tool := range tools {
		q, ok := SearchQualifier(tool)
		if !ok || q == "" {
			t.Fatalf("%s has no qualifier", tool)
		}
		if prev, dup := seen[q]; dup {
			t.Fatalf("%s and %s share the qualifier %q", prev, tool, q)
		}
		seen[q] = tool
	}
	if q, ok := SearchQualifier("symbols.find"); !ok || q != "symbols" {
		t.Fatalf("retired symbols.find must still render with the symbols qualifier, got %q ok=%v", q, ok)
	}
}

// TestSearchTargetOfASymbolLookupIsTheName: a symbol lookup's subject is the
// name, so a row that also carries a path must not lead with the path.
func TestSearchTargetOfASymbolLookupIsTheName(t *testing.T) {
	e := registry.AuditEvent{
		ToolName:      "symbols",
		Args:          json.RawMessage(`{"name":"Parse","op":"definition","path":"a.go"}`),
		ResultSummary: "a.go:2",
	}
	if got := SearchTarget(e); got != "Parse" {
		t.Fatalf("symbols target = %q, want the name", got)
	}
}

func TestNonSearchToolHasNoQualifier(t *testing.T) {
	for _, n := range []string{"shell.run", "file.read", "git.status"} {
		if _, ok := SearchQualifier(n); ok {
			t.Errorf("%s must not have a search qualifier", n)
		}
	}
}

// TestSearchTargetOfAJSONQueryIsTheExpression: json.query is retired (its
// entry survives for persisted transcripts), and its primary subject was the
// jq expression, not the file path.
func TestSearchTargetOfAJSONQueryIsTheExpression(t *testing.T) {
	e := registry.AuditEvent{
		ToolName:      "json.query",
		Args:          json.RawMessage(`{"path":"config.toml","query":".dependencies | keys"}`),
		ResultSummary: "3 results",
	}
	if got := SearchTarget(e); got != ".dependencies | keys" {
		t.Fatalf("json.query target = %q, want the jq expression", got)
	}
}

func TestDiffStat(t *testing.T) {
	diff := "--- a/x\n+++ b/x\n@@ -1,2 +1,3 @@\n-old\n+new\n+another\n context\n"
	if got := DiffStat(diff); got != "+2 −1" {
		t.Fatalf("got %q, want %q", got, "+2 −1")
	}
	if got := DiffStat(""); got != "" {
		t.Fatalf("empty diff got %q, want empty", got)
	}
}

func TestToolTarget(t *testing.T) {
	cases := []struct {
		name  string
		event registry.AuditEvent
		want  string
	}{
		{
			name:  "file.read returns the path",
			event: registry.AuditEvent{ToolName: "file.read", Args: json.RawMessage(`{"path":"budget.go"}`)},
			want:  "budget.go",
		},
		{
			name:  "shell.run returns the command",
			event: registry.AuditEvent{ToolName: "shell.run", Args: json.RawMessage(`{"command":"go test ./..."}`)},
			want:  "go test ./...",
		},
		{
			name:  "search tool returns the query",
			event: registry.AuditEvent{ToolName: "repo.search", Args: json.RawMessage(`{"query":"auth flow"}`)},
			want:  "auth flow",
		},
		{
			name: "FilesChanged wins over args",
			event: registry.AuditEvent{
				ToolName:     "file.write_patch",
				FilesChanged: []string{"a.go"},
				Args:         json.RawMessage(`{"path":"b.go"}`),
			},
			want: "a.go",
		},
		{
			name:  "no recognizable subject returns empty",
			event: registry.AuditEvent{ToolName: "todos", Args: json.RawMessage(`{"items":[]}`)},
			want:  "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ToolTarget(tc.event); got != tc.want {
				t.Errorf("ToolTarget() = %q, want %q", got, tc.want)
			}
		})
	}
}
