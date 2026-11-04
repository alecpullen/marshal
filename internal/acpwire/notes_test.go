package acpwire

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestNotesJSON pins the bytes a note, a notice and error data carry: a
// client-typed message, the session's banner and the notice a failed run's
// error carries. Each must encode exactly as the contract spells it.
func TestNotesJSON(t *testing.T) {
	cases := []struct {
		name  string
		value any
		want  string
	}{
		{
			name:  "note",
			value: Note{Text: "Turn stopped."},
			want:  `{"text":"Turn stopped."}`,
		},
		{
			name:  "note with a content type and a role",
			value: Note{Text: "hi", ContentType: ContentTypePlain, Role: RoleUser},
			want:  `{"text":"hi","contentType":"plain","role":"user"}`,
		},
		{
			name: "notice with a hint and a source",
			value: Notice{
				Category: NoticeProvider,
				Severity: NoticeError,
				Message:  `provider "openai": 500`,
				Hint:     "Run /connect to review the provider, or /models to pick another model.",
				Source:   "turn",
				SetAt:    1700000000000,
			},
			want: `{"category":"provider","severity":"error","message":"provider \"openai\": 500","hint":"Run /connect to review the provider, or /models to pick another model.","source":"turn","setAt":1700000000000}`,
		},
		{
			name:  "notice with no hint or source",
			value: Notice{Category: NoticeInternal, Severity: NoticeWarn, Message: "slow"},
			want:  `{"category":"internal","severity":"warn","message":"slow"}`,
		},
		{
			name: "error data carrying a notice",
			value: ErrorData{Notice: &Notice{
				Category: NoticeProvider,
				Severity: NoticeError,
				Message:  "no stored tokens",
				Hint:     "Run /connect to sign in to codex.",
			}},
			want: `{"notice":{"category":"provider","severity":"error","message":"no stored tokens","hint":"Run /connect to sign in to codex."}}`,
		},
		{
			name:  "error data with no notice",
			value: ErrorData{},
			want:  `{}`,
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

// TestSessionStatusNotice pins the banner's place in the session status: a
// status with no notice carries no notice key at all, and one that has a
// notice carries it whole.
func TestSessionStatusNotice(t *testing.T) {
	none, err := json.Marshal(SessionStatus{})
	if err != nil {
		t.Fatalf("marshal status with no notice: %v", err)
	}
	if strings.Contains(string(none), `"notice"`) {
		t.Errorf("status with no notice carries a notice key: %s", none)
	}

	withNotice, err := json.Marshal(SessionStatus{
		Notice: &Notice{Category: NoticeConfig, Severity: NoticeWarn, Message: "m"},
	})
	if err != nil {
		t.Fatalf("marshal status with a notice: %v", err)
	}
	want := `"notice":{"category":"config","severity":"warn","message":"m"}`
	if !strings.Contains(string(withNotice), want) {
		t.Errorf("status with a notice:\n got: %s\nwant it to contain: %s", withNotice, want)
	}
}
