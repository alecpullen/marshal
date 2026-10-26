package acpwire

import (
	"encoding/json"
	"testing"
)

// intPtr is the *int a wire field takes when its pointer is what distinguishes
// "no exit code" from exit 0.
func intPtr(v int) *int { return &v }

// TestStackSnapshotJSON pins the snapshot contract field for field: the JSON a
// client receives. A field renamed, reordered or retagged here is a change to
// the wire, so the literal is compared byte for byte rather than by shape.
func TestStackSnapshotJSON(t *testing.T) {
	cases := []struct {
		name  string
		value StackSnapshot
		want  string
	}{
		{
			name: "every field set",
			value: StackSnapshot{
				SessionID:  "s1",
				SubagentID: 2,
				Rev:        3,
				Roots:      []string{"turn:1"},
				Nodes: []WireNode{
					{ID: "turn:1", Kind: KindTurn, Children: []string{"step:1", "msg:2"}},
					{
						ID: "step:1", Kind: KindStep, Parent: "turn:1", Children: []string{"tool:1:c1"}, Live: true,
						Step: &WireStep{
							Headline: "Reading the config.", Rest: "Then editing it.", StepIDs: []int64{1},
							Owner: "reviewer #1", RoleWord: "reviewer", Role: "reviewer",
							Model: "qwen3", Provider: "local", TodoID: "t2",
							Todo:      &WireTodoLabel{ID: "t2", Index: 2, Total: 4, Content: "Edit the config"},
							StartedAt: 1000, EndedAt: 2000,
							Thoughts: []WireThought{{Text: "weighing it", DurationMs: 2000}},
						},
					},
					{
						ID: "tool:1:c1", Kind: KindTool, Parent: "step:1",
						Tool: &WireTool{
							Name: "shell.run", Display: "Shell", WorkKey: "w1",
							Calls: []WireCall{{
								CallID: "c1", Target: "go test", Args: `{"command":"go test"}`,
								Summary: "exit 1", Output: "FAIL", Error: "boom", ExitCode: intPtr(1),
								Failed: true, Approval: "approved", Risk: "low", Files: []string{"a.go"},
								Role: "reviewer", Notice: "note", DurationMs: 3000, At: 4000, Truncated: true,
							}},
							Running:    &WireRunning{Target: "go vet", Output: "ok", StartedAt: 5000, Truncated: true},
							SubagentID: 3,
						},
					},
					{
						ID: "msg:2", Kind: KindMessage, Parent: "turn:1",
						Message: &WireMessage{
							Role: "assistant", ContentType: "markdown", Content: "Done.", Final: true,
							Usage: "12k tok", Salvaged: true, SalvageReason: "cut off", ThinkMs: 1500, At: 6000,
						},
					},
					{
						ID: "think:live", Kind: KindThinking,
						Thinking: &WireThinking{Text: "hmm", DurationMs: 700},
					},
					{
						ID: "run:7", Kind: KindRunEvent,
						RunEvent: &WireRunEvent{
							Kind: RunEventVerifyFailed, TaskN: 2, Title: "go test", Detail: "exit 1",
							Body: "FAIL", Severity: "high", At: 7000, Truncated: true,
						},
					},
					{
						ID: "done:job-1", Kind: KindCompletion,
						Completion: &WireCompletion{
							Source: CompletionJob, ID: "job-1", SubagentID: 3, Label: "go build",
							Outcome: "exit 1", Failed: true, ExitCode: intPtr(1), StopCause: "user",
							StartedAt: 8000, EndedAt: 9000, DurationMs: 1000, Measure: "3/4",
							Summary: "FAIL x", Command: "go build ./...", Cwd: "/w", Output: "out",
							Report: "rep", Error: "err", WorkKey: "w2", ExecutionID: "e1",
							ReportMessageID: 11, Truncated: true,
						},
					},
					{
						ID: "plan:tool:1:c2", Kind: KindPlanUpdate,
						Plan: &WirePlan{
							Status: []string{"completed", "in_progress"}, Done: 1, Total: 2, Active: 2,
							ActiveText: "Edit the config", Cleared: true,
						},
					},
					{
						ID: "receipt:1", Kind: KindReceipt,
						Receipt: &WireReceipt{
							DurationMs: 400000, Tasks: 4, Steps: 11, Tools: 19, Files: 3,
							Usage: "212k tok", Salvaged: true,
						},
					},
					{
						ID: BandID, Kind: KindBand,
						Band: &WireBand{
							Todos: true, Done: 1, Total: 4, Active: "Edit the config", Stale: true,
							StaleAgeMs: 60000, StaleSince: 10000, Next: []string{"Run the evals"},
							Tasks: []WireTask{{
								ID: "t2", Content: "Edit the config", Status: "in_progress",
								StartedAt: 1000, CompletedAt: 2000, Steps: 3, WorkMs: 4000, Ref: "step:1",
							}},
							Entities: []WireBandEntity{{
								Ref: "tool:1:c1", Label: "go test", Detail: "running", StartedAt: 5000,
							}},
						},
					},
				},
			},
			want: `{"sessionId":"s1","subagentId":2,"rev":3,"roots":["turn:1"],"nodes":[{"id":"turn:1","kind":"turn","children":["step:1","msg:2"]},{"id":"step:1","kind":"step","parent":"turn:1","children":["tool:1:c1"],"live":true,"step":{"headline":"Reading the config.","rest":"Then editing it.","stepIds":[1],"owner":"reviewer #1","roleWord":"reviewer","role":"reviewer","model":"qwen3","provider":"local","todoId":"t2","todo":{"id":"t2","index":2,"total":4,"content":"Edit the config"},"startedAt":1000,"endedAt":2000,"thoughts":[{"text":"weighing it","durationMs":2000}]}},{"id":"tool:1:c1","kind":"tool","parent":"step:1","tool":{"name":"shell.run","display":"Shell","workKey":"w1","calls":[{"callId":"c1","target":"go test","args":"{\"command\":\"go test\"}","summary":"exit 1","output":"FAIL","error":"boom","exitCode":1,"failed":true,"approval":"approved","risk":"low","files":["a.go"],"role":"reviewer","notice":"note","durationMs":3000,"at":4000,"truncated":true}],"running":{"target":"go vet","output":"ok","startedAt":5000,"truncated":true},"subagentId":3}},{"id":"msg:2","kind":"message","parent":"turn:1","message":{"role":"assistant","contentType":"markdown","content":"Done.","final":true,"usage":"12k tok","salvaged":true,"salvageReason":"cut off","thinkMs":1500,"at":6000}},{"id":"think:live","kind":"thinking","thinking":{"text":"hmm","durationMs":700}},{"id":"run:7","kind":"runEvent","runEvent":{"kind":"verifyFailed","taskN":2,"title":"go test","detail":"exit 1","body":"FAIL","severity":"high","at":7000,"truncated":true}},{"id":"done:job-1","kind":"completion","completion":{"source":"job","id":"job-1","subagentId":3,"label":"go build","outcome":"exit 1","failed":true,"exitCode":1,"stopCause":"user","startedAt":8000,"endedAt":9000,"durationMs":1000,"measure":"3/4","summary":"FAIL x","command":"go build ./...","cwd":"/w","output":"out","report":"rep","error":"err","workKey":"w2","executionId":"e1","reportMessageId":11,"truncated":true}},{"id":"plan:tool:1:c2","kind":"planUpdate","plan":{"status":["completed","in_progress"],"done":1,"total":2,"active":2,"activeText":"Edit the config","cleared":true}},{"id":"receipt:1","kind":"receipt","receipt":{"durationMs":400000,"tasks":4,"steps":11,"tools":19,"files":3,"usage":"212k tok","salvaged":true}},{"id":"band","kind":"band","band":{"todos":true,"done":1,"total":4,"active":"Edit the config","stale":true,"staleAgeMs":60000,"staleSince":10000,"next":["Run the evals"],"tasks":[{"id":"t2","content":"Edit the config","status":"in_progress","startedAt":1000,"completedAt":2000,"steps":3,"workMs":4000,"ref":"step:1"}],"entities":[{"ref":"tool:1:c1","label":"go test","detail":"running","startedAt":5000}]}}]}`,
		},
		{
			name:  "zero snapshot",
			value: StackSnapshot{},
			want:  `{"sessionId":"","rev":0,"roots":null,"nodes":null}`,
		},
	}
	for _, c := range cases {
		got, err := json.Marshal(c.value)
		if err != nil {
			t.Fatalf("%s: marshal: %v", c.name, err)
		}
		if string(got) != c.want {
			t.Errorf("%s JSON =\n%s\nwant\n%s", c.name, got, c.want)
		}
	}
}

