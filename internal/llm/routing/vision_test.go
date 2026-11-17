package routing

import (
	"fmt"
	"testing"
)

// TestResolveVisionLadder pins the whole first-match-wins order: explicit
// preset, learned rejection, probe, name heuristic, else attempt.
func TestResolveVisionLadder(t *testing.T) {
	capable := "gpt-4o-mini"
	tests := []struct {
		vision     string
		probe      *bool
		rejected   bool
		want       bool
		wantForced bool
	}{
		// vision = "on" always wins, and is forced.
		{VisionOn, nil, false, true, true},
		{VisionOn, nil, true, true, true},
		{VisionOn, boolPtr(true), false, true, true},
		{VisionOn, boolPtr(true), true, true, true},
		{VisionOn, boolPtr(false), false, true, true},
		{VisionOn, boolPtr(false), true, true, true},

		// vision = "off" always wins, and is never forced.
		{VisionOff, nil, false, false, false},
		{VisionOff, nil, true, false, false},
		{VisionOff, boolPtr(true), false, false, false},
		{VisionOff, boolPtr(true), true, false, false},
		{VisionOff, boolPtr(false), false, false, false},
		{VisionOff, boolPtr(false), true, false, false},

		// "auto" is the unset value: rejection, then probe, then heuristic.
		{VisionAuto, nil, false, true, false},
		{VisionAuto, nil, true, false, false},
		{VisionAuto, boolPtr(true), false, true, false},
		{VisionAuto, boolPtr(true), true, false, false},
		{VisionAuto, boolPtr(false), false, false, false},
		{VisionAuto, boolPtr(false), true, false, false},

		// Unset ("") behaves exactly like "auto".
		{"", nil, false, true, false},
		{"", nil, true, false, false},
		{"", boolPtr(true), false, true, false},
		{"", boolPtr(true), true, false, false},
		{"", boolPtr(false), false, false, false},
		{"", boolPtr(false), true, false, false},
	}
	for _, tc := range tests {
		probe := "nil"
		if tc.probe != nil {
			probe = fmt.Sprintf("%t", *tc.probe)
		}
		name := fmt.Sprintf("vision=%q/probe=%s/rejected=%t", tc.vision, probe, tc.rejected)
		t.Run(name, func(t *testing.T) {
			preset := ModelPreset{Name: "test/" + capable, Model: capable, Vision: tc.vision}
			vision, forced := resolveVision(preset, tc.probe, tc.rejected)
			if vision != tc.want || forced != tc.wantForced {
				t.Fatalf("resolveVision(%q, %s, %t) = (%t, %t), want (%t, %t)",
					tc.vision, probe, tc.rejected, vision, forced, tc.want, tc.wantForced)
			}
		})
	}
}

// TestResolveVisionExplicitOnIsForced asserts vision = "on" is a user
// decision the runner must not quietly undo by retrying without images.
func TestResolveVisionExplicitOnIsForced(t *testing.T) {
	preset := ModelPreset{Name: "local/whisper-large-v3", Model: "whisper-large-v3", Vision: VisionOn}
	vision, forced := resolveVision(preset, boolPtr(false), true)
	if !vision {
		t.Fatalf("vision = false, want true")
	}
	if !forced {
		t.Fatalf("forced = false, want true")
	}
}

// TestResolveVisionUnrecognisedModelAttempts asserts the default is
// attempt: a wrong "off" blocks the feature at first use.
func TestResolveVisionUnrecognisedModelAttempts(t *testing.T) {
	preset := ModelPreset{Name: "local/totally-unknown-model", Model: "totally-unknown-model"}
	vision, forced := resolveVision(preset, nil, false)
	if !vision || forced {
		t.Fatalf("resolveVision = (%t, %t), want (true, false)", vision, forced)
	}
}

func TestResolveVisionTextOnlyNameRefused(t *testing.T) {
	preset := ModelPreset{Name: "local/whisper-large-v3", Model: "whisper-large-v3"}
	vision, forced := resolveVision(preset, nil, false)
	if vision || forced {
		t.Fatalf("resolveVision = (%t, %t), want (false, false)", vision, forced)
	}
}

// TestTextOnlyModelNameIsCaseInsensitive guards the lowercasing in the
// heuristic: provider model lists are not consistent about case.
func TestTextOnlyModelNameIsCaseInsensitive(t *testing.T) {
	for _, model := range []string{"Whisper-Large-V3", "TEXT-EMBEDDING-3-LARGE", "nomic-embed-text"} {
		if !textOnlyModelName(model) {
			t.Errorf("textOnlyModelName(%q) = false, want true", model)
		}
	}
	for _, model := range []string{"gpt-4o", "qwen2.5-vl:7b", ""} {
		if textOnlyModelName(model) {
			t.Errorf("textOnlyModelName(%q) = true, want false", model)
		}
	}
}
