package viewmodel

import (
	"encoding/json"
	"testing"
	"time"

	"marshal/internal/progress"
)

// workWireSnapshot is the source-side snapshot the wire literal below was
// written against.
func workWireSnapshot() WorkSnapshot {
	exitCode := 1
	total := 10.0
	rate := 2.5
	return WorkSnapshot{
		Version:         1,
		ConversationID:  "s1",
		RuntimeEpoch:    "e1",
		Revision:        7,
		HistoryRevision: 3,
		ObservedAt:      time.UnixMilli(10000000),
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
			StartedAt:      time.UnixMilli(9940000),
			EndedAt:        time.UnixMilli(9990000),
			ResultRevision: "r1",
			Activity:       "running tests",
			OutputTail:     "FAIL",
			Warning:        "slow",
			Attention:      "exit 1",
			Reference:      WorkReference{MessageID: 12, NodeKey: "n1", ChildSessionID: "c1"},
			Prompt:         &WorkPrompt{Kind: "approval", RequestID: "R1", AgentID: 3},
			Capabilities:   WorkCapabilities{Inspect: true, Stop: true},
			ProgressConfig: &progress.Config{Profile: "go-test", Source: "go-test-json", Title: "tests"},
			Progress: &progress.Snapshot{
				Frame: progress.Frame{
					Version:         1,
					Sequence:        9,
					Phase:           &progress.Phase{ID: "run", Label: "Running", Index: 1, Total: 2},
					PrimaryMetricID: "tests",
					Metrics: []progress.Metric{
						{ID: "tests", Label: "tests", Unit: "tests", Current: 4, Total: &total, Rate: &rate},
					},
					Rows:   []progress.Row{{ID: "pkg", Label: "./internal/...", State: "running"}},
					Detail: "4/10",
				},
				ObservedAt:    time.UnixMilli(9989000),
				MeasuredAt:    time.UnixMilli(9988000),
				Partial:       true,
				Warning:       "late",
				ReportedError: true,
			},
		}},
		AttentionCount: 1,
	}
}

// workWireJSON is the contract literal workWireSnapshot must encode as.
const workWireJSON = `{"version":1,"conversationId":"s1","runtimeEpoch":"e1","revision":7,"historyRevision":3,"observedAt":10000000,"items":[{"taskKey":"work:1","displayId":"job-1","conversationId":"s1","runtimeEpoch":"e1","sourceId":"job-1","sourceRevision":4,"kind":"command","label":"go test ./...","owner":"implementer","origin":{"turnId":"t1","stepId":"2","toolCallId":"call-1"},"state":"failed","outcome":"exit","exitCode":1,"attachment":"background","startedAt":9940000,"endedAt":9990000,"terminalResultRevision":"r1","activity":"running tests","outputTail":"FAIL","warning":"slow","attention":"exit 1","reference":{"messageId":12,"nodeKey":"n1","childSessionId":"c1"},"prompt":{"kind":"approval","requestId":"R1","agentId":3},"capabilities":{"inspect":true,"stop":true,"acknowledge":false,"resolvePrompt":false},"progressConfig":{"profile":"go-test","source":"go-test-json","title":"tests"},"progress":{"version":1,"sequence":9,"phase":{"id":"run","label":"Running","index":1,"total":2},"primaryMetricId":"tests","metrics":[{"id":"tests","label":"tests","unit":"tests","current":4,"total":10,"rate":2.5}],"rows":[{"id":"pkg","label":"./internal/...","state":"running"}],"detail":"4/10","observedAt":9989000,"measuredAt":9988000,"partial":true,"warning":"late","reportedError":true}}],"activeCount":0,"attentionCount":1,"successReceiptCount":0,"stoppedReceiptCount":0}`

// TestWorkSnapshotOfMatchesTheWire pins the projection the client reads: the
// source snapshot, its times made with time.UnixMilli, encodes exactly as the
// contract spells the wire snapshot.
func TestWorkSnapshotOfMatchesTheWire(t *testing.T) {
	got, err := json.Marshal(WorkSnapshotOf(workWireSnapshot()))
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	if string(got) != workWireJSON {
		t.Errorf("snapshot JSON:\n got: %s\nwant: %s", got, workWireJSON)
	}
}

