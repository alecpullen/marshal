package db

import "testing"

func TestEscapeLike(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"", ""},
		{"hello", "hello"},
		{"foo_bar", `foo\_bar`},
		{"100%", `100\%`},
		{`a\b`, `a\\b`},
		{`test\_file`, `test\\\_file`},
		{"a%b_c", `a\%b\_c`},
	}
	for _, tc := range tests {
		got := escapeLike(tc.input)
		if got != tc.want {
			t.Errorf("escapeLike(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}
