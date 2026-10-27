package viewmodel

import (
	"encoding/json"
	"testing"
	"time"

	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
)

func TestCallVerb(t *testing.T) {
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
		if got := CallVerb(name); got != want {
			t.Errorf("CallVerb(%q) = %q, want %q", name, got, want)
		}
	}
}

// A call whose arguments name nothing falls back to the rest of its tool
// name, but never just repeats the verb.
func TestFallbackSubjectNeverRepeatsTheVerb(t *testing.T) {
	cases := map[string]string{"git.status": "status", "agent.await": "await", "file.read": "", "file.write": "", "todos": ""}
	for name, want := range cases {
		if got := FallbackSubject(name); got != want {
			t.Errorf("FallbackSubject(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestCallSubject(t *testing.T) {
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
			if got := CallSubject(c.ev, "/w"); got != c.want {
				t.Fatalf("CallSubject = %q, want %q", got, c.want)
			}
		})
	}
}

// A running call names its path, else the first line of its arguments, else
// the rest of its tool name, and never carries a control sequence.
func TestRunningSubject(t *testing.T) {
	cases := []struct {
		name string
		atc  session.ActiveToolCall
		want string
	}{
		{"a path wins over arguments", session.ActiveToolCall{Name: "file.edit", Path: "evals/thresholds.toml", Args: "ignored"}, "evals/thresholds.toml"},
		{"the first line of the arguments", session.ActiveToolCall{Name: "shell.run", Args: "go test ./...\necho done"}, "go test ./..."},
		{"the rest of the tool name", session.ActiveToolCall{Name: "git.status"}, "status"},
		{"an escape sequence is scrubbed", session.ActiveToolCall{Name: "file.read", Path: "a\x1b[31mb.go"}, "a[31mb.go"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := RunningSubject(c.atc); got != c.want {
				t.Fatalf("RunningSubject = %q, want %q", got, c.want)
			}
		})
	}
}

// A row shows the summary's first line, scrubbed, and an await reports its
// wait rather than the completion event's text.
func TestSummaryLineIsTheFirstLineScrubbed(t *testing.T) {
	cases := []struct {
		name string
		ev   registry.AuditEvent
		want string
	}{
		{"the first line", registry.AuditEvent{ToolName: "repo.search", ResultSummary: "4 matches\nmore"}, "4 matches"},
		{"an await reports its wait", registry.AuditEvent{ToolName: "agent.await", Duration: 5 * time.Second, ResultSummary: "ignored"}, "waited 5s"},
		{"an escape byte is dropped", registry.AuditEvent{ToolName: "repo.search", ResultSummary: "4\x1b matches"}, "4 matches"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := SummaryLine(c.ev); got != c.want {
				t.Fatalf("SummaryLine = %q, want %q", got, c.want)
			}
		})
	}
}

// Only an edit or a whole-file write shows a diff stat, and only when its
// result is a diff.
func TestCallDiffStatIsForEditsAndWrites(t *testing.T) {
	diff := "--- a/x.go\n+++ b/x.go\n@@ -1 +1,2 @@\n-a\n+b\n+c\n"
	for _, tool := range []string{"file.edit", "file.write_patch", "patch.apply", "file.write"} {
		ev := registry.AuditEvent{ToolName: tool, ResultContent: diff}
		if got := CallDiffStat(ev); got != "+2 −1" {
			t.Errorf("CallDiffStat(%s) = %q, want %q", tool, got, "+2 −1")
		}
	}
	if got := CallDiffStat(registry.AuditEvent{ToolName: "shell.run", ResultContent: diff}); got != "" {
		t.Errorf("CallDiffStat(shell.run) = %q, want %q", got, "")
	}
	if got := CallDiffStat(registry.AuditEvent{ToolName: "file.write", ResultContent: "new contents"}); got != "" {
		t.Errorf("CallDiffStat(file.write, not a diff) = %q, want %q", got, "")
	}
}

// A failed row shows the last three lines of its output, or of its error when
// it printed nothing, with control characters removed.
func TestFailedTailIsTheLastLines(t *testing.T) {
	ev := registry.AuditEvent{ResultContent: "line 1\nline 2\nline 3\nline 4\nline 5"}
	got := FailedTail(ev)
	want := []string{"line 3", "line 4", "line 5"}
	if len(got) != len(want) {
		t.Fatalf("FailedTail = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("FailedTail = %q, want %q", got, want)
		}
	}

	if got := FailedTail(registry.AuditEvent{Error: "boom\nbang"}); len(got) != 2 || got[0] != "boom" || got[1] != "bang" {
		t.Fatalf("FailedTail(error) = %q, want [boom bang]", got)
	}
	if got := FailedTail(registry.AuditEvent{}); got != nil {
		t.Fatalf("FailedTail(empty) = %q, want nil", got)
	}
	if got := FailedTail(registry.AuditEvent{ResultContent: "a\r\nb\x1b"}); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("FailedTail(control chars) = %q, want [a b]", got)
	}
}

// A hook's decision leads: block beats a rewrite, a rewrite beats a
// failed-open, and a plain hook is only counted.
func TestHookHintPrefersBlockThenRewriteThenFailOpen(t *testing.T) {
	cases := []struct {
		name  string
		hooks []registry.HookMetadata
		want  string
	}{
		{"block beats a rewrite", []registry.HookMetadata{{Decision: "block"}, {Rewrote: true}}, "hook blocked"},
		{"halt blocks too", []registry.HookMetadata{{Decision: "halt"}}, "hook blocked"},
		{"a rewrite beats a failed-open", []registry.HookMetadata{{Rewrote: true}, {FailedOpen: true}}, "hook rewrote"},
		{"a failed-open", []registry.HookMetadata{{FailedOpen: true}}, "hook failed-open"},
		{"a plain hook is counted", []registry.HookMetadata{{}, {}}, "hooks 2"},
		{"none", nil, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := HookHint(c.hooks); got != c.want {
				t.Fatalf("HookHint = %q, want %q", got, c.want)
			}
		})
	}
}
