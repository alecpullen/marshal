package acpwire

import (
	"encoding/json"
	"testing"
)

// TestWorkJSON pins the bytes the work projection, its queries and its stop
// answers travel as: every field set on a snapshot's one item must encode
// exactly as the contract spells it.
func TestWorkJSON(t *testing.T) {
	exitCode := 1
	total := 10.0
	rate := 2.5
	full := WorkSnapshot{
		Version:         1,
		ConversationID:  "s1",
		RuntimeEpoch:    "e1",
		Revision:        7,
		HistoryRevision: 3,
		ObservedAt:      10000000,
		Items: []WorkItem{{
			TaskKey:        "work:1",
			DisplayID:      "job-1",
			ConversationID: "s1",
			RuntimeEpoch:   "e1",
			SourceID:       "job-1",
			SourceRevision: 4,
			Kind:           "command",
			Label:          "go test ./...",
			Owner:          "implementer",
			Origin:         WorkOrigin{TurnID: "t1", StepID: "2", ToolCallID: "call-1"},
			State:          "failed",
			Outcome:        "exit",
			ExitCode:       &exitCode,
			Attachment:     "background",
			StartedAt:      9940000,
			EndedAt:        9990000,
			ResultRevision: "r1",
			Activity:       "running tests",
			OutputTail:     "FAIL",
			Warning:        "slow",
			Attention:      "exit 1",
			Reference:      WorkReference{MessageID: 12, NodeKey: "n1", ChildSessionID: "c1"},
			Prompt:         &WorkPrompt{Kind: "approval", RequestID: "R1", AgentID: 3},
			Capabilities:   WorkCapabilities{Inspect: true, Stop: true},
			ProgressConfig: &WorkProgressConfig{Profile: "go-test", Source: "go-test-json", Title: "tests"},
			Progress: &WorkProgress{
				Version:         1,
				Sequence:        9,
				Phase:           &WorkPhase{ID: "run", Label: "Running", Index: 1, Total: 2},
				PrimaryMetricID: "tests",
				Metrics: []WorkMetric{
					{ID: "tests", Label: "tests", Unit: "tests", Current: 4, Total: &total, Rate: &rate},
				},
				Rows:          []WorkRow{{ID: "pkg", Label: "./internal/...", State: "running"}},
				Detail:        "4/10",
				ObservedAt:    9989000,
				MeasuredAt:    9988000,
				Partial:       true,
				Warning:       "late",
				ReportedError: true,
			},
		}},
		AttentionCount: 1,
	}

	cases := []struct {
		name  string
		value any
		want  string
	}{
		{
			name:  "one failed command",
			value: full,
			want:  `{"version":1,"conversationId":"s1","runtimeEpoch":"e1","revision":7,"historyRevision":3,"observedAt":10000000,"items":[{"taskKey":"work:1","displayId":"job-1","conversationId":"s1","runtimeEpoch":"e1","sourceId":"job-1","sourceRevision":4,"kind":"command","label":"go test ./...","owner":"implementer","origin":{"turnId":"t1","stepId":"2","toolCallId":"call-1"},"state":"failed","outcome":"exit","exitCode":1,"attachment":"background","startedAt":9940000,"endedAt":9990000,"terminalResultRevision":"r1","activity":"running tests","outputTail":"FAIL","warning":"slow","attention":"exit 1","reference":{"messageId":12,"nodeKey":"n1","childSessionId":"c1"},"prompt":{"kind":"approval","requestId":"R1","agentId":3},"capabilities":{"inspect":true,"stop":true,"acknowledge":false,"resolvePrompt":false},"progressConfig":{"profile":"go-test","source":"go-test-json","title":"tests"},"progress":{"version":1,"sequence":9,"phase":{"id":"run","label":"Running","index":1,"total":2},"primaryMetricId":"tests","metrics":[{"id":"tests","label":"tests","unit":"tests","current":4,"total":10,"rate":2.5}],"rows":[{"id":"pkg","label":"./internal/...","state":"running"}],"detail":"4/10","observedAt":9989000,"measuredAt":9988000,"partial":true,"warning":"late","reportedError":true}}],"activeCount":0,"attentionCount":1,"successReceiptCount":0,"stoppedReceiptCount":0}`,
		},
		{
			name:  "history query",
			value: WorkQuery{View: "history", Query: "test", Cursor: "c2", Limit: 50},
			want:  `{"view":"history","query":"test","cursor":"c2","limit":50}`,
		},
		{
			name:  "stop answer",
			value: WorkStopResult{TaskKey: "work:1", State: "stopping", Summary: "stop requested", Accepted: true},
			want:  `{"taskKey":"work:1","state":"stopping","summary":"stop requested","accepted":true}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := json.Marshal(tc.value)
			if err != nil {
				t.Fatalf("marshal %s: %v", tc.name, err)
			}
			if string(got) != tc.want {
				t.Errorf("%s JSON:\n got: %s\nwant: %s", tc.name, got, tc.want)
			}
		})
	}
}
