package settings

import "testing"

func TestMaskKey(t *testing.T) {
	cases := map[string]string{
		"":                "(not set)",
		"abc":             "••••",
		"sk-live-1234":    "••••1234",
		"x-key-abcd-WXYZ": "••••WXYZ",
	}
	for in, want := range cases {
		if got := maskKey(in); got != want {
			t.Errorf("maskKey(%q) = %q, want %q", in, got, want)
		}
	}
}
