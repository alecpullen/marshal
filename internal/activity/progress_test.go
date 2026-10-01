package activity

import (
	"strings"
	"testing"
)

func TestDecodeProgressOmissionAndExplicitClearing(t *testing.T) {
	got, err := DecodeProgress([]byte(`{"mode":"revise","headline":"Update","body":"","sections":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	if got.CurrentAction != nil || got.Body == nil || *got.Body != "" || got.Sections == nil || len(*got.Sections) != 0 {
		t.Fatalf("omission/clearing lost: %#v", got)
	}
}

func TestDecodeProgressRejectsInvalidShapesAndBounds(t *testing.T) {
	long := strings.Repeat("x", MaxHeadlineRunes+1)
	tests := []struct{ name, raw string }{
		{"null mode", `{"mode":null,"headline":"x"}`},
		{"unknown field", `{"mode":"begin","headline":"x","success":true}`},
		{"duplicate field", `{"mode":"begin","headline":"x","headline":"y"}`},
		{"invalid mode", `{"mode":"finish","headline":"x"}`},
		{"blank begin", `{"mode":"begin","headline":"  "}`},
		{"headline limit", `{"mode":"begin","headline":"` + long + `"}`},
		{"invalid kind", `{"mode":"begin","headline":"x","sections":[{"kind":"result","text":"x"}]}`},
		{"duplicate section", `{"mode":"begin","headline":"x","sections":[{"kind":"change","text":"a"},{"kind":"change","text":"b"}]}`},
		{"control", "{\"mode\":\"begin\",\"headline\":\"x\\u0001\"}"},
		{"null optional", `{"mode":"begin","headline":"x","body":null}`},
		{"malformed", `{broken`},
		{"oversized", `{"mode":"begin","headline":"` + strings.Repeat("x", MaxProgressBytes) + `"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := DecodeProgress([]byte(tt.raw)); err == nil {
				t.Fatal("DecodeProgress accepted invalid payload")
			}
		})
	}
}

func TestDecodeProgressEnforcesEveryContentLimit(t *testing.T) {
	section := func(text string) string {
		return `{"mode":"begin","headline":"h","sections":[{"kind":"change","text":"` + text + `"}]}`
	}
	refs := make([]string, MaxSectionRefs+1)
	for i := range refs {
		refs[i] = `"e"`
	}
	refsJSON := strings.Join(refs, ",")
	tests := []string{
		`{"mode":"begin","headline":"` + strings.Repeat("h", MaxHeadlineRunes+1) + `"}`,
		`{"mode":"revise","body":"` + strings.Repeat("b", MaxBodyRunes+1) + `"}`,
		`{"mode":"revise","current_action":"` + strings.Repeat("a", MaxActionRunes+1) + `"}`,
		section(strings.Repeat("s", MaxSectionRunes+1)),
		`{"mode":"begin","headline":"h","sections":[{"kind":"change","text":"x"},{"kind":"evidence","text":"x"},{"kind":"checking","text":"x"},{"kind":"next","text":"x"},{"kind":"work","text":"x"},{"kind":"change","text":"x"}]}`,
		`{"mode":"begin","headline":"h","sections":[{"kind":"evidence","text":"x","evidence_refs":[` + refsJSON + `]}]}`,
	}
	for i, raw := range tests {
		if _, err := DecodeProgress([]byte(raw)); err == nil {
			t.Errorf("case %d exceeded a limit but decoded", i)
		}
	}
}
