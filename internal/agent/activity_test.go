package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"marshal/internal/activity"
)

func TestActivityRefsBindNarrationAndCallsWithoutEnteringActionJSON(t *testing.T) {
	state := newTestState(t)
	state.AddMessage("user", "inspect the project", "plain")
	messages := state.Messages()
	run := state.BeginActivityRun(messages[len(messages)-1].ID)
	response := state.BeginActivityResponse()
	if response.RunID != run.RunID || response.ResponseID == "" {
		t.Fatalf("response = %+v, run = %+v", response, run)
	}
	owner := state.BindActivityNarration(response, "I will inspect two files.")
	if got := state.AddNarrationMessage(owner, "I will inspect two files."); got == 0 {
		t.Fatal("narration source message was not recorded")
	}
	first := state.BeginActivityCall(owner, "provider-1")
	second := state.BeginActivityCall(owner, "provider-2")
	if first.CallID == second.CallID || first.CallID == "" || first.ProviderCallID != "provider-1" || second.ProviderCallID != "provider-2" {
		t.Fatalf("call refs did not preserve unique runtime and provider identities: %+v %+v", first, second)
	}
	if first.NarrationID != owner.NarrationID || first.ResponseID != owner.ResponseID {
		t.Fatalf("call owner lost response/narration: %+v", first)
	}

	headline := "private progress"
	action := ModelAction{Activity: first, Progress: &activity.ProgressUpdate{Mode: activity.ProgressBegin, Headline: &headline}, ProgressDiagnostic: "private diagnostic", Type: ActionToolCall, Tool: "file.read"}
	encoded, err := json.Marshal(action)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "runtime") || strings.Contains(string(encoded), "narration") || strings.Contains(string(encoded), "provider-1") || strings.Contains(string(encoded), "progress") || strings.Contains(string(encoded), "diagnostic") {
		t.Fatalf("internal activity leaked into JSON action: %s", encoded)
	}
	snapshot := state.ActivitySnapshot()
	if len(snapshot.Narrations) != 1 || snapshot.Narrations[0].Source != activity.SourceModelProse || snapshot.Narrations[0].Text != "I will inspect two files." {
		t.Fatalf("snapshot narration = %+v", snapshot.Narrations)
	}
	if got, ok := snapshot.NarrationForResponse(response.ResponseID); !ok || got.ID != owner.NarrationID {
		t.Fatalf("response narration = %+v, %v", got, ok)
	}
}