// TestCollapsedRowFieldsJSON pins the fields a client renders a collapsed row
// from: the node's version, the verb it leads with, and the row's one-line
// facts. Each literal is the whole node, so a field moving or a tag changing
// is a wire change rather than a rendering detail.
func TestCollapsedRowFieldsJSON(t *testing.T) {
	cases := []struct {
		name  string
		value WireNode
		want  string
	}{
		{
			name: "step",
			value: WireNode{
				ID: "step:1", Kind: KindStep, Version: "9f3a",
				Step: &WireStep{Headline: "Reading the config.",
					Narration: []string{"Reading the config. Then editing it.", ""}},
			},
			want: `{"id":"step:1","kind":"step","version":"9f3a","step":{"headline":"Reading the config.","narration":["Reading the config. Then editing it.",""]}}`,
		},
		{
			name: "tool row",
			value: WireNode{
				ID: "tool:1:c1", Kind: KindTool, Version: "1c",
				Tool: &WireTool{
					Name: "shell.run", Display: "Shell", Verb: "shell",
					Calls: []WireCall{{
						Subject: "⌂ go test ./...", SummaryLine: "exit 1", DiffStat: "+1 −1",
						HookHint: "hook rewrote", KilledReason: "timeout", JobID: "job-1",
						Tail: []string{"--- FAIL: TestEval", "FAIL"},
					}},
					Running: &WireRunning{Subject: "evals/config.toml", CallID: "c2", Args: "go vet ./..."},
				},
			},
			want: `{"id":"tool:1:c1","kind":"tool","version":"1c","tool":{"name":"shell.run","display":"Shell","verb":"shell","calls":[{"subject":"⌂ go test ./...","summaryLine":"exit 1","diffStat":"+1 −1","hookHint":"hook rewrote","killedReason":"timeout","jobId":"job-1","tail":["--- FAIL: TestEval","FAIL"]}],"running":{"subject":"evals/config.toml","callId":"c2","args":"go vet ./..."}}}`,
		},
		{
			name: "message",
			value: WireNode{
				ID: "msg:2", Kind: KindMessage,
				Message: &WireMessage{Role: "assistant", Content: "Done.", Reasoning: "weighing two configs"},
			},
			want: `{"id":"msg:2","kind":"message","message":{"role":"assistant","content":"Done.","reasoning":"weighing two configs"}}`,
		},
		{
			name: "live reasoning",
			value: WireNode{
				ID: LiveThinkingID, Kind: KindThinking,
				Thinking: &WireThinking{Text: "hmm", StartedAt: 1000},
			},
			want: `{"id":"think:live","kind":"thinking","thinking":{"text":"hmm","startedAt":1000}}`,
		},
		{
			name: "run event",
			value: WireNode{
				ID: "run:7", Kind: KindRunEvent,
				RunEvent: &WireRunEvent{Kind: RunEventVerifyFailed, Body: "a\nb", BodyLines: 14},
			},
			want: `{"id":"run:7","kind":"runEvent","runEvent":{"kind":"verifyFailed","body":"a\nb","bodyLines":14}}`,
		},
	}
	for _, c := range cases {
		got, err := json.Marshal(c.value)
		if err != nil {
			t.Fatalf("%s: marshal: %v", c.name, err)
		}
		if string(got) != c.want {
			t.Errorf("%s JSON =\n%s\nwant\n%s", c.name, got, c.want)
		}
	}
}
