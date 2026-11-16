package bridge

import (
	"encoding/json"
	"reflect"
	"testing"

	"marshal/web/bridge/contract"
)

// The issues route answers with []Issue, which carries no struct tags, so its
// JSON keys are the Go field names. contract.RepoIssue transcribes them so a
// client outside this process can decode the answer; this test holds the
// transcription to what the route actually encodes, so a reshaped key fails
// here rather than in the app.
func TestListRepoIssuesAnswerIsContractRepoIssue(t *testing.T) {
	for _, tc := range []struct {
		name  string
		issue Issue
		want  contract.RepoIssue
	}{
		{
			name:  "with labels",
			issue: Issue{Number: 42, Title: "the login redirect", Body: "steps", URL: "https://example.test/issues/42", Labels: []string{"bug"}},
			want:  contract.RepoIssue{Number: 42, Title: "the login redirect", Body: "steps", URL: "https://example.test/issues/42", Labels: []string{"bug"}},
		},
		{
			name:  "without labels",
			issue: Issue{Number: 7, Title: "no labels", Body: "", URL: "u"},
			want:  contract.RepoIssue{Number: 7, Title: "no labels", URL: "u"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wire, err := json.Marshal([]Issue{tc.issue})
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			var got []contract.RepoIssue
			if err := json.Unmarshal(wire, &got); err != nil {
				t.Fatalf("decode %s into contract.RepoIssue: %v", wire, err)
			}
			if len(got) != 1 {
				t.Fatalf("got %d issues, want 1", len(got))
			}
			if !reflect.DeepEqual(got[0], tc.want) {
				t.Fatalf("got %#v, want %#v", got[0], tc.want)
			}
		})
	}
}
