package acpwire

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// TestPromptCallParamsCarryTheTextAsOneBlock pins the client-to-server prompt:
// PromptParams.Text travels as one text block, queued rides alongside it, and
// an optional watch resume keeps its name and sequence.
func TestPromptCallParamsCarryTheTextAsOneBlock(t *testing.T) {
	params := PromptCallParams{
		SessionID: "s1",
		Prompt:    []TextBlock{{Type: "text", Text: "hi"}},
		Queued:    true,
	}

	got, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal prompt: %v", err)
	}
	for _, want := range []string{`"prompt":[{"type":"text","text":"hi"}]`, `"queued":true`} {
		if !strings.Contains(string(got), want) {
			t.Errorf("prompt JSON %s does not carry %s", got, want)
		}
	}

	var back PromptCallParams
	if err := json.Unmarshal(got, &back); err != nil {
		t.Fatalf("unmarshal prompt: %v", err)
	}
	if !reflect.DeepEqual(back, params) {
		t.Errorf("prompt round trip:\n got: %+v\nwant: %+v", back, params)
	}

	params.WatchResume = &WatchResume{Name: "build", Seq: 7}
	got, err = json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal prompt with a watch resume: %v", err)
	}
	if want := `"watchResume":{"name":"build","seq":7}`; !strings.Contains(string(got), want) {
		t.Errorf("prompt JSON %s does not carry %s", got, want)
	}
}

// TestWorkUpdateRoundTrips pins the work_snapshot update: the whole work state
// with a one-item snapshot survives a round trip, and its kind is
// work_snapshot.
func TestWorkUpdateRoundTrips(t *testing.T) {
	update := WorkUpdate{
		Kind: UpdateWork,
		Work: WorkState{
			Exists: true,
			Snapshot: WorkSnapshot{
				Version:         1,
				ConversationID:  "s1",
				RuntimeEpoch:    "e1",
				Revision:        7,
				HistoryRevision: 3,
				Items: []WorkItem{{
					TaskKey:        "work:1",
					DisplayID:      "job-1",
					ConversationID: "s1",
					RuntimeEpoch:   "e1",
					SourceID:       "job-1",
					SourceRevision: 4,
					Kind:           "command",
					Label:          "go test ./...",
					Origin:         WorkOrigin{TurnID: "t1", StepID: "2", ToolCallID: "call-1"},
					State:          "running",
					Activity:       "running tests",
					Reference:      WorkReference{MessageID: 12},
					Capabilities:   WorkCapabilities{Inspect: true, Stop: true},
				}},
				ActiveCount: 1,
			},
		},
	}

	got, err := json.Marshal(update)
	if err != nil {
		t.Fatalf("marshal work update: %v", err)
	}

	var back WorkUpdate
	if err := json.Unmarshal(got, &back); err != nil {
		t.Fatalf("unmarshal work update: %v", err)
	}
	if !reflect.DeepEqual(back, update) {
		t.Errorf("work update round trip:\n got: %+v\nwant: %+v", back, update)
	}
	if back.Kind != "work_snapshot" {
		t.Errorf("work update kind: got %q, want %q", back.Kind, "work_snapshot")
	}
}

// TestAnEmptyWorkStateSaysItServesNoWork pins the negative case: a session
// that holds no work state encodes exists false rather than omitting the key.
func TestAnEmptyWorkStateSaysItServesNoWork(t *testing.T) {
	got, err := json.Marshal(WorkState{})
	if err != nil {
		t.Fatalf("marshal empty work state: %v", err)
	}
	if want := `"exists":false`; !strings.Contains(string(got), want) {
		t.Errorf("empty work state JSON %s does not carry %s", got, want)
	}
}

// TestTheControlResultsRoundTrip pins the four control results: each survives
// a round trip, and a zero steering pop answer is a plain refusal.
func TestTheControlResultsRoundTrip(t *testing.T) {
	cases := []struct {
		name  string
		value any
	}{
		{"steering pop", SteeringPopResult{Text: "stop and test", OK: true}},
		{"effort", EffortResult{Effort: "high", AppliesFrom: "next turn"}},
		{"subagent cancel", SubagentCancelResult{Cancelled: true}},
		{"watch resume take", WatchResumeTakeResult{Name: "build", Repeat: true, OK: true}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := json.Marshal(tc.value)
			if err != nil {
				t.Fatalf("marshal %s: %v", tc.name, err)
			}
			back := reflect.New(reflect.TypeOf(tc.value))
			if err := json.Unmarshal(got, back.Interface()); err != nil {
				t.Fatalf("unmarshal %s: %v", tc.name, err)
			}
			if !reflect.DeepEqual(back.Elem().Interface(), tc.value) {
				t.Errorf("%s round trip:\n got: %+v\nwant: %+v", tc.name, back.Elem().Interface(), tc.value)
			}
		})
	}

	got, err := json.Marshal(SteeringPopResult{})
	if err != nil {
		t.Fatalf("marshal zero steering pop: %v", err)
	}
	if want := `{"ok":false}`; string(got) != want {
		t.Errorf("zero steering pop JSON:\n got: %s\nwant: %s", got, want)
	}
}
