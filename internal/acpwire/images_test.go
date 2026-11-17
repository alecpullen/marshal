package acpwire

import (
	"encoding/json"
	"sort"
	"testing"
)

// keysOf reports the JSON object's top-level keys, sorted.
func keysOf(t *testing.T, raw []byte) []string {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal %s: %v", raw, err)
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func equalKeys(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// TestImagesPutParamsRoundTrip pins the wire keys: a client's upload is
// sessionId, data and label, and its answer is one image.
func TestImagesPutParamsRoundTrip(t *testing.T) {
	raw, err := json.Marshal(ImagesPutParams{SessionID: "s1", Data: "AAEC", Label: "shot"})
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	if got, want := keysOf(t, raw), []string{"data", "label", "sessionId"}; !equalKeys(got, want) {
		t.Fatalf("ImagesPutParams keys = %v, want %v", got, want)
	}

	ref := PromptImage{Hash: "abc", MIME: "image/png", Width: 1, Height: 1, Bytes: 10, Label: "shot"}
	raw, err = json.Marshal(ImagesPutResult{Image: ref})
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	if got, want := keysOf(t, raw), []string{"image"}; !equalKeys(got, want) {
		t.Fatalf("ImagesPutResult keys = %v, want %v", got, want)
	}

	var envelope struct {
		Image map[string]json.RawMessage `json:"image"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	imageKeys := make([]string, 0, len(envelope.Image))
	for k := range envelope.Image {
		imageKeys = append(imageKeys, k)
	}
	sort.Strings(imageKeys)
	if want := []string{"bytes", "hash", "height", "label", "mime", "width"}; !equalKeys(imageKeys, want) {
		t.Fatalf("PromptImage keys = %v, want %v", imageKeys, want)
	}

	var back ImagesPutResult
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal back: %v", err)
	}
	if back.Image != ref {
		t.Fatalf("round trip = %+v, want %+v", back.Image, ref)
	}
}

// TestCodeVisionUnsupported pins the refusal's code: acpwire's codes are
// strings, and a client branches on this one.
func TestCodeVisionUnsupported(t *testing.T) {
	if got, want := string(CodeVisionUnsupported), "vision_unsupported"; got != want {
		t.Fatalf("CodeVisionUnsupported = %q, want %q", got, want)
	}
}
