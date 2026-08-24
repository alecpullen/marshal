package agent

import "testing"

func TestLastCompleteLine(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"no newline", ""},
		{"one line\n", "one line"},
		{"one line\npartial", "one line"},
		{"first\nsecond\npartial", "second"},
		{"first\n\nsecond\n", "second"},
		{"first\n   \npartial", "first"},
		{"  padded  \n", "padded"},
	}
	for _, tc := range cases {
		if got := lastCompleteLine(tc.in); got != tc.want {
			t.Errorf("lastCompleteLine(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