// TestWorkConversionKeepsNilsAndZeroTimes pins the empty case: an item with no
// times, exit code, prompt, config or progress converts to zero times and nil
// pointers, and a snapshot with no items keeps them nil.
func TestWorkConversionKeepsNilsAndZeroTimes(t *testing.T) {
	it := WorkItemOf(WorkItem{TaskKey: "work:1", State: "running"})
	if it.StartedAt != 0 || it.EndedAt != 0 {
		t.Errorf("times: got started %d ended %d, want 0 and 0", it.StartedAt, it.EndedAt)
	}
	if it.ExitCode != nil {
		t.Errorf("exit code: got %v, want nil", *it.ExitCode)
	}
	if it.Prompt != nil || it.ProgressConfig != nil || it.Progress != nil {
		t.Errorf("optional parts: got prompt %v config %v progress %v, want all nil",
			it.Prompt, it.ProgressConfig, it.Progress)
	}

	snap := WorkSnapshotOf(WorkSnapshot{ConversationID: "s1"})
	if snap.Items != nil {
		t.Errorf("items: got %v, want nil", snap.Items)
	}
	if snap.ObservedAt != 0 {
		t.Errorf("observed at: got %d, want 0", snap.ObservedAt)
	}
}

// TestWorkConversionCopiesPointers pins that the wire value owns its own data:
// changing the converted item leaves the source untouched.
func TestWorkConversionCopiesPointers(t *testing.T) {
	exitCode := 1
	total := 10.0
	src := WorkItem{
		ExitCode: &exitCode,
		Progress: &progress.Snapshot{
			Frame: progress.Frame{
				Phase:   &progress.Phase{ID: "run", Label: "Running"},
				Metrics: []progress.Metric{{ID: "tests", Current: 4, Total: &total}},
			},
		},
	}

	got := WorkItemOf(src)
	*got.ExitCode = 2
	*got.Progress.Metrics[0].Total = 99
	got.Progress.Phase.Label = "Done"

	if *src.ExitCode != 1 {
		t.Errorf("source exit code: got %d, want 1", *src.ExitCode)
	}
	if *src.Progress.Metrics[0].Total != 10 {
		t.Errorf("source metric total: got %v, want 10", *src.Progress.Metrics[0].Total)
	}
	if src.Progress.Phase.Label != "Running" {
		t.Errorf("source phase label: got %q, want %q", src.Progress.Phase.Label, "Running")
	}
}

// TestWorkPageDetailAndStopConvert pins the three smaller conversions: every
// field of a page, a detail and a stop result reaches the wire.
func TestWorkPageDetailAndStopConvert(t *testing.T) {
	page := WorkPageOf(WorkPage{
		RuntimeEpoch: "e1",
		Revision:     7,
		Items:        []WorkItem{{TaskKey: "work:1", State: "failed"}},
		NextCursor:   "c2",
	})
	if page.RuntimeEpoch != "e1" || page.Revision != 7 || page.NextCursor != "c2" {
		t.Errorf("page header: got epoch %q revision %d cursor %q, want e1, 7, c2",
			page.RuntimeEpoch, page.Revision, page.NextCursor)
	}
	if len(page.Items) != 1 || page.Items[0].TaskKey != "work:1" || page.Items[0].State != "failed" {
		t.Errorf("page items: got %+v, want the one failed item", page.Items)
	}

	detail := WorkDetailOf(WorkDetail{
		Item:        WorkItem{TaskKey: "work:1"},
		Output:      "FAIL",
		Raw:         true,
		Report:      "the tests failed",
		Unavailable: "no output kept",
	})
	if detail.Item.TaskKey != "work:1" || detail.Output != "FAIL" || !detail.Raw ||
		detail.Report != "the tests failed" || detail.Unavailable != "no output kept" {
		t.Errorf("detail: got %+v, want every field carried", detail)
	}

	stop := WorkStopResultOf(WorkStopResult{
		TaskKey:  "work:1",
		State:    "stopping",
		Summary:  "stop requested",
		Accepted: true,
	})
	if stop.TaskKey != "work:1" || stop.State != "stopping" || stop.Summary != "stop requested" || !stop.Accepted {
		t.Errorf("stop result: got %+v, want every field carried", stop)
	}
}
