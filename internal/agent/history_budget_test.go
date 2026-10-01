package agent

import (
	"reflect"
	"strings"
	"testing"

	"marshal/internal/app/session"
	"marshal/internal/db"
)

func TestHistoryBudget_Adaptive(t *testing.T) {
	cases := []struct {
		name       string
		window     int
		configured int
		want       int
	}{
		{"128k window, no override -> 16000", 128000, 0, 16000},
		{"32k window scales to an eighth", 32000, 0, 4000},
		{"16k window scales to an eighth", 16384, 0, 2048},
		{"8k window clamps to the 1000 floor", 8000, 0, 1000},
		{"1M window -> 125000", 1_000_000, 0, 125000},
		{"explicit 9000 wins", 128000, 9000, 9000},
		{"explicit larger than cap clamps via cap not used", 128000, 50000, 50000}, // explicit wins regardless
		{"64k window -> 8000", 64000, 0, 8000},
		{"200k window -> 25000", 200000, 0, 25000},
		{"256k window -> 32000 (boundary)", 256000, 0, 32000},
		{"257k window -> 32125", 257000, 0, 32125},
		{"2M window clamps to 128000", 2_000_000, 0, 128000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := historyBudget(tc.window, tc.configured)
			if got != tc.want {
				t.Fatalf("historyBudget(window=%d, configured=%d) = %d, want %d",
					tc.window, tc.configured, got, tc.want)
			}
		})
	}
}

func TestPublicProgressDoesNotConsumeHistoryBudget(t *testing.T) {
	base := []session.Message{
		{Role: session.RoleUser, Content: "inspect the parser", ContentType: session.ContentTypePlain},
		{Role: session.RoleAssistant, Content: "The loop boundary was off by one.", ContentType: session.ContentTypeMarkdown, Final: true},
	}
	withProgress := []session.Message{
		base[0],
		{Role: session.RoleAssistant, Content: strings.Repeat("public progress detail ", 500), ContentType: session.ContentTypeNarration},
		base[1],
	}
	want := buildHistoryMessages(base, 64, session.GenerationInfo{}, map[int64][]db.ToolAuditEntry{})
	got := buildHistoryMessages(withProgress, 64, session.GenerationInfo{}, map[int64][]db.ToolAuditEntry{})
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("public progress changed budgeted history\n got: %+v\nwant: %+v", got, want)
	}
}
