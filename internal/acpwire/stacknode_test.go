package acpwire

import (
	"encoding/json"
	"testing"
)

// The two rows below are the detail transport's whole answer shape: every field
// set, and the zero value a session with nothing to say sends. The literals
// were produced by encoding these declared types, so a mismatch means the
// construction is wrong, not the literal.
func TestStackNodeResultJSON(t *testing.T) {
	code := 1
	full := StackNodeResult{
		Detail: NodeDetail{
			Calls: []CallDetail{{
				CallID:       "c1",
				StepID:       3,
				ToolName:     "shell.run",
				Args:         "{\n  \"command\": \"go test\"\n}",
				OriginalArgs: "{\n  \"command\": \"go vet\"\n}",
				Output:       "FAIL",
				Diff:         "--- a/x\n+++ b/x\n",
				Error:        "boom",
				ExitCode:     &code,
				Model:        "qwen3",
				FinishReason: "tool_calls",
				Rewritten:    true,
				Hooks:        json.RawMessage(`[{"event":"PreToolUse","decision":"allow","failed_open":true}]`),
				Sandbox:      json.RawMessage(`{"Enabled":true,"Backend":"restricted","KilledReason":"timeout"}`),
				Symbols:      []SymbolDetail{{File: "a.go", Name: "Run", Kind: "function"}},
				Notice:       &NoticeDetail{Kind: "hint", Text: "use test.run"},
				Execution: &ExecutionDetail{
					Version: 1, ExecutionID: "e1", DisplayID: "job-1", ConversationID: "conv",
					Actor: "parent", TurnID: "t1", StepID: "3", ToolCallID: "c1", Cwd: "/w",
					Generation: 2, PID: 42, State: "failed", Outcome: "nonzero_exit",
					Attachment: "background", Wait: "30s", DetachReason: "wait expired",
					DetachedAt: 1000, StopCause: "user", StopRequestedAt: 2000,
					Exit: &code, Truncated: true, StartedAt: 500, EndedAt: 3000,
				},
			}},
			Narration: []string{"Running the tests."},
			Thinking:  []ThoughtDetail{{Text: "hmm", DurationMs: 700}},
			Todo:      &TodoDetail{ID: "t2", Content: "Run the tests", Status: "in_progress", StartedAt: 100, CompletedAt: 200},
			Relations: &RelationDetail{CausedBy: []string{"step:1"}, FixedBy: []string{"step:4"}},
		},
		Node: WireNode{ID: "tool:3:c1", Kind: KindTool, Parent: "step:3"},
	}

	cases := []struct {
		name string
		got  StackNodeResult
		want string
	}{
		{
			name: "every field set",
			got:  full,
			want: `{"detail":{"calls":[{"callId":"c1","stepId":3,"toolName":"shell.run","args":"{\n  \"command\": \"go test\"\n}","originalArgs":"{\n  \"command\": \"go vet\"\n}","output":"FAIL","diff":"--- a/x\n+++ b/x\n","error":"boom","exitCode":1,"model":"qwen3","finishReason":"tool_calls","rewritten":true,"hooks":[{"event":"PreToolUse","decision":"allow","failed_open":true}],"sandbox":{"Enabled":true,"Backend":"restricted","KilledReason":"timeout"},"symbols":[{"file":"a.go","name":"Run","kind":"function"}],"notice":{"kind":"hint","text":"use test.run"},"execution":{"version":1,"executionId":"e1","displayId":"job-1","conversationId":"conv","actor":"parent","turnId":"t1","stepId":"3","toolCallId":"c1","cwd":"/w","generation":2,"pid":42,"state":"failed","outcome":"nonzero_exit","attachment":"background","wait":"30s","detachReason":"wait expired","detachedAt":1000,"stopCause":"user","stopRequestedAt":2000,"exit":1,"truncated":true,"startedAt":500,"endedAt":3000}}],"narration":["Running the tests."],"thinking":[{"text":"hmm","durationMs":700}],"todo":{"id":"t2","content":"Run the tests","status":"in_progress","startedAt":100,"completedAt":200},"relations":{"causedBy":["step:1"],"fixedBy":["step:4"]}},"node":{"id":"tool:3:c1","kind":"tool","parent":"step:3"}}`,
		},
		{
			name: "zero",
			got:  StackNodeResult{},
			want: `{"detail":{},"node":{"id":"","kind":""}}`,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			raw, err := json.Marshal(c.got)
			if err != nil {
				t.Fatal(err)
			}
			if string(raw) != c.want {
				t.Fatalf("JSON = %s\nwant    %s", raw, c.want)
			}
		})
	}
}

// The raw hook and sandbox JSON a CallDetail carries is what a client decodes:
// the hook trail keeps registry.HookMetadata's own spelling, and the sandbox
// record spells its keys in Go's field spelling, which encoding/json matches
// case-insensitively.
func TestHookAndSandboxDetailsDecode(t *testing.T) {
	var hooks []HookDetail
	if err := json.Unmarshal([]byte(`[{"event":"PreToolUse","decision":"block","failed_open":true}]`), &hooks); err != nil {
		t.Fatal(err)
	}
	if len(hooks) != 1 || hooks[0].Event != "PreToolUse" || hooks[0].Decision != "block" {
		t.Fatalf("hooks = %+v", hooks)
	}

	var sandbox SandboxDetail
	if err := json.Unmarshal([]byte(`{"Enabled":true,"Backend":"restricted","NetworkIsolated":true,"KilledReason":"timeout"}`), &sandbox); err != nil {
		t.Fatal(err)
	}
	want := SandboxDetail{Enabled: true, Backend: "restricted", NetworkIsolated: true, KilledReason: "timeout"}
	if sandbox != want {
		t.Fatalf("sandbox = %+v, want %+v", sandbox, want)
	}
}

// TestDetailAdditionsJSON pins the fields plan 3b adds to the detail transport
// and to a call row's wire form. As with TestStackNodeResultJSON, the literals
// were produced by encoding the declared types.
func TestDetailAdditionsJSON(t *testing.T) {
	cases := []struct {
		name string
		got  any
		want string
	}{
		{
			name: "the new detail fields",
			got: NodeDetail{
				Calls: []CallDetail{{ToolName: "file.edit", Summary: "edit failed"}},
				Step: &StepDetail{
					ID: 3, Owner: "reviewer #1", Model: "qwen3", Provider: "local",
					Why: "Running the tests.", TodoID: "t2",
				},
				Completion: &CompletionDetail{Output: "ok\n", Report: "## Review"},
			},
			want: `{"calls":[{"toolName":"file.edit","summary":"edit failed"}],"step":{"id":3,"owner":"reviewer #1","model":"qwen3","provider":"local","why":"Running the tests.","todoId":"t2"},"completion":{"output":"ok\n","report":"## Review"}}`,
		},
		{
			name: "a row a callers lookup asks about",
			got: WireNode{
				ID: "tool:3:c1", Kind: "tool",
				Tool: &WireTool{Name: "file.edit", Display: "Edit", Verb: "edit", Callers: true},
			},
			want: `{"id":"tool:3:c1","kind":"tool","tool":{"name":"file.edit","display":"Edit","verb":"edit","callers":true}}`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			raw, err := json.Marshal(c.got)
			if err != nil {
				t.Fatal(err)
			}
			if string(raw) != c.want {
				t.Fatalf("JSON = %s\nwant    %s", raw, c.want)
			}
		})
	}
}
